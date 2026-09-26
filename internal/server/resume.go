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

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/execution"
)

// claim takes over every dispatch whose lease lapsed and resumes each in the
// background.
func (s *Server) claim(ctx context.Context) {
	now := s.now()

	claimed, err := s.executions.ClaimDispatches(ctx, s.owner, now, now.Add(dispatch.LeaseTTL))
	if err != nil {
		s.logger.WarnContext(ctx, "claiming abandoned dispatches", "error", err)

		return
	}

	for _, d := range claimed {
		s.resume(ctx, d)
	}
}

// resume takes a claimed dispatch to its end on its own context, which
// outlives the server's so a shutdown leaves it to the next server rather
// than cancelling its executions.
func (s *Server) resume(ctx context.Context, d *execution.Dispatch) {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.track(d.ID, cancel)

	logger := s.logger.With("dispatch", d.ID, "job", d.Job, "version", d.JobVersion, "namespace", d.Namespace)
	logger.InfoContext(ctx, "dispatch resumed", "previous_owner", d.Owner)

	go func() {
		defer s.untrack(d.ID)
		defer cancel()

		state, reason, err := s.dispatcher.Resume(runCtx, d)
		if err != nil {
			logger.WarnContext(runCtx, "resuming dispatch", "error", err)

			return
		}

		logger.InfoContext(runCtx, "resumed dispatch finished", "state", state, "reason", reason)
	}()
}
