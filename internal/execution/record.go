// -------------------------------------------------------------------------------
// Execution Record
//
// Author: Alex Freidah
//
// What is persisted about one attempt: its status, where it came from, and what
// it produced. One record per execution ID, so a rerouted task is several
// records linked by Previous.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
	"time"
)

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
// CPU and Memory are the task's declared shape, which a result without a bill
// is charged at. Released is when what the provider left behind for this
// execution was cleaned up, zero until then.
//
// The output is stored apart from the record. LogsAt is when it was captured,
// zero until then. LogsStart is the first byte of it still stored, above zero
// once output past the ceiling was dropped from the front. LogsTruncated means
// the provider gave less than the task printed.
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
	CPU        int
	Memory     int

	Result   *Result
	Failure  string
	Released time.Time

	LogsAt        time.Time
	LogsStart     int64
	LogsTruncated bool
}
