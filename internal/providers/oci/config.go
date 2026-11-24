// -------------------------------------------------------------------------------
// OCI Functions Configuration
//
// Author: Alex Freidah
//
// Decoded from the provider's own config block, so a typo is reported against
// the line the operator wrote it on. The block names the API key and the
// region; the credential is only the private key requests are signed with.
// -------------------------------------------------------------------------------

package oci

import (
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin"
)

// Type is what a provider block names to select this plugin.
const Type = "oci-functions"

// Config is what OCI Functions needs beyond the private key: the region its
// API is called in, and the tenancy, user and fingerprint that name the key.
type Config struct {
	Region      string `hcl:"region"`
	Tenancy     string `hcl:"tenancy"`
	User        string `hcl:"user"`
	Fingerprint string `hcl:"fingerprint"`
}

// decodeConfig reads the provider's own block, refusing a setting left empty,
// since every one of them is part of each request's signature.
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, Type, body, "region, tenancy, user and fingerprint")
	if cfg == nil {
		return nil, diags
	}

	return cfg, append(diags, plugin.EmptySettings(name, map[string]string{
		"region":      cfg.Region,
		"tenancy":     cfg.Tenancy,
		"user":        cfg.User,
		"fingerprint": cfg.Fingerprint,
	})...)
}
