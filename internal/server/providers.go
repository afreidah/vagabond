// -------------------------------------------------------------------------------
// Provider Routes
//
// Author: Alex Freidah
//
// GET /v1/providers shows how every configured provider stands and how much of
// each budget it has used. It reads what admission already holds, so no
// provider is contacted and usage is as old as the ledger's last snapshot.
//
// POST /v1/provider/{name}/disable and /enable turn a provider off and on for
// every server sharing the store. The server that took the request applies the
// change before answering, and the others read it within disabledInterval.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/scheduler"
	"github.com/afreidah/vagabond/internal/state"
)

// -------------------------------------------------------------------------
// LISTING
// -------------------------------------------------------------------------

// listProviders answers with every configured provider and its budgets. These
// are provider totals, so any namespace reads the same.
func (s *Server) listProviders(_ http.ResponseWriter, _ *http.Request) (any, error) {
	inputs := s.registry.Inputs(job.DefaultNamespace, s.ledger.PoolUsage)

	out := api.ProviderList{
		Refreshed: s.ledger.Refreshed().UTC(),
		Providers: make([]api.ProviderListStub, 0, len(inputs)),
	}

	for i := range inputs {
		out.Providers = append(out.Providers, providerStub(&inputs[i]))
	}

	return out, nil
}

// providerStub translates one provider's admission input for the wire, each
// pool converted from base units to the unit its meter is written in.
func providerStub(in *scheduler.Input) api.ProviderListStub {
	pools := in.Limits.Pools()

	out := api.ProviderListStub{
		Name:    in.Provider,
		Tier:    in.Tier,
		Enabled: in.Enabled && in.Disabled == nil,
		Healthy: in.Healthy,
		Pools:   make([]api.ProviderPool, 0, len(pools)),
	}

	if in.Disabled != nil {
		out.Disabled = &api.ProviderDisabled{At: in.Disabled.At.UTC(), By: in.Disabled.By}
	}

	for i := range pools {
		p := &pools[i]
		used := in.Usage[p.Name]

		out.Pools = append(out.Pools, api.ProviderPool{
			Name:        p.Name,
			Meter:       string(p.Meter),
			Unit:        p.Meter.Unit(),
			Period:      string(p.Period),
			Used:        p.Meter.Natural(used),
			Limit:       p.Meter.Natural(p.Limit),
			FreePercent: p.FreePercent(used),
		})
	}

	return out
}

// -------------------------------------------------------------------------
// TURNING OFF AND ON
// -------------------------------------------------------------------------

// disableProvider stops new work going to a provider and answers with it as
// it now stands. Executions already running on it are left to finish. A
// provider already disabled keeps the time and name of its first disable.
func (s *Server) disableProvider(_ http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")

	if _, err := s.provider(name); err != nil {
		return nil, err
	}

	d := state.Disabled{At: s.now().UTC(), By: caller(r)}
	if err := s.providers.DisableProvider(r.Context(), name, d); err != nil {
		return nil, err
	}

	s.logger.InfoContext(r.Context(), "provider disabled", "provider", name, "by", d.By)

	return s.switched(r.Context(), name)
}

// enableProvider lets new work go to a provider an operator disabled, and
// answers with it as it now stands. A provider the configuration file turns
// off is refused, since only the file can turn it on.
func (s *Server) enableProvider(_ http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")

	in, err := s.provider(name)
	if err != nil {
		return nil, err
	}

	if !in.Enabled {
		return nil, refuse(http.StatusConflict, fmt.Errorf(
			"provider %q is turned off in the configuration file; set enabled = true there instead", name))
	}

	if err := s.providers.EnableProvider(r.Context(), name); err != nil {
		return nil, err
	}

	s.logger.InfoContext(r.Context(), "provider enabled", "provider", name, "by", caller(r))

	return s.switched(r.Context(), name)
}

// switched reads the disabled providers again, so this server applies a
// change before answering, and answers with the named provider as it now
// stands.
func (s *Server) switched(ctx context.Context, name string) (any, error) {
	if err := s.refreshDisabled(ctx); err != nil {
		return nil, err
	}

	in, err := s.provider(name)
	if err != nil {
		return nil, err
	}

	return providerStub(in), nil
}

// provider returns the named provider's admission input, with its usage as
// the list shows it, or a 404 when no provider has that name.
func (s *Server) provider(name string) (*scheduler.Input, error) {
	inputs := s.registry.Inputs(job.DefaultNamespace, s.ledger.PoolUsage)

	for i := range inputs {
		if inputs[i].Provider == name {
			return &inputs[i], nil
		}
	}

	return nil, refuse(http.StatusNotFound, fmt.Errorf("no provider is named %q", name))
}

// refreshDisabled reads which providers operators turned off through the API
// and hands them to the registry. A failed read keeps the last set.
func (s *Server) refreshDisabled(ctx context.Context) error {
	disabled, err := s.providers.DisabledProviders(ctx)
	if err != nil {
		return fmt.Errorf("reading disabled providers: %w", err)
	}

	s.registry.SetDisabled(disabled)

	return nil
}
