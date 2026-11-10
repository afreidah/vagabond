// -------------------------------------------------------------------------------
// Code Engine Configuration Tests
//
// Author: Alex Freidah
//
// Decoding the provider's own block, and the checks that catch a wrong region
// or project before Code Engine answers it with a 403.
// -------------------------------------------------------------------------------

package ibm

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// A complete block decodes into its fields.
func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	cfg, diags := decodeConfig("ibm", body(t, `
region       = "us-east"
project_id   = "`+testProject+`"
image_secret = "ghcr"
`))
	if diags.HasErrors() {
		t.Fatalf("decodeConfig() = %s", diags.Error())
	}

	if cfg.Region != "us-east" || cfg.ProjectID != testProject || cfg.ImageSecret == nil || *cfg.ImageSecret != "ghcr" {
		t.Errorf("decodeConfig() = %+v", cfg)
	}
}

// A missing block is one diagnostic naming what the block needs.
func TestDecodeConfig_NoBlock(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("ibm", nil)
	if len(diags) != 1 || !strings.Contains(diags[0].Detail, "region and project_id") {
		t.Fatalf("decodeConfig(nil) = %v", diags)
	}
}

// A field present but empty would build a URL with a hole in it.
func TestDecodeConfig_RejectsEmptySettings(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("ibm", body(t, `
region       = ""
project_id   = "`+testProject+`"
image_secret = " "
`))

	var empty []string

	for _, d := range diags {
		if d.Summary == "Empty provider setting" {
			empty = append(empty, d.Detail)
		}
	}

	if len(empty) != 2 {
		t.Fatalf("empty-setting diagnostics = %v, want region and image_secret", empty)
	}
}

// A project_id that is not a UUID is refused before any call is made.
func TestDecodeConfig_RejectsANonUUIDProject(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("ibm", body(t, `
region     = "us-east"
project_id = "vagabond"
`))
	if !diags.HasErrors() || !strings.Contains(diags.Error(), "Invalid project_id") {
		t.Fatalf("decodeConfig() = %v, want an invalid project_id", diags)
	}
}

// An unknown region is warned about, not refused, so a new one still works.
func TestDecodeConfig_WarnsOnAnUnknownRegion(t *testing.T) {
	t.Parallel()

	cfg, diags := decodeConfig("ibm", body(t, `
region     = "mars-north"
project_id = "`+testProject+`"
`))
	if diags.HasErrors() || cfg == nil {
		t.Fatalf("decodeConfig() = %v, want a warning only", diags)
	}

	if len(diags) != 1 || diags[0].Severity != hcl.DiagWarning {
		t.Errorf("diagnostics = %v, want one warning", diags)
	}
}

// An attribute the plugin does not know is an error against its line.
func TestDecodeConfig_ReportsUnknownAttributes(t *testing.T) {
	t.Parallel()

	_, diags := decodeConfig("ibm", body(t, `
region     = "us-east"
project_id = "`+testProject+`"
project    = "typo"
`))
	if !diags.HasErrors() {
		t.Fatal("an unknown attribute decoded")
	}
}

// -------------------------------------------------------------------------
// ENDPOINTS
// -------------------------------------------------------------------------

// Every v2 URL carries the project and the pinned version.
func TestURLs(t *testing.T) {
	t.Parallel()

	cfg := &Config{Region: "us-east", ProjectID: testProject}
	base := apiBase("us-east")

	cases := map[string]string{
		cfg.jobRunsURL(base):        base + "/v2/projects/" + testProject + "/job_runs?version=" + apiVersion,
		cfg.jobRunURL(base, "run1"): base + "/v2/projects/" + testProject + "/job_runs/run1?version=" + apiVersion,
		cfg.kubeconfigURL(base):     base + "/api/v1/project/" + testProject + "/config",
	}

	for got, want := range cases {
		if got != want {
			t.Errorf("URL = %q, want %q", got, want)
		}
	}
}
