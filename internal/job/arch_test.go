// -------------------------------------------------------------------------------
// Architecture Tests
//
// Author: Alex Freidah
//
// Covers the spellings a person actually writes. x86_64 and aarch64 are the
// names the rest of the toolchain uses for the same processors, so they are the
// most likely wrong answers and must be rejected with a message that shows the
// right ones.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"strings"
	"testing"
)

func TestArch_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input Arch
		want  bool
	}{
		{name: "amd64", input: ArchAMD64, want: true},
		{name: "arm64", input: ArchARM64, want: true},
		{name: "empty", input: "", want: false},
		{name: "x86_64 is not the spelling", input: "x86_64", want: false},
		{name: "aarch64 is not the spelling", input: "aarch64", want: false},
		{name: "case variant", input: "AMD64", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Arch(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseArch(t *testing.T) {
	for _, want := range Arches() {
		t.Run(want.String(), func(t *testing.T) {
			got, err := ParseArch(want.String())
			if err != nil {
				t.Fatalf("ParseArch(%q) returned unexpected error: %v", want, err)
			}

			if got != want {
				t.Errorf("ParseArch(%q) = %q, want %q", want, got, want)
			}
		})
	}
}

func TestParseArch_Invalid(t *testing.T) {
	for _, input := range []string{"", "x86_64", "riscv64", "Arm64"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseArch(input)
			if err == nil {
				t.Fatalf("ParseArch(%q) = %q, want error", input, got)
			}

			if !errors.Is(err, ErrUnknownArch) {
				t.Errorf("ParseArch(%q) error does not wrap ErrUnknownArch: %v", input, err)
			}
		})
	}
}

func TestArch_ErrorListsVocabulary(t *testing.T) {
	_, err := ParseArch("x86_64")
	if err == nil {
		t.Fatal("ParseArch(\"x86_64\") returned no error")
	}

	for _, a := range Arches() {
		if !strings.Contains(err.Error(), a.String()) {
			t.Errorf("error %q omits valid architecture %q", err, a)
		}
	}
}

func TestArch_MarshalTextRejectsZeroValue(t *testing.T) {
	var a Arch

	if _, err := a.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value Arch")
	}
}

func TestArches_ReturnsCopy(t *testing.T) {
	first := Arches()
	first[0] = "mutated"

	if Arches()[0] == "mutated" {
		t.Error("Arches() exposed the package-level vocabulary to mutation")
	}
}

func TestArch_TextRoundTrip(t *testing.T) {
	for _, want := range Arches() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got Arch
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != want {
				t.Errorf("round trip produced %q, want %q", got, want)
			}
		})
	}
}

func TestArch_UnmarshalTextRejectsUnknown(t *testing.T) {
	var a Arch

	err := a.UnmarshalText([]byte("x86_64"))
	if err == nil {
		t.Fatal("UnmarshalText accepted x86_64")
	}

	if !errors.Is(err, ErrUnknownArch) {
		t.Errorf("error does not wrap ErrUnknownArch: %v", err)
	}
}
