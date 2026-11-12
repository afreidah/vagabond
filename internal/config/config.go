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
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// File is the decoded contents of one configuration file.
type File struct {
	Server     *ServerBlock     `hcl:"server,block"`
	Store      *StoreBlock      `hcl:"store,block"`
	Scheduling *SchedulingBlock `hcl:"scheduling,block"`
	Providers  []Provider       `hcl:"provider,block"`
	Namespaces []Namespace      `hcl:"namespace,block"`
}

// SchedulingBlock sets how the server orders the providers admission passed.
// Absent means the defaults.
type SchedulingBlock struct {
	Tiers *job.TierMode `hcl:"tiers,optional"` // a job's routing.tiers overrides it
}

// TierMode returns how provider tiers order candidates when a job does not
// say. A nil block is the default.
func (s *SchedulingBlock) TierMode() job.TierMode {
	if s == nil || s.Tiers == nil {
		return job.DefaultTierMode
	}

	return *s.Tiers
}

// Where the server listens for API clients and for agents when nothing says
// otherwise: localhost only, since without a CA to verify clients against the
// API accepts anyone who can reach it.
const (
	DefaultBind      = "127.0.0.1:4747"
	DefaultAgentBind = "127.0.0.1:4748"
)

// ServerBlock configures vagabond server. Absent means the defaults. TLS
// covers the API; AgentTLS covers the agent listener at AgentBind. MaxLogMiB
// is how much of an execution's output is kept, the end of it, and
// LogRetention how long it is kept after the execution ends.
type ServerBlock struct {
	Bind         *string       `hcl:"bind,optional"`
	AgentBind    *string       `hcl:"agent_bind,optional"`
	MaxLogMiB    *int          `hcl:"max_log_mib,optional"`
	LogRetention *job.Duration `hcl:"log_retention,optional"`
	TLS          *TLSBlock     `hcl:"tls,block"`
	AgentTLS     *TLSBlock     `hcl:"agent_tls,block"`
}

// TLSBlock is one listener's TLS: the certificate and key the server presents,
// and the CA its clients' certificates must chain to. The API verifies clients
// only when CA is set; the agent listener always does, so agent_tls needs one.
type TLSBlock struct {
	Cert string `hcl:"cert"`
	Key  string `hcl:"key"`
	CA   string `hcl:"ca,optional"`
}

// Address returns where the server listens. A nil block is the default.
func (s *ServerBlock) Address() string {
	if s == nil || s.Bind == nil {
		return DefaultBind
	}

	return *s.Bind
}

// AgentAddress returns where the server listens for agents. A nil block is the
// default.
func (s *ServerBlock) AgentAddress() string {
	if s == nil || s.AgentBind == nil {
		return DefaultAgentBind
	}

	return *s.AgentBind
}

// MaxLogBytes returns how much of an execution's output is kept. A nil block or
// an unset one is the default.
func (s *ServerBlock) MaxLogBytes() int64 {
	if s == nil || s.MaxLogMiB == nil {
		return execution.DefaultMaxLogBytes
	}

	return int64(*s.MaxLogMiB) << 20
}

// RetainLogs returns how long an execution's output is kept after it ends. A
// nil block or an unset one is the default; validation has proved a set one
// parses.
func (s *ServerBlock) RetainLogs() time.Duration {
	if s == nil || s.LogRetention == nil {
		return execution.DefaultLogRetention
	}

	retain, _ := s.LogRetention.Std()

	return retain
}

// Namespace is an owner of jobs and, optionally, of its own share of each
// provider's allowance. The default namespace exists without being declared.
//
// A namespace's pools sit inside the provider's, never beside them: an
// execution charges both and needs room in both, so the shares cannot add up
// past what the provider offers.
type Namespace struct {
	Name   string           `hcl:"name,label"`
	Quotas []NamespaceQuota `hcl:"quota,block"`
}

// NamespaceQuota is a namespace's share of one provider, labelled with the
// provider's name.
type NamespaceQuota struct {
	Provider string      `hcl:"provider,label"`
	Pools    []PoolBlock `hcl:"pool,block"`
}

// StoreBlock is where the usage ledger persists. Absent means the ledger lives
// in memory and starts empty every run.
//
// DSN is anything pgx accepts. A password can stay out of it: pgx reads
// PGPASSWORD and ~/.pgpass as libpq does.
type StoreBlock struct {
	DSN string `hcl:"dsn"`
}

// Provider is one backend a deployment can dispatch to.
//
// Name is the routing identifier a job refers to and Type is the plugin that
// implements it, so one plugin can be registered twice against two accounts.
// Enabled is a pointer so an omitted value means enabled, not false. Config is
// undecoded because its shape belongs to the plugin, which decodes and reports
// on its own block.
type Provider struct {
	Name    string `hcl:"name,label"`
	Type    string `hcl:"type"`
	Enabled *bool  `hcl:"enabled,optional"`
	Tier    *int   `hcl:"tier,optional"` // lower is tried first; 0 when unset

	Config      *job.RawBlock     `hcl:"config,block"`
	Credentials *CredentialsBlock `hcl:"credentials,block"`
	Meta        *MetaBlock        `hcl:"meta,block"`
	Pools       []PoolBlock       `hcl:"pool,block"`
}

