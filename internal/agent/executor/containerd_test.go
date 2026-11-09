//go:build containerd

// -------------------------------------------------------------------------------
// containerd Executor Tests
//
// Author: Alex Freidah
//
// Real workloads on the host's containerd, in a namespace of their own. Needs
// root, so it is behind the containerd build tag and skips when the socket
// cannot be reached; make containerd-test builds it as the user and runs it
// with sudo.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// testImage is small and has a shell.
const testImage = "alpine:3.20"

// newTestExecutor connects to the host's containerd, or skips.
func newTestExecutor(t *testing.T) *Containerd {
	t.Helper()

	c, err := New(Config{
		Socket:    "/run/containerd/containerd.sock",
		Namespace: "vagabond-test",
		DataDir:   t.TempDir(),
		Cgroup:    "/vagabond-test",
	})
	if err != nil {
		t.Skipf("containerd unavailable: %v", err)
	}

	if err := c.Check(t.Context()); err != nil {
		t.Skipf("containerd unavailable: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

// run submits a shell script as a workload, released when the test ends.
func run(t *testing.T, c *Containerd, id, script string, timeout time.Duration) {
	t.Helper()

	err := c.Submit(t.Context(), id, &Spec{
		Image: testImage, Command: []string{"sh", "-c", script}, Memory: 64, CPU: 250, Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	t.Cleanup(func() { _ = c.Release(context.Background(), id) })
}

// finished polls a workload until it reaches a terminal state.
func finished(t *testing.T, c *Containerd, id string) execution.Status {
	t.Helper()

	deadline := time.Now().Add(time.Minute)

	for time.Now().Before(deadline) {
		st, err := c.Status(t.Context(), id)
		if err != nil {
			t.Fatalf("Status() = %v", err)
		}

		if st.State.Terminal() {
			return st
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("workload %s did not finish", id)

	return execution.Status{}
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
	if err != nil || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("Result() = %+v, %v; want exit 3", res, err)
	}

	if !strings.Contains(string(res.Logs), "hello from vagabond") {
		t.Errorf("logs = %q", res.Logs)
	}

	held, err := c.Held(t.Context())
	if err != nil {
		t.Fatalf("Held() = %v", err)
	}

	found := false

	for _, h := range held {
		if h.ID == id {
			found = true

			if h.Running || h.CPU != 250 || h.Memory != 64 {
				t.Errorf("held = %+v, want finished with 250 millicores and 64 MiB", h)
			}
		}
	}

	if !found {
		t.Errorf("Held() = %+v; want %s", held, id)
	}

	if err := c.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	if _, err := c.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Errorf("Status() after release = %v, want ErrUnknown", err)
	}
}

// Output read from an offset starts after the bytes the caller already has.
func TestContainerd_LogsFromAnOffset(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-offset-" + time.Now().Format("150405.000")

	run(t, c, id, "echo hello from vagabond", 0)
	finished(t, c, id)

	var rest strings.Builder

	if err := c.Logs(t.Context(), id, 6, &rest); err != nil || rest.String() != "from vagabond\n" {
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

	go func() { done <- c.Logs(t.Context(), id, 0, w) }()

	select {
	case <-w.first:
	case err := <-done:
		t.Fatalf("Logs() = %v before any output", err)
	case <-time.After(time.Minute):
		t.Fatal("Logs() wrote nothing")
	}

	// The follower is held inside its first write while the workload ends and
	// its container goes.
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

	second, err := New(first.cfg)
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
	if err != nil || res.ExitCode == nil || *res.ExitCode != 5 || !strings.Contains(string(res.Logs), "after the restart") {
		t.Errorf("Result() = %+v, %v; want exit 5 with the output", res, err)
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
