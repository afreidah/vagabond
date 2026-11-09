// -------------------------------------------------------------------------------
// Provider Registry
//
// Author: Alex Freidah
//
// Holds every configured provider alongside what was last observed about it,
// and produces the inputs admission reads. Admission is a pure function over
// snapshots, so the network calls that gather them happen here rather than on
// the path a plan takes. A disabled provider is kept rather than dropped, so a
// plan can say why it was not considered.
// -------------------------------------------------------------------------------

package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Registry is every provider a deployment can dispatch to.
//
// namespaces holds every declared namespace, each with its compiled shares by
// provider; a namespace with none maps to an empty map. mu guards what Refresh
// changes on the entries, since a server refreshes while serving plans.
type Registry struct {
	mu         sync.RWMutex
	entries    []*entry
	namespaces map[string]map[string]quota.Limits
}

// entry is one provider and everything last known about it.
//
// Capabilities are held rather than fetched on demand, because admission
// calls nothing. healthy is separate from enabled because an operator's off
// switch and a failed check are different rejections with different fixes.
// credentials is where the credential comes from, resolved again when the
// platform rejects it; recredentialed is when that last happened, under
// recredential.
type entry struct {
	name     string
	provider plugin.Provider
	tags     map[string]string
	limits   quota.Limits
	tier     int
	enabled  bool
	healthy  bool

	capabilities plugin.Capabilities

	credentials    *config.CredentialsBlock
	recredential   sync.Mutex
	recredentialed time.Time
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// Option configures what New builds providers with.
type Option func(*options)

// options is what the Options set.
type options struct {
	nodes *nodes.Conns
}

// WithNodes gives pool providers the server's connected agent nodes, which
// they are made of. Without it, a pool is a configuration error.
func WithNodes(conns *nodes.Conns) Option {
	return func(o *options) { o.nodes = conns }
}

// New builds a registry from configuration. ctx only resolves credentials;
// providers are constructed but not contacted until Refresh. Every provider
// is attempted even after one fails, so one run reports every config error.
func New(ctx context.Context, cfg *config.File, opts ...Option) (*Registry, hcl.Diagnostics) {
	if cfg == nil {
		return &Registry{}, nil
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}

	namespaces, diags := compileNamespaces(cfg.Namespaces)

	r := &Registry{
		entries:    make([]*entry, 0, len(cfg.Providers)),
		namespaces: namespaces,
	}

	for i := range cfg.Providers {
		e, entryDiags := newEntry(ctx, &cfg.Providers[i], o.nodes)

		diags = append(diags, entryDiags...)

		if e != nil {
			r.entries = append(r.entries, e)
		}
	}

	// Sorted once, so plans list providers by name whatever the config order.
	slices.SortFunc(r.entries, func(a, b *entry) int {
		return strings.Compare(a.name, b.name)
	})

	return r, diags
}

// compileNamespaces compiles every namespace's shares. Config validation
// already reported a bad pool against its line; this is the backstop.
func compileNamespaces(namespaces []config.Namespace) (map[string]map[string]quota.Limits, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	compiled := make(map[string]map[string]quota.Limits, len(namespaces))

	for i := range namespaces {
		ns := &namespaces[i]
		shares := make(map[string]quota.Limits, len(ns.Quotas))

		for j := range ns.Quotas {
			q := &ns.Quotas[j]

			limits, err := quota.NewLimits(q.PoolSpecs())
			if err != nil {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid quota pools",
					Detail:   fmt.Sprintf("Namespace %q quota for provider %q: %s.", ns.Name, q.Provider, err),
				})

				continue
			}

			shares[q.Provider] = limits
		}

		compiled[ns.Name] = shares
	}

	return compiled, diags
}

// newEntry constructs one provider from its configuration.
func newEntry(ctx context.Context, cfg *config.Provider, conns *nodes.Conns) (*entry, hcl.Diagnostics) {
	tags, diags := cfg.Tags()
	if diags.HasErrors() {
		return nil, diags
	}

	// Resolved to bytes, so a plugin never learns where its credential came from.
	credentials, err := cfg.Credentials.Resolve(ctx)
	if err != nil {
		return nil, append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Cannot resolve credentials",
			Detail:   fmt.Sprintf("Provider %q: %s.", cfg.Name, err),
		})
	}

	// Backstop: config validation already reported a bad pool against its line.
	limits, err := quota.NewLimits(cfg.PoolSpecs())
	if err != nil {
		return nil, append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid quota pools",
			Detail:   fmt.Sprintf("Provider %q: %s.", cfg.Name, err),
		})
	}

	provider, buildDiags := Build(ctx, cfg.Type, Settings{
		Name:        cfg.Name,
		Config:      cfg.ConfigBody(),
		Credentials: credentials,
		Nodes:       conns,
	})

	diags = append(diags, buildDiags...)

	if provider == nil {
		return nil, diags
	}

	return &entry{
		name:        cfg.Name,
		provider:    provider,
		tags:        tags,
		limits:      limits,
		tier:        cfg.TierOf(),
		enabled:     cfg.IsEnabled(),
		credentials: cfg.Credentials,

		// Healthy until a refresh says otherwise, so all are usable before one.
		healthy: true,
	}, diags
}

