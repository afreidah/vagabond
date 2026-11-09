// -------------------------------------------------------------------------------
// Outcomes
//
// Author: Alex Freidah
//
// What a dispatch produced, including the providers it gave up on, so a
// caller can tell a clean first attempt from a job rerouted around outages.
// -------------------------------------------------------------------------------

package dispatch

import (
	"errors"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrNoCandidates reports a task no configured provider can run, so nothing
// was attempted; the outcome's rejections say why.
var ErrNoCandidates = errors.New("no provider can run this task")

// ErrExhausted reports that every attempt ended in an infrastructure failure.
var ErrExhausted = errors.New("every attempt failed")

// ErrNoProvider reports a ranked candidate the registry has no plugin for,
// which means the registry and the ranking disagree about what exists.
var ErrNoProvider = errors.New("provider is not registered")

// -------------------------------------------------------------------------
// OUTCOMES
// -------------------------------------------------------------------------

// Attempt is one provider's turn at a task. A refused attempt is not counted
// as a try.
type Attempt struct {
	Provider string
	ID       execution.ID
	Err      error // nil on the attempt that answered
	Refused  bool  // the ledger refused; nothing was submitted
}

// TaskOutcome is what became of one task. Result is nil when no attempt
// produced one, and the error returned alongside says whether nothing was
// eligible or everything failed.
type TaskOutcome struct {
	Task       string
	Provider   string
	ID         execution.ID
	State      execution.State // the terminal state the provider reported; the verdict
	Result     *execution.Result
	Attempts   []Attempt             // every provider tried, including the one that answered
	Rejections []scheduler.Rejection // why the ineligible providers were never tried
}

// Succeeded reports whether the task ran and the provider reported it
// succeeded. False when no result was produced, so no nil check is needed.
func (o *TaskOutcome) Succeeded() bool {
	return o.Result != nil && o.State == execution.StateSucceeded
}

// Rerouted reports whether this task was submitted to more than one provider.
func (o *TaskOutcome) Rerouted() bool {
	return o.Tried() > 1
}

// Tried counts the providers the task was actually submitted to, leaving out
// the ones the ledger refused.
func (o *TaskOutcome) Tried() int {
	n := 0

	for i := range o.Attempts {
		if !o.Attempts[i].Refused {
			n++
		}
	}

	return n
}

// JobOutcome is what became of every task in a job, in declaration order. A
// job that stopped early has fewer outcomes than tasks.
type JobOutcome struct {
	Job      string
	Dispatch execution.ID
	Tasks    []TaskOutcome
}

// Succeeded reports whether every task ran and reported success. False for a
// job that stopped early or has no tasks.
func (o *JobOutcome) Succeeded() bool {
	if len(o.Tasks) == 0 {
		return false
	}

	for i := range o.Tasks {
		if !o.Tasks[i].Succeeded() {
			return false
		}
	}

	return true
}
