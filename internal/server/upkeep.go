// -------------------------------------------------------------------------------
// Upkeep
//
// Author: Alex Freidah
//
// What the CLI does once per call, the server does on timers: refreshing
// provider capabilities and quota usage, reaping reservations nothing was
// recorded for, claiming dispatches whose owner died, and pruning stored
// output past its retention. Each task logs its own failures and waits for its
// next tick, so one failing never stops another.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"sync"
	"time"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	capabilitiesInterval = time.Minute
	usageInterval        = 15 * time.Second
	reapInterval         = 5 * time.Minute
	claimInterval        = 30 * time.Second
	pruneInterval        = time.Hour
)

// -------------------------------------------------------------------------
// UPKEEP
// -------------------------------------------------------------------------

// startUpkeep runs every upkeep task on its interval until ctx is done. The
// returned group is waited on to know they have all stopped.
func (s *Server) startUpkeep(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup

	wg.Go(func() { every(ctx, capabilitiesInterval, s.refreshCapabilities) })
	wg.Go(func() { every(ctx, usageInterval, s.refreshUsage) })
	wg.Go(func() { every(ctx, reapInterval, s.reap) })
	wg.Go(func() { every(ctx, s.claimEvery, s.claim) })
	wg.Go(func() { every(ctx, pruneInterval, s.prune) })

	return &wg
}

// every calls fn each interval until ctx is done. The first call is one
// interval in; a task that needs a pass at startup gets it before serving.
func every(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// refreshCapabilities asks every provider what it can do. One that fails is
// marked unhealthy, and admission rejects it by name until it answers again.
func (s *Server) refreshCapabilities(ctx context.Context) {
	if err := s.registry.Refresh(ctx); err != nil {
		s.logger.WarnContext(ctx, "refreshing provider capabilities", "error", err)
	}
}

// refreshUsage re-reads quota usage, so plans see what every process has
// charged. A failed read keeps the last snapshot.
func (s *Server) refreshUsage(ctx context.Context) {
	if err := s.ledger.Refresh(ctx); err != nil {
		s.logger.WarnContext(ctx, "refreshing quota usage", "error", err)
	}
}

// reap drops reservations whose execution was never recorded, which a process
// that died between reserving and recording leaves holding quota.
func (s *Server) reap(ctx context.Context) {
	reaped, err := s.dispatcher.Reap(ctx)

	switch {
	case err != nil:
		s.logger.WarnContext(ctx, "resolving abandoned quota reservations", "error", err)
	case reaped > 0:
		s.logger.InfoContext(ctx, "resolved abandoned quota reservations", "count", reaped)
	}
}

// prune deletes the stored output of executions that ended longer ago than the
// retention.
func (s *Server) prune(ctx context.Context) {
	pruned, err := s.executions.PruneLogs(ctx, s.now().Add(-s.retainLogs))

	switch {
	case err != nil:
		s.logger.WarnContext(ctx, "pruning stored output", "error", err)
	case pruned > 0:
		s.logger.InfoContext(ctx, "pruned stored output", "executions", pruned)
	}
}
