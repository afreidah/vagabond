// Package execution holds the identity and lifecycle of a dispatched task.
//
// The identity is Vagabond's, not the provider's. An ID is minted and recorded
// durably before Submit is called, so a crash between dispatching and recording
// leaves a row to reconcile against rather than an orphaned run on someone
// else's free tier. The same ID is the submission idempotency key and the
// subject of the token the bootstrap presents when it reports a result.
//
// The state machine is one shape across three genuinely different execution
// families: an asynchronous batch job, a synchronous function invocation, and
// an HTTP call to an edge worker. Only the first can go missing, which is why
// StateLost exists and why it is not terminal.
package execution
