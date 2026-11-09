// -------------------------------------------------------------------------------
// Live Output Tests
//
// Author: Alex Freidah
//
// Following a running execution's output over the API, from an offset, and
// what is served once it has ended; how a follow ends, with its execution or
// with the server.
// -------------------------------------------------------------------------------

package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// startSleepy runs sleepyJob and returns its execution once the provider has
// accepted it.
func (h *harness) startSleepy(t *testing.T) api.Execution {
	t.Helper()

	var started api.DispatchResponse

	if status := h.call(t, http.MethodPost, "/v1/jobs/run", api.RunRequest{Source: sleepyJob}, &started); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	return h.awaitExecution(t, started.DispatchID, "accepted")
}

// succeed moves a run on the container provider through running to succeeded.
func (h *harness) succeed(t *testing.T, exec *api.Execution) {
	t.Helper()

	id, err := execution.ParseID(exec.ID)
	if err != nil {
		t.Fatalf("ParseID() = %v", err)
	}

	for _, next := range []execution.State{execution.StateRunning, execution.StateSucceeded} {
		if err := h.box.Advance(id, next); err != nil {
			t.Fatalf("Advance(%s) = %v", next, err)
		}
	}
}

// firstLine follows exec's output with query and reads its first line.
func (h *harness) firstLine(t *testing.T, exec *api.Execution, query string) (*bufio.Reader, string) {
	t.Helper()

	_, r := h.openLogs(t, exec.ID, query)

	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the first line with %q: %v", query, err)
	}

	return r, line
}

// -------------------------------------------------------------------------
// FOLLOWING
// -------------------------------------------------------------------------

// A followed execution's output streams from its provider, from the requested
// offset, and each stream ends with the run.
func TestExecutionLogs_FollowsARunningExecution(t *testing.T) {
	h := newHarness(t)
	exec := h.startSleepy(t)

	whole, line := h.firstLine(t, &exec, "follow=true")
	if line != "fake execution output\n" {
		t.Errorf("followed = %q, want the provider's output", line)
	}

	rest, line := h.firstLine(t, &exec, "follow=true&offset=5")
	if line != "execution output\n" {
		t.Errorf("followed from offset 5 = %q, want the output past it", line)
	}

	h.succeed(t, &exec)

	for _, r := range []*bufio.Reader{whole, rest} {
		if tail, err := io.ReadAll(r); err != nil || len(tail) != 0 {
			t.Errorf("after the run ended = %q, %v; want the stream closed", tail, err)
		}
	}
}

// A provider that cannot resume refuses a follow from an offset with 416,
// before sending any output, and still streams from the start.
func TestExecutionLogs_FollowCannotResume(t *testing.T) {
	h := newHarness(t)
	h.box.NoResume = true

	exec := h.startSleepy(t)

	if status, _ := h.openLogs(t, exec.ID, "follow=true&offset=5"); status != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("follow from offset 5 = %d, want 416", status)
	}

	if _, line := h.firstLine(t, &exec, "follow=true"); line != "fake execution output\n" {
		t.Errorf("follow from the start = %q, want the provider's output", line)
	}
}

// An ended execution's stored output is served from the requested offset,
// followed or not, and an offset that is not a byte count is refused.
func TestExecutionLogs_StoredFromAnOffset(t *testing.T) {
	h := newHarness(t)
	exec := h.startSleepy(t)

	h.succeed(t, &exec)
	h.awaitExecution(t, exec.DispatchID, "succeeded")

	for query, want := range map[string]string{
		"":                     "fake execution output\n",
		"offset=5":             "execution output\n",
		"follow=true&offset=5": "execution output\n",
		"offset=100":           "",
	} {
		status, body := h.openLogs(t, exec.ID, query)
		if got := readLogs(t, body); status != http.StatusOK || got != want {
			t.Errorf("logs?%s = %d %q, want %q", query, status, got, want)
		}
	}

	for _, query := range []string{"offset=-1", "offset=many"} {
		if status, _ := h.openLogs(t, exec.ID, query); status != http.StatusBadRequest {
			t.Errorf("logs?%s = %d, want 400", query, status)
		}
	}
}

// A truncated copy holds only the end of the output, so only offset zero can
// be placed in it.
func TestStoredFrom(t *testing.T) {
	logs := []byte("0123456789")

	tests := []struct {
		name   string
		result *execution.Result
		offset int64
		want   string
	}{
		{"no result", nil, 0, ""},
		{"whole", &execution.Result{Logs: logs}, 0, "0123456789"},
		{"from an offset", &execution.Result{Logs: logs}, 4, "456789"},
		{"past the end", &execution.Result{Logs: logs}, 10, ""},
		{"truncated, whole", &execution.Result{Logs: logs, LogsTruncated: true}, 0, "0123456789"},
		{"truncated, from an offset", &execution.Result{Logs: logs, LogsTruncated: true}, 4, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(storedFrom(tc.result, tc.offset)); got != tc.want {
				t.Errorf("storedFrom() = %q, want %q", got, tc.want)
			}
		})
	}
}

