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

// The execution table reads None. when empty, shows a node or a dash, and
// carries the job only when asked to.
func TestExecutionTable(t *testing.T) {
	if got := executionTable(nil, true); got != "None." {
		t.Errorf("executionTable(none) = %q, want None.", got)
	}

	runs := []api.Execution{
		{ID: "exec-2", Job: "ci", Task: "make", Provider: "homelab", Node: "nuc-01", Attempt: 2, State: "running"},
		{ID: "exec-1", Job: "ci", Task: "make", Provider: "gcp-cloud-run", Attempt: 1, State: "failed"},
	}

	lines := strings.Split(executionTable(runs, false), "\n")
	if len(lines) != 3 || strings.Contains(lines[0], "JOB") ||
		!strings.Contains(lines[1], "nuc-01") || !strings.Contains(lines[2], "gcp-cloud-run  -") {
		t.Errorf("executionTable(without job):\n%s", strings.Join(lines, "\n"))
	}

	if got := executionTable(runs, true); !strings.Contains(got, "ID      JOB") || !strings.Contains(got, "exec-2  ci ") {
		t.Errorf("executionTable(with job):\n%s", got)
	}
}
