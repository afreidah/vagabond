// -------------------------------------------------------------------------------
// Live Cloud Build Test
//
// Author: Alex Freidah
//
// Runs a real container as a build in a real project and reads back what it
// produced. Skipped unless a credential is supplied, so the default suite
// needs no cloud account anywhere:
//
//	VAGABOND_GCP_CREDENTIALS=/path/to/key.json \
//	VAGABOND_GCP_PROJECT=munchbox-66afc \
//	VAGABOND_GCP_RUNTIME_SA=vagabond-runtime@munchbox-66afc.iam.gserviceaccount.com \
//	go test -run TestLive -v -timeout 15m ./internal/providers/gcp/cloudbuild/
//
// The key's account needs to create builds and act as the runtime account,
// and the runtime account to write logs.
// -------------------------------------------------------------------------------

package cloudbuild

import (
	"testing"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/providers/gcp/gcptest"
)

// A failing container comes back with its exit code and both streams, read
// whole and followed while it runs, whatever size it asks the one machine for.
func TestLiveRoundTrip(t *testing.T) {
	body, credentials := gcptest.LiveConfig(t)

	p, diags := New(t.Context(), "gcp-build-live", body, credentials)
	if diags.HasErrors() {
		t.Fatalf("building the provider: %s", diags.Error())
	}

	gcptest.Live.RoundTrip(t, p, job.Resources{CPU: new(1000), Memory: new(512)})
}
