// -------------------------------------------------------------------------------
// Provider Status
//
// Author: Alex Freidah
//
// GET /v1/providers: how every configured provider stands and how much of each
// budget it has used. It reads what admission already holds, so no provider is
// contacted and usage is as old as the ledger's last snapshot.
// -------------------------------------------------------------------------------

package server

import (
	"net/http"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/scheduler"
)

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
		Enabled: in.Enabled,
		Healthy: in.Healthy,
		Pools:   make([]api.ProviderPool, 0, len(pools)),
	}

	for i := range pools {
		p := &pools[i]
		used := in.Usage[p.Name]

		out.Pools = append(out.Pools, api.ProviderPool{
			Name:        p.Name,
			Meter:       string(p.Meter),
			Period:      string(p.Period),
			Used:        p.Meter.Natural(used),
			Limit:       p.Meter.Natural(p.Limit),
			FreePercent: p.FreePercent(used),
		})
	}

	return out
}
