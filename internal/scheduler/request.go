// -------------------------------------------------------------------------------
// Admission Request
//
// Author: Alex Freidah
//
// What is being placed: one task, plus the routing policy its job declared,
// together because half the admission rules live on each. Everything here is
// derived once, before any provider is considered, since admission fans out
// across every provider.
// -------------------------------------------------------------------------------

package scheduler

import (
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Request is one task and the policy governing where it may run. Image and
// Execution are derived here once, so no checker decodes HCL and admission,
// free_quota_percent and the dispatcher's reservation price the same thing.
// ServerTiers is the deployment's tier mode, empty for the default.
type Request struct {
	Task        *job.Task
	Routing     *job.Routing
	Image       string
	Execution   quota.Execution
	ServerTiers job.TierMode
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// NewRequest derives a request from a task, its job's routing and the server's
// tier mode. ctx evaluates the driver config, which may reference job
// metadata; diagnostics report a bad image against the line the author wrote.
func NewRequest(
	task *job.Task, routing *job.Routing, tiers job.TierMode, ctx *hcl.EvalContext,
) (*Request, hcl.Diagnostics) {
	image, diags := task.Image(ctx)

	return &Request{
		Task:        task,
		Routing:     routing,
		Image:       image,
		Execution:   executionOf(task),
		ServerTiers: tiers,
	}, diags
}

// executionOf reads what the task declared, which is all a charge may rest on
// before anything runs. An unparseable timeout counts as none, since jobspec
// validation reports it.
func executionOf(task *job.Task) quota.Execution {
	var e quota.Execution

	if task.Resources != nil {
		if task.Resources.CPU != nil {
			e.CPU = *task.Resources.CPU
		}

		if task.Resources.Memory != nil {
			e.Memory = *task.Resources.Memory
		}
	}

	if task.Timeout != nil {
		if timeout, err := task.Timeout.Std(); err == nil {
			e.Duration = timeout
		}
	}

	return e
}

// -------------------------------------------------------------------------
// ROUTING POLICY
// -------------------------------------------------------------------------

// Providers returns the allowlist the job declared; an empty one allows every
// provider.
func (r *Request) Providers() []string {
	if r.Routing == nil {
		return nil
	}

	return r.Routing.Providers
}

// MaxCost returns the most the job will pay for one execution: zero when it
// did not say, so paying is a deliberate act.
func (r *Request) MaxCost() job.Cost {
	if r.Routing == nil || r.Routing.MaxCost == nil {
		return 0
	}

	return *r.Routing.MaxCost
}

// WillPay reports whether the job accepts capacity that costs money, which
// makes a spent budget a price for it rather than a rejection.
func (r *Request) WillPay() bool {
	return r.MaxCost() > 0
}

// Strategy returns how the job wants its candidates ordered, free-first when
// it did not say.
func (r *Request) Strategy() job.Strategy {
	if r.Routing == nil || r.Routing.Strategy == nil {
		return job.StrategyFreeFirst
	}

	return *r.Routing.Strategy
}

// Tiers returns how provider tiers order candidates: the job's own mode, else
// the server's, else strict.
func (r *Request) Tiers() job.TierMode {
	switch {
	case r.Routing != nil && r.Routing.Tiers != nil:
		return *r.Routing.Tiers

	case r.ServerTiers != "":
		return r.ServerTiers

	default:
		return job.DefaultTierMode
	}
}

// Constraints returns the job's hard requirements, which may be empty.
func (r *Request) Constraints() []job.Constraint {
	if r.Routing == nil {
		return nil
	}

	return r.Routing.Constraints
}

// Affinities returns the job's soft preferences, which may be empty.
func (r *Request) Affinities() []job.Affinity {
	if r.Routing == nil {
		return nil
	}

	return r.Routing.Affinities
}

// -------------------------------------------------------------------------
// TASK REQUIREMENTS
// -------------------------------------------------------------------------

// Architecture returns the architecture the task requires, and whether it
// required one.
func (r *Request) Architecture() (job.Arch, bool) {
	if r.Task.Execution == nil || r.Task.Execution.Architecture == nil {
		return "", false
	}

	return *r.Task.Execution.Architecture, true
}

// CPU returns the millicores the task asked for, zero if it did not ask.
func (r *Request) CPU() int {
	if r.Task.Resources == nil || r.Task.Resources.CPU == nil {
		return 0
	}

	return *r.Task.Resources.CPU
}

// Memory returns the MiB the task asked for, zero if it did not ask.
func (r *Request) Memory() int {
	if r.Task.Resources == nil || r.Task.Resources.Memory == nil {
		return 0
	}

	return *r.Task.Resources.Memory
}

// NeedsInternet reports whether the task requires egress. Only an explicit
// true is a requirement; absent leaves it to the provider, false denies it.
func (r *Request) NeedsInternet() bool {
	return r.Task.Network != nil &&
		r.Task.Network.Internet != nil &&
		*r.Task.Network.Internet
}

// NeedsPrivateNetwork reports whether the task requires private connectivity.
func (r *Request) NeedsPrivateNetwork() bool {
	return r.Task.Network != nil &&
		r.Task.Network.Private != nil &&
		*r.Task.Network.Private
}
