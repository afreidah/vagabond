//go:build firecracker

// -------------------------------------------------------------------------------
// Firecracker Executor Tests
//
// Author: Alex Freidah
//
// Real microVMs, through the vagabond-firecracker and containerd the executor
// test environment starts, found by VAGABOND_FIRECRACKER and
// VAGABOND_CONTAINERD. Every call goes through the executor as the agent makes
// it: Submit pulls into a devmapper snapshot and hands it to the daemon, which
// answers the rest. Needs root and /dev/kvm, so it is behind the firecracker
// build tag and skips without the environment; make executor-test starts it.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// boot is how long a workload may take to pull, boot and end.
const boot = 2 * time.Minute

// newTestFirecracker reaches the test environment's daemon and containerd, or
// skips when there are none.
func newTestFirecracker(t *testing.T) *Firecracker {
	t.Helper()

	socket, containerd := os.Getenv("VAGABOND_FIRECRACKER"), os.Getenv("VAGABOND_CONTAINERD")
	if socket == "" || containerd == "" {
		t.Skip("VAGABOND_FIRECRACKER and VAGABOND_CONTAINERD name no daemon; make executor-test starts one")
	}

	f, err := NewFirecracker(FirecrackerConfig{
		Socket:     socket,
		Containerd: containerd,
		Namespace:  "vagabond-firecracker-test",
		Cgroup:     "/vagabond-test",
	})
	if err != nil {
		t.Fatalf("NewFirecracker() = %v", err)
	}

	t.Cleanup(func() { _ = f.Close() })

	return f
}

// boots submits a shell script as a microVM's workload under a new ID, which
// it returns, released when the test ends.
func boots(t *testing.T, f *Firecracker, script string) string {
	t.Helper()

	id := testkit.NewID(t).String()
	if err := f.Submit(t.Context(), id, workload(0, "/bin/sh", "-c", script)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	t.Cleanup(func() { _ = f.Release(context.Background(), id) })

	return id
}

// ended waits for a workload to reach a terminal state, and returns it.
func ended(t *testing.T, f *Firecracker, id string) execution.State {
	t.Helper()

	var state execution.State

	testkit.Eventually(t, boot, id+" to end", func() bool {
		st, err := f.Status(t.Context(), id)
		if err != nil {
			t.Fatalf("Status() = %v", err)
		}

		state = execution.State(st.GetState())

		return state.Terminal()
	})

	return state
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// Info names the daemon as workloads do, with the memory each microVM takes
// beyond what it declared, once containerd's devmapper snapshotter answers.
func TestFirecracker_Info(t *testing.T) {
	f := newTestFirecracker(t)

	info, err := f.Info(t.Context())
	if err != nil || info.GetName() != agentrpc.ExecutorFirecracker || info.GetOverhead().GetMemory() <= 0 {
		t.Errorf("Info() = %+v, %v; want firecracker with its overhead", info, err)
	}
}

// A workload runs in a microVM booted from its image's snapshot: its output
// and exit code come back, it is held at its size and overhead, and once
// released it is gone from the daemon and containerd alike.
func TestFirecracker_RunsAWorkload(t *testing.T) {
	f := newTestFirecracker(t)
	id := boots(t, f, "echo hello from a microVM; exit 3")

	if state := ended(t, f, id); state != execution.StateFailed {
		t.Errorf("state = %s, want failed on exit 3", state)
	}

	if res, err := f.Result(t.Context(), id); err != nil || res.ExitCode == nil || res.GetExitCode() != 3 {
		t.Errorf("Result() = %+v, %v; want exit 3", res, err)
	}

	var logs strings.Builder

	if err := f.Logs(t.Context(), id, 0, &logs); err != nil || !strings.Contains(logs.String(), "hello from a microVM") {
		t.Errorf("Logs() = %q, %v", logs.String(), err)
	}

	held, err := f.Held(t.Context())
	if err != nil {
		t.Fatalf("Held() = %v", err)
	}

	at := slices.IndexFunc(held, func(h *agentrpc.Held) bool { return h.GetExecutionId() == id })
	if at < 0 || held[at].GetResources().GetMemory() <= 128 {
		t.Errorf("Held() = %+v; want %s at 128 MiB and its overhead", held, id)
	}

	if err := f.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	if _, err := f.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Errorf("Status() after release = %v, want ErrUnknown", err)
	}

	if _, err := f.containerd.LoadContainer(t.Context(), id); !errdefs.IsNotFound(err) {
		t.Errorf("the container after release = %v, want none", err)
	}
}

// A cancelled workload stops and reports cancelled.
func TestFirecracker_Cancel(t *testing.T) {
	f := newTestFirecracker(t)
	id := boots(t, f, "sleep 60")

	testkit.Eventually(t, boot, id+" to start", func() bool {
		st, err := f.Status(t.Context(), id)

		return err == nil && st.GetState() == string(execution.StateRunning)
	})

	if err := f.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}

	if state := ended(t, f, id); state != execution.StateCancelled {
		t.Errorf("state = %s, want cancelled", state)
	}
}

// A second Submit of an execution the daemon holds starts nothing, since the
// ID is the idempotency key.
func TestFirecracker_SubmitTwiceStartsOnce(t *testing.T) {
	f := newTestFirecracker(t)
	id := boots(t, f, "echo once")

	if err := f.Submit(t.Context(), id, workload(0, "/bin/sh", "-c", "echo twice")); err != nil {
		t.Fatalf("second Submit() = %v", err)
	}

	ended(t, f, id)

	var logs strings.Builder

	if err := f.Logs(t.Context(), id, 0, &logs); err != nil || logs.String() != "once\n" {
		t.Errorf("Logs() = %q, %v; want the first workload alone", logs.String(), err)
	}
}

// Logs of an execution the daemon does not hold is unknown, with nothing
// written, so the agent can ask its other executor.
func TestFirecracker_LogsOfAnUnknownWorkload(t *testing.T) {
	f := newTestFirecracker(t)

	var logs strings.Builder

	if err := f.Logs(t.Context(), "exec-elsewhere", 0, &logs); !errors.Is(err, ErrUnknown) || logs.Len() > 0 {
		t.Errorf("Logs() = %q, %v; want nothing and ErrUnknown", logs.String(), err)
	}
}

// A workload the daemon refuses, here for declaring no memory to size its
// microVM by, leaves no container behind for a retry to trip on.
func TestFirecracker_ARefusedSubmitLeavesNothing(t *testing.T) {
	f := newTestFirecracker(t)
	id := testkit.NewID(t).String()

	w := workload(0, "/bin/true")
	w.Resources = nil

	if err := f.Submit(t.Context(), id, w); err == nil {
		_ = f.Release(context.Background(), id)
		t.Fatal("Submit() = nil, want the daemon's refusal")
	}

	if _, err := f.containerd.LoadContainer(t.Context(), id); !errdefs.IsNotFound(err) {
		t.Errorf("the container after a refused submit = %v, want none", err)
	}
}

// A workload outlives the executor that submitted it: a second over the same
// daemon, as a restarted agent opens, collects its result.
func TestFirecracker_SurvivesAnAgentRestart(t *testing.T) {
	first := newTestFirecracker(t)
	id := boots(t, first, "sleep 2; exit 5")

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second := newTestFirecracker(t)
	ended(t, second, id)

	if res, err := second.Result(t.Context(), id); err != nil || res.GetExitCode() != 5 {
		t.Errorf("Result() = %+v, %v; want exit 5", res, err)
	}

	if err := second.Release(t.Context(), id); err != nil {
		t.Errorf("Release() = %v", err)
	}
}
