// -------------------------------------------------------------------------------
// Execution Record Tests
//
// Author: Alex Freidah
//
// What each attempt leaves in the execution store, read back after the run as
// a fresh process would.
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
)

func TestRecord_PolledRunEndsWithItsResult(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 2, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	r := record(t, reg, outcome.ID)

	if r.State != execution.StateSucceeded || r.Result == nil || r.Result.Duration != time.Second {
		t.Errorf("record = %+v, want succeeded with the scripted result", r)
	}

	if r.Namespace != ns || r.Job != origin.Job || r.Task != "test" || r.Provider != "a" || r.Attempt != 1 {
		t.Errorf("record identity = %+v", r)
	}

	if r.ProviderID == "" || r.StartedAt.IsZero() || r.EndedAt.IsZero() {
		t.Errorf("record = %+v, want the provider id and both timestamps", r)
	}

	if !r.Previous.IsZero() {
		t.Errorf("Previous = %s on a first attempt", r.Previous)
	}
}

// A function answers from Submit, and the record goes straight to its answer.
func TestRecord_SynchronousRun(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", synchronous: true}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if r := record(t, reg, outcome.ID); r.State != execution.StateSucceeded || r.Result == nil {
		t.Errorf("record = %+v, want succeeded with a result", r)
	}
}

// A submission refused before it ran failed outright, with its class kept.
func TestRecord_FailedSubmission(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}
	reg := newRegistry(p)

	outcome, _ := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)

	r := record(t, reg, outcome.Attempts[0].ID)

	if r.State != execution.StateFailed || r.Failure != string(plugin.ClassInfrastructure) {
		t.Errorf("record = %+v, want failed as infrastructure", r)
	}
}

// Submitted and then silent: nothing is known, so it is lost rather than
// failed.
func TestRecord_ProviderStopsAnswering(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", statusErr: plugin.Infrastructure(errors.New("503"))}
	reg := newRegistry(p)

	outcome, _ := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateLost {
		t.Errorf("state = %s, want lost", r.State)
	}
}

// The caller giving up cancels the work, and the record says so.
func TestRecord_CallerGivesUp(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	ctx, cancel := context.WithCancel(t.Context())

	d := over(reg, WithSleeper(func(context.Context, time.Duration) error {
		if p.polls >= 2 {
			cancel()

			return context.Canceled
		}

		return nil
	}))

	outcome, _ := d.RunTask(ctx, origin, containerTask(t, nil), nil, nil)

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateCancelled {
		t.Errorf("state = %s, want cancelled", r.State)
	}
}

// A rerouted task is two records, the second pointing at the first.
func TestRecord_RerouteLinksAttempts(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}
	second := &scriptedProvider{name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(first, second)

	outcome, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, reroutingRetry(2)), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	r := record(t, reg, outcome.ID)

	if r.Previous != outcome.Attempts[0].ID || r.Attempt != 2 {
		t.Errorf("record = %+v, want attempt 2 after %s", r, outcome.Attempts[0].ID)
	}
}

// Every task of one run shares the dispatch ID the outcome reports, and the
// run records the version it was given.
func TestRecord_TasksShareTheDispatch(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	j := &job.Job{Name: "ci", Tasks: []job.Task{*containerTask(t, nil), *containerTask(t, nil)}}
	j.Tasks[1].Name = "second"

	outcome, err := runJob(t, newDispatcher(t, reg), Origin{Namespace: ns, JobVersion: 4}, j)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if outcome.Dispatch.IsZero() || len(outcome.Tasks) != 2 {
		t.Fatalf("outcome = %+v, want a dispatch ID and two tasks", outcome)
	}

	for _, task := range outcome.Tasks {
		r := record(t, reg, task.ID)

		if r.Dispatch != outcome.Dispatch || r.JobVersion != 4 || r.Job != "ci" {
			t.Errorf("record = %+v, want dispatch %s at version 4", r, outcome.Dispatch)
		}
	}
}

// failingStore records nothing.
type failingStore struct{}

// CreateDispatch fails, as every write to a store that is down does.
func (failingStore) CreateDispatch(context.Context, *execution.Dispatch) error {
	return errors.New("store down")
}

