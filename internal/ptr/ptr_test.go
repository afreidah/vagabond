// -------------------------------------------------------------------------------
// Pointer Helper Tests
//
// Author: Alex Freidah
//
// The distinction these helpers exist to preserve is unset against zero, so the
// tests that matter are the ones where the pointed-at value is the zero value.
// -------------------------------------------------------------------------------

package ptr

import "testing"

func TestOf(t *testing.T) {
	v := Of(42)
	if v == nil {
		t.Fatal("Of returned nil")
	}

	if *v != 42 {
		t.Errorf("*Of(42) = %d, want 42", *v)
	}
}

// A pointer to the zero value is not nil, which is the entire reason the
// specification uses pointers.
func TestOf_ZeroValueIsNotNil(t *testing.T) {
	if Of(0) == nil {
		t.Error("Of(0) returned nil")
	}

	if Of(false) == nil {
		t.Error("Of(false) returned nil")
	}

	if Of("") == nil {
		t.Error(`Of("") returned nil`)
	}
}

func TestDeref(t *testing.T) {
	if got := Deref(Of(7)); got != 7 {
		t.Errorf("Deref(Of(7)) = %d, want 7", got)
	}

	var nilInt *int
	if got := Deref(nilInt); got != 0 {
		t.Errorf("Deref(nil) = %d, want 0", got)
	}
}

func TestDerefOr(t *testing.T) {
	tests := []struct {
		name     string
		value    *int
		fallback int
		want     int
	}{
		{name: "set", value: Of(3), fallback: 99, want: 3},
		{name: "unset takes fallback", value: nil, fallback: 99, want: 99},
		{name: "explicit zero beats fallback", value: Of(0), fallback: 99, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DerefOr(tt.value, tt.fallback); got != tt.want {
				t.Errorf("DerefOr() = %d, want %d", got, tt.want)
			}
		})
	}
}

// An explicitly false bool must not silently take a true default. This is the
// case that a value-typed field would get wrong.
func TestDerefOr_ExplicitFalse(t *testing.T) {
	if got := DerefOr(Of(false), true); got {
		t.Error("DerefOr(Of(false), true) = true, want false")
	}

	var unset *bool
	if got := DerefOr(unset, true); !got {
		t.Error("DerefOr(nil, true) = false, want true")
	}
}
