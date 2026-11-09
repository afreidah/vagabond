// Package dispatch runs an admitted task: it submits to the selected
// provider, follows the execution to its end, collects what it produced, and
// moves to the next candidate when a provider fails to give an answer.
//
// Every attempt is recorded in the execution store before it is submitted and
// as it changes state, so a process that dies mid-run leaves a record of where
// it got to.
//
// A workload failure is an answer. A task that exits non-zero has reported
// something true, and running it elsewhere spends capacity to report the same
// thing, so only a provider that failed to produce a verdict is retried.
package dispatch
