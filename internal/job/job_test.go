// -------------------------------------------------------------------------------
// Job and Job Type Tests
//
// Author: Alex Freidah
//
// The job type vocabulary rejects Nomad's other types by name. They are the
// spellings a Nomad user would reach for first, and Vagabond brokers only
// short-lived work, so refusing them with a message naming the valid set is
// better than accepting a job with no completion to report.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"strings"
	"testing"
)

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

func TestParseJobType(t *testing.T) {
	got, err := ParseType("batch")
	if err != nil {
		t.Fatalf("ParseType returned unexpected error: %v", err)
	}

	if got != TypeBatch {
		t.Errorf("ParseType = %q, want %q", got, TypeBatch)
	}
}

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

func TestJobTypes_ReturnsCopy(t *testing.T) {
	first := Types()
	first[0] = "mutated"

	if Types()[0] == "mutated" {
		t.Error("Types() exposed the package-level vocabulary to mutation")
	}
}

// Absent routing is not the same as routing that allows nothing. A job with no
// preference is admissible everywhere; an empty provider list would mean no
// provider is permitted, and conflating the two would silently un-route every
// job that simply did not care.
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

func TestJobType_MarshalTextRejectsZeroValue(t *testing.T) {
	var jt Type

	if _, err := jt.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value Type")
	}
}

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
