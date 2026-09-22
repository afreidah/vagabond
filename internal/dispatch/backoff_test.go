// -------------------------------------------------------------------------------
// Backoff Tests
//
// Author: Alex Freidah
//
// Two separate schedules that are easy to confuse: poll intervals govern how
// often a running execution is asked about, retry backoff governs the pause
// before trying a different provider.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// POLLING
// -------------------------------------------------------------------------

func TestPollIntervalDoublesToACeiling(t *testing.T) {
	t.Parallel()

	p := Poll{Initial: 2 * time.Second, Max: 15 * time.Second}

	want := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		15 * time.Second, // 16 would exceed the ceiling
		15 * time.Second,
	}

	for n, expected := range want {
		if got := p.interval(n); got != expected {
			t.Errorf("interval(%d) = %s, want %s", n, got, expected)
		}
	}
}

// A ceiling below the first interval is still a ceiling.
func TestPollIntervalRespectsALowCeiling(t *testing.T) {
	t.Parallel()

	p := Poll{Initial: 10 * time.Second, Max: 3 * time.Second}

	if got := p.interval(1); got != 3*time.Second {
		t.Errorf("interval(1) = %s, want the ceiling", got)
	}
}

// -------------------------------------------------------------------------
// RETRY BACKOFF
// -------------------------------------------------------------------------

func TestRetryBackoff(t *testing.T) {
	t.Parallel()

	p := policy{initial: 5 * time.Second, max: 30 * time.Second}

	want := map[int]time.Duration{
		0: 0, // the first attempt never waits
		1: 5 * time.Second,
		2: 10 * time.Second,
		3: 20 * time.Second,
		4: 30 * time.Second,
		5: 30 * time.Second,
	}

	for attempt, expected := range want {
		if got := p.backoff(attempt); got != expected {
			t.Errorf("backoff(%d) = %s, want %s", attempt, got, expected)
		}
	}
}

// -------------------------------------------------------------------------
// POLICY
// -------------------------------------------------------------------------

func TestRetryPolicyDefaults(t *testing.T) {
	t.Parallel()

	p := retryPolicy(&job.Task{Name: "test"})

	if p.attempts != 1 {
		t.Errorf("attempts = %d, want 1", p.attempts)
	}

	if p.reroute {
		t.Error("reroute defaulted to true")
	}

	if p.initial != defaultBackoffInitial || p.max != defaultBackoffMax {
		t.Errorf("backoff = %s/%s, want the defaults", p.initial, p.max)
	}
}

func TestRetryPolicyReadsTheBlock(t *testing.T) {
	t.Parallel()

	p := retryPolicy(&job.Task{
		Name: "test",
		Retry: &job.Retry{
			Attempts: ptr.Of(3),
			Reroute:  ptr.Of(true),
			Backoff: &job.Backoff{
				Initial: ptr.Of(job.Duration("1s")),
				Max:     ptr.Of(job.Duration("4s")),
			},
		},
	})

	if p.attempts != 3 || !p.reroute {
		t.Errorf("policy = %+v", p)
	}

	if p.initial != time.Second || p.max != 4*time.Second {
		t.Errorf("backoff = %s/%s, want 1s/4s", p.initial, p.max)
	}
}

// A job already passed validation, so a bad duration here means something
// upstream changed. Refusing to run the work over a backoff interval would be
// the wrong trade.
func TestRetryPolicyIgnoresUnusableDurations(t *testing.T) {
	t.Parallel()

	p := retryPolicy(&job.Task{
		Name: "test",
		Retry: &job.Retry{
			Backoff: &job.Backoff{
				Initial: ptr.Of(job.Duration("not a duration")),
				Max:     ptr.Of(job.Duration("0s")),
			},
		},
	})

	if p.initial != defaultBackoffInitial || p.max != defaultBackoffMax {
		t.Errorf("backoff = %s/%s, want the defaults", p.initial, p.max)
	}
}

// Zero attempts is not a request to do nothing; it is a field nobody set
// meaningfully.
func TestRetryPolicyIgnoresZeroAttempts(t *testing.T) {
	t.Parallel()

	p := retryPolicy(&job.Task{
		Name:  "test",
		Retry: &job.Retry{Attempts: ptr.Of(0)},
	})

	if p.attempts != 1 {
		t.Errorf("attempts = %d, want 1", p.attempts)
	}
}

// -------------------------------------------------------------------------
// SLEEPING
// -------------------------------------------------------------------------

func TestSleepContextReturnsWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := sleepContext(ctx, time.Hour); err == nil {
		t.Error("a cancelled context slept anyway")
	}
}

func TestSleepContextWaits(t *testing.T) {
	t.Parallel()

	start := time.Now()

	if err := sleepContext(t.Context(), 10*time.Millisecond); err != nil {
		t.Fatalf("sleepContext failed: %v", err)
	}

	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Errorf("returned after %s, want at least 10ms", elapsed)
	}
}
