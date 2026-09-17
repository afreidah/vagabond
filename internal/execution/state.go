// -------------------------------------------------------------------------------
// Execution State
//
// Author: Alex Freidah
//
// One state machine across three execution families: an asynchronous batch job,
// a synchronous function invocation, and an HTTP call to an edge worker. Only
// the first can go missing, which is why StateLost exists.
//
// The machine is acyclic. Anything that waits or spawns becomes a separate
// entity rather than a state here, which is how Nomad models periodic jobs,
// retries, and blocked work. Keeping that property is what lets a transition
// table stay the whole implementation.
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
// before it is recorded as failed.
//
// Without a bound, lost rows accumulate forever and each one is charged its
// full declared timeout against the free-tier ledger permanently. A day is
// longer than any task Vagabond dispatches and longer than most provider log
// retention, so an execution unresolved by then is one nothing further will be
// learned about.
const LostGracePeriod = 24 * time.Hour

// -------------------------------------------------------------------------
// STATES
// -------------------------------------------------------------------------

// State is where an execution has reached in its lifecycle.
type State string

// The states an execution passes through.
//
// StatePending is recorded before Submit is called, so a crash in between is
// recoverable. StateSubmitted means the call was made and its outcome is not
// yet known; StateAccepted means the provider acknowledged the work and queued
// it, which is not the same as starting it.
//
// That distinction is what makes quota derivable. Capacity is consumed once an
// execution reaches StateRunning, so a provider that accepts work and then
// drops it before starting has cost nothing, and the ledger can tell that from
// the status alone without consulting an error.
//
// Both middle states are skippable: a synchronous invocation goes from
// submitted straight to a terminal state, while a container job passes through
// them as Status is polled.
//
// StateLost is an execution that was submitted and then stopped answering. It
// is not terminal, because reconciliation may still learn what happened, and it
// is not failed, because it may still be running and consuming capacity that
// has to be accounted for.
//
// There is no timed-out state. A task that ran past its own timeout failed, and
// why it failed is carried by the failure classification rather than by a
// second terminal state meaning nearly the same thing.
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

// terminalStates are the states an execution never leaves. StateLost is
// deliberately absent: it is an open question, and the grace period is what
// turns it into an answer.
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

// States returns the valid states in lifecycle order.
//
// The returned slice is a copy, so a caller rendering it cannot reorder the
// vocabulary for everyone else.
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

// Terminal reports whether an execution in this state will never change again.
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

// Transition checks a state change and explains a refusal.
//
// The error names both states, because the useful question when this fires is
// which caller believed the execution was somewhere it was not.
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
