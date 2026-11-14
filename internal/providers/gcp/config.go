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

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin"
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

// decodeConfig reads the provider's own block, refusing a value present but
// empty, which would build a URL with a hole in it.
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, Type, body, "project, region and runtime_service_account")
	if cfg == nil {
		return nil, diags
	}

	return cfg, append(diags, plugin.EmptySettings(name, map[string]string{
		"project":                 cfg.Project,
		"region":                  cfg.Region,
		"runtime_service_account": cfg.RuntimeServiceAccount,
	})...)
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
