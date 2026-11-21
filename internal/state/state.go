// -------------------------------------------------------------------------------
// Store Errors and Records
//
// Author: Alex Freidah
//
// Errors and records that both stores return, Postgres and memory. They live
// here so the server and admission can use them without importing either
// store.
// -------------------------------------------------------------------------------

package state

import (
	"errors"
	"time"
)

// ErrUnavailable reports a store that could not be reached. Nothing was
// written; the same call may succeed once the store is back.
var ErrUnavailable = errors.New("the store is unreachable")

// Disabled records that an operator turned a provider off through the API. At
// is when. By is the common name on the client certificate they used, or
// empty when the API does not require one.
type Disabled struct {
	At time.Time
	By string
}
