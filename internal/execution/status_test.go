// -------------------------------------------------------------------------------
// Execution Status and Result Tests
//
// Author: Alex Freidah
//
// To is the only checked path into a state change, so the tests cover both that
// it refuses illegal moves and that it leaves the status untouched when it
// does. A half-applied transition is worse than a refused one.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/ptr"
)

func newStatus(t *testing.T, state State) *Status {
	t.Helper()

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	return &Status{ID: id, State: state}
}

// -------------------------------------------------------------------------
// TRANSITIONS
// -------------------------------------------------------------------------

func TestStatus_To(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := newStatus(t, StatePending)

	if err := s.To(StateSubmitted, now); err != nil {
		t.Fatalf("pending to submitted refused: %v", err)
	}

	if s.State != StateSubmitted {
		t.Errorf("State = %q, want %q", s.State, StateSubmitted)
	}

	if !s.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", s.UpdatedAt, now)
	}
}

// A refused transition must change nothing. A status left half-updated is
// harder to diagnose than one that never moved.
func TestStatus_ToRefusedLeavesStatusUntouched(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := newStatus(t, StateSucceeded)

	err := s.To(StateRunning, now)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("error = %v, want ErrIllegalTransition", err)
	}

	if s.State != StateSucceeded {
		t.Errorf("State = %q after a refused transition, want %q", s.State, StateSucceeded)
	}

	if !s.UpdatedAt.IsZero() {
		t.Error("UpdatedAt was set by a refused transition")
	}
}

func TestStatus_ToSetsStartedAt(t *testing.T) {
	start := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := newStatus(t, StateSubmitted)

	if err := s.To(StateRunning, start); err != nil {
		t.Fatalf("submitted to running refused: %v", err)
	}

	if !s.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, start)
	}

	if !s.EndedAt.IsZero() {
		t.Error("EndedAt was set on a non-terminal transition")
	}
}

func TestStatus_ToSetsEndedAtOnTerminal(t *testing.T) {
	start := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)

	s := newStatus(t, StateSubmitted)
	if err := s.To(StateRunning, start); err != nil {
		t.Fatalf("submitted to running refused: %v", err)
	}

	if err := s.To(StateSucceeded, end); err != nil {
		t.Fatalf("running to succeeded refused: %v", err)
	}

	if !s.EndedAt.Equal(end) {
		t.Errorf("EndedAt = %v, want %v", s.EndedAt, end)
	}

	if !s.StartedAt.Equal(start) {
		t.Errorf("StartedAt moved to %v, want %v", s.StartedAt, start)
	}
}

// A synchronous invocation never passes through running, so a terminal status
// can legitimately have no StartedAt.
func TestStatus_SynchronousPathSkipsRunning(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := newStatus(t, StateSubmitted)

	if err := s.To(StateSucceeded, now); err != nil {
		t.Fatalf("submitted to succeeded refused: %v", err)
	}

	if !s.StartedAt.IsZero() {
		t.Error("StartedAt was set without passing through running")
	}

	if !s.Terminal() {
		t.Error("succeeded status does not report terminal")
	}
}

// -------------------------------------------------------------------------
// LOST GRACE PERIOD
// -------------------------------------------------------------------------

func TestStatus_LostExpired(t *testing.T) {
	lostAt := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "just lost", now: lostAt.Add(time.Minute), want: false},
		{name: "one hour short", now: lostAt.Add(LostGracePeriod - time.Hour), want: false},
		{name: "exactly at the period", now: lostAt.Add(LostGracePeriod), want: true},
		{name: "well past", now: lostAt.Add(72 * time.Hour), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Status{State: StateLost, UpdatedAt: lostAt}
			if got := s.LostExpired(tt.now); got != tt.want {
				t.Errorf("LostExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Asking any status is safe, so callers do not have to check the state first.
func TestStatus_LostExpiredIsFalseForOtherStates(t *testing.T) {
	long := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	for _, state := range States() {
		if state == StateLost {
			continue
		}

		s := &Status{State: state, UpdatedAt: long}
		if s.LostExpired(now) {
			t.Errorf("LostExpired() = true for state %q", state)
		}
	}
}

// An expired lost execution becomes failed, which the table has to permit or
// the grace period cannot be applied.
func TestStatus_ExpiredLostCanBecomeFailed(t *testing.T) {
	lostAt := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	now := lostAt.Add(LostGracePeriod)

	s := &Status{State: StateLost, UpdatedAt: lostAt}
	if !s.LostExpired(now) {
		t.Fatal("status did not report as expired")
	}

	if err := s.To(StateFailed, now); err != nil {
		t.Fatalf("lost to failed refused: %v", err)
	}
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

func TestResult_Succeeded(t *testing.T) {
	tests := []struct {
		name     string
		exitCode *int
		want     bool
	}{
		{name: "exit zero", exitCode: ptr.Of(0), want: true},
		{name: "exit one", exitCode: ptr.Of(1), want: false},
		{name: "exit 127", exitCode: ptr.Of(127), want: false},
		{name: "no exit code at all", exitCode: nil, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Result{ExitCode: tt.exitCode}
			if got := r.Succeeded(); got != tt.want {
				t.Errorf("Succeeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Nil means the family has no exit status, not that the task exited zero. A
// worker execution reaching a result at all is its success.
func TestResult_NilExitCodeIsNotZero(t *testing.T) {
	worker := &Result{ExitCode: nil}
	explicit := &Result{ExitCode: ptr.Of(0)}

	if !worker.Succeeded() || !explicit.Succeeded() {
		t.Fatal("both should report success")
	}

	if worker.ExitCode != nil {
		t.Error("a worker result carries an exit code")
	}
}

// -------------------------------------------------------------------------
// CAPACITY ACCOUNTING
// -------------------------------------------------------------------------

// mustTo applies a transition or fails the test, so that building a status in a
// particular state stays a single expression.
func mustTo(t *testing.T, s *Status, next State, at time.Time) *Status {
	t.Helper()

	if err := s.To(next, at); err != nil {
		t.Fatalf("%q to %q refused: %v", s.State, next, err)
	}

	return s
}

// Capacity is charged from running onward, or while lost. This is the rule the
// quota ledger reads, and it is derivable from the status alone.
func TestStatus_ConsumedCapacity(t *testing.T) {
	start := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)

	tests := []struct {
		name   string
		status *Status
		want   bool
	}{
		{
			name:   "never submitted",
			status: newStatus(t, StatePending),
		},
		{
			name:   "submitted, outcome unknown",
			status: newStatus(t, StateSubmitted),
		},
		{
			name:   "accepted but never started",
			status: newStatus(t, StateAccepted),
		},
		{
			name:   "accepted then dropped before starting",
			status: mustTo(t, newStatus(t, StateAccepted), StateFailed, start),
		},
		{
			name:   "ran and succeeded",
			status: mustTo(t, mustTo(t, newStatus(t, StateAccepted), StateRunning, start), StateSucceeded, end),
			want:   true,
		},
		{
			name:   "ran and failed still burned capacity",
			status: mustTo(t, mustTo(t, newStatus(t, StateAccepted), StateRunning, start), StateFailed, end),
			want:   true,
		},
		{
			name:   "lost before starting is charged anyway",
			status: mustTo(t, newStatus(t, StateAccepted), StateLost, start),
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.ConsumedCapacity(); got != tt.want {
				t.Errorf("ConsumedCapacity() = %v, want %v (state %q)", got, tt.want, tt.status.State)
			}
		})
	}
}