// FinishDispatch fails, as every write to a store that is down does.
func (failingStore) FinishDispatch(context.Context, *execution.Dispatch) error {
	return errors.New("store down")
}

// RenewDispatch fails, as every write to a store that is down does.
func (failingStore) RenewDispatch(context.Context, execution.ID, string, time.Time) error {
	return errors.New("store down")
}

// DispatchExecutions fails, as every read from a store that is down does.
func (failingStore) DispatchExecutions(context.Context, execution.ID) ([]*execution.Record, error) {
	return nil, errors.New("store down")
}

// Create fails, so no attempt can be recorded before Submit.
func (failingStore) Create(context.Context, *execution.Record) error {
	return errors.New("store down")
}

// Update fails, as every write to a store that is down does.
func (failingStore) Update(context.Context, *execution.Record, execution.State) error {
	return errors.New("store down")
}

// -------------------------------------------------------------------------
// DISPATCH RECORDS
// -------------------------------------------------------------------------

// dispatchRecord reads back the record of one run.
func dispatchRecord(t *testing.T, reg *fakeRegistry, id execution.ID) *execution.Dispatch {
	t.Helper()

	d, err := reg.executions.GetDispatch(t.Context(), id)
	if err != nil {
		t.Fatalf("GetDispatch(%s) = %v", id, err)
	}

	return d
}

// How a run ended is recorded on its dispatch: succeeded, failed on a task's
// verdict, or unanswered with the reason.
func TestDispatchRecord_Outcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  *scriptedProvider
		want      execution.DispatchState
		wantError bool
	}{
		{
			name:     "succeeded",
			provider: &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded},
			want:     execution.DispatchSucceeded,
		},
		{
			name:     "task failed",
			provider: &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateFailed, exitCode: 1},
			want:     execution.DispatchFailed,
		},
		{
			name:      "no answer",
			provider:  &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))},
			want:      execution.DispatchUnanswered,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := newRegistry(tt.provider)
			j := &job.Job{Name: "ci", Tasks: []job.Task{*containerTask(t, nil)}}

			outcome, _ := runJob(t, newDispatcher(t, reg), origin, j)
			d := dispatchRecord(t, reg, outcome.Dispatch)

			if d.State != tt.want || (d.Error != "") != tt.wantError || d.Ended.IsZero() {
				t.Errorf("dispatch = %+v, want %s", d, tt.want)
			}
		})
	}
}

// A run refused before any task was submitted has no executions, and its
// dispatch record is what says why.
func TestDispatchRecord_RefusedRunHasOnlyItsRecord(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	j := &job.Job{Name: "ci", Tasks: []job.Task{*containerTask(t, nil)}}
	j.Tasks[0].Driver = job.DriverWorker

	outcome, err := runJob(t, newDispatcher(t, reg), origin, j)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("Run() = %v, want ErrNoCandidates", err)
	}

	d := dispatchRecord(t, reg, outcome.Dispatch)

	if d.State != execution.DispatchUnanswered || !strings.Contains(d.Error, "no provider") {
		t.Errorf("dispatch = %+v, want unanswered with the reason", d)
	}

	runs, _ := reg.executions.DispatchExecutions(t.Context(), outcome.Dispatch)
	if len(runs) != 0 {
		t.Errorf("a refused run recorded %d executions", len(runs))
	}
}

// Run refuses a dispatch Begin never recorded.
func TestRun_NeedsBegin(t *testing.T) {
	t.Parallel()

	j := &job.Job{Name: "ci", Tasks: []job.Task{*containerTask(t, nil)}}

	if _, err := newDispatcher(t, newRegistry()).Run(t.Context(), origin, j, nil); err == nil {
		t.Error("Run() started a dispatch that was never begun")
	}
}

// The record must exist before Submit, so one that cannot be written stops the
// dispatch.
func TestRecord_UnrecordableAttemptIsNotSubmitted(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	_, err := New(reg, reg.ledger, failingStore{}, WithSleeper(noWait)).
		RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)
	if err == nil {
		t.Fatal("dispatched with no record of the execution")
	}

	if p.submits != 0 {
		t.Errorf("submitted %d times with no record", p.submits)
	}
}
