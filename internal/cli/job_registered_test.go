// -------------------------------------------------------------------------------
// Registered Job Command Tests
//
// Author: Alex Freidah
//
// A registered job lives in the database, so every command that registers or
// reads one refuses without a store block. Behaviour against a store is covered
// by the integration suite.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"
	"testing"
)

func TestRegisteredJobCommandsNeedAStore(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T) []string
	}{
		{name: "register", args: func(t *testing.T) []string {
			return []string{"job", "register", writeJob(t, planJob)}
		}},
		{name: "dispatch", args: func(*testing.T) []string { return []string{"job", "dispatch", "ci"} }},
		{name: "status", args: func(*testing.T) []string { return []string{"job", "status"} }},
		{name: "stop", args: func(*testing.T) []string { return []string{"job", "stop", "ci"} }},
		{name: "plan by name", args: func(*testing.T) []string { return []string{"job", "plan", "ci"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VAGABOND_CONFIG", "")
			t.Chdir(t.TempDir())

			args := tt.args(t)
			args = append(args[:2], append([]string{"-config", writeConfig(t, planConfig)}, args[2:]...)...)

			code, _, stderr := run(args...)

			if code != ExitFailure || !strings.Contains(stderr, "needs a store block") {
				t.Errorf("exit %d, stderr:\n%s", code, stderr)
			}
		})
	}
}

// A path that does not exist is still a file, so a typo reads as a missing file
// rather than a missing registered job.
func TestJobPlan_MissingFileIsAFile(t *testing.T) {
	code, _, stderr := plan(t, planJob, planConfig)
	if code != ExitSuccess {
		t.Fatalf("the ordinary plan failed:\n%s", stderr)
	}

	t.Setenv("VAGABOND_CONFIG", "")
	t.Chdir(t.TempDir())

	code, _, stderr = run("job", "plan", "-config", writeConfig(t, planConfig), "typo.vagabond.hcl")

	if code != ExitFailure || !strings.Contains(stderr, "Cannot read job file") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}
