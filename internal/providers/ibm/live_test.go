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
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// SETUP
// -------------------------------------------------------------------------

// live waits generously on a container that does almost nothing, because an
// image pull dominates a short run.
var live = plugintest.Live{Poll: 5 * time.Second, Deadline: 6 * time.Minute}

// liveSize is the smallest size Code Engine accepts.
var liveSize = job.Resources{CPU: new(125), Memory: new(256)}

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

	p, diags := New(t.Context(), "ibm-live", plugintest.Body(t, `
region     = "`+region+`"
project_id = "`+project+`"
`), []byte(key))
	if diags.HasErrors() {
		t.Fatalf("building the provider: %s", diags.Error())
	}

	return p
}

// submitLive submits task, cancelling it when the test ends.
func submitLive(t *testing.T, p *Provider, id execution.ID, task *job.Task) {
	t.Helper()

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
}

// -------------------------------------------------------------------------
// ROUND TRIP
// -------------------------------------------------------------------------

// A failing container comes back with its exit code and both streams,
// captured while it runs. Released, the run is gone.
func TestLiveRoundTrip(t *testing.T) {
	p := liveProvider(t)
	id := live.RoundTrip(t, p, liveSize)

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
	id := testkit.NewID(t)

	// $0 is the one-line third argument and LIVE_PRICE an env value, both echoed as received
	submitLive(t, p, id, &job.Task{
		Name:   "live",
		Driver: job.DriverContainer,
		Config: plugintest.RawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-ec", "if true; then\n  echo line-one\nfi\necho \"$0 / $LIVE_PRICE\"", "$(UNSET) $$"]
`),
		Env:       plugintest.RawBlock(t, `LIVE_PRICE = "$(UNSET) costs $$5"`+"\n"),
		Resources: &liveSize,
		Timeout:   new(job.Duration("5m")),
	})

	if state := live.Finish(t, p, id); state != execution.StateSucceeded {
		t.Errorf("state = %s, want succeeded", state)
	}

	logs := live.ReadAll(t, p, id)

	t.Logf("logs=%q", logs)

	if want := "line-one\n$(UNSET) $$ / $(UNSET) costs $$5\n"; logs != want {
		t.Errorf("logs = %q, want %q", logs, want)
	}
}

// Output past 64 KiB comes back whole, which no bound on the pod log request
// may cut.
func TestLiveLongOutput(t *testing.T) {
	p := liveProvider(t)
	id := testkit.NewID(t)

	submitLive(t, p, id, &job.Task{
		Name:   "live-long",
		Driver: job.DriverContainer,
		Config: plugintest.RawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-c", "seq 1 20000"]
`),
		Resources: &liveSize,
		Timeout:   new(job.Duration("5m")),
	})

	if state := live.Finish(t, p, id); state != execution.StateSucceeded {
		t.Errorf("state = %s, want succeeded", state)
	}

	logs := live.ReadAll(t, p, id)

	if !strings.HasPrefix(logs, "1\n2\n") || !strings.HasSuffix(logs, "\n20000\n") || len(logs) <= 64<<10 {
		t.Errorf("the capture gave %d bytes, want all of the output", len(logs))
	}
}
