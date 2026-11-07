// -------------------------------------------------------------------------------
// Agent Executions Endpoint Tests
//
// Author: Alex Freidah
//
// Concurrency, reservation handoff, and admission tests for AgentExecutions:
// cached workloads start while an image pull is in flight, concurrent racers
// cannot overcommit node capacity, duplicate submissions wait on the in-flight
// reservation, and workloads present in both Held and reserved are counted once.
// -------------------------------------------------------------------------------

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// FAKES
// -------------------------------------------------------------------------

// signalTimeout bounds channel waits so tests fail cleanly without flaking
// under the race detector.
const signalTimeout = 2 * time.Second

// fakeExecutor keeps submitted workloads in memory and exposes hooks before
// and after a workload enters held so tests can pause during an image pull or
// during the handoff window while Submit still holds its reservation.
type fakeExecutor struct {
	mu         sync.Mutex
	held       []executor.Held
	statuses   map[string]execution.Status
	beforeHold func(ctx context.Context, id string, spec *executor.Spec) error
	afterHold  func(ctx context.Context, id string, spec *executor.Spec)
}

// Submit leaves an already-held execution untouched, otherwise runs beforeHold,
// records the workload as running, and runs afterHold before returning.
func (f *fakeExecutor) Submit(ctx context.Context, id string, spec *executor.Spec) error {
	f.mu.Lock()
	for _, existing := range f.held {
		if existing.ID == id {
			f.mu.Unlock()
			return nil
		}
	}
	f.mu.Unlock()

	if f.beforeHold != nil {
		if err := f.beforeHold(ctx, id, spec); err != nil {
			return err
		}
	}

	f.mu.Lock()
	f.held = append(f.held, executor.Held{
		ID:      id,
		Running: true,
		CPU:     spec.CPU,
		Memory:  spec.Memory,
	})
	if f.statuses == nil {
		f.statuses = make(map[string]execution.Status)
	}
	f.statuses[id] = execution.Status{State: execution.StateRunning, ProviderID: "container-" + id}
	f.mu.Unlock()

	if f.afterHold != nil {
		f.afterHold(ctx, id, spec)
	}

	return nil
}

// Status returns the recorded status for id, or executor.ErrUnknown.
func (f *fakeExecutor) Status(_ context.Context, id string) (execution.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, ok := f.statuses[id]
	if !ok {
		return execution.Status{}, executor.ErrUnknown
	}

	return st, nil
}

// Result reports executor.ErrUnknown for fake workloads.
func (f *fakeExecutor) Result(_ context.Context, _ string) (*execution.Result, error) {
	return nil, executor.ErrUnknown
}

// Cancel is a no-op stub satisfying agentExecutor.
func (f *fakeExecutor) Cancel(_ context.Context, _ string) error {
	return nil
}

// Release is a no-op stub satisfying agentExecutor.
func (f *fakeExecutor) Release(_ context.Context, _ string) error {
	return nil
}

// Logs is a no-op stub satisfying agentExecutor.
func (f *fakeExecutor) Logs(_ context.Context, _ string, _ io.Writer) error {
	return nil
}

// Held returns a snapshot copy of the workloads currently recorded.
func (f *fakeExecutor) Held(_ context.Context) ([]executor.Held, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]executor.Held, len(f.held))
	copy(out, f.held)

	return out, nil
}

// Runtimes advertises a single container runtime.
func (f *fakeExecutor) Runtimes() []string {
	return []string{"runc"}
}

// newTestEndpoint builds an executions endpoint with an initialized reservation
// map and buffered change channel.
func newTestEndpoint(exec agentExecutor, cpu, memory int64) *executions {
	return &executions{
		exec:     exec,
		capacity: &agentrpc.Resources{Cpu: cpu, Memory: memory},
		changed:  make(chan struct{}, 32),
		reserved: make(map[string]*reservation),
	}
}

// submitRequest builds an AgentExecutions SubmitRequest with the given size.
func submitRequest(id, image string, cpu, memory int64) *agentrpc.SubmitRequest {
	return &agentrpc.SubmitRequest{
		ExecutionId: id,
		Workload: &agentrpc.Workload{
			Image:     image,
			Resources: &agentrpc.Resources{Cpu: cpu, Memory: memory},
		},
	}
}

