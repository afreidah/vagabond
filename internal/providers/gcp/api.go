// -------------------------------------------------------------------------------
// Cloud Run API Types
//
// Author: Alex Freidah
//
// Only the fields this plugin reads, not Google's whole schema. Each was taken
// from a real API response rather than assumed from documentation.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// runExecution is one run of a job. Google picks its Name: vagabond-<uuid>
// runs as vagabond-<uuid>-nhrzk, so it is listed rather than derived.
type runExecution struct {
	Name           string `json:"name"`
	StartTime      string `json:"startTime"`
	CompletionTime string `json:"completionTime"`

	RunningCount   int `json:"runningCount"`
	SucceededCount int `json:"succeededCount"`
	FailedCount    int `json:"failedCount"`
	CancelledCount int `json:"cancelledCount"`
}

// state translates Cloud Run's counters into an execution state. Counters
// rather than conditions, whose prose has changed between API versions.
func (e *runExecution) state() execution.State {
	switch {
	case e.CompletionTime == "":
		// runningCount hits zero before completionTime is set
		if e.RunningCount > 0 || e.StartTime != "" {
			return execution.StateRunning
		}

		// Provisioning takes a minute; running would hide a stuck pull
		return execution.StateAccepted

	case e.CancelledCount > 0:
		return execution.StateCancelled

	case e.FailedCount > 0:
		return execution.StateFailed

	default:
		return execution.StateSucceeded
	}
}

// execution finds the newest execution of a Vagabond job; there is normally
// one, since Submit creates a job per execution.
//
// No job, or no execution, is plugin.ErrUnknownExecution. The empty case stays
// infrastructure, since right after Submit the listing can lag the run.
func (p *Provider) execution(ctx context.Context, jobName string) (*runExecution, error) {
	var out struct {
		Executions []runExecution `json:"executions"`
	}

	url := p.cfg.jobURL(p.runURL, jobName) + "/executions"
	if err := p.call(ctx, http.MethodGet, url, nil, &out); err != nil {
		if errors.Is(err, plugin.ErrNotFound) {
			return nil, plugin.Internal(
				fmt.Errorf("%w: job %s does not exist", plugin.ErrUnknownExecution, jobName))
		}

		return nil, err
	}

	if len(out.Executions) == 0 {
		return nil, plugin.Infrastructure(
			fmt.Errorf("%w: job %s has no execution", plugin.ErrUnknownExecution, jobName))
	}

	newest := &out.Executions[0]
	for i := range out.Executions {
		if plugin.ParseTime(out.Executions[i].StartTime).After(plugin.ParseTime(newest.StartTime)) {
			newest = &out.Executions[i]
		}
	}

	return newest, nil
}

// -------------------------------------------------------------------------
// TASKS
// -------------------------------------------------------------------------

// runTask is one attempt at the work, and where the exit code lives.
type runTask struct {
	StartTime      string `json:"startTime"`
	CompletionTime string `json:"completionTime"`

	LastAttemptResult struct {
		ExitCode *int `json:"exitCode"`
	} `json:"lastAttemptResult"`
}

// exitCode returns what the container exited with, nil for a task killed before
// its process ran, which zero would report as a success.
func (t *runTask) exitCode() *int {
	return t.LastAttemptResult.ExitCode
}

// task finds the single task of an execution; every job is submitted with
// taskCount 1.
func (p *Provider) task(ctx context.Context, executionName string) (*runTask, error) {
	var out struct {
		Tasks []runTask `json:"tasks"`
	}

	url := p.runURL + "/v2/" + executionName + "/tasks"
	if err := p.call(ctx, http.MethodGet, url, nil, &out); err != nil {
		return nil, err
	}

	if len(out.Tasks) == 0 {
		return nil, plugin.Infrastructure(
			fmt.Errorf("execution %s has no task", executionName))
	}

	return &out.Tasks[0], nil
}
