// -------------------------------------------------------------------------------
// Submit, Watch, Collect
//
// Author: Alex Freidah
//
// One attempt against one provider, from submission to result. Status is
// polled on a backoff because a container can spend minutes provisioning, and
// a fixed short interval spends API calls learning nothing.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// SCHEDULE
// -------------------------------------------------------------------------

// Poll is how often a running execution is asked about: from Initial,
// doubling up to Max.
type Poll struct {
	Initial time.Duration
	Max     time.Duration
}

// Bounds on the two cleanup calls, both made on their own context because the
// caller's may already be cancelled.
const (
	abandonTimeout = 30 * time.Second
	releaseTimeout = 30 * time.Second
)

// DefaultPoll starts at two seconds, for an invocation that finishes almost at
// once, and settles at fifteen for a container that runs for minutes.
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

// submit hands the task to provider: to the members admission passed, for a
// provider made of members, or plainly otherwise. The execution ID is the
// submission's idempotency key, so retrying it on a refreshed credential is
// safe.
func (d *Dispatcher) submit(
	ctx context.Context, provider plugin.Provider, id execution.ID, task *job.Task, members []string,
) (plugin.Submission, error) {
	return authorized(ctx, d, provider, func() (plugin.Submission, error) {
		if ms, ok := provider.(plugin.MemberSubmitter); ok && len(members) > 0 {
			return ms.SubmitTo(ctx, id, task, members)
		}

		return provider.Submit(ctx, id, task)
	})
}

// answer is how an execution ended: the provider's terminal state, which is
// the verdict, and what the task produced. refused is a submission the
// provider turned down, so nothing ran and nothing is owed.
type answer struct {
	state   execution.State
	result  *execution.Result
	refused bool
}

// execute runs a task on one provider under an id the caller reserved quota
// for, and returns how it ended. members are the provider's members admission
// passed, empty for a provider without any.
func (d *Dispatcher) execute(
	ctx context.Context, provider plugin.Provider, task *job.Task, run *tracked, members []string,
) (answer, error) {
	id := run.rec.ID

	submission, err := d.submit(ctx, provider, id, task, members)
	if err != nil {
		run.failed(ctx, err)

		return answer{refused: turnedDown(err)}, err
	}

	// An incoherent submission is a plugin bug, not an outage, so no reroute
	if invalid := submission.Validate(); invalid != nil {
		err = plugin.Internal(invalid)
		run.failed(ctx, err)

		return answer{}, err
	}

	run.submitted(ctx, &submission)

	// A function or worker finished inside Submit and has nothing to poll.
	if submission.Synchronous() {
		d.release(provider, id)

		return answer{state: submission.State, result: submission.Result}, nil
	}

	return d.follow(ctx, provider, run)
}

// turnedDown reports whether a Submit error means nothing ran: only one
// classified internal, Vagabond's request refused or never sent, says so. An
// outage, or an error nobody classified, may have left a run behind.
func turnedDown(err error) bool {
	classified, ok := errors.AsType[*plugin.Error](err)

	return ok && classified.Class == plugin.ClassInternal
}

// follow watches a submitted execution to its end and collects its result.
// Shared by a fresh submission and one resumed after a restart, which differ
// only in how they got here.
func (d *Dispatcher) follow(ctx context.Context, provider plugin.Provider, run *tracked) (answer, error) {
	id := run.rec.ID

	state, err := d.watch(ctx, provider, run)
	if err != nil {
		// The caller gave up, so stop the work before it keeps billing
		if ctx.Err() != nil {
			d.abandon(provider, id)
			run.to(ctx, execution.StateCancelled)
		} else {
			run.failed(ctx, err)
		}

		return answer{}, err
	}

	result, err := authorized(ctx, d, provider, func() (*execution.Result, error) {
		return provider.Result(ctx, id)
	})
	run.finish(ctx, state, result)

	if err != nil {
		return answer{}, err
	}

	// After the result, because releasing may destroy what it reads.
	d.release(provider, id)

	ended := answer{state: state, result: result}

	// Cancelled by the provider is no verdict, even with a result
	if state == execution.StateCancelled {
		return ended, plugin.Infrastructure(fmt.Errorf("execution %s was cancelled by the provider", id))
	}

	return ended, nil
}

// release frees what the provider left behind, if anything, and records that
// it was done. Best effort on its own context; a failure leaves the record
// unreleased for the release loop to retry.
func (d *Dispatcher) release(provider plugin.Provider, id execution.ID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()),
		releaseTimeout)
	defer cancel()

	if releaser, ok := provider.(plugin.Releaser); ok {
		err := authorizedErr(ctx, d, provider, func() error { return releaser.Release(ctx, id) })
		if err != nil && !errors.Is(err, plugin.ErrUnknownExecution) {
			return
		}
	}

	_ = d.executions.MarkReleased(ctx, id, d.now())
}

// abandon cancels an execution the caller stopped waiting for, best effort and
// on its own context since the caller's is cancelled.
func (d *Dispatcher) abandon(provider plugin.Provider, id execution.ID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()),
		abandonTimeout)
	defer cancel()

	_ = authorizedErr(ctx, d, provider, func() error { return provider.Cancel(ctx, id) })
}

// watch polls until the execution reaches a terminal state, recording each
// state on the way. The terminal one is left for the caller to record with its
// result.
func (d *Dispatcher) watch(ctx context.Context, provider plugin.Provider, run *tracked) (execution.State, error) {
	for n := 0; ; n++ {
		if err := d.sleep(ctx, d.poll.interval(n)); err != nil {
			return "", err
		}

		status, err := d.status(ctx, provider, run.rec.ID)
		if err != nil {
			return "", err
		}

		if status.State.Terminal() {
			return status.State, nil
		}

		// Writes only on a change, not on every poll
		run.to(ctx, status.State)
	}
}
