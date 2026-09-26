// -------------------------------------------------------------------------------
// Execution Persistence
//
// Author: Alex Freidah
//
// One row per attempt. Every write is one statement under the same serializable
// retry as the ledger, and an update is conditional on the state the caller
// read, so a resumed server and the dispatcher cannot overwrite each other.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// Create records a new execution. The ID is the primary key, so recording one
// twice fails rather than overwriting.
func (s *Store) Create(ctx context.Context, r *execution.Record) error {
	row := rowOf(r)

	return s.serializable(ctx, func(q *db.Queries) error {
		if err := q.CreateExecution(ctx, db.CreateExecutionParams(row)); err != nil {
			return fmt.Errorf("create execution %s: %w", r.ID, err)
		}

		return nil
	})
}

// Update writes r over the stored record if it is still in from.
func (s *Store) Update(ctx context.Context, r *execution.Record, from execution.State) error {
	row := rowOf(r)

	params := db.UpdateExecutionParams{
		State:         row.State,
		ProviderID:    row.ProviderID,
		Failure:       row.Failure,
		StartedAt:     row.StartedAt,
		EndedAt:       row.EndedAt,
		UpdatedAt:     row.UpdatedAt,
		ExitCode:      row.ExitCode,
		DurationMs:    row.DurationMs,
		BilledCpu:     row.BilledCpu,
		BilledMemory:  row.BilledMemory,
		BilledMs:      row.BilledMs,
		Logs:          row.Logs,
		LogsTruncated: row.LogsTruncated,
		ID:            row.ID,
		FromState:     string(from),
	}

	return s.serializable(ctx, func(q *db.Queries) error {
		updated, err := q.UpdateExecution(ctx, params)
		if err != nil {
			return fmt.Errorf("update execution %s: %w", r.ID, err)
		}

		if updated == 0 {
			return fmt.Errorf("%w: %s", execution.ErrStale, r.ID)
		}

		return nil
	})
}

// Get reads the record for id.
func (s *Store) Get(ctx context.Context, id execution.ID) (*execution.Record, error) {
	row, err := s.queries.GetExecution(ctx, id.String())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", execution.ErrNotFound, id)
	}

	if err != nil {
		return nil, fmt.Errorf("get execution %s: %w", id, err)
	}

	return recordOf(&row)
}

// -------------------------------------------------------------------------
// MAPPING
// -------------------------------------------------------------------------

// rowOf flattens a record into columns. Zero times and an absent result are
// nulls; logs are bounded here as well as by callers, since this is the last
// place that can.
func rowOf(r *execution.Record) db.Execution {
	row := db.Execution{
		ID:         r.ID.String(),
		Namespace:  r.Namespace,
		Job:        r.Job,
		JobVersion: r.JobVersion,
		Task:       r.Task,
		Provider:   r.Provider,
		Attempt:    int64(r.Attempt),
		State:      string(r.State),
		ProviderID: r.ProviderID,
		Failure:    r.Failure,
		CreatedAt:  r.ID.Created(),
		StartedAt:  nullTime(r.StartedAt),
		EndedAt:    nullTime(r.EndedAt),
		UpdatedAt:  r.UpdatedAt,
	}

	if !r.Previous.IsZero() {
		row.PreviousID = r.Previous.String()
	}

	result := r.Result.Bounded()
	if result == nil {
		return row
	}

	row.DurationMs = ptr.Of(result.Duration.Milliseconds())
	row.Logs = result.Logs
	row.LogsTruncated = result.LogsTruncated

	if result.ExitCode != nil {
		row.ExitCode = ptr.Of(int64(*result.ExitCode))
	}

	if b := result.Billed; b != nil {
		row.BilledCpu = ptr.Of(int64(b.CPU))
		row.BilledMemory = ptr.Of(int64(b.Memory))
		row.BilledMs = ptr.Of(b.Duration.Milliseconds())
	}

	return row
}

// recordOf rebuilds a record from its row. A null duration means no result was
// recorded.
func recordOf(row *db.Execution) (*execution.Record, error) {
	id, err := execution.ParseID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("execution row: %w", err)
	}

	r := &execution.Record{
		Status: execution.Status{
			ID:         id,
			State:      execution.State(row.State),
			ProviderID: row.ProviderID,
			StartedAt:  ptr.Deref(row.StartedAt),
			EndedAt:    ptr.Deref(row.EndedAt),
			UpdatedAt:  row.UpdatedAt,
		},
		Namespace:  row.Namespace,
		Job:        row.Job,
		JobVersion: row.JobVersion,
		Task:       row.Task,
		Provider:   row.Provider,
		Attempt:    int(row.Attempt),
		Failure:    row.Failure,
	}

	if row.PreviousID != "" {
		if r.Previous, err = execution.ParseID(row.PreviousID); err != nil {
			return nil, fmt.Errorf("execution row previous: %w", err)
		}
	}

	if row.DurationMs == nil {
		return r, nil
	}

	r.Result = &execution.Result{
		ID:            id,
		Duration:      time.Duration(*row.DurationMs) * time.Millisecond,
		Logs:          row.Logs,
		LogsTruncated: row.LogsTruncated,
	}

	if row.ExitCode != nil {
		r.Result.ExitCode = ptr.Of(int(*row.ExitCode))
	}

	if row.BilledMs != nil {
		r.Result.Billed = &quota.Execution{
			CPU:      int(ptr.Deref(row.BilledCpu)),
			Memory:   int(ptr.Deref(row.BilledMemory)),
			Duration: time.Duration(*row.BilledMs) * time.Millisecond,
		}
	}

	return r, nil
}

// nullTime is nil for the zero time.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}
