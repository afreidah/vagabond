// -------------------------------------------------------------------------------
// Upkeep
//
// Author: Alex Freidah
//
// What the CLI does once per call, the server does on timers: refreshing
// provider capabilities and quota usage, reading which providers are disabled,
// reaping reservations nothing was recorded for, claiming dispatches whose
// owner died, pruning stored output past its retention, and launching periodic
// jobs. A task that fails is logged and runs again at its next call, so one
// failing never stops another.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/panics"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	capabilitiesInterval = time.Minute
	usageInterval        = 15 * time.Second
	disabledInterval     = 5 * time.Second
	reapInterval         = 5 * time.Minute
	claimInterval        = 30 * time.Second
	pruneInterval        = time.Hour
)

// Each task's name, which its failures and panics are logged under. The
// startup passes use the same names.
const (
	disabledTask = "reading disabled providers"
	claimTask    = "claiming abandoned dispatches"
	reapTask     = "resolving abandoned quota reservations"
)

// -------------------------------------------------------------------------
// UPKEEP
// -------------------------------------------------------------------------

// startUpkeep runs every upkeep task on its interval, and periodic launches
// whenever one comes, until ctx is done. The returned group is waited on to
// know they have all stopped.
func (s *Server) startUpkeep(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup

	wg.Go(func() { s.every(ctx, "refreshing provider capabilities", capabilitiesInterval, s.registry.Refresh) })
	wg.Go(func() { s.every(ctx, "refreshing quota usage", usageInterval, s.ledger.Refresh) })
	wg.Go(func() { s.every(ctx, disabledTask, disabledInterval, s.refreshDisabled) })
	wg.Go(func() { s.every(ctx, reapTask, reapInterval, s.reap) })
	wg.Go(func() { s.every(ctx, claimTask, s.claimEvery, s.claim) })
	wg.Go(func() { s.every(ctx, "pruning stored output", pruneInterval, s.prune) })

	// Periodic jobs get their first pass at once, without holding up serving
	wg.Go(func() { s.repeat(ctx, s.wake, time.Now(), s.launchPass) })

	return &wg
}

// every calls fn each interval until ctx is done, counting each interval from
// the start of the call before. The first call is one interval in; a task that
// needs a pass at startup gets it before serving. An error or a panic in one
// call is logged under what, and the next call runs as any other.
func (s *Server) every(ctx context.Context, what string, interval time.Duration, fn func(context.Context) error) {
	s.repeat(ctx, nil, time.Now().Add(interval), func(ctx context.Context) time.Time {
		next := time.Now().Add(interval)
		s.tick(ctx, what, fn)

		return next
	})
}

// repeat calls fn when at comes, then again at whatever time each call
// returns, until ctx is done. A receive on wake makes the next call at once;
// a nil wake never does.
func (s *Server) repeat(ctx context.Context, wake <-chan struct{}, at time.Time, fn func(context.Context) time.Time) {
	for {
		timer := time.NewTimer(time.Until(at))

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-wake:
		case <-timer.C:
		}

		timer.Stop()

		at = fn(ctx)
	}
}

// tick calls fn once, logging the error it returns, or a panic in it, under
// what rather than ending the loop.
func (s *Server) tick(ctx context.Context, what string, fn func(context.Context) error) {
	defer panics.Catch(ctx, s.logger, what, nil)

	if err := fn(ctx); err != nil {
		s.logger.WarnContext(ctx, "upkeep task failed", "task", what, "error", err)
	}
}

// reap drops reservations whose execution was never recorded, which a process
// that died between reserving and recording leaves holding quota. Any it
// dropped are logged even when a later one failed.
func (s *Server) reap(ctx context.Context) error {
	reaped, err := s.dispatcher.Reap(ctx)
	if reaped > 0 {
		s.logger.InfoContext(ctx, "resolved abandoned quota reservations", "count", reaped)
	}

	return err
}

// prune deletes the stored output of executions that ended longer ago than the
// retention, logging how many it pruned.
func (s *Server) prune(ctx context.Context) error {
	pruned, err := s.executions.PruneLogs(ctx, s.now().Add(-s.retainLogs))
	if pruned > 0 {
		s.logger.InfoContext(ctx, "pruned stored output", "count", pruned)
	}

	return err
}
