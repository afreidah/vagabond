// -------------------------------------------------------------------------------
// Cost - monetary amounts as exact integers
//
// Author: Alex Freidah
//
// The only question asked of this type is whether a job would spend anything.
// An exact integer comparison against zero cannot drift the way accumulated
// floating point can, which is the whole reason the type exists.
// -------------------------------------------------------------------------------

package job

// Cost is a monetary amount as an integer.
//
// The unit is deliberately not pinned yet. Every value in phase 1 is zero,
// because max_cost_usd = 0 is the only policy Vagabond implements, and the right
// granularity depends on how providers actually meter: Lambda bills per
// GB-second at a rate finer than millionths of a dollar. Pinning the unit before
// a paid path exists would be guessing.
type Cost int64

// Free reports whether the amount is zero.
func (c Cost) Free() bool {
	return c == 0
}
