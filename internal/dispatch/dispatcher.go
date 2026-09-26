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
	"io"
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

// Registry is the part of the provider registry dispatch needs.
//
// Declared here rather than imported, so a test supplies two methods instead
// of a configured registry, and so dispatch does not depend on how providers
// happen to be constructed.
type Registry interface {
	// Inputs is what admission reads for a job in namespace.
	Inputs(namespace string, usage func(namespace, provider string) (total, share quota.PoolUsage)) []scheduler.Input

	// Provider is the plugin registered under a name.
	Provider(name string) (plugin.Provider, bool)
}

// Ledger is the account every dispatch charges, declared here for the same
// reason as Registry.
//
// Reserve refuses with a *ledger.Refusal when the charge would pass a limit.
// Any other error means the charge could not be made at all, and nothing is
// dispatched on it.
type Ledger interface {
	Reserve(ctx context.Context, id execution.ID, namespace, provider string, e quota.Execution) error
	Settle(ctx context.Context, id execution.ID, namespace, provider string, actual quota.Execution) error
	Reap(ctx context.Context, resolve ledger.Resolver) (int, error)
	PoolUsage(namespace, provider string) (total, share quota.PoolUsage)
}

// Executions is where every attempt is recorded, declared here for the same
// reason as Registry.
//
// Update writes the record only if the stored one is still in from, and fails
// with execution.ErrStale otherwise.
type Executions interface {
	Create(ctx context.Context, r *execution.Record) error
	Update(ctx context.Context, r *execution.Record, from execution.State) error
}

// Origin is what a task runs for: the job, the namespace it runs in, and the
// dispatch it belongs to. JobVersion is 0 for a job run from a file. Run mints
// the dispatch ID when it is zero.
type Origin struct {
	Namespace  string
	Job        string
	JobVersion int64
	Dispatch   execution.ID
}

// Dispatcher runs jobs against the providers a registry holds.
//
// sleep is injected because the alternative is a test suite that waits out real
// backoff. One that returns immediately turns a two minute run into
// microseconds without the polling logic knowing it was not real.
type Dispatcher struct {
	registry   Registry
	ledger     Ledger
	executions Executions
	now        func() time.Time
	poll       Poll
	linger     time.Duration // how long to keep a stream open past the last poll
	logs       io.Writer     // nil means nobody is watching, and nothing is streamed
	progress   func(Event)   // nil means nobody is listening
	sleep      func(ctx context.Context, d time.Duration) error
}

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

// WithLogs streams a running execution's output to w, where the provider can.
func WithLogs(w io.Writer) Option {
	return func(d *Dispatcher) { d.logs = writerOrNil(w) }
}

// WithLinger replaces how long a stream is held open past the last poll.
func WithLinger(d time.Duration) Option {
	return func(disp *Dispatcher) { disp.linger = d }
}

// WithProgress reports state changes as they happen.
func WithProgress(fn func(Event)) Option {
	return func(d *Dispatcher) { d.progress = fn }
}

