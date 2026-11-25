// -------------------------------------------------------------------------------
// Test Kit
//
// Author: Alex Freidah
//
// Helpers the tests of every package share: waiting for a condition that comes
// to hold, minting an execution ID, and a buffer a test reads while something
// else still writes it.
// -------------------------------------------------------------------------------

package testkit

import (
	"bytes"
	"sync"
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

// Buffer is a buffer that goroutines write while a test reads it, which a
// bytes.Buffer cannot take: a command's streams, or a logger's output.
type Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p under the lock.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
