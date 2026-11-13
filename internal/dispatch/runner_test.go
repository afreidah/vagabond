// -------------------------------------------------------------------------------
// Execution Runner Tests
//
// Author: Alex Freidah
//
// What each attempt leaves in the execution store, read back after the run as
// a fresh process would; what its provider is asked to release; and how a
// runner behaves when the store fails, the lease moves, or a cancel arrives.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/backoff"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/state"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// releasingProvider leaves a resource behind, as Cloud Run does, and fails
// its first failures releases.
type releasingProvider struct {
	scriptedProvider

	failures      int
	releases      int
	releasedAfter int   // how many Result calls had happened by then
	releaseErr    error // what a failing Release returns
}

// Release counts the call, failing while failures remain.
func (p *releasingProvider) Release(context.Context, execution.ID) error {
	p.releases++
	p.releasedAfter = p.results

	if p.releases <= p.failures {
		return p.releaseErr
	}

	return nil
}

// releasing registers a releasing provider named "a" that ends in state after
// polls Status calls.
func releasing(state execution.State, polls int) (*releasingProvider, *fakeRegistry) {
	p := &releasingProvider{name: "a", pollsToFinish: polls, finalState: state}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	return p, reg
}

// answeringProvider answers Status from answers, one per call, then as the
// scripted provider does. forgetOnCancel makes it lose all record of an
// execution it cancels, as a platform deleting the run does.
type answeringProvider struct {
	scriptedProvider

	answers        []statusAnswer
	forgetOnCancel bool
	forgot         bool
}

// statusAnswer is one Status reply: a state, or the error the call fails
// with.
type statusAnswer struct {
	state execution.State
	err   error
}

// Status gives the next scripted answer, or the scripted provider's once they
// run out; an execution it forgot is unknown.
func (p *answeringProvider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	switch {
	case p.forgot:
		p.polls++

		return execution.Status{}, plugin.ErrUnknownExecution
	case len(p.answers) == 0:
		return p.scriptedProvider.Status(ctx, id)
	}

	next := p.answers[0]
	p.answers = p.answers[1:]
	p.polls++

	return execution.Status{ID: id, State: next.state}, next.err
}

// Cancel counts the call, and forgets the execution when forgetOnCancel is
// set.
func (p *answeringProvider) Cancel(ctx context.Context, id execution.ID) error {
	p.forgot = p.forgetOnCancel

	return p.scriptedProvider.Cancel(ctx, id)
}

// answering registers an answering provider named "a" that gives answers,
// then ends in state.
func answering(state execution.State, answers ...statusAnswer) (*answeringProvider, *fakeRegistry) {
	p := &answeringProvider{name: "a", finalState: state, answers: answers}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	return p, reg
}

// flakyStore is the registry's memory store with calls that fail: the first
// failSaves saves, and every Create, AppendLog or DispatchExecutions when
// failCreate, failLogs or failReads is set, as an unreachable store fails
// them. onSave runs before each save that is let through, with the record
// about to be written.
type flakyStore struct {
	*memory.Executions

	failSaves  atomic.Int32
	failCreate bool
	failLogs   bool
	failReads  bool
	onSave     func(r *execution.Record)
}

// DispatchExecutions fails when failReads is set.
func (s *flakyStore) DispatchExecutions(ctx context.Context, dispatch execution.ID) ([]*execution.Record, error) {
	if s.failReads {
		return nil, fmt.Errorf("read: %w", state.ErrUnavailable)
	}

	return s.Executions.DispatchExecutions(ctx, dispatch)
}

// AppendLog fails when failLogs is set.
func (s *flakyStore) AppendLog(
	ctx context.Context, id execution.ID, seq int64, data []byte, keepFrom int64, lease execution.Lease,
) error {
	if s.failLogs {
		return fmt.Errorf("append: %w", state.ErrUnavailable)
	}

	return s.Executions.AppendLog(ctx, id, seq, data, keepFrom, lease)
}

// Create fails when failCreate is set.
func (s *flakyStore) Create(ctx context.Context, r *execution.Record, lease execution.Lease) error {
	if s.failCreate {
		return fmt.Errorf("create: %w", state.ErrUnavailable)
	}

	return s.Executions.Create(ctx, r, lease)
}

