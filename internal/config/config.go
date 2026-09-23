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
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/quota"
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
//
// Config is left undecoded because its shape belongs to the plugin. Cloud Run
// needs a project, a region and a runtime identity; Code Engine needs a region
// and a project GUID; Azure would need a subscription and a resource group.
// Declaring any of them here makes every provider carry another's fields, so
// the plugin decodes its own block and reports its own diagnostics. The same
// reasoning as a task's driver config, for the same reason.
type Provider struct {
	Name    string `hcl:"name,label"`
	Type    string `hcl:"type"`
	Enabled *bool  `hcl:"enabled,optional"`

	Config      *job.RawBlock     `hcl:"config,block"`
	Credentials *CredentialsBlock `hcl:"credentials,block"`
	Meta        *MetaBlock        `hcl:"meta,block"`
	Quota       *QuotaBlock       `hcl:"quota,block"`
	Pools       []PoolBlock       `hcl:"pool,block"`
}

// PoolBlock is one usage budget an operator declares for a provider, in the
// unit that provider itself meters.
//
// Nothing ships a default. The limit encodes how much an operator is willing to
// spend on a backend, which for most is the free tier exactly and for some is
// deliberately more, so there is no correct number to supply. A provider with
// no pools enforces nothing.
type PoolBlock struct {
	Name   string `hcl:"name,label"`
	Meter  string `hcl:"meter"`
	Limit  int64  `hcl:"limit"`
	Period string `hcl:"period"`
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

// LoadPath reads configuration from a file or a directory.
//
// A directory loads every .hcl file inside it, sorted by name, and merges them
// into one configuration. Nomad's agent config works the same way, and it is
// what makes a provider per file possible: /etc/vagabond.d/ibm.hcl beside
// lambda.hcl, each one reviewable on its own.
//
// Merging is concatenation, because a File is a list of providers. Two files
// declaring the same provider name are caught by the duplicate check that
// already runs, and reported once rather than per file.
func LoadPath(path string) (*File, hcl.Diagnostics) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read configuration",
			Detail:   fmt.Sprintf("Reading %s: %s.", path, err),
		}}
	}

	if !info.IsDir() {
		return LoadFile(path)
	}

	return loadDir(path)
}

// loadDir merges every .hcl file in a directory.
func loadDir(dir string) (*File, hcl.Diagnostics) {
	// Sorted, because two files disagreeing should be reported the same way
	// every run, and because a reader looking for where a provider came from
	// should not have to know the order a filesystem happened to return.
	names, err := filepath.Glob(filepath.Join(dir, "*"+Extension))
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read configuration directory",
			Detail:   fmt.Sprintf("Listing %s: %s.", dir, err),
		}}
	}

	sort.Strings(names)

	if len(names) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Empty configuration directory",
			Detail: fmt.Sprintf("%s holds no %s files, so no provider is "+
				"configured.", dir, Extension),
		}}
	}

	var (
		merged File
		diags  hcl.Diagnostics
	)

	for _, name := range names {
		// Decoded without validating, so that a provider declared twice across
		// two files is reported once by the merged check below rather than
		// slipping past a per-file one.
		file, fileDiags := decode(name)
		diags = append(diags, fileDiags...)

		if file != nil {
			merged.Providers = append(merged.Providers, file.Providers...)
		}
	}

	return &merged, append(diags, merged.validate()...)
}

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
	file, diags := decodeSource(filename, src)
	if file == nil {
		return nil, diags
	}

	return file, append(diags, file.validate()...)
}

// decode reads and decodes one file without validating it.
//
// Separate from Load so that a directory can validate the merged result
// instead, and report a provider declared in two files once.
func decode(path string) (*File, hcl.Diagnostics) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read configuration",
			Detail:   fmt.Sprintf("Reading %s: %s.", path, err),
		}}
	}

	return decodeSource(path, src)
}

// decodeSource parses and decodes, leaving validation to the caller.
func decodeSource(filename string, src []byte) (*File, hcl.Diagnostics) {
	f, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if f == nil {
		return nil, diags
	}

	var file File

	return &file, append(diags, gohcl.DecodeBody(f.Body, nil, &file)...)
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

	diags = append(diags, p.Credentials.validate(p.Name)...)
	diags = append(diags, p.validatePools()...)

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

// validatePools reports budgets that cannot be enforced.
//
// Every attribute is required. A pool with no limit refuses nothing, and a
// daily budget left to default to monthly is enforced twelve times too
// loosely, which is the direction that spends money.
func (p *Provider) validatePools() hcl.Diagnostics {
	var diags hcl.Diagnostics

	seen := make(map[string]bool, len(p.Pools))

	for i := range p.Pools {
		pool := &p.Pools[i]

		switch {
		case seen[pool.Name]:
			diags = append(diags, poolDiag(p.Name, pool.Name, "is declared twice. A pool "+
				"name is what its usage is counted under, so it must identify one budget."))
		case !quota.Meter(pool.Meter).Valid():
			diags = append(diags, poolDiag(p.Name, pool.Name, fmt.Sprintf(
				"meters %q, which is not something Vagabond counts. Known meters are %s.",
				pool.Meter, joinMeters())))
		case !quota.Period(pool.Period).Valid():
			diags = append(diags, poolDiag(p.Name, pool.Name, fmt.Sprintf(
				"resets %q. Known periods are %s.", pool.Period, joinPeriods())))
		case pool.Limit <= 0:
			diags = append(diags, poolDiag(p.Name, pool.Name, fmt.Sprintf(
				"has a limit of %d. A pool exists to refuse an execution, so its limit "+
					"must be positive.", pool.Limit)))
		}

		seen[pool.Name] = true
	}

	return diags
}

// poolDiag builds one pool diagnostic, since each names the provider and the
// pool the same way.
func poolDiag(provider, pool, problem string) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid quota pool",
		Detail:   fmt.Sprintf("Provider %q pool %q %s", provider, pool, problem),
	}
}

// joinMeters and joinPeriods list the vocabulary for a diagnostic, so the
// message stays correct when the vocabulary grows.
func joinMeters() string {
	names := make([]string, 0, len(quota.Meters()))
	for _, m := range quota.Meters() {
		names = append(names, string(m))
	}

	return strings.Join(names, ", ")
}

func joinPeriods() string {
	names := make([]string, 0, len(quota.Periods()))
	for _, p := range quota.Periods() {
		names = append(names, string(p))
	}

	return strings.Join(names, ", ")
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

// ConfigBody returns the provider's own configuration block, undecoded.
//
// Nil when the provider declared none, which is correct for anything needing
// no settings. A plugin that requires some reports that itself, against the
// block it was expecting.
func (p *Provider) ConfigBody() hcl.Body {
	if p.Config == nil {
		return nil
	}

	return p.Config.Body
}

// PoolSpecs returns the declared budgets in the form quota compiles.
//
// Field mapping only. Whether the meter and period are ones Vagabond knows is
// the validate pass's business, so that an operator sees every problem with
// their file in one run rather than the first one at startup.
func (p *Provider) PoolSpecs() []quota.PoolSpec {
	if len(p.Pools) == 0 {
		return nil
	}

	specs := make([]quota.PoolSpec, 0, len(p.Pools))

	for _, pool := range p.Pools {
		specs = append(specs, quota.PoolSpec{
			Name:   pool.Name,
			Meter:  quota.Meter(pool.Meter),
			Limit:  pool.Limit,
			Period: quota.Period(pool.Period),
		})
	}

	return specs
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
