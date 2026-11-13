// -------------------------------------------------------------------------------
// Store Contract Fixtures
//
// Author: Alex Freidah
//
// The store surface the contracts cover, and the dispatches and records they
// are run on. Each contract builds what it needs from these, so a rule reads
// as the writes it makes.
// -------------------------------------------------------------------------------

package statetest

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// Store is the part of an execution store the contracts cover.
type Store interface {
	CreateDispatch(ctx context.Context, d *execution.Dispatch) error
	FinishDispatch(ctx context.Context, d *execution.Dispatch) error
	CloseDispatch(ctx context.Context, id execution.ID, lease execution.Lease, at time.Time) error
	RenewDispatch(ctx context.Context, id execution.ID, lease execution.Lease, until time.Time) (time.Time, error)
	RequestCancel(ctx context.Context, id execution.ID, at time.Time) error
	ClaimDispatches(ctx context.Context, owner string, now, until time.Time, skip []execution.ID) ([]*execution.Dispatch, error)
	GetDispatch(ctx context.Context, id execution.ID) (*execution.Dispatch, error)

	Create(ctx context.Context, r *execution.Record, lease execution.Lease) error
	Save(ctx context.Context, r *execution.Record, lease execution.Lease) error
	Get(ctx context.Context, id execution.ID) (*execution.Record, error)

	AppendLog(ctx context.Context, id execution.ID, seq int64, data []byte, keepFrom int64, lease execution.Lease) error
	ReadLog(ctx context.Context, id execution.ID, fromSeq int64, w io.Writer) error
	PruneLogs(ctx context.Context, before time.Time) (int, error)
}

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// owner is who the contracts' dispatches are leased to when they begin, and
// claimer another server that claims them.
const (
	owner   = "server:a:1"
	claimer = "server:b:2"
)

// newID returns a fresh ID, failing the test if one cannot be made.
func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// dispatched records a running dispatch leased to owner under its first epoch
// until leaseUntil, and returns it.
func dispatched(t *testing.T, s Store, leaseUntil time.Time) *execution.Dispatch {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Microsecond)

	d := &execution.Dispatch{
		ID:         newID(t),
		Namespace:  "default",
		Job:        "ci",
		Tasks:      1,
		State:      execution.DispatchRunning,
		Owner:      owner,
		Epoch:      1,
		LeaseUntil: leaseUntil.UTC().Truncate(time.Microsecond),
		Created:    now,
	}

	if err := s.CreateDispatch(t.Context(), d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	return d
}

// recorded records an execution of d under d's lease, ended at ended when
// that is set and running otherwise, and returns it.
func recorded(t *testing.T, s Store, d *execution.Dispatch, ended time.Time) *execution.Record {
	t.Helper()

	id := newID(t)

	r := &execution.Record{
		ID:        id,
		State:     execution.StateRunning,
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Namespace: d.Namespace,
		Job:       d.Job,
		Dispatch:  d.ID,
		Task:      "test",
		Provider:  "box",
		Attempt:   1,
	}

	if !ended.IsZero() {
		r.State, r.StartedAt, r.EndedAt = execution.StateSucceeded, ended.Add(-time.Second), ended
	}

	if err := s.Create(t.Context(), r, d.Lease()); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return r
}

// lapsed is a lease end already past, so a claim takes the dispatch.
func lapsed() time.Time {
	return time.Now().Add(-time.Minute)
}

// held is a lease end well ahead, so no claim takes the dispatch.
func held() time.Time {
	return time.Now().Add(time.Hour)
}
