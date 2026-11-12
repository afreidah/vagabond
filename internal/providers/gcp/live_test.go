// -------------------------------------------------------------------------------
// Live Cloud Run Test
//
// Author: Alex Freidah
//
// Runs one real container in a real project and reads back what it produced.
// Skipped unless a credential is supplied, so the default suite needs no cloud
// account anywhere:
//
//	VAGABOND_GCP_CREDENTIALS=/path/to/key.json \
//	VAGABOND_GCP_PROJECT=munchbox-66afc \
//	VAGABOND_GCP_RUNTIME_SA=vagabond-runtime@munchbox-66afc.iam.gserviceaccount.com \
//	go test -run TestLive -v -timeout 10m ./internal/providers/gcp/
//
// This is the only test that catches a field Google renamed.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// SETUP
// -------------------------------------------------------------------------

// How long to wait for a container that does almost nothing, generous because
// a cold start and image pull have taken two minutes for a four second task.
const (
	livePoll     = 10 * time.Second
	liveDeadline = 6 * time.Minute
)

// liveProvider builds a provider against a real project, or skips.
func liveProvider(t *testing.T) *Provider {
	t.Helper()

	path := os.Getenv("VAGABOND_GCP_CREDENTIALS")
	if path == "" {
		t.Skip("VAGABOND_GCP_CREDENTIALS is unset")
	}

	credentials, err := os.ReadFile(path) //nolint:gosec // the operator named this file
	if err != nil {
		t.Fatalf("reading the credential: %v", err)
	}

	project := os.Getenv("VAGABOND_GCP_PROJECT")
	runtimeSA := os.Getenv("VAGABOND_GCP_RUNTIME_SA")

	if project == "" || runtimeSA == "" {
		t.Fatal("VAGABOND_GCP_PROJECT and VAGABOND_GCP_RUNTIME_SA are required")
	}

	region := os.Getenv("VAGABOND_GCP_REGION")
	if region == "" {
		region = "us-central1"
	}

	p, diags := New(t.Context(), "gcp-live", body(t, `
project                 = "`+project+`"
region                  = "`+region+`"
runtime_service_account = "`+runtimeSA+`"
`), credentials)
	if diags.HasErrors() {
		t.Fatalf("building the provider: %s", diags.Error())
	}

	return p
}

// -------------------------------------------------------------------------
// ROUND TRIP
// -------------------------------------------------------------------------

// A failing container, because a successful one proves less: exit code zero is
// also what a plugin that lost the exit code would report.
func TestLiveRoundTrip(t *testing.T) {
	p := liveProvider(t)

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID failed: %v", err)
	}

	task := &job.Task{
		Name:   "live",
		Driver: job.DriverContainer,
		Config: rawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-c", "echo hello-stdout; echo hello-stderr >&2; exit 3"]
`),
		Env:       rawBlock(t, "VAGABOND_LIVE = \"1\"\n"),
		Resources: &job.Resources{CPU: new(1000), Memory: new(512)},
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

	state := pollToCompletion(t, p, id)
	if state != execution.StateFailed {
		t.Errorf("state = %s, want failed", state)
	}

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}

	var logs strings.Builder

	if err := p.ReadLogs(t.Context(), id, &logs); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	t.Logf("exit=%d duration=%s logs=%q",
		ptr.Deref(result.ExitCode), result.Duration, logs.String())

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	// Both streams, since stdout alone looks correct on a passing build
	for _, want := range []string{"hello-stdout", "hello-stderr"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs do not contain %q: %q", want, logs.String())
		}
	}
}

// Output past 64 KiB comes back whole, and a follow that attaches after the
// container has printed most of it starts at the first line, as does one
// reopened at an offset. The container prints, then waits, so the follow
// attaches late; 12,000 lines keep each read to 12 of the 60 list calls a
// minute allows.
func TestLiveLateFollow(t *testing.T) {
	p := liveProvider(t)
	id := newID(t)

	task := &job.Task{
		Name:   "live-late",
		Driver: job.DriverContainer,
		Config: rawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-c", "seq 1 12000; sleep 45; echo the-end"]
`),
		Resources: &job.Resources{CPU: new(1000), Memory: new(512)},
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

	awaitRunning(t, p, id)

	// The first output is logged before the follow opens
	time.Sleep(20 * time.Second)

	var streamed bytes.Buffer

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- p.StreamLogs(ctx, id, 0, &streamed) }()

	pollToCompletion(t, p, id)

	// A tail trails the container by seconds, and never ends by itself
	time.Sleep(15 * time.Second)
	cancel()

	if err := <-done; err != nil {
		t.Errorf("StreamLogs failed: %v", err)
	}

	var whole strings.Builder

	if err := p.ReadLogs(t.Context(), id, &whole); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	if !strings.HasPrefix(whole.String(), "1\n2\n") || !strings.HasSuffix(whole.String(), "12000\nthe-end\n") ||
		whole.Len() <= 64<<10 {
		t.Errorf("ReadLogs gave %d bytes, want all of the output", whole.Len())
	}

	if streamed.String() != whole.String() {
		t.Errorf("the late follow streamed %d bytes, want the %d stored, from the first line", streamed.Len(), whole.Len())
	}

	// Reopened at an offset, it picks up exactly there
	var rest bytes.Buffer

	resumed, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()

	if err := p.StreamLogs(resumed, id, 2, &rest); err != nil {
		t.Errorf("StreamLogs from an offset failed: %v", err)
	}

	if rest.String() != whole.String()[2:] {
		t.Errorf("StreamLogs from offset 2 gave %d bytes, want the %d after it", rest.Len(), whole.Len()-2)
	}
}

// awaitRunning waits until the execution is running, or fails the test if it
// ends or never starts.
func awaitRunning(t *testing.T, p *Provider, id execution.ID) {
	t.Helper()

	deadline := time.Now().Add(liveDeadline)

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

		time.Sleep(livePoll)
	}

	t.Fatalf("execution did not start within %s", liveDeadline)
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
