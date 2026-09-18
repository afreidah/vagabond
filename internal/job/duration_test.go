// -------------------------------------------------------------------------------
// Duration Tests
//
// Author: Alex Freidah
//
// The value is text until something proves it parses, so the cases that matter
// are the ones where it does not: a negative span, which parses cleanly as a Go
// duration and is meaningless as a timeout, and a bare number, which is the
// spelling a person writes before learning the unit is required.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"testing"
	"time"
)

func TestDuration_Std(t *testing.T) {
	tests := []struct {
		name  string
		input Duration
		want  time.Duration
	}{
		{name: "minutes", input: "15m", want: 15 * time.Minute},
		{name: "seconds", input: "5s", want: 5 * time.Second},
		{name: "compound", input: "1h30m", want: 90 * time.Minute},
		{name: "zero", input: "0s", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.input.Std()
			if err != nil {
				t.Fatalf("Std() returned unexpected error: %v", err)
			}

			if got != tt.want {
				t.Errorf("Std() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDuration_StdInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input Duration
	}{
		{name: "empty", input: ""},
		{name: "no unit", input: "15"},
		{name: "nonsense", input: "soon"},
		{name: "negative", input: "-5s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.input.Std()
			if err == nil {
				t.Fatalf("Std() = %v, want error", got)
			}

			if !errors.Is(err, ErrInvalidDuration) {
				t.Errorf("error does not wrap ErrInvalidDuration: %v", err)
			}

			if tt.input.Valid() {
				t.Error("Valid() reports true for a value that does not parse")
			}
		})
	}
}

// FromDuration is how a specification is built in Go rather than read from a
// file, so it has to produce something the parser would accept back.
func TestFromDuration_RoundTrips(t *testing.T) {
	for _, want := range []time.Duration{15 * time.Minute, 5 * time.Second, 90 * time.Minute} {
		t.Run(want.String(), func(t *testing.T) {
			got, err := FromDuration(want).Std()
			if err != nil {
				t.Fatalf("Std() returned unexpected error: %v", err)
			}

			if got != want {
				t.Errorf("round trip produced %v, want %v", got, want)
			}
		})
	}
}

func TestDuration_TextRoundTrip(t *testing.T) {
	for _, input := range []Duration{"15m", "5s", "30s", "1h0m0s"} {
		t.Run(input.String(), func(t *testing.T) {
			encoded, err := input.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Duration
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != input {
				t.Errorf("round trip produced %q, want %q", got, input)
			}
		})
	}
}

// JSON and database columns go through these, so an unparseable value must not
// enter the model that way. HCL is deliberately the exception, and validation
// is what covers it.
func TestDuration_TextEncodingRejectsInvalid(t *testing.T) {
	var d Duration

	if err := d.UnmarshalText([]byte("-5s")); !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("UnmarshalText error = %v, want ErrInvalidDuration", err)
	}

	if _, err := Duration("soon").MarshalText(); !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("MarshalText error = %v, want ErrInvalidDuration", err)
	}
}
