// -------------------------------------------------------------------------------
// Resuming a Dispatch
//
// Author: Alex Freidah
//
// A dispatch whose owner died is claimed by a server and taken to its end: a
// runner is restored for every execution not yet released and starts at the
// first step its record says is left. Tasks the job had not reached are not
// run, since the job and its metadata are not kept, so the dispatch ends
// unanswered unless the task in flight was its last.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"fmt"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// RESUME
// -------------------------------------------------------------------------

// Resume takes a claimed dispatch to its end and returns its verdict and why.
// A dispatch whose executions cannot be read is left open, so its lease lapses
// and the next claim tries again.
func (d *Dispatcher) Resume(
	ctx context.Context, claimed *execution.Dispatch,
) (execution.DispatchState, string, error) {
	l := d.hold(ctx, claimed.ID, claimed.Lease())

	// A cancel asked for while no process held the dispatch is acted on at once
	if !claimed.CancelRequested.IsZero() {
		l.requestCancel()
	}

	records, err := d.executions.DispatchExecutions(ctx, claimed.ID)
	if err != nil {
		l.release()

		return "", "", fmt.Errorf("reading executions of dispatch %s: %w", claimed.ID, err)
	}

	runners := make(map[execution.ID]*runner)

	for _, rec := range records {
		if !done(rec) {
			runners[rec.ID] = d.restore(l, rec).start(ctx)
		}
	}

	// The verdict is read from each record once its runner has answered
	for i, rec := range records {
		if r, ok := runners[rec.ID]; ok {
			answered := r.wait().rec
			records[i] = &answered
		}
	}

	if claimed.State != execution.DispatchRunning {
		defer l.release()
		d.close(context.WithoutCancel(ctx), l)

		return claimed.State, claimed.Error, nil
	}

	state, reason := ended(claimed.Tasks, records)

	origin := Origin{
		Namespace: claimed.Namespace, Job: claimed.Job, JobVersion: claimed.JobVersion, Dispatch: claimed.ID, lease: l,
	}
	d.end(ctx, origin, state, reason)

	return state, reason, nil
}

// restore builds a runner for a record of a claimed dispatch. It has no task,
// so a record still pending is asked about rather than submitted.
func (d *Dispatcher) restore(l *lease, rec *execution.Record) *runner {
	provider, _ := d.registry.Provider(rec.Provider)

	return d.newRunner(provider, l, rec)
}

// done reports whether an execution has finished every step: terminal, its
// output collected, and what it left behind released.
func done(rec *execution.Record) bool {
	return rec.Terminal() && !rec.LogsAt.IsZero() && !rec.Released.IsZero()
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
