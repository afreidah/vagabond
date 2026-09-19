// -------------------------------------------------------------------------------
// Admission Input and Result Tests
//
// Author: Alex Freidah
//
// The acceptance test for these types is that the plan table in the README can
// be built from them and nothing else. That is at the bottom of this file, and
// it is what proves the shapes carry enough to be useful.
// -------------------------------------------------------------------------------

package scheduler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

func observedQuota(percent int) quota.Snapshot {
	return quota.Snapshot{
		FreePercent: percent,
		ObservedAt:  time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}
}

// -------------------------------------------------------------------------
// ATTRIBUTES
// -------------------------------------------------------------------------

// The quota-derived attribute is merged here because this is the only layer
// holding both snapshots. A capability model could never know it.
func TestInput_AttributesMergeQuota(t *testing.T) {
	in := &Input{
		Provider:     "ibm-code-engine",
		Capabilities: plugin.FixtureContainer(time.Now()),
		Quota:        observedQuota(72),
	}

	attrs := in.Attributes()

	if got := attrs[plugin.AttrFreeQuotaPercent]; got != "72" {
		t.Errorf("attrs[%q] = %q, want %q", plugin.AttrFreeQuotaPercent, got, "72")
	}

	if _, ok := attrs[plugin.AttrDrivers]; !ok {
		t.Errorf("capability attribute %q was lost in the merge", plugin.AttrDrivers)
	}
}

// The example job matches on this exact name, so it has to stay reserved and
// spelled this way.
func TestAttrFreeQuotaPercent_IsReserved(t *testing.T) {
	if plugin.AttrFreeQuotaPercent != "provider.free_quota_percent" {
		t.Errorf("plugin.AttrFreeQuotaPercent = %q, want provider.free_quota_percent", plugin.AttrFreeQuotaPercent)
	}

	if !plugin.Reserved(plugin.AttrFreeQuotaPercent) {
		t.Error("the quota attribute is not under the reserved prefix")
	}
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

func TestResult_Admitted(t *testing.T) {
	empty := &Result{}
	if empty.Admitted() {
		t.Error("an empty result reports as admitted")
	}

	withCandidate := &Result{Candidates: []Candidate{{Provider: "ibm-code-engine"}}}
	if !withCandidate.Admitted() {
		t.Error("a result with a candidate does not report as admitted")
	}
}

// Retryable is the difference between waiting for a quota reset and looping
// forever against a provider that will never support the driver.
func TestResult_Retryable(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		want   bool
	}{
		{
			name: "admitted somewhere is not a retry",
			result: Result{
				Candidates: []Candidate{{Provider: "ibm-code-engine"}},
				Rejections: []Rejection{{Provider: "aws-lambda", Reason: ReasonQuotaExhausted}},
			},
			want: false,
		},
		{
			name: "rejected everywhere on quota",
			result: Result{
				Rejections: []Rejection{
					{Provider: "gcp-cloud-run", Reason: ReasonQuotaExhausted},
					{Provider: "ibm-code-engine", Reason: ReasonQuotaExhausted},
				},
			},
			want: true,
		},
		{
			name: "rejected everywhere on driver support",
			result: Result{
				Rejections: []Rejection{
					{Provider: "aws-lambda", Reason: ReasonDriverUnsupported},
					{Provider: "cloudflare-workers", Reason: ReasonDriverUnsupported},
				},
			},
			want: false,
		},
		{
			name: "one transient rejection among permanent ones",
			result: Result{
				Rejections: []Rejection{
					{Provider: "aws-lambda", Reason: ReasonDriverUnsupported},
					{Provider: "ibm-code-engine", Reason: ReasonUnhealthy},
				},
			},
			want: true,
		},
		{
			name:   "nothing configured at all",
			result: Result{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Retryable(); got != tt.want {
				t.Errorf("Retryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Plan output has to be diffable in CI, which means it cannot depend on the
// order providers happened to be evaluated in.
func TestResult_SortIsDeterministic(t *testing.T) {
	r := Result{
		Candidates: []Candidate{
			{Provider: "ibm-code-engine"},
			{Provider: "gcp-cloud-run"},
		},
		Rejections: []Rejection{
			{Provider: "cloudflare-workers", Reason: ReasonDriverUnsupported},
			{Provider: "aws-lambda", Reason: ReasonDriverUnsupported},
		},
	}

	r.Sort()

	if r.Candidates[0].Provider != "gcp-cloud-run" {
		t.Errorf("candidates[0] = %q, want gcp-cloud-run", r.Candidates[0].Provider)
	}

	if r.Rejections[0].Provider != "aws-lambda" {
		t.Errorf("rejections[0] = %q, want aws-lambda", r.Rejections[0].Provider)
	}
}

// -------------------------------------------------------------------------
// CANDIDATE
// -------------------------------------------------------------------------

// Every candidate is free while max_cost_usd = 0 is the only policy, and the
// zero cost must read as free without interpretation.
func TestCandidate_CostIsFreeByDefault(t *testing.T) {
	var c Candidate

	if !c.EstimatedCost.Free() {
		t.Error("a zero-value candidate does not report as free")
	}
}

// -------------------------------------------------------------------------
// ACCEPTANCE
// -------------------------------------------------------------------------

// The plan table in the README has to be renderable from these types alone,
// with no further lookups. This builds it.
func TestResult_RendersThePlanTable(t *testing.T) {
	now := time.Now()

	result := Result{
		Candidates: []Candidate{
			{Provider: "ibm-code-engine", Capabilities: plugin.FixtureContainer(now)},
			{Provider: "gcp-cloud-run", Capabilities: plugin.FixtureContainer(now)},
		},
		Rejections: []Rejection{
			{Provider: "aws-lambda", Reason: ReasonDriverUnsupported, Detail: "driver container unsupported"},
			{Provider: "cloudflare-workers", Reason: ReasonDriverUnsupported, Detail: "driver container unsupported"},
		},
	}
	result.Sort()

	var b strings.Builder
	for _, c := range result.Candidates {
		fmt.Fprintf(&b, "%-22s admitted\n", c.Provider)
	}

	for _, r := range result.Rejections {
		fmt.Fprintf(&b, "%-22s rejected       %s\n", r.Provider, r.Detail)
	}

	rendered := b.String()

	for _, want := range []string{
		"ibm-code-engine",
		"gcp-cloud-run",
		"aws-lambda",
		"driver container unsupported",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered plan is missing %q:\n%s", want, rendered)
		}
	}

	if !result.Admitted() {
		t.Error("the example plan reports nothing admitted")
	}
}