// -------------------------------------------------------------------------
// REFRESH
// -------------------------------------------------------------------------

// refreshTimeout bounds one provider's answer during Refresh, so one that
// hangs does not hold the others' snapshots back.
const refreshTimeout = 30 * time.Second

// isLive reports whether a provider's capabilities are read on every plan
// rather than refreshed, so Refresh leaves it alone.
func isLive(e *entry) bool {
	_, ok := e.provider.(plugin.Live)

	return ok
}

// Refresh asks every enabled provider, except live ones, what it can do now.
// Providers are asked in parallel and every failure is returned. One that
// fails is marked unhealthy but keeps its last snapshot, whose ObservedAt lets
// admission judge how stale it is.
func (r *Registry) Refresh(ctx context.Context) error {
	type answer struct {
		caps plugin.Capabilities
		err  error
	}

	answers := make([]answer, len(r.entries))

	var wg sync.WaitGroup

	for i, e := range r.entries {
		if !e.enabled || isLive(e) {
			continue
		}

		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
			defer cancel()

			answers[i].caps, answers[i].err = e.provider.Capabilities(ctx)
		})
	}

	wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()

	var failures []error

	for i, e := range r.entries {
		if !e.enabled || isLive(e) {
			continue
		}

		if err := answers[i].err; err != nil {
			e.healthy = false
			failures = append(failures, fmt.Errorf("provider %q: %w", e.name, err))

			continue
		}

		e.capabilities = answers[i].caps
		e.healthy = true
	}

	return errors.Join(failures...)
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// Inputs returns what admission reads for a job in namespace, one per
// provider in name order. usage reads each provider's total and namespace
// share, so plan and run price from one ledger; nil reads as nothing charged.
func (r *Registry) Inputs(
	namespace string, usage func(namespace, provider string) (total, share quota.PoolUsage),
) []scheduler.Input {
	inputs := make([]scheduler.Input, 0, len(r.entries))

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, e := range r.entries {
		caps, healthy := e.capabilities.Clone(), e.healthy

		// Read a live provider now, so this plan sees a node that joined or filled.
		if live, ok := e.provider.(plugin.Live); ok && e.enabled {
			var err error

			caps, err = live.LiveCapabilities()
			healthy = err == nil
		}

		in := scheduler.Input{
			Provider:     e.name,
			Capabilities: caps,
			Limits:       e.limits,
			Namespace:    namespace,
			Share:        r.namespaces[namespace][e.name],
			Tags:         e.tags,
			Tier:         e.tier,
			Enabled:      e.enabled,
			Healthy:      healthy,
		}

		if usage != nil {
			in.Usage, in.ShareUsage = usage(namespace, e.name)
		}

		inputs = append(inputs, in)
	}

	return inputs
}

// HasNamespace reports whether a job may name namespace. The default always
// exists.
func (r *Registry) HasNamespace(namespace string) bool {
	if namespace == job.DefaultNamespace {
		return true
	}

	_, ok := r.namespaces[namespace]

	return ok
}

// Budgets returns every compiled pool, which is what a ledger is built over.
func (r *Registry) Budgets() quota.Budgets {
	totals := make(map[string]quota.Limits, len(r.entries))
	for _, e := range r.entries {
		totals[e.name] = e.limits
	}

	return quota.Budgets{Totals: totals, Namespaces: r.namespaces}
}

// Provider returns the plugin registered under a name, for the dispatcher to
// submit to; admission reads Inputs instead.
func (r *Registry) Provider(name string) (plugin.Provider, bool) {
	for _, e := range r.entries {
		if e.name == name {
			return e.provider, true
		}
	}

	return nil, false
}

// recredentialEvery is the least time between two resolutions of one
// provider's credential, so a wrong one does not rerun its command every call.
const recredentialEvery = time.Minute

// Recredential resolves the named provider's credential again and hands it
// over, after the platform rejected the one it held. It reports whether a
// retry can succeed: true when a credential is fresh, including one another
// caller resolved within recredentialEvery.
func (r *Registry) Recredential(ctx context.Context, name string) (bool, error) {
	var e *entry

	for _, candidate := range r.entries {
		if candidate.name == name {
			e = candidate
		}
	}

	if e == nil || e.credentials == nil {
		return false, nil
	}

	recredentialer, ok := e.provider.(plugin.Recredentialer)
	if !ok {
		return false, nil
	}

	// One refresh at a time; a caller that waited on another's finds it done.
	e.recredential.Lock()
	defer e.recredential.Unlock()

	if time.Since(e.recredentialed) < recredentialEvery {
		return true, nil
	}

	credential, err := e.credentials.Resolve(ctx)
	if err != nil {
		return false, fmt.Errorf("resolving the credential of %s again: %w", name, err)
	}

	if err := recredentialer.Recredential(ctx, credential); err != nil {
		return false, fmt.Errorf("handing %s its new credential: %w", name, err)
	}

	e.recredentialed = time.Now()

	return true, nil
}

// Names returns every configured provider, enabled or not, in order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		names = append(names, e.name)
	}

	return names
}

// Len reports how many providers are configured.
func (r *Registry) Len() int {
	return len(r.entries)
}
