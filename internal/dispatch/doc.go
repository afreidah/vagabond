// -------------------------------------------------------------------------------
// Dispatch - running an admitted task somewhere
//
// Author: Alex Freidah
//
// Everything before this package decides where work should go. This is what
// takes it there: submit to the selected provider, watch until it finishes,
// collect what it produced, and move to another provider when the first one
// fails to give an answer.
//
// Synchronous and stateless. A Run call owns one job from admission to result
// and holds the execution in memory, so a process that dies mid-run loses it.
// That is the honest shape until there is somewhere durable to write, and a
// half-ledger living here would only have to be unpicked when the real one
// lands.
//
// The one rule that shapes the rest: a workload failure is an answer. A test
// suite that exits non-zero has told us something true, and running it again
// somewhere else spends capacity to be told the same thing. Only a provider
// that failed to produce a verdict is worth another attempt.
// -------------------------------------------------------------------------------

package dispatch