// Save fails while failSaves remain, then runs onSave and saves.
func (s *flakyStore) Save(ctx context.Context, r *execution.Record, lease execution.Lease) error {
	if s.failSaves.Add(-1) >= 0 {
		return fmt.Errorf("save: %w", state.ErrUnavailable)
	}

	if s.onSave != nil {
		s.onSave(r)
	}

	return s.Executions.Save(ctx, r, lease)
}

// flaky builds a dispatcher over reg whose store is a flakyStore.
func flaky(reg *fakeRegistry, opts ...Option) (*Dispatcher, *flakyStore) {
	store := &flakyStore{Executions: reg.executions}

	return New(reg, reg.ledger, store, append([]Option{WithSleeper(noWait)}, opts...)...), store
}

// cancelling is a sleeper that gives up once p has been polled twice,
// cancelling the run, as a caller interrupting it does.
func cancelling(p *scriptedProvider, cancel context.CancelFunc) Option {
	return WithSleeper(func(context.Context, time.Duration) error {
		if p.polls >= 2 {
			cancel()

			return context.Canceled
		}

		return nil
	})
}

// stepping returns a clock that moves on by step every time it is read, so a
// lost execution's grace period passes in a few polls.
func stepping(step time.Duration) func() time.Time {
	var (
		mu  sync.Mutex
		now = time.Now()
	)

	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		now = now.Add(step)

		return now
	}
}

// claimer is another server, which claims a dispatch from under its owner.
const claimer = "server:b:2"

// claimFrom has claimer take every dispatch in reg's store, as it would once
// their leases lapsed.
func claimFrom(t *testing.T, reg *fakeRegistry) {
	t.Helper()

	later := time.Now().Add(2 * LeaseTTL)

	if _, err := reg.executions.ClaimDispatches(context.Background(), claimer, later, later.Add(LeaseTTL), nil); err != nil {
		t.Errorf("ClaimDispatches() = %v", err)
	}
}

// hasVerdict reports whether the dispatch has its verdict. Safe off the
// test's goroutine: a failed read reads as not yet.
func hasVerdict(reg *fakeRegistry, id execution.ID) bool {
	d, err := reg.executions.GetDispatch(context.Background(), id)

	return err == nil && d.State != execution.DispatchRunning
}

// invalidProvider reports a submission that breaks its own rules: finished,
// with no result.
type invalidProvider struct {
	scriptedProvider
}

// Submit answers succeeded without the result a finished submission carries.
func (p *invalidProvider) Submit(context.Context, execution.ID, *job.Task) (plugin.Submission, error) {
	p.submits++

	return plugin.Submission{ProviderID: "a-1", State: execution.StateSucceeded}, nil
}

// -------------------------------------------------------------------------
// EXECUTION RECORDS
// -------------------------------------------------------------------------

// A polled run's record ends succeeded, with its result, identity, times,
// output and release.
func TestRecord_PolledRunEndsWithItsResult(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 2, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
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

	if r.ProviderID == "" || r.StartedAt.IsZero() || r.EndedAt.IsZero() || r.Timeout != 5*time.Minute {
		t.Errorf("record = %+v, want the provider id, both timestamps and the declared timeout", r)
	}

	if r.LogsAt.IsZero() || r.Released.IsZero() || !r.Previous.IsZero() {
		t.Errorf("record = %+v, want output captured, released, and no previous attempt", r)
	}
}

// A function answers from Submit, and the record goes straight to its answer.
func TestRecord_SynchronousRun(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", synchronous: true}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if r := record(t, reg, outcome.ID); r.State != execution.StateSucceeded || r.Result == nil {
		t.Errorf("record = %+v, want succeeded with a result", r)
	}
}

// A submission that got no answer failed outright, with its class kept and
// nothing left to collect.
func TestRecord_FailedSubmission(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}
	reg := newRegistry(p)

	outcome, _ := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)

	r := record(t, reg, outcome.Attempts[0].ID)

	if r.State != execution.StateFailed || r.Failure != string(plugin.ClassInfrastructure) {
		t.Errorf("record = %+v, want failed as infrastructure", r)
	}

	if r.LogsAt.IsZero() || r.Released.IsZero() {
		t.Errorf("record = %+v, want it collected and released", r)
	}
}

