// -------------------------------------------------------------------------------
// HTTP Plumbing
//
// Author: Alex Freidah
//
// Every handler returns a value and an error; wrap encodes the one or maps the
// other to a status and an api.Error body. The mapping from sentinel to status
// lives here once.
// -------------------------------------------------------------------------------

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// maxBody bounds a request body. A job file is a few kilobytes.
const maxBody = 1 << 20

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// handler is a route that returns what to encode, or an error that wrap maps
// to a status.
type handler func(w http.ResponseWriter, r *http.Request) (any, error)

// statusError is an error that carries the status and body to answer with.
type statusError struct {
	status int
	body   api.Error
}

// Error returns the message the body carries.
func (e *statusError) Error() string {
	return e.body.Error
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// badRequest is a 400 carrying err's message.
func badRequest(err error) error {
	return &statusError{status: http.StatusBadRequest, body: api.Error{Error: err.Error()}}
}

// invalidJob is a 400 carrying a job file's diagnostics, each against its
// line.
func invalidJob(diags hcl.Diagnostics) error {
	body := api.Error{Error: "the job is not valid"}

	for _, d := range diags {
		out := api.Diagnostic{Summary: d.Summary, Detail: d.Detail, Severity: "error"}

		if d.Severity == hcl.DiagWarning {
			out.Severity = "warning"
		}

		if d.Subject != nil {
			out.Range = d.Subject.String()
		}

		body.Diagnostics = append(body.Diagnostics, out)
	}

	return &statusError{status: http.StatusBadRequest, body: body}
}

// failure maps err to a status and body. Anything unrecognised is a 500 and is
// logged, since it is the server's fault rather than the request's.
func (s *Server) failure(r *http.Request, err error) (int, api.Error) {
	var known *statusError

	switch {
	case errors.As(err, &known):
		return known.status, known.body
	case errors.Is(err, jobs.ErrNotFound), errors.Is(err, execution.ErrNotFound):
		return http.StatusNotFound, api.Error{Error: err.Error()}
	case errors.Is(err, jobs.ErrStopped), errors.Is(err, execution.ErrStale):
		return http.StatusConflict, api.Error{Error: err.Error()}
	default:
		s.logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)

		return http.StatusInternalServerError, api.Error{Error: err.Error()}
	}
}

// -------------------------------------------------------------------------
// ENCODING
// -------------------------------------------------------------------------

// wrap turns a handler into an http.HandlerFunc: a bounded body in, JSON out,
// 200 on success.
func (s *Server) wrap(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)

		out, err := h(w, r)
		if err != nil {
			status, body := s.failure(r, err)
			write(w, status, body)

			return
		}

		write(w, http.StatusOK, out)
	}
}

// write encodes v as JSON with status. A failed encode has nowhere left to be
// reported, the status having been sent.
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a JSON request body into v, or answers 400.
func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return badRequest(fmt.Errorf("reading the request body: %w", err))
	}

	return nil
}

// -------------------------------------------------------------------------
// REQUEST LOGGING
// -------------------------------------------------------------------------

// statusRecorder remembers the status a handler wrote, for the request log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records status before passing it on.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// logRequests logs every request at debug level with its status and how long
// it took.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		s.logger.DebugContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(started))
	})
}

// -------------------------------------------------------------------------
// REQUEST VALUES
// -------------------------------------------------------------------------

// namespace resolves the namespace for j from ?namespace=. A nil j is a request
// naming a registered job, which lives where it was registered.
func (s *Server) namespace(r *http.Request, j *job.Job) (string, error) {
	if j == nil {
		j = &job.Job{}
	}

	ns, err := jobs.Namespace(r.URL.Query().Get("namespace"), j, s.registry.HasNamespace)
	if err != nil {
		return "", badRequest(err)
	}

	return ns, nil
}

// executionID parses the {id} path value, answering 400 for one that is not an
// execution ID.
func executionID(r *http.Request) (execution.ID, error) {
	id, err := execution.ParseID(r.PathValue("id"))
	if err != nil {
		return execution.ID{}, badRequest(err)
	}

	return id, nil
}
