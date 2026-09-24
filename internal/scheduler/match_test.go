// -------------------------------------------------------------------------------
// Attribute Matching Tests
//
// Author: Alex Freidah
//
// The cases worth covering are the ones where an operator meets a value it was
// not obviously written for: equality against a set, an ordering comparison
// against text, and an attribute the provider never published. Each of those
// has a defensible answer and a tempting wrong one.
// -------------------------------------------------------------------------------

package scheduler

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
)

// providerAttrs is what the container fixture publishes, with quota merged in,
// which is what a constraint actually sees.
func providerAttrs(t *testing.T) map[string]string {
	t.Helper()

	in := &Input{
		Provider:     "fake-container",
		Capabilities: plugin.FixtureContainer(time.Now()),
	}
	setFreePercent(in, 72)

	return in.Attributes(quota.Execution{})
}

func constrain(attribute string, op job.Operator, value string) *job.Constraint {
	return &job.Constraint{Attribute: attribute, Operator: op, Value: value}
}

// -------------------------------------------------------------------------
// EQUALITY AND SETS
// -------------------------------------------------------------------------

// The distinction the operator vocabulary exists for. The container fixture
// offers amd64 and arm64, so equality asks whether amd64 is the only one it
// offers, which it is not.
func TestMatch_EqualityAgainstASet(t *testing.T) {
	attrs := providerAttrs(t)

	if MatchConstraint(attrs, constrain(plugin.AttrArchitecture, job.OperatorEqual, "amd64")) {
		t.Error("equality matched a set containing the value, rather than equalling it")
	}

	if !MatchConstraint(attrs, constrain(plugin.AttrArchitecture, job.OperatorSetContains, "amd64")) {
		t.Error("set_contains did not find amd64 in amd64,arm64")
	}
}

func TestMatch_SetContains(t *testing.T) {
	attrs := map[string]string{"provider.architecture": "amd64, arm64 ,riscv64"}

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "first member", value: "amd64", want: true},
		{name: "member with surrounding space", value: "arm64", want: true},
		{name: "last member", value: "riscv64", want: true},
		{name: "absent", value: "s390x", want: false},
		{name: "the whole set is not a member", value: "amd64, arm64 ,riscv64", want: false},
		{name: "a prefix is not a member", value: "amd", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchConstraint(attrs,
				constrain("provider.architecture", job.OperatorSetContains, tt.value))

			if got != tt.want {
				t.Errorf("set_contains %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestMatch_Equality(t *testing.T) {
	attrs := map[string]string{"provider.internet": "true"}

	if !MatchConstraint(attrs, constrain("provider.internet", job.OperatorEqual, "true")) {
		t.Error("equality did not match an identical value")
	}

	if MatchConstraint(attrs, constrain("provider.internet", job.OperatorEqual, "false")) {
		t.Error("equality matched a different value")
	}
}

// -------------------------------------------------------------------------
// ABSENT ATTRIBUTES
// -------------------------------------------------------------------------

// An attribute the provider never published is not equal to anything, so !=
// holds. Nomad draws the same line, deliberately omitting the found check for
// inequality, and it is easy to get backwards.
func TestMatch_AbsentAttribute(t *testing.T) {
	attrs := map[string]string{}

	tests := []struct {
		name string
		op   job.Operator
		want bool
	}{
		{name: "equality fails", op: job.OperatorEqual, want: false},
		{name: "inequality holds", op: job.OperatorNotEqual, want: true},
		{name: "ordering fails", op: job.OperatorGreater, want: false},
		{name: "set membership fails", op: job.OperatorSetContains, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchConstraint(attrs, constrain("provider.absent", tt.op, "anything"))

			if got != tt.want {
				t.Errorf("%s against an absent attribute = %v, want %v", tt.op, got, tt.want)
			}
		})
	}
}

func TestMatch_PresenceOperators(t *testing.T) {
	attrs := map[string]string{"provider.internet": "true"}

	if !MatchConstraint(attrs, constrain("provider.internet", job.OperatorIsSet, "")) {
		t.Error("is_set did not match a published attribute")
	}

	if MatchConstraint(attrs, constrain("provider.absent", job.OperatorIsSet, "")) {
		t.Error("is_set matched an absent attribute")
	}

	if !MatchConstraint(attrs, constrain("provider.absent", job.OperatorIsNotSet, "")) {
		t.Error("is_not_set did not match an absent attribute")
	}
}

// -------------------------------------------------------------------------
// ORDERING
// -------------------------------------------------------------------------

// The example job's affinity. Seventy-two percent remaining is more than fifty,
// and the comparison has to be numeric rather than textual to say so.
func TestMatch_NumericOrdering(t *testing.T) {
	attrs := providerAttrs(t)

	if !MatchConstraint(attrs,
		constrain(plugin.AttrFreeQuotaPercent, job.OperatorGreater, "50")) {
		t.Error("72 was not greater than 50")
	}

	if MatchConstraint(attrs,
		constrain(plugin.AttrFreeQuotaPercent, job.OperatorGreater, "90")) {
		t.Error("72 was greater than 90")
	}
}

// Nine is greater than fifty as text and less as a number, which is the case
// that catches a comparison falling back to string ordering.
func TestMatch_NumericOrderingBeatsLexical(t *testing.T) {
	attrs := map[string]string{"provider.free_quota_percent": "9"}

	if MatchConstraint(attrs,
		constrain("provider.free_quota_percent", job.OperatorGreater, "50")) {
		t.Error("9 was greater than 50, so the comparison was lexical")
	}
}

// Durations are tried before numbers, because provider.max_duration is
// published as "15m0s" and every numeric reading of it fails. Nomad falls back
// to text here, which compares "15m0s" against "10m" as strings and produces an
// answer that is nonsense dressed as a result.
func TestMatch_DurationOrdering(t *testing.T) {
	attrs := map[string]string{"provider.max_duration": "15m0s"}

	tests := []struct {
		name  string
		op    job.Operator
		value string
		want  bool
	}{
		{name: "longer than ten minutes", op: job.OperatorGreater, value: "10m", want: true},
		{name: "not longer than an hour", op: job.OperatorGreater, value: "1h", want: false},
		{name: "at least fifteen minutes", op: job.OperatorGreaterEqual, value: "15m", want: true},
		{name: "under twenty minutes", op: job.OperatorLess, value: "20m", want: true},
		{name: "compound durations", op: job.OperatorLess, value: "1h30m", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchConstraint(attrs,
				constrain("provider.max_duration", tt.op, tt.value))

			if got != tt.want {
				t.Errorf("max_duration %s %s = %v, want %v", tt.op, tt.value, got, tt.want)
			}
		})
	}
}

