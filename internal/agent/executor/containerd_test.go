//go:build containerd

// -------------------------------------------------------------------------------
// containerd Executor Tests
//
// Author: Alex Freidah
//
// Real workloads on the containerd the executor test environment starts, found
// by VAGABOND_CONTAINERD, in a namespace of their own. Needs root, so it is
// behind the containerd build tag and skips without the environment; make
// executor-test starts it, builds the tests as the user and runs them with
// sudo.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/client"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// newTestExecutor connects to the test environment's containerd, or skips
// when there is none.
func newTestExecutor(t *testing.T) *Containerd {
	t.Helper()

	socket := os.Getenv("VAGABOND_CONTAINERD")
	if socket == "" {
		t.Skip("VAGABOND_CONTAINERD names no containerd; make executor-test starts one")
	}

	c, err := New(&Config{
		Socket:    socket,
		Namespace: "vagabond-test",
		DataDir:   t.TempDir(),
		Cgroup:    "/vagabond-test",
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Info(t.Context()); err != nil {
		t.Fatalf("Info() = %v", err)
	}

	return c
}

// run submits a shell script as a workload, released when the test ends.
func run(t *testing.T, c *Containerd, id, script string, timeout time.Duration) {
	t.Helper()

	if err := c.Submit(t.Context(), id, workload(timeout, "sh", "-c", script)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	t.Cleanup(func() { _ = c.Release(context.Background(), id) })
}

// finished polls a workload until it reaches a terminal state.
func finished(t *testing.T, c *Containerd, id string) execution.Status {
	t.Helper()

	deadline := time.Now().Add(time.Minute)

	for time.Now().Before(deadline) {
		st, err := c.status(t.Context(), id)
		if err != nil {
			t.Fatalf("status() = %v", err)
		}

		if st.State.Terminal() {
			return st
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("workload %s did not finish", id)

	return execution.Status{}
}

// heldAs is the workload id among the held ones, or nil.
func heldAs(t *testing.T, c *Containerd, id string) *agentrpc.Held {
	t.Helper()

	held, err := c.Held(t.Context())
	if err != nil {
		t.Fatalf("Held() = %v", err)
	}

	for _, h := range held {
		if h.GetExecutionId() == id {
			return h
		}
	}

	return nil
}

// gatedWriter collects what is written to it, holding its first write until
// open is closed and announcing it by closing first.
type gatedWriter struct {
	first chan struct{}
	open  chan struct{}
	once  sync.Once
	buf   strings.Builder
}

// Write blocks the first time until the gate opens, then collects p.
func (g *gatedWriter) Write(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.first)
		<-g.open
	})

	return g.buf.Write(p)
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// A workload printing past the cap keeps only the end: a read from its first
// byte is placed where the kept part begins, and the front is freed on disk.
func TestContainerd_KeepsTheEndOfLongOutput(t *testing.T) {
	cfg := newTestExecutor(t).cfg
	cfg.MaxLogBytes = 4 * execution.LogChunk

	c, err := New(&cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	id := "exec-long-" + time.Now().Format("150405.000")
	printed := 6 * execution.LogChunk

	run(t, c, id, fmt.Sprintf("yes x | head -c %d; echo the end", printed), 0)
	finished(t, c, id)

	var logs strings.Builder

	w := &execution.PlacedWriter{Writer: &logs}

	if err := c.Logs(t.Context(), id, 0, w); err != nil || !strings.HasSuffix(logs.String(), "x\nthe end\n") {
		t.Fatalf("Logs() = %d bytes, %v; want the end", logs.Len(), err)
	}

	size := int64(printed + len("the end\n"))
	if start := execution.KeepFrom(size, cfg.MaxLogBytes) * execution.LogChunk; w.Start != start || int64(logs.Len()) != size-start {
		t.Errorf("Logs() placed at %d with %d bytes, want at %d with %d", w.Start, logs.Len(), start, size-start)
	}

	// The front is freed by the next pass of the trimming loop
	for deadline := time.Now().Add(2 * logTrim); allocated(t, c.logPath(id)) >= size; {
		if time.Now().After(deadline) {
			t.Fatalf("the front of %s was not freed", c.logPath(id))
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// A workload runs, exits with its own code, and its output and duration come
// back; released, it is gone.
func TestContainerd_RunsAWorkload(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-exit-" + time.Now().Format("150405.000")

	run(t, c, id, "echo hello from vagabond; exit 3", 0)

	if st := finished(t, c, id); st.State != execution.StateFailed || st.EndedAt.IsZero() {
		t.Errorf("status = %+v, want failed with an end time", st)
	}

	res, err := c.Result(t.Context(), id)
	if err != nil || res.ExitCode == nil || res.GetExitCode() != 3 {
		t.Fatalf("Result() = %+v, %v; want exit 3", res, err)
	}

	var logs strings.Builder

	if err := c.Logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: &logs}); err != nil || !strings.Contains(logs.String(), "hello from vagabond") {
		t.Errorf("Logs() = %q, %v", logs.String(), err)
	}

	h := heldAs(t, c, id)
	if h.GetRunning() || h.GetResources().GetCpu() != 250 || h.GetResources().GetMemory() != 128 {
		t.Errorf("held = %+v, want finished with 250 millicores and 128 MiB", h)
	}

	if err := c.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	if _, err := c.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Errorf("Status() after release = %v, want ErrUnknown", err)
	}
}

// An image reference that does not parse is refused as invalid, before any
// pull.
func TestContainerd_RefusesABadImage(t *testing.T) {
	c := newTestExecutor(t)
	w := workload(0, "true")
	w.Image = "Not A Reference"

	if err := c.Submit(t.Context(), "exec-bad-image", w); !errors.Is(err, ErrInvalid) {
		t.Errorf("Submit() = %v, want ErrInvalid", err)
	}
}

// A workload that cannot start leaves nothing behind, so a retry is not taken
// for a workload already there.
func TestContainerd_AFailedStartLeavesNothing(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-no-start-" + time.Now().Format("150405.000")

	if err := c.Submit(t.Context(), id, workload(0, "/no/such/program")); err == nil {
		_ = c.Release(context.Background(), id)
		t.Fatal("Submit() = nil, want the start to fail")
	}

	if _, err := c.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Errorf("Status() after a failed start = %v, want ErrUnknown", err)
	}
}

// Logs of a workload another executor holds is unknown here, with nothing
// written, so the caller can ask the next one.
func TestContainerd_LogsOfAnUnknownWorkload(t *testing.T) {
	c := newTestExecutor(t)

	var logs strings.Builder

	if err := c.Logs(t.Context(), "exec-elsewhere", 0, &execution.PlacedWriter{Writer: &logs}); !errors.Is(err, ErrUnknown) || logs.Len() > 0 {
		t.Errorf("Logs() = %q, %v; want nothing and ErrUnknown", logs.String(), err)
	}
}

// A workload whose task went after it started, as when the host restarts, has
// failed without an exit code and no longer takes room.
func TestContainerd_ATaskGoneAfterItStarted(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-gone-" + time.Now().Format("150405.000")

	run(t, c, id, "sleep 60", 0)

	_, task, err := c.load(t.Context(), id)
	if err != nil {
		t.Fatalf("load() = %v", err)
	}

	if _, err := task.Delete(t.Context(), client.WithProcessKill); err != nil {
		t.Fatalf("deleting the task: %v", err)
	}

	st, err := c.Status(t.Context(), id)
	if err != nil || st.GetState() != string(execution.StateFailed) {
		t.Errorf("Status() = %+v, %v; want failed", st, err)
	}

	if res, err := c.Result(t.Context(), id); err != nil || res.ExitCode != nil {
		t.Errorf("Result() = %+v, %v; want no exit code", res, err)
	}

	if h := heldAs(t, c, id); h.GetRunning() {
		t.Errorf("held = %+v, want not running", h)
	}
}

// Output read from an offset starts after the bytes the caller already has.
func TestContainerd_LogsFromAnOffset(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-offset-" + time.Now().Format("150405.000")

	run(t, c, id, "echo hello from vagabond", 0)
	finished(t, c, id)

	var rest strings.Builder

	if err := c.Logs(t.Context(), id, 6, &execution.PlacedWriter{Writer: &rest}); err != nil || rest.String() != "from vagabond\n" {
		t.Errorf("Logs() from offset 6 = %q, %v; want the rest of the line", rest.String(), err)
	}
}

// A follower whose workload is released mid-follow, as the dispatcher releases
// one once it has the result, still gets every line and ends without an error.
func TestContainerd_LogsFollowsThroughRelease(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-logs-" + time.Now().Format("150405.000")

	run(t, c, id, "echo first; sleep 1; echo last", 0)

	w := &gatedWriter{first: make(chan struct{}), open: make(chan struct{})}
	done := make(chan error, 1)

	go func() { done <- c.Logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: w}) }()

	select {
	case <-w.first:
	case err := <-done:
		t.Fatalf("Logs() = %v before any output", err)
	case <-time.After(time.Minute):
		t.Fatal("Logs() wrote nothing")
	}

	// The follower waits in its first write while the workload ends and goes.
	finished(t, c, id)

	if err := c.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	close(w.open)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Logs() = %v, want nil after a release", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("Logs() did not end after the release")
	}

	if got := w.buf.String(); got != "first\nlast\n" {
		t.Errorf("logs = %q, want both lines", got)
	}
}

