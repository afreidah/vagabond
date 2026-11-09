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
func Deref[T any](v *T) T {
	if v == nil {
		var zero T

		return zero
	}

	return *v
}

// DerefOr returns the value v points at, or fallback if v is nil, for a field
// whose documented default differs from the type's zero.
func DerefOr[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}

	return *v
}
