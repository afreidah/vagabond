// -------------------------------------------------------------------------------
// Execution State
//
// Author: Alex Freidah
//
// One state machine across three execution families: an asynchronous batch job,
// a synchronous function invocation, and an HTTP call to an edge worker. Only
// the first can go missing, which is why StateLost exists. The machine is
// acyclic: anything that waits or spawns is a separate entity, so a transition
// table stays the whole implementation.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// LostGracePeriod is how long a lost execution stays open for reconciliation
// before it is recorded as failed, so lost rows are not charged forever. A day
// outlasts any dispatched task and most provider log retention.
const LostGracePeriod = 24 * time.Hour

// -------------------------------------------------------------------------
// STATES
// -------------------------------------------------------------------------

// State is where an execution has reached in its lifecycle.
type State string

// The states an execution passes through.
//
// StatePending is recorded before Submit is called, so a crash in between is
// recoverable. StateAccepted means the provider queued the work, not started
// it; capacity is consumed from StateRunning, so work dropped before starting
// costs nothing. A synchronous invocation skips both middle states.
//
// StateLost is an execution that stopped answering. It is not terminal, since
// reconciliation may still learn what happened, and not failed, since it may
// still be consuming capacity. A task past its timeout is failed, with the
// failure classification saying why.
const (
	StatePending   State = "pending"
	StateSubmitted State = "submitted"
	StateAccepted  State = "accepted"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateLost      State = "lost"
)

var stateNames = []State{
	StatePending,
	StateSubmitted,
	StateAccepted,
	StateRunning,
	StateSucceeded,
	StateFailed,
	StateCancelled,
	StateLost,
}

// terminalStates are the states an execution never leaves. StateLost is absent:
// the grace period is what turns it into an answer.
var terminalStates = []State{StateSucceeded, StateFailed, StateCancelled}

// legalTransitions is the state machine, written out so it can be read.
var legalTransitions = map[State][]State{
	StatePending:   {StateSubmitted, StateFailed, StateCancelled},
	StateSubmitted: {StateAccepted, StateRunning, StateSucceeded, StateFailed, StateCancelled, StateLost},
	StateAccepted:  {StateRunning, StateSucceeded, StateFailed, StateCancelled, StateLost},
	StateRunning:   {StateSucceeded, StateFailed, StateCancelled, StateLost},
	StateLost:      {StateSucceeded, StateFailed, StateCancelled},
	StateSucceeded: nil,
	StateFailed:    nil,
	StateCancelled: nil,
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrUnknownState is the sentinel behind every unrecognized state name.
var ErrUnknownState = errors.New("unknown execution state")

// ErrIllegalTransition is the sentinel behind every rejected state change.
var ErrIllegalTransition = errors.New("illegal state transition")

// -------------------------------------------------------------------------
// VOCABULARY
// -------------------------------------------------------------------------

// States returns the valid states in lifecycle order, as a copy.
func States() []State {
	return slices.Clone(stateNames)
}

// Valid reports whether s is a state Vagabond records.
func (s State) Valid() bool {
	return slices.Contains(stateNames, s)
}

// String returns the state as it is persisted and reported.
func (s State) String() string {
	return string(s)
}

// Terminal reports whether an execution in this state never changes again.
func (s State) Terminal() bool {
	return slices.Contains(terminalStates, s)
}

// ParseState converts in into a State, or reports that it is not one.
func ParseState(in string) (State, error) {
	s := State(in)
	if !s.Valid() {
		return "", unknownState(in)
	}

	return s, nil
}

// MarshalText implements encoding.TextMarshaler.
func (s State) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, unknownState(string(s))
	}

	return []byte(s), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *State) UnmarshalText(text []byte) error {
	parsed, err := ParseState(string(text))
	if err != nil {
		return err
	}

	*s = parsed

	return nil
}

// unknownState builds the ErrUnknownState failure for name, listing the valid
// set.
func unknownState(name string) error {
	valid := make([]string, 0, len(stateNames))
	for _, s := range stateNames {
		valid = append(valid, string(s))
	}

	return fmt.Errorf("%w %q: valid states are %s",
		ErrUnknownState, name, strings.Join(valid, ", "))
}

// -------------------------------------------------------------------------
// TRANSITIONS
// -------------------------------------------------------------------------

// CanTransition reports whether an execution may move from s to next.
func (s State) CanTransition(next State) bool {
	return slices.Contains(legalTransitions[s], next)
}

// Transition checks a state change, naming both states in a refusal so the
// caller that believed the execution was elsewhere can be found.
func (s State) Transition(next State) error {
	if !s.Valid() {
		return unknownState(string(s))
	}

	if !next.Valid() {
		return unknownState(string(next))
	}

	if !s.CanTransition(next) {
		return fmt.Errorf("%w: %s to %s", ErrIllegalTransition, s, next)
	}

	return nil
}
