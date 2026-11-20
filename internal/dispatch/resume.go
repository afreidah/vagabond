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
	"github.com/afreidah/vagabond/internal/panics"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// RESUME
// -------------------------------------------------------------------------

// Resume takes a claimed dispatch to its end and returns its verdict and why.
// A dispatch whose executions cannot be read is left open, so its lease lapses
// and the next claim tries again. One claimed past maxResumes is ended without
// running anything, and a panic ends it unanswered.
func (d *Dispatcher) Resume(
	ctx context.Context, claimed *execution.Dispatch,
) (state execution.DispatchState, reason string, err error) {
	l := d.hold(ctx, claimed.ID, claimed.Lease())
	origin := Origin{
		Namespace: claimed.Namespace, Job: claimed.Job, JobVersion: claimed.JobVersion, Dispatch: claimed.ID, lease: l,
	}

	if claimed.Epoch-1 > maxResumes {
		return d.abandon(ctx, claimed, origin)
	}

	defer panics.Catch(ctx, d.logger, "resuming dispatch "+claimed.ID.String(), func(p error) {
		state, reason, err = execution.DispatchUnanswered, p.Error(), nil
		d.end(ctx, origin, state, reason)
	})

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

	state, reason = ended(claimed.Tasks, records)
	d.end(ctx, origin, state, reason)

	return state, reason, nil
}

// maxResumes is how many times a dispatch is resumed before a claim ends it
// instead. A dispatch that ends its server in a way no recover catches, such
// as a fatal runtime error, would otherwise end every server that claims it.
const maxResumes = 5

// abandon ends a dispatch claimed past maxResumes without starting any of its
// executions, which are left as their records stand: unanswered when it had no
// verdict, and closed under the verdict it had otherwise.
func (d *Dispatcher) abandon(
	ctx context.Context, claimed *execution.Dispatch, origin Origin,
) (execution.DispatchState, string, error) {
	reason := fmt.Sprintf("abandoned after %d resumes without finishing", maxResumes)
	d.logger.ErrorContext(ctx, "abandoning dispatch", "dispatch", claimed.ID, "epoch", claimed.Epoch, "reason", reason)

	if claimed.State != execution.DispatchRunning {
		defer origin.lease.release()
		d.close(context.WithoutCancel(ctx), origin.lease)

		return claimed.State, claimed.Error, nil
	}

	d.end(ctx, origin, execution.DispatchUnanswered, reason)

	return execution.DispatchUnanswered, reason, nil
}

// restore builds a runner for a record of a claimed dispatch, on the node the
// record names. It has no task, so a record still pending is asked about
// rather than submitted.
func (d *Dispatcher) restore(l *lease, rec *execution.Record) *runner {
	provider, _ := d.registry.Provider(rec.Provider)

	return d.newRunner(plugin.Target(provider, rec.Node), l, rec)
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
