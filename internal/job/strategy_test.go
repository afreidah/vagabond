// -------------------------------------------------------------------------------
// Routing Strategy Tests
//
// Author: Alex Freidah
//
// The vocabulary holds one entry in phase 1, so these tests exist mostly to
// make adding a second one safe: the copy and error-message guarantees are what
// a later strategy inherits for free.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"strings"
	"testing"
)

func TestStrategy_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input Strategy
		want  bool
	}{
		{name: "free-first", input: StrategyFreeFirst, want: true},
		{name: "empty", input: "", want: false},
		{name: "underscore variant", input: "free_first", want: false},
		{name: "case variant", input: "Free-First", want: false},
		{name: "unimplemented strategy", input: "cheapest", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Strategy(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseStrategy(t *testing.T) {
	got, err := ParseStrategy("free-first")
	if err != nil {
		t.Fatalf("ParseStrategy returned unexpected error: %v", err)
	}

	if got != StrategyFreeFirst {
		t.Errorf("ParseStrategy = %q, want %q", got, StrategyFreeFirst)
	}
}

func TestParseStrategy_Invalid(t *testing.T) {
	for _, input := range []string{"", "cheapest", "free_first"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseStrategy(input)
			if err == nil {
				t.Fatalf("ParseStrategy(%q) = %q, want error", input, got)
			}

			if !errors.Is(err, ErrUnknownStrategy) {
				t.Errorf("ParseStrategy(%q) error does not wrap ErrUnknownStrategy: %v", input, err)
			}
		})
	}
}

func TestStrategy_ErrorListsVocabulary(t *testing.T) {
	_, err := ParseStrategy("cheapest")
	if err == nil {
		t.Fatal("ParseStrategy(\"cheapest\") returned no error")
	}

	for _, s := range Strategies() {
		if !strings.Contains(err.Error(), s.String()) {
			t.Errorf("error %q omits valid strategy %q", err, s)
		}
	}
}

func TestStrategies_ReturnsCopy(t *testing.T) {
	first := Strategies()
	first[0] = "mutated"

	if Strategies()[0] == "mutated" {
		t.Error("Strategies() exposed the package-level vocabulary to mutation")
	}
}

func TestStrategy_TextRoundTrip(t *testing.T) {
	for _, want := range Strategies() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Strategy
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != want {
				t.Errorf("round trip produced %q, want %q", got, want)
			}
		})
	}
}

func TestStrategy_MarshalTextRejectsZeroValue(t *testing.T) {
	var s Strategy

	if _, err := s.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value Strategy")
	}
}

func TestStrategy_UnmarshalTextRejectsUnknown(t *testing.T) {
	var s Strategy

	err := s.UnmarshalText([]byte("cheapest"))
	if err == nil {
		t.Fatal("UnmarshalText accepted cheapest")
	}

	if !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("error does not wrap ErrUnknownStrategy: %v", err)
	}
}
