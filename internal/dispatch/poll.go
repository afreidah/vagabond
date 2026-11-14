// -------------------------------------------------------------------------------
// Polling and Backoff
//
// Author: Alex Freidah
//
// How often a running execution is asked about, how long one provider call
// may take, how long a failed store write waits before it is tried again, and
// the submission call itself. Status is polled on a backoff because a
// container can spend minutes provisioning, and a fixed short interval spends
// API calls learning nothing.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/afreidah/vagabond/internal/backoff"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// SCHEDULE
// -------------------------------------------------------------------------

// Poll is how often a running execution is asked about: from Initial,
// doubling up to Max.
type Poll struct {
	Initial time.Duration
	Max     time.Duration
}

// callTimeout bounds one call about an execution already submitted, so a
// platform that stops responding fails the call rather than holding the
// dispatch open. Submit is not bounded: a synchronous provider answers it
// only once the task is done.
const callTimeout = 30 * time.Second

// How long a store write that failed waits before it is made again, doubling
// up to the maximum while the store stays unreachable.
const (
	persistRetry    = 250 * time.Millisecond
	persistRetryMax = 10 * time.Second
)

// DefaultPoll starts at two seconds, for an invocation that finishes almost at
// once, and settles at fifteen for a container that runs for minutes.
var DefaultPoll = Poll{
	Initial: 2 * time.Second,
	Max:     15 * time.Second,
}

// interval returns how long to wait before the nth status call, counting from
// zero.
func (p Poll) interval(n int) time.Duration {
	return backoff.Exponential(p.Initial, p.Max, n)
}

// bounded makes call on a context that ends after callTimeout. A call that
// ran out of time is a provider that failed to answer, so it is classified
// infrastructure; one whose caller gave up keeps the caller's error.
func bounded[T any](ctx context.Context, call func(ctx context.Context) (T, error)) (T, error) {
	bctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	out, err := call(bctx)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		err = plugin.Infrastructure(err)
	}

	return out, err
}

// boundedErr is bounded for a call that returns only an error.
func boundedErr(ctx context.Context, call func(ctx context.Context) error) error {
	_, err := bounded(ctx, func(ctx context.Context) (struct{}, error) { return struct{}{}, call(ctx) })

	return err
}

// -------------------------------------------------------------------------
// SUBMISSION
// -------------------------------------------------------------------------

// submit hands the task to provider. The execution ID is the submission's
// idempotency key, so retrying it on a refreshed credential is safe.
func (d *Dispatcher) submit(
	ctx context.Context, provider plugin.Provider, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	return authorized(ctx, d, provider, func() (plugin.Submission, error) {
		return provider.Submit(ctx, id, task)
	})
}