// waitSignal fails the test if ch does not close within timeout.
func waitSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal(msg)
	}
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// A cached workload starts without waiting behind an unrelated image pull in
// flight on the same node.
func TestSubmit_CachedWorkloadStartsWhilePullInProgress(t *testing.T) {
	t.Parallel()

	pullStarted := make(chan struct{})
	pullRelease := make(chan struct{})
	fake := &fakeExecutor{
		beforeHold: func(_ context.Context, id string, _ *executor.Spec) error {
			if id == "slow-pull" {
				close(pullStarted)
				<-pullRelease
			}
			return nil
		},
	}

	ep := newTestEndpoint(fake, 2000, 2048)

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := ep.Submit(t.Context(), submitRequest("slow-pull", "heavy:latest", 500, 512)); err != nil {
			t.Errorf("slow-pull Submit() = %v", err)
		}
	})

	waitSignal(t, pullStarted, signalTimeout, "timed out waiting for slow pull to enter Submit")

	fastDone := make(chan struct{})
	wg.Go(func() {
		defer close(fastDone)
		sub, err := ep.Submit(t.Context(), submitRequest("cached", "cached:latest", 500, 512))
		if err != nil {
			t.Errorf("cached Submit() = %v", err)
			return
		}
		if sub.GetProviderId() != "container-cached" {
			t.Errorf("ProviderId = %q, want %q", sub.GetProviderId(), "container-cached")
		}
	})

	waitSignal(t, fastDone, signalTimeout, "cached workload blocked behind in-flight image pull")

	close(pullRelease)
	wg.Wait()
}

// Capacity reserved by an in-flight pull rejects a second submission exceeding
// remaining room and is released once Submit returns.
func TestSubmit_InflightReservationBlocksOvercommit(t *testing.T) {
	t.Parallel()

	pullStarted := make(chan struct{})
	pullRelease := make(chan struct{})
	fake := &fakeExecutor{
		beforeHold: func(_ context.Context, id string, _ *executor.Spec) error {
			if id == "sub-1" {
				close(pullStarted)
				<-pullRelease
			}
			return nil
		},
	}

	ep := newTestEndpoint(fake, 1000, 1024)

	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = ep.Submit(t.Context(), submitRequest("sub-1", "image-1:latest", 800, 800))
	})

	waitSignal(t, pullStarted, signalTimeout, "timed out waiting for sub-1 to enter Submit")

	_, err := ep.Submit(t.Context(), submitRequest("sub-2", "image-2:latest", 400, 400))
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("sub-2 status = %v, want ResourceExhausted", status.Code(err))
	}

	close(pullRelease)
	wg.Wait()

	if _, err := ep.Submit(t.Context(), submitRequest("sub-3", "image-3:latest", 200, 200)); err != nil {
		t.Fatalf("sub-3 Submit() = %v, want nil", err)
	}
}

// Concurrent submissions racing for the last slot on a node admit exactly one
// workload and reject the rest with ResourceExhausted.
func TestSubmit_ConcurrentRaceAdmitsExactlyOne(t *testing.T) {
	t.Parallel()

	const racers = 16

	fake := &fakeExecutor{}
	ep := newTestEndpoint(fake, 1000, 1024)

	start := make(chan struct{})
	var admitted, exhausted, unexpected atomic.Int32

	var wg sync.WaitGroup
	for i := range racers {
		id := fmt.Sprintf("racer-%d", i)
		wg.Go(func() {
			<-start
			_, err := ep.Submit(t.Context(), submitRequest(id, "race:latest", 600, 600))
			switch status.Code(err) {
			case codes.OK:
				admitted.Add(1)
			case codes.ResourceExhausted:
				exhausted.Add(1)
			default:
				unexpected.Add(1)
			}
		})
	}

	close(start)
	wg.Wait()

	if got := admitted.Load(); got != 1 {
		t.Fatalf("admitted = %d, want 1 (exhausted=%d, unexpected=%d)", got, exhausted.Load(), unexpected.Load())
	}
	if got := exhausted.Load(); got != racers-1 {
		t.Fatalf("exhausted = %d, want %d", got, racers-1)
	}
}

// A workload present in both Held and reserved during the handoff before Submit
// returns is counted only once, leaving the true remaining capacity available.
func TestSubmit_HandoffDoesNotDoubleCountHeldReservation(t *testing.T) {
	t.Parallel()

	handoffReached := make(chan struct{})
	handoffRelease := make(chan struct{})
	fake := &fakeExecutor{
		afterHold: func(_ context.Context, id string, _ *executor.Spec) {
			if id == "sub-1" {
				close(handoffReached)
				<-handoffRelease
			}
		},
	}

	ep := newTestEndpoint(fake, 1000, 1000)

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := ep.Submit(t.Context(), submitRequest("sub-1", "image-1:latest", 600, 600)); err != nil {
			t.Errorf("sub-1 Submit() = %v", err)
		}
	})

	waitSignal(t, handoffReached, signalTimeout, "timed out waiting for sub-1 to reach held-and-reserved handoff")

	// sub-1 is now both in Held (600/600) and still reserved (600/600).
	// Counting it once leaves 400/400 free for sub-2, then 0/0 for sub-3.
	if _, err := ep.Submit(t.Context(), submitRequest("sub-2", "image-2:latest", 400, 400)); err != nil {
		t.Fatalf("sub-2 Submit() during handoff = %v, want nil", err)
	}

	_, err := ep.Submit(t.Context(), submitRequest("sub-3", "image-3:latest", 100, 100))
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("sub-3 status = %v, want ResourceExhausted", status.Code(err))
	}

	close(handoffRelease)
	wg.Wait()
}

