// -------------------------------------------------------------------------------
// Wire Conversions
//
// Author: Alex Freidah
//
// Internal types to their api counterparts. Nothing internal is encoded
// directly, so a change inside does not change the API by accident, and every
// time goes out in UTC.
// -------------------------------------------------------------------------------

package server

import (
	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// jobOf converts a registered job's standing to its wire form.
func jobOf(j *jobs.Job) api.Job {
	return api.Job{
		Namespace: j.Namespace,
		Name:      j.Name,
		Version:   j.Version,
		Stopped:   j.Stopped,
		Updated:   j.Updated.UTC(),
	}
}

// versionOf converts one registered version, without its source.
func versionOf(v *jobs.Version) api.JobVersion {
	return api.JobVersion{Version: v.Version, Registered: v.Created.UTC()}
}

// dispatchOf converts a run's record with the executions it created.
func dispatchOf(d *execution.Dispatch, runs []*execution.Record) api.Dispatch {
	out := api.Dispatch{
		DispatchID: d.ID.String(),
		Namespace:  d.Namespace,
		Job:        d.Job,
		JobVersion: d.JobVersion,
		State:      string(d.State),
		Error:      d.Error,
		Created:    d.Created.UTC(),
		Executions: executionsOf(runs),
	}

	if !d.Ended.IsZero() {
		ended := d.Ended.UTC()
		out.Ended = &ended
	}

	return out
}

// executionsOf converts a list of records, keeping their order.
func executionsOf(records []*execution.Record) []api.Execution {
	out := make([]api.Execution, 0, len(records))
	for _, r := range records {
		out = append(out, executionOf(r))
	}

	return out
}

// executionOf converts one record. Unset times and IDs stay empty, and the
// result fields are filled only once there is a result.
func executionOf(r *execution.Record) api.Execution {
	out := api.Execution{
		ID:         r.ID.String(),
		Namespace:  r.Namespace,
		Job:        r.Job,
		JobVersion: r.JobVersion,
		Task:       r.Task,
		Provider:   r.Provider,
		Attempt:    r.Attempt,
		State:      string(r.State),
		ProviderID: r.ProviderID,
		Failure:    r.Failure,
		Created:    r.ID.Created(),
		Updated:    r.UpdatedAt.UTC(),
	}

	if !r.Dispatch.IsZero() {
		out.DispatchID = r.Dispatch.String()
	}

	if !r.Previous.IsZero() {
		out.PreviousID = r.Previous.String()
	}

	if !r.StartedAt.IsZero() {
		started := r.StartedAt.UTC()
		out.Started = &started
	}

	if !r.EndedAt.IsZero() {
		ended := r.EndedAt.UTC()
		out.Ended = &ended
	}

	if res := r.Result; res != nil {
		out.HasResult = true
		out.ExitCode = res.ExitCode
		out.Duration = res.Duration
		out.LogsTruncated = res.LogsTruncated

		if b := res.Billed; b != nil {
			out.Billed = &api.Billed{CPU: b.CPU, Memory: b.Memory, Duration: b.Duration}
		}
	}

	return out
}

// taskPlanOf converts one task's plan: candidates best first, rejections by
// provider, and the selection when there is one.
func taskPlanOf(jobName string, task *job.Task, plan scheduler.Plan) api.TaskPlan {
	out := api.TaskPlan{
		Job:        jobName,
		Task:       task.Name,
		Driver:     string(task.Driver),
		Candidates: make([]api.Candidate, 0, len(plan.Ranking)),
		Rejections: make([]api.Rejection, 0, len(plan.Rejections)),
		Retryable:  plan.Retryable,
		Tiers:      plan.Tiers.String(),
	}

	for i := range plan.Ranking {
		out.Candidates = append(out.Candidates, candidateOf(&plan.Ranking[i]))
	}

	for i := range plan.Rejections {
		out.Rejections = append(out.Rejections, rejectionOf(&plan.Rejections[i]))
	}

	if selected, ok := plan.Ranking.Selected(); ok {
		out.Selected = selected.Provider
		out.EstimatedCost = int64(selected.EstimatedCost)
	}

	return out
}

// candidateOf converts an admitted provider, with each scorer's contribution
// and when its capabilities were observed.
func candidateOf(c *scheduler.ScoredCandidate) api.Candidate {
	out := api.Candidate{
		Provider: c.Provider,
		Tier:     c.Tier,
		Score:    c.Percent(),
		Observed: c.Capabilities.ObservedAt.UTC(),
	}

	for _, sc := range c.Scores {
		out.Scores = append(out.Scores, api.Score{Name: sc.Name, Value: sc.Value})
	}

	return out
}

// rejectionOf converts a rejected provider, the first rule it failed and any
// others it also failed.
func rejectionOf(r *scheduler.Rejection) api.Rejection {
	out := api.Rejection{Provider: r.Provider, Reason: string(r.Reason), Detail: r.Detail}

	for _, also := range r.Also {
		out.Also = append(out.Also, string(also))
	}

	return out
}
