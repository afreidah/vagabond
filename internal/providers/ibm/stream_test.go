// -------------------------------------------------------------------------------
// Log Streaming Tests
//
// Author: Alex Freidah
//
// Following a run's output: waiting for its container, resuming from an
// offset, and how each way a stream can stop is reported.
// -------------------------------------------------------------------------------

package ibm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// waitingPod is a pod whose task container is still being created.
func waitingPod(name string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name, "creationTimestamp": time.Now().Format(time.RFC3339)},
		"status": map[string]any{
			"phase": "Pending",
			"containerStatuses": []map[string]any{{
				"name":  userContainer,
				"state": map[string]any{"waiting": map[string]any{"reason": "ContainerCreating"}},
			}},
		},
	}
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

// Write calls the function.
func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// failingReader returns its data, then an error that is not EOF.
type failingReader struct{ data *strings.Reader }

// Read serves the data, then fails.
func (r failingReader) Read(p []byte) (int, error) {
	if r.data.Len() == 0 {
		return 0, errors.New("connection reset")
	}

	return r.data.Read(p)
}

// -------------------------------------------------------------------------
// STREAMING
// -------------------------------------------------------------------------

// A follower waits through no pod and a container being created, then streams
// the container's log from its first line.
func TestStreamLogs_WaitsThenStreams(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"

	f.on("GET "+podsPath(),
		reply(http.StatusOK, podList()),
		reply(http.StatusOK, podList(waitingPod(podName))),
		reply(http.StatusOK, podList(podBody(podName, time.Now(), nil))))
	f.on("GET "+podsPath()+"/"+podName+"/log", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "out\nerr\n")
	})

	var out bytes.Buffer
	if err := p.StreamLogs(t.Context(), id, 0, &out); err != nil {
		t.Fatalf("StreamLogs() = %v", err)
	}

	if out.String() != "out\nerr\n" {
		t.Errorf("streamed %q", out.String())
	}

	if n := len(f.requestsTo("GET " + podsPath())); n != 3 {
		t.Errorf("%d pod lookups, want 3", n)
	}

	query := f.requestsTo("GET " + podsPath() + "/" + podName + "/log")[0].query
	if query.Get("follow") != "true" || query.Get("container") != userContainer {
		t.Errorf("log query = %v", query)
	}
}

// A follower reconnecting at an offset gets only what it does not have.
func TestStreamLogs_ResumesFromAnOffset(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), nil))))
	f.on("GET "+podsPath()+"/"+podName+"/log", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "out\nerr\n")
	})

	var out bytes.Buffer
	if err := p.StreamLogs(t.Context(), id, 4, &out); err != nil {
		t.Fatalf("StreamLogs() = %v", err)
	}

	if out.String() != "err\n" {
		t.Errorf("streamed %q, want err", out.String())
	}
}

// A stream the platform refuses to open is classified.
func TestStreamLogs_ClassifiesARefusal(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), nil))))
	f.on("GET "+podsPath()+"/"+podName+"/log",
		reply(http.StatusForbidden, map[string]any{"kind": "Status", "message": "forbidden", "reason": "Forbidden", "code": 403}))

	err := p.StreamLogs(t.Context(), id, 0, io.Discard)
	if !errors.Is(err, plugin.ErrUnauthorized) {
		t.Fatalf("StreamLogs() = %v, want unauthorized", err)
	}
}

// A pod lookup that fails ends the stream with the failure.
func TestStreamLogs_PodLookupFails(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+podsPath(), reply(http.StatusServiceUnavailable, map[string]any{"message": "unavailable"}))

	if err := p.StreamLogs(t.Context(), newID(t), 0, io.Discard); !plugin.Reroutable(err) {
		t.Fatalf("StreamLogs() = %v, want an infrastructure failure", err)
	}
}

// A container that never starts is waited on until the caller gives up, which
// is a normal end.
func TestStreamLogs_CancelWhileWaiting(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList()))

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if err := p.StreamLogs(ctx, newID(t), 0, io.Discard); err != nil {
		t.Fatalf("StreamLogs() = %v, want a quiet end", err)
	}
}

// -------------------------------------------------------------------------
// COPYING
// -------------------------------------------------------------------------

// An offset past the end of the output writes nothing and ends quietly.
func TestCopyFollow_OffsetPastTheEnd(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := copyFollow(strings.NewReader("short"), &out, 100); err != nil || out.Len() != 0 {
		t.Fatalf("copyFollow() = %v, wrote %q", err, out.String())
	}
}

// A read that fails mid-stream ends the copy quietly, keeping what arrived.
func TestCopyFollow_ReadFailureIsQuiet(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := copyFollow(failingReader{strings.NewReader("partial")}, &out, 0); err != nil {
		t.Fatalf("copyFollow() = %v, want a quiet end", err)
	}

	if out.String() != "partial" {
		t.Errorf("wrote %q", out.String())
	}
}

// A write that fails is reported, so the caller knows its client is gone.
func TestCopyFollow_WriteFailureIsInternal(t *testing.T) {
	t.Parallel()

	gone := writerFunc(func([]byte) (int, error) { return 0, errors.New("client gone") })

	err := copyFollow(strings.NewReader("data"), gone, 0)
	if err == nil || plugin.Reroutable(err) {
		t.Fatalf("copyFollow() = %v, want an internal failure", err)
	}
}
