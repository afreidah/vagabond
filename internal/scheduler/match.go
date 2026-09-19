// -------------------------------------------------------------------------------
// Attribute Matching
//
// Author: Alex Freidah
//
// Evaluates a job's constraints and affinities against what a provider
// publishes about itself. A constraint answers yes or no and decides admission;
// an affinity answers the same question and contributes its weight to a score.
//
// Everything here is a pure function of an attribute map and a comparison.
// Admission fans out across every configured provider on a plan, so nothing in
// this path may allocate a connection, read a clock, or care what order it is
// called in.
// -------------------------------------------------------------------------------

package scheduler

import (
	"strconv"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTRAINTS
// -------------------------------------------------------------------------

// MatchConstraint reports whether a provider satisfies one constraint.
func MatchConstraint(attrs map[string]string, c *job.Constraint) bool {
	return match(attrs, c.Attribute, c.Operator, c.Value)
}

// MatchAffinity reports whether a provider satisfies one affinity.
//
// Identical to a constraint; what differs is the consequence. Nomad shares the
// implementation the same way, with checkAffinity calling straight through to
// checkConstraint.
func MatchAffinity(attrs map[string]string, a *job.Affinity) bool {
	return match(attrs, a.Attribute, a.Operator, a.Value)
}

// match evaluates one comparison against a provider's attributes.
//
// Absence is handled per operator rather than uniformly. An attribute the
// provider never published is not equal to anything, so != holds, and is_not_set
// holds by definition; every other operator needs a value to compare and fails
// without one. Nomad draws the same line, and getting it wrong means a provider
// silently matching a constraint about a capability it never claimed.
func match(attrs map[string]string, name string, op job.Operator, want string) bool {
	got, found := attrs[name]

	switch op {
	case job.OperatorIsSet:
		return found

	case job.OperatorIsNotSet:
		return !found

	case job.OperatorNotEqual:
		return got != want

	case job.OperatorEqual:
		return found && got == want

	case job.OperatorSetContains:
		return found && setContains(got, want)

	case job.OperatorLess, job.OperatorLessEqual,
		job.OperatorGreater, job.OperatorGreaterEqual:
		return found && compare(op, got, want)

	default:
		return false
	}
}

// -------------------------------------------------------------------------
// SETS
// -------------------------------------------------------------------------

// setContains reports whether a comma-separated attribute holds a value.
//
// Whitespace around an entry is ignored, because a provider rendering a set and
// a job author writing one should not have to agree about spacing.
func setContains(set, want string) bool {
	want = strings.TrimSpace(want)

	for _, member := range strings.Split(set, ",") {
		if strings.TrimSpace(member) == want {
			return true
		}
	}

	return false
}

// -------------------------------------------------------------------------
// ORDERING
// -------------------------------------------------------------------------

// compare evaluates an ordering operator, choosing how to read the operands.
//
// Durations first, then integers, then floats, then text. Nomad tries integer,
// float, then text; durations are added ahead of those because
// provider.max_duration is published as "15m0s" and every numeric reading of it
// fails, leaving a text comparison that is nonsense dressed as an answer.
//
// The order is unambiguous rather than merely convenient: a bare number is not
// a valid Go duration, so nothing that should be read as a number is read as a
// duration first.
func compare(op job.Operator, got, want string) bool {
	if result, ok := compareDurations(op, got, want); ok {
		return result
	}

	if result, ok := compareIntegers(op, got, want); ok {
		return result
	}

	if result, ok := compareFloats(op, got, want); ok {
		return result
	}

	return ordered(op, strings.Compare(got, want))
}

// compareDurations reads both sides as durations, reporting whether it could.
func compareDurations(op job.Operator, got, want string) (result, ok bool) {
	left, leftErr := time.ParseDuration(got)
	if leftErr != nil {
		return false, false
	}

	right, rightErr := time.ParseDuration(want)
	if rightErr != nil {
		return false, false
	}

	return ordered(op, compareOrdered(left, right)), true
}

// compareIntegers reads both sides as integers, reporting whether it could.
func compareIntegers(op job.Operator, got, want string) (result, ok bool) {
	left, leftErr := strconv.ParseInt(got, 10, 64)
	if leftErr != nil {
		return false, false
	}

	right, rightErr := strconv.ParseInt(want, 10, 64)
	if rightErr != nil {
		return false, false
	}

	return ordered(op, compareOrdered(left, right)), true
}

// compareFloats reads both sides as floats, reporting whether it could.
func compareFloats(op job.Operator, got, want string) (result, ok bool) {
	left, leftErr := strconv.ParseFloat(got, 64)
	if leftErr != nil {
		return false, false
	}

	right, rightErr := strconv.ParseFloat(want, 64)
	if rightErr != nil {
		return false, false
	}

	return ordered(op, compareOrdered(left, right)), true
}

// compareOrdered returns the sign of left minus right, in the shape
// strings.Compare already uses.
func compareOrdered[T int64 | float64 | time.Duration](left, right T) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

// ordered turns a comparison result into the answer an operator wanted.
func ordered(op job.Operator, cmp int) bool {
	switch op {
	case job.OperatorLess:
		return cmp < 0
	case job.OperatorLessEqual:
		return cmp <= 0
	case job.OperatorGreater:
		return cmp > 0
	case job.OperatorGreaterEqual:
		return cmp >= 0
	default:
		return false
	}
}

// -------------------------------------------------------------------------
// ADMISSION AND SCORING
// -------------------------------------------------------------------------

// UnmatchedConstraint returns the first constraint a provider fails, or nil.
//
// The first rather than all of them, because the plan table shows one reason per
// provider and a provider failing four checks would otherwise crowd out the
// three that did not.
func UnmatchedConstraint(attrs map[string]string, constraints []job.Constraint) *job.Constraint {
	for i := range constraints {
		if !MatchConstraint(attrs, &constraints[i]) {
			return &constraints[i]
		}
	}

	return nil
}

// UnknownAttribute returns the first constraint or affinity naming an attribute
// Vagabond does not publish, or an empty string.
//
// Separate from matching because it is a different kind of answer. A constraint
// that does not hold is a provider being unsuitable; a constraint naming
// provider.architekture is a job file being wrong, and reporting the second as
// the first is how a typo comes to look like an outage.
func UnknownAttribute(constraints []job.Constraint, affinities []job.Affinity) string {
	for i := range constraints {
		if !plugin.Matchable(constraints[i].Attribute) {
			return constraints[i].Attribute
		}
	}

	for i := range affinities {
		if !plugin.Matchable(affinities[i].Attribute) {
			return affinities[i].Attribute
		}
	}

	return ""
}

// AffinityWeight sums the weights of every affinity a provider satisfies.
//
// An unsatisfied affinity contributes nothing rather than subtracting, which is
// what keeps it a preference: a provider that matches none is still admissible,
// merely ranked below one that matches some.
func AffinityWeight(attrs map[string]string, affinities []job.Affinity) int {
	total := 0

	for i := range affinities {
		affinity := &affinities[i]

		if !MatchAffinity(attrs, affinity) {
			continue
		}

		total += affinityWeight(affinity)
	}

	return total
}

// defaultAffinityWeight is what an affinity contributes when a job does not say.
//
// Nomad's weight range is -100 to 100 and its stanza requires one; ours is
// optional, so an omitted weight has to mean something. Half of the maximum
// reads as "prefer this, no strong feeling", which is what writing an affinity
// and declining to weight it says.
const defaultAffinityWeight = 50

// affinityWeight returns an affinity's declared weight, or the default.
func affinityWeight(a *job.Affinity) int {
	if a.Weight == nil {
		return defaultAffinityWeight
	}

	return *a.Weight
}
