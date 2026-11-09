// -------------------------------------------------------------------------------
// Execution Status
//
// Author: Alex Freidah
//
// Where an execution is now, in a shape that persists to a row and serializes
// to an API response without conversion. State changes go through To, which
// checks the transition table.
// -------------------------------------------------------------------------------

package execution

import "time"

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Status is the current state of one dispatched execution.
//
// ProviderID is the provider's own identifier, carried alongside ID. State is
// exported so persistence and JSON stay simple, but To is the only path that
// checks a change.
type Status struct {
	ID         ID
	State      State
	ProviderID string

	StartedAt time.Time // zero until running
	EndedAt   time.Time // zero until terminal
	UpdatedAt time.Time // when State last changed
}

// -------------------------------------------------------------------------
// TRANSITIONS
// -------------------------------------------------------------------------

// To moves the execution into next, or refuses and leaves it untouched. It sets
// StartedAt and EndedAt itself, since the quota ledger reads both.
func (s *Status) To(next State, now time.Time) error {
	if err := s.State.Transition(next); err != nil {
		return err
	}

	s.State = next
	s.UpdatedAt = now

	if next == StateRunning && s.StartedAt.IsZero() {
		s.StartedAt = now
	}

	if next.Terminal() {
		s.EndedAt = now
	}

	return nil
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// Terminal reports whether the execution has reached a state it never leaves.
func (s *Status) Terminal() bool {
	return s.State.Terminal()
}

// ConsumedCapacity reports whether the execution is charged: once it reached
// running, whatever the outcome, or while lost, since the ledger over-counts.
func (s *Status) ConsumedCapacity() bool {
	return !s.StartedAt.IsZero() || s.State == StateLost
}

// LostExpired reports whether a lost execution has waited out the grace period
// and should be recorded as failed. It is false for every other state.
func (s *Status) LostExpired(now time.Time) bool {
	if s.State != StateLost {
		return false
	}

	return now.Sub(s.UpdatedAt) >= LostGracePeriod
}
