// -------------------------------------------------------------------------------
// Resuming Dispatches
//
// Author: Alex Freidah
//
// A dispatch whose owner died stops having its lease renewed. The server
// claims every lapsed one, at startup and on a timer, and takes each to its
// end in the background, as it would a dispatch it started.
// -------------------------------------------------------------------------------

package server

import (
	"context"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/panics"
)

// claim takes over every dispatch whose lease lapsed and resumes each in the
// background. Dispatches this server is running are skipped: their leases
// lapse while the store is unreachable, and the process running them is alive.
func (s *Server) claim(ctx context.Context) error {
	now := s.now()

	claimed, err := s.executions.ClaimDispatches(ctx, s.owner, now, now.Add(s.leaseTTL), s.runningIDs())
	if err != nil {
		return err
	}

	for _, d := range claimed {
		s.resume(ctx, d)
	}

	return nil
}

// resume takes a claimed dispatch to its end on its own context, which
// outlives the server's so a shutdown leaves it to the next server rather
// than cancelling its executions.
func (s *Server) resume(ctx context.Context, d *execution.Dispatch) {
	runCtx, cancel := context.WithCancel(dispatchContext(ctx, d.ID, d.Namespace, d.Job, d.JobVersion))
	s.track(d.ID, cancel)

	s.logger.InfoContext(runCtx, "dispatch resumed", "epoch", d.Epoch)

	go func() {
		defer s.untrack(d.ID)
		defer cancel()
		defer panics.Catch(runCtx, s.logger, "resumed dispatch", nil)

		state, reason, err := s.dispatcher.Resume(runCtx, d)
		if err != nil {
			s.logger.WarnContext(runCtx, "resuming the dispatch failed", "error", err)

			return
		}

		s.logger.InfoContext(runCtx, "resumed dispatch finished", "state", state, "reason", reason)
	}()
}
