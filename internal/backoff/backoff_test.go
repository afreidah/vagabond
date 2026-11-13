// -------------------------------------------------------------------------------
// Backoff Tests
//
// Author: Alex Freidah
//
// The doubling schedule, its ceiling on every wait, and the sleep ending when
// its caller does.
// -------------------------------------------------------------------------------

package backoff

import (
	"context"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// EXPONENTIAL
// -------------------------------------------------------------------------

// The delay doubles from initial and holds at the ceiling.
func TestExponential_DoublesToTheCeiling(t *testing.T) {
	t.Parallel()

	want := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		15 * time.Second, // 16 would exceed the ceiling
		15 * time.Second,
	}

	for n, expected := range want {
		if got := Exponential(2*time.Second, 15*time.Second, n); got != expected {
			t.Errorf("Exponential(n=%d) = %s, want %s", n, got, expected)
		}
	}
}

// A ceiling below initial caps the first wait too.
func TestExponential_CapsTheFirstWait(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 5} {
		if got := Exponential(time.Minute, 30*time.Second, n); got != 30*time.Second {
			t.Errorf("Exponential(n=%d) = %s, want the 30s ceiling", n, got)
		}
	}
}

// Far past the ceiling the delay stays there rather than overflowing.
func TestExponential_ManyFailures(t *testing.T) {
	t.Parallel()

	if got := Exponential(time.Millisecond, time.Second, 1000); got != time.Second {
		t.Errorf("Exponential(n=1000) = %s, want the ceiling", got)
	}
}

// -------------------------------------------------------------------------
// SLEEP
// -------------------------------------------------------------------------

// A live context sleeps for at least the requested duration.
func TestSleep_Waits(t *testing.T) {
	t.Parallel()

	start := time.Now()

	if err := Sleep(t.Context(), 10*time.Millisecond); err != nil {
		t.Fatalf("Sleep() = %v", err)
	}

	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Errorf("returned after %s, want at least 10ms", elapsed)
	}
}

// A context that ends cuts the sleep short with its error, whatever the
// wait, a zero one included.
func TestSleep_EndsWithTheCaller(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for _, d := range []time.Duration{time.Hour, 0} {
		if err := Sleep(ctx, d); err == nil {
			t.Errorf("Sleep(%s) on an ended context = nil, want its error", d)
		}
	}

	if err := Sleep(t.Context(), 0); err != nil {
		t.Errorf("Sleep(0) on a live context = %v, want nil", err)
	}
}
