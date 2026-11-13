// -------------------------------------------------------------------------------
// Execution Runner
//
// Author: Alex Freidah
//
// One goroutine per execution, the only writer of its record, takes it from
// submission through release: submit, watch, collect, settle, release. Each
// step leaves its mark on the record, so a runner restored after a claim
// starts at the first step not done. Every write is checked against the
// dispatch's lease; a refused one stops the runner.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/afreidah/vagabond/internal/backoff"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// answer is how an execution ended for its task: the provider's terminal
// state, which is the verdict, what the task produced, and the error that
// kept it from answering. rec is the record as it stood.
type answer struct {
	state  execution.State
	result *execution.Result
	rec    execution.Record
	err    error
}

// runner drives one execution. provider is nil when it is no longer
// configured, and task is nil for a restored runner, which cannot submit.
type runner struct {
	d        *Dispatcher
	provider plugin.Provider
	lease    *lease
	rec      execution.Record
	task     *job.Task
	members  []string

	submitted  *execution.Result // the answer Submit carried, from a synchronous provider
	ranNothing bool              // the provider turned the work down or never got it
	unanswered bool              // nothing says what ran, so it is charged as reserved
	abandoned  error             // why the runner cancelled the work itself
	unread     error             // why the result could not be read

	answered chan answer
	once     sync.Once
}

// newRunner builds a runner for rec under l.
func (d *Dispatcher) newRunner(provider plugin.Provider, l *lease, rec *execution.Record) *runner {
	return &runner{d: d, provider: provider, lease: l, rec: *rec, answered: make(chan answer, 1)}
}

// start runs the runner in the background, counted against its lease, and
// returns it.
func (r *runner) start(ctx context.Context) *runner {
	r.lease.runners.Add(1)

	go r.run(ctx)

	return r
}

// wait returns the runner's first answer, which comes before settling and
// releasing.
func (r *runner) wait() answer {
	return <-r.answered
}

// -------------------------------------------------------------------------
// STEPS
// -------------------------------------------------------------------------

// run drives the execution to the end of its steps. Work that acts on the
// provider stops when ctx ends or the dispatch is cancelled; the steps after
// it run however the run ends.
func (r *runner) run(ctx context.Context) {
	defer r.lease.runners.Done()

	work := context.WithoutCancel(ctx)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-r.lease.stopped():
			cancel()
		case <-ctx.Done():
		}
	}()

	// A runner that stopped early still answers, so its task does not wait on it
	if err := r.drive(ctx, work); err != nil {
		r.answer(&answer{state: r.rec.State, err: err})
	}
}

// drive runs every step the record says is left. An error means the runner
// had to stop: the lease moved, or the store was given up on.
func (r *runner) drive(ctx, work context.Context) error {
	if r.provider == nil {
		return r.unconfigured(work)
	}

	if r.rec.State == execution.StatePending {
		done, err := r.submit(ctx, work)
		if err != nil || done {
			return errors.Join(err, r.finish(work, err == nil))
		}
	}

	// A restored execution already lost answers as it stands; it is resolved in the background
	if r.task == nil && r.rec.State == execution.StateLost {
		r.answer(r.verdict())
	}

	if !r.rec.Terminal() {
		if err := r.watch(ctx, work); err != nil {
			return err
		}
	}

	if r.rec.LogsAt.IsZero() {
		if err := r.collect(work); err != nil {
			return err
		}
	}

	r.answer(r.verdict())

	return r.finish(work, true)
}

