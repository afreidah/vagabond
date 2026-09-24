// -------------------------------------------------------------------------------
// Outcomes
//
// Author: Alex Freidah
//
// What a dispatch produced, including the providers it gave up on. A caller
// that only ever sees the provider which answered cannot tell a clean first
// attempt from a job that limped through two outages to get there, and the
// second is worth knowing about before it becomes every job.
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

// ErrNoCandidates reports a task no configured provider can run.
//
// Distinct from every provider failing: nothing was attempted, because nothing
// was eligible. The rejections carried on the outcome say why, and they are
// the actionable part.
var ErrNoCandidates = errors.New("no provider can run this task")

// ErrExhausted reports that every attempt ended in an infrastructure failure.
var ErrExhausted = errors.New("every attempt failed")

// ErrNoProvider reports a ranked candidate the registry has no plugin for,
// which means the registry and the ranking disagree about what exists.
var ErrNoProvider = errors.New("provider is not registered")

// -------------------------------------------------------------------------
// OUTCOMES
// -------------------------------------------------------------------------

// Attempt is one provider's turn at a task.
//
// Err is nil on the attempt that produced a result. Every other attempt
// carries the failure that ended it, which is what the decision to try
// somewhere else was made on.
//
// Refused marks a provider passed over because the ledger would not reserve
// its quota. Nothing was submitted, so it is not counted as a try.
type Attempt struct {
	Provider string
	ID       execution.ID
	Err      error // nil on the attempt that answered
	Refused  bool  // the ledger refused; nothing was submitted
}

// TaskOutcome is what became of one task.
//
// Result is nil when no attempt produced one, in which case the error returned
// alongside says whether nothing was eligible or everything failed.
//
// Rejections is carried so that a task with nowhere to run explains itself the
// way a plan does, rather than reporting only that it found nothing.
type TaskOutcome struct {
	Task       string
	Provider   string
	ID         execution.ID
	Result     *execution.Result
	Streamed   bool                  // output was shown live; do not print it again
	Attempts   []Attempt             // every provider tried, including the one that answered
	Rejections []scheduler.Rejection // why the ineligible providers were never tried
}

// Succeeded reports whether the task ran and reported success.
//
// False when no result was produced at all, so a caller can ask this without
// checking for nil first.
func (o *TaskOutcome) Succeeded() bool {
	return o.Result != nil && o.Result.Succeeded()
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

// JobOutcome is what became of every task in a job.
//
// Tasks are in the order they ran, which is the order they were declared. A
// job that stopped early has fewer outcomes than it has tasks.
type JobOutcome struct {
	Job   string
	Tasks []TaskOutcome
}

// Succeeded reports whether every task ran and reported success.
//
// False for a job that stopped early, because the tasks that never ran cannot
// be said to have passed, and false for a job with no tasks at all.
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
