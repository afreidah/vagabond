// -------------------------------------------------------------------------------
// Pointer Helpers
//
// Author: Alex Freidah
//
// Of takes the address of a value of any type, which Go does not permit for a
// literal. Optional specification fields are pointers so that unset is
// distinguishable from zero, and without this every such field would need a
// named temporary at every construction site.
// -------------------------------------------------------------------------------

package ptr

// Of returns a pointer to v.
func Of[T any](v T) *T {
	return &v
}

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
