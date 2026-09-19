// -------------------------------------------------------------------------------
// Policy Checks
//
// Author: Alex Freidah
//
// The rejections a job asked for. These run first, ahead of every capability
// comparison, because a provider the author excluded is excluded whether or not
// it could have run the work: telling them the driver is unsupported invites a
// fix to a job that was never trying to go there.
//
// They are also the cheapest checks in the set, which is the same order Nomad
// assembles its stack in.
// -------------------------------------------------------------------------------

package scheduler

import (
	"fmt"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// ALLOWLIST
// -------------------------------------------------------------------------

// allowlistChecker removes providers a job's routing did not name.
type allowlistChecker struct{}

// Name identifies the checker in traces and test failures.
func (allowlistChecker) Name() string { return "allowlist" }

// Check rejects a provider absent from a non-empty routing list.
//
// An empty list admits everything. A job that named no providers declined to
// have an opinion, which is not the same as forbidding all of them.
func (allowlistChecker) Check(req *Request, in *Input) *Rejection {
	allowed := req.Providers()
	if len(allowed) == 0 || slices.Contains(allowed, in.Provider) {
		return nil
	}

	return reject(ReasonNotAllowlisted, fmt.Sprintf(
		"The job routes only to %s.", strings.Join(allowed, ", ")))
}

// -------------------------------------------------------------------------
// COST
// -------------------------------------------------------------------------

// costChecker removes providers that charge more than the job will pay.
type costChecker struct{}

// Name identifies the checker in traces and test failures.
func (costChecker) Name() string { return "cost" }

// Check rejects a provider whose price exceeds the job's ceiling.
//
// The ceiling is zero unless the job raised it, so the default is the promise
// this project exists to keep. Every provider currently publishes a price of
// zero, which means this check passes universally today and is here so that the
// first one that does not is caught by a comparison rather than by a bill.
func (costChecker) Check(req *Request, in *Input) *Rejection {
	cost := in.Capabilities.EstimatedCost

	max := req.MaxCost()
	if cost <= max {
		return nil
	}

	return reject(ReasonCostPolicy, fmt.Sprintf(
		"This provider charges %d per execution and the job allows %d.", cost, max))
}
