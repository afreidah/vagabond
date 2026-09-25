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
// Synchronous. A Run call owns one job from admission to result. Every attempt
// is recorded in the execution store before it is submitted and as it changes
// state, so a process that dies mid-run leaves a record of where it got to.
//
// The one rule that shapes the rest: a workload failure is an answer. A test
// suite that exits non-zero has told us something true, and running it again
// somewhere else spends capacity to be told the same thing. Only a provider
// that failed to produce a verdict is worth another attempt.
// -------------------------------------------------------------------------------

package dispatch
