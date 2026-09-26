// -------------------------------------------------------------------------------
// Dispatch Record
//
// Author: Alex Freidah
//
// One run of a job, above its executions. A run refused before any task was
// submitted has no executions, so this is the only trace of it and of why.
// -------------------------------------------------------------------------------

package execution

import "time"

// DispatchState is where a run of a job stands.
type DispatchState string

const (
	DispatchRunning    DispatchState = "running"
	DispatchSucceeded  DispatchState = "succeeded"  // every task ran and passed
	DispatchFailed     DispatchState = "failed"     // a task ran and failed
	DispatchUnanswered DispatchState = "unanswered" // no answer: refused, unreachable or interrupted
)

// Dispatch is one run of a job. Error says why an unanswered run got no
// answer; Ended is zero while it runs.
type Dispatch struct {
	ID         ID
	Namespace  string
	Job        string
	JobVersion int64
	State      DispatchState
	Error      string
	Created    time.Time
	Ended      time.Time
}