// streamerFunc is a LogStreamer from a function.
type streamerFunc func(ctx context.Context, id execution.ID, offset int64, w io.Writer) error

// StreamLogs calls f.
func (f streamerFunc) StreamLogs(ctx context.Context, id execution.ID, offset int64, w io.Writer) error {
	return f(ctx, id, offset, w)
}

// Only a follow of a running execution on a provider that can stream is
// streamed; anything else is served the stored output.
func TestStreamer(t *testing.T) {
	reg, led := fixtures(t)
	s := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	running := execution.Status{State: execution.StateRunning}

	tests := []struct {
		name  string
		query string
		rec   execution.Record
		want  bool
	}{
		{"following a running execution", "follow=true", execution.Record{Status: running, Provider: "box"}, true},
		{"not following", "", execution.Record{Status: running, Provider: "box"}, false},
		{"ended", "follow=true", execution.Record{Status: execution.Status{State: execution.StateSucceeded}, Provider: "box"}, false},
		{"provider no longer configured", "follow=true", execution.Record{Status: running, Provider: "gone"}, false},
		{"provider cannot stream", "follow=true", execution.Record{Status: running, Provider: "fn"}, false},
	}

	for i := range tests {
		tc := &tests[i]

		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/execution/x/logs?"+tc.query, nil)

			if _, got := s.streamer(r, &tc.rec); got != tc.want {
				t.Errorf("streamer() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A stream that fails ends the response with whatever it sent, and one that
// cannot resume is answered 416 only while nothing has been sent.
func TestFollow_StreamErrors(t *testing.T) {
	reg, led := fixtures(t)
	s := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	tests := []struct {
		name     string
		wrote    string
		err      error
		want     int
		wantBody string
	}{
		{"broken", "", errors.New("connection reset"), http.StatusOK, ""},
		{"cannot resume", "", plugin.ErrNoResume, http.StatusRequestedRangeNotSatisfiable, ""},
		{"cannot resume after output", "line\n", plugin.ErrNoResume, http.StatusOK, "line\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			stream := streamerFunc(func(_ context.Context, _ execution.ID, _ int64, out io.Writer) error {
				if tc.wrote != "" {
					_, _ = io.WriteString(out, tc.wrote)
				}

				return tc.err
			})

			s.follow(t.Context(), w, stream, newExecutionID(t), 5)

			if w.Code != tc.want || (tc.wantBody != "" && w.Body.String() != tc.wantBody) {
				t.Errorf("follow() = %d %q, want %d %q", w.Code, w.Body.String(), tc.want, tc.wantBody)
			}
		})
	}
}

// -------------------------------------------------------------------------
// ENDING
// -------------------------------------------------------------------------

// A follow ends once its execution has ended and the linger has passed, and at
// once when shutdown begins, even while its record cannot be read.
func TestEndFollow(t *testing.T) {
	reg, led := fixtures(t)
	store := memory.NewExecutions()

	s := New(reg, led, store, NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))
	s.followPoll, s.followLinger = time.Millisecond, 100*time.Millisecond

	rec := &execution.Record{Status: execution.Status{ID: newExecutionID(t), State: execution.StateRunning}}
	if err := store.Create(t.Context(), rec); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	ended := make(chan struct{})

	go s.endFollow(t.Context(), func() { close(ended) }, rec.ID)

	select {
	case <-ended:
		t.Fatal("the follow ended while its execution ran")
	case <-time.After(20 * time.Millisecond):
	}

	finished := time.Now()

	if err := rec.To(execution.StateSucceeded, finished); err != nil {
		t.Fatalf("To() = %v", err)
	}

	if err := store.Update(t.Context(), rec, execution.StateRunning); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	select {
	case <-ended:
		if waited := time.Since(finished); waited < s.followLinger {
			t.Errorf("ended %s after the execution, before the %s linger", waited, s.followLinger)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the follow did not end with its execution")
	}

	stopped := make(chan struct{})

	// Nothing recorded this one, so every read of it fails and is retried.
	go s.endFollow(t.Context(), func() { close(stopped) }, newExecutionID(t))

	time.Sleep(10 * time.Millisecond)
	s.endStreams()
	s.endStreams()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the follow outlived shutdown")
	}
}

// A server stopping with a follow open ends it rather than waiting out its
// shutdown timeout.
func TestServeListener_ShutdownEndsFollows(t *testing.T) {
	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	reg, led := fixtures(t)
	srv := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)),
		slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- srv.ServeListener(ctx, listener, nil) }()

	box, _ := reg.Provider("box")
	h := &harness{url: "http://" + listener.Addr().String(), box: box.(*plugin.FakeContainerProvider)}

	exec := h.startSleepy(t)
	stream, _ := h.firstLine(t, &exec, "follow=true")

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeListener() = %v", err)
		}
	case <-time.After(shutdownTimeout / 2):
		t.Fatal("the server waited on the open follow")
	}

	if _, err := io.ReadAll(stream); err != nil {
		t.Errorf("the follow ended with %v, want a closed stream", err)
	}
}
