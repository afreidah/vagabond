// -------------------------------------------------------------------------------
// Provider Configuration
//
// Author: Alex Freidah
//
// Decoding a provider's config block, which every plugin reads into a type of
// its own. Kept here so a missing block and an empty setting read the same
// whichever provider they belong to.
// -------------------------------------------------------------------------------

package plugin

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// DecodeConfig decodes provider name's config block into a T. needs names
// what a provider that cannot run without the block must set; empty makes the
// block optional, and a missing one a zero T.
func DecodeConfig[T any](name, providerType string, body hcl.Body, needs string) (*T, hcl.Diagnostics) {
	var cfg T

	if body == nil {
		if needs == "" {
			return &cfg, nil
		}

		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing provider configuration",
			Detail: fmt.Sprintf("Provider %q is a %s provider and declares no config block. It needs %s.",
				name, providerType, needs),
		}}
	}

	diags := gohcl.DecodeBody(body, nil, &cfg)
	if diags.HasErrors() {
		return nil, diags
	}

	return &cfg, diags
}

// EmptySettings reports each setting, by the name it is written under, given
// as an empty string, which no provider can use.
func EmptySettings(name string, settings map[string]string) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for _, setting := range slices.Sorted(maps.Keys(settings)) {
		if strings.TrimSpace(settings[setting]) == "" {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Empty provider setting",
				Detail:   fmt.Sprintf("Provider %q sets %s to an empty string.", name, setting),
			})
		}
	}

	return diags
}
