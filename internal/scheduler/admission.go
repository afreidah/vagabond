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
// Capabilities and Quota are snapshots taken before admission runs. Enabled and
// Healthy are the control plane's own view, held separately because a provider
// an operator turned off and one failing its health checks are different
// rejections with different fixes.
type Input struct {
	Provider     string
	Capabilities plugin.Capabilities
	Quota        quota.Snapshot
	Enabled      bool
	Healthy      bool
}

// Attributes returns everything a constraint or affinity can match on for this
// provider.
//
// Capability attributes come from the snapshot; the quota-derived ones are
// merged here, because this is the only place holding both. A job matching on
// provider.free_quota_percent is reading a number no capability model could
// have known.
func (in *Input) Attributes() map[string]string {
	attrs := in.Capabilities.Attributes()
	attrs[plugin.AttrFreeQuotaPercent] = strconv.Itoa(in.Quota.FreePercent)

	return attrs
}

// -------------------------------------------------------------------------
// OUTPUTS
// -------------------------------------------------------------------------

// Candidate is a provider that can run the task.
//
// EstimatedCost is always zero while max_cost_usd = 0 is the only policy
// Vagabond implements. It is carried anyway because adding it later means
// touching every caller and every persisted row, and because a candidate that
// cannot state its price is not one a paid path could ever use.
type Candidate struct {
	Provider      string
	EstimatedCost job.Cost
	Capabilities  plugin.Capabilities
}

// Rejection is a provider that cannot run the task, and why.
//
// Reason is what a machine reads and Detail is what a person reads. Both are
// needed: the code lets a CI system branch without parsing prose, and the
// detail carries the numbers that make the code actionable, as in a duration
// limit the job exceeded by four minutes.
type Rejection struct {
	Provider string
	Reason   Reason
	Detail   string
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

// Checker decides whether one provider can run one task.
//
// Returning nil admits. Returning a rejection removes the provider and says
// why. A checker owns exactly one concern, which is what lets each be table
// tested on its own and what keeps a new rule from being buried inside an
// existing one.
//
// Implementations must be pure. Admission runs across every configured provider
// on a plan, which has to stay fast and free of side effects; a checker that
// reached the network would make that untrue without the signature changing.
type Checker interface {
	Name() string
	Check(task job.Task, in *Input) *Rejection
}
