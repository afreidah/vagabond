// -------------------------------------------------------------------------------
// API Types
//
// Author: Alex Freidah
//
// Request and response bodies of every /v1 route. Field names are the JSON
// keys, untagged, and durations are nanoseconds.
// -------------------------------------------------------------------------------

package api

import "time"

// -------------------------------------------------------------------------
// JOBS
// -------------------------------------------------------------------------

// RegisterRequest is the body of POST /v1/jobs: a job file's source.
type RegisterRequest struct {
	Source string
}

// RegisterResponse reports the job's version after registering and whether
// it changed.
type RegisterResponse struct {
	Namespace string
	Name      string
	Version   int64
	Changed   bool
}

// Job is a registered job's standing.
type Job struct {
	Namespace string
	Name      string
	Version   int64
	Stopped   bool
	Updated   time.Time
}

// JobVersion is one registered version, without its source.
type JobVersion struct {
	Version    int64
	Registered time.Time
}

// JobStatus is GET /v1/job/{name}: the job, its versions newest first, and its
// most recent executions.
type JobStatus struct {
	Job
	Versions   []JobVersion
	Executions []Execution
}

// -------------------------------------------------------------------------
// DISPATCH
// -------------------------------------------------------------------------

// DispatchRequest is the body of POST /v1/job/{name}/dispatch.
type DispatchRequest struct {
	Meta map[string]string
}

// RunRequest is the body of POST /v1/jobs/run: a job file run once, not
// registered.
type RunRequest struct {
	Source string
	Meta   map[string]string
}

// DispatchResponse identifies a run that has started. Its executions are read
// from GET /v1/dispatch/{id} as they are created.
type DispatchResponse struct {
	Namespace  string
	Job        string
	JobVersion int64
	DispatchID string
}

// Dispatch is GET /v1/dispatch/{id}: one run and every execution it created,
// oldest first. State is running, succeeded, failed, or unanswered, and Error
// says why an unanswered run got no answer.
type Dispatch struct {
	DispatchID string
	Namespace  string
	Job        string
	JobVersion int64
	State      string
	Error      string
	Created    time.Time
	Ended      *time.Time
	Executions []Execution
}

// -------------------------------------------------------------------------
// PLANS
// -------------------------------------------------------------------------

// PlanRequest is the body of POST /v1/jobs/plan: a job file's Source, or a
// registered job's Name, never both.
type PlanRequest struct {
	Source string
	Name   string
	Meta   map[string]string
}

// Plan is where every task would run.
type Plan struct {
	Tasks []TaskPlan
}

// TaskPlan is one task's admission and ranking. Selected is empty when nothing
// can run it, and Retryable then says whether that may change on its own.
type TaskPlan struct {
	Job           string
	Task          string
	Driver        string
	Candidates    []Candidate
	Rejections    []Rejection
	Selected      string
	EstimatedCost int64
	Retryable     bool
}

// Candidate is an admitted provider and why it ranked where it did. Score is
// the percentage a plan prints.
type Candidate struct {
	Provider string
	Score    int
	Scores   []Score
	Observed time.Time
}

// Score is one scorer's contribution, in [0,1].
type Score struct {
	Name  string
	Value float64
}

// Rejection is a provider that cannot run the task, the first rule it failed,
// and any others.
type Rejection struct {
	Provider string
	Reason   string
	Detail   string
	Also     []string
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// Execution is one attempt of one task. The result fields are zero until the
// execution ends with one; its output is read from /logs.
type Execution struct {
	ID         string
	Namespace  string
	Job        string
	JobVersion int64
	DispatchID string
	Task       string
	Provider   string
	Attempt    int
	PreviousID string
	State      string
	ProviderID string
	Failure    string

	Created time.Time
	Started *time.Time
	Ended   *time.Time
	Updated time.Time

	HasResult     bool
	ExitCode      *int
	Duration      time.Duration
	Billed        *Billed
	LogsTruncated bool
}

// Billed is what the platform reported it charged.
type Billed struct {
	CPU      int
	Memory   int
	Duration time.Duration
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// Error is every non-2xx body. Diagnostics carry a job file's parse and
// validation problems, each against its line.
type Error struct {
	Error       string
	Diagnostics []Diagnostic `json:",omitempty"`
}

// Diagnostic is one problem with a job file.
type Diagnostic struct {
	Severity string
	Summary  string
	Detail   string
	Range    string `json:",omitempty"`
}
