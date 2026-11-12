// -------------------------------------------------------------------------------
// Execution Status Rendering Tests
//
// Author: Alex Freidah
//
// The fields an execution's record shows only when it has them, which a fake
// provider through a real server does not produce.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/api"
)

// A result with a platform's reason shows it, and one without has no Reason row.
func TestRecord_Reason(t *testing.T) {
	ended := &api.Execution{ID: "exec-1", State: executionFailed, HasResult: true, ExitCode: new(137)}

	if got := record(ended); strings.Contains(got, "Reason") {
		t.Errorf("a result with no reason shows one:\n%s", got)
	}

	ended.Reason = "OOMKilled"

	if got := record(ended); !strings.Contains(got, "Reason      = OOMKilled") {
		t.Errorf("the reason is missing:\n%s", got)
	}
}
