// -------------------------------------------------------------------------------
// Container Log Tests
//
// Author: Alex Freidah
//
// Capturing a run's output: waiting for its container, following its log to
// the container's exit, telling a cut follow from that exit, and reading the
// log whole once the run has ended, from what the execution's log already
// holds on, and when that read is incomplete.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/testkit"
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

// ended is an ended that has closed, for a capture of a finished run.
func ended() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)

	return ch
}

// serving answers a log request with text.
func serving(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, text)
	}
}

// -------------------------------------------------------------------------
// FOLLOWING
// -------------------------------------------------------------------------

// A capture waits through no pod and a container being created, follows the
// container's log from its first line, and once it has seen the container
// exit and the run end, needs nothing more.
func TestCaptureLogs_WaitsThenFollows(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(),
		reply(http.StatusOK, podList()),
		reply(http.StatusOK, podList(waitingPod(podName))),
		reply(http.StatusOK, podList(podBody(podName, time.Now(), nil))),
		reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log", serving("out\nerr\n"))

	logs, end := &plugin.MemoryLog{}, make(chan struct{})
	done := make(chan error, 1)

	go func() { done <- p.CaptureLogs(t.Context(), id, logs, end) }()

	for deadline := time.Now().Add(5 * time.Second); logs.String() != "out\nerr\n"; {
		if time.Now().After(deadline) {
			t.Fatalf("captured %q, want the log", logs.String())
		}

		time.Sleep(time.Millisecond)
	}

	close(end)

	if err := <-done; err != nil {
		t.Fatalf("CaptureLogs() = %v", err)
	}

	reads := f.requestsTo("GET " + podsPath() + "/" + podName + "/log")
	if len(reads) != 1 || reads[0].query.Get("follow") != "true" || reads[0].query.Get("container") != userContainer {
		t.Errorf("log requests = %v, want one follow of the task's container", reads)
	}

	if n := len(f.requestsTo("GET " + podsPath())); n != 4 {
		t.Errorf("%d pod lookups, want 4", n)
	}
}

// A follow that ends while the container still runs was cut, which is
// reported for the capture to be made again, after what arrived.
func TestCaptureLogs_ReportsACutFollow(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), nil))))
	f.on("GET "+podsPath()+"/"+podName+"/log", serving("out\n"))

	logs := &plugin.MemoryLog{}

	err := p.CaptureLogs(t.Context(), id, logs, make(chan struct{}))
	if !plugin.Reroutable(err) || !strings.Contains(err.Error(), "while its container runs") {
		t.Errorf("CaptureLogs() = %v, want an infrastructure failure naming the cut", err)
	}

	if logs.String() != "out\n" {
		t.Errorf("captured %q, want what arrived", logs.String())
	}
}

// A capture made again follows from what the log holds.
func TestCaptureLogs_FollowsFromTheLog(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log", serving("out\nerr\n"))

	logs := &plugin.MemoryLog{}
	_, _ = logs.Write([]byte("out\n"))

	end := make(chan struct{})
	time.AfterFunc(10*time.Millisecond, func() { close(end) })

	if err := p.CaptureLogs(t.Context(), id, logs, end); err != nil || logs.String() != "out\nerr\n" {
		t.Errorf("CaptureLogs() = %q, %v; want the rest after what the log held", logs.String(), err)
	}
}

// A follow the platform refuses is classified, and a pod lookup that fails is
// the failure.
func TestCaptureLogs_FollowRefusals(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), nil))))
	f.on("GET "+podsPath()+"/"+podName+"/log",
		reply(http.StatusForbidden, map[string]any{"kind": "Status", "message": "forbidden", "reason": "Forbidden", "code": 403}))

	if err := p.CaptureLogs(t.Context(), id, &plugin.MemoryLog{}, make(chan struct{})); !errors.Is(err, plugin.ErrUnauthorized) {
		t.Errorf("CaptureLogs() = %v, want unauthorized", err)
	}

	g, q := newFakeIBM(t)
	g.on("GET "+podsPath(), reply(http.StatusServiceUnavailable, map[string]any{"message": "unavailable"}))

	if err := q.CaptureLogs(t.Context(), id, &plugin.MemoryLog{}, make(chan struct{})); !plugin.Reroutable(err) {
		t.Errorf("CaptureLogs() = %v, want an infrastructure failure", err)
	}
}

