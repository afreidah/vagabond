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

	t.Logf("exit=%d duration=%s logs=%q",
		ptr.Deref(result.ExitCode), result.Duration, result.Logs)

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	// Both streams, since stdout alone looks correct on a passing build
	for _, want := range []string{"hello-stdout", "hello-stderr"} {
		if !strings.Contains(string(result.Logs), want) {
			t.Errorf("logs do not contain %q: %q", want, result.Logs)
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