// A workload outlives the executor that started it: a second executor on the
// same namespace and data directory, as a restarted agent would open, finds it,
// watches it, and collects its result.
func TestContainerd_SurvivesARestart(t *testing.T) {
	first := newTestExecutor(t)
	id := "exec-restart-" + time.Now().Format("150405.000")

	run(t, first, id, "sleep 2; echo after the restart; exit 5", 0)

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second, err := New(&first.cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = second.Close() })
	t.Cleanup(func() { _ = second.Release(context.Background(), id) })

	if err := second.Recover(t.Context()); err != nil {
		t.Fatalf("Recover() = %v", err)
	}

	finished(t, second, id)

	res, err := second.Result(t.Context(), id)
	if err != nil || res.ExitCode == nil || res.GetExitCode() != 5 {
		t.Errorf("Result() = %+v, %v; want exit 5", res, err)
	}

	var logs strings.Builder

	if err := second.Logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: &logs}); err != nil || !strings.Contains(logs.String(), "after the restart") {
		t.Errorf("Logs() = %q, %v; want the output from before the restart", logs.String(), err)
	}
}

// A cancelled workload stops and reports cancelled.
func TestContainerd_Cancel(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-cancel-" + time.Now().Format("150405.000")

	run(t, c, id, "sleep 60", 0)

	if err := c.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}

	if st := finished(t, c, id); st.State != execution.StateCancelled {
		t.Errorf("status = %s, want cancelled", st.State)
	}
}

// A workload past its timeout is stopped.
func TestContainerd_Timeout(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-timeout-" + time.Now().Format("150405.000")

	run(t, c, id, "sleep 60", time.Second)

	if st := finished(t, c, id); st.State != execution.StateFailed {
		t.Errorf("status = %s, want failed after the timeout", st.State)
	}
}
