// -------------------------------------------------------------------------------
// Panic Tests
//
// Author: Alex Freidah
//
// A panic ends the unit of work it happened in: an execution fails as
// internal, a run ends unanswered, a lease is lost, and a dispatch claimed
// past the resume limit is ended rather than run again.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// DOUBLES
// -------------------------------------------------------------------------

// panickingProvider is a scripted provider whose Status panics.
type panickingProvider struct {
	*scriptedProvider
}

// Status panics, as a provider mishandling a response would.
func (p *panickingProvider) Status(context.Context, execution.ID) (execution.Status, error) {
	panic("status response in an unexpected shape")
}

// panickingRegistry is a fake registry whose Inputs panics, inside Run's own
// goroutine.
type panickingRegistry struct {
	*fakeRegistry
}

// Inputs panics before any provider is tried.
func (r *panickingRegistry) Inputs(string, func(string, string) (quota.PoolUsage, quota.PoolUsage)) []scheduler.Input {
	panic("nil map in admission")
}

// panickingRenewals is a memory store whose renewals panic.
type panickingRenewals struct {
	*memory.Executions
}

// RenewDispatch panics.
func (s *panickingRenewals) RenewDispatch(context.Context, execution.ID, execution.Lease, time.Time) (time.Time, error) {
	panic("renewal")
}

// dispatchOf reads back what the registry's store holds for dispatch id.
func dispatchOf(t *testing.T, reg *fakeRegistry, id execution.ID) *execution.Dispatch {
	t.Helper()

	d, err := reg.executions.GetDispatch(t.Context(), id)
	if err != nil {
		t.Fatalf("GetDispatch(%s) = %v", id, err)
	}

	return d
}

// -------------------------------------------------------------------------
// RUNNERS AND RUNS
// -------------------------------------------------------------------------

// A runner that panics answers its task with an internal failure, which is
// not rerouted, and records its execution failed as internal.
func TestRunner_PanicFailsTheExecutionAsInternal(t *testing.T) {
	t.Parallel()

	p := &panickingProvider{&scriptedProvider{name: "a", finalState: execution.StateSucceeded}}

	reg := newRegistry(p.scriptedProvider)
	reg.providers["a"] = p
	d := newDispatcher(t, reg)

	o := begin(t, d)
	_, err := d.RunTask(t.Context(), o, containerTask(t, reroutingRetry(3)), nil, nil)

	if failure, ok := errors.AsType[*plugin.Error](err); !ok || failure.Class != plugin.ClassInternal ||
		!strings.Contains(err.Error(), "status response in an unexpected shape") {
		t.Fatalf("RunTask() = %v, want an internal failure naming the panic", err)
	}

	d.Finish(t.Context(), o, &JobOutcome{Job: origin.Job, Dispatch: o.Dispatch}, err)

	recs, err := reg.executions.DispatchExecutions(t.Context(), o.Dispatch)
	if err != nil || len(recs) != 1 {
		t.Fatalf("DispatchExecutions() = %v, %v; want the one execution", recs, err)
	}

	if rec := recs[0]; rec.State != execution.StateFailed || rec.Failure != string(plugin.ClassInternal) {
		t.Errorf("record = %s, %q; want failed as internal", rec.State, rec.Failure)
	}

	if p.submits != 1 {
		t.Errorf("submitted %d times, want once: an internal failure is not rerouted", p.submits)
	}

	if got := dispatchOf(t, reg, o.Dispatch); got.State != execution.DispatchUnanswered || got.Done.IsZero() {
		t.Errorf("dispatch = %s, done %v; want unanswered and closed", got.State, got.Done)
	}
}

// A panic in the run itself ends it unanswered with the panic as its reason,
// and the process carries on.
func TestRun_PanicEndsTheDispatchUnanswered(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	d := New(&panickingRegistry{reg}, reg.ledger, reg.executions, WithSleeper(noWait))

	o, err := d.Begin(t.Context(), origin, &job.Job{Name: origin.Job, Tasks: []job.Task{*containerTask(t, nil)}})
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}

	_, err = d.Run(t.Context(), o, &job.Job{Name: origin.Job, Tasks: []job.Task{*containerTask(t, nil)}}, nil)
	if failure, ok := errors.AsType[*plugin.Error](err); !ok || failure.Class != plugin.ClassInternal {
		t.Fatalf("Run() = %v, want an internal failure", err)
	}

	d.Finish(t.Context(), o, nil, err)

	got := dispatchOf(t, reg, o.Dispatch)
	if got.State != execution.DispatchUnanswered || !strings.Contains(got.Error, "nil map in admission") {
		t.Errorf("dispatch = %s %q, want unanswered with the panic", got.State, got.Error)
	}
}

