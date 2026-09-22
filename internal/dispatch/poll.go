// -------------------------------------------------------------------------------
// Submit, Watch, Collect
//
// Author: Alex Freidah
//
// One attempt against one provider, from submission to result. The polling
// schedule matters more than it looks: a measured Cloud Run job spent about
// 110 seconds provisioning before its container started, so a fixed short
// interval spends a dozen API calls learning nothing.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// SCHEDULE
// -------------------------------------------------------------------------

// Poll is how often a running execution is asked about.
//
// Backing off rather than a fixed interval, because the wait is dominated by
// provisioning the caller cannot influence, and because the provider is the
// one paying for the request.
type Poll struct {
	Initial time.Duration
	Max     time.Duration
}

// DefaultPoll starts responsive and settles into something cheap.
//
// Two seconds catches a function invocation that finished almost immediately;
// fifteen is a reasonable ceiling against a container that will be minutes.
var DefaultPoll = Poll{
	Initial: 2 * time.Second,
	Max:     15 * time.Second,
}

// interval returns how long to wait before the nth status call.
func (p Poll) interval(n int) time.Duration {
	wait := p.Initial
	for range n {
		wait *= 2

		if wait >= p.Max {
			return p.Max
		}
	}

	return wait
}

// sleepContext waits, or gives up when the caller does.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-timer.C:
		return nil
	}
}

// -------------------------------------------------------------------------
// ONE ATTEMPT
// -------------------------------------------------------------------------

// execute runs a task on one provider and returns what it produced.
//
// The execution id is generated here rather than by the caller because it is
// the submission idempotency key: a second attempt is a different execution,
// and reusing the id would ask the provider to resume the one that just
// failed.
func (d *Dispatcher) execute(
	ctx context.Context, provider plugin.Provider, task *job.Task,
) (execution.ID, *execution.Result, error) {
	id, err := execution.NewID()
	if err != nil {
		return id, nil, plugin.Internal(fmt.Errorf("generating an execution id: %w", err))
	}

	submission, err := provider.Submit(ctx, id, task)
	if err != nil {
		return id, nil, err
	}

	if err := submission.Validate(); err != nil {
		// A plugin describing its own submission incoherently is our bug to
		// fix, not a provider outage, so it is not sent onward.
		return id, nil, plugin.Internal(err)
	}

	// A function or worker finished inside Submit and has nothing to poll.
	if submission.Synchronous() {
		return id, submission.Result, nil
	}

	state, err := d.watch(ctx, provider, id)
	if err != nil {
		return id, nil, err
	}

	result, err := provider.Result(ctx, id)
	if err != nil {
		return id, nil, err
	}

	// A provider that reported cancelled produced no verdict about the work,
	// so it is not an answer even though a result came back.
	if state == execution.StateCancelled {
		return id, result, plugin.Infrastructure(
			fmt.Errorf("execution %s was cancelled by the provider", id))
	}

	return id, result, nil
}

// watch polls until the execution reaches a state it never leaves.
//
// Returns the terminal state rather than a status, because nothing here keeps
// a status: there is no ledger to write it to, and the provider is the record
// until there is.
func (d *Dispatcher) watch(
	ctx context.Context, provider plugin.Provider, id execution.ID,
) (execution.State, error) {
	for n := 0; ; n++ {
		if err := d.sleep(ctx, d.poll.interval(n)); err != nil {
			return "", err
		}

		status, err := provider.Status(ctx, id)
		if err != nil {
			return "", err
		}

		if status.State.Terminal() {
			return status.State, nil
		}
	}
}
