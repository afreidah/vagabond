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
		t.Run(e.name, func(t *testing.T) {
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
		t.Run(e.name, func(t *testing.T) {
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
		t.Run(e.name, func(t *testing.T) {
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

func TestExecutions_GetUnknown(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			if _, err := store.Get(ctx, newID(t)); !errors.Is(err, execution.ErrNotFound) {
				t.Errorf("Get() = %v, want ErrNotFound", err)
			}
		})
	}
}
