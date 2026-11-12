// -------------------------------------------------------------------------------
// The Dispatcher
//
// Author: Alex Freidah
//
// Admission and ranking already decide where a task should go; this runs it
// there. The selection is not reimplemented here: the same Admit and Rank a
// plan calls are called again, so what `job plan` printed and what a run does
// cannot drift apart.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Registry is the part of the provider registry dispatch needs, declared here
// so a test supplies it without constructing providers.
type Registry interface {
	Inputs(namespace string, usage func(namespace, provider string) (total, share quota.PoolUsage)) []scheduler.Input
	Provider(name string) (plugin.Provider, bool)
	Recredential(ctx context.Context, name string) (bool, error)
}

// Ledger is the account every dispatch charges, declared here for the same
// reason as Registry. Reserve refuses with a *ledger.Refusal when the charge
// would pass a limit and pays is false; any other error means the charge was
// not made, and nothing is dispatched on it.
type Ledger interface {
	Reserve(ctx context.Context, id execution.ID, namespace, provider string, e quota.Execution, pays bool) error
	Settle(ctx context.Context, id execution.ID, namespace, provider string, actual quota.Execution) error
	Drop(ctx context.Context, id execution.ID) error
	Reserved(ctx context.Context, id execution.ID) (bool, error)
	Reap(ctx context.Context, resolve ledger.Resolver) (int, error)
	Refresh(ctx context.Context) error
	PoolUsage(namespace, provider string) (total, share quota.PoolUsage)
}

// Executions is where every run and attempt is recorded, declared here for the
// same reason as Registry. Update writes only if the stored record is still in
// from, failing with execution.ErrStale otherwise; FinishDispatch and
// RenewDispatch change only a dispatch still running under the named owner.
// Unreleased lists records not yet released and unchanged since before.
// AppendLog stores chunk seq of an execution's output, dropping the chunks
// before keepFrom and from seq on.
type Executions interface {
	CreateDispatch(ctx context.Context, d *execution.Dispatch) error
	FinishDispatch(ctx context.Context, d *execution.Dispatch) error
	RenewDispatch(ctx context.Context, id execution.ID, owner string, until time.Time) error
	DispatchExecutions(ctx context.Context, dispatch execution.ID) ([]*execution.Record, error)
	Create(ctx context.Context, r *execution.Record) error
	Update(ctx context.Context, r *execution.Record, from execution.State) error
	Unreleased(ctx context.Context, before time.Time) ([]*execution.Record, error)
	MarkReleased(ctx context.Context, id execution.ID, at time.Time) error
	AppendLog(ctx context.Context, id execution.ID, seq int64, data []byte, keepFrom int64) error
}

// Origin is what a task runs for: the job, the namespace it runs in, and the
// dispatch it belongs to. JobVersion is 0 for a job run from a file. Begin
// mints the dispatch ID when it is zero and holds the dispatch's lease.
type Origin struct {
	Namespace  string
	Job        string
	JobVersion int64
	Dispatch   execution.ID

	lease *lease
}

