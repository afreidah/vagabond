// Package quota holds each provider's usage budgets and the account of what has
// been spent against them.
//
// Budgets are declared entirely in configuration, in the units a provider
// itself meters. Nothing ships a default, because the number an operator writes
// encodes how much they are willing to spend on a backend: for most that is the
// free tier exactly, for some it is deliberately more. A provider declared with
// no budgets enforces nothing.
//
// The account is Vagabond's own. No provider exposes remaining capacity in a
// form that can be scheduled against, and billing data lags by hours where it
// exists at all, so provider APIs are a reconciliation signal rather than truth.
//
// Where the account is uncertain it over-counts. Over-counting wastes free
// capacity, which is recoverable; under-counting drifts toward spending money.
//
// This package defines the budgets and what an execution charges against them.
// The ledger that accumulates those charges and survives a restart is separate.
package quota
