// -------------------------------------------------------------------------------
// Resuming a Dispatch
//
// Author: Alex Freidah
//
// A dispatch whose owner died is claimed by a server and taken to its end.
// Every execution left unfinished is followed to its result and charged, since
// plugins derive everything from the execution ID. Tasks the job had not
// reached are not run: the job and its metadata are not kept, so the dispatch
// ends unanswered unless the task in flight was its last.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"fmt"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// RESUME
// -------------------------------------------------------------------------

// Resume takes a claimed dispatch to its end: follows its unfinished
// executions, then records how it ended and returns that state and why.
//
// A failure to read its executions returns an error and leaves the dispatch
// running, so its lease lapses and the next claim tries again.
func (d *Dispatcher) Resume(
	ctx context.Context, claimed *execution.Dispatch,
) (execution.DispatchState, string, error) {
	origin := Origin{
		Namespace:  claimed.Namespace,
		Job:        claimed.Job,
		JobVersion: claimed.JobVersion,
		Dispatch:   claimed.ID,
		lease:      d.hold(ctx, claimed.ID),
	}

	records, err := d.executions.DispatchExecutions(ctx, claimed.ID)
	if err != nil {
		origin.lease.release()

		return "", "", fmt.Errorf("reading executions of dispatch %s: %w", claimed.ID, err)
	}

	for _, rec := range records {
		if !rec.Terminal() {
			d.resume(ctx, rec)
		}
	}

	state, reason := ended(claimed.Tasks, records)
	d.end(ctx, origin, state, reason)

	return state, reason, nil
}

// resume follows one execution to its result and charges it. rec is updated
// in place, so the dispatch's verdict reads where it ended.
//
// An execution the provider cannot account for is recorded as failed before
// submission and lost after, and its reservation is left to the reaper, which
// asks the provider again later.
func (d *Dispatcher) resume(ctx context.Context, rec *execution.Record) {
	run := &tracked{rec: *rec, store: d.executions, now: d.now}
	defer func() { *rec = run.rec }()

	provider, ok := d.registry.Provider(rec.Provider)
	if !ok {
		run.failed(ctx, plugin.Internal(fmt.Errorf("%w: %s", ErrNoProvider, rec.Provider)))

		return
	}

	status, err := provider.Status(ctx, rec.ID)
	if err != nil {
		run.failed(ctx, plugin.Infrastructure(err))

		return
	}

	if run.rec.State == execution.StatePending {
		run.to(ctx, execution.StateSubmitted)
	}

	event := Event{
		Task: rec.Task, Provider: rec.Provider, ID: rec.ID, Attempt: rec.Attempt, State: status.State,
	}

	ended, _ := d.follow(ctx, provider, run, event)
	d.charge(ctx, &run.rec, ended.result)
}

// -------------------------------------------------------------------------
// VERDICT
// -------------------------------------------------------------------------

// ended decides how a resumed dispatch ended from its executions, oldest
// first. The last one is the task in flight: it passing ends the dispatch only
// when every declared task has run, and it failing on its own answer fails it.
// Anything else got no answer.
func ended(tasks int, records []*execution.Record) (execution.DispatchState, string) {
	if len(records) == 0 {
		return execution.DispatchUnanswered, "interrupted before any task was submitted"
	}

	ran := make(map[string]bool)
	for _, rec := range records {
		ran[rec.Task] = true
	}

	last := records[len(records)-1]
	answered := (last.State == execution.StateSucceeded || last.State == execution.StateFailed) &&
		last.Result != nil && last.Failure == ""

	switch {
	// The provider's terminal state is the verdict, not the exit code.
	case answered && last.State == execution.StateSucceeded && len(ran) >= tasks:
		return execution.DispatchSucceeded, ""
	case answered && last.State == execution.StateSucceeded:
		return execution.DispatchUnanswered, fmt.Sprintf("interrupted after %d of %d tasks", len(ran), tasks)
	case answered:
		return execution.DispatchFailed, ""
	default:
		return execution.DispatchUnanswered, fmt.Sprintf("interrupted; execution %s ended %s", last.ID, last.State)
	}
}
