// Package ptr converts values into pointers so that optional specification
// fields can be written inline.
//
// The job specification uses pointers for every optional scalar, because a
// value type cannot distinguish a field an author set to its zero value from
// one they omitted, and the two mean opposite things: attempts = 0 forbids
// retrying while an absent attempts takes the default. Go has no way to take
// the address of a literal, so constructing those values otherwise requires a
// named temporary per field.
//
// The same helper exists in Nomad as helper/pointer for the same reason.
package ptr
