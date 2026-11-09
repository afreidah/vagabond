// -------------------------------------------------------------------------------
// Cloud Run Configuration
//
// Author: Alex Freidah
//
// The values a deployment has to state, decoded from the provider's own config
// block. Vagabond hands that block over undecoded, so a typo is reported
// against the line the operator wrote it on rather than at dispatch.
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

// Config is what Cloud Run needs beyond a credential. RuntimeServiceAccount is
// what the container executes as, deliberately an identity holding nothing.
type Config struct {
	Project               string `hcl:"project"`
	Region                string `hcl:"region"`
	RuntimeServiceAccount string `hcl:"runtime_service_account"`
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// decodeConfig reads the provider's own block. A missing block is one
// diagnostic rather than three about absent attributes, since its fix differs.
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

// validate reports configuration that decoded but cannot be used: a value
// present but empty, which would build a URL with a hole in it.
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
// -------------------------------------------------------------------------

// jobsURL is the collection every job of this provider lives in, built from a
// base the provider holds so a test can point it at an httptest server.
func (c *Config) jobsURL(base string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs",
		base, c.Project, c.Region)
}

// jobURL addresses one job by name.
func (c *Config) jobURL(base, job string) string {
	return c.jobsURL(base) + "/" + job
}
