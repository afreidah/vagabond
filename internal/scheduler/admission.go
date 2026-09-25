// -------------------------------------------------------------------------------
// Admission Inputs and Results
//
// Author: Alex Freidah
//
// Admission answers one question: which providers can currently run this task,
// and for each that cannot, why not. Its inputs are values gathered beforehand
// and its output is normalized, so the scheduler never sees a provider type and
// admission never reaches a network.
//
// Checkers follow the shape Nomad uses for feasibility: many small ones, each
// owning a single concern and each naming the reason it filtered. One large
// function would test as one unit and read as none.
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

// Input is everything admission knows about one provider.
//
// Capabilities is a snapshot taken before admission runs. Enabled and Healthy
// are the control plane's own view, held separately because a provider an
// operator turned off and one failing its health checks are different
// rejections with different fixes.
// Tags are the operator's own labels, reaching a job as provider.meta.*
// attributes. Vagabond never interprets them.
//
// Limits are the budgets an operator declared and Usage is what the ledger has
// charged against them. Both are needed because neither means anything alone.
//
// Share and ShareUsage are the job's namespace's own slice of this provider,
// zero when the namespace declared none. Both layers must have room.
type Input struct {
	Provider     string
	Capabilities plugin.Capabilities
	Limits       quota.Limits
	Usage        quota.PoolUsage
	Namespace    string
	Share        quota.Limits
	ShareUsage   quota.PoolUsage
	Tags         map[string]string
	Enabled      bool
	Healthy      bool
}

// FreePercent is the tightest remaining allowance among the pools e charges,
// across the provider's total and the namespace's share.
func (in *Input) FreePercent(e quota.Execution) int {
	return min(in.Limits.FreePercent(in.Usage, e), in.Share.FreePercent(in.ShareUsage, e))
}

// Attributes returns everything a constraint or affinity can match on for this
// provider.
//
// Capability attributes come from the snapshot; the quota-derived ones are
// merged here, because this is the only place holding both.
//
// provider.free_quota_percent depends on the task. A pool the task does not
// charge cannot constrain it, so the same provider reports a different number
// for two jobs that meter differently.
func (in *Input) Attributes(e quota.Execution) map[string]string {
	attrs := in.Capabilities.Attributes()
	attrs[plugin.AttrFreeQuotaPercent] = strconv.Itoa(in.FreePercent(e))

	// An operator's tags cannot shadow a capability, because they land under a
	// prefix nothing else writes to. That is what makes the closed half of the
	// namespace checkable while this half stays open.
	for name, value := range in.Tags {
		attrs[plugin.MetaPrefix+name] = value
	}

	return attrs
}

// -------------------------------------------------------------------------
// OUTPUTS
// -------------------------------------------------------------------------

// Candidate is a provider that can run the task.
//
// It embeds the input it was admitted from rather than copying fields out of
// it, because scoring needs the quota snapshot and the operator's tags that
// admission was already holding. Matching a candidate back to its input by name
// afterwards would be the same data with a lookup in front of it.
//
// EstimatedCost is always zero while max_cost_usd = 0 is the only policy
// Vagabond implements. It is stated anyway, because a candidate that cannot say
// its price is not one a paid path could ever use.
type Candidate struct {
	Input
	EstimatedCost job.Cost
}

// Rejection is a provider that cannot run the task, and why.
//
// Reason is what a machine reads and Detail is what a person reads. Both are
// needed: the code lets a CI system branch without parsing prose, and the
// detail carries the numbers that make the code actionable, as in a duration
// limit the job exceeded by four minutes.
//
// Also holds every other reason the provider failed, in the order the checkers
// ran. The plan table shows one line per provider so only Reason is rendered,
// but a task that fails five checks against a provider takes five edit-and-rerun
// cycles to discover that if the other four are thrown away. Nomad reports
// aggregate counts instead and has no equivalent, which is the right call at
// five thousand anonymous nodes and the wrong one at five named providers.
type Rejection struct {
	Provider string
	Reason   Reason
	Detail   string
	Also     []Reason
}

// Result is the outcome of admitting one task against every configured
// provider.
//
// Both slices are ordered by provider name, so plan output is stable for the
// same inputs and can be diffed in CI. That is a property of how this value is
// built rather than of whatever renders it.
type Result struct {
	Candidates []Candidate
	Rejections []Rejection
}

// Admitted reports whether any provider can run the task.
func (r *Result) Admitted() bool {
	return len(r.Candidates) > 0
}

// Retryable reports whether a job that was admitted nowhere might be admitted
// later without changing.
//
// True when at least one rejection was transient. A caller holding a job that
// every provider refused on quota should wait for a period reset; one refused
// everywhere on driver support should not, and telling them apart is the
// difference between patience and a loop.
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

// Sort orders candidates and rejections by provider name.
//
// Called once after admission has collected every verdict, so that output is
// deterministic regardless of the order providers were evaluated in.
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

// Checker decides whether one provider can run one request.
//
// Returning nil admits. Returning a rejection removes the provider and says
// why. A checker owns exactly one concern, which is what lets each be table
// tested on its own and what keeps a new rule from being buried inside an
// existing one.
//
// Provider is left empty on the returned rejection and filled in by Admit,
// because a checker is handed the input it is judging and should not have to
// copy a field out of it correctly twelve times.
//
// Implementations are pure functions of their two arguments. Nomad's equivalent
// returns a bare bool and reports its reason by writing to a shared metrics
// sink; returning the rejection as a value instead means a checker can be
// tested without constructing a context, and means nothing in admission holds
// state that the order of evaluation could disturb.
type Checker interface {
	Name() string
	Check(req *Request, in *Input) *Rejection
}
