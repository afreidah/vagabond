// -------------------------------------------------------------------------------
// Retry Policy
//
// Author: Alex Freidah
//
// Reading a task's retry block into the two numbers dispatch acts on: how many
// attempts it may make, and how long to wait between them.
// -------------------------------------------------------------------------------

package dispatch

import (
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Defaults for a task that declared no retry block: one attempt, since a task
// that said nothing about retrying asked for the work to be done once.
const (
	defaultAttempts       = 1
	defaultBackoffInitial = 5 * time.Second
	defaultBackoffMax     = 30 * time.Second
)

// -------------------------------------------------------------------------
// POLICY
// -------------------------------------------------------------------------

// policy is a task's retry block, resolved. attempts counts every attempt,
// the first included.
type policy struct {
	attempts int
	reroute  bool

	initial time.Duration
	max     time.Duration
}

// retryPolicy reads a task's retry block. An unparseable duration falls back
// to the default, since the job already passed validation and refusing the
// work over a backoff interval is the wrong trade.
func retryPolicy(task *job.Task) policy {
	p := policy{
		attempts: defaultAttempts,
		initial:  defaultBackoffInitial,
		max:      defaultBackoffMax,
	}

	if task.Retry == nil {
		return p
	}

	if task.Retry.Attempts != nil && *task.Retry.Attempts > 0 {
		p.attempts = *task.Retry.Attempts
	}

	if task.Retry.Reroute != nil {
		p.reroute = *task.Retry.Reroute
	}

	if task.Retry.Backoff == nil {
		return p
	}

	if d, ok := duration(task.Retry.Backoff.Initial); ok {
		p.initial = d
	}

	if d, ok := duration(task.Retry.Backoff.Max); ok {
		p.max = d
	}

	return p
}

// budget is how many submissions a task gets. Without reroute it gets one
// provider, the best one: retrying in place would spend capacity on a provider
// that just failed.
func (p policy) budget() int {
	if !p.reroute {
		return 1
	}

	return p.attempts
}

// backoff returns how long to wait before attempt n, counting from zero: none
// for the first, then doubling from initial up to max.
func (p policy) backoff(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}

	return doubling(p.initial, p.max, attempt-1)
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// duration converts a job duration, reporting whether it was usable.
func duration(d *job.Duration) (time.Duration, bool) {
	if d == nil {
		return 0, false
	}

	parsed, err := d.Std()
	if err != nil || parsed <= 0 {
		return 0, false
	}

	return parsed, true
}
