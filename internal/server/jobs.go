// -------------------------------------------------------------------------------
// Job Routes
//
// Author: Alex Freidah
//
// Registering, listing, reading and stopping registered jobs. Registering
// validates the job in full before storing it.
// -------------------------------------------------------------------------------

package server

import (
	"net/http"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/jobs"
)

// statusExecutions is how many recent executions a job's status carries.
const statusExecutions = 20

// registerJob stores a job's source as a new version when it changed, and
// answers with the version it is at.
func (s *Server) registerJob(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req api.RegisterRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}

	parsed, diags := jobs.ForRegister("job", []byte(req.Source))
	if diags.HasErrors() {
		return nil, invalidJob(diags)
	}

	j := &parsed.Spec.Jobs[0]

	ns, err := s.namespace(r, j)
	if err != nil {
		return nil, err
	}

	version, changed, err := s.jobs.Register(r.Context(), ns, j.Name, []byte(req.Source), s.now())
	if err != nil {
		return nil, err
	}

	return api.RegisterResponse{Namespace: ns, Name: j.Name, Version: version, Changed: changed}, nil
}

// listJobs answers with every job registered in the namespace, stopped ones
// included.
func (s *Server) listJobs(_ http.ResponseWriter, r *http.Request) (any, error) {
	ns, err := s.namespace(r, nil)
	if err != nil {
		return nil, err
	}

	all, err := s.jobs.Jobs(r.Context(), ns)
	if err != nil {
		return nil, err
	}

	out := make([]api.Job, 0, len(all))
	for _, j := range all {
		out = append(out, jobOf(j))
	}

	return out, nil
}

// jobStatus answers with a job, its versions newest first, and its most recent
// executions.
func (s *Server) jobStatus(_ http.ResponseWriter, r *http.Request) (any, error) {
	ns, err := s.namespace(r, nil)
	if err != nil {
		return nil, err
	}

	name := r.PathValue("name")

	j, err := s.jobs.Job(r.Context(), ns, name)
	if err != nil {
		return nil, err
	}

	versions, err := s.jobs.Versions(r.Context(), ns, name)
	if err != nil {
		return nil, err
	}

	runs, err := s.jobs.JobExecutions(r.Context(), ns, name, statusExecutions)
	if err != nil {
		return nil, err
	}

	status := api.JobStatus{
		Job:        jobOf(j),
		Versions:   make([]api.JobVersion, 0, len(versions)),
		Executions: executionsOf(runs),
	}

	for _, v := range versions {
		status.Versions = append(status.Versions, versionOf(v))
	}

	return status, nil
}

// stopJob deregisters a job and answers with its standing. Versions are kept,
// and registering it again revives it.
func (s *Server) stopJob(_ http.ResponseWriter, r *http.Request) (any, error) {
	ns, err := s.namespace(r, nil)
	if err != nil {
		return nil, err
	}

	name := r.PathValue("name")

	if err := s.jobs.Stop(r.Context(), ns, name, s.now()); err != nil {
		return nil, err
	}

	j, err := s.jobs.Job(r.Context(), ns, name)
	if err != nil {
		return nil, err
	}

	return jobOf(j), nil
}
