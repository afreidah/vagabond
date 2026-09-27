// -------------------------------------------------------------------------------
// Run, Register and Execution Tests
//
// Author: Alex Freidah
//
// The commands that start runs and read them back, against a server over the
// fake function provider, which finishes inside Submit with whatever exit code
// the test sets.
// -------------------------------------------------------------------------------

package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
)

// functionConfig is one synchronous provider.
const functionConfig = `provider "fn" { type = "fake-function" }`

// functionJob runs one task on it.
const functionJob = `
job "hello" {
  type = "batch"

  task "greet" {
    driver = "function"

    config {
      function = "hello"
    }
  }
}
`

// exitWith makes the server's function provider exit with code.
func (s *testServer) exitWith(t *testing.T, code int) {
	t.Helper()

	p, _ := s.registry.Provider("fn")
	p.(*plugin.FakeSyncProvider).ExitCode = ptr.Of(code)
}

// -------------------------------------------------------------------------
// RUN AND DISPATCH
// -------------------------------------------------------------------------

// job run waits for the task and exits with its result.
func TestJobRun_ExitsWithTheTaskResult(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		want     int
		verdict  string
	}{
		{"succeeded", 0, ExitSuccess, "greet succeeded on fn"},
		{"task failed", 3, ExitFailure, "greet failed (exit 3) on fn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serve(t, functionConfig)
			s.exitWith(t, tt.exitCode)

			code, _, stderr := s.at([]string{"job", "run"}, writeJob(t, functionJob))

			if code != tt.want || !strings.Contains(stderr, tt.verdict) {
				t.Errorf("exit %d, want %d with %q:\n%s", code, tt.want, tt.verdict, stderr)
			}
		})
	}
}

// A run nothing can take never ran, and says to plan it.
func TestJobRun_NothingCanRunIt(t *testing.T) {
	const container = `
job "ci" {
  type = "batch"

  task "test" {
    driver = "container"

    config {
      image = "golang:1.27"
    }
  }
}
`

	code, _, stderr := serve(t, functionConfig).at([]string{"job", "run"}, writeJob(t, container))

	if code != ExitNoCapacity || !strings.Contains(stderr, "job plan") {
		t.Errorf("exit %d, want %d:\n%s", code, ExitNoCapacity, stderr)
	}
}

// A job file is validated before it is sent, and a mistake with a position is
// reported against its source.
func TestJobRun_InvalidJobIsNotSent(t *testing.T) {
	code, _, stderr := serve(t, functionConfig).at([]string{"job", "run"},
		writeJob(t, `job "ci" {`))

	if code != ExitFailure || !strings.Contains(stderr, "test.vagabond.hcl") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// A registered job dispatches by name, and a stopped one is refused.
func TestJobDispatch_RegisteredAndStopped(t *testing.T) {
	s := serve(t, functionConfig)

	code, stdout, stderr := s.at([]string{"job", "register"}, writeJob(t, functionJob))
	if code != ExitSuccess || !strings.Contains(stdout, `"hello" registered as version 1`) {
		t.Fatalf("register: exit %d\n%s%s", code, stdout, stderr)
	}

	if code, _, stderr := s.at([]string{"job", "dispatch"}, "hello"); code != ExitSuccess {
		t.Fatalf("dispatch: exit %d\n%s", code, stderr)
	}

	if code, _, stderr := s.at([]string{"job", "stop"}, "hello"); code != ExitSuccess {
		t.Fatalf("stop: exit %d\n%s", code, stderr)
	}

	code, _, stderr = s.at([]string{"job", "dispatch"}, "hello")
	if code != ExitFailure || !strings.Contains(stderr, "stopped") {
		t.Errorf("dispatch of a stopped job: exit %d\n%s", code, stderr)
	}
}

// Registering the same job again is not a new version.
func TestJobRegister_UnchangedIsNotAVersion(t *testing.T) {
	s := serve(t, functionConfig)
	path := writeJob(t, functionJob)

	s.at([]string{"job", "register"}, path)

	_, stdout, _ := s.at([]string{"job", "register"}, path)
	if !strings.Contains(stdout, "unchanged at version 1") {
		t.Errorf("second register:\n%s", stdout)
	}
}

// -------------------------------------------------------------------------
// STATUS AND EXECUTIONS
// -------------------------------------------------------------------------

// job status lists registered jobs and shows one with its executions, and an
// execution reads back by ID.
func TestStatus_JobAndExecution(t *testing.T) {
	s := serve(t, functionConfig)

	s.at([]string{"job", "register"}, writeJob(t, functionJob))
	s.at([]string{"job", "dispatch"}, "hello")

	_, list, _ := s.at([]string{"job", "status"})
	if !strings.Contains(list, "hello") || !strings.Contains(list, "registered") {
		t.Errorf("job status:\n%s", list)
	}

	_, shown, _ := s.at([]string{"job", "status"}, "hello")
	if !strings.Contains(shown, "Recent executions") || !strings.Contains(shown, "succeeded") {
		t.Fatalf("job status hello:\n%s", shown)
	}

	id := executionID(t, s)

	code, record, stderr := s.at([]string{"execution", "status"}, id)
	if code != ExitSuccess || !strings.Contains(record, "Exit Code   = 0") {
		t.Errorf("execution status: exit %d\n%s%s", code, record, stderr)
	}

	if code, _, stderr := s.at([]string{"execution", "logs"}, id); code != ExitSuccess {
		t.Errorf("execution logs: exit %d\n%s", code, stderr)
	}
}

// An unknown execution is an error.
func TestExecutionStatus_Unknown(t *testing.T) {
	code, _, stderr := serve(t, functionConfig).at([]string{"execution", "status"},
		"01920000-0000-7000-8000-000000000000")

	if code != ExitFailure || !strings.Contains(stderr, "not found") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// uuid finds an execution ID in text.
var uuid = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// executionID runs the function job and returns the ID of its execution, read
// from the dispatch the run reports.
func executionID(t *testing.T, s *testServer) string {
	t.Helper()

	_, _, stderr := s.at([]string{"job", "run"}, writeJob(t, functionJob))

	dispatch := uuid.FindString(stderr)
	if dispatch == "" {
		t.Fatalf("no dispatch ID in:\n%s", stderr)
	}

	client, err := (&Meta{address: s.address}).client()
	if err != nil {
		t.Fatalf("client() = %v", err)
	}

	d, err := client.DispatchStatus(t.Context(), dispatch)
	if err != nil || len(d.Executions) == 0 {
		t.Fatalf("DispatchStatus() = %+v, %v", d, err)
	}

	return d.Executions[0].ID
}
