// -------------------------------------------------------------------------------
// Run, Register and Execution Tests
//
// Author: Alex Freidah
//
// The commands that start runs and read them back, against a server over the
// fake function provider, which finishes inside Submit with whatever exit code
// the test sets, and the fake container provider, whose runs last until the
// test ends them.
// -------------------------------------------------------------------------------

package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

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

// containerConfig is one provider whose runs last until a test ends them.
const containerConfig = `provider "box" { type = "fake-container" }`

// containerJob runs one container task.
const containerJob = `
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

// background is a command running on its own goroutine, with streams a test
// can read while it runs.
type background struct {
	stdout, stderr testkit.Buffer
	code           chan int
}

// start runs the CLI in the background.
func start(args ...string) *background {
	b := &background{code: make(chan int, 1)}

	go func() { b.code <- Run(args, strings.NewReader(""), &b.stdout, &b.stderr) }()

	return b
}

// awaitOutput waits until stdout reads want.
func (b *background) awaitOutput(t *testing.T, want string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for b.stdout.String() != want {
		if time.Now().After(deadline) {
			t.Fatalf("stdout %q, want %q; stderr:\n%s", b.stdout.String(), want, b.stderr.String())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// exit waits for the command to finish and returns its exit code.
func (b *background) exit(t *testing.T) int {
	t.Helper()

	select {
	case code := <-b.code:
		return code
	case <-time.After(30 * time.Second):
		t.Fatalf("the command did not finish; stderr:\n%s", b.stderr.String())

		return 0
	}
}

// finishRun ends, as succeeded, the container run whose dispatch a run
// reported on stderr.
func (s *testServer) finishRun(t *testing.T, stderr string) {
	t.Helper()

	id, err := execution.ParseID(s.firstExecution(t, stderr))
	if err != nil {
		t.Fatalf("ParseID() = %v", err)
	}

	p, _ := s.registry.Provider("box")

	for _, next := range []execution.State{execution.StateRunning, execution.StateSucceeded} {
		if err := p.(*plugin.FakeContainerProvider).Advance(id, next); err != nil {
			t.Fatalf("Advance(%s) = %v", next, err)
		}
	}
}

// exitWith makes the server's function provider exit with code.
func (s *testServer) exitWith(t *testing.T, code int) {
	t.Helper()

	p, _ := s.registry.Provider("fn")
	p.(*plugin.FakeSyncProvider).ExitCode = new(code)
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

// job run shows a container task's output while the task runs, prints it once
// when the task ends, and exits with its result.
func TestJobRun_StreamsOutputWhileTheTaskRuns(t *testing.T) {
	s := serve(t, containerConfig)
	cmd := start("job", "run", "-address", s.address, writeJob(t, containerJob))

	cmd.awaitOutput(t, "fake execution output\n")

	// The task ends only now, so the output above arrived while it ran.
	s.finishRun(t, cmd.stderr.String())

	if code := cmd.exit(t); code != ExitSuccess || cmd.stdout.String() != "fake execution output\n" {
		t.Errorf("exit %d with stdout %q, want success and the output once", code, cmd.stdout.String())
	}
}

// A run nothing can take never ran, and says to plan it.
func TestJobRun_NothingCanRunIt(t *testing.T) {
	code, _, stderr := serve(t, functionConfig).at([]string{"job", "run"}, writeJob(t, containerJob))

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

// A periodic job prints its next launch when registered and shows its
// schedule in its status. Running it is refused, and planning it says when it
// would first launch.
func TestPeriodicJob_RegisterStatusRunPlan(t *testing.T) {
	s := serve(t, functionConfig)
	path := writeJob(t, strings.Replace(functionJob, `type = "batch"`,
		"type = \"batch\"\n\n  periodic {\n    crons = [\"@yearly\"]\n  }", 1))
	nextYear := stamp(time.Date(time.Now().UTC().Year()+1, 1, 1, 0, 0, 0, 0, time.UTC))

	code, stdout, stderr := s.at([]string{"job", "register"}, path)
	if code != ExitSuccess || !strings.Contains(stdout, "Next launch: "+nextYear+" (in ") {
		t.Fatalf("register: exit %d\n%s%s", code, stdout, stderr)
	}

	_, shown, _ := s.at([]string{"job", "status"}, "hello")
	for _, want := range []string{"@yearly", "UTC", "Prohibit overlap = false", "Next launch      = " + nextYear} {
		if !strings.Contains(shown, want) {
			t.Errorf("job status hello lacks %q:\n%s", want, shown)
		}
	}

	code, _, stderr = s.at([]string{"job", "run"}, path)
	if code != ExitFailure || !strings.Contains(stderr, "vagabond job register") {
		t.Errorf("run: exit %d\n%s", code, stderr)
	}

	_, planned, _ := s.at([]string{"job", "plan"}, path)
	if !strings.Contains(planned, "If registered now, the next launch would be at "+nextYear+" (in ") {
		t.Errorf("plan:\n%s", planned)
	}
}

// A time ahead reads as how long until it, and one behind or now as how long
// since, both to the second.
func TestRelative(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		at   time.Time
		want string
	}{
		{at: now.Add(3*time.Minute + 400*time.Millisecond), want: "in 3m0s"},
		{at: now.Add(-4 * time.Second), want: "4s ago"},
		{at: now, want: "0s ago"},
	}

	for _, tt := range tests {
		if got := relative(tt.at, now); got != tt.want {
			t.Errorf("relative(%s) = %q, want %q", tt.at, got, tt.want)
		}
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

// A finished run is found again without its ID: job run names its dispatch and
// execution, execution list lists it as the flags narrow, and dispatch status
// shows the run it belongs to.
func TestExecutionList_FindsARun(t *testing.T) {
	s := serve(t, functionConfig)

	_, _, stderr := s.at([]string{"job", "run"}, writeJob(t, functionJob))

	ids := uuid.FindAllString(stderr, -1)
	if len(ids) != 2 || !strings.Contains(stderr, "(execution "+ids[1]+")") {
		t.Fatalf("job run named %v, want its dispatch then its execution:\n%s", ids, stderr)
	}

	dispatch, execution := ids[0], ids[1]

	code, list, errs := s.at([]string{"execution", "list"}, "-job", "hello", "-state", "succeeded")
	if code != ExitSuccess || !strings.Contains(list, execution) || !strings.Contains(list, "JOB") {
		t.Errorf("execution list: exit %d\n%s%s", code, list, errs)
	}

	if _, none, _ := s.at([]string{"execution", "list"}, "-provider", "elsewhere"); none != "No executions match.\n" {
		t.Errorf("execution list -provider elsewhere = %q, want none", none)
	}

	code, shown, errs := s.at([]string{"dispatch", "status"}, dispatch)
	if code != ExitSuccess || !strings.Contains(shown, "State     = succeeded") || !strings.Contains(shown, execution) {
		t.Errorf("dispatch status: exit %d\n%s%s", code, shown, errs)
	}
}

// execution list refuses a state that is not one and a limit that lists
// nothing, and dispatch status an unknown dispatch.
func TestExecutionList_Refusals(t *testing.T) {
	s := serve(t, functionConfig)

	cases := []struct {
		words []string
		args  []string
		want  string
	}{
		{[]string{"execution", "list"}, []string{"-state", "done"}, "valid states are"},
		{[]string{"execution", "list"}, []string{"-limit", "0"}, "-limit 0 lists nothing"},
		{[]string{"execution", "list"}, []string{"extra"}, "takes no arguments"},
		{[]string{"dispatch", "status"}, []string{"01920000-0000-7000-8000-000000000000"}, "not found"},
	}

	for _, c := range cases {
		if code, _, stderr := s.at(c.words, c.args...); code != ExitFailure || !strings.Contains(stderr, c.want) {
			t.Errorf("%v %v: exit %d, want %q in:\n%s", c.words, c.args, code, c.want, stderr)
		}
	}
}

// uuid finds an execution ID in text.
var uuid = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// executionID runs the function job and returns the ID of its execution.
func executionID(t *testing.T, s *testServer) string {
	t.Helper()

	_, _, stderr := s.at([]string{"job", "run"}, writeJob(t, functionJob))

	return s.firstExecution(t, stderr)
}

// firstExecution returns the ID of the first execution of the dispatch a run
// reported on stderr.
func (s *testServer) firstExecution(t *testing.T, stderr string) string {
	t.Helper()

	dispatch := uuid.FindString(stderr)
	if dispatch == "" {
		t.Fatalf("no dispatch ID in:\n%s", stderr)
	}

	client, err := api.NewClient(s.address, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	// A run just started may not have recorded its execution yet.
	deadline := time.Now().Add(5 * time.Second)

	for {
		d, err := client.DispatchStatus(t.Context(), dispatch)
		if err == nil && len(d.Executions) > 0 {
			return d.Executions[0].ID
		}

		if time.Now().After(deadline) {
			t.Fatalf("DispatchStatus() = %+v, %v", d, err)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// execution logs -f streams a running execution's output, prints it once, and
// exits when the execution ends.
func TestExecutionLogs_Follow(t *testing.T) {
	s := serve(t, containerConfig)

	client, err := api.NewClient(s.address, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	started, err := client.Run(t.Context(), "", containerJob, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	cmd := start("execution", "logs", "-f", "-address", s.address, s.firstExecution(t, started.DispatchID))

	cmd.awaitOutput(t, "fake execution output\n")
	s.finishRun(t, started.DispatchID)

	if code := cmd.exit(t); code != ExitSuccess || cmd.stdout.String() != "fake execution output\n" {
		t.Errorf("exit %d with stdout %q, want success and the output once", code, cmd.stdout.String())
	}
}

// execution logs -f on one that already ended prints its stored output, and on
// one nothing recorded is an error.
func TestExecutionLogs_FollowAnEndedExecution(t *testing.T) {
	s := serve(t, functionConfig)
	id := executionID(t, s)

	if code, stdout, stderr := s.at([]string{"execution", "logs", "-f"}, id); code != ExitSuccess || stdout != "" {
		t.Errorf("exit %d with stdout %q, want success and no output\n%s", code, stdout, stderr)
	}

	code, _, stderr := s.at([]string{"execution", "logs", "-f"}, "01920000-0000-7000-8000-000000000000")
	if code != ExitFailure || !strings.Contains(stderr, "not found") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}
