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

// Cost is a monetary amount as an integer. The unit is unpinned, because the
// only policy implemented is max_cost_usd = 0 and every value is zero.
type Cost int64

// Free reports whether the amount is zero.
func (c Cost) Free() bool {
	return c == 0
}
