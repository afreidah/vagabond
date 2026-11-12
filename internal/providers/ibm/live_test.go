// -------------------------------------------------------------------------------
// Live Code Engine Test
//
// Author: Alex Freidah
//
// Runs one real container in a real project, follows its output while it runs,
// and reads back what it produced. Skipped unless a key is supplied, so the
// default suite needs no cloud account anywhere:
//
//	VAGABOND_IBM_API_KEY=... VAGABOND_IBM_PROJECT=<guid> VAGABOND_IBM_REGION=us-east \
//	go test -run TestLive -v -timeout 10m ./internal/providers/ibm/
//
// This is the only test that catches a field IBM renamed.
// -------------------------------------------------------------------------------

package ibm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// SETUP
// -------------------------------------------------------------------------

// How long to wait for a container that does almost nothing, generous because
// an image pull dominates a short run.
const (
	livePoll     = 5 * time.Second
	liveDeadline = 6 * time.Minute
)

// liveProvider builds a provider against a real project, or skips. The region
// is required: a wrong one fails every call with 403.
func liveProvider(t *testing.T) *Provider {
	t.Helper()

	key := os.Getenv("VAGABOND_IBM_API_KEY")
	if key == "" {
		t.Skip("VAGABOND_IBM_API_KEY is unset")
	}

	project := os.Getenv("VAGABOND_IBM_PROJECT")
	region := os.Getenv("VAGABOND_IBM_REGION")

	if project == "" || region == "" {
		t.Fatal("VAGABOND_IBM_PROJECT and VAGABOND_IBM_REGION are required")
	}

	p, diags := New(t.Context(), "ibm-live", body(t, `
region     = "`+region+`"
project_id = "`+project+`"
`), []byte(key))
	if diags.HasErrors() {
		t.Fatalf("building the provider: %s", diags.Error())
	}

	return p
}

// -------------------------------------------------------------------------
// ROUND TRIP
// -------------------------------------------------------------------------

// A failing container, because a successful one proves less: exit code zero is
// also what a plugin that lost the exit code would report. It sleeps so the
// follower attaches while it runs.
func TestLiveRoundTrip(t *testing.T) {
	p := liveProvider(t)
	id := newID(t)

	task := &job.Task{
		Name:   "live",
		Driver: job.DriverContainer,
		Config: rawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-c", "echo hello-stdout; echo hello-stderr >&2; sleep 10; exit 3"]
`),
		Env:       rawBlock(t, "VAGABOND_LIVE = \"1\"\n"),
		Resources: &job.Resources{CPU: new(125), Memory: new(256)},
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

	streamed, wait := follow(t, p, id)

	if state := pollToCompletion(t, p, id); state != execution.StateFailed {
		t.Errorf("state = %s, want failed", state)
	}

	wait()

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}

	logs := readLogs(t, p, id)

	t.Logf("exit=%d duration=%s logs=%q streamed=%q",
		ptr.Deref(result.ExitCode), result.Duration, logs, streamed.String())

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	// Both streams, since stdout alone looks correct on a passing build
	for _, want := range []string{"hello-stdout", "hello-stderr"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs do not contain %q: %q", want, logs)
		}

		if !strings.Contains(streamed.String(), want) {
			t.Errorf("the live stream did not carry %q: %q", want, streamed.String())
		}
	}

	if err := p.Release(t.Context(), id); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	if _, err := p.Status(t.Context(), id); !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("Status after Release = %v, want ErrUnknownExecution", err)
	}
}

// A multi-line script reaches the container as written, $ and all, though Code
// Engine refuses a line break in an argument and rewrites $(NAME) and $$.
func TestLiveMultiLine(t *testing.T) {
	p := liveProvider(t)
	id := newID(t)

	// $0 is the one-line third argument and LIVE_PRICE an env value, both echoed as received
	task := &job.Task{
		Name:   "live",
		Driver: job.DriverContainer,
		Config: rawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-ec", "if true; then\n  echo line-one\nfi\necho \"$0 / $LIVE_PRICE\"", "$(UNSET) $$"]
`),
		Env:       rawBlock(t, `LIVE_PRICE = "$(UNSET) costs $$5"`+"\n"),
		Resources: &job.Resources{CPU: new(125), Memory: new(256)},
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

	if state := pollToCompletion(t, p, id); state != execution.StateSucceeded {
		t.Errorf("state = %s, want succeeded", state)
	}

	logs := readLogs(t, p, id)

	t.Logf("logs=%q", logs)

	if want := "line-one\n$(UNSET) $$ / $(UNSET) costs $$5\n"; logs != want {
		t.Errorf("logs = %q, want %q", logs, want)
	}
}

// Output past 64 KiB comes back whole, which no bound on the pod log request
// may cut.
func TestLiveLongOutput(t *testing.T) {
	p := liveProvider(t)
	id := newID(t)

	task := &job.Task{
		Name:   "live-long",
		Driver: job.DriverContainer,
		Config: rawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-c", "seq 1 20000"]
`),
		Resources: &job.Resources{CPU: new(125), Memory: new(256)},
		Timeout:   new(job.Duration("5m")),
	}

	if _, err := p.Submit(t.Context(), id, task); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Its own context: t.Context is cancelled before cleanups run
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		_ = p.Cancel(ctx, id)
	})

	if state := pollToCompletion(t, p, id); state != execution.StateSucceeded {
		t.Errorf("state = %s, want succeeded", state)
	}

	logs := readLogs(t, p, id)

	if !strings.HasPrefix(logs, "1\n2\n") || !strings.HasSuffix(logs, "\n20000\n") || len(logs) <= 64<<10 {
		t.Errorf("ReadLogs gave %d bytes, want all of the output", len(logs))
	}
}

// readLogs reads a finished run's whole output.
func readLogs(t *testing.T, p *Provider, id execution.ID) string {
	t.Helper()

	var out strings.Builder

	if err := p.ReadLogs(t.Context(), id, &out); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	return out.String()
}

// follow streams the execution's output in the background, returning what it
// wrote and a wait for the stream to end once the container has.
func follow(t *testing.T, p *Provider, id execution.ID) (*lockedBuffer, func()) {
	t.Helper()

	out := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(t.Context(), liveDeadline)
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

// pollToCompletion waits the way a dispatcher would.
func pollToCompletion(t *testing.T, p *Provider, id execution.ID) execution.State {
	t.Helper()

	deadline := time.Now().Add(liveDeadline)

	for time.Now().Before(deadline) {
		status, err := p.Status(t.Context(), id)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}

		t.Logf("status: %s", status.State)

		if status.State.Terminal() {
			return status.State
		}

		time.Sleep(livePoll)
	}

	t.Fatalf("execution did not finish within %s", liveDeadline)

	return ""
}

// lockedBuffer is a buffer the stream writes and the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends under the lock.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// String reads under the lock.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
