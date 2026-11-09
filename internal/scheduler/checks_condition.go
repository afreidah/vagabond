// -------------------------------------------------------------------------------
// Condition Checks
//
// Author: Alex Freidah
//
// The rejections that may not hold tomorrow: a provider filtered here could run
// this job once it is turned back on, answers a refresh, or has its quota
// reset, which Reason.Transient reports. Enabled and healthy run before every
// capability comparison, since an unrefreshed provider has an empty snapshot;
// quota runs last.
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

// Check rejects a provider that failed its last refresh. Separate from enabled
// because an outage and an operator's decision have different fixes.
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

// Check rejects a provider with no room for this task unless the job budgeted
// for paid capacity, since a spent budget means the work costs money from
// here on rather than that the provider cannot run it.
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
