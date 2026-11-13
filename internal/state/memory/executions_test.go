// -------------------------------------------------------------------------------
// Memory Execution Store Tests
//
// Author: Alex Freidah
//
// The memory store keeps the Postgres store's rules: the shared contracts run
// against it, and these cover what is particular to memory, such as copies
// kept apart from the caller's.
// -------------------------------------------------------------------------------

package memory

import (
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/state/statetest"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// pending records a dispatch and returns a fresh pending record of it, with
// the lease to write it under.
func pending(t *testing.T, s *Executions) (*execution.Record, execution.Lease) {
	t.Helper()

	dispatch, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	d := &execution.Dispatch{
		ID: dispatch, State: execution.DispatchRunning, Owner: "test", Epoch: 1, LeaseUntil: time.Now().Add(time.Hour),
	}

	if err := s.CreateDispatch(t.Context(), d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	r := &execution.Record{
		ID:        id,
		State:     execution.StatePending,
		UpdatedAt: time.Now(),
		Job:       "ci",
		Dispatch:  dispatch,
		Task:      "test",
		Attempt:   1,
	}

	return r, d.Lease()
}

// -------------------------------------------------------------------------
// RECORDS
// -------------------------------------------------------------------------

// A created record reads back, and the same ID is refused a second time.
func TestExecutions_CreateThenGet(t *testing.T) {
	s := NewExecutions()
	r, lease := pending(t, s)

	if err := s.Create(t.Context(), r, lease); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := s.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}

	if got.State != execution.StatePending || got.Job != "ci" {
		t.Errorf("Get() = %+v", got)
	}

	if err := s.Create(t.Context(), r, lease); err == nil {
		t.Error("Create() recorded the same ID twice")
	}
}

// Reading an ID nothing recorded is ErrNotFound.
func TestExecutions_GetUnknown(t *testing.T) {
	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	if _, err := NewExecutions().Get(t.Context(), id); !errors.Is(err, execution.ErrNotFound) {
		t.Errorf("Get() = %v, want ErrNotFound", err)
	}
}

// A result's own copy of the output is not kept, as in Postgres, and the
// caller's copy is not touched.
func TestExecutions_ResultOutputIsNotKept(t *testing.T) {
	s := NewExecutions()
	r, lease := pending(t, s)
	r.Result = &execution.Result{ID: r.ID, ExitCode: new(0), Logs: []byte("output"), LogsTruncated: true}

	if err := s.Create(t.Context(), r, lease); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := s.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}

	if got.Result.Logs != nil || got.Result.LogsTruncated || got.Result.ExitCode == nil {
		t.Errorf("read back %+v, want the result without its output", got.Result)
	}

	if string(r.Result.Logs) != "output" || !r.Result.LogsTruncated {
		t.Error("the caller's copy was changed")
	}
}

// -------------------------------------------------------------------------
// CONTRACTS
// -------------------------------------------------------------------------

// The memory store keeps the lease rules every store keeps.
func TestExecutions_LeaseContract(t *testing.T) {
	statetest.LeaseContract(t, func(*testing.T) statetest.Store { return NewExecutions() })
}

// The memory store keeps the output rules every store keeps.
func TestExecutions_LogContract(t *testing.T) {
	statetest.LogContract(t, func(*testing.T) statetest.Store { return NewExecutions() })
}
