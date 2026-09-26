// -------------------------------------------------------------------------------
// Execution Routes
//
// Author: Alex Freidah
//
// Reading one execution, its stored output, and cancelling it. A cancel stops
// the whole dispatch when this process is running it, and otherwise asks the
// provider directly.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// cancelTimeout bounds asking a provider to stop an execution.
const cancelTimeout = 30 * time.Second

// executionStatus answers with one execution's record, result fields included
// once it has one.
func (s *Server) executionStatus(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := executionID(r)
	if err != nil {
		return nil, err
	}

	rec, err := s.executions.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}

	return executionOf(rec), nil
}

// executionLogs writes an execution's stored output as plain text, empty until
// it has a result. Not wrapped, since the body is not JSON.
func (s *Server) executionLogs(w http.ResponseWriter, r *http.Request) {
	id, err := executionID(r)
	if err == nil {
		var rec *execution.Record

		if rec, err = s.executions.Get(r.Context(), id); err == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")

			if rec.Result != nil {
				_, _ = w.Write(rec.Result.Logs) //nolint:gosec // plain text, never rendered as HTML
			}

			return
		}
	}

	status, body := s.failure(r, err)
	write(w, status, body)
}

// cancelExecution stops an execution and answers with its record. A dispatch
// running here is cancelled whole, ending the job as an interrupted run does.
func (s *Server) cancelExecution(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := executionID(r)
	if err != nil {
		return nil, err
	}

	rec, err := s.executions.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}

	if rec.Terminal() || s.stopDispatch(rec.Dispatch) {
		return executionOf(rec), nil
	}

	if err := s.cancelUnwatched(r.Context(), rec); err != nil {
		return nil, err
	}

	return executionOf(rec), nil
}

// cancelUnwatched stops an execution no dispatch here is running, and records
// it cancelled unless something recorded it first.
func (s *Server) cancelUnwatched(ctx context.Context, rec *execution.Record) error {
	provider, ok := s.registry.Provider(rec.Provider)
	if !ok {
		return fmt.Errorf("execution %s ran on %q, which is no longer configured", rec.ID, rec.Provider)
	}

	cancelCtx, cancel := context.WithTimeout(ctx, cancelTimeout)
	defer cancel()

	if err := provider.Cancel(cancelCtx, rec.ID); err != nil {
		return fmt.Errorf("cancelling %s on %s: %w", rec.ID, rec.Provider, err)
	}

	from := rec.State

	if err := rec.To(execution.StateCancelled, s.now()); err != nil {
		return err
	}

	if err := s.executions.Update(ctx, rec, from); err != nil && !errors.Is(err, execution.ErrStale) {
		return err
	}

	return nil
}
