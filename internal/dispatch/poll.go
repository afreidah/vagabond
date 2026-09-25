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

// Bounds on the two cleanup calls, both made on their own context because the
// caller's may already be cancelled.
const (
	abandonTimeout = 30 * time.Second
	releaseTimeout = 30 * time.Second
)

// DefaultLinger is how long a log stream is held open after the execution
// ends. Cloud Logging runs seconds behind the container, so a job that
// finishes promptly finishes before its own last lines are readable.
const DefaultLinger = 5 * time.Second

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

// execute runs a task on one provider under an id the caller reserved quota
// for, and returns what it produced.
func (d *Dispatcher) execute(
	ctx context.Context, provider plugin.Provider, task *job.Task, run *tracked,
) (*execution.Result, bool, error) {
	var streamed bool

	id := run.rec.ID

	event := Event{
		Task: task.Name, Provider: provider.Name(), ID: id, Attempt: run.rec.Attempt,
	}

	submission, err := provider.Submit(ctx, id, task)
	if err == nil {
		// A plugin describing its own submission incoherently is our bug to
		// fix, not a provider outage, so it is not sent onward.
		if invalid := submission.Validate(); invalid != nil {
			err = plugin.Internal(invalid)
		}
	}

	if err != nil {
		run.failed(ctx, err)

		return nil, streamed, err
	}

	run.submitted(ctx, &submission)

	event.State = submission.State
	d.report(event)

	// A function or worker finished inside Submit and has nothing to poll.
	if submission.Synchronous() {
		d.release(provider, id)

		return submission.Result, streamed, nil
	}

	stream := d.startStream(ctx, provider, id)
	defer stream.stop()

	state, err := d.watch(ctx, provider, run, event)

	// Settled before anything else prints, so the tail of a build does not land
	// underneath the result.
	d.settle(ctx, stream)

	streamed = stream.wrote()

	if err != nil {
		// The caller gave up rather than the provider failing, so the work is
		// still out there. Stop it, or it keeps running and billing.
		if ctx.Err() != nil {
			d.abandon(provider, id)
			run.to(ctx, execution.StateCancelled)
		} else {
			run.failed(ctx, err)
		}

		return nil, streamed, err
	}

	result, err := provider.Result(ctx, id)
	run.finish(ctx, state, result)

	if err != nil {
		return nil, streamed, err
	}

	// After the result, because releasing may destroy what it reads.
	d.release(provider, id)

	// A provider that reported cancelled produced no verdict about the work,
	// so it is not an answer even though a result came back.
	if state == execution.StateCancelled {
		return result, streamed, plugin.Infrastructure(
			fmt.Errorf("execution %s was cancelled by the provider", id))
	}

	return result, streamed, nil
}

// release frees whatever the provider left behind, for providers that leave
// anything.
//
// Best effort and on its own context, so that a caller who has already stopped
// waiting still gets the resource cleaned up. A failure here has not failed the
// execution, and the provider's own sweep is the backstop.
func (d *Dispatcher) release(provider plugin.Provider, id execution.ID) {
	releaser, ok := provider.(plugin.Releaser)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()),
		releaseTimeout)
	defer cancel()

	_ = releaser.Release(ctx, id)
}

// abandon stops an execution the caller stopped waiting for.
//
// On its own context, because the one that was cancelled is why we are here.
// Best effort: nothing is left to report a failure to, and the sweep is the
// backstop for whatever this misses.
func (d *Dispatcher) abandon(provider plugin.Provider, id execution.ID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()),
		abandonTimeout)
	defer cancel()

	_ = provider.Cancel(ctx, id)
}

// watch polls until the execution reaches a state it never leaves, recording
// each state it passes through.
//
// Returns the terminal state unrecorded, because the caller records it with
// the result that goes with it.
func (d *Dispatcher) watch(
	ctx context.Context, provider plugin.Provider, run *tracked, event Event,
) (execution.State, error) {
	last := event.State

	for n := 0; ; n++ {
		if err := d.sleep(ctx, d.poll.interval(n)); err != nil {
			return "", err
		}

		status, err := provider.Status(ctx, run.rec.ID)
		if err != nil {
			return "", err
		}

		// A terminal state is left unreported: the caller announces that itself
		// once the last of the output has arrived, and reporting it here puts
		// "succeeded" above the build it describes.
		if status.State.Terminal() {
			return status.State, nil
		}

		// Only on a change, so a job that provisions for two minutes reports
		// once rather than every poll.
		if status.State != last {
			last = status.State
			event.State = status.State

			run.to(ctx, status.State)
			d.report(event)
		}
	}
}
