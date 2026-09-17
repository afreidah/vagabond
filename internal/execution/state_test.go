// -------------------------------------------------------------------------------
// Execution State Tests
//
// Author: Alex Freidah
//
// The transition table is the whole implementation, so it gets exhaustive
// coverage: every pair of states is checked against what the table says, and
// the properties that must hold regardless of the table are asserted
// separately.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// VOCABULARY
// -------------------------------------------------------------------------

func TestState_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input State
		want  bool
	}{
		{name: "pending", input: StatePending, want: true},
		{name: "lost", input: StateLost, want: true},
		{name: "empty", input: "", want: false},
		{name: "retired timed-out state", input: "timed-out", want: false},
		{name: "case variant", input: "Running", want: false},
		{name: "nomad spelling", input: "complete", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("State(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestState_Terminal(t *testing.T) {
	terminal := map[State]bool{
		StateSucceeded: true,
		StateFailed:    true,
		StateCancelled: true,
		StatePending:   false,
		StateSubmitted: false,
		StateRunning:   false,
		StateLost:      false,
	}

	for state, want := range terminal {
		t.Run(state.String(), func(t *testing.T) {
			if got := state.Terminal(); got != want {
				t.Errorf("%q.Terminal() = %v, want %v", state, got, want)
			}
		})
	}
}

// Lost is an open question, not an answer. If it ever becomes terminal the
// grace period stops meaning anything and reconciliation can never resolve it.
func TestState_LostIsNotTerminal(t *testing.T) {
	if StateLost.Terminal() {
		t.Error("StateLost is terminal, which makes the grace period unreachable")
	}
}

// -------------------------------------------------------------------------
// TRANSITIONS
// -------------------------------------------------------------------------

func TestState_TransitionLegal(t *testing.T) {
	legal := []struct {
		from State
		to   State
	}{
		{StatePending, StateSubmitted},
		{StatePending, StateFailed},
		{StatePending, StateCancelled},
		{StateSubmitted, StateRunning},
		{StateSubmitted, StateSucceeded},
		{StateSubmitted, StateFailed},
		{StateSubmitted, StateLost},
		{StateRunning, StateSucceeded},
		{StateRunning, StateFailed},
		{StateRunning, StateCancelled},
		{StateRunning, StateLost},
		{StateLost, StateSucceeded},
		{StateLost, StateFailed},
	}

	for _, tt := range legal {
		t.Run(tt.from.String()+"_to_"+tt.to.String(), func(t *testing.T) {
			if err := tt.from.Transition(tt.to); err != nil {
				t.Errorf("%q to %q refused: %v", tt.from, tt.to, err)
			}
		})
	}
}

func TestState_TransitionIllegal(t *testing.T) {
	illegal := []struct {
		name string
		from State
		to   State
	}{
		{name: "skipping submission", from: StatePending, to: StateRunning},
		{name: "running before submitted", from: StatePending, to: StateSucceeded},
		{name: "reviving a success", from: StateSucceeded, to: StateRunning},
		{name: "reviving a failure", from: StateFailed, to: StateRunning},
		{name: "un-cancelling", from: StateCancelled, to: StateRunning},
		{name: "succeeded to failed", from: StateSucceeded, to: StateFailed},
		{name: "backwards to pending", from: StateRunning, to: StatePending},
		{name: "losing a finished execution", from: StateSucceeded, to: StateLost},
	}

	for _, tt := range illegal {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.from.Transition(tt.to)
			if err == nil {
				t.Fatalf("%q to %q was allowed", tt.from, tt.to)
			}

			if !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("error does not wrap ErrIllegalTransition: %v", err)
			}

			if !strings.Contains(err.Error(), tt.from.String()) ||
				!strings.Contains(err.Error(), tt.to.String()) {
				t.Errorf("error %q does not name both states", err)
			}
		})
	}
}

// No terminal state has an outgoing transition. This is the property the table
// has to satisfy however it is edited.
func TestState_TerminalStatesAreSinks(t *testing.T) {
	for _, from := range States() {
		if !from.Terminal() {
			continue
		}

		for _, to := range States() {
			if from.CanTransition(to) {
				t.Errorf("terminal state %q can transition to %q", from, to)
			}
		}
	}
}