// PoolBlock is one usage budget an operator declares for a provider, in the
// unit that provider itself meters.
//
// Nothing ships a default, since the limit encodes how much an operator is
// willing to use. A provider with no pools enforces nothing.
type PoolBlock struct {
	Name   string `hcl:"name,label"`
	Meter  string `hcl:"meter"`
	Limit  int64  `hcl:"limit"`
	Period string `hcl:"period"`
}

// MetaBlock holds an operator's own tags for a provider. They reach admission
// as provider.meta.* attributes, which Vagabond never interprets.
type MetaBlock struct {
	Body hcl.Body `hcl:",remain"`
}

// -------------------------------------------------------------------------
// LOADING
// -------------------------------------------------------------------------

// LoadPath reads configuration from a file, or from every .hcl file in a
// directory merged in name order, so each provider can live in its own file.
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
	// Sorted so a conflict between files is reported the same way every run
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
		// Unvalidated, so a cross-file duplicate is reported once below
		file, fileDiags := decode(name)
		diags = append(diags, fileDiags...)

		if file == nil {
			continue
		}

		merged.Providers = append(merged.Providers, file.Providers...)
		merged.Namespaces = append(merged.Namespaces, file.Namespaces...)

		// A second singleton block is refused rather than chosen by file order
		switch {
		case file.Store != nil && merged.Store != nil:
			diags = append(diags, duplicate(name, "store", "A deployment has one ledger."))
		case file.Store != nil:
			merged.Store = file.Store
		}

		switch {
		case file.Server != nil && merged.Server != nil:
			diags = append(diags, duplicate(name, "server", "A deployment has one server."))
		case file.Server != nil:
			merged.Server = file.Server
		}

		switch {
		case file.Scheduling != nil && merged.Scheduling != nil:
			diags = append(diags, duplicate(name, "scheduling", "A deployment orders providers one way."))
		case file.Scheduling != nil:
			merged.Scheduling = file.Scheduling
		}
	}

	return &merged, append(diags, merged.validate()...)
}

// duplicate reports a block that may appear once, declared again in file.
func duplicate(file, block, why string) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  fmt.Sprintf("Duplicate %s", block),
		Detail: fmt.Sprintf("%s declares a %s block, but one is already declared in an "+
			"earlier file. %s", file, block, why),
	}
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

// Load decodes configuration from source, returning what decoded alongside its
// diagnostics so one bad provider does not hide the rest.
func Load(filename string, src []byte) (*File, hcl.Diagnostics) {
	file, diags := decodeSource(filename, src)
	if file == nil {
		return nil, diags
	}

	return file, append(diags, file.validate()...)
}

// decode reads and decodes one file without validating it, so a directory can
// validate the merged result instead.
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

	if f.Store != nil && f.Store.DSN == "" {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Empty store DSN",
			Detail: "The store block names no database. Set dsn to a Postgres or " +
				"CockroachDB connection string, or run the server with -dev to keep " +
				"everything in memory.",
		})
	}

	diags = append(diags, f.Server.validate()...)

	if mode := f.Scheduling.TierMode(); !mode.Valid() {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unknown tier mode",
			Detail: fmt.Sprintf("The scheduling block sets tiers to %q. Valid modes are "+
				"strict and weighted.", mode),
		})
	}

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

	return append(diags, f.validateNamespaces(seen)...)
}

// validate reports server settings that cannot be applied: an agent listener
// that could not verify agents, a ceiling that keeps no output, and a
// retention that does not parse or keeps nothing.
func (s *ServerBlock) validate() hcl.Diagnostics {
	var diags hcl.Diagnostics

	if s == nil {
		return nil
	}

	if s.AgentTLS != nil && s.AgentTLS.CA == "" {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing agent CA",
			Detail: "The agent_tls block names no ca. An agent registers under its " +
				"certificate's common name, so every agent certificate must be " +
				"verified against a CA.",
		})
	}

	if s.MaxLogMiB != nil && *s.MaxLogMiB < 1 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid max_log_mib",
			Detail: fmt.Sprintf("The server block sets max_log_mib to %d. It is how many "+
				"MiB of each execution's output are kept, so it must be at least 1.", *s.MaxLogMiB),
		})
	}

	if s.LogRetention != nil {
		if retain, err := s.LogRetention.Std(); err != nil || retain <= 0 {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid log_retention",
				Detail: fmt.Sprintf("The server block sets log_retention to %q. It is how long "+
					"output is kept after its execution ends, a duration above zero such as "+
					"\"168h\".", *s.LogRetention),
			})
		}
	}

	return diags
}

