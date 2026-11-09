// Package quota holds each provider's usage budgets and what an execution
// charges against them.
//
// Budgets are declared entirely in configuration, in the units a provider
// itself meters. Nothing ships a default, because the number an operator writes
// encodes how much they are willing to spend on a backend: for most that is the
// free tier exactly, for some it is deliberately more. A provider declared with
// no budgets enforces nothing.
//
// The charges accumulate in internal/ledger, which survives a restart.
package quota