// submit hands the task to the provider, or for a restored runner asks the
// provider whether it ever got it, and reports whether nothing more is left to
// watch.
func (r *runner) submit(ctx, work context.Context) (bool, error) {
	if r.task == nil {
		if _, err := r.d.status(work, r.provider, r.rec.ID); err != nil {
			return true, r.failSubmit(work, plugin.Infrastructure(err))
		}

		r.move(execution.StateSubmitted)

		return false, r.save(work)
	}

	sub, err := r.d.submit(ctx, r.provider, r.rec.ID, r.task, r.members)
	if err == nil {
		if invalid := sub.Validate(); invalid != nil {
			err = plugin.Internal(invalid)
		}
	}

	if err != nil {
		return true, r.failSubmit(work, err)
	}

	r.rec.ProviderID = sub.ProviderID
	r.submitted = sub.Result
	r.move(execution.StateSubmitted)

	if !sub.State.Terminal() {
		r.move(sub.State)
	}

	if err := r.save(work); err != nil {
		return false, err
	}

	// A synchronous answer is written with its output, by collect
	r.move(sub.State)

	return false, nil
}

// failSubmit records a submission that got no answer as failed, with nothing
// to collect. One the provider may have started anyway is cancelled, after
// the write confirms the lease, and the provider is asked what ran.
func (r *runner) failSubmit(work context.Context, err error) error {
	r.ranNothing = turnedDown(err)
	r.rec.Failure = failureClass(err)
	r.rec.LogsAt = r.d.now()
	r.move(execution.StateFailed)

	if serr := r.save(work); serr != nil {
		return serr
	}

	r.answer(&answer{state: r.rec.State, err: err})

	if !r.ranNothing {
		r.cancel(work)
		r.probe(work)
	}

	return nil
}

// probe asks the provider what ran of work the runner stopped: one it reports
// ran for the times it gives. One it does not know, gone or never received,
// and one it cannot be asked about are charged for the time the record saw
// running; with none seen, the first ran nothing and the second is charged as
// reserved.
func (r *runner) probe(work context.Context) {
	status, err := r.d.status(work, r.provider, r.rec.ID)

	switch {
	case errors.Is(err, plugin.ErrUnknownExecution):
		// A provider whose Cancel deletes the run forgets one that did run
		r.ranNothing = r.rec.StartedAt.IsZero()
	case err != nil:
		r.unanswered = r.rec.StartedAt.IsZero()
	default:
		// The provider's times are the run's own; a zero one says nothing
		if !status.StartedAt.IsZero() {
			r.rec.StartedAt = status.StartedAt
		}

		if !status.EndedAt.IsZero() {
			r.rec.EndedAt = status.EndedAt
		}
	}
}