// validateNamespaces reports namespaces declared twice, and shares of
// providers that do not exist or are declared twice.
func (f *File) validateNamespaces(providers map[string]bool) hcl.Diagnostics {
	var diags hcl.Diagnostics

	seen := make(map[string]bool, len(f.Namespaces))

	for i := range f.Namespaces {
		ns := &f.Namespaces[i]

		if seen[ns.Name] {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate namespace",
				Detail:   fmt.Sprintf("Two namespaces are named %q.", ns.Name),
			})
		}

		seen[ns.Name] = true

		shares := make(map[string]bool, len(ns.Quotas))

		for j := range ns.Quotas {
			q := &ns.Quotas[j]
			owner := fmt.Sprintf("Namespace %q quota for provider %q", ns.Name, q.Provider)

			switch {
			case !providers[q.Provider]:
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Unknown provider in namespace quota",
					Detail:   owner + " names a provider that is not configured.",
				})
			case shares[q.Provider]:
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate namespace quota",
					Detail: fmt.Sprintf("Namespace %q declares two quotas for provider %q.",
						ns.Name, q.Provider),
				})
			}

			shares[q.Provider] = true

			diags = append(diags, validatePools(owner, q.Pools)...)
		}
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

	if p.TierOf() < 0 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid provider tier",
			Detail: fmt.Sprintf("Provider %q has tier %d. Tiers start at 0, the most "+
				"preferred.", p.Name, p.TierOf()),
		})
	}

	diags = append(diags, p.Credentials.validate(p.Name)...)
	diags = append(diags, validatePools(fmt.Sprintf("Provider %q", p.Name), p.Pools)...)

	return diags
}

// validatePools reports budgets that cannot be enforced, owner naming whose
// they are. Every attribute is required, since no default is safe to assume.
func validatePools(owner string, pools []PoolBlock) hcl.Diagnostics {
	var diags hcl.Diagnostics

	seen := make(map[string]bool, len(pools))

	for i := range pools {
		pool := &pools[i]

		switch {
		case seen[pool.Name]:
			diags = append(diags, poolDiag(owner, pool.Name, "is declared twice. A pool "+
				"name is what its usage is counted under, so it must identify one budget."))
		case !quota.Meter(pool.Meter).Valid():
			diags = append(diags, poolDiag(owner, pool.Name, fmt.Sprintf(
				"meters %q, which is not something Vagabond counts. Known meters are %s.",
				pool.Meter, joinMeters())))
		case !quota.Period(pool.Period).Valid():
			diags = append(diags, poolDiag(owner, pool.Name, fmt.Sprintf(
				"resets %q. Known periods are %s.", pool.Period, joinPeriods())))
		case pool.Limit <= 0:
			diags = append(diags, poolDiag(owner, pool.Name, fmt.Sprintf(
				"has a limit of %d. A pool exists to refuse an execution, so its limit "+
					"must be positive.", pool.Limit)))
		}

		seen[pool.Name] = true
	}

	return diags
}

// poolDiag builds one pool diagnostic, since each names the owner and the pool
// the same way.
func poolDiag(owner, pool, problem string) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid quota pool",
		Detail:   fmt.Sprintf("%s pool %q %s", owner, pool, problem),
	}
}

// joinMeters lists the known meters for a diagnostic.
func joinMeters() string {
	names := make([]string, 0, len(quota.Meters()))
	for _, m := range quota.Meters() {
		names = append(names, string(m))
	}

	return strings.Join(names, ", ")
}

// joinPeriods lists the known periods for a diagnostic.
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

// IsEnabled reports whether the provider should be considered. It defaults to
// true, so turning a declared provider off is the deliberate act.
func (p *Provider) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}

	return *p.Enabled
}

// TierOf returns the provider's tier, 0 when unset.
func (p *Provider) TierOf() int {
	if p.Tier == nil {
		return 0
	}

	return *p.Tier
}

// ConfigBody returns the provider's own configuration block, undecoded, or nil
// when none was declared; a plugin that needs one reports that itself.
func (p *Provider) ConfigBody() hcl.Body {
	if p.Config == nil {
		return nil
	}

	return p.Config.Body
}

// PoolSpecs returns the declared budgets in the form quota compiles. It maps
// fields only; validate checks the meters and periods.
func (p *Provider) PoolSpecs() []quota.PoolSpec {
	return poolSpecs(p.Pools)
}

// PoolSpecs returns a namespace's share of one provider in the form quota
// compiles.
func (q *NamespaceQuota) PoolSpecs() []quota.PoolSpec {
	return poolSpecs(q.Pools)
}

// poolSpecs maps pool blocks to specs. Nil for none.
func poolSpecs(pools []PoolBlock) []quota.PoolSpec {
	if len(pools) == 0 {
		return nil
	}

	specs := make([]quota.PoolSpec, 0, len(pools))

	for _, pool := range pools {
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
