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
// answer; Ended is zero while it runs. Tasks is how many the job declares.
//
// A running dispatch is leased: Owner names the process running it and renews
// LeaseUntil while it does. A lease that lapses means the owner died, and a
// server takes the dispatch over.
type Dispatch struct {
	ID         ID
	Namespace  string
	Job        string
	JobVersion int64
	Tasks      int
	State      DispatchState
	Error      string
	Owner      string
	LeaseUntil time.Time
	Created    time.Time
	Ended      time.Time
}
