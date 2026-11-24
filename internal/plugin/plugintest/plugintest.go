// -------------------------------------------------------------------------------
// Provider Test Kit
//
// Author: Alex Freidah
//
// What the cloud providers' tests share: configuration parsed the way the
// loaders leave it, a container task shaped like the documented example, and
// the live round trip each container provider proves itself with against a
// real account, its output captured the way a dispatcher captures it.
// -------------------------------------------------------------------------------

package plugintest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// Body parses a provider config block the way the config loader leaves it.
func Body(t *testing.T, src string) hcl.Body {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "vagabond.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return f.Body
}

// RawBlock parses a driver config the way the job parser leaves it: decoded
// as a block, with its attributes still expressions.
func RawBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "job.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

// ContainerTask is a container task shaped like the documented example.
func ContainerTask(t *testing.T) *job.Task {
	t.Helper()

	return &job.Task{
		Name:      "test",
		Driver:    job.DriverContainer,
		Config:    RawBlock(t, "image = \"golang:1.27\"\ncommand = \"go\"\nargs = [\"test\", \"./...\"]\n"),
		Env:       RawBlock(t, "CI = \"true\"\nCGO_ENABLED = \"0\"\n"),
		Resources: &job.Resources{CPU: new(1000), Memory: new(2048)},
		Timeout:   new(job.Duration("15m")),
	}
}

// -------------------------------------------------------------------------
// LIVE RUNS
// -------------------------------------------------------------------------

// LiveProvider is a container provider under a live test: it runs a task and
// captures its output.
type LiveProvider interface {
	plugin.Provider
	plugin.LogCapturer
}

// Live is how a live test waits on its platform: how often Status is asked,
// and how long a run may take before the test fails.
type Live struct {
	Poll     time.Duration
	Deadline time.Duration
}

// liveLine is the first line a round trip's task prints, with the $ in its
// args and env passed through as written.
const liveLine = "hello-stdout-$BUILD_ID"

// RoundTrip runs a container that prints to both streams, sleeps so its first
// line is captured while it runs, and exits 3, since exit zero is also what a
// plugin that lost the code would report. It fails unless the exit code and
// both streams come back, the first line before the task ends, with the $ in
// the task's args and env as written. The execution is cancelled when the
// test ends; its id is returned for checks of the provider's own.
func (l Live) RoundTrip(t *testing.T, p LiveProvider, resources job.Resources) execution.ID {
	t.Helper()

	id := testkit.NewID(t)

	// A platform that substitutes its own $NAME variables would refuse
	// $VAGABOND_LIVE or fill in $BUILD_ID, so the line comes back as written
	// only if the plugin passes every $ through
	task := &job.Task{
		Name:   "live",
		Driver: job.DriverContainer,
		Config: RawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-c", "echo hello-stdout-$VAGABOND_LIVE; echo hello-stderr >&2; sleep 30; exit 3"]
`),
		Env:       RawBlock(t, `VAGABOND_LIVE = "$BUILD_ID"`+"\n"),
		Resources: &resources,
		Timeout:   new(job.Duration("5m")),
	}

	submission, err := p.Submit(t.Context(), id, task)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	t.Logf("submitted %s", submission.ProviderID)

	// Its own context: t.Context is cancelled before cleanups run
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		if err := p.Cancel(ctx, id); err != nil {
			t.Errorf("cleaning up %s failed: %v", submission.ProviderID, err)
		}
	})

	logs, end, wait := l.Capture(t, p, id)

	if !l.shows(t, p, id, logs, liveLine) {
		t.Errorf("the first line was not captured while the task ran: %q", logs.String())
	}

	if state := l.Finish(t, p, id); state != execution.StateFailed {
		t.Errorf("state = %s, want failed", state)
	}

	end()
	wait()

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}

	t.Logf("exit=%d duration=%s logs=%q", ptr.Deref(result.ExitCode), result.Duration, logs.String())

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	// Both streams, since stdout alone looks correct on a passing build
	for _, want := range []string{liveLine, "hello-stderr"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs do not contain %q: %q", want, logs.String())
		}
	}

	return id
}

// Finish polls Status the way a dispatcher would until the execution ends,
// and returns how it ended.
func (l Live) Finish(t *testing.T, p plugin.Provider, id execution.ID) execution.State {
	t.Helper()

	deadline := time.Now().Add(l.Deadline)

	for time.Now().Before(deadline) {
		state := stateOf(t, p, id)

		t.Logf("status: %s", state)

		if state.Terminal() {
			return state
		}

		time.Sleep(l.Poll)
	}

	t.Fatalf("execution did not finish within %s", l.Deadline)

	return ""
}

// AwaitRunning waits until the execution is running, failing the test if it
// ends or never starts.
func (l Live) AwaitRunning(t *testing.T, p plugin.Provider, id execution.ID) {
	t.Helper()

	deadline := time.Now().Add(l.Deadline)

	for time.Now().Before(deadline) {
		switch state := stateOf(t, p, id); {
		case state == execution.StateRunning:
			return
		case state.Terminal():
			t.Fatalf("execution ended %s before it was seen running", state)
		}

		time.Sleep(l.Poll)
	}

	t.Fatalf("execution did not start within %s", l.Deadline)
}

// stateOf asks p where the execution stands, failing the test when it cannot
// say.
func stateOf(t *testing.T, p plugin.Provider, id execution.ID) execution.State {
	t.Helper()

	status, err := p.Status(t.Context(), id)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	return status.State
}

// shows reports whether logs held want before the execution ended, polling
// both until one happens or the deadline passes.
func (l Live) shows(t *testing.T, p plugin.Provider, id execution.ID, logs *plugin.MemoryLog, want string) bool {
	t.Helper()

	deadline := time.Now().Add(l.Deadline)

	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), want) {
			return true
		}

		if stateOf(t, p, id).Terminal() {
			return false
		}

		time.Sleep(l.Poll)
	}

	return false
}

// Capture captures the execution's output in the background as a dispatcher
// does, making the call again after one that returns early. It returns the
// log, what tells the capture the workload has ended, and a wait for the
// capture to end once told, failing the test past a minute.
func (l Live) Capture(t *testing.T, p plugin.LogCapturer, id execution.ID) (*plugin.MemoryLog, func(), func()) {
	t.Helper()

	logs, ended, done := &plugin.MemoryLog{}, make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), l.Deadline)

	go func() {
		defer close(done)

		for {
			err := p.CaptureLogs(ctx, id, logs, ended)
			if err == nil || ctx.Err() != nil {
				return
			}

			t.Logf("CaptureLogs returned early: %v", err)

			select {
			case <-ctx.Done():
				return
			case <-time.After(l.Poll):
			}
		}
	}()

	return logs, sync.OnceFunc(func() { close(ended) }), func() {
		defer cancel()

		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Error("the capture did not end a minute after the workload")
		}
	}
}

// ReadAll captures a finished run's whole output.
func (l Live) ReadAll(t *testing.T, p plugin.LogCapturer, id execution.ID) string {
	t.Helper()

	logs, end, wait := l.Capture(t, p, id)
	end()
	wait()

	return logs.String()
}
