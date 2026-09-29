// -------------------------------------------------------------------------------
// Lease and Resume Tests
//
// Author: Alex Freidah
//
// A dispatch whose owner died, set up in the memory store as it would be left,
// claimed and resumed; and an owner finding its dispatch taken over.
// -------------------------------------------------------------------------------

package dispatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// declared is the shape every abandoned execution here reserved.
var declared = quota.Execution{CPU: 1000, Memory: 512, Duration: 5 * time.Minute}

// abandon records a dispatch of tasks tasks whose owner died, with one
// execution per state given, each reserved on provider a. Returns the
// dispatch as a server claims it, and the executions in order.
func abandon(
	t *testing.T, reg *fakeRegistry, tasks int, states ...execution.State,
) (*execution.Dispatch, []execution.ID) {
	t.Helper()

	ctx := t.Context()
	lapsed := time.Now().Add(-time.Hour)

	d := &execution.Dispatch{
		ID: newTestID(t), Namespace: ns, Job: "ci", Tasks: tasks,
		State: execution.DispatchRunning, Owner: "cli:dead:1", LeaseUntil: lapsed, Created: lapsed,
	}

	if err := reg.executions.CreateDispatch(ctx, d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	ids := make([]execution.ID, 0, len(states))

	for i, state := range states {
		id := newTestID(t)
		ids = append(ids, id)

		if err := reg.ledger.Reserve(ctx, id, ns, "a", declared); err != nil {
			t.Fatalf("Reserve() = %v", err)
		}

		rec := &execution.Record{
			ID: id, State: state, UpdatedAt: lapsed,
			Namespace: ns, Job: "ci", Dispatch: d.ID, Task: string(rune('a' + i)),
			Provider: "a", Attempt: 1, CPU: declared.CPU, Memory: declared.Memory,
		}

		if state.Terminal() {
			rec.Result = &execution.Result{ID: id, ExitCode: new(0), Duration: time.Second}
		}

		if err := reg.executions.Create(ctx, rec); err != nil {
			t.Fatalf("Create() = %v", err)
		}
	}

	claimed, err := reg.executions.ClaimDispatches(ctx, "server", time.Now(), time.Now().Add(LeaseTTL), nil)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDispatches() = %v, %v; want the abandoned one", claimed, err)
	}

	return claimed[0], ids
}

// newTestID mints an execution ID.
func newTestID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// resumer builds a dispatcher that never waits, leased as the claiming
// server.
func resumer(reg *fakeRegistry) *Dispatcher {
	return over(reg, WithSleeper(noWait), WithOwner("server"))
}

// -------------------------------------------------------------------------
// RESUME
// -------------------------------------------------------------------------

// The execution in flight is followed to its result and settled, and a job
// whose last task it was ends succeeded.
func TestResume_FollowsTheExecutionInFlight(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)
	claimed, ids := abandon(t, reg, 1, execution.StateRunning)

	state, reason, err := resumer(reg).Resume(t.Context(), claimed)
	if err != nil || state != execution.DispatchSucceeded {
		t.Fatalf("Resume() = %s, %q, %v; want succeeded", state, reason, err)
	}

	if r := record(t, reg, ids[0]); r.State != execution.StateSucceeded || r.Result == nil {
		t.Errorf("execution = %s with result %v, want succeeded with its result", r.State, r.Result)
	}

	want := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: time.Second})
	if got := usageOf(reg, "a")["cpu"]; got != want["cpu"] {
		t.Errorf("cpu = %d, want the settled %d", got, want["cpu"])
	}

	if d := dispatchRecord(t, reg, claimed.ID); d.State != execution.DispatchSucceeded || d.Owner != "server" {
		t.Errorf("dispatch = %+v, want succeeded under the server", d)
	}
}

// Tasks the job had not reached are not run, so a passing task that was not
// the last leaves the dispatch unanswered.
func TestResume_EarlierTaskLeavesTheRestUnrun(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)
	claimed, _ := abandon(t, reg, 2, execution.StateRunning)

	state, reason, err := resumer(reg).Resume(t.Context(), claimed)
	if err != nil || state != execution.DispatchUnanswered || !strings.Contains(reason, "1 of 2") {
		t.Errorf("Resume() = %s, %q, %v; want unanswered after 1 of 2", state, reason, err)
	}

	if p.submits != 0 {
		t.Errorf("submitted %d times resuming", p.submits)
	}
}

// An execution recorded but never submitted is unknown to the provider, so it
// failed, and its reservation is left for the reaper.
func TestResume_UnsubmittedExecutionFails(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", statusErr: plugin.ErrUnknownExecution}
	reg := newRegistry(p)
	claimed, ids := abandon(t, reg, 1, execution.StatePending)

	state, _, err := resumer(reg).Resume(t.Context(), claimed)
	if err != nil || state != execution.DispatchUnanswered {
		t.Errorf("Resume() = %s, %v; want unanswered", state, err)
	}

	if r := record(t, reg, ids[0]); r.State != execution.StateFailed {
		t.Errorf("execution = %s, want failed", r.State)
	}
}