// Text that is neither a duration nor a number still orders, because an
// operator's tag may hold anything and refusing to compare would be less useful
// than comparing the only way left.
func TestMatch_LexicalFallback(t *testing.T) {
	attrs := map[string]string{"provider.meta.tier": "gold"}

	if !MatchConstraint(attrs, constrain("provider.meta.tier", job.OperatorLess, "silver")) {
		t.Error("gold did not sort before silver")
	}
}

func TestMatch_FloatOrdering(t *testing.T) {
	attrs := map[string]string{"provider.meta.ratio": "1.5"}

	if !MatchConstraint(attrs, constrain("provider.meta.ratio", job.OperatorGreater, "1.25")) {
		t.Error("1.5 was not greater than 1.25")
	}
}

// -------------------------------------------------------------------------
// CONSTRAINT SETS
// -------------------------------------------------------------------------

// The first failure is returned rather than all of them, because the plan table
// shows one reason per provider.
func TestUnmatchedConstraint(t *testing.T) {
	attrs := providerAttrs(t)

	satisfied := []job.Constraint{
		*constrain(plugin.AttrArchitecture, job.OperatorSetContains, "amd64"),
		*constrain(plugin.AttrInternetEgress, job.OperatorEqual, "true"),
	}

	if failed := UnmatchedConstraint(attrs, satisfied); failed != nil {
		t.Errorf("a satisfied constraint set reported %q as failing", failed.Attribute)
	}

	mixed := []job.Constraint{
		*constrain(plugin.AttrInternetEgress, job.OperatorEqual, "true"),
		*constrain(plugin.AttrPrivateNetwork, job.OperatorEqual, "true"),
		*constrain(plugin.AttrArchitecture, job.OperatorSetContains, "s390x"),
	}

	failed := UnmatchedConstraint(attrs, mixed)
	if failed == nil {
		t.Fatal("an unsatisfiable constraint set reported nothing failing")
	}

	if failed.Attribute != plugin.AttrPrivateNetwork {
		t.Errorf("reported %q, want the first failure %q",
			failed.Attribute, plugin.AttrPrivateNetwork)
	}
}

// -------------------------------------------------------------------------
// UNKNOWN ATTRIBUTES
// -------------------------------------------------------------------------

