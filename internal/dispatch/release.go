// -------------------------------------------------------------------------------
// Releasing Leftovers
//
// Author: Alex Freidah
//
// Some providers leave a resource behind for every execution, as Cloud Run
// leaves a Job and a pool node a stopped container. Dispatch releases it after
// reading the result; any other ending leaves it for this loop, which releases
// each unreleased execution once it is over and settled, reconciling a lost
// one on the way.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// RELEASE LOOP
// -------------------------------------------------------------------------

// ReleaseAfter is how long an execution must go unchanged before the loop
// takes it up, so it never races a dispatch still reading the result.
const ReleaseAfter = 10 * time.Minute

// ReleaseLeftovers releases what providers still hold for executions that are
// over, and reports how many it released. One that cannot be released yet is
// left for the next call; the errors of those that failed are joined.
func (d *Dispatcher) ReleaseLeftovers(ctx context.Context) (int, error) {
	records, err := d.executions.Unreleased(ctx, d.now().Add(-ReleaseAfter))
	if err != nil {
		return 0, fmt.Errorf("listing unreleased executions: %w", err)
	}

	var (
		released int
		failures []error
	)

	for _, rec := range records {
		done, err := d.releaseLeftover(ctx, rec)
		if err != nil {
			failures = append(failures, err)

			continue
		}

		if done {
			released++
		}
	}

	return released, errors.Join(failures...)
}

// releaseLeftover releases one execution's leftovers if it is over, and
// reports whether it did. An execution still holding a reservation waits,
// because the reaper settles it by asking the provider what it did, which a
// released execution can no longer answer.
func (d *Dispatcher) releaseLeftover(ctx context.Context, rec *execution.Record) (bool, error) {
	// Resolve a lost execution first, before releasing destroys its result
	if rec.State == execution.StateLost {
		if err := d.reconcileLost(ctx, rec); err != nil {
			return false, err
		}

		// Still unresolved, so possibly still running: nothing to release yet.
		if rec.State == execution.StateLost {
			return false, nil
		}
	}

	reserved, err := d.ledger.Reserved(ctx, rec.ID)
	if err != nil {
		return false, err
	}

	if reserved {
		return false, nil
	}

	// An unconfigured provider cannot be asked; a non-Releaser left nothing
	provider, ok := d.registry.Provider(rec.Provider)
	releaser, releases := provider.(plugin.Releaser)

	if !ok || !releases {
		return true, d.markReleased(ctx, rec.ID)
	}

	// Without a recorded result, ask the provider whether it is over
	if rec.Result == nil {
		status, err := d.status(ctx, provider, rec.ID)

		switch {
		case errors.Is(err, plugin.ErrUnknownExecution):
			return true, d.markReleased(ctx, rec.ID)
		case err != nil:
			return false, fmt.Errorf("status of %s on %s: %w", rec.ID, rec.Provider, err)
		case !status.State.Terminal():
			return false, nil
		}
	}

	err = authorizedErr(ctx, d, provider, func() error { return releaser.Release(ctx, rec.ID) })
	if err != nil && !errors.Is(err, plugin.ErrUnknownExecution) {
		return false, fmt.Errorf("releasing %s on %s: %w", rec.ID, rec.Provider, err)
	}

	return true, d.markReleased(ctx, rec.ID)
}

// -------------------------------------------------------------------------
// LOST EXECUTIONS
// -------------------------------------------------------------------------

// reconcileLost learns what became of a lost execution, updating rec in place.
// One the provider reports over is recorded with its result; one it does not
// know, or still unresolved past execution.LostGracePeriod, is recorded failed.
func (d *Dispatcher) reconcileLost(ctx context.Context, rec *execution.Record) error {
	provider, ok := d.registry.Provider(rec.Provider)
	if !ok {
		// A provider no longer configured can never answer.
		return d.resolveLost(ctx, rec, execution.StateFailed, nil)
	}

	status, err := d.status(ctx, provider, rec.ID)

	switch {
	case errors.Is(err, plugin.ErrUnknownExecution):
		return d.resolveLost(ctx, rec, execution.StateFailed, nil)

	case err == nil && status.State.Terminal():
		result, resultErr := authorized(ctx, d, provider, func() (*execution.Result, error) {
			return provider.Result(ctx, rec.ID)
		})
		if resultErr == nil {
			return d.resolveLost(ctx, rec, status.State, result)
		}

		err = resultErr
	}

	// Still running or not answering: fail it only past the grace period
	if rec.LostExpired(d.now()) {
		return d.resolveLost(ctx, rec, execution.StateFailed, nil)
	}

	if err != nil {
		return fmt.Errorf("reconciling lost %s on %s: %w", rec.ID, rec.Provider, err)
	}

	return nil
}

// resolveLost records a lost execution as ended in state, with result when one
// was fetched. The write must land: releasing after an unrecorded result would
// lose it.
func (d *Dispatcher) resolveLost(
	ctx context.Context, rec *execution.Record, state execution.State, result *execution.Result,
) error {
	resolved := *rec
	resolved.Result = result.Bounded()

	if err := resolved.To(state, d.now()); err != nil {
		return fmt.Errorf("resolving lost %s: %w", rec.ID, err)
	}

	if err := d.executions.Update(ctx, &resolved, execution.StateLost); err != nil {
		return fmt.Errorf("recording lost %s as %s: %w", rec.ID, state, err)
	}

	*rec = resolved

	return nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// markReleased records that id's leftovers are gone.
func (d *Dispatcher) markReleased(ctx context.Context, id execution.ID) error {
	if err := d.executions.MarkReleased(ctx, id, d.now()); err != nil {
		return fmt.Errorf("marking %s released: %w", id, err)
	}

	return nil
}
