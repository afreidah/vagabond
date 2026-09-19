// -------------------------------------------------------------------------------
// Daemon Configuration
//
// Author: Alex Freidah
//
// The providers a deployment can dispatch to, and what an operator wants to say
// about each. Decoded with the same machinery job files use, so a mistake here
// is reported the same way: with a line, a column, and every problem in one run.
// -------------------------------------------------------------------------------

package config

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// File is the decoded contents of one configuration file.
type File struct {
	Providers []Provider `hcl:"provider,block"`
}

// Provider is one backend a deployment can dispatch to.
//
// Name is the routing identifier a job's provider list refers to, and Type is
// which plugin implements it. They are separate so that one deployment can
// register the same plugin twice, against two accounts or two regions, and a
// job can name them apart.
//
// Enabled is a pointer so that an omitted value means the default rather than
// false. A provider listed in configuration and silently off would be the
// worst reading of a missing attribute.
type Provider struct {
	Name    string `hcl:"name,label"`
	Type    string `hcl:"type"`
	Enabled *bool  `hcl:"enabled,optional"`

	Meta  *MetaBlock  `hcl:"meta,block"`
	Quota *QuotaBlock `hcl:"quota,block"`
}

// MetaBlock holds an operator's own tags for a provider.
//
// These reach admission as provider.meta.* attributes, the open half of the
// attribute namespace. Vagabond never interprets them; a job may constrain on
// them and that is the whole contract.
type MetaBlock struct {
	Body hcl.Body `hcl:",remain"`
}

// QuotaBlock states a provider's free-tier standing.
//
// A stand-in. The ledger that tracks consumption and survives a restart does
// not exist yet, so until it does an operator states what they believe is left
// and admission reads that. It is here rather than absent because a provider
// whose quota is unknown is one admission refuses, which would leave nothing
// admissible and nothing demonstrable.
type QuotaBlock struct {
	FreePercent *int  `hcl:"free_percent,optional"`
	Exhausted   *bool `hcl:"exhausted,optional"`
}

// -------------------------------------------------------------------------
// LOADING
// -------------------------------------------------------------------------

// LoadFile reads and decodes the named configuration file.
func LoadFile(path string) (*File, hcl.Diagnostics) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read configuration",
			Detail:   fmt.Sprintf("Reading %s: %s.", path, err),
		}}
	}

	return Load(path, src)
}

// Load decodes configuration from source.
//
// Returns whatever decoded alongside its diagnostics, so that a file with one
// bad provider still reports the rest.
func Load(filename string, src []byte) (*File, hcl.Diagnostics) {
	f, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if f == nil {
		return nil, diags
	}

	var file File

	diags = append(diags, gohcl.DecodeBody(f.Body, nil, &file)...)
	diags = append(diags, file.validate()...)

	return &file, diags
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// validate reports configuration that decoded but means nothing.
func (f *File) validate() hcl.Diagnostics {
	var diags hcl.Diagnostics

	seen := make(map[string]bool, len(f.Providers))

	for i := range f.Providers {
		p := &f.Providers[i]

		if seen[p.Name] {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate provider",
				Detail: fmt.Sprintf("Two providers are named %q. The name is what a "+
					"job's provider list refers to, so it must identify one.", p.Name),
			})
		}

		seen[p.Name] = true

		diags = append(diags, p.validate()...)
	}

	return diags
}

// validate reports one provider that means nothing.
func (p *Provider) validate() hcl.Diagnostics {
	var diags hcl.Diagnostics

	if p.Type == "" {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing provider type",
			Detail: fmt.Sprintf("Provider %q does not say what kind it is, so nothing "+
				"can be constructed for it.", p.Name),
		})
	}

	if p.Quota == nil || p.Quota.FreePercent == nil {
		return diags
	}

	if percent := *p.Quota.FreePercent; percent < 0 || percent > 100 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid free quota",
			Detail: fmt.Sprintf("Provider %q reports %d percent remaining. A percentage "+
				"runs from 0 to 100.", p.Name, percent),
		})
	}

	return diags
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// IsEnabled reports whether the provider should be considered.
//
// Enabled by default. An operator who took the trouble to write a provider
// block meant it to be used, and turning it off is the deliberate act.
func (p *Provider) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}

	return *p.Enabled
}

// Tags returns the operator's own labels for this provider.
func (p *Provider) Tags() (map[string]string, hcl.Diagnostics) {
	if p.Meta == nil || p.Meta.Body == nil {
		return nil, nil
	}

	attrs, diags := p.Meta.Body.JustAttributes()
	if attrs == nil {
		return nil, diags
	}

	tags := make(map[string]string, len(attrs))

	for name, attr := range attrs {
		value, valueDiags := attr.Expr.Value(nil)
		diags = append(diags, valueDiags...)

		if value.IsNull() || !value.IsKnown() || value.Type() != cty.String {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid provider tag",
				Detail:   fmt.Sprintf("The value for %q must be a string.", name),
				Subject:  attr.Expr.Range().Ptr(),
			})

			continue
		}

		tags[name] = value.AsString()
	}

	return tags, diags
}