// A provider that stops answering makes the execution lost, which the task
// reroutes on at once, and failed once the grace period passes.
func TestRecord_LostExecutionFailsAfterTheGracePeriod(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", statusErr: plugin.Infrastructure(errors.New("503"))}
	reg := newRegistry(p)
	d := newDispatcher(t, reg)
	d.now = stepping(time.Hour)

	outcome, err := d.runTaskIn(t, containerTask(t, nil), nil, nil)
	if !plugin.Reroutable(err) {
		t.Fatalf("RunTask() = %v, want a reroutable failure", err)
	}

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateFailed || r.Released.IsZero() {
		t.Errorf("record = %+v, want failed and released after the grace period", r)
	}
}

// A Status failure after the provider itself reported the execution lost
// answers the task at once, so it reroutes rather than waiting out the grace
// period.
func TestRecord_SilenceAfterAReportedLossAnswers(t *testing.T) {
	t.Parallel()

	_, reg := answering(execution.StateSucceeded,
		statusAnswer{state: execution.StateLost},
		statusAnswer{err: plugin.Infrastructure(errors.New("503"))},
	)

	if _, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil); !plugin.Reroutable(err) {
		t.Errorf("RunTask() = %v, want the reroutable failure", err)
	}
}

// A lost execution that answers after all is collected and charged like any
// other: its result read, its loss cleared.
func TestRecord_LostExecutionThatAnswersIsCollected(t *testing.T) {
	t.Parallel()

	p, reg := answering(execution.StateSucceeded, statusAnswer{err: plugin.Infrastructure(errors.New("503"))})

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if !plugin.Reroutable(err) {
		t.Fatalf("RunTask() = %v, want the reroutable failure", err)
	}

	r := record(t, reg, outcome.Attempts[0].ID)
	if r.State != execution.StateSucceeded || r.Result == nil || r.Failure != "" || p.results != 1 {
		t.Errorf("record = %+v after %d result reads, want succeeded with its result and no failure", r, p.results)
	}

	// The scripted result reports one second.
	want := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: time.Second})

	if got := usageOf(reg, "a")["compute"]; got != want["compute"] {
		t.Errorf("compute = %d, want the result's %d", got, want["compute"])
	}
}

// An execution the provider no longer knows will never answer, so it fails at
// once, charged for the time it was seen running rather than as reserved.
func TestRecord_ForgottenExecutionFailsAtOnce(t *testing.T) {
	t.Parallel()

	p, reg := answering(execution.StateSucceeded,
		statusAnswer{state: execution.StateRunning},
		statusAnswer{err: plugin.Internal(fmt.Errorf("%w: job gone", plugin.ErrUnknownExecution))},
	)
	d := newDispatcher(t, reg)
	d.now = stepping(time.Minute)

	outcome, err := d.runTaskIn(t, containerTask(t, nil), nil, nil)
	if err == nil {
		t.Fatal("a forgotten execution reported success")
	}

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateFailed || p.polls != 2 {
		t.Errorf("state = %s after %d polls, want failed at the second", r.State, p.polls)
	}

	reserved := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: 5 * time.Minute})

	if got := usageOf(reg, "a")["compute"]; got == 0 || got >= reserved["compute"] {
		t.Errorf("compute = %d, want the time seen running: above 0, under the reserved %d", got, reserved["compute"])
	}
}

// An unknown the provider classifies infrastructure may pass, so it makes the
// execution lost rather than failed, and a later answer still counts.
func TestRecord_PassingUnknownIsLost(t *testing.T) {
	t.Parallel()

	_, reg := answering(execution.StateSucceeded,
		statusAnswer{err: plugin.Infrastructure(fmt.Errorf("%w: no execution yet", plugin.ErrUnknownExecution))},
	)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if !plugin.Reroutable(err) {
		t.Fatalf("RunTask() = %v, want the reroutable failure", err)
	}

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateSucceeded {
		t.Errorf("state = %s, want the later answer", r.State)
	}
}

// A submission that breaks its own rules is Vagabond's fault to report, so it
// is not rerouted, and ran nothing to charge.
func TestRecord_InvalidSubmission(t *testing.T) {
	t.Parallel()

	p := &invalidProvider{scriptedProvider{name: "a"}}
	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if err == nil || plugin.Reroutable(err) {
		t.Fatalf("RunTask() = %v, want a failure that is not rerouted", err)
	}

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateFailed || r.Failure != string(plugin.ClassInternal) {
		t.Errorf("record = %+v, want failed as internal", r)
	}

	if got := usageOf(reg, "a")["compute"]; got != 0 {
		t.Errorf("compute = %d, want nothing charged", got)
	}
}

