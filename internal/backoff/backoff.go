// -------------------------------------------------------------------------------
// Backoff
//
// Author: Alex Freidah
//
// The doubling delay and the context-aware sleep the retry loops share. A loop
// counts its consecutive failures and asks Exponential how long the next wait
// is; Sleep waits it out unless the caller gives up first.
// -------------------------------------------------------------------------------

package backoff

import (
	"context"
	"time"
)

// Exponential is initial doubled n times, never more than ceiling, the first
// wait included, so a ceiling below initial holds from the start.
func Exponential(initial, ceiling time.Duration, n int) time.Duration {
	wait := min(initial, ceiling)

	for range n {
		if wait >= ceiling {
			break
		}

		wait = min(2*wait, ceiling)
	}

	return wait
}

// Sleep waits d, or returns ctx's error as soon as ctx ends. A wait of zero
// or less returns at once, with ctx's error if it has already ended.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
