// -------------------------------------------------------------------------------
// Admission Request
//
// Author: Alex Freidah
//
// What is being placed: one task, plus the routing policy its job declared.
// They arrive together because half the admission rules live on each, and
// splitting them would leave the allowlist and the cost ceiling applied
// somewhere outside the checker set, with their own reasons and their own
// ordering.
//
// Everything here is derived once, before any provider is considered. Nomad
// does the same with TaskGroupConstraints, for the same reason: admission fans
// out across every provider, and a checker that parsed HCL on each one would
// be doing the same work five times to reach the same answer.
// -------------------------------------------------------------------------------

package scheduler

import (
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Request is one task and the policy governing where it may run.
//
// Routing is a pointer because a job that states no preference is admissible
// everywhere, which is not the same as one declaring an empty provider list.
//
// Image is derived from the task's driver config rather than read from it,
// because the config block's shape belongs to the driver and a checker must
// not decode HCL. It is empty when the task names no image.
type Request struct {
	Task    *job.Task
	Routing *job.Routing
	Image   string
}

// NewRequest derives a request from a task and its job's routing.
//
// ctx evaluates the driver config, which is left undecoded at parse time and
// may still reference job metadata. Diagnostics rather than an error, so a bad
// image expression is reported against the line the author wrote.
func NewRequest(task *job.Task, routing *job.Routing, ctx *hcl.EvalContext) (*Request, hcl.Diagnostics) {
	image, diags := task.Image(ctx)

	return &Request{
		Task:    task,
		Routing: routing,
		Image:   image,
	}, diags
}

// -------------------------------------------------------------------------
// ROUTING POLICY
// -------------------------------------------------------------------------

// Providers returns the allowlist the job declared, which may be empty.
//
// An empty list means every provider is allowed. A job that named none did not
// forbid all of them; it declined to have an opinion.
func (r *Request) Providers() []string {
	if r.Routing == nil {
		return nil
	}

	return r.Routing.Providers
}

// MaxCost returns the most the job will pay for one execution.
//
// Zero when the job did not say. Vagabond brokers free capacity, so an author
// who wrote no ceiling gets the one the tool exists to enforce rather than an
// unbounded one. Paying is the deliberate act, the same way disabling a
// provider is.
func (r *Request) MaxCost() job.Cost {
	if r.Routing == nil || r.Routing.MaxCost == nil {
		return 0
	}

	return *r.Routing.MaxCost
}

// WillPay reports whether the job accepts capacity that costs money.
//
// This is what makes an exhausted free tier a rejection for most jobs and
// merely a price for the rest. Without it, a job that explicitly budgeted for
// paid capacity would still be refused the moment a free tier ran out, which
// reads as a bug to everyone except the person who wrote the check.
func (r *Request) WillPay() bool {
	return r.MaxCost() > 0
}

// Strategy returns how the job wants its candidates ordered.
//
// Free-first when the job did not say, which is the only strategy Vagabond
// implements and the one the project exists for. A job that stated nothing gets
// the behaviour it would have chosen.
func (r *Request) Strategy() job.Strategy {
	if r.Routing == nil || r.Routing.Strategy == nil {
		return job.StrategyFreeFirst
	}

	return *r.Routing.Strategy
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

// CPU returns the MHz the task asked for, zero if it did not ask.
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

// NeedsInternet reports whether the task requires egress.
//
// An absent internet leaves the decision to the provider, so only an explicit
// true is a requirement. An explicit false denies egress, which no provider is
// rejected for being unable to offer.
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
