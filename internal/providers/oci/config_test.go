// -------------------------------------------------------------------------------
// OCI Functions Configuration and Credential Tests
//
// Author: Alex Freidah
//
// The config block and the key are what an operator writes by hand, so each
// mistake is checked to be reported as itself rather than surfacing later as a
// request OCI refused.
// -------------------------------------------------------------------------------

package oci

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin/plugintest"
)

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// A block naming the region and the key decodes.
func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	cfg, diags := decodeConfig("oci", plugintest.Body(t, testConfig))
	if diags.HasErrors() {
		t.Fatalf("decode failed: %s", diags.Error())
	}

	if cfg.Region != "us-ashburn-1" || cfg.Tenancy != testTenancy || cfg.User != testUser || cfg.Fingerprint != testFingerprint {
		t.Errorf("Config = %+v", cfg)
	}
}

// Each malformed block is refused with its own diagnostic.
func TestDecodeConfig_Refusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body hcl.Body
		want string
	}{
		{name: "no block", body: nil, want: "Missing provider configuration"},
		{name: "empty user", body: plugintest.Body(t, strings.Replace(testConfig, testUser, " ", 1)), want: "sets user to an empty string"},
		{name: "no fingerprint", body: plugintest.Body(t, strings.Split(testConfig, "fingerprint")[0]), want: "Missing required argument"},
		{name: "typo", body: plugintest.Body(t, testConfig+"regoin = \"x\"\n"), want: "Unsupported argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, diags := decodeConfig("oci", tt.body)
			if !diags.HasErrors() || !strings.Contains(diags.Error(), tt.want) {
				t.Errorf("diags = %v, want %q", diags, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CREDENTIALS
// -------------------------------------------------------------------------

// A PEM key builds a provider calling its region's API; no key, or one that
// does not parse, is reported against the provider.
func TestNew(t *testing.T) {
	t.Parallel()

	p, diags := New(t.Context(), "oci", plugintest.Body(t, testConfig), testKeyPEM(t))
	if diags.HasErrors() {
		t.Fatalf("New() failed: %s", diags.Error())
	}

	if p.Name() != "oci" || p.Client() == nil || p.endpoint != "https://functions.us-ashburn-1.oci.oraclecloud.com" {
		t.Errorf("New() = %+v, want the name, a client and the region's endpoint", p)
	}

	for name, key := range map[string][]byte{"no key": nil, "not a key": []byte("not a key")} {
		if _, diags := New(t.Context(), "oci", plugintest.Body(t, testConfig), key); !diags.HasErrors() {
			t.Errorf("New() with %s was accepted", name)
		}
	}
}

// A refreshed key replaces the client; one that does not parse leaves the
// client in place.
func TestRecredential_ReplacesTheClient(t *testing.T) {
	t.Parallel()

	p, diags := New(t.Context(), "oci", plugintest.Body(t, testConfig), testKeyPEM(t))
	if diags.HasErrors() {
		t.Fatalf("New() failed: %s", diags.Error())
	}

	before := p.Client()

	if err := p.Recredential(t.Context(), []byte("not a key")); err == nil || p.Client() != before {
		t.Fatalf("Recredential(garbage) = %v, and the client changed: %v", err, p.Client() != before)
	}

	if err := p.Recredential(t.Context(), testKeyPEM(t)); err != nil || p.Client() == before {
		t.Errorf("Recredential() = %v, client replaced: %v; want it replaced", err, p.Client() != before)
	}
}
