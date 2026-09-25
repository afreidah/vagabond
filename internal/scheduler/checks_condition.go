// -------------------------------------------------------------------------------
// Condition Checks
//
// Author: Alex Freidah
//
// The rejections that may not hold tomorrow. A provider filtered here could run
// this exact job later, once an operator turns it back on, once it answers a
// refresh, or once its free tier resets, which is what Reason.Transient reports
// and what tells a caller to wait rather than to edit.
//
// They do not all run at the same point, and the split is deliberate. Enabled
// and healthy run before every capability comparison, because a provider nobody
// refreshed has an empty fingerprint and the comparisons would be made against
// it: a disabled provider rejected for offering no drivers is describing its
// blank snapshot, not itself. Quota runs last, so that a provider which will
// never run this driver says so rather than reporting its allowance.
//
// Nomad drops ineligible nodes at the source and never filters on them at all,
// because at five thousand nodes a per-node reason is noise it reports as a
// count instead. At five named providers, "why did nothing go to IBM" is the
// question a plan exists to answer, so ours stay in and are rejected by name.
// -------------------------------------------------------------------------------

package scheduler

import (
	"fmt"

	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// OPERATOR STATE
// -------------------------------------------------------------------------

// enabledChecker removes providers an operator turned off.
type enabledChecker struct{}

// Name identifies the checker in traces and test failures.
func (enabledChecker) Name() string { return "enabled" }

// Check rejects a provider disabled in configuration.
func (enabledChecker) Check(_ *Request, in *Input) *Rejection {
	if in.Enabled {
		return nil
	}

	return reject(ReasonProviderDisabled,
		"An operator disabled this provider in configuration.")
}

// healthyChecker removes providers that are not answering.
type healthyChecker struct{}

// Name identifies the checker in traces and test failures.
func (healthyChecker) Name() string { return "healthy" }

// Check rejects a provider that failed its last refresh.
//
// Separate from enabled because the two are different rejections with different
// fixes: one is an operator's decision and the other is an outage, and a plan
// that conflated them would send someone to edit a config file over a network
// partition.
func (healthyChecker) Check(_ *Request, in *Input) *Rejection {
	if in.Healthy {
		return nil
	}

	return reject(ReasonUnhealthy,
		"This provider did not answer its last capability refresh.")
}

// -------------------------------------------------------------------------
// FREE-TIER STANDING
// -------------------------------------------------------------------------

// quotaChecker removes providers whose declared budgets have no room for this
// task, for jobs that will not pay for what comes after them.
type quotaChecker struct{}

// Name identifies the checker in traces and test failures.
func (quotaChecker) Name() string { return "quota" }

// Check rejects a provider with no room unless the job budgeted for paid
// capacity.
//
// The willingness to pay is what makes this a policy question rather than a
// fact. An exhausted budget is not a provider being unable to run the work; it
// is the work costing money from here on, and only a job that said it would not
// pay is refused for that. Without the distinction a job that explicitly
// budgeted would still be turned away the moment an allowance ran out.
//
// Asks whether this task fits rather than whether the provider is generally
// spent. A pool with room for a small task and not a large one gives different
// answers to each, and a percentage could not express either.
//
// Last in the set, mirroring Nomad's note that its quota iterator must be the
// final feasibility step so that usage never counts nodes already ineligible.
func (quotaChecker) Check(req *Request, in *Input) *Rejection {
	if req.WillPay() {
		return nil
	}

	if r := exhausted("Pool", in.Limits, in.Usage, req.Execution); r != nil {
		return r
	}

	return exhausted(fmt.Sprintf("Namespace %q's pool", in.Namespace), in.Share, in.ShareUsage, req.Execution)
}

// exhausted rejects when e does not fit one layer of pools. owner opens the
// message, naming whose pool it is.
func exhausted(owner string, limits quota.Limits, usage quota.PoolUsage, e quota.Execution) *Rejection {
	pool := limits.Exceeded(usage, e)
	if pool == nil {
		return nil
	}

	return reject(ReasonQuotaExhausted, fmt.Sprintf(
		"%s %q has %g of %g %s left and this task needs %g, and the job will "+
			"not pay for capacity beyond it.",
		owner,
		pool.Name,
		pool.Meter.Natural(pool.Remaining(usage[pool.Name])),
		pool.Meter.Natural(pool.Limit),
		pool.Meter.Unit(),
		pool.Meter.Natural(limits.Deltas(e)[pool.Name]),
	))
}
