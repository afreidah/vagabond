// Package postgres persists jobs, dispatches, executions and the quota ledger
// in Postgres or CockroachDB, behind the same interfaces the memory stores
// satisfy.
//
// Every write runs in a serializable transaction retried on conflict, because
// more than one process writes the same rows. A database that does not answer
// is reported as state.ErrUnavailable, so a caller can tell an outage from a
// refusal. A server refuses a schema version other than its own rather than
// write rows another version misreads.
package postgres