// New builds a dispatcher over a registry, charging ledger for what it runs and
// recording every attempt in executions.
//
// Both are arguments rather than options: a dispatcher that charges nothing or
// records nothing is the failure each exists to prevent.
func New(registry Registry, ledger Ledger, executions Executions, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		registry:   registry,
		ledger:     ledger,
		executions: executions,
		now:        time.Now,
		poll:       DefaultPoll,
		linger:     DefaultLinger,
		sleep:      sleepContext,
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// -------------------------------------------------------------------------
// RUNNING A JOB
// -------------------------------------------------------------------------

// Run executes every task in a job, in declaration order.
//
// Sequential and fail-fast. The specification has no dependency graph, so a
// job's tasks read as steps, and running step two after step one failed is
// work nobody asked for. A task that produced a non-zero exit code stops the
// job exactly as a task that could not be dispatched does: both mean the thing
// the job describes did not happen.
//
// The outcome is populated even when the error is non-nil, so a caller can
// report what did run before the failure.
//
// origin names the namespace the caller resolved, whose share of each provider
// is charged alongside the provider's total, and the job version when the job
// is registered.
func (d *Dispatcher) Run(
	ctx context.Context, origin Origin, j *job.Job, eval *hcl.EvalContext,
) (*JobOutcome, error) {
	origin.Job = j.Name

	if origin.Dispatch.IsZero() {
		id, err := execution.NewID()
		if err != nil {
			return nil, plugin.Internal(fmt.Errorf("generating a dispatch id: %w", err))
		}

		origin.Dispatch = id
	}

	outcome := &JobOutcome{Job: j.Name, Dispatch: origin.Dispatch}

	for i := range j.Tasks {
		task := &j.Tasks[i]

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

// RunTask admits, ranks, and executes one task.
//
// Candidates are tried in ranked order. A provider that fails to give an
// answer is abandoned and the next is tried, subject to the task's retry
// policy; a provider that gives one, of any kind, ends the task.
func (d *Dispatcher) RunTask(
	ctx context.Context, origin Origin, task *job.Task, routing *job.Routing, eval *hcl.EvalContext,
) (*TaskOutcome, error) {
	req, diags := scheduler.NewRequest(task, routing, eval)
	if diags.HasErrors() {
		return nil, fmt.Errorf("building the request: %s", diags.Error())
	}

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
// out.
//
// The budget counts submissions. A provider whose ledger refuses the
// reservation is passed over without spending one, because nothing was sent
// and there is no outage to back off from. Trying the same provider again for
// an outage it is still having is a slower way to reach the same place, so
// each candidate is tried at most once.
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

		result, streamed, err := d.execute(ctx, provider, task, run)
		d.charge(ctx, id, origin.Namespace, name, req.Execution, result)

		outcome.Attempts = append(outcome.Attempts, Attempt{
			Provider: name,
			ID:       id,
			Err:      err,
		})

		if err == nil {
			outcome.Provider = name
			outcome.ID = id
			outcome.Result = result
			outcome.Streamed = streamed

			return outcome, nil
		}

		// Our own bug reproduces on every provider, so sending it onward
		// spends capacity to fail identically.
		if !plugin.Reroutable(err) {
			return outcome, err
		}

		lastErr = err
	}

	// Every admitted provider refused the reservation, so nothing was
	// attempted: the quota admission saw was spent before dispatch got there.
	if tried == 0 {
		return outcome, fmt.Errorf("%w: %w", ErrNoCandidates, lastErr)
	}

	return outcome, fmt.Errorf("%w: %w", ErrExhausted, lastErr)
}

// prepare mints an execution ID, charges it against name's quota, and records
// it as pending. A refusal comes back as the *ledger.Refusal with the run
// carrying its ID; any other failure means nothing may be dispatched.
//
// The ID is minted per attempt because it is the submission idempotency key:
// reusing it would ask the provider to resume the one that just failed. It is
// recorded before Submit for the same reason, so it exists durably first.
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
		},
	}

	if err := d.ledger.Reserve(ctx, id, origin.Namespace, name, req.Execution); err != nil {
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

// charge settles what a run cost, from its result.
//
// Only a result says what the run cost. Without one the reservation stands:
// the run may be out there still, and charging its declared worst case is the
// over-count this errs toward. A result carrying what the platform billed is
// charged at that; otherwise at the declared shape over how long it ran.
func (d *Dispatcher) charge(
	ctx context.Context, id execution.ID, namespace, name string, declared quota.Execution, result *execution.Result,
) {
	if result == nil {
		return
	}

	actual := quota.Execution{
		CPU:      declared.CPU,
		Memory:   declared.Memory,
		Duration: result.Duration,
	}

	if result.Billed != nil {
		actual = *result.Billed
	}

	_ = d.ledger.Settle(ctx, id, namespace, name, actual)
}
