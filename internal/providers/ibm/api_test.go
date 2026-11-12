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
// LAST FAILURE
// -------------------------------------------------------------------------

// The fallback is exit 0 for a completed run and N for ContainerExitedCode<N>;
// any other reason that says something is the platform's reason, with no code.
func TestJobRun_LastFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status, last string
		code         *int
		reason       string
	}{
		{"completed", "", new(0), ""},
		{"failed", "ContainerExitedCode3", new(3), ""},
		{"failed", "ContainerExitedCode137", new(137), ""},
		{"failed", "OOMKilled", nil, "OOMKilled"},
		{"failed", "ContainerExitedCodeX", nil, "ContainerExitedCodeX"},
		{"failed", "Error", nil, ""},
		{"failed", "", nil, ""},
		{"running", "", nil, ""},
	}

	for _, tc := range cases {
		run := &jobRun{Status: tc.status}
		run.StatusDetails.Indices = map[string]indexDetails{"0": {LastFailureReason: tc.last}}

		code, reason := run.lastFailure()

		if (code == nil) != (tc.code == nil) || (code != nil && *code != *tc.code) || reason != tc.reason {
			t.Errorf("%s/%q: lastFailure() = %v, %q; want %v, %q", tc.status, tc.last, code, reason, tc.code, tc.reason)
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

// The instance's times give the duration when they hold, the run's when the
// instance reports finishing before it started, as an evicted one does.
func TestJobRun_Ran(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		started, ended   string
		instanceFinished string
		want             time.Duration
	}{
		{"the instance's", "2026-11-10T05:57:40Z", "2026-11-10T05:58:21Z", "2026-11-10T05:58:18Z", 32 * time.Second},
		{"an epoch finish falls back to the run's", "2026-11-10T05:57:40Z", "2026-11-10T05:58:21Z", "1970-01-01T00:00:00Z", 41 * time.Second},
		{"no times at all", "", "", "", 0},
	}

	for _, tc := range cases {
		run := &jobRun{}
		run.StatusDetails.StartTime = tc.started
		run.StatusDetails.CompletionTime = tc.ended
		run.StatusDetails.Indices = map[string]indexDetails{"0": {StartedAt: "2026-11-10T05:57:46Z", FinishedAt: tc.instanceFinished}}

		if got := run.ran(); got != tc.want {
			t.Errorf("%s: ran() = %s, want %s", tc.name, got, tc.want)
		}
	}
}
