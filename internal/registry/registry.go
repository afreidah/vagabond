// -------------------------------------------------------------------------------
// Provider Registry
//
// Author: Alex Freidah
//
// Holds every configured provider alongside what was last observed about it,
// and produces the inputs admission reads. Admission is a pure function over
// snapshots, so the network calls that gather them happen here, once, rather
// than on the path a plan takes.
//
// A disabled provider is kept rather than dropped. A plan that silently omits a
// provider cannot explain why it was not considered, and "why did this not go to
// IBM" is the question the plan exists to answer.
// -------------------------------------------------------------------------------

package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrUnknownProviderType reports configuration naming a plugin that does not
// exist.
var ErrUnknownProviderType = errors.New("unknown provider type")

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Registry is every provider a deployment can dispatch to.
type Registry struct {
	entries []*entry
}

// entry is one provider and everything last known about it.
//
// Capabilities and quota are held beside the plugin rather than fetched on
// demand, because admission must not call anything. Healthy is separate from
// enabled because a provider an operator turned off and one failing its checks
// are different rejections with different fixes.
type entry struct {
	name     string
	provider plugin.Provider
	tags     map[string]string
	enabled  bool
	healthy  bool

	capabilities plugin.Capabilities
	quota        quota.Snapshot
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// New builds a registry from configuration.
//
// Providers are constructed but not contacted. Refresh is what gathers
// capabilities, so that a caller decides when to pay for it and a registry can
// be built in a test without anything reaching outward.
func New(cfg *config.File) (*Registry, error) {
	if cfg == nil {
		return &Registry{}, nil
	}

	r := &Registry{entries: make([]*entry, 0, len(cfg.Providers))}

	for i := range cfg.Providers {
		e, err := newEntry(&cfg.Providers[i])
		if err != nil {
			return nil, err
		}

		r.entries = append(r.entries, e)
	}

	// Sorted once here rather than at every read, so that a plan lists
	// providers the same way whatever order they were configured in.
	slices.SortFunc(r.entries, func(a, b *entry) int {
		return strings.Compare(a.name, b.name)
	})

	return r, nil
}

// newEntry constructs one provider from its configuration.
func newEntry(cfg *config.Provider) (*entry, error) {
	provider, err := Build(cfg.Type, cfg.Name)
	if err != nil {
		return nil, err
	}

	tags, diags := cfg.Tags()
	if diags.HasErrors() {
		return nil, fmt.Errorf("provider %q tags: %s", cfg.Name, diags.Error())
	}

	return &entry{
		name:     cfg.Name,
		provider: provider,
		tags:     tags,
		enabled:  cfg.IsEnabled(),

		// Healthy until something says otherwise. Health checking arrives with
		// the refresh loop that can observe a provider failing; assuming the
		// worst before then would make every provider unusable.
		healthy: true,

		quota: configuredQuota(cfg),
	}, nil
}

// configuredQuota reads the stand-in an operator stated.
//
// ObservedAt is set because an unobserved snapshot has no headroom by design,
// and a configured value is an observation: an operator said so.
func configuredQuota(cfg *config.Provider) quota.Snapshot {
	snapshot := quota.Snapshot{
		Provider:   cfg.Name,
		ObservedAt: time.Now(),
	}

	if cfg.Quota == nil {
		return snapshot
	}

	if cfg.Quota.FreePercent != nil {
		snapshot.FreePercent = *cfg.Quota.FreePercent
	}

	if cfg.Quota.Exhausted != nil {
		snapshot.Exhausted = *cfg.Quota.Exhausted
	}

	return snapshot
}

// -------------------------------------------------------------------------
// REFRESH
// -------------------------------------------------------------------------

// Refresh asks every enabled provider what it can currently do.
//
// A provider that fails to answer is marked unhealthy and keeps whatever was
// last known about it, rather than being dropped. Losing a provider because one
// call failed would turn a transient outage into a job that cannot be placed,
// and the stale snapshot carries ObservedAt so admission can decide for itself
// how old is too old.
//
// Errors are collected rather than returned on the first one: a refresh that
// stops at the first unreachable provider leaves the rest stale for no reason.
func (r *Registry) Refresh(ctx context.Context) error {
	var failures []error

	for _, e := range r.entries {
		if !e.enabled {
			continue
		}

		caps, err := e.provider.Capabilities(ctx)
		if err != nil {
			e.healthy = false
			failures = append(failures, fmt.Errorf("provider %q: %w", e.name, err))

			continue
		}

		e.capabilities = caps
		e.healthy = true
	}

	return errors.Join(failures...)
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// Inputs returns what admission reads, one per configured provider.
//
// Ordered by provider name, so a plan built from them is deterministic without
// the caller having to sort.
func (r *Registry) Inputs() []scheduler.Input {
	inputs := make([]scheduler.Input, 0, len(r.entries))

	for _, e := range r.entries {
		inputs = append(inputs, scheduler.Input{
			Provider:     e.name,
			Capabilities: e.capabilities.Clone(),
			Quota:        e.quota,
			Tags:         e.tags,
			Enabled:      e.enabled,
			Healthy:      e.healthy,
		})
	}

	return inputs
}

// Provider returns the plugin registered under a name.
//
// The dispatcher needs this to submit; admission never does, and reads Inputs
// instead.
func (r *Registry) Provider(name string) (plugin.Provider, bool) {
	for _, e := range r.entries {
		if e.name == name {
			return e.provider, true
		}
	}

	return nil, false
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
