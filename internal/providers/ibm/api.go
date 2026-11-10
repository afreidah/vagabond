// -------------------------------------------------------------------------------
// Code Engine API Types
//
// Author: Alex Freidah
//
// The v2 job-run calls, and only the fields this plugin reads, each confirmed
// against a real job run rather than assumed from documentation.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// exitReasonPrefix starts the failure reason Code Engine records for a container
// that exited non-zero, followed by the code: ContainerExitedCode3.
const exitReasonPrefix = "ContainerExitedCode"

// -------------------------------------------------------------------------
// JOB RUNS
// -------------------------------------------------------------------------

// jobRun is one job run as Code Engine reports it.
type jobRun struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	StatusDetails struct {
		StartTime      string                  `json:"start_time"`
		CompletionTime string                  `json:"completion_time"`
		Indices        map[string]indexDetails `json:"indices_details"`
	} `json:"status_details"`
}

// indexDetails is one instance of a job run. The plugin runs exactly one, at
// index 0.
type indexDetails struct {
	Status            string `json:"status"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at"`
	LastFailureReason string `json:"last_failure_reason"`
}

// state translates Code Engine's status into an execution state. An unknown
// status is an error rather than a guess, so a schema change is loud instead
// of misread.
func (r *jobRun) state() (execution.State, error) {
	switch r.Status {
	case "pending": // also a run held back while the project is over its quota
		return execution.StateAccepted, nil
	case "running":
		return execution.StateRunning, nil
	case "completed":
		return execution.StateSucceeded, nil
	case "failed":
		return execution.StateFailed, nil
	default:
		return "", plugin.Internal(fmt.Errorf("job run %s reports unknown status %q", r.Name, r.Status))
	}
}

// started is when the run began, zero while it has not.
func (r *jobRun) started() time.Time {
	t, _ := time.Parse(time.RFC3339, r.StatusDetails.StartTime)

	return t
}

// ended is when the run was reported finished, zero until it is.
func (r *jobRun) ended() time.Time {
	t, _ := time.Parse(time.RFC3339, r.StatusDetails.CompletionTime)

	return t
}

// instance is the run's single instance, the zero value until Code Engine
// reports one.
func (r *jobRun) instance() indexDetails {
	return r.StatusDetails.Indices["0"]
}

// failureExit is the exit code read from the run's own status, for when the
// pod holding the real one is gone: 0 for a completed run, N from
// ContainerExitedCode<N>, and nil for any other reason, OOMKilled among them.
func (r *jobRun) failureExit() *int {
	if r.Status == "completed" {
		return new(0)
	}

	code, ok := strings.CutPrefix(r.instance().LastFailureReason, exitReasonPrefix)
	if !ok {
		return nil
	}

	n, err := strconv.Atoi(code)
	if err != nil {
		return nil
	}

	return new(n)
}

// started is when the instance began, zero while it has not.
func (d indexDetails) started() time.Time {
	t, _ := time.Parse(time.RFC3339, d.StartedAt)

	return t
}

// finished is when the instance ended, zero while it runs.
func (d indexDetails) finished() time.Time {
	t, _ := time.Parse(time.RFC3339, d.FinishedAt)

	return t
}

// -------------------------------------------------------------------------
// CALLS
// -------------------------------------------------------------------------

// createJobRun submits a job run and returns Code Engine's first view of it.
// A name already taken answers 409, which classify leaves internal.
func (p *Provider) createJobRun(ctx context.Context, spec *jobRunSpec) (*jobRun, error) {
	var run jobRun
	if err := p.call(ctx, http.MethodPost, p.cfg.jobRunsURL(p.apiURL), spec, &run); err != nil {
		return nil, err
	}

	return &run, nil
}

// getJobRun reads one job run. A run Code Engine has no record of wraps
// ErrUnknownExecution, so the reaper drops its reservation.
func (p *Provider) getJobRun(ctx context.Context, name string) (*jobRun, error) {
	var run jobRun

	err := p.call(ctx, http.MethodGet, p.cfg.jobRunURL(p.apiURL, name), nil, &run)
	if errors.Is(err, plugin.ErrNotFound) {
		return nil, plugin.Internal(fmt.Errorf("%w: job run %s does not exist",
			plugin.ErrUnknownExecution, name))
	}

	if err != nil {
		return nil, err
	}

	return &run, nil
}

// deleteJobRun removes a job run and its pods, which stops it if it is still
// running. A run already gone is not an error.
func (p *Provider) deleteJobRun(ctx context.Context, name string) error {
	err := p.call(ctx, http.MethodDelete, p.cfg.jobRunURL(p.apiURL, name), nil, nil)
	if errors.Is(err, plugin.ErrNotFound) {
		return nil
	}

	return err
}