// A container that never starts is waited on until the capture is given up.
func TestCaptureLogs_CancelWhileWaiting(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList()))

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if err := p.CaptureLogs(ctx, testkit.NewID(t), &plugin.MemoryLog{}, make(chan struct{})); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CaptureLogs() = %v, want the context's end", err)
	}
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// A finished run's log is read whole, unfollowed and unbounded, from the
// task's container.
func TestCaptureLogs_ReadsAFinishedRunWhole(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"
	text := strings.Repeat("x", 5*maxResponse/4)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log", serving(text))

	logs := &plugin.MemoryLog{}
	if err := p.CaptureLogs(t.Context(), id, logs, ended()); err != nil {
		t.Fatalf("CaptureLogs() = %v", err)
	}

	if len(logs.String()) != len(text) {
		t.Errorf("read %d bytes, want all %d", len(logs.String()), len(text))
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
func TestCaptureLogs_PodGone(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList()))

	if err := p.CaptureLogs(t.Context(), testkit.NewID(t), &plugin.MemoryLog{}, ended()); !plugin.Reroutable(err) {
		t.Fatalf("CaptureLogs() = %v, want an infrastructure failure", err)
	}
}

// A container that never started printed nothing, so there is no log to ask
// for and nothing missing.
func TestCaptureLogs_NeverStarted(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(waitingPod(podName))))

	if err := p.CaptureLogs(t.Context(), id, &plugin.MemoryLog{}, ended()); err != nil {
		t.Fatalf("CaptureLogs() = %v", err)
	}

	if n := len(f.requestsTo("GET " + podsPath() + "/" + podName + "/log")); n != 0 {
		t.Errorf("asked for the log %d times, want none", n)
	}
}

// A log cut off partway is reported as incomplete, after what arrived.
func TestCaptureLogs_CutRead(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log", func(w http.ResponseWriter, _ *http.Request) {
		// Promise more than is sent, so the client sees the body end early
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
	})

	logs := &plugin.MemoryLog{}

	if err := p.CaptureLogs(t.Context(), id, logs, ended()); !plugin.Reroutable(err) {
		t.Fatalf("CaptureLogs() = %v, want an infrastructure failure", err)
	}

	if logs.String() != "partial" {
		t.Errorf("read %q, want what arrived", logs.String())
	}
}

// A whole read the platform refuses is classified, and a pod lookup that
// fails is the failure.
func TestCaptureLogs_ReadRefusals(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := testkit.NewID(t)
	podName := plugin.ResourceName(id) + "-0-0"

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(0)))))
	f.on("GET "+podsPath()+"/"+podName+"/log",
		reply(http.StatusForbidden, map[string]any{"kind": "Status", "message": "forbidden", "reason": "Forbidden", "code": 403}))

	if err := p.CaptureLogs(t.Context(), id, &plugin.MemoryLog{}, ended()); !errors.Is(err, plugin.ErrUnauthorized) {
		t.Errorf("CaptureLogs() = %v, want unauthorized", err)
	}

	g, q := newFakeIBM(t)
	g.on("GET "+podsPath(), reply(http.StatusServiceUnavailable, map[string]any{"message": "unavailable"}))

	if err := q.CaptureLogs(t.Context(), id, &plugin.MemoryLog{}, ended()); !plugin.Reroutable(err) {
		t.Errorf("CaptureLogs() = %v, want an infrastructure failure", err)
	}
}