// watch polls the provider until the execution is terminal, recording each
// state it reaches but the last, which collect writes with the output. A
// provider that stops answering makes it lost, which is answered at once and
// resolved here: failed once the grace period passes, or once the provider
// no longer knows it.
func (r *runner) watch(ctx, work context.Context) error {
	for n := 0; !r.rec.Terminal(); n++ {
		if err := r.d.sleep(ctx, r.d.poll.interval(n)); err != nil || r.lease.stopReason() != nil {
			return r.abandon(ctx, work)
		}

		status, err := r.d.status(ctx, r.provider, r.rec.ID)

		switch {
		case ctx.Err() != nil:
			return r.abandon(ctx, work)
		case err != nil:
			err = r.lose(work, err)
		default:
			err = r.reached(work, status.State)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// reached records a state Status reported, writing only a change, and a
// terminal one not at all: collect writes it with the output, so a client
// that sees the end finds the output there. A lost execution leaves only for
// an answer, which clears why it was lost, so it is collected and charged
// like any other.
func (r *runner) reached(work context.Context, next execution.State) error {
	lost := r.rec.State == execution.StateLost

	if next == r.rec.State || (lost && !next.Terminal()) {
		return nil
	}

	if lost {
		r.rec.Failure = ""
	}

	r.move(next)

	if r.rec.Terminal() {
		return nil
	}

	return r.save(work)
}

// lose records a failed Status call. The first makes the execution lost, or
// keeps it lost when the provider already said so, and answers. It fails
// once lost past the grace period, charged as reserved, or at once when the
// provider no longer knows it, charged for the time it was seen running.
func (r *runner) lose(work context.Context, cause error) error {
	// An unknown classified infrastructure may pass, as a listing that lags the run
	forgotten := errors.Is(cause, plugin.ErrUnknownExecution) && !plugin.Reroutable(cause)

	switch {
	case forgotten || r.rec.LostExpired(r.d.now()):
		r.unanswered = !forgotten
		r.move(execution.StateFailed)
	case r.rec.Failure == "":
		r.move(execution.StateLost)
	default:
		return nil
	}

	if r.rec.Failure == "" {
		r.rec.Failure = failureClass(cause)
	}

	// A failed one is written with its output, by collect
	if !r.rec.Terminal() {
		if err := r.save(work); err != nil {
			return err
		}
	}

	r.answer(&answer{state: r.rec.State, err: cause})

	return nil
}

// abandon cancels work its caller gave up on or its dispatch was cancelled
// for, after a write confirms the lease, and marks it cancelled with the
// times the provider gives, so it is charged for what it ran. Collect writes
// it with the output.
func (r *runner) abandon(ctx, work context.Context) error {
	if r.lease.taken() {
		return ErrLeaseLost
	}

	r.abandoned = r.lease.stopReason()
	if r.abandoned == nil {
		r.abandoned = ctx.Err()
	}

	if err := r.save(work); err != nil {
		return err
	}

	r.cancel(work)
	r.move(execution.StateCancelled)
	r.probe(work)

	return nil
}

// collect reads the result, unless the record has one, and the output, in
// that order, then records both. A result that cannot be read leaves the
// output captured and the record without one.
func (r *runner) collect(work context.Context) error {
	result := r.submitted
	if result == nil {
		result = r.rec.Result
	}

	if result == nil && r.abandoned == nil && r.rec.Failure == "" {
		res, err := r.d.result(work, r.provider, r.rec.ID)
		if err != nil {
			r.unread = err
		} else {
			result = res
		}
	}

	r.d.capture(work, r.provider, &r.rec, result, r.lease.key)

	if result != nil {
		r.rec.Result = result
	}

	return r.save(work)
}

// verdict is the runner's answer once the execution is terminal.
func (r *runner) verdict() *answer {
	out := &answer{state: r.rec.State, result: r.rec.Result}

	switch {
	case r.abandoned != nil:
		out.err = r.abandoned
	case r.unread != nil:
		out.err = r.unread
	case r.rec.State == execution.StateLost:
		out.err = plugin.Infrastructure(fmt.Errorf("execution %s is lost", r.rec.ID))
	case r.rec.State == execution.StateFailed && r.rec.Failure != "":
		out.err = plugin.Infrastructure(fmt.Errorf("execution %s was lost and never answered", r.rec.ID))
	case r.rec.State == execution.StateCancelled:
		out.err = plugin.Infrastructure(fmt.Errorf("execution %s was cancelled by the provider", r.rec.ID))
	}

	return out
}

// finish settles the execution's quota and releases what its provider left
// behind. ran is false when the steps before stopped early, which leaves both
// to a runner that holds the lease.
func (r *runner) finish(work context.Context, ran bool) error {
	if !ran {
		return nil
	}

	if err := r.settle(work); err != nil {
		return err
	}

	return r.release(work)
}

// settle replaces the execution's reservation with what it cost: nothing when
// nothing ran, what the platform billed when it said, otherwise the declared
// shape over how long it ran.
func (r *runner) settle(work context.Context) error {
	var reserved bool

	err := r.d.persist(work, func(ctx context.Context) error {
		var err error

		reserved, err = r.d.ledger.Reserved(ctx, r.rec.ID)

		return err
	})
	if err != nil || !reserved {
		return err
	}

	if r.ranNothing {
		return r.d.persist(work, func(ctx context.Context) error { return r.d.ledger.Drop(ctx, r.rec.ID) })
	}

	actual := cost(&r.rec, r.unanswered)

	return r.d.persist(work, func(ctx context.Context) error {
		return r.d.ledger.Settle(ctx, r.rec.ID, r.rec.Namespace, r.rec.Provider, actual)
	})
}

// release frees what the provider left behind, retrying until it is gone,
// each try within callTimeout, and records it released. The last write
// confirmed the lease.
func (r *runner) release(work context.Context) error {
	if releaser, ok := r.provider.(plugin.Releaser); ok {
		for n := 0; ; n++ {
			err := boundedErr(work, func(ctx context.Context) error {
				return authorizedErr(ctx, r.d, r.provider, func() error { return releaser.Release(ctx, r.rec.ID) })
			})
			if err == nil || errors.Is(err, plugin.ErrUnknownExecution) {
				break
			}

			if r.lease.taken() {
				return ErrLeaseLost
			}

			r.d.logger.WarnContext(work, "releasing provider leftovers", "execution", r.rec.ID, "error", err)

			if err := r.d.sleep(work, backoff.Exponential(persistRetry, persistRetryMax, n)); err != nil {
				return err
			}
		}
	}

	r.rec.Released = r.d.now()

	return r.save(work)
}

// unconfigured ends an execution whose provider is no longer configured: it
// can never answer, so it fails, is charged for the time it was seen
// running, and has nothing to release.
func (r *runner) unconfigured(work context.Context) error {
	if !r.rec.Terminal() {
		r.rec.Failure = string(plugin.ClassInternal)
		r.move(execution.StateFailed)
	}

	if r.rec.LogsAt.IsZero() {
		r.rec.LogsAt = r.d.now()
	}

	if err := r.save(work); err != nil {
		return err
	}

	r.answer(&answer{state: r.rec.State, err: fmt.Errorf("%w: %s", ErrNoProvider, r.rec.Provider)})

	if err := r.settle(work); err != nil {
		return err
	}

	r.rec.Released = r.d.now()

	return r.save(work)
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// answer hands the dispatch the runner's first answer, with the record as it
// stands. Later answers are dropped.
func (r *runner) answer(a *answer) {
	r.once.Do(func() {
		a.rec = r.rec
		r.answered <- *a
	})
}

// move advances the record to next, leaving it as it is for a move the state
// machine refuses, such as to the state it is already in.
func (r *runner) move(next execution.State) {
	_ = r.rec.To(next, r.d.now())
}

// save writes the record under the lease until the store takes it.
func (r *runner) save(work context.Context) error {
	err := r.d.persist(work, func(ctx context.Context) error { return r.d.executions.Save(ctx, &r.rec, r.lease.key) })
	if errors.Is(err, ErrLeaseLost) {
		r.lease.lose()
	}

	return err
}

// cancel asks the provider to stop the execution, best effort and within
// callTimeout.
func (r *runner) cancel(work context.Context) {
	_ = boundedErr(work, func(ctx context.Context) error {
		return authorizedErr(ctx, r.d, r.provider, func() error { return r.provider.Cancel(ctx, r.rec.ID) })
	})
}

// cost is what an ended execution cost: what the platform billed, else the
// declared shape over its run time, or over its timeout when it never
// answered, else over the time it was seen running.
func cost(rec *execution.Record, unanswered bool) quota.Execution {
	if res := rec.Result; res != nil {
		if res.Billed != nil {
			return *res.Billed
		}

		return quota.Execution{CPU: rec.CPU, Memory: rec.Memory, Duration: res.Duration}
	}

	if unanswered {
		return quota.Execution{CPU: rec.CPU, Memory: rec.Memory, Duration: rec.Timeout}
	}

	return quota.Execution{CPU: rec.CPU, Memory: rec.Memory, Duration: execution.Elapsed(rec.StartedAt, rec.EndedAt)}
}

// failureClass is the class an error carries, internal when it carries none.
func failureClass(err error) string {
	if classified, ok := errors.AsType[*plugin.Error](err); ok {
		return string(classified.Class)
	}

	return string(plugin.ClassInternal)
}

// turnedDown reports whether a Submit error means nothing ran: only one
// classified internal, Vagabond's request refused or never sent, says so. An
// outage, or an error nobody classified, may have left a run behind.
func turnedDown(err error) bool {
	classified, ok := errors.AsType[*plugin.Error](err)

	return ok && classified.Class == plugin.ClassInternal
}