// A result that cannot be read ends the attempt with that error; the record
// keeps the terminal state and the output, without a result.
func TestRecord_UnreadableResult(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded, resultErr: plugin.Infrastructure(errors.New("503")),
	}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if !plugin.Reroutable(err) {
		t.Fatalf("RunTask() = %v, want the reroutable failure", err)
	}

	r := record(t, reg, outcome.Attempts[0].ID)
	if r.State != execution.StateSucceeded || r.Result != nil || r.LogsAt.IsZero() || r.Released.IsZero() {
		t.Errorf("record = %+v, want succeeded with no result, collected and released", r)
	}
}

// A run the provider cancelled gave no verdict, whatever its result says, so
// the task may be rerouted.
func TestRecord_CancelledByTheProvider(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateCancelled}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if !plugin.Reroutable(err) {
		t.Fatalf("RunTask() = %v, want the reroutable failure", err)
	}

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateCancelled || r.Result == nil {
		t.Errorf("record = %+v, want cancelled with its result", r)
	}
}

// The caller giving up cancels the work, and the record says so.
func TestRecord_CallerGivesUp(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	ctx, cancel := context.WithCancel(t.Context())
	d := over(reg, cancelling(p, cancel))

	o := begin(t, d)
	outcome, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateCancelled || p.cancels != 1 {
		t.Errorf("state = %s after %d cancels, want cancelled once", r.State, p.cancels)
	}
}

// A rerouted task is two records, the second pointing at the first.
func TestRecord_RerouteLinksAttempts(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}
	second := &scriptedProvider{name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(first, second)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, reroutingRetry(2)), nil, nil)
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

// The record must exist before Submit, so one that cannot be written stops the
// dispatch.
func TestRecord_UnrecordableAttemptIsNotSubmitted(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	d, store := flaky(reg)
	store.failCreate = true

	if _, err := d.runTaskIn(t, containerTask(t, nil), nil, nil); !errors.Is(err, state.ErrUnavailable) {
		t.Fatalf("RunTask() = %v, want the store's failure", err)
	}

	if p.submits != 0 {
		t.Errorf("submitted %d times with no record", p.submits)
	}
}

// -------------------------------------------------------------------------
// RELEASE
// -------------------------------------------------------------------------

// What the provider left behind is released after the result is read, and the
// record says so.
func TestRelease_AfterTheResult(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateSucceeded, 1)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if p.releases != 1 || p.releasedAfter != 1 {
		t.Errorf("released %d times after %d results, want once after the result", p.releases, p.releasedAfter)
	}

	if r := record(t, reg, outcome.ID); r.Released.IsZero() {
		t.Error("the record does not say it was released")
	}
}

// A workload that failed on its own is released as a success is.
func TestRelease_OnAFailedWorkload(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateFailed, 1)
	p.exitCode = 1

	if _, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if p.releases != 1 {
		t.Errorf("released %d times, want once", p.releases)
	}
}

// A release that fails is made again until it succeeds, holding the dispatch
// open meanwhile.
func TestRelease_RetriedUntilItSucceeds(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateSucceeded, 1)
	p.failures, p.releaseErr = 2, plugin.Infrastructure(errors.New("node offline"))

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if r := record(t, reg, outcome.ID); p.releases != 3 || r.Released.IsZero() {
		t.Errorf("released %d times, released at %s; want the third to land", p.releases, r.Released)
	}

	if d := dispatchRecord(t, reg, record(t, reg, outcome.ID).Dispatch); d.Done.IsZero() {
		t.Error("the dispatch was not done once its release landed")
	}
}

// A provider that no longer knows the execution has nothing left to release.
func TestRelease_UnknownCountsAsReleased(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateSucceeded, 1)
	p.failures, p.releaseErr = 10, plugin.Internal(fmt.Errorf("%w: gone", plugin.ErrUnknownExecution))

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if r := record(t, reg, outcome.ID); p.releases != 1 || r.Released.IsZero() {
		t.Errorf("released %d times, released at %s; want marked after one", p.releases, r.Released)
	}
}

