// -------------------------------------------------------------------------------
// Cloud Run Configuration Tests
//
// Author: Alex Freidah
//
// Configuration is the one part of a provider an operator writes by hand, so
// what matters here is that a mistake is reported as the mistake it was rather
// than surfacing later as a request Google could not parse.
// -------------------------------------------------------------------------------

package gcp

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// body parses a provider config block the way the config loader leaves it.
func body(t *testing.T, src string) hcl.Body {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "vagabond.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return f.Body
}

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	cfg, diags := decodeConfig("gcp", body(t, `
project                 = "munchbox-66afc"
region                  = "us-central1"
runtime_service_account = "vagabond-runtime@munchbox-66afc.iam.gserviceaccount.com"
`))
	if diags.HasErrors() {
		t.Fatalf("decode failed: %s", diags.Error())
	}

	if cfg.Project != "munchbox-66afc" || cfg.Region != "us-central1" {
		t.Errorf("decoded %+v", cfg)
	}
}

// A provider that declared no config block gets one diagnostic naming the
// block, not three about absent attributes: the fix is different.
func TestDecodeConfigWithNoBlock(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("gcp", nil)
	if !diags.HasErrors() {
		t.Fatal("a missing config block was accepted")
	}

	if len(diags) != 1 || !strings.Contains(diags[0].Summary, "Missing provider configuration") {
		t.Errorf("diagnostics = %s", diags.Error())
	}
}

// gohcl refuses an absent attribute; what it accepts is a present and empty
// one, which would build a URL with a hole in it.
func TestDecodeConfigRejectsEmptySettings(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("gcp", body(t, `
project                 = ""
region                  = "us-central1"
runtime_service_account = "  "
`))
	if !diags.HasErrors() {
		t.Fatal("empty settings were accepted")
	}

	var reported string
	for _, d := range diags {
		reported += d.Detail
	}

	if !strings.Contains(reported, "project") ||
		!strings.Contains(reported, "runtime_service_account") {
		t.Errorf("both empty fields were not reported: %s", reported)
	}
}

// A typo should name the line the operator wrote it on.
func TestDecodeConfigReportsUnknownAttributes(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("gcp", body(t, `
project                 = "p"
region                  = "r"
runtime_service_account = "sa"
regoin                  = "typo"
`))
	if !diags.HasErrors() {
		t.Fatal("an unknown attribute was accepted")
	}

	if got := diags[0].Subject; got == nil || got.Start.Line != 5 {
		t.Errorf("diagnostic did not point at line 5: %v", got)
	}
}

func TestJobURL(t *testing.T) {
	t.Parallel()

	cfg := &Config{Project: "p", Region: "us-central1"}

	want := "https://x/v2/projects/p/locations/us-central1/jobs/vagabond-abc"
	if got := cfg.jobURL("https://x", "vagabond-abc"); got != want {
		t.Errorf("jobURL = %q, want %q", got, want)
	}
}
