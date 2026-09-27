// Package ptr reads optional specification fields.
//
// The job specification uses pointers for every optional scalar, because a
// value type cannot distinguish a field an author set to its zero value from
// one they omitted, and the two mean opposite things: attempts = 0 forbids
// retrying while an absent attempts takes the default. Deref and DerefOr read
// them back; new(value) sets one.
package ptr
