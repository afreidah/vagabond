// -------------------------------------------------------------------------------
// Google API Client Tests
//
// Author: Alex Freidah
//
// The base both providers are built on: a usable key builds it against the
// real Cloud Logging endpoint, an unusable one stops startup, and a key
// resolved again replaces the client only when it parses. An external test
// package, so it can use the fixtures in gcptest.
// -------------------------------------------------------------------------------

package gcp_test

import (
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/providers/gcp"
	"github.com/afreidah/vagabond/internal/providers/gcp/gcptest"
)

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// A usable key and config build the base against the real Cloud Logging
// endpoint, holding a client.
func TestNewAPI_BuildsWithAKey(t *testing.T) {
	t.Parallel()

	api, diags := gcp.NewAPI(t.Context(), "gcp-cloud-run", "cloud-run", plugintest.Body(t, gcptest.Config), []byte(gcptest.Key))
	if diags.HasErrors() {
		t.Fatalf("NewAPI() failed: %s", diags.Error())
	}

	if api.Name() != "gcp-cloud-run" || api.LogsURL != "https://logging.googleapis.com" ||
		api.Config.Project != "test-project" || api.Client() == nil {
		t.Errorf("NewAPI() = %+v, want the name, the config, the real endpoint and a client", api)
	}
}

// A key that cannot be parsed, or none, is reported against the provider,
// since the alternative is a rejection on the first call.
func TestNewAPI_RejectsAnUnusableKey(t *testing.T) {
	t.Parallel()

	for _, key := range [][]byte{[]byte(`not json`), nil} {
		_, diags := gcp.NewAPI(t.Context(), "gcp-cloud-run", "cloud-run", plugintest.Body(t, gcptest.Config), key)
		if !diags.HasErrors() || !strings.Contains(diags.Error(), "Unusable credential") {
			t.Errorf("NewAPI(%q) = %v, want an unusable credential", key, diags)
		}
	}
}

// -------------------------------------------------------------------------
// CREDENTIALS
// -------------------------------------------------------------------------

// A refreshed key replaces the client; one that cannot be parsed leaves the
// client in place.
func TestRecredential_ReplacesTheClient(t *testing.T) {
	t.Parallel()

	api, diags := gcp.NewAPI(t.Context(), "gcp-cloud-run", "cloud-run", plugintest.Body(t, gcptest.Config), []byte(gcptest.Key))
	if diags.HasErrors() {
		t.Fatalf("NewAPI() failed: %s", diags.Error())
	}

	before := api.Client()

	if err := api.Recredential(t.Context(), []byte(`not json`)); err == nil || api.Client() != before {
		t.Fatalf("Recredential(garbage) = %v, and the client changed: %v", err, api.Client() != before)
	}

	if err := api.Recredential(t.Context(), []byte(gcptest.Key)); err != nil || api.Client() == before {
		t.Errorf("Recredential() = %v, client replaced: %v; want it replaced", err, api.Client() != before)
	}
}
