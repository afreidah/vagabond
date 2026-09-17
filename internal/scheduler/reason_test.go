// -------------------------------------------------------------------------------
// Rejection Reason Tests
//
// Author: Alex Freidah
//
// These codes are permanent API surface, so the tests pin the vocabulary itself
// rather than only the behaviour around it. A reason quietly removed or
// respelled is a breaking change for anything branching on plan output.
// -------------------------------------------------------------------------------

package scheduler

import (
	"errors"
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// VOCABULARY
// -------------------------------------------------------------------------

// The exact set, spelled out. This test failing means the published vocabulary
// changed, which should be a deliberate decision rather than a side effect.
func TestReasons_ExactVocabulary(t *testing.T) {
	want := []Reason{
		"driver-unsupported",
		"arch-unsupported",
		"resources-exceeded",
		"duration-exceeded",
		"network-unsupported",
		"image-unsupported",
		"attribute-unknown",
		"constraint-unmet",
		"not-allowlisted",
		"cost-policy",
		"quota-exhausted",
		"provider-disabled",
		"provider-unhealthy",
	}

	got := Reasons()
	if len(got) != len(want) {
		t.Fatalf("Reasons() returned %d reasons, want %d", len(got), len(want))
	}

	for i, w := range want {
		if got[i] != w {
			t.Errorf("Reasons()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// The README's plan output prints these two, so they have to keep their
// spelling whatever else changes.
func TestReasons_ReadmeExamplesExist(t *testing.T) {
	for _, r := range []Reason{ReasonDriverUnsupported, ReasonQuotaExhausted} {
		if !r.Valid() {
			t.Errorf("%q is not a valid reason", r)
		}
	}
}

func TestReason_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input Reason
		want  bool
	}{
		{name: "driver unsupported", input: ReasonDriverUnsupported, want: true},
		{name: "quota exhausted", input: ReasonQuotaExhausted, want: true},
		{name: "empty", input: "", want: false},
		{name: "bare unsupported is too coarse to be a reason", input: "unsupported", want: false},
		{name: "underscore variant", input: "driver_unsupported", want: false},
		{name: "case variant", input: "Driver-Unsupported", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Reason(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// TRANSIENCE
// -------------------------------------------------------------------------

// Transience is the difference between waiting for a quota period to reset and
// waiting forever for a Wasm runtime to grow a container engine.
func TestReason_Transient(t *testing.T) {
	transient := map[Reason]bool{
		ReasonQuotaExhausted:   true,
		ReasonProviderDisabled: true,
		ReasonUnhealthy:        true,

		ReasonDriverUnsupported:  false,
		ReasonArchUnsupported:    false,
		ReasonResourcesExceeded:  false,
		ReasonDurationExceeded:   false,
		ReasonNetworkUnsupported: false,
		ReasonImageUnsupported:   false,
		ReasonAttributeUnknown:   false,
		ReasonConstraintUnmet:    false,
		ReasonNotAllowlisted:     false,
		ReasonCostPolicy:         false,
	}

	if len(transient) != len(Reasons()) {
		t.Fatalf("the transience table covers %d reasons, but %d exist", len(transient), len(Reasons()))
	}

	for reason, want := range transient {
		t.Run(reason.String(), func(t *testing.T) {
			if got := reason.Transient(); got != want {
				t.Errorf("%q.Transient() = %v, want %v", reason, got, want)
			}
		})
	}
}

// A task mismatch is never transient. Retrying one burns free-tier capacity to
// reach the same refusal.
func TestReason_TaskMismatchesAreNeverTransient(t *testing.T) {
	mismatches := []Reason{
		ReasonDriverUnsupported,
		ReasonArchUnsupported,
		ReasonResourcesExceeded,
		ReasonDurationExceeded,
		ReasonImageUnsupported,
	}

	for _, r := range mismatches {
		if r.Transient() {
			t.Errorf("%q is transient, but no amount of waiting changes it", r)
		}
	}
}

// -------------------------------------------------------------------------
// ENCODING
// -------------------------------------------------------------------------

func TestReason_TextRoundTrip(t *testing.T) {
	for _, want := range Reasons() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Reason
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != want {
				t.Errorf("round trip produced %q, want %q", got, want)
			}
		})
	}
}

func TestReason_MarshalTextRejectsZeroValue(t *testing.T) {
	var r Reason

	if _, err := r.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value Reason")
	}
}

func TestParseReason_Invalid(t *testing.T) {
	for _, input := range []string{"", "unsupported", "no-capacity", "Quota-Exhausted"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseReason(input)
			if err == nil {
				t.Fatalf("ParseReason(%q) = %q, want error", input, got)
			}

			if !errors.Is(err, ErrUnknownReason) {
				t.Errorf("error does not wrap ErrUnknownReason: %v", err)
			}
		})
	}
}

func TestReason_UnmarshalTextRejectsUnknown(t *testing.T) {
	var r Reason

	if err := r.UnmarshalText([]byte("unsupported")); !errors.Is(err, ErrUnknownReason) {
		t.Errorf("error = %v, want ErrUnknownReason", err)
	}
}

func TestReason_ErrorListsVocabulary(t *testing.T) {
	_, err := ParseReason("nonsense")
	if err == nil {
		t.Fatal("ParseReason(\"nonsense\") returned no error")
	}

	if !strings.Contains(err.Error(), ReasonDriverUnsupported.String()) {
		t.Errorf("error %q does not list the valid reasons", err)
	}
}

func TestReasons_ReturnsCopy(t *testing.T) {
	first := Reasons()
	first[0] = "mutated"

	if Reasons()[0] == "mutated" {
		t.Error("Reasons() exposed the package-level vocabulary to mutation")
	}
}
