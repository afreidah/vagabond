// -------------------------------------------------------------------------------
// Plan Route
//
// Author: Alex Freidah
//
// Where a job would run, from a file or a registered job, without dispatching
// or reserving anything. The same admission and ranking job plan prints.
// -------------------------------------------------------------------------------

package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// planJob answers with where each task of a job file or registered job would
// run, priced from the ledger but reserving nothing.
func (s *Server) planJob(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req api.PlanRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}

	j, ns, err := s.planned(r, &req)
	if err != nil {
		return nil, err
	}

	eval := jobspec.EvalContext(req.Meta)
	inputs := s.registry.Inputs(ns, s.ledger.PoolUsage)
	plan := api.Plan{Tasks: make([]api.TaskPlan, 0, len(j.Tasks))}

	for i := range j.Tasks {
		task := &j.Tasks[i]

		taskReq, diags := scheduler.NewRequest(task, j.Routing, eval)
		if diags.HasErrors() {
			return nil, invalidJob(diags)
		}

		plan.Tasks = append(plan.Tasks, taskPlanOf(j.Name, task, scheduler.PlanTask(taskReq, inputs)))
	}

	return plan, nil
}

// planned loads the job a plan request names, from its Source or its registered
// Name, and resolves the namespace it plans in.
func (s *Server) planned(r *http.Request, req *api.PlanRequest) (*job.Job, string, error) {
	switch {
	case req.Source != "" && req.Name != "":
		return nil, "", badRequest(errors.New("a plan names a Source or a Name, not both"))

	case req.Name != "":
		ns, err := s.namespace(r, nil)
		if err != nil {
			return nil, "", err
		}

		version, err := jobs.Current(r.Context(), s.jobs, ns, req.Name)
		if err != nil {
			return nil, "", err
		}

		parsed, diags, err := jobs.ForDispatch(
			fmt.Sprintf("%s (version %d)", req.Name, version.Version), version.Source, req.Meta)
		if err != nil {
			return nil, "", badRequest(err)
		}

		if diags.HasErrors() {
			return nil, "", invalidJob(diags)
		}

		return &parsed.Spec.Jobs[0], ns, nil

	default:
		parsed, diags := jobs.Load("job", []byte(req.Source), req.Meta)
		if diags.HasErrors() {
			return nil, "", invalidJob(diags)
		}

		j := &parsed.Spec.Jobs[0]

		ns, err := s.namespace(r, j)
		if err != nil {
			return nil, "", err
		}

		return j, ns, nil
	}
}
