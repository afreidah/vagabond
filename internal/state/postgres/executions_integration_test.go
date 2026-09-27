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
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
)

// pendingRecord is a first attempt, just created.
func pendingRecord(t *testing.T) *execution.Record {
	t.Helper()

	return &execution.Record{
		Status: execution.Status{
			ID:        newID(t),
			State:     execution.StatePending,
			UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
		},
		Namespace: "ci",
		Job:       "go-test",
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

// A finished record, binary output and bill included, reads back as written
// from a fresh read.
func TestExecutions_RoundTrip(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			r := pendingRecord(t)
			r.Previous = newID(t)
			r.Attempt = 2

			if err := store.Create(ctx, r); err != nil {
				t.Fatalf("Create() = %v", err)
			}

			for _, next := range []execution.State{execution.StateSubmitted, execution.StateRunning} {
				from := r.State
				advance(t, r, next)

				if err := store.Update(ctx, r, from); err != nil {
					t.Fatalf("Update(%s) = %v", next, err)
				}
			}

			r.ProviderID = "vagabond-abc"
			r.Result = &execution.Result{
				ID:       r.ID,
				ExitCode: ptr.Of(3),
				Duration: 12 * time.Second,
				Billed:   &quota.Execution{Memory: 512, Duration: 13 * time.Second},
				Logs:     []byte("ok\x00\xff\xfe done\n"),
			}

			advance(t, r, execution.StateFailed)

			if err := store.Update(ctx, r, execution.StateRunning); err != nil {
				t.Fatalf("Update(failed) = %v", err)
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

// An update written against a state the record has left changes nothing.
func TestExecutions_UpdateIsConditional(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			r := pendingRecord(t)
			if err := store.Create(ctx, r); err != nil {
				t.Fatalf("Create() = %v", err)
			}

			advance(t, r, execution.StateSubmitted)

			if err := store.Update(ctx, r, execution.StatePending); err != nil {
				t.Fatalf("Update() = %v", err)
			}

			stale := *r
			advance(t, &stale, execution.StateCancelled)

			if err := store.Update(ctx, &stale, execution.StatePending); !errors.Is(err, execution.ErrStale) {
				t.Errorf("stale Update() = %v, want ErrStale", err)
			}

			got, err := store.Get(ctx, r.ID)
			if err != nil {
				t.Fatalf("Get() = %v", err)
			}

			if got.State != execution.StateSubmitted {
				t.Errorf("state = %s after a stale update, want submitted", got.State)
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

			r := pendingRecord(t)

			if err := store.Create(ctx, r); err != nil {
				t.Fatalf("Create() = %v", err)
			}

			if err := store.Create(ctx, r); err == nil {
				t.Error("Create() recorded the same ID twice")
			}
		})
	}
}

// A run's record round-trips, finishes once, and keeps why it got no answer.
func TestDispatches_FinishOnce(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			now := time.Now().UTC().Truncate(time.Microsecond)
			d := &execution.Dispatch{
				ID: newID(t), Namespace: "ci", Job: "go-test", JobVersion: 2, Tasks: 3,
				State: execution.DispatchRunning, Owner: "cli", LeaseUntil: now.Add(time.Minute), Created: now,
			}

			if err := store.CreateDispatch(ctx, d); err != nil {
				t.Fatalf("CreateDispatch() = %v", err)
			}

			d.State, d.Error, d.Ended = execution.DispatchUnanswered, "no provider", time.Now().UTC().Truncate(time.Microsecond)

			if err := store.FinishDispatch(ctx, d); err != nil {
				t.Fatalf("FinishDispatch() = %v", err)
			}

			if err := store.FinishDispatch(ctx, d); !errors.Is(err, execution.ErrStale) {
				t.Errorf("second FinishDispatch() = %v, want ErrStale", err)
			}

			got, err := store.GetDispatch(ctx, d.ID)
			if err != nil {
				t.Fatalf("GetDispatch() = %v", err)
			}

			if diff := cmp.Diff(d, got); diff != "" {
				t.Errorf("round trip (-want +got):\n%s", diff)
			}
		})
	}
}

// A claim takes only lapsed leases, once, and afterwards only the new owner
// can renew or finish the dispatch.
func TestDispatches_LeaseClaim(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)
			now := time.Now().UTC().Truncate(time.Microsecond)

			lapsed := &execution.Dispatch{
				ID: newID(t), Namespace: "ci", Job: "go-test", Tasks: 2, State: execution.DispatchRunning,
				Owner: "cli", LeaseUntil: now.Add(-time.Second), Created: now,
			}
			held := &execution.Dispatch{
				ID: newID(t), Namespace: "ci", Job: "go-test", Tasks: 1, State: execution.DispatchRunning,
				Owner: "cli", LeaseUntil: now.Add(time.Minute), Created: now,
			}

			for _, d := range []*execution.Dispatch{lapsed, held} {
				if err := store.CreateDispatch(ctx, d); err != nil {
					t.Fatalf("CreateDispatch() = %v", err)
				}
			}

			until := now.Add(time.Minute)

			claimed, err := store.ClaimDispatches(ctx, "server", now, until)
			if err != nil {
				t.Fatalf("ClaimDispatches() = %v", err)
			}

			want := *lapsed
			want.Owner, want.LeaseUntil = "server", until

			if len(claimed) != 1 {
				t.Fatalf("claimed %d dispatches, want the lapsed one", len(claimed))
			}

			if diff := cmp.Diff(&want, claimed[0]); diff != "" {
				t.Errorf("claimed (-want +got):\n%s", diff)
			}

			if again, _ := store.ClaimDispatches(ctx, "other", now, until); len(again) != 0 {
				t.Errorf("a second claim took %d dispatches", len(again))
			}

			if err := store.RenewDispatch(ctx, lapsed.ID, "cli", until); !errors.Is(err, execution.ErrStale) {
				t.Errorf("RenewDispatch() by the old owner = %v, want ErrStale", err)
			}

			if err := store.RenewDispatch(ctx, lapsed.ID, "server", until.Add(time.Minute)); err != nil {
				t.Errorf("RenewDispatch() by the new owner = %v", err)
			}

			finished := want
			finished.State, finished.Ended = execution.DispatchSucceeded, now

			finished.Owner = "cli"
			if err := store.FinishDispatch(ctx, &finished); !errors.Is(err, execution.ErrStale) {
				t.Errorf("FinishDispatch() by the old owner = %v, want ErrStale", err)
			}

			finished.Owner = "server"
			if err := store.FinishDispatch(ctx, &finished); err != nil {
				t.Errorf("FinishDispatch() by the new owner = %v", err)
			}
		})
	}
}

// TestExecutions_GetUnknown reads an ID nothing recorded.
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
