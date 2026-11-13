// -------------------------------------------------------------------------------
// Execution Routes
//
// Author: Alex Freidah
//
// Reading one execution, its output stored or live, and cancelling it. A
// cancel stops the whole dispatch: at once when this process is running it,
// otherwise through the dispatch's lease, so only its holder acts on the
// provider.
// -------------------------------------------------------------------------------

package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

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

// cancelExecution stops an execution's dispatch and answers with its record as
// it stands. A dispatch running here is cancelled at once; any other is asked
// to stop through its lease, which its holder renews within LeaseRenew.
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

	if err := s.executions.RequestCancel(r.Context(), rec.Dispatch, s.now()); err != nil {
		return nil, fmt.Errorf("requesting the cancel of %s: %w", rec.ID, err)
	}

	return executionOf(rec), nil
}
