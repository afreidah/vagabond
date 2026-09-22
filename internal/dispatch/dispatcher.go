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
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
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
	// Inputs returns the capability and quota snapshot for every provider.
	Inputs() []scheduler.Input

	// Provider returns the plugin registered under a name.
	Provider(name string) (plugin.Provider, bool)
}

// Dispatcher runs jobs against the providers a registry holds.
type Dispatcher struct {
	registry Registry
	poll     Poll

	// sleep is injected because the alternative is a test suite that waits out
	// real backoff. One that returns immediately turns a two minute run into
	// microseconds without the polling logic knowing.
	sleep func(ctx context.Context, d time.Duration) error
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

// New builds a dispatcher over a registry.
func New(registry Registry, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		registry: registry,
		poll:     DefaultPoll,
		sleep:    sleepContext,
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
func (d *Dispatcher) Run(
	ctx context.Context, j *job.Job, eval *hcl.EvalContext,
) (*JobOutcome, error) {
	outcome := &JobOutcome{Job: j.Name}

	for i := range j.Tasks {
		task := &j.Tasks[i]

		result, err := d.RunTask(ctx, task, j.Routing, eval)
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
	ctx context.Context, task *job.Task, routing *job.Routing, eval *hcl.EvalContext,
) (*TaskOutcome, error) {
	req, diags := scheduler.NewRequest(task, routing, eval)
	if diags.HasErrors() {
		return nil, fmt.Errorf("building the request: %s", diags.Error())
	}

	admitted := scheduler.Admit(req, d.registry.Inputs())

	outcome := &TaskOutcome{
		Task:       task.Name,
		Rejections: admitted.Rejections,
	}

	if len(admitted.Candidates) == 0 {
		return outcome, ErrNoCandidates
	}

	return d.attempt(ctx, task, scheduler.Rank(req, admitted.Candidates), outcome)
}

// attempt works down the ranking until something answers or the budget runs
// out.
//
// The budget is the smaller of what the task allows and how many providers
// were admitted, because trying the same provider again for an outage it is
// still having is a slower way to reach the same place.
func (d *Dispatcher) attempt(
	ctx context.Context, task *job.Task, ranking scheduler.Ranking, outcome *TaskOutcome,
) (*TaskOutcome, error) {
	policy := retryPolicy(task)

	budget := policy.attempts

	// Without reroute a task gets one provider: the best one. Retrying in
	// place would spend capacity on a provider that just failed.
	if !policy.reroute {
		budget = 1
	}

	if budget > len(ranking) {
		budget = len(ranking)
	}

	var lastErr error

	for i := range budget {
		name := ranking[i].Provider

		if i > 0 {
			if err := d.sleep(ctx, policy.backoff(i)); err != nil {
				return outcome, err
			}
		}

		provider, ok := d.registry.Provider(name)
		if !ok {
			return outcome, fmt.Errorf("%w: %s", ErrNoProvider, name)
		}

		id, result, err := d.execute(ctx, provider, task)

		outcome.Attempts = append(outcome.Attempts, Attempt{
			Provider: name,
			ID:       id,
			Err:      err,
		})

		if err == nil {
			outcome.Provider = name
			outcome.ID = id
			outcome.Result = result

			return outcome, nil
		}

		// Our own bug reproduces on every provider, so sending it onward
		// spends capacity to fail identically.
		if !plugin.Reroutable(err) {
			return outcome, err
		}

		lastErr = err
	}

	return outcome, fmt.Errorf("%w: %w", ErrExhausted, lastErr)
}
