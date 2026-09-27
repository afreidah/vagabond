// -------------------------------------------------------------------------------
// Pointer Helpers
//
// Author: Alex Freidah
//
// Reading optional specification fields, which are pointers so that unset is
// distinguishable from zero. Setting one is new(value).
// -------------------------------------------------------------------------------

package ptr

// Deref returns the value v points at, or zero if v is nil.
//
// Reading an optional field is the mirror of setting one, and a nil check at
// every read site is the cost of the pointers that make unset distinguishable.
func Deref[T any](v *T) T {
	if v == nil {
		var zero T

		return zero
	}

	return *v
}

// DerefOr returns the value v points at, or fallback if v is nil.
//
// This is the common case for specification fields: an absent value takes a
// documented default rather than the type's zero, and the two are rarely the
// same. An absent retry attempts count means the default, not zero attempts.
func DerefOr[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}

	return *v
}
