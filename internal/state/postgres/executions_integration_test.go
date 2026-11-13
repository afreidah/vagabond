//go:build integration

// -------------------------------------------------------------------------------
// Execution Store Integration Tests
//
// Author: Alex Freidah
//
// Every case runs against Postgres and CockroachDB, using the engines and open
// helper from the store suite.
// -------------------------------------------------------------------------------

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/state/postgres"
	"github.com/afreidah/vagabond/internal/state/statetest"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// leased records a running dispatch and returns its ID and the lease its
// executions are written under.
func leased(ctx context.Context, t *testing.T, store *postgres.Store) (execution.ID, execution.Lease) {
	t.Helper()

	d := &execution.Dispatch{
		ID:         newID(t),
		Namespace:  "ci",
		Job:        "go-test",
		Tasks:      1,
		State:      execution.DispatchRunning,
		Owner:      "server:a:1",
		Epoch:      1,
		LeaseUntil: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond),
		Created:    time.Now().UTC().Truncate(time.Microsecond),
	}

	if err := store.CreateDispatch(ctx, d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	return d.ID, d.Lease()
}

// pendingRecord is a first attempt of dispatch, just created.
func pendingRecord(t *testing.T, dispatch execution.ID) *execution.Record {
	t.Helper()

	return &execution.Record{
		ID:        newID(t),
		State:     execution.StatePending,
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Namespace: "ci",
		Job:       "go-test",
		Dispatch:  dispatch,
		Task:      "test",
		Provider:  "gcp-cloud-run",
		Attempt:   1,
		CPU:       1000,
		Memory:    512,
	}
}

// advance moves r to next the way dispatch does.
func advance(t *testing.T, r *execution.Record, next execution.State) {
	t.Helper()

	if err := r.To(next, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatalf("To(%s) = %v", next, err)
	}
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// A finished, released record, bill, reason and where its output stands
// included, reads back as written from a fresh read.
func TestExecutions_RoundTrip(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)
			dispatch, lease := leased(ctx, t, store)

			r := pendingRecord(t, dispatch)
			r.Previous = newID(t)
			r.Attempt = 2

			if err := store.Create(ctx, r, lease); err != nil {
				t.Fatalf("Create() = %v", err)
			}

			for _, next := range []execution.State{execution.StateSubmitted, execution.StateRunning} {
				advance(t, r, next)

				if err := store.Save(ctx, r, lease); err != nil {
					t.Fatalf("Save(%s) = %v", next, err)
				}
			}

			r.ProviderID = "vagabond-abc"
			r.Result = &execution.Result{
				ID:       r.ID,
				ExitCode: new(137),
				Duration: 12 * time.Second,
				Billed:   &quota.Execution{Memory: 512, Duration: 13 * time.Second},
				Reason:   "OOMKilled",
			}

			advance(t, r, execution.StateFailed)

			r.LogsAt = r.EndedAt
			r.LogsStart = 2 * execution.LogChunk
			r.LogsTruncated = true
			r.Released = r.EndedAt.Add(time.Second)

			if err := store.Save(ctx, r, lease); err != nil {
				t.Fatalf("Save(failed) = %v", err)
			}

			got, err := store.Get(ctx, r.ID)
			if err != nil {
				t.Fatalf("Get() = %v", err)
			}

			if diff := cmp.Diff(r, got); diff != "" {
				t.Errorf("round trip (-want +got):\n%s", diff)
			}
		})
	}
}

// The ID is the idempotency key, so recording it twice fails.
func TestExecutions_CreateTwiceFails(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)
			dispatch, lease := leased(ctx, t, store)

			r := pendingRecord(t, dispatch)

			if err := store.Create(ctx, r, lease); err != nil {
				t.Fatalf("Create() = %v", err)
			}

			if err := store.Create(ctx, r, lease); err == nil {
				t.Error("Create() recorded the same ID twice")
			}
		})
	}
}

// A reservation is reported until it is settled.
func TestExecutions_Reserved(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			r := fnReservation(t, time.Now(), 1)
			mustReserve(ctx, t, store, r)

			if held, err := store.Reserved(ctx, r.ID); err != nil || !held {
				t.Fatalf("Reserved() = %v, %v; want true", held, err)
			}

			if err := store.Settle(ctx, r.ID, nil); err != nil {
				t.Fatalf("Settle() = %v", err)
			}

			if held, err := store.Reserved(ctx, r.ID); err != nil || held {
				t.Errorf("Reserved() after settling = %v, %v; want false", held, err)
			}
		})
	}
}

// Reading an ID nothing recorded is ErrNotFound.
func TestExecutions_GetUnknown(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			if _, err := store.Get(ctx, newID(t)); !errors.Is(err, execution.ErrNotFound) {
				t.Errorf("Get() = %v, want ErrNotFound", err)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CONTRACTS
// -------------------------------------------------------------------------

// Both engines keep the lease rules every store keeps.
func TestExecutions_LeaseContract(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			statetest.LeaseContract(t, func(t *testing.T) statetest.Store {
				return open(t.Context(), t, e)
			})
		})
	}
}

// Both engines keep the output rules every store keeps.
func TestExecutions_LogContract(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			statetest.LogContract(t, func(t *testing.T) statetest.Store {
				return open(t.Context(), t, e)
			})
		})
	}
}
