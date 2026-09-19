// -------------------------------------------------------------------------------
// Constraint Operator Tests
//
// Author: Alex Freidah
//
// The vocabulary is smaller than Nomad's on purpose, so the tests pin what is
// absent as well as what is present: an operator arriving later should be a
// decision rather than something that slipped in.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"strings"
	"testing"
)

// The exact set. This failing means the published vocabulary changed, which
// should be deliberate.
func TestOperators_ExactVocabulary(t *testing.T) {
	want := []Operator{
		"=", "!=", "<", "<=", ">", ">=",
		"set_contains", "is_set", "is_not_set",
	}

	got := Operators()
	if len(got) != len(want) {
		t.Fatalf("Operators() returned %d, want %d: %v", len(got), len(want), got)
	}

	for i, w := range want {
		if got[i] != w {
			t.Errorf("Operators()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// Nomad's extra operators are deliberately absent. Each answers a question a
// provider attribute does not raise, and each would be surface to maintain.
func TestOperator_NomadExtrasAreAbsent(t *testing.T) {
	for _, absent := range []Operator{
		"regexp", "version", "semver",
		"set_contains_all", "set_contains_any",
		"distinct_hosts", "distinct_property",
		"==", "is", "not",
	} {
		if absent.Valid() {
			t.Errorf("%q is in the vocabulary but was not meant to be", absent)
		}
	}
}

func TestOperator_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input Operator
		want  bool
	}{
		{name: "equality", input: OperatorEqual, want: true},
		{name: "set membership", input: OperatorSetContains, want: true},
		{name: "presence", input: OperatorIsSet, want: true},
		{name: "empty", input: "", want: false},
		{name: "spelled out", input: "equals", want: false},
		{name: "case variant", input: "SET_CONTAINS", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Operator(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// Ordering decides whether both sides have to be comparable as magnitudes,
// which is what makes a duration or a number meaningful.
func TestOperator_Ordering(t *testing.T) {
	ordering := map[Operator]bool{
		OperatorLess:         true,
		OperatorLessEqual:    true,
		OperatorGreater:      true,
		OperatorGreaterEqual: true,

		OperatorEqual:       false,
		OperatorNotEqual:    false,
		OperatorSetContains: false,
		OperatorIsSet:       false,
		OperatorIsNotSet:    false,
	}

	if len(ordering) != len(Operators()) {
		t.Fatalf("the ordering table covers %d operators, but %d exist",
			len(ordering), len(Operators()))
	}

	for op, want := range ordering {
		t.Run(op.String(), func(t *testing.T) {
			if got := op.Ordering(); got != want {
				t.Errorf("%q.Ordering() = %v, want %v", op, got, want)
			}
		})
	}
}

// A presence operator takes no value, which is what lets validation report a
// value alongside one as the misunderstanding it is.
func TestOperator_Presence(t *testing.T) {
	if !OperatorIsSet.Presence() || !OperatorIsNotSet.Presence() {
		t.Error("a presence operator does not report as one")
	}

	for _, op := range []Operator{OperatorEqual, OperatorGreater, OperatorSetContains} {
		if op.Presence() {
			t.Errorf("%q reports as a presence operator", op)
		}
	}
}

func TestParseOperator(t *testing.T) {
	got, err := ParseOperator("set_contains")
	if err != nil {
		t.Fatalf("ParseOperator returned unexpected error: %v", err)
	}

	if got != OperatorSetContains {
		t.Errorf("ParseOperator = %q, want %q", got, OperatorSetContains)
	}
}

func TestParseOperator_Invalid(t *testing.T) {
	for _, input := range []string{"", "equals", "=~", "contains"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseOperator(input)
			if err == nil {
				t.Fatalf("ParseOperator(%q) = %q, want error", input, got)
			}

			if !errors.Is(err, ErrUnknownOperator) {
				t.Errorf("error does not wrap ErrUnknownOperator: %v", err)
			}
		})
	}
}

// The message reaches an author editing a job file, and the correction is
// almost always a spelling they can take off the list.
func TestOperator_ErrorListsVocabulary(t *testing.T) {
	_, err := ParseOperator("contains")
	if err == nil {
		t.Fatal("ParseOperator(\"contains\") returned no error")
	}

	for _, op := range Operators() {
		if !strings.Contains(err.Error(), op.String()) {
			t.Errorf("error %q omits valid operator %q", err, op)
		}
	}
}

func TestOperator_TextRoundTrip(t *testing.T) {
	for _, want := range Operators() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Operator
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != want {
				t.Errorf("round trip produced %q, want %q", got, want)
			}
		})
	}
}

func TestOperator_MarshalTextRejectsZeroValue(t *testing.T) {
	var o Operator

	if _, err := o.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value Operator")
	}
}

func TestOperator_UnmarshalTextRejectsUnknown(t *testing.T) {
	var o Operator

	if err := o.UnmarshalText([]byte("regexp")); !errors.Is(err, ErrUnknownOperator) {
		t.Errorf("error = %v, want ErrUnknownOperator", err)
	}
}

func TestOperators_ReturnsCopy(t *testing.T) {
	first := Operators()
	first[0] = "mutated"

	if Operators()[0] == "mutated" {
		t.Error("Operators() exposed the package-level vocabulary to mutation")
	}
}
