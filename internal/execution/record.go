// -------------------------------------------------------------------------------
// Execution Record
//
// Author: Alex Freidah
//
// What is persisted about one attempt: its status, where it came from, and what
// it produced. One record per execution ID, so a rerouted task is several
// records linked by Previous, as Nomad links rescheduled allocations.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
)

// MaxStoredLogs is how much output a record keeps. The tail, because the end of
// a build is where it failed.
const MaxStoredLogs = 64 << 10

// ErrNotFound reports an execution no store has a record of.
var ErrNotFound = errors.New("execution not found")

// ErrStale reports an update written against a state the record has since left,
// so two writers cannot silently overwrite each other.
var ErrStale = errors.New("execution changed since it was read")

// Record is one execution as stored.
//
// JobVersion is 0 for a job that was run from a file rather than registered.
// Dispatch groups the records of one run of a job, every task and attempt.
// Previous is the zero ID on a task's first attempt. Failure is the class of
// the error that ended the attempt without an answer, empty when none did.
type Record struct {
	Status

	Namespace  string
	Job        string
	JobVersion int64
	Dispatch   ID
	Task       string
	Provider   string
	Attempt    int
	Previous   ID

	Result  *Result
	Failure string
}

// Bounded returns a copy of r holding at most MaxStoredLogs of output, the tail
// kept, marked truncated when anything was cut. Nil for nil.
func (r *Result) Bounded() *Result {
	if r == nil {
		return nil
	}

	out := *r

	if len(out.Logs) > MaxStoredLogs {
		out.Logs = out.Logs[len(out.Logs)-MaxStoredLogs:]
		out.LogsTruncated = true
	}

	out.Logs = append([]byte(nil), out.Logs...)

	return &out
}