// Dispatcher runs jobs against the providers a registry holds. sleep is
// injected so tests do not wait out real backoff.
type Dispatcher struct {
	registry   Registry
	ledger     Ledger
	executions Executions
	owner      string        // who this process's dispatches are leased to
	leaseTTL   time.Duration // how long a lease runs unrenewed
	leaseRenew time.Duration // how often a held lease is renewed
	now        func() time.Time
	poll       Poll
	sleep      func(ctx context.Context, d time.Duration) error
	tiers      job.TierMode // the deployment's tier mode; empty is the default

	maxLogBytes int64 // how much of an execution's output is kept, the end of it
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithPoll replaces the polling schedule.
func WithPoll(p Poll) Option {
	return func(d *Dispatcher) { d.poll = p }
}

// WithSleeper replaces waiting, for tests.
func WithSleeper(sleep func(ctx context.Context, d time.Duration) error) Option {
	return func(d *Dispatcher) { d.sleep = sleep }
}

// WithOwner names who this dispatcher's dispatches are leased to, in place of
// ProcessOwner("vagabond").
func WithOwner(owner string) Option {
	return func(d *Dispatcher) { d.owner = owner }
}

// WithLease replaces how long a lease runs and how often it is renewed, in
// place of LeaseTTL and LeaseRenew. For tests that cannot wait a minute for one
// to lapse.
func WithLease(ttl, renew time.Duration) Option {
	return func(d *Dispatcher) { d.leaseTTL, d.leaseRenew = ttl, renew }
}

// WithTierMode sets how provider tiers order candidates for a job that does
// not say, in place of job.DefaultTierMode.
func WithTierMode(mode job.TierMode) Option {
	return func(d *Dispatcher) { d.tiers = mode }
}

// WithMaxLogBytes sets how much of an execution's output is kept, in place of
// execution.DefaultMaxLogBytes. Past it the front is dropped.
func WithMaxLogBytes(n int64) Option {
	return func(d *Dispatcher) { d.maxLogBytes = n }
}

// New builds a dispatcher over a registry, charging ledger for what it runs and
// recording every attempt in executions. Both are arguments rather than
// options, since a dispatcher that charges or records nothing is the failure
// each exists to prevent.
func New(registry Registry, ledger Ledger, executions Executions, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		registry:   registry,
		ledger:     ledger,
		executions: executions,
		owner:      ProcessOwner("vagabond"),
		leaseTTL:   LeaseTTL,
		leaseRenew: LeaseRenew,
		now:        time.Now,
		poll:       DefaultPoll,
		sleep:      sleepContext,

		maxLogBytes: execution.DefaultMaxLogBytes,
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// -------------------------------------------------------------------------
// RUNNING A JOB
// -------------------------------------------------------------------------

// Begin records a run of j as running, leased to this dispatcher's owner, and
// returns origin with its dispatch ID, minting one when it has none. A run that
// cannot be recorded must not start, so the error is the caller's to stop on.
func (d *Dispatcher) Begin(ctx context.Context, origin Origin, j *job.Job) (Origin, error) {
	origin.Job = j.Name

	if origin.Dispatch.IsZero() {
		id, err := execution.NewID()
		if err != nil {
			return origin, plugin.Internal(fmt.Errorf("generating a dispatch id: %w", err))
		}

		origin.Dispatch = id
	}

	now := d.now()

	err := d.executions.CreateDispatch(ctx, &execution.Dispatch{
		ID:         origin.Dispatch,
		Namespace:  origin.Namespace,
		Job:        j.Name,
		JobVersion: origin.JobVersion,
		Tasks:      len(j.Tasks),
		State:      execution.DispatchRunning,
		Owner:      d.owner,
		LeaseUntil: now.Add(d.leaseTTL),
		Created:    now,
	})
	if err != nil {
		return origin, fmt.Errorf("recording dispatch %s: %w", origin.Dispatch, err)
	}

	origin.lease = d.hold(ctx, origin.Dispatch)

	return origin, nil
}

// Finish records how a begun run ended: succeeded or failed on the tasks'
// verdicts, or unanswered with runErr, and releases the lease. Best effort,
// since the run is over, and nothing is written for a run taken over.
func (d *Dispatcher) Finish(ctx context.Context, origin Origin, outcome *JobOutcome, runErr error) {
	state, reason := execution.DispatchFailed, ""

	switch {
	case runErr != nil:
		state, reason = execution.DispatchUnanswered, runErr.Error()
	case outcome.Succeeded():
		state = execution.DispatchSucceeded
	}

	d.end(ctx, origin, state, reason)
}

// end releases origin's lease and records the dispatch as ended in state,
// unless another process took it over.
func (d *Dispatcher) end(ctx context.Context, origin Origin, state execution.DispatchState, reason string) {
	origin.lease.release()

	if origin.lease.taken() {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	_ = d.executions.FinishDispatch(ctx, &execution.Dispatch{
		ID:         origin.Dispatch,
		Namespace:  origin.Namespace,
		Job:        origin.Job,
		JobVersion: origin.JobVersion,
		State:      state,
		Error:      reason,
		Owner:      d.owner,
		Ended:      d.now(),
	})
}

// Run executes every task in a job, in declaration order, under the dispatch
// Begin recorded. Tasks read as steps, so the first that fails or cannot be
// dispatched stops the job; the outcome holds what ran even alongside an error.
func (d *Dispatcher) Run(
	ctx context.Context, origin Origin, j *job.Job, eval *hcl.EvalContext,
) (*JobOutcome, error) {
	if origin.Dispatch.IsZero() {
		return nil, plugin.Internal(errors.New("run of a dispatch that was never begun"))
	}

	origin.Job = j.Name
	outcome := &JobOutcome{Job: j.Name, Dispatch: origin.Dispatch}

	for i := range j.Tasks {
		task := &j.Tasks[i]

		if origin.lease.taken() {
			return outcome, ErrLeaseLost
		}

		result, err := d.RunTask(ctx, origin, task, j.Routing, eval)
		if result != nil {
			outcome.Tasks = append(outcome.Tasks, *result)
		}

		if err != nil {
			return outcome, fmt.Errorf("task %q: %w", task.Name, err)
		}

		if !result.Succeeded() {
			return outcome, nil
		}
	}

	return outcome, nil
}

// -------------------------------------------------------------------------
// RUNNING A TASK
// -------------------------------------------------------------------------

// RunTask admits, ranks, and executes one task, trying candidates in ranked
// order until one gives an answer of any kind. Usage is re-read first so
// admission sees earlier tasks' charges; a failed read keeps the last
// snapshot, since reserving is what enforces the limit.
func (d *Dispatcher) RunTask(
	ctx context.Context, origin Origin, task *job.Task, routing *job.Routing, eval *hcl.EvalContext,
) (*TaskOutcome, error) {
	req, diags := scheduler.NewRequest(task, routing, d.tiers, eval)
	if diags.HasErrors() {
		return nil, fmt.Errorf("building the request: %s", diags.Error())
	}

	_ = d.ledger.Refresh(ctx)

	admitted := scheduler.Admit(req, d.registry.Inputs(origin.Namespace, d.ledger.PoolUsage))

	outcome := &TaskOutcome{
		Task:       task.Name,
		Rejections: admitted.Rejections,
	}

	if len(admitted.Candidates) == 0 {
		return outcome, ErrNoCandidates
	}

	return d.attempt(ctx, origin, req, scheduler.Rank(req, admitted.Candidates), outcome)
}

// attempt works down the ranking until something answers or the budget runs
// out. The budget counts submissions, so a refused reservation spends none,
// and each candidate is tried at most once: retrying one mid-outage only
// delays the same failure.
func (d *Dispatcher) attempt(
	ctx context.Context, origin Origin, req *scheduler.Request, ranking scheduler.Ranking, outcome *TaskOutcome,
) (*TaskOutcome, error) {
	task := req.Task
	policy := retryPolicy(task)
	budget := policy.budget()

	var (
		lastErr  error
		tried    int
		previous execution.ID
	)

	for i := 0; i < len(ranking) && tried < budget; i++ {
		name := ranking[i].Provider

		provider, ok := d.registry.Provider(name)
		if !ok {
			return outcome, fmt.Errorf("%w: %s", ErrNoProvider, name)
		}

		run, err := d.prepare(ctx, origin, req, name, tried+1, previous)
		if refused(err) {
			outcome.Attempts = append(outcome.Attempts, Attempt{
				Provider: name, ID: run.rec.ID, Err: err, Refused: true,
			})
			lastErr = err

			continue
		}

		if err != nil {
			return outcome, err
		}

		id := run.rec.ID
		previous = id

		if err := d.pause(ctx, policy, tried); err != nil {
			return outcome, err
		}

		tried++

		ended, err := d.execute(ctx, provider, task, run, ranking[i].Members)
		d.charge(ctx, &run.rec, ended)

		outcome.Attempts = append(outcome.Attempts, Attempt{
			Provider: name,
			ID:       id,
			Err:      err,
		})

		if err == nil {
			outcome.Provider = name
			outcome.ID = id
			outcome.State = ended.state
			outcome.Result = ended.result

			return outcome, nil
		}

		// An internal fault fails identically on every provider
		if !plugin.Reroutable(err) {
			return outcome, err
		}

		lastErr = err
	}

	// Every candidate refused its reservation, so nothing was attempted
	if tried == 0 {
		return outcome, fmt.Errorf("%w: %w", ErrNoCandidates, lastErr)
	}

	return outcome, fmt.Errorf("%w: %w", ErrExhausted, lastErr)
}

// prepare mints an execution ID, reserves name's quota for it, and records it
// as pending; a refusal returns the *ledger.Refusal alongside the run. The ID
// is per attempt because it is the submission idempotency key, and reusing it
// would resume the attempt that just failed.
func (d *Dispatcher) prepare(
	ctx context.Context, origin Origin, req *scheduler.Request, name string, attempt int, previous execution.ID,
) (*tracked, error) {
	id, err := execution.NewID()
	if err != nil {
		return nil, plugin.Internal(fmt.Errorf("generating an execution id: %w", err))
	}

	now := d.now()

	run := &tracked{
		store: d.executions,
		now:   d.now,
		rec: execution.Record{
			Status:     execution.Status{ID: id, State: execution.StatePending, UpdatedAt: now},
			Namespace:  origin.Namespace,
			Job:        origin.Job,
			JobVersion: origin.JobVersion,
			Dispatch:   origin.Dispatch,
			Task:       req.Task.Name,
			Provider:   name,
			Attempt:    attempt,
			Previous:   previous,
			CPU:        req.Execution.CPU,
			Memory:     req.Execution.Memory,
		},
	}

	// A paying job passed admission's quota check, so its reservation is uncapped
	if err := d.ledger.Reserve(ctx, id, origin.Namespace, name, req.Execution, req.WillPay()); err != nil {
		if refused(err) {
			return run, err
		}

		return nil, fmt.Errorf("reserving quota on %s: %w", name, err)
	}

	if err := d.executions.Create(ctx, &run.rec); err != nil {
		return nil, fmt.Errorf("recording execution %s: %w", id, err)
	}

	return run, nil
}

// refused reports whether err is the ledger turning a reservation down.
func refused(err error) bool {
	var refusal *ledger.Refusal

	return errors.As(err, &refusal)
}

// pause waits out the backoff before every submission but the first.
func (d *Dispatcher) pause(ctx context.Context, p policy, tried int) error {
	if tried == 0 {
		return nil
	}

	return d.sleep(ctx, p.backoff(tried))
}

// charge settles what an execution cost: nothing for a submission the provider
// refused, what the platform billed when the result carries it, otherwise the
// declared shape over how long it ran. Without a result the worst-case
// reservation stands, since the run may still be out there.
func (d *Dispatcher) charge(ctx context.Context, rec *execution.Record, ended answer) {
	result := ended.result

	switch {
	case ended.refused:
		_ = d.ledger.Drop(ctx, rec.ID)

		return
	case result == nil:
		return
	}

	actual := quota.Execution{
		CPU:      rec.CPU,
		Memory:   rec.Memory,
		Duration: result.Duration,
	}

	if result.Billed != nil {
		actual = *result.Billed
	}

	_ = d.ledger.Settle(ctx, rec.ID, rec.Namespace, rec.Provider, actual)
}
