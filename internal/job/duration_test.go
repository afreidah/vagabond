// -------------------------------------------------------------------------------
// Duration Tests
//
// Author: Alex Freidah
//
// Negative values get explicit coverage because they parse cleanly as Go
// durations and are meaningless as a timeout or a backoff, which makes them the
// failure the standard library will not catch.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Duration
	}{
		{name: "minutes", input: "15m", want: 15 * time.Minute},
		{name: "seconds", input: "5s", want: 5 * time.Second},
		{name: "compound", input: "1h30m", want: 90 * time.Minute},
		{name: "zero", input: "0s", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if err != nil {
				t.Fatalf("ParseDuration(%q) returned unexpected error: %v", tt.input, err)
			}

			if got.Std() != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got.Std(), tt.want)
			}
		})
	}
}

func TestParseDuration_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "no unit", input: "15"},
		{name: "nonsense", input: "soon"},
		{name: "negative", input: "-5s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if err == nil {
				t.Fatalf("ParseDuration(%q) = %v, want error", tt.input, got)
			}

			if !errors.Is(err, ErrInvalidDuration) {
				t.Errorf("ParseDuration(%q) error does not wrap ErrInvalidDuration: %v", tt.input, err)
			}
		})
	}
}

func TestDuration_TextRoundTrip(t *testing.T) {
	for _, input := range []string{"15m", "5s", "30s", "1h0m0s"} {
		t.Run(input, func(t *testing.T) {
			original, err := ParseDuration(input)
			if err != nil {
				t.Fatalf("ParseDuration(%q) returned unexpected error: %v", input, err)
			}

			encoded, err := original.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Duration
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != original {
				t.Errorf("round trip produced %v, want %v", got, original)
			}
		})
	}
}

func TestDuration_UnmarshalTextRejectsInvalid(t *testing.T) {
	var d Duration

	err := d.UnmarshalText([]byte("-5s"))
	if err == nil {
		t.Fatal("UnmarshalText accepted a negative duration")
	}

	if !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("error does not wrap ErrInvalidDuration: %v", err)
	}
}
