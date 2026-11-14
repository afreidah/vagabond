// -------------------------------------------------------------------------------
// Lambda Configuration
//
// Author: Alex Freidah
//
// Decoded from the provider's own config block, so a typo is reported against
// the line the operator wrote it on.
// -------------------------------------------------------------------------------

package aws

import (
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin"
)

// Type is what a provider block names to select this plugin.
const Type = "lambda"

// Config is what Lambda needs beyond a credential.
type Config struct {
	Region string `hcl:"region"`
}

// decodeConfig reads the provider's own block, refusing an empty region.
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, Type, body, "region")
	if cfg == nil {
		return nil, diags
	}

	return cfg, append(diags, plugin.EmptySettings(name, map[string]string{"region": cfg.Region})...)
}
