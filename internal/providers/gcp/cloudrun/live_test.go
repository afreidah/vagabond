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
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
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

// A failing container comes back with its exit code and both streams, read
// whole and followed while it runs.
func TestLiveRoundTrip(t *testing.T) {
	gcptest.Live.RoundTrip(t, liveProvider(t), job.Resources{CPU: new(1000), Memory: new(512)})
}

// -------------------------------------------------------------------------
// LATE FOLLOW
// -------------------------------------------------------------------------

// Output past 64 KiB comes back whole, and a follow that attaches after the
// container has printed most of it starts at the first line, as does one
// reopened at an offset. The container prints, then waits, so the follow
// attaches late; 12,000 lines keep each read to 12 of the 60 list calls a
// minute allows.
func TestLiveLateFollow(t *testing.T) {
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

	// The first output is logged before the follow opens
	time.Sleep(20 * time.Second)

	var streamed bytes.Buffer

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- p.StreamLogs(ctx, id, 0, &streamed) }()

	gcptest.Live.Finish(t, p, id)

	// A tail trails the container by seconds, and never ends by itself
	time.Sleep(15 * time.Second)
	cancel()

	if err := <-done; err != nil {
		t.Errorf("StreamLogs failed: %v", err)
	}

	whole := plugintest.ReadAll(t, p, id)

	if !strings.HasPrefix(whole, "1\n2\n") || !strings.HasSuffix(whole, "12000\nthe-end\n") || len(whole) <= 64<<10 {
		t.Errorf("ReadLogs gave %d bytes, want all of the output", len(whole))
	}

	if streamed.String() != whole {
		t.Errorf("the late follow streamed %d bytes, want the %d stored, from the first line", streamed.Len(), len(whole))
	}

	// Reopened at an offset, it picks up exactly there
	var rest bytes.Buffer

	resumed, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()

	if err := p.StreamLogs(resumed, id, 2, &rest); err != nil {
		t.Errorf("StreamLogs from an offset failed: %v", err)
	}

	if rest.String() != whole[2:] {
		t.Errorf("StreamLogs from offset 2 gave %d bytes, want the %d after it", rest.Len(), len(whole)-2)
	}
}
