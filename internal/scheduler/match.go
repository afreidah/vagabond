// -------------------------------------------------------------------------------
// Attribute Matching
//
// Author: Alex Freidah
//
// Evaluates a job's constraints and affinities against what a provider
// publishes about itself. A constraint answers yes or no and decides admission;
// an affinity answers the same question and contributes its weight to a score.
// Everything here is a pure function of an attribute map and a comparison,
// since admission fans out across every configured provider on a plan.
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

// MatchAffinity reports whether a provider satisfies one affinity. The match is
// a constraint's; only the consequence differs.
func MatchAffinity(attrs map[string]string, a *job.Affinity) bool {
	return match(attrs, a.Attribute, a.Operator, a.Value)
}

// match evaluates one comparison against a provider's attributes. An attribute
// never published equals nothing, so != and is_not_set hold; every other
// operator needs a value and fails without one.
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

// setContains reports whether a comma-separated attribute holds a value,
// ignoring whitespace around each entry.
func setContains(set, want string) bool {
	want = strings.TrimSpace(want)

	for member := range strings.SplitSeq(set, ",") {
		if strings.TrimSpace(member) == want {
			return true
		}
	}

	return false
}

// -------------------------------------------------------------------------
// ORDERING
// -------------------------------------------------------------------------

// compare evaluates an ordering operator, reading the operands as durations,
// then integers, then floats, then text. Durations come first because
// provider.max_duration is published as "15m0s"; a bare number is not a valid
// duration, so nothing numeric is misread as one.
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

// UnmatchedConstraint returns the first constraint a provider fails, or nil,
// since the plan table shows one reason per provider.
func UnmatchedConstraint(attrs map[string]string, constraints []job.Constraint) *job.Constraint {
	for i := range constraints {
		if !MatchConstraint(attrs, &constraints[i]) {
			return &constraints[i]
		}
	}

	return nil
}

// UnknownAttribute returns the first constraint or affinity naming an attribute
// Vagabond does not publish, or an empty string. Separate from matching, since
// a misspelled attribute is a job file being wrong, not a provider unsuitable.
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

// AffinityWeight sums the weights of every affinity a provider satisfies. An
// unsatisfied one contributes nothing rather than subtracting, so it stays a
// preference.
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

// TotalAffinityWeight sums every affinity's weight, satisfied or not: the
// denominator that makes a weight relative rather than a number of points.
func TotalAffinityWeight(affinities []job.Affinity) int {
	total := 0

	for i := range affinities {
		total += affinityWeight(&affinities[i])
	}

	return total
}

// AffinityScore reports matched weight over total weight, from zero to one;
// weights are validated positive. A job with no affinities scores zero, and
// the caller leaves this scorer out rather than average the zero in.
func AffinityScore(attrs map[string]string, affinities []job.Affinity) float64 {
	total := TotalAffinityWeight(affinities)
	if total == 0 {
		return 0
	}

	return float64(AffinityWeight(attrs, affinities)) / float64(total)
}

// defaultAffinityWeight is what an affinity contributes when a job does not
// say: half the maximum, a preference with no strong feeling.
const defaultAffinityWeight = 50

// affinityWeight returns an affinity's declared weight, or the default.
func affinityWeight(a *job.Affinity) int {
	if a.Weight == nil {
		return defaultAffinityWeight
	}

	return *a.Weight
}
