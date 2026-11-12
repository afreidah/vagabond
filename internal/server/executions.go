// -------------------------------------------------------------------------------
// Execution Routes
//
// Author: Alex Freidah
//
// Reading one execution, its output stored or live, and cancelling it. A
// cancel stops the whole dispatch when this process is running it, and
// otherwise asks the provider directly.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// cancelTimeout bounds asking a provider to stop an execution.
const cancelTimeout = 30 * time.Second

// -------------------------------------------------------------------------
// STATUS
// -------------------------------------------------------------------------

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

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

// executionLogs writes an execution's output as plain text, from the requested
// byte offset on: streamed live while it runs when the request follows and its
// provider can, and otherwise what is stored, empty until it is captured. An
// offset in output dropped past the ceiling starts at the first byte kept. Not
// wrapped, since the body is not JSON.
func (s *Server) executionLogs(w http.ResponseWriter, r *http.Request) {
	rec, offset, err := s.logsRequest(r)
	if err != nil {
		status, body := s.failure(r, err)
		write(w, status, body)

		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if streamer, ok := s.streamer(r, rec); ok {
		s.follow(r.Context(), w, streamer, rec.ID, offset)

		return
	}

	// Headers wait for the first write, so a read that fails at once can still set a status
	out := &flushing{w: w, rc: http.NewResponseController(w)}
	seq, skip := execution.PlaceLogs(rec.LogsStart, offset)

	err = s.executions.ReadLog(r.Context(), rec.ID, seq, plugin.SkipWriter(out, skip))

	switch {
	case err != nil && !out.wrote:
		status, body := s.failure(r, err)
		write(w, status, body)
	case err != nil && r.Context().Err() == nil:
		s.logger.WarnContext(r.Context(), "reading stored output ended early", "execution", rec.ID, "error", err)
	}
}

// logsRequest reads which execution's output a request asks for, and from
// which byte.
func (s *Server) logsRequest(r *http.Request) (*execution.Record, int64, error) {
	id, err := executionID(r)
	if err != nil {
		return nil, 0, err
	}

	var offset int64

	if raw := r.URL.Query().Get("offset"); raw != "" {
		if offset, err = strconv.ParseInt(raw, 10, 64); err != nil || offset < 0 {
			return nil, 0, badRequest(fmt.Errorf("offset %q is not a byte count", raw))
		}
	}

	rec, err := s.executions.Get(r.Context(), id)

	return rec, offset, err
}

// -------------------------------------------------------------------------
// CANCELLATION
// -------------------------------------------------------------------------

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
