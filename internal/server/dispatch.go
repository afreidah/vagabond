// -------------------------------------------------------------------------------
// Dispatch Routes
//
// Author: Alex Freidah
//
// Starting runs and reading them back. A run starts in its own goroutine and
// the request returns its dispatch ID; its executions are read from
// /v1/dispatch/{id} as they appear.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/jobspec"
)

// -------------------------------------------------------------------------
// ROUTES
// -------------------------------------------------------------------------

// dispatchJob starts a run of a registered job's current version, with its
// metadata checked against what the job declares.
func (s *Server) dispatchJob(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req api.DispatchRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}

	ns, err := s.namespace(r, nil)
	if err != nil {
		return nil, err
	}

	name := r.PathValue("name")

	version, err := jobs.Current(r.Context(), s.jobs, ns, name)
	if err != nil {
		return nil, err
	}

	parsed, diags, err := jobs.ForDispatch(fmt.Sprintf("%s (version %d)", name, version.Version), version.Source, req.Meta)
	if err != nil {
		return nil, badRequest(err)
	}

	if diags.HasErrors() {
		return nil, invalidJob(diags)
	}

	return s.launch(r.Context(), dispatch.Origin{Namespace: ns, JobVersion: version.Version}, &parsed.Spec.Jobs[0], req.Meta)
}

// runJob starts a run of a job file without registering it, the API's
// equivalent of job run.
func (s *Server) runJob(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req api.RunRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}

	parsed, diags := jobs.Load("job", []byte(req.Source), req.Meta)
	if diags.HasErrors() {
		return nil, invalidJob(diags)
	}

	j := &parsed.Spec.Jobs[0]

	ns, err := s.namespace(r, j)
	if err != nil {
		return nil, err
	}

	return s.launch(r.Context(), dispatch.Origin{Namespace: ns}, j, req.Meta)
}

// dispatchStatus answers with a run's record, including why it got no answer,
// and every execution it has created so far, oldest first.
func (s *Server) dispatchStatus(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := executionID(r)
	if err != nil {
		return nil, err
	}

	rec, err := s.executions.GetDispatch(r.Context(), id)
	if err != nil {
		return nil, err
	}

	runs, err := s.executions.DispatchExecutions(r.Context(), id)
	if err != nil {
		return nil, err
	}

	return dispatchOf(rec, runs), nil
}

// -------------------------------------------------------------------------
// RUNNING
// -------------------------------------------------------------------------

// launch records a run of j and starts it in the background. The record exists
// before this returns, and the run's own context outlives the request.
func (s *Server) launch(
	ctx context.Context, origin dispatch.Origin, j *job.Job, meta map[string]string,
) (any, error) {
	origin, err := s.dispatcher.Begin(ctx, origin, j)
	if err != nil {
		return nil, err
	}

	id := origin.Dispatch

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.track(id, cancel)

	logger := s.logger.With("dispatch", id, "job", j.Name, "version", origin.JobVersion, "namespace", origin.Namespace)
	logger.InfoContext(ctx, "dispatch started")

	go func() {
		defer s.untrack(id)
		defer cancel()

		outcome, err := s.dispatcher.Run(runCtx, origin, j, jobspec.EvalContext(meta))
		s.dispatcher.Finish(runCtx, origin, outcome, err)
		logFinished(runCtx, logger, outcome, err)
	}()

	return api.DispatchResponse{
		Namespace:  origin.Namespace,
		Job:        j.Name,
		JobVersion: origin.JobVersion,
		DispatchID: id.String(),
	}, nil
}

// logFinished records how a dispatch ended: how many tasks ran, whether they
// all succeeded, and the error when the run never got an answer.
func logFinished(ctx context.Context, logger *slog.Logger, outcome *dispatch.JobOutcome, err error) {
	var ran int

	succeeded := false
	if outcome != nil {
		ran = len(outcome.Tasks)
		succeeded = outcome.Succeeded()
	}

	if err != nil {
		logger.WarnContext(ctx, "dispatch finished without an answer", "tasks", ran, "error", err)

		return
	}

	logger.InfoContext(ctx, "dispatch finished", "tasks", ran, "succeeded", succeeded)
}

// track records the cancel function of a dispatch starting in this process,
// so cancelling one of its executions can stop it.
func (s *Server) track(id execution.ID, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.running[id] = cancel
}

// untrack forgets a dispatch once its run has returned, whatever the
// outcome.
func (s *Server) untrack(id execution.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.running, id)
}

// stopDispatch cancels a dispatch running in this process, and reports whether
// there was one to cancel.
func (s *Server) stopDispatch(id execution.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	cancel, ok := s.running[id]
	if ok {
		cancel()
	}

	return ok
}