// Finished executions are not asked about again.
func TestResume_LeavesFinishedExecutionsAlone(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)
	claimed, _ := abandon(t, reg, 2, execution.StateSucceeded, execution.StateRunning)

	state, _, err := resumer(reg).Resume(t.Context(), claimed)
	if err != nil || state != execution.DispatchSucceeded {
		t.Errorf("Resume() = %s, %v; want succeeded", state, err)
	}

	if p.results != 1 {
		t.Errorf("fetched %d results, want only the one in flight", p.results)
	}
}

// How a resumed dispatch ended, from its executions.
func TestEnded(t *testing.T) {
	t.Parallel()

	passed := &execution.Result{ExitCode: new(0)}
	exited := &execution.Result{ExitCode: new(1)}

	rec := func(task string, state execution.State, result *execution.Result, failure string) *execution.Record {
		return &execution.Record{
			State: state, Task: task, Result: result, Failure: failure,
		}
	}

	tests := []struct {
		name    string
		tasks   int
		records []*execution.Record
		want    execution.DispatchState
	}{
		{"nothing submitted", 1, nil, execution.DispatchUnanswered},
		{"last task passed", 2, []*execution.Record{
			rec("a", execution.StateSucceeded, passed, ""),
			rec("b", execution.StateSucceeded, passed, ""),
		}, execution.DispatchSucceeded},
		{"earlier task passed", 2, []*execution.Record{
			rec("a", execution.StateSucceeded, passed, ""),
		}, execution.DispatchUnanswered},
		{"task exited non-zero", 1, []*execution.Record{
			rec("a", execution.StateFailed, exited, ""),
		}, execution.DispatchFailed},
		{"task failed with no exit code", 1, []*execution.Record{
			rec("a", execution.StateFailed, &execution.Result{}, ""),
		}, execution.DispatchFailed},
		{"attempt failed without an answer", 1, []*execution.Record{
			rec("a", execution.StateFailed, nil, string(plugin.ClassInfrastructure)),
		}, execution.DispatchUnanswered},
		{"lost", 1, []*execution.Record{
			rec("a", execution.StateLost, nil, string(plugin.ClassInfrastructure)),
		}, execution.DispatchUnanswered},
		{"cancelled by the provider", 1, []*execution.Record{
			rec("a", execution.StateCancelled, exited, ""),
		}, execution.DispatchUnanswered},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, _ := ended(tt.tasks, tt.records); got != tt.want {
				t.Errorf("ended() = %s, want %s", got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// LEASES
// -------------------------------------------------------------------------

// An owner whose dispatch was taken over starts no more tasks and leaves the
// ending to the new owner.
func TestLease_TakenOverRunStops(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)
	d := over(reg, WithSleeper(noWait), WithOwner("cli"))
	d.now = func() time.Time { return time.Now().Add(-time.Hour) }

	j := &job.Job{Name: "ci", Tasks: []job.Task{*containerTask(t, nil)}}

	o, err := d.Begin(t.Context(), origin, j)
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}

	if _, err := reg.executions.ClaimDispatches(t.Context(), "server", time.Now(), time.Now().Add(LeaseTTL), nil); err != nil {
		t.Fatalf("ClaimDispatches() = %v", err)
	}

	if err := d.renew(t.Context(), o.Dispatch); !errors.Is(err, execution.ErrStale) {
		t.Fatalf("renew() = %v, want ErrStale once taken over", err)
	}

	o.lease.lost.Store(true)

	outcome, err := d.Run(t.Context(), o, j, nil)
	d.Finish(t.Context(), o, outcome, err)

	if !errors.Is(err, ErrLeaseLost) || p.submits != 0 {
		t.Errorf("Run() = %v after %d submits, want ErrLeaseLost before any", err, p.submits)
	}

	if rec := dispatchRecord(t, reg, o.Dispatch); rec.State != execution.DispatchRunning || rec.Owner != "server" {
		t.Errorf("dispatch = %+v, want still running under the server", rec)
	}
}

// Begin leases the dispatch to its owner and records how many tasks it has.
func TestLease_BeginLeasesToTheOwner(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	d := over(reg, WithOwner("cli"))
	j := &job.Job{Name: "ci", Tasks: []job.Task{*containerTask(t, nil), *containerTask(t, nil)}}

	o, err := d.Begin(t.Context(), origin, j)
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}

	defer o.lease.release()

	rec := dispatchRecord(t, reg, o.Dispatch)
	if rec.Owner != "cli" || rec.Tasks != 2 || !rec.LeaseUntil.After(time.Now()) {
		t.Errorf("dispatch = %+v, want leased to cli with 2 tasks", rec)
	}
}
