// -------------------------------------------------------------------------------
// Log Streaming Tests
//
// Author: Alex Freidah
//
// Following a run's output: waiting for its container, resuming from an
// offset, and how each way a stream can stop is reported. Reading it whole
// once the run has ended, and when that read is incomplete.
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
// READING
// -------------------------------------------------------------------------

// A finished run's log is read whole, unfollowed and unbounded, from the
// task's container.
func TestReadLogs_ReadsTheWholeLog(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"
	log := strings.Repeat("x", 5*maxResponse/4)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, log)
	})

	var out bytes.Buffer
	if err := p.ReadLogs(t.Context(), id, &out); err != nil {
		t.Fatalf("ReadLogs() = %v", err)
	}

	if out.Len() != len(log) {
		t.Errorf("read %d bytes, want all %d", out.Len(), len(log))
	}

	got := f.requestsTo("GET " + podsPath() + "/" + podName + "/log")[0]
	if got.query.Has("follow") || got.query.Has("limitBytes") || got.query.Get("container") != userContainer {
		t.Errorf("log query = %v", got.query)
	}

	if !strings.HasPrefix(got.header.Get("Accept"), "text/plain") {
		t.Errorf("Accept = %q", got.header.Get("Accept"))
	}
}

// A pod already gone took its log with it, which is output lost.
func TestReadLogs_PodGone(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList()))

	if err := p.ReadLogs(t.Context(), newID(t), io.Discard); !plugin.Reroutable(err) {
		t.Fatalf("ReadLogs() = %v, want an infrastructure failure", err)
	}
}

// A container that never started printed nothing, so there is no log to ask
// for and nothing missing.
func TestReadLogs_NeverStarted(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(waitingPod(podName))))

	if err := p.ReadLogs(t.Context(), id, io.Discard); err != nil {
		t.Fatalf("ReadLogs() = %v", err)
	}

	if n := len(f.requestsTo("GET " + podsPath() + "/" + podName + "/log")); n != 0 {
		t.Errorf("asked for the log %d times, want none", n)
	}
}

// A log cut off partway is reported as incomplete, after what arrived.
func TestReadLogs_CutConnection(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log", func(w http.ResponseWriter, _ *http.Request) {
		// Promise more than is sent, so the client sees the body end early
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
	})

	var out bytes.Buffer

	if err := p.ReadLogs(t.Context(), id, &out); !plugin.Reroutable(err) {
		t.Fatalf("ReadLogs() = %v, want an infrastructure failure", err)
	}

	if out.String() != "partial" {
		t.Errorf("read %q, want what arrived", out.String())
	}
}

// A log the platform refuses to open is classified, and a pod lookup that
// fails is the failure.
func TestReadLogs_Refusals(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	podName := runName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log",
		reply(http.StatusForbidden, map[string]any{"kind": "Status", "message": "forbidden", "reason": "Forbidden", "code": 403}))

	if err := p.ReadLogs(t.Context(), id, io.Discard); !errors.Is(err, plugin.ErrUnauthorized) {
		t.Errorf("ReadLogs() = %v, want unauthorized", err)
	}

	g, q := newFakeIBM(t)
	g.on("GET "+podsPath(), reply(http.StatusServiceUnavailable, map[string]any{"message": "unavailable"}))

	if err := q.ReadLogs(t.Context(), id, io.Discard); !plugin.Reroutable(err) {
		t.Errorf("ReadLogs() = %v, want an infrastructure failure", err)
	}
}
