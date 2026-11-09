// -------------------------------------------------------------------------------
// Policy Checks
//
// Author: Alex Freidah
//
// The rejections a job asked for. These run first, ahead of every capability
// comparison, because a provider the author excluded is excluded whether or not
// it could have run the work. They are also the cheapest checks in the set.
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

// Check rejects a provider absent from a non-empty routing list. An empty list
// admits everything.
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

// Check rejects a provider whose price exceeds the job's ceiling, which is zero
// unless the job raised it. Every provider publishes zero today, so this
// catches the first that does not before it bills.
func (costChecker) Check(req *Request, in *Input) *Rejection {
	cost := in.Capabilities.EstimatedCost

	max := req.MaxCost()
	if cost <= max {
		return nil
	}

	return reject(ReasonCostPolicy, fmt.Sprintf(
		"This provider charges %d per execution and the job allows %d.", cost, max))
}