// A provider that leaves nothing behind is marked released without a call.
func TestRelease_NothingToRelease(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	outcome, err := newDispatcher(t, reg).runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if r := record(t, reg, outcome.ID); r.Released.IsZero() {
		t.Error("the record does not say it was released")
	}
}

// -------------------------------------------------------------------------
// STORE AND LEASE
// -------------------------------------------------------------------------

// A record is never written terminal without its output, so a client that
// sees an execution end finds the output there to read, however it ended.
func TestSave_TheEndIsWrittenWithTheOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func(cancel context.CancelFunc) (*fakeRegistry, []Option)
	}{
		{"polled", func(context.CancelFunc) (*fakeRegistry, []Option) {
			return newRegistry(&scriptedProvider{name: "a", pollsToFinish: 2, finalState: execution.StateSucceeded}), nil
		}},
		{"synchronous", func(context.CancelFunc) (*fakeRegistry, []Option) {
			return newRegistry(&scriptedProvider{name: "a", synchronous: true}), nil
		}},
		{"forgotten", func(context.CancelFunc) (*fakeRegistry, []Option) {
			_, reg := answering(execution.StateSucceeded,
				statusAnswer{state: execution.StateRunning},
				statusAnswer{err: plugin.Internal(fmt.Errorf("%w: gone", plugin.ErrUnknownExecution))},
			)

			return reg, nil
		}},
		{"cancelled", func(cancel context.CancelFunc) (*fakeRegistry, []Option) {
			p := &scriptedProvider{name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded}

			return newRegistry(p), []Option{cancelling(p, cancel)}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			reg, opts := tt.build(cancel)
			d, store := flaky(reg, opts...)

			var early atomic.Int32

			store.onSave = func(r *execution.Record) {
				if r.Terminal() && r.LogsAt.IsZero() {
					early.Add(1)
				}
			}

			o := begin(t, d)
			outcome, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
			d.Finish(t.Context(), o, &JobOutcome{Job: origin.Job, Dispatch: o.Dispatch, Tasks: []TaskOutcome{*outcome}}, err)

			if n := early.Load(); n != 0 {
				t.Errorf("%d writes ended the execution before its output was captured", n)
			}

			if r := record(t, reg, outcome.Attempts[0].ID); !r.Terminal() || r.LogsAt.IsZero() {
				t.Errorf("record = %+v, want it ended with its output captured", r)
			}
		})
	}
}

// Writes the store refuses while it is unreachable are made again until it
// takes them, so the record ends with its result.
func TestSave_RetriedThroughAnOutage(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 2, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	d, store := flaky(reg)
	store.failSaves.Store(3)

	outcome, err := d.runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if r := record(t, reg, outcome.ID); r.State != execution.StateSucceeded || r.Result == nil {
		t.Errorf("record = %+v, want succeeded with its result", r)
	}
}

// claimCase is a run another server claims from under its owner. The claim
// lands before the refused-th save, counting from one, and after the
// verdict when answered; setup shapes the provider, and releases are the
// Release calls made before the claim.
type claimCase struct {
	name     string
	refused  int32
	setup    func(p *releasingProvider, cancel context.CancelFunc) []Option
	answered bool
	releases int
}

// run makes the run, claims it where the case says, and checks the runner
// stopped there and left the dispatch to the claimer.
func (c claimCase) run(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	p, reg := releasing(execution.StateSucceeded, 1)

	var opts []Option
	if c.setup != nil {
		opts = c.setup(p, cancel)
	}

	d, store := flaky(reg, opts...)
	o := begin(t, d)

	var saves atomic.Int32

	store.onSave = func(*execution.Record) {
		if saves.Add(1) != c.refused {
			return
		}

		// A claim after the verdict leaves the dispatch ended but not done
		for c.answered && !hasVerdict(reg, o.Dispatch) {
			time.Sleep(time.Millisecond)
		}

		claimFrom(t, reg)
	}

	_, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, &JobOutcome{Job: origin.Job, Dispatch: o.Dispatch}, err)

	if c.answered == errors.Is(err, ErrLeaseLost) {
		t.Errorf("RunTask() = %v; answered before the claim: %t", err, c.answered)
	}

	if p.releases != c.releases || p.cancels != 0 {
		t.Errorf("released %d and cancelled %d times, want %d and none", p.releases, p.cancels, c.releases)
	}

	if rec := dispatchRecord(t, reg, o.Dispatch); rec.Owner != claimer || !rec.Done.IsZero() {
		t.Errorf("dispatch = %+v, want left open to the server that claimed it", rec)
	}
}

