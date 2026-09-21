// -------------------------------------------------------------------------------
// Cloud Run API Types
//
// Author: Alex Freidah
//
// Only the fields this plugin reads, not Google's whole schema. A generated
// client would carry all of it; these were taken from what a spike actually
// got back, so every one of them is known to exist rather than assumed from
// documentation.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// runExecution is one run of a job.
//
// Name is Google's, not ours: a job called vagabond-<uuid> produces an
// execution called vagabond-<uuid>-nhrzk, and the suffix is theirs to choose.
// That is why it has to be listed rather than derived.
type runExecution struct {
	Name           string `json:"name"`
	StartTime      string `json:"startTime"`
	CompletionTime string `json:"completionTime"`

	RunningCount   int `json:"runningCount"`
	SucceededCount int `json:"succeededCount"`
	FailedCount    int `json:"failedCount"`
	CancelledCount int `json:"cancelledCount"`
}

// state translates Cloud Run's counters into ours.
//
// Counters rather than conditions, because a condition's state is prose that
// has changed spelling between API versions while the counts have not. An
// execution with a completion time and nothing succeeded has failed, whatever
// it says about itself.
func (e *runExecution) state() execution.State {
	switch {
	case e.CompletionTime == "":
		if e.RunningCount > 0 {
			return execution.StateRunning
		}

		// Accepted rather than running: Cloud Run spends a minute or more
		// provisioning before a container starts, and reporting that as
		// running would make a stuck pull look like a slow test suite.
		return execution.StateAccepted

	case e.CancelledCount > 0:
		return execution.StateCancelled

	case e.FailedCount > 0:
		return execution.StateFailed

	default:
		return execution.StateSucceeded
	}
}

// execution finds the single execution belonging to one of our jobs.
//
// Listing rather than addressing, because the name is Google's. Submit creates
// one job per execution, so more than one here would mean somebody ran the job
// by hand and the newest is the one we care about.
func (p *Provider) execution(ctx context.Context, jobName string) (*runExecution, error) {
	var out struct {
		Executions []runExecution `json:"executions"`
	}

	url := p.cfg.jobURL(p.runURL, jobName) + "/executions"
	if err := p.call(ctx, http.MethodGet, url, nil, &out); err != nil {
		return nil, err
	}

	if len(out.Executions) == 0 {
		return nil, plugin.Infrastructure(
			fmt.Errorf("job %s has no execution", jobName))
	}

	newest := &out.Executions[0]
	for i := range out.Executions {
		if parseTime(out.Executions[i].StartTime).After(parseTime(newest.StartTime)) {
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

// exitCode returns what the container exited with.
//
// A pointer because absence is meaningful: a task killed before its process
// ran has no exit code, and reporting zero would call that a success.
func (t *runTask) exitCode() *int {
	return t.LastAttemptResult.ExitCode
}

// task finds the single task of an execution.
//
// One, because every job is submitted with taskCount 1. Vagabond's own
// parallelism is one task per provider, not one execution split across
// several.
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

// -------------------------------------------------------------------------
// TIME
// -------------------------------------------------------------------------

// parseTime reads one of Google's RFC 3339 timestamps.
//
// An unparseable or absent time is the zero value rather than an error. These
// are absent by design while a run is in progress, and a status that failed to
// decode because a job had not started yet would be useless.
func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}

	return parsed
}
