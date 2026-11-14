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
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agent/executor/executortest"
	"github.com/afreidah/vagabond/internal/agentrpc"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// signalTimeout bounds channel waits so tests fail cleanly without flaking
// under the race detector.
const signalTimeout = 2 * time.Second

// newTestEndpoint builds an executions endpoint over exec with the given room
// and a buffered change channel.
func newTestEndpoint(exec agentExecutor, cpu, memory int64) *executions {
	return newExecutions(exec, &agentrpc.Resources{Cpu: cpu, Memory: memory}, make(chan struct{}, 32))
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

// pausing is a fake whose pull of id signals started, then waits for release.
func pausing(id string, started, release chan struct{}) *executortest.Fake {
	return &executortest.Fake{Pull: func(_ context.Context, pulled string) error {
		if pulled == id {
			close(started)
			<-release
		}

		return nil
	}}
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
	ep := newTestEndpoint(pausing("slow-pull", pullStarted, pullRelease), 2000, 2048)

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
		if sub.GetProviderId() != "cached" {
			t.Errorf("ProviderId = %q, want %q", sub.GetProviderId(), "cached")
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
	fake := pausing("sub-1", pullStarted, pullRelease)
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

	// sub-1 now runs on the node, so only what it left is free
	if _, err := ep.Submit(t.Context(), submitRequest("sub-3", "image-3:latest", 200, 200)); err != nil {
		t.Fatalf("sub-3 Submit() = %v, want nil", err)
	}
}

// Concurrent submissions racing for the last slot on a node admit exactly one
// workload and reject the rest with ResourceExhausted.
func TestSubmit_ConcurrentRaceAdmitsExactlyOne(t *testing.T) {
	t.Parallel()

	const racers = 16

	ep := newTestEndpoint(&executortest.Fake{}, 1000, 1024)

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
	fake := &executortest.Fake{Started: func(id string) {
		if id == "sub-1" {
			close(handoffReached)
			<-handoffRelease
		}
	}}

	ep := newTestEndpoint(fake, 1000, 1000)

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := ep.Submit(t.Context(), submitRequest("sub-1", "image-1:latest", 600, 600)); err != nil {
			t.Errorf("sub-1 Submit() = %v", err)
		}
	})

	waitSignal(t, handoffReached, signalTimeout, "timed out waiting for sub-1 to reach held-and-reserved handoff")

	// sub-1 is held and reserved; counted once, it leaves 400/400 for sub-2.
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
		fake := &executortest.Fake{Pull: func(context.Context, string) error {
			pulls.Add(1)
			<-pullRelease
			return nil
		}}

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
			if sub.GetProviderId() != "dup-1" {
				t.Errorf("ProviderId = %q, want %q", sub.GetProviderId(), "dup-1")
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
	ep := newTestEndpoint(pausing("dup-ctx", pullStarted, pullRelease), 1000, 1024)

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

	fake := &executortest.Fake{Pull: func(_ context.Context, id string) error {
		if id == "broken-pull" {
			return errors.New("pull failed")
		}
		return nil
	}}

	ep := newTestEndpoint(fake, 1000, 1024)

	_, err := ep.Submit(t.Context(), submitRequest("broken-pull", "bad:latest", 1000, 1024))
	if status.Code(err) != codes.Internal {
		t.Fatalf("broken-pull status = %v, want Internal", status.Code(err))
	}

	if _, err := ep.Submit(t.Context(), submitRequest("healthy", "good:latest", 1000, 1024)); err != nil {
		t.Fatalf("healthy Submit() after failed pull = %v, want nil", err)
	}
}
