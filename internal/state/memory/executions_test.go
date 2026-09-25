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
		Status:  execution.Status{ID: id, State: execution.StatePending, UpdatedAt: time.Now()},
		Job:     "ci",
		Task:    "test",
		Attempt: 1,
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
