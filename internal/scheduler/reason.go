// -------------------------------------------------------------------------------
// Rejection Reasons
//
// Author: Alex Freidah
//
// These strings are permanent surface. They appear in job plan output, in API
// responses, and in whatever a CI system branches on when Vagabond declines
// work, so they are expensive to change and are designed rather than grown.
//
// One code per distinct cause. A vocabulary small enough to be convenient makes
// the plan table useless, and being diagnostic is the only reason that table
// exists: "driver container unsupported" tells an author what to do, while
// "unsupported" tells them to go reading.
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

// The reasons admission can give.
//
// They fall into three groups, which is worth knowing when reading plan output.
// A task mismatch means this job will never run on this provider whatever
// happens. A policy rejection means the job asked Vagabond not to. A provider
// condition is temporary and the same job may be admitted an hour later.
const (
	// Task mismatches: permanent for this pairing.
	ReasonDriverUnsupported  Reason = "driver-unsupported"
	ReasonArchUnsupported    Reason = "arch-unsupported"
	ReasonResourcesExceeded  Reason = "resources-exceeded"
	ReasonDurationExceeded   Reason = "duration-exceeded"
	ReasonNetworkUnsupported Reason = "network-unsupported"
	ReasonImageUnsupported   Reason = "image-unsupported"
	ReasonAttributeUnknown   Reason = "attribute-unknown"
	ReasonConstraintUnmet    Reason = "constraint-unmet"

	// Policy: the job declined this provider.
	ReasonNotAllowlisted Reason = "not-allowlisted"
	ReasonCostPolicy     Reason = "cost-policy"

	// Provider conditions: may change without the job changing.
	ReasonQuotaExhausted   Reason = "quota-exhausted"
	ReasonProviderDisabled Reason = "provider-disabled"
	ReasonUnhealthy        Reason = "provider-unhealthy"
)

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
// job changing. A caller deciding whether to retry later rather than fail reads
// this, and it is the difference between waiting for a quota period to reset
// and waiting forever for a Wasm runtime to grow a container engine.
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

// Reasons returns every rejection reason, grouped mismatch, policy, condition.
//
// The returned slice is a copy, so a caller rendering it cannot reorder the
// vocabulary for everyone else.
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

func unknownReason(name string) error {
	valid := make([]string, 0, len(reasonNames))
	for _, r := range reasonNames {
		valid = append(valid, string(r))
	}

	return fmt.Errorf("%w %q: valid reasons are %s",
		ErrUnknownReason, name, strings.Join(valid, ", "))
}
