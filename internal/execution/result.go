// -------------------------------------------------------------------------------
// Execution Result
//
// Author: Alex Freidah
//
// What a finished execution produced, kept separate from Status because the
// two arrive from different places. A container job's result is read from its
// platform once it has ended; a function or worker result comes straight out
// of the invocation. Status is what Vagabond observed, Result is what the task
// said.
// -------------------------------------------------------------------------------

package execution

import (
	"time"

	"github.com/afreidah/vagabond/internal/quota"
)

// Result is the output of a finished execution.
//
// A nil ExitCode means the family has no exit status, as in a Wasm worker, not
// that the task exited zero. Billed is what the platform reported it charged,
// when it says; when nil the ledger prices the declared shape over Duration,
// and when set it supersedes that estimate even above the reservation. Reason
// is why the platform ended the workload, in its words, empty when the process
// exited on its own or the platform gave none.
//
// Logs carry the output of a provider that hands it over with the answer, as a
// function does; LogsTruncated means that copy is not all of it. The dispatcher
// stores it with the rest of the execution's output, so a result read back
// from a store carries none.
type Result struct {
	ID       ID
	ExitCode *int
	Duration time.Duration
	Billed   *quota.Execution
	Reason   string

	Logs          []byte
	LogsTruncated bool
}
