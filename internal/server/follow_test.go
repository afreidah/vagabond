// -------------------------------------------------------------------------------
// Live Output Tests
//
// Author: Alex Freidah
//
// Following an execution's output over the API as its dispatch stores it,
// from an offset, and what is served once it has ended; where a response says
// its body begins; how a follow ends, with the output or with the server; and
// that writes flush through the request log.
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
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/testkit"
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

// A followed execution's output arrives as its dispatch stores it, from the
// requested offset, and each follow ends once the run has, its output whole.
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

// Output whose front was dropped past the ceiling is still read by position in
// the whole output, and an offset in what was dropped starts at the first byte
// kept, which the response's header names.
func TestExecutionLogs_StoredPastTheCeiling(t *testing.T) {
	h := newHarness(t)
	id, lease := ended(t, h.executions, time.Now(), execution.LogChunk)

	if err := h.executions.AppendLog(t.Context(), id, 1, []byte("the end\n"), 1, lease); err != nil {
		t.Fatalf("AppendLog() = %v", err)
	}

	chunk := strconv.Itoa(execution.LogChunk)

	for query, want := range map[string]struct{ body, start string }{
		"":         {"the end\n", chunk},
		"offset=5": {"the end\n", chunk},
		"offset=" + strconv.Itoa(execution.LogChunk+4): {"end\n", strconv.Itoa(execution.LogChunk + 4)},
	} {
		status, start, got := h.getLogs(t, id.String(), query)
		if status != http.StatusOK || got != want.body || start != want.start {
			t.Errorf("logs?%s = %d %q from %s, want %q from %s", query, status, got, start, want.body, want.start)
		}
	}
}

// getLogs requests an execution's output with query and returns the status,
// where the response says the body begins, and the body.
func (h *harness) getLogs(t *testing.T, id, query string) (int, string, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.url+"/v1/execution/"+id+"/logs?"+query, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	defer resp.Body.Close()

	return resp.StatusCode, resp.Header.Get(api.LogsStartHeader), readLogs(t, resp.Body)
}

// Pruning takes the output of executions that ended longer ago than the
// retention, and leaves the rest.
func TestPrune_TakesOutputPastTheRetention(t *testing.T) {
	reg, led := fixtures(t)
	executions := memory.NewExecutions()
	s := New(reg, led, executions, NewMockserverJobs(gomock.NewController(t)), memory.NewProviders(), nil,
		WithLogs(execution.DefaultMaxLogBytes, time.Hour))

	old, oldLease := ended(t, executions, time.Now().Add(-2*time.Hour), 0)
	recent, recentLease := ended(t, executions, time.Now(), 0)

	for id, lease := range map[execution.ID]execution.Lease{old: oldLease, recent: recentLease} {
		if err := executions.AppendLog(t.Context(), id, 0, []byte("output"), 0, lease); err != nil {
			t.Fatalf("AppendLog() = %v", err)
		}
	}

	if err := s.prune(t.Context()); err != nil {
		t.Fatalf("prune() = %v", err)
	}

	for id, want := range map[execution.ID]string{old: "", recent: "output"} {
		var out strings.Builder

		err := executions.ReadLog(t.Context(), id, 0, func(_ int64, data []byte) error {
			out.Write(data)

			return nil
		})
		if err != nil || out.String() != want {
			t.Errorf("ReadLog(%s) = %q, %v; want %q", id, out.String(), err, want)
		}
	}
}