// A panic in renewal loses the lease, which stops everything working under it.
func TestLease_PanicInRenewalLosesTheLease(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	d := New(reg, reg.ledger, &panickingRenewals{reg.executions}, WithLease(time.Minute, time.Millisecond))

	l := d.hold(t.Context(), testkit.NewID(t), execution.Lease{Owner: "server", Epoch: 1})
	defer l.release()

	select {
	case <-l.stopped():
	case <-time.After(5 * time.Second):
		t.Fatal("the lease was not lost after a panic in renewal")
	}

	if !l.taken() {
		t.Error("taken() = false after a panic in renewal")
	}
}

// -------------------------------------------------------------------------
// THE RESUME LIMIT
// -------------------------------------------------------------------------

// claimedAgain claims a lapsed dispatch times more times, as servers dying
// with it would, and returns it as the last claim left it.
func claimedAgain(t *testing.T, reg *fakeRegistry, times int) *execution.Dispatch {
	t.Helper()

	var claimed []*execution.Dispatch

	for i := range times {
		now := time.Now().Add(time.Duration(i+1) * 2 * LeaseTTL)

		var err error

		claimed, err = reg.executions.ClaimDispatches(t.Context(), "server", now, now.Add(LeaseTTL), nil)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimDispatches() = %v, %v; want the dispatch", claimed, err)
		}
	}

	return claimed[0]
}

// A dispatch resumed maxResumes times is ended unanswered on the next claim,
// without asking its provider anything, and its execution is left as it was.
func TestResume_AbandonsPastTheLimit(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	first, ids := abandon(t, reg, 1, execution.StateRunning)
	claimed := claimedAgain(t, reg, maxResumes)

	if claimed.Epoch != first.Epoch+maxResumes {
		t.Fatalf("epoch = %d, want %d", claimed.Epoch, first.Epoch+maxResumes)
	}

	state, reason, err := resumer(reg).Resume(t.Context(), claimed)
	if err != nil || state != execution.DispatchUnanswered || !strings.Contains(reason, "abandoned after 5 resumes") {
		t.Fatalf("Resume() = %s, %q, %v; want abandoned", state, reason, err)
	}

	if p.polls != 0 || p.results != 0 {
		t.Errorf("polled %d and fetched %d, want nothing asked", p.polls, p.results)
	}

	if rec := record(t, reg, ids[0]); rec.State != execution.StateRunning {
		t.Errorf("record = %s, want it left running", rec.State)
	}

	if got := dispatchOf(t, reg, claimed.ID); got.State != execution.DispatchUnanswered || got.Done.IsZero() {
		t.Errorf("dispatch = %s, done %v; want unanswered and closed", got.State, got.Done)
	}
}

// One resume short of the limit still runs.
func TestResume_RunsUpToTheLimit(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	abandon(t, reg, 1, execution.StateRunning)
	claimed := claimedAgain(t, reg, maxResumes-1)

	if state, _, err := resumer(reg).Resume(t.Context(), claimed); err != nil || state != execution.DispatchSucceeded {
		t.Errorf("Resume() = %s, %v; want it run to success", state, err)
	}

	if p.polls == 0 {
		t.Error("the provider was not asked about its execution")
	}
}

// A dispatch with a verdict, claimed past the limit, is closed under it.
func TestResume_AbandonedKeepsItsVerdict(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})

	abandon(t, reg, 1, execution.StateSucceeded)
	claimed := claimedAgain(t, reg, maxResumes)

	claimed.State, claimed.Ended = execution.DispatchSucceeded, time.Now()
	if err := reg.executions.FinishDispatch(t.Context(), claimed); err != nil {
		t.Fatalf("FinishDispatch() = %v", err)
	}

	state, _, err := resumer(reg).Resume(t.Context(), claimed)
	if err != nil || state != execution.DispatchSucceeded {
		t.Errorf("Resume() = %s, %v; want its verdict", state, err)
	}

	if got := dispatchOf(t, reg, claimed.ID); got.Done.IsZero() {
		t.Error("the dispatch was not closed")
	}
}
