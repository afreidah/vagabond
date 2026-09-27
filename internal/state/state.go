// -------------------------------------------------------------------------------
// Store Errors
//
// Author: Alex Freidah
//
// What any store reports that its callers act on without knowing which store
// it is. Here rather than in a store package, so the server can map it to a
// status without importing Postgres.
// -------------------------------------------------------------------------------

package state

import "errors"

// ErrUnavailable reports a store that could not be reached. Nothing was
// written; the same call may succeed once the store is back.
var ErrUnavailable = errors.New("the store is unreachable")