// dispatched records a running dispatch leased to this test for an hour, for
// executions to be recorded under.
func dispatched(t *testing.T, s *memory.Executions) *execution.Dispatch {
	t.Helper()

	d := &execution.Dispatch{
		ID: testkit.NewID(t), Namespace: "default", Job: "sleepy", Tasks: 1, State: execution.DispatchRunning,
		Owner: "server:test:1", Epoch: 1, LeaseUntil: time.Now().Add(time.Hour), Created: time.Now(),
	}

	if err := s.CreateDispatch(t.Context(), d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	return d
}

// ended records an execution that succeeded at at, its output stored from
// byte start, in a dispatch of its own; returns it and the dispatch's lease.
func ended(t *testing.T, s *memory.Executions, at time.Time, start int64) (execution.ID, execution.Lease) {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	d := dispatched(t, s)

	r := &execution.Record{
		ID:        id,
		State:     execution.StateSucceeded,
		StartedAt: at.Add(-time.Second),
		EndedAt:   at,
		UpdatedAt: at,
		Dispatch:  d.ID,
		Provider:  "box",
		Result:    &execution.Result{ID: id, ExitCode: new(0), Duration: time.Second},
		LogsAt:    at,
		LogsStart: start,
	}

	if err := s.Create(t.Context(), r, d.Lease()); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return id, d.Lease()
}

// followed is a server over store, which reads mem, following every 10ms, a
// running execution recorded in mem, and the lease its output is written
// under.
func followed(t *testing.T, store serverExecutions, mem *memory.Executions) (*Server, *execution.Record, execution.Lease) {
	t.Helper()

	reg, led := fixtures(t)
	s := New(reg, led, store, NewMockserverJobs(gomock.NewController(t)), memory.NewProviders(), nil)
	s.followPoll = 10 * time.Millisecond

	d := dispatched(t, mem)

	rec := &execution.Record{ID: testkit.NewID(t), State: execution.StateRunning, Dispatch: d.ID}
	if err := mem.Create(t.Context(), rec, d.Lease()); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return s, rec, d.Lease()
}

// A follow sends output as it is stored, the last chunk as it grows, and ends
// once a read made after the record was terminal has been sent.
func TestSendLogs_EndsWithTheOutput(t *testing.T) {
	store := memory.NewExecutions()
	s, rec, lease := followed(t, store, store)

	if err := store.AppendLog(t.Context(), rec.ID, 0, []byte("a"), 0, lease); err != nil {
		t.Fatalf("AppendLog() = %v", err)
	}

	w := httptest.NewRecorder()
	done := make(chan error, 1)

	go func() { done <- s.sendLogs(t.Context(), rec, newLogsResponse(w, 0), true) }()

	time.Sleep(30 * time.Millisecond)

	// The output is whole before the record is terminal, as collect writes them
	if err := store.AppendLog(t.Context(), rec.ID, 0, []byte("ab"), 0, lease); err != nil {
		t.Fatalf("AppendLog() = %v", err)
	}

	ended := *rec
	if err := ended.To(execution.StateSucceeded, time.Now()); err != nil {
		t.Fatalf("To() = %v", err)
	}

	if err := store.Save(t.Context(), &ended, lease); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	select {
	case err := <-done:
		if err != nil || w.Body.String() != "ab" {
			t.Errorf("sendLogs() = %v, sent %q; want every byte once", err, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the follow did not end with the output")
	}
}

// A follow ends at once when shutdown begins, and when the client goes.
func TestSendLogs_EndsWithTheServerOrClient(t *testing.T) {
	store := memory.NewExecutions()
	s, rec, _ := followed(t, store, store)

	stopped := make(chan error, 1)

	go func() { stopped <- s.sendLogs(t.Context(), rec, newLogsResponse(httptest.NewRecorder(), 0), true) }()

	time.Sleep(20 * time.Millisecond)
	s.endStreams()
	s.endStreams()

	ctx, cancel := context.WithCancel(t.Context())
	gone := make(chan error, 1)

	go func() { gone <- s.sendLogs(ctx, rec, newLogsResponse(httptest.NewRecorder(), 0), true) }()

	cancel()

	for name, ch := range map[string]chan error{"shutdown": stopped, "client gone": gone} {
		select {
		case err := <-ch:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("%s: sendLogs() = %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the follow outlived it", name)
		}
	}
}

// failingReads is a memory store whose reads of output hand over what is
// stored, then fail.
type failingReads struct {
	*memory.Executions
}

// ReadLog hands over the stored chunks, then fails.
func (f failingReads) ReadLog(ctx context.Context, id execution.ID, fromSeq int64, each func(int64, []byte) error) error {
	if err := f.Executions.ReadLog(ctx, id, fromSeq, each); err != nil {
		return err
	}

	return errors.New("store unreachable")
}

// A read that fails before anything is sent is answered with its error, and
// one that fails after ends the response with what was sent.
func TestExecutionLogs_ReadFails(t *testing.T) {
	store := failingReads{memory.NewExecutions()}
	s, rec, lease := followed(t, store, store.Executions)

	// In order: the empty case must run before anything is stored
	for _, c := range []struct {
		stored string
		begun  bool
	}{{"", false}, {"partial", true}} {
		stored, begun := c.stored, c.begun
		if stored != "" {
			if err := store.AppendLog(t.Context(), rec.ID, 0, []byte(stored), 0, lease); err != nil {
				t.Fatalf("AppendLog() = %v", err)
			}
		}

		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/execution/"+rec.ID.String()+"/logs", nil)
		r.SetPathValue("id", rec.ID.String())

		w := httptest.NewRecorder()
		s.executionLogs(w, r)

		if (w.Code == http.StatusOK) != begun || (begun && w.Body.String() != stored) {
			t.Errorf("with %q stored: %d %q, want the response begun %t", stored, w.Code, w.Body.String(), begun)
		}
	}
}

// A response is placed past a gap through its header until its body begins,
// and ended after; an empty write begins nothing.
func TestLogsResponse_StartsAt(t *testing.T) {
	w := httptest.NewRecorder()
	out := newLogsResponse(w, 5)

	if n, err := out.Write(nil); n != 0 || err != nil || out.wrote {
		t.Errorf("Write(nil) = %d, %v, began %t; want nothing begun", n, err, out.wrote)
	}

	if err := out.StartsAt(execution.LogChunk); err != nil || w.Header().Get(api.LogsStartHeader) != strconv.Itoa(execution.LogChunk) {
		t.Errorf("StartsAt() before the body = %v, header %q; want the header moved", err, w.Header().Get(api.LogsStartHeader))
	}

	_, _ = out.Write([]byte("x"))

	if err := out.StartsAt(2 * execution.LogChunk); !errors.Is(err, errMoved) || out.next != execution.LogChunk+1 {
		t.Errorf("StartsAt() after the body began = %v at %d, want errMoved at %d", err, out.next, execution.LogChunk+1)
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
	srv := New(reg, led, memory.NewExecutions(), memory.NewJobs(),
		memory.NewProviders(), nil)

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

// -------------------------------------------------------------------------
// REQUEST LOG
// -------------------------------------------------------------------------

// A write flushed behind the request log reaches the client while the handler
// is still running, so live output is not held until the response ends.
func TestLogRequests_Flushes(t *testing.T) {
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	flushed := make(chan error, 1)
	release := make(chan struct{})

	srv := httptest.NewServer(s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first line\n")

		err := http.NewResponseController(w).Flush()
		flushed <- err

		// On a failed flush the response ends, so the test can report it.
		if err == nil {
			<-release
		}
	})))

	// Cleanups run last first: the handler is released before the server closes.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET = %v", err)
	}
	defer resp.Body.Close()

	if err := <-flushed; err != nil {
		t.Fatalf("Flush() behind the request log = %v", err)
	}

	if line, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil || line != "first line\n" {
		t.Errorf("read while the handler runs = %q, %v; want the flushed line", line, err)
	}
}