// A second submission for an execution already in flight waits for the first
// reservation to finish and then succeeds as a held resubmission without a
// second pull.
func TestSubmit_SameIDWaitsForInflightAndSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pulls atomic.Int32
		pullRelease := make(chan struct{})
		fake := &fakeExecutor{
			beforeHold: func(_ context.Context, _ string, _ *executor.Spec) error {
				pulls.Add(1)
				<-pullRelease
				return nil
			},
		}

		ep := newTestEndpoint(fake, 1000, 1024)

		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := ep.Submit(t.Context(), submitRequest("dup-1", "app:latest", 1000, 1024)); err != nil {
				t.Errorf("first Submit() = %v", err)
			}
		})

		synctest.Wait()

		dupDone := make(chan struct{})
		wg.Go(func() {
			defer close(dupDone)
			sub, err := ep.Submit(t.Context(), submitRequest("dup-1", "app:latest", 1000, 1024))
			if err != nil {
				t.Errorf("duplicate Submit() = %v", err)
				return
			}
			if sub.GetProviderId() != "container-dup-1" {
				t.Errorf("ProviderId = %q, want %q", sub.GetProviderId(), "container-dup-1")
			}
		})

		synctest.Wait()

		select {
		case <-dupDone:
			t.Fatal("duplicate submission returned before in-flight pull finished")
		default:
		}

		close(pullRelease)
		wg.Wait()

		if got := pulls.Load(); got != 1 {
			t.Fatalf("image pulls = %d, want 1", got)
		}
	})
}

// Cancelling or timing out a duplicate submission while waiting on an in-flight
// reservation returns the caller's context status rather than Internal.
func TestSubmit_SameIDContextDoneWhileWaiting(t *testing.T) {
	t.Parallel()

	pullStarted := make(chan struct{})
	pullRelease := make(chan struct{})
	fake := &fakeExecutor{
		beforeHold: func(_ context.Context, _ string, _ *executor.Spec) error {
			close(pullStarted)
			<-pullRelease
			return nil
		},
	}

	ep := newTestEndpoint(fake, 1000, 1024)

	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = ep.Submit(t.Context(), submitRequest("dup-ctx", "app:latest", 500, 512))
	})

	waitSignal(t, pullStarted, signalTimeout, "timed out waiting for first submission to enter Submit")

	cancelCtx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := ep.Submit(cancelCtx, submitRequest("dup-ctx", "app:latest", 500, 512))
	if status.Code(err) != codes.Canceled {
		t.Fatalf("cancelled duplicate status = %v, want Canceled", status.Code(err))
	}

	deadlineCtx, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Millisecond))
	defer stop()

	_, err = ep.Submit(deadlineCtx, submitRequest("dup-ctx", "app:latest", 500, 512))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expired duplicate status = %v, want DeadlineExceeded", status.Code(err))
	}

	close(pullRelease)
	wg.Wait()
}

// A failed pull releases its reserved capacity so subsequent workloads can fit.
func TestSubmit_FailedPullReleasesReservedCapacity(t *testing.T) {
	t.Parallel()

	fake := &fakeExecutor{
		beforeHold: func(_ context.Context, id string, _ *executor.Spec) error {
			if id == "broken-pull" {
				return errors.New("pull failed")
			}
			return nil
		},
	}

	ep := newTestEndpoint(fake, 1000, 1024)

	_, err := ep.Submit(t.Context(), submitRequest("broken-pull", "bad:latest", 1000, 1024))
	if status.Code(err) != codes.Internal {
		t.Fatalf("broken-pull status = %v, want Internal", status.Code(err))
	}

	if _, err := ep.Submit(t.Context(), submitRequest("healthy", "good:latest", 1000, 1024)); err != nil {
		t.Fatalf("healthy Submit() after failed pull = %v, want nil", err)
	}
}
