// -------------------------------------------------------------------------------
// Cloud Run Configuration
//
// Author: Alex Freidah
//
// The four values a deployment has to state, decoded from the provider's own
// config block. Vagabond hands that block over undecoded, so a typo here is
// reported against the line the operator wrote it on rather than surfacing as
// a missing field at dispatch.
// -------------------------------------------------------------------------------

package gcp

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Type is what a provider block names to select this plugin.
const Type = "cloud-run"

// Config is what Cloud Run needs beyond a credential.
//
// RuntimeServiceAccount is what the container executes as, and is deliberately
// not the identity Vagabond authenticates with. A workload running as the
// dispatcher could create further jobs; running as an account holding nothing
// means a compromised dependency reaches nothing.
type Config struct {
	Project               string `hcl:"project"`
	Region                string `hcl:"region"`
	RuntimeServiceAccount string `hcl:"runtime_service_account"`
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// decodeConfig reads the provider's own block.
//
// A missing block is its own diagnostic rather than three complaints about
// absent attributes, because the fix is different: one is a block to add and
// the other is a field to correct.
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics) {
	if body == nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing provider configuration",
			Detail: fmt.Sprintf("Provider %q is a %s provider and declares no config "+
				"block. It needs project, region and runtime_service_account.",
				name, Type),
		}}
	}

	var cfg Config

	diags := gohcl.DecodeBody(body, nil, &cfg)
	if diags.HasErrors() {
		return nil, diags
	}

	return &cfg, append(diags, cfg.validate(name)...)
}

// validate reports configuration that decoded but cannot be used.
//
// gohcl already refuses an absent attribute, so what is left is one present
// and empty, which would otherwise build a URL with a hole in it and fail
// against Google with something unrecognisable.
func (c *Config) validate(name string) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for _, field := range []struct{ label, value string }{
		{"project", c.Project},
		{"region", c.Region},
		{"runtime_service_account", c.RuntimeServiceAccount},
	} {
		if strings.TrimSpace(field.value) == "" {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Empty provider setting",
				Detail: fmt.Sprintf("Provider %q sets %s to an empty string.",
					name, field.label),
			})
		}
	}

	return diags
}

// -------------------------------------------------------------------------
// ENDPOINTS
//
// Built from a base the provider holds rather than a constant, so a test can
// point the whole plugin at an httptest server and exercise real request
// building, headers, encoding and status handling. Mocking the HTTP client
// instead would assert that we called Do with the right arguments, which is a
// weaker claim than that Google would have understood us.
// -------------------------------------------------------------------------

// jobsURL is the collection every job of this provider lives in.
func (c *Config) jobsURL(base string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs",
		base, c.Project, c.Region)
}

// jobURL addresses one job by name.
func (c *Config) jobURL(base, job string) string {
	return c.jobsURL(base) + "/" + job
}
