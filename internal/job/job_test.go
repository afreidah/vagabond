// -------------------------------------------------------------------------------
// Job and Job Type Tests
//
// Author: Alex Freidah
//
// The job type vocabulary rejects service, system and sysbatch by name. They
// are the likeliest wrong spellings, and Vagabond brokers only short-lived
// work, so refusing them with the valid set beats accepting a job with no
// completion to report.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// JOB TYPE
// -------------------------------------------------------------------------

// Only batch validates; long-running types and case variants do not.
func TestJobType_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input Type
		want  bool
	}{
		{name: "batch", input: TypeBatch, want: true},
		{name: "empty", input: "", want: false},
		{name: "nomad service type", input: "service", want: false},
		{name: "nomad system type", input: "system", want: false},
		{name: "case variant", input: "Batch", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Type(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// The batch spelling parses to TypeBatch.
func TestParseJobType(t *testing.T) {
	got, err := ParseType("batch")
	if err != nil {
		t.Fatalf("ParseType returned unexpected error: %v", err)
	}

	if got != TypeBatch {
		t.Errorf("ParseType = %q, want %q", got, TypeBatch)
	}
}

// A type outside the vocabulary fails with ErrUnknownType.
func TestParseJobType_Invalid(t *testing.T) {
	for _, input := range []string{"", "service", "system", "sysbatch"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseType(input)
			if err == nil {
				t.Fatalf("ParseType(%q) = %q, want error", input, got)
			}

			if !errors.Is(err, ErrUnknownType) {
				t.Errorf("ParseType(%q) error does not wrap ErrUnknownType: %v", input, err)
			}
		})
	}
}

// The parse error names every valid job type.
func TestJobType_ErrorListsVocabulary(t *testing.T) {
	_, err := ParseType("service")
	if err == nil {
		t.Fatal("ParseType(\"service\") returned no error")
	}

	for _, jt := range Types() {
		if !strings.Contains(err.Error(), jt.String()) {
			t.Errorf("error %q omits valid job type %q", err, jt)
		}
	}
}

// Mutating the returned slice leaves the vocabulary untouched.
func TestJobTypes_ReturnsCopy(t *testing.T) {
	first := Types()
	first[0] = "mutated"

	if Types()[0] == "mutated" {
		t.Error("Types() exposed the package-level vocabulary to mutation")
	}
}

// Every job type survives a MarshalText and UnmarshalText round trip.
func TestJobType_TextRoundTrip(t *testing.T) {
	for _, want := range Types() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Type
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != want {
				t.Errorf("round trip produced %q, want %q", got, want)
			}
		})
	}
}

// The zero value is not a job type and does not marshal.
func TestJobType_MarshalTextRejectsZeroValue(t *testing.T) {
	var jt Type

	if _, err := jt.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value Type")
	}
}

// Unmarshalling an unknown type fails with ErrUnknownType.
func TestJobType_UnmarshalTextRejectsUnknown(t *testing.T) {
	var jt Type

	err := jt.UnmarshalText([]byte("service"))
	if err == nil {
		t.Fatal("UnmarshalText accepted service")
	}

	if !errors.Is(err, ErrUnknownType) {
		t.Errorf("error does not wrap ErrUnknownType: %v", err)
	}
}

// -------------------------------------------------------------------------
// ROUTING
// -------------------------------------------------------------------------

// Absent routing admits every provider; an empty provider list admits none, and
// conflating the two would un-route every job that did not care.
func TestJob_AbsentRoutingIsDistinctFromEmpty(t *testing.T) {
	noPreference := Job{Name: "a"}
	if noPreference.Routing != nil {
		t.Error("a job without a routing block has non-nil Routing")
	}

	explicitlyEmpty := Job{Name: "b", Routing: &Routing{Providers: []string{}}}
	if explicitlyEmpty.Routing == nil {
		t.Fatal("a job with an empty provider list has nil Routing")
	}

	if explicitlyEmpty.Routing.Providers == nil {
		t.Error("an explicitly empty provider list decoded as absent")
	}
}
