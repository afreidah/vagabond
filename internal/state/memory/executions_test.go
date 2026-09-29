// -------------------------------------------------------------------------------
// Memory Execution Store Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package memory

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

func pending(t *testing.T) *execution.Record {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return &execution.Record{
		ID:        id,
		State:     execution.StatePending,
		UpdatedAt: time.Now(),
		Job:       "ci",
		Task:      "test",
		Attempt:   1,
	}
}

func TestExecutions_CreateThenGet(t *testing.T) {
	s := NewExecutions()
	r := pending(t)

	if err := s.Create(t.Context(), r); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := s.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}

	if got.State != execution.StatePending || got.Job != "ci" {
		t.Errorf("Get() = %+v", got)
	}

	if err := s.Create(t.Context(), r); err == nil {
		t.Error("Create() recorded the same ID twice")
	}
}

// An update written against a state the record has left is refused.
func TestExecutions_UpdateIsConditional(t *testing.T) {
	s := NewExecutions()
	r := pending(t)

	if err := s.Create(t.Context(), r); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if err := r.To(execution.StateSubmitted, time.Now()); err != nil {
		t.Fatalf("To() = %v", err)
	}

	if err := s.Update(t.Context(), r, execution.StatePending); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	if err := s.Update(t.Context(), r, execution.StatePending); !errors.Is(err, execution.ErrStale) {
		t.Errorf("stale Update() = %v, want ErrStale", err)
	}
}

// A run finishes once: a second finish, or one for a run never recorded, is
// refused as stale.
func TestExecutions_DispatchFinishesOnce(t *testing.T) {
	s := NewExecutions()
	id, _ := execution.NewID()

	d := &execution.Dispatch{ID: id, Job: "ci", State: execution.DispatchRunning, Created: time.Now()}
	if err := s.CreateDispatch(t.Context(), d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	finished := &execution.Dispatch{ID: id, State: execution.DispatchSucceeded, Ended: time.Now()}
	if err := s.FinishDispatch(t.Context(), finished); err != nil {
		t.Fatalf("FinishDispatch() = %v", err)
	}

	again := &execution.Dispatch{ID: id, State: execution.DispatchFailed, Ended: time.Now()}
	if err := s.FinishDispatch(t.Context(), again); !errors.Is(err, execution.ErrStale) {
		t.Errorf("second FinishDispatch() = %v, want ErrStale", err)
	}

	got, err := s.GetDispatch(t.Context(), id)
	if err != nil || got.State != execution.DispatchSucceeded || got.Job != "ci" {
		t.Errorf("GetDispatch() = %+v, %v; want the first finish, keeping the job", got, err)
	}
}

// Only the owner renews or finishes a dispatch, and a claim takes only lapsed
// leases, after which the old owner holds nothing.
func TestExecutions_DispatchLeases(t *testing.T) {
	s := NewExecutions()
	now := time.Now()

	lapsed := &execution.Dispatch{State: execution.DispatchRunning, Owner: "cli", LeaseUntil: now.Add(-time.Second)}
	held := &execution.Dispatch{State: execution.DispatchRunning, Owner: "cli", LeaseUntil: now.Add(time.Minute)}

	for _, d := range []*execution.Dispatch{lapsed, held} {
		d.ID, _ = execution.NewID()
		if err := s.CreateDispatch(t.Context(), d); err != nil {
			t.Fatalf("CreateDispatch() = %v", err)
		}
	}

	if err := s.RenewDispatch(t.Context(), held.ID, "other", now); !errors.Is(err, execution.ErrStale) {
		t.Errorf("RenewDispatch() by another owner = %v, want ErrStale", err)
	}

	if skipped, _ := s.ClaimDispatches(t.Context(), "server", now, now.Add(time.Minute), []execution.ID{lapsed.ID}); len(skipped) != 0 {
		t.Errorf("ClaimDispatches() took %d dispatches it was told to skip", len(skipped))
	}

	claimed, err := s.ClaimDispatches(t.Context(), "server", now, now.Add(time.Minute), nil)
	if err != nil || len(claimed) != 1 || claimed[0].ID != lapsed.ID || claimed[0].Owner != "server" {
		t.Fatalf("ClaimDispatches() = %+v, %v; want only the lapsed one, now the server's", claimed, err)
	}

	if err := s.RenewDispatch(t.Context(), lapsed.ID, "cli", now); !errors.Is(err, execution.ErrStale) {
		t.Errorf("RenewDispatch() by the old owner = %v, want ErrStale", err)
	}

	lapsed.State, lapsed.Ended = execution.DispatchSucceeded, now
	if err := s.FinishDispatch(t.Context(), lapsed); !errors.Is(err, execution.ErrStale) {
		t.Errorf("FinishDispatch() by the old owner = %v, want ErrStale", err)
	}

	lapsed.Owner = "server"
	if err := s.FinishDispatch(t.Context(), lapsed); err != nil {
		t.Errorf("FinishDispatch() by the new owner = %v", err)
	}
}

// TestExecutions_GetUnknown reads an ID nothing recorded.
// A record is unreleased until marked, only past the cutoff, and an update
// after marking keeps the mark, as the Postgres store does.
func TestExecutions_Release(t *testing.T) {
	s := NewExecutions()
	r := pending(t)

	if err := s.Create(t.Context(), r); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if got, _ := s.Unreleased(t.Context(), r.UpdatedAt); len(got) != 0 {
		t.Errorf("Unreleased() before the cutoff = %d records, want 0", len(got))
	}

	if got, _ := s.Unreleased(t.Context(), r.UpdatedAt.Add(time.Second)); len(got) != 1 {
		t.Fatalf("Unreleased() past the cutoff = %d records, want 1", len(got))
	}

	at := time.Now()
	if err := s.MarkReleased(t.Context(), r.ID, at); err != nil {
		t.Fatalf("MarkReleased() = %v", err)
	}

	if err := r.To(execution.StateSubmitted, time.Now()); err != nil {
		t.Fatalf("To() = %v", err)
	}

	if err := s.Update(t.Context(), r, execution.StatePending); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	if got, _ := s.Get(t.Context(), r.ID); !got.Released.Equal(at) {
		t.Errorf("Released = %v after an update, want %v", got.Released, at)
	}

	if got, _ := s.Unreleased(t.Context(), time.Now().Add(time.Hour)); len(got) != 0 {
		t.Errorf("Unreleased() after marking = %d records, want 0", len(got))
	}
}

func TestExecutions_GetUnknown(t *testing.T) {
	id, _ := execution.NewID()

	if _, err := NewExecutions().Get(t.Context(), id); !errors.Is(err, execution.ErrNotFound) {
		t.Errorf("Get() = %v, want ErrNotFound", err)
	}
}

// Stored output is bounded to its tail, and the caller's copy is not touched.
func TestExecutions_LogsAreBounded(t *testing.T) {
	s := NewExecutions()
	r := pending(t)

	logs := append(bytes.Repeat([]byte("x"), execution.MaxStoredLogs), []byte("the end")...)
	r.Result = &execution.Result{ID: r.ID, Logs: logs}

	if err := s.Create(t.Context(), r); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := s.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}

	if len(got.Result.Logs) != execution.MaxStoredLogs || !bytes.HasSuffix(got.Result.Logs, []byte("the end")) {
		t.Errorf("stored %d bytes, want the last %d", len(got.Result.Logs), execution.MaxStoredLogs)
	}

	if !got.Result.LogsTruncated || r.Result.LogsTruncated || len(r.Result.Logs) != len(logs) {
		t.Error("the stored copy is not marked truncated, or the caller's was changed")
	}
}
