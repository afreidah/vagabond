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

// The states a run of a job moves through.
const (
	DispatchRunning    DispatchState = "running"
	DispatchSucceeded  DispatchState = "succeeded"  // every task ran and passed
	DispatchFailed     DispatchState = "failed"     // a task ran and failed
	DispatchUnanswered DispatchState = "unanswered" // no answer: refused, unreachable or interrupted
)

// Dispatch is one run of a job. Error says why an unanswered run got no
// answer; Ended is zero while it runs. Tasks is how many the job declares.
//
// The owner renews LeaseUntil until the dispatch is done; a lapsed lease is
// claimed by another server. Epoch counts claims. Ended is the verdict, Done
// when every execution finished through release. CancelRequested is a cancel
// for the owner to act on.
type Dispatch struct {
	ID         ID
	Namespace  string
	Job        string
	JobVersion int64
	Tasks      int
	State      DispatchState
	Error      string
	Owner      string
	Epoch      int64
	LeaseUntil time.Time
	Created    time.Time
	Ended      time.Time

	CancelRequested time.Time
	Done            time.Time
}

// Lease is the process holding a dispatch and the claim it holds it under.
type Lease struct {
	Owner string
	Epoch int64
}

// Lease returns the lease d is held under: its owner and the claim it holds
// it by.
func (d *Dispatch) Lease() Lease {
	return Lease{Owner: d.Owner, Epoch: d.Epoch}
}