// A typo is a different kind of answer from a provider being unsuitable, and
// reporting the second as the first is how it comes to look like an outage.
func TestUnknownAttribute(t *testing.T) {
	tests := []struct {
		name        string
		constraints []job.Constraint
		affinities  []job.Affinity
		want        string
	}{
		{
			name:        "all known",
			constraints: []job.Constraint{*constrain(plugin.AttrArchitecture, job.OperatorSetContains, "amd64")},
			want:        "",
		},
		{
			name:        "a typo in a constraint",
			constraints: []job.Constraint{*constrain("provider.architekture", job.OperatorEqual, "amd64")},
			want:        "provider.architekture",
		},
		{
			name:        "outside the reserved prefix entirely",
			constraints: []job.Constraint{*constrain("node.class", job.OperatorEqual, "large")},
			want:        "node.class",
		},
		{
			name:        "an operator tag is not a typo",
			constraints: []job.Constraint{*constrain("provider.meta.region", job.OperatorEqual, "eu")},
			want:        "",
		},
		{
			name: "a typo in an affinity",
			affinities: []job.Affinity{{
				Attribute: "provider.free_quota_percnt",
				Operator:  job.OperatorGreater,
				Value:     "50",
			}},
			want: "provider.free_quota_percnt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnknownAttribute(tt.constraints, tt.affinities); got != tt.want {
				t.Errorf("UnknownAttribute() = %q, want %q", got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// AFFINITIES
// -------------------------------------------------------------------------

func TestAffinityWeight(t *testing.T) {
	attrs := providerAttrs(t)

	satisfied := job.Affinity{
		Attribute: plugin.AttrFreeQuotaPercent,
		Operator:  job.OperatorGreater,
		Value:     "50",
		Weight:    ptr.Of(75),
	}

	unsatisfied := job.Affinity{
		Attribute: plugin.AttrFreeQuotaPercent,
		Operator:  job.OperatorGreater,
		Value:     "90",
		Weight:    ptr.Of(40),
	}

	if got := AffinityWeight(attrs, []job.Affinity{satisfied}); got != 75 {
		t.Errorf("a satisfied affinity contributed %d, want 75", got)
	}

	// Unsatisfied contributes nothing rather than subtracting, which is what
	// keeps an affinity a preference rather than a constraint in disguise.
	if got := AffinityWeight(attrs, []job.Affinity{unsatisfied}); got != 0 {
		t.Errorf("an unsatisfied affinity contributed %d, want 0", got)
	}

	if got := AffinityWeight(attrs, []job.Affinity{satisfied, unsatisfied}); got != 75 {
		t.Errorf("a mixed set contributed %d, want 75", got)
	}
}

// An affinity with no weight still has to mean something, since the attribute
// is optional in the specification.
func TestAffinityWeight_DefaultsWhenUnweighted(t *testing.T) {
	attrs := providerAttrs(t)

	unweighted := job.Affinity{
		Attribute: plugin.AttrFreeQuotaPercent,
		Operator:  job.OperatorGreater,
		Value:     "50",
	}

	if got := AffinityWeight(attrs, []job.Affinity{unweighted}); got != defaultAffinityWeight {
		t.Errorf("an unweighted affinity contributed %d, want %d", got, defaultAffinityWeight)
	}
}

func TestAffinityWeight_NoAffinities(t *testing.T) {
	if got := AffinityWeight(providerAttrs(t), nil); got != 0 {
		t.Errorf("no affinities contributed %d, want 0", got)
	}
}

// -------------------------------------------------------------------------
// UNKNOWN OPERATORS
// -------------------------------------------------------------------------

// Validation rejects these before admission sees one, but matching must not
// accidentally admit on a value it cannot evaluate.
func TestMatch_UnknownOperatorNeverMatches(t *testing.T) {
	attrs := map[string]string{"provider.internet": "true"}

	if MatchConstraint(attrs, constrain("provider.internet", "approximately", "true")) {
		t.Error("an unknown operator matched")
	}
}

// Every ordering operator, so none is left unexercised by the cases above.
func TestMatch_EveryOrderingOperator(t *testing.T) {
	attrs := map[string]string{"provider.meta.count": "10"}

	tests := []struct {
		op    job.Operator
		value string
		want  bool
	}{
		{op: job.OperatorLess, value: "11", want: true},
		{op: job.OperatorLess, value: "10", want: false},
		{op: job.OperatorLessEqual, value: "10", want: true},
		{op: job.OperatorLessEqual, value: "9", want: false},
		{op: job.OperatorGreater, value: "9", want: true},
		{op: job.OperatorGreater, value: "10", want: false},
		{op: job.OperatorGreaterEqual, value: "10", want: true},
		{op: job.OperatorGreaterEqual, value: "11", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.op.String()+tt.value, func(t *testing.T) {
			got := MatchConstraint(attrs, constrain("provider.meta.count", tt.op, tt.value))

			if got != tt.want {
				t.Errorf("10 %s %s = %v, want %v", tt.op, tt.value, got, tt.want)
			}
		})
	}
}