// The machine must stay acyclic. Cycles are what would make a hand-written
// table unpleasant, and every future feature that looked like it needed one
// belongs in a separate entity instead.
func TestState_NoSelfTransitions(t *testing.T) {
	for _, s := range States() {
		if s.CanTransition(s) {
			t.Errorf("state %q can transition to itself", s)
		}
	}
}

// Every state must be reachable from pending, or it is unreachable in practice
// and the table is lying about the lifecycle.
func TestState_AllStatesReachableFromPending(t *testing.T) {
	seen := map[State]bool{StatePending: true}
	queue := []State{StatePending}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, next := range legalTransitions[current] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}

	for _, s := range States() {
		if !seen[s] {
			t.Errorf("state %q is unreachable from pending", s)
		}
	}
}

func TestState_TransitionRejectsUnknownStates(t *testing.T) {
	if err := State("bogus").Transition(StateRunning); !errors.Is(err, ErrUnknownState) {
		t.Errorf("unknown source state error = %v, want ErrUnknownState", err)
	}

	if err := StateRunning.Transition("bogus"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("unknown target state error = %v, want ErrUnknownState", err)
	}
}

// -------------------------------------------------------------------------
// TEXT ENCODING
// -------------------------------------------------------------------------

func TestState_TextRoundTrip(t *testing.T) {
	for _, want := range States() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() returned unexpected error: %v", err)
			}

			var got State
			if err := got.UnmarshalText(encoded); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
			}

			if got != want {
				t.Errorf("round trip produced %q, want %q", got, want)
			}
		})
	}
}

func TestState_MarshalTextRejectsZeroValue(t *testing.T) {
	var s State

	if _, err := s.MarshalText(); err == nil {
		t.Error("MarshalText() accepted the zero-value State")
	}
}

func TestParseState_Invalid(t *testing.T) {
	for _, input := range []string{"", "timed-out", "Running", "complete"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseState(input)
			if err == nil {
				t.Fatalf("ParseState(%q) = %q, want error", input, got)
			}

			if !errors.Is(err, ErrUnknownState) {
				t.Errorf("error does not wrap ErrUnknownState: %v", err)
			}
		})
	}
}

func TestStates_ReturnsCopy(t *testing.T) {
	first := States()
	first[0] = "mutated"

	if States()[0] == "mutated" {
		t.Error("States() exposed the package-level vocabulary to mutation")
	}
}

func TestState_UnmarshalTextRejectsUnknown(t *testing.T) {
	var s State

	err := s.UnmarshalText([]byte("timed-out"))
	if err == nil {
		t.Fatal("UnmarshalText accepted the retired timed-out state")
	}

	if !errors.Is(err, ErrUnknownState) {
		t.Errorf("error does not wrap ErrUnknownState: %v", err)
	}

	if s != "" {
		t.Errorf("a refused unmarshal left %q behind", s)
	}
}

// -------------------------------------------------------------------------
// ACCEPTED
// -------------------------------------------------------------------------

// Accepted sits between submitted and running so that work a provider queued
// and then dropped is distinguishable from work that actually ran. The quota
// ledger reads that difference.
func TestState_AcceptedTransitions(t *testing.T) {
	legal := []State{StateRunning, StateSucceeded, StateFailed, StateCancelled, StateLost}
	for _, next := range legal {
		if !StateAccepted.CanTransition(next) {
			t.Errorf("accepted to %q was refused", next)
		}
	}

	if StateAccepted.CanTransition(StatePending) {
		t.Error("accepted can transition backwards to pending")
	}

	if StateAccepted.CanTransition(StateSubmitted) {
		t.Error("accepted can transition backwards to submitted")
	}
}

// A synchronous invocation has no separate acceptance step, so submitted must
// still reach a terminal state directly.
func TestState_SubmittedMaySkipAccepted(t *testing.T) {
	for _, next := range []State{StateRunning, StateSucceeded} {
		if !StateSubmitted.CanTransition(next) {
			t.Errorf("submitted to %q was refused", next)
		}
	}
}
