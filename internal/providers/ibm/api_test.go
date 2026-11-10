// -------------------------------------------------------------------------------
// Code Engine API Type Tests
//
// Author: Alex Freidah
//
// How a job run's status, failure reason and timestamps are read. The calls
// themselves are exercised through the Provider methods.
// -------------------------------------------------------------------------------

package ibm

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// STATE
// -------------------------------------------------------------------------

// Each Code Engine status maps to one execution state.
func TestJobRun_State(t *testing.T) {
	t.Parallel()

	cases := map[string]execution.State{
		"pending":   execution.StateAccepted,
		"running":   execution.StateRunning,
		"completed": execution.StateSucceeded,
		"failed":    execution.StateFailed,
	}

	for status, want := range cases {
		run := &jobRun{Name: "run1", Status: status}

		got, err := run.state()
		if err != nil || got != want {
			t.Errorf("state(%q) = %q, %v; want %q", status, got, err, want)
		}
	}

	if _, err := (&jobRun{Name: "run1", Status: "suspended"}).state(); err == nil {
		t.Error("an unknown status was given a state")
	}
}

// -------------------------------------------------------------------------
// EXIT CODE
// -------------------------------------------------------------------------

// The fallback exit code is 0 for a completed run and N for
// ContainerExitedCode<N>; any other reason has none.
func TestJobRun_FailureExit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status, reason string
		want           *int
	}{
		{"completed", "", new(0)},
		{"failed", "ContainerExitedCode3", new(3)},
		{"failed", "ContainerExitedCode137", new(137)},
		{"failed", "OOMKilled", nil},
		{"failed", "ContainerExitedCodeX", nil},
		{"failed", "", nil},
		{"running", "", nil},
	}

	for _, tc := range cases {
		run := &jobRun{Status: tc.status}
		run.StatusDetails.Indices = map[string]indexDetails{"0": {LastFailureReason: tc.reason}}

		got := run.failureExit()

		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s/%q: failureExit() = %d, want none", tc.status, tc.reason, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%s/%q: failureExit() = %v, want %d", tc.status, tc.reason, got, *tc.want)
		}
	}
}

// -------------------------------------------------------------------------
// TIMES
// -------------------------------------------------------------------------

// Run and instance timestamps parse, and an absent one is zero.
func TestJobRun_Times(t *testing.T) {
	t.Parallel()

	run := &jobRun{}
	run.StatusDetails.StartTime = "2026-11-10T05:57:46Z"
	run.StatusDetails.Indices = map[string]indexDetails{"0": {StartedAt: "2026-11-10T05:57:46Z"}}

	start := time.Date(2026, 11, 10, 5, 57, 46, 0, time.UTC)

	if !run.started().Equal(start) || !run.instance().started().Equal(start) {
		t.Errorf("started = %s / %s, want %s", run.started(), run.instance().started(), start)
	}

	if !run.ended().IsZero() || !run.instance().finished().IsZero() {
		t.Error("an absent end time is not zero")
	}

	if !(&jobRun{}).instance().started().IsZero() {
		t.Error("a run with no instance reports a start")
	}
}
