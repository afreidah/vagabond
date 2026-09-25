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
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// Type is what a provider block names to select this plugin.
const Type = "lambda"

// Config is what Lambda needs beyond a credential.
type Config struct {
	Region string `hcl:"region"`
}

// decodeConfig reads the provider's own block.
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics) {
	if body == nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing provider configuration",
			Detail: fmt.Sprintf("Provider %q is a %s provider and declares no config "+
				"block. It needs region.", name, Type),
		}}
	}

	var cfg Config

	diags := gohcl.DecodeBody(body, nil, &cfg)
	if diags.HasErrors() {
		return nil, diags
	}

	if strings.TrimSpace(cfg.Region) == "" {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Empty provider setting",
			Detail:   fmt.Sprintf("Provider %q sets region to an empty string.", name),
		})
	}

	return &cfg, diags
}
