// Package ledger keeps Vagabond's own account of what each provider's quota
// pools have been charged, since no provider reports remaining allowance in a
// form that can be scheduled against.
//
// An execution reserves its declared worst case before it is submitted and
// settles to what it cost once it ends. The store decides each reservation in
// one statement, so two processes charging the same pool cannot both see
// room. A reservation nobody settled stays charged until the reaper learns
// what became of the run: over-counting wastes allowance, under-counting
// spends money.
package ledger
