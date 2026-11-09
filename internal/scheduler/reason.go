// -------------------------------------------------------------------------------
// Rejection Reasons
//
// Author: Alex Freidah
//
// These strings are permanent surface: they appear in job plan output, in API
// responses, and in whatever a CI system branches on, so they are designed
// rather than grown. Each code names one distinct cause, so the plan table
// tells an author what to fix.
// -------------------------------------------------------------------------------

package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// REASONS
// -------------------------------------------------------------------------

// Reason is why a provider cannot run a task.
type Reason string

// The reasons admission can give, in three groups: task mismatches are
// permanent for the pairing, policy rejections are the job declining the
// provider, and provider conditions may clear without the job changing.
const (
	ReasonDriverUnsupported  Reason = "driver-unsupported"
	ReasonArchUnsupported    Reason = "arch-unsupported"
	ReasonResourcesExceeded  Reason = "resources-exceeded"
	ReasonDurationExceeded   Reason = "duration-exceeded"
	ReasonNetworkUnsupported Reason = "network-unsupported"
	ReasonImageUnsupported   Reason = "image-unsupported"
	ReasonAttributeUnknown   Reason = "attribute-unknown"
	ReasonConstraintUnmet    Reason = "constraint-unmet"

	ReasonNotAllowlisted Reason = "not-allowlisted"
	ReasonCostPolicy     Reason = "cost-policy"

	ReasonQuotaExhausted   Reason = "quota-exhausted"
	ReasonProviderDisabled Reason = "provider-disabled"
	ReasonUnhealthy        Reason = "provider-unhealthy"
)

// reasonNames is the vocabulary, in the order Reasons returns it.
var reasonNames = []Reason{
	ReasonDriverUnsupported,
	ReasonArchUnsupported,
	ReasonResourcesExceeded,
	ReasonDurationExceeded,
	ReasonNetworkUnsupported,
	ReasonImageUnsupported,
	ReasonAttributeUnknown,
	ReasonConstraintUnmet,
	ReasonNotAllowlisted,
	ReasonCostPolicy,
	ReasonQuotaExhausted,
	ReasonProviderDisabled,
	ReasonUnhealthy,
}

// transientReasons are the rejections a provider can recover from without the
// job changing, which tells a caller to retry later rather than fail.
var transientReasons = []Reason{
	ReasonQuotaExhausted,
	ReasonProviderDisabled,
	ReasonUnhealthy,
}

// ErrUnknownReason is the sentinel behind every unrecognized reason code.
var ErrUnknownReason = errors.New("unknown rejection reason")

// -------------------------------------------------------------------------
// VOCABULARY
// -------------------------------------------------------------------------

// Reasons returns every rejection reason, grouped mismatch, policy, condition,
// as a copy a caller cannot reorder for everyone else.
func Reasons() []Reason {
	return slices.Clone(reasonNames)
}

// Valid reports whether r is a reason admission can give.
func (r Reason) Valid() bool {
	return slices.Contains(reasonNames, r)
}

// String returns the reason as it appears in plan output and API responses.
func (r Reason) String() string {
	return string(r)
}

// Transient reports whether the provider may accept this same job later without
// anything about the job changing.
func (r Reason) Transient() bool {
	return slices.Contains(transientReasons, r)
}

// ParseReason converts in into a Reason, or reports that it is not one.
func ParseReason(in string) (Reason, error) {
	r := Reason(in)
	if !r.Valid() {
		return "", unknownReason(in)
	}

	return r, nil
}

// MarshalText implements encoding.TextMarshaler.
func (r Reason) MarshalText() ([]byte, error) {
	if !r.Valid() {
		return nil, unknownReason(string(r))
	}

	return []byte(r), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *Reason) UnmarshalText(text []byte) error {
	parsed, err := ParseReason(string(text))
	if err != nil {
		return err
	}

	*r = parsed

	return nil
}

// unknownReason wraps ErrUnknownReason with name and the valid vocabulary.
func unknownReason(name string) error {
	valid := make([]string, 0, len(reasonNames))
	for _, r := range reasonNames {
		valid = append(valid, string(r))
	}

	return fmt.Errorf("%w %q: valid reasons are %s",
		ErrUnknownReason, name, strings.Join(valid, ", "))
}
