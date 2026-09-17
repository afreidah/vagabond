// -------------------------------------------------------------------------------
// Execution Result
//
// Author: Alex Freidah
//
// What a finished execution produced, kept separate from Status because the
// two arrive from different places. A container job's result is POSTed back by
// the bootstrap; a function or worker result comes straight out of the
// invocation. Status is what Vagabond observed, Result is what the task said.
// -------------------------------------------------------------------------------

package execution

import "time"

// Result is the output of a finished execution.
//
// ExitCode is a pointer because a worker execution has none: a Wasm sandbox has
// no process to exit. Nil means the family has no exit status, not that the
// task exited zero.
//
// Logs are bounded and may be truncated. Truncation is deliberate rather than a
// limitation, so that CockroachDB stays the only datastore: a caller needing
// complete output should be writing it somewhere itself.
type Result struct {
	ID       ID
	ExitCode *int
	Duration time.Duration

	Logs          []byte
	LogsTruncated bool
}

// Succeeded reports whether the task itself reported success.
//
// A worker execution has no exit code, so reaching this point at all is its
// success. Anything that failed to produce a result never gets here: that is a
// Status question, and the answer is in State.
func (r *Result) Succeeded() bool {
	if r.ExitCode == nil {
		return true
	}

	return *r.ExitCode == 0
}
