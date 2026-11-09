// -------------------------------------------------------------------------------
// Admission Inputs and Results
//
// Author: Alex Freidah
//
// Admission answers one question: which providers can currently run this task,
// and for each that cannot, why not. Its inputs are values gathered beforehand
// and its output is normalized, so the scheduler never sees a provider type and
// admission never reaches a network. Each checker owns a single concern and
// names the reason it filtered.
// -------------------------------------------------------------------------------

package scheduler

import (
	"slices"
	"strconv"
	"strings"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// INPUTS
// -------------------------------------------------------------------------

// Input is everything admission knows about one provider. Enabled and Healthy
// are kept apart because a provider an operator turned off and one failing its
// health checks are different rejections with different fixes. Share and
// ShareUsage are the namespace's own slice, zero when it declared none; both
// layers must have room.
type Input struct {
	Provider     string
	Capabilities plugin.Capabilities
	Limits       quota.Limits
	Usage        quota.PoolUsage
	Namespace    string
	Share        quota.Limits
	ShareUsage   quota.PoolUsage
	Tags         map[string]string // the operator's labels, matched as provider.meta.*
	Labels       map[string]string // a pool node's labels, set while that node is judged
	Tier         int               // the operator's preference, lower first
	Enabled      bool
	Healthy      bool
}

// FreePercent is the tightest remaining allowance among the pools e charges,
// across the provider's total and the namespace's share.
func (in *Input) FreePercent(e quota.Execution) int {
	return min(in.Limits.FreePercent(in.Usage, e), in.Share.FreePercent(in.ShareUsage, e))
}

// Attributes returns everything a constraint or affinity can match on for this
// provider, merging the quota-derived ones into the capability snapshot.
// provider.free_quota_percent depends on the task, since a pool the task does
// not charge cannot constrain it.
func (in *Input) Attributes(e quota.Execution) map[string]string {
	attrs := in.Capabilities.Attributes()
	attrs[plugin.AttrFreeQuotaPercent] = strconv.Itoa(in.FreePercent(e))
	attrs[plugin.AttrTier] = strconv.Itoa(in.Tier)

	// Tags sit under their own prefix, so they cannot shadow a capability
	for name, value := range in.Tags {
		attrs[plugin.MetaPrefix+name] = value
	}

	for name, value := range in.Labels {
		attrs[plugin.NodeLabelPrefix+name] = value
	}

	return attrs
}

// -------------------------------------------------------------------------
// OUTPUTS
// -------------------------------------------------------------------------

// Candidate is a provider that can run the task. It embeds the input it was
// admitted from because scoring needs the quota snapshot and tags admission
// already holds. EstimatedCost is the provider's advertised price for one
// execution, zero for every current provider.
type Candidate struct {
	Input
	EstimatedCost job.Cost
	Members       []string // for a pool, the nodes that passed; placement picks among them
}

// Rejection is a provider that cannot run the task, and why: Reason for a
// machine to branch on, Detail with the numbers for a person. Also holds every
// other reason the provider failed, in checker order, so one plan shows every
// fix rather than one per rerun.
type Rejection struct {
	Provider string
	Reason   Reason
	Detail   string
	Also     []Reason
}

// Result is the outcome of admitting one task against every configured
// provider. Both slices are ordered by provider name, so plan output is stable
// and can be diffed in CI.
type Result struct {
	Candidates []Candidate
	Rejections []Rejection
}

// Admitted reports whether any provider can run the task.
func (r *Result) Admitted() bool {
	return len(r.Candidates) > 0
}

// Retryable reports whether a job admitted nowhere might be admitted later
// unchanged: true when at least one rejection was transient, as spent quota
// is and a missing driver is not.
func (r *Result) Retryable() bool {
	if r.Admitted() {
		return false
	}

	for _, rejection := range r.Rejections {
		if rejection.Reason.Transient() {
			return true
		}
	}

	return false
}

// Sort orders candidates and rejections by provider name, so output does not
// depend on the order providers were evaluated in.
func (r *Result) Sort() {
	slices.SortFunc(r.Candidates, func(a, b Candidate) int {
		return strings.Compare(a.Provider, b.Provider)
	})

	slices.SortFunc(r.Rejections, func(a, b Rejection) int {
		return strings.Compare(a.Provider, b.Provider)
	})
}

// -------------------------------------------------------------------------
// CHECKERS
// -------------------------------------------------------------------------

// Checker decides whether one provider can run one request: nil admits, and a
// rejection removes the provider and says why. Each owns one concern and is a
// pure function of its arguments; Admit fills in the rejection's Provider.
type Checker interface {
	Name() string
	Check(req *Request, in *Input) *Rejection
}
