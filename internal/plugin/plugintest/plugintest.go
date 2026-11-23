// -------------------------------------------------------------------------------
// Provider Test Kit
//
// Author: Alex Freidah
//
// What the cloud providers' tests share: configuration parsed the way the
// loaders leave it, a container task shaped like the documented example, and
// the live round trip each container provider proves itself with against a
// real account.
// -------------------------------------------------------------------------------

package plugintest

import (
	"bytes"
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
// reads its whole output back.
type LiveProvider interface {
	plugin.Provider
	plugin.LogReader
}

// Live is how a live test waits on its platform: how often Status is asked,
// and how long a run may take before the test fails.
type Live struct {
	Poll     time.Duration
	Deadline time.Duration
}

// RoundTrip runs a container that prints to both streams, sleeps so a follow
// attaches while it runs, and exits 3, since exit zero is also what a plugin
// that lost the code would report. It fails unless the exit code and both
// streams come back, read whole and, where p streams, followed live, with the
// $ in the task's args and env as written. The execution is cancelled when the
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
args    = ["-c", "echo hello-stdout-$VAGABOND_LIVE; echo hello-stderr >&2; sleep 10; exit 3"]
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

	streamed, wait := &SyncBuffer{}, func() {}
	if streamer, ok := p.(plugin.LogStreamer); ok {
		streamed, wait = l.Follow(t, streamer, id)
	}

	if state := l.Finish(t, p, id); state != execution.StateFailed {
		t.Errorf("state = %s, want failed", state)
	}

	wait()

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}

	logs := ReadAll(t, p, id)

	t.Logf("exit=%d duration=%s logs=%q streamed=%q",
		ptr.Deref(result.ExitCode), result.Duration, logs, streamed.String())

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	// Both streams, since stdout alone looks correct on a passing build
	for _, want := range []string{"hello-stdout-$BUILD_ID", "hello-stderr"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs do not contain %q: %q", want, logs)
		}

		if _, ok := p.(plugin.LogStreamer); ok && !strings.Contains(streamed.String(), want) {
			t.Errorf("the live stream did not carry %q: %q", want, streamed.String())
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
		status, err := p.Status(t.Context(), id)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}

		t.Logf("status: %s", status.State)

		if status.State.Terminal() {
			return status.State
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
		status, err := p.Status(t.Context(), id)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}

		switch {
		case status.State == execution.StateRunning:
			return
		case status.State.Terminal():
			t.Fatalf("execution ended %s before it was seen running", status.State)
		}

		time.Sleep(l.Poll)
	}

	t.Fatalf("execution did not start within %s", l.Deadline)
}

// Follow streams the execution's output in the background, returning what it
// wrote and a wait for the stream to end once the container has.
func (l Live) Follow(t *testing.T, p plugin.LogStreamer, id execution.ID) (*SyncBuffer, func()) {
	t.Helper()

	out := &SyncBuffer{}
	ctx, cancel := context.WithTimeout(t.Context(), l.Deadline)
	done := make(chan struct{})

	go func() {
		defer close(done)

		if err := p.StreamLogs(ctx, id, 0, out); err != nil {
			t.Errorf("StreamLogs failed: %v", err)
		}
	}()

	return out, func() {
		defer cancel()

		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Error("the live stream did not end with the container")
		}
	}
}

// ReadAll reads a finished run's whole output.
func ReadAll(t *testing.T, p plugin.LogReader, id execution.ID) string {
	t.Helper()

	var out strings.Builder

	if err := p.ReadLogs(t.Context(), id, &execution.PlacedWriter{Writer: &out}); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	return out.String()
}

// -------------------------------------------------------------------------
// BUFFERS
// -------------------------------------------------------------------------

// SyncBuffer is a buffer a stream writes and a test reads at the same time.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends under the lock.
func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// String reads under the lock.
func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
