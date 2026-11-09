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

import (
	"time"

	"github.com/afreidah/vagabond/internal/quota"
)

// Result is the output of a finished execution.
//
// A nil ExitCode means the family has no exit status, as in a Wasm worker, not
// that the task exited zero. Logs are bounded so CockroachDB stays the only
// datastore. Billed is what the platform reported it charged, when it says;
// when nil the ledger prices the declared shape over Duration, and when set it
// supersedes that estimate even above the reservation.
type Result struct {
	ID       ID
	ExitCode *int
	Duration time.Duration
	Billed   *quota.Execution

	Logs          []byte
	LogsTruncated bool
}
