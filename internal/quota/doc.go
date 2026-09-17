// Package quota models each provider's free-tier allowance as a ledger
// Vagabond maintains, not a number it reads back from the provider.
//
// No provider exposes remaining free-tier capacity in a form that can be
// scheduled against. Billing data lags by hours where it exists at all, so
// Vagabond keeps its own account of what it has spent and treats provider APIs
// as a reconciliation signal rather than as truth.
//
// Where the account is uncertain it over-counts. An execution that vanished is
// charged its full declared timeout until reconciliation proves otherwise.
// Over-counting wastes free capacity, which is recoverable; under-counting
// drifts toward spending money, which is the one failure this project exists to
// prevent.
//
// Chunk 1 defines only the snapshot admission reads. The ledger that produces
// it, and the periods it resets across, arrive with persistence.
package quota
