// -------------------------------------------------------------------------------
// Live Cloud Run Tests
//
// Author: Alex Freidah
//
// Runs real containers in a real project and reads back what they produced.
// Skipped unless a credential is supplied, so the default suite needs no
// cloud account anywhere:
//
//	VAGABOND_GCP_CREDENTIALS=/path/to/key.json \
//	VAGABOND_GCP_PROJECT=munchbox-66afc \
//	VAGABOND_GCP_RUNTIME_SA=vagabond-runtime@munchbox-66afc.iam.gserviceaccount.com \
//	go test -run TestLive -v -timeout 12m ./internal/providers/gcp/cloudrun/
//
// These are the only tests that catch a field Google renamed.
// -------------------------------------------------------------------------------

package cloudrun

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/providers/gcp/gcptest"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// SETUP
// -------------------------------------------------------------------------

// liveProvider builds a Cloud Run provider against a real project, or skips.
func liveProvider(t *testing.T) *Provider {
	t.Helper()

	body, credentials := gcptest.LiveConfig(t)

	p, diags := New(t.Context(), "gcp-live", body, credentials)
	if diags.HasErrors() {
		t.Fatalf("building the provider: %s", diags.Error())
	}

	return p
}

// -------------------------------------------------------------------------
// ROUND TRIP
// -------------------------------------------------------------------------

// A failing container comes back with its exit code and both streams,
// captured while it runs.
func TestLiveRoundTrip(t *testing.T) {
	gcptest.Live.RoundTrip(t, liveProvider(t), job.Resources{CPU: new(1000), Memory: new(512)})
}

// -------------------------------------------------------------------------
// LATE CAPTURE
// -------------------------------------------------------------------------

// Output past 64 KiB comes back whole and once, from a capture begun after the
// container printed most of it, as a server taking one over begins. One begun
// on a log holding the first lines adds only the rest. The container prints,
// then waits, so the capture begins late; 12,000 lines keep each catch-up to
// 12 of the 60 list calls a minute allows.
func TestLiveLateCapture(t *testing.T) {
	p := liveProvider(t)
	id := testkit.NewID(t)

	task := &job.Task{
		Name:   "live-late",
		Driver: job.DriverContainer,
		Config: plugintest.RawBlock(t, `
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

	gcptest.Live.AwaitRunning(t, p, id)

	// The first output is logged before the capture begins
	time.Sleep(20 * time.Second)

	logs, end, wait := gcptest.Live.Capture(t, p, id)

	gcptest.Live.Finish(t, p, id)
	end()
	wait()

	whole := logs.String()

	if !strings.HasPrefix(whole, "1\n2\n") || !strings.HasSuffix(whole, "12000\nthe-end\n") ||
		strings.Count(whole, "\n") != 12001 {
		t.Errorf("the late capture gave %d bytes in %d lines, want 12,001 lines from the first",
			len(whole), strings.Count(whole, "\n"))
	}

	// Begun on a log holding the first lines, it adds the rest
	ended := make(chan struct{})
	close(ended)

	resumed := &plugin.MemoryLog{}
	_, _ = resumed.Write([]byte("1\n2\n3\n"))

	if err := p.CaptureLogs(t.Context(), id, resumed, ended); err != nil || resumed.String() != whole {
		t.Errorf("a resumed capture gave %d bytes, %v; want the %d of the whole", len(resumed.String()), err, len(whole))
	}
}
