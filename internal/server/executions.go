// -------------------------------------------------------------------------------
// Execution Routes
//
// Author: Alex Freidah
//
// Reading one execution, its output stored or followed as it is stored, and
// cancelling it. A cancel stops the whole dispatch: at once when this process
// is running it, otherwise through the dispatch's lease, so only its holder
// acts on the provider.
// -------------------------------------------------------------------------------

package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/afreidah/vagabond/internal/execution"
)

// maxListed is the most executions one listing answers with.
const maxListed = 1000

// -------------------------------------------------------------------------
// LISTING
// -------------------------------------------------------------------------

// listExecutions answers with a namespace's most recent executions, newest
// first, narrowed by ?job=, ?state= and ?provider=, and at most ?limit= of
// them: recentExecutions when it is absent, never more than maxListed.
func (s *Server) listExecutions(_ http.ResponseWriter, r *http.Request) (any, error) {
	ns, err := s.namespace(r, nil)
	if err != nil {
		return nil, err
	}

	query := r.URL.Query()
	f := execution.Filter{Namespace: ns, Job: query.Get("job"), Provider: query.Get("provider")}

	if raw := query.Get("state"); raw != "" {
		if f.State, err = execution.ParseState(raw); err != nil {
			return nil, badRequest(err)
		}
	}

	limit, err := count(r, "limit", recentExecutions)
	if err != nil {
		return nil, err
	}

	f.Limit = int(min(limit, maxListed))

	runs, err := s.executions.ListExecutions(r.Context(), f)
	if err != nil {
		return nil, err
	}

	return executionsOf(runs), nil
}

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

// executionLogs writes an execution's stored output as plain text from the
// requested byte offset on, as much as has been stored; with follow, it sends
// what is stored after until the output is complete. The Vagabond-Logs-Start
// header says where the body begins, past the offset when the output there
// was dropped past the ceiling. Not wrapped, since the body is not JSON.
func (s *Server) executionLogs(w http.ResponseWriter, r *http.Request) {
	rec, offset, err := s.logsRequest(r)
	if err != nil {
		status, body := s.failure(r, err)
		write(w, status, body)

		return
	}

	// Headers wait for the first write, so a read that fails at once can still set a status
	out := newLogsResponse(w, offset)

	err = s.sendLogs(r.Context(), rec, out, r.URL.Query().Get("follow") == "true")

	switch {
	case err == nil, errors.Is(err, errMoved):
	case !out.wrote:
		status, body := s.failure(r, err)
		write(w, status, body)
	case r.Context().Err() == nil:
		s.logger.WarnContext(r.Context(), "reading stored output ended early", "execution_id", rec.ID, "error", err)
	}
}

// logsRequest reads which execution's output a request asks for, and from
// which byte.
func (s *Server) logsRequest(r *http.Request) (*execution.Record, int64, error) {
	id, err := executionID(r)
	if err != nil {
		return nil, 0, err
	}

	offset, err := count(r, "offset", 0)
	if err != nil {
		return nil, 0, err
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
