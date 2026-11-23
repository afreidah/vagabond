// -------------------------------------------------------------------------------
// Google Provider Configuration
//
// Author: Alex Freidah
//
// The values a deployment has to state, decoded from the provider's own config
// block, and the run limit, the same for Cloud Run and Cloud Build. Vagabond
// hands that block over undecoded, so a typo is reported against the line the
// operator wrote it on rather than at dispatch.
// -------------------------------------------------------------------------------

package gcp

import (
	"cmp"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is what a Google provider needs beyond a credential.
// RuntimeServiceAccount is what the container executes as, deliberately an
// identity holding nothing but what the platform requires of it.
type Config struct {
	Project               string `hcl:"project"`
	Region                string `hcl:"region"`
	RuntimeServiceAccount string `hcl:"runtime_service_account"`
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// decodeConfig reads a provider's own block, refusing a value present but
// empty, which would build a URL with a hole in it.
func decodeConfig(name, providerType string, body hcl.Body) (*Config, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, providerType, body, "project, region and runtime_service_account")
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
// RUN LIMIT
// -------------------------------------------------------------------------

// MaxDuration is the longest a Cloud Run task or a Cloud Build build may run.
const MaxDuration = 24 * time.Hour

// Timeout renders a timeout in whole seconds as Cloud Run and Cloud Build
// both take it, or MaxDuration when the task sets none, since zero would end
// the run as it started.
func Timeout(d time.Duration) string {
	return fmt.Sprintf("%ds", int(cmp.Or(d, MaxDuration).Seconds()))
}
