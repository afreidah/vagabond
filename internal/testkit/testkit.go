// -------------------------------------------------------------------------------
// Test Kit
//
// Author: Alex Freidah
//
// Helpers the tests of every package share: waiting for a condition that comes
// to hold, and minting an execution ID.
// -------------------------------------------------------------------------------

package testkit

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// poll is how often Eventually checks its condition.
const poll = 20 * time.Millisecond

// Eventually checks cond every poll until it holds, failing the test with what
// it waited for once timeout has passed.
func Eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()

	// Checked once more past the deadline, so a slow last check still counts.
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}

		time.Sleep(poll)
	}
}

// NewID mints an execution ID, failing the test if it cannot.
func NewID(t testing.TB) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("execution.NewID() = %v", err)
	}

	return id
}
