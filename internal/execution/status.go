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
// ProviderID is the provider's own identifier, carried alongside ID rather than
// replacing it. It is unique only within that provider, is assigned too late to
// be useful for idempotency, and is absent entirely when a submission fails
// before returning.
//
// The State field is exported so that persistence and JSON stay boring, but To
// is the only path that checks a change. Assigning State directly compiles and
// is visibly off the normal path.
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

// To moves the execution into next, or refuses and leaves it untouched.
//
// StartedAt and EndedAt are set here rather than by callers, because a status
// whose timestamps disagree with its state is worse than one missing them: the
// quota ledger reads both.
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

// ConsumedCapacity reports whether this execution should be charged against the
// provider's free-tier allowance.
//
// True once the execution reached running, which StartedAt records, and true
// while it is lost. A workload that ran and failed still burned the capacity it
// used, so the charge does not depend on the outcome. Work the provider
// accepted and then dropped before starting cost nothing and is not charged.
//
// A lost execution is charged because nothing is known about it, and the ledger
// errs toward over-counting: wasting free capacity is recoverable, and drifting
// toward paid capacity is the one failure this project exists to prevent.
func (s *Status) ConsumedCapacity() bool {
	return !s.StartedAt.IsZero() || s.State == StateLost
}

// LostExpired reports whether a lost execution has waited out the grace period
// and should now be recorded as failed.
//
// False for every other state, so a caller can ask this of any status without
// checking first.
func (s *Status) LostExpired(now time.Time) bool {
	if s.State != StateLost {
		return false
	}

	return now.Sub(s.UpdatedAt) >= LostGracePeriod
}