// A runner whose write is refused because another server claimed the
// dispatch stops there, whichever step it was in, acts on the provider no
// more, and leaves the dispatch open to the claimer.
func TestLease_ClaimStopsTheRunner(t *testing.T) {
	t.Parallel()

	tests := []claimCase{
		{name: "submitted", refused: 1},
		{name: "failed submission", refused: 1, setup: func(p *releasingProvider, _ context.CancelFunc) []Option {
			p.submitErr = plugin.Infrastructure(errors.New("503"))

			return nil
		}},
		{name: "lost", refused: 2, setup: func(p *releasingProvider, _ context.CancelFunc) []Option {
			p.statusErr = plugin.Infrastructure(errors.New("503"))

			return nil
		}},
		{name: "collected", refused: 2},
		{name: "released", refused: 3, answered: true, releases: 1},
		{name: "cancelled", refused: 3, setup: func(p *releasingProvider, cancel context.CancelFunc) []Option {
			p.pollsToFinish = 100

			return []Option{cancelling(&p.scriptedProvider, cancel)}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

// An owner whose renewal finds the dispatch claimed stops watching without
// cancelling the run, which is the claimer's now.
func TestLease_RenewalFindsTheClaim(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateSucceeded, 1<<30)

	var claimed atomic.Bool

	// The first wait claims the dispatch, then lasts until the renewal finds it
	d := over(reg, WithLease(time.Minute, 5*time.Millisecond), WithSleeper(func(ctx context.Context, _ time.Duration) error {
		if !claimed.CompareAndSwap(false, true) {
			return nil
		}

		claimFrom(t, reg)
		<-ctx.Done()

		return ctx.Err()
	}))

	o := begin(t, d)
	_, err := d.RunTask(t.Context(), o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	if !errors.Is(err, ErrLeaseLost) || p.cancels != 0 {
		t.Errorf("RunTask() = %v after %d cancels, want ErrLeaseLost and none", err, p.cancels)
	}
}

// An attempt whose record the store refuses, because another server claimed
// the dispatch, is never submitted, and the lease is given up.
func TestLease_ClaimRefusesTheNextAttempt(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)
	d := newDispatcher(t, reg)

	o := begin(t, d)
	claimFrom(t, reg)

	_, err := d.RunTask(t.Context(), o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	if !errors.Is(err, execution.ErrStale) || p.submits != 0 || !o.lease.taken() {
		t.Errorf("RunTask() = %v after %d submissions, lease taken %t; want ErrStale, none, taken",
			err, p.submits, o.lease.taken())
	}
}

// A cancel requested by a server that does not hold the dispatch reaches the
// owner through its renewal, which cancels the work.
func TestLease_CancelRequestStopsTheRun(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1 << 30, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	d := over(reg, WithLease(time.Minute, 5*time.Millisecond),
		WithSleeper(func(ctx context.Context, _ time.Duration) error { return backoff.Sleep(ctx, time.Millisecond) }))

	o := begin(t, d)

	if err := reg.executions.RequestCancel(t.Context(), o.Dispatch, time.Now()); err != nil {
		t.Fatalf("RequestCancel() = %v", err)
	}

	outcome, err := d.RunTask(t.Context(), o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	if !errors.Is(err, ErrCancelRequested) {
		t.Fatalf("RunTask() = %v, want ErrCancelRequested", err)
	}

	if r := record(t, reg, outcome.Attempts[0].ID); r.State != execution.StateCancelled || p.cancels != 1 {
		t.Errorf("state = %s after %d cancels, want cancelled once", r.State, p.cancels)
	}
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
// verdict, or unanswered with the reason, and done once released.
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

			if d.State != tt.want || (d.Error != "") != tt.wantError || d.Ended.IsZero() || d.Done.IsZero() {
				t.Errorf("dispatch = %+v, want %s, ended and done", d, tt.want)
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

	if d.State != execution.DispatchUnanswered || !strings.Contains(d.Error, "no provider") || d.Done.IsZero() {
		t.Errorf("dispatch = %+v, want unanswered with the reason, and done", d)
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
