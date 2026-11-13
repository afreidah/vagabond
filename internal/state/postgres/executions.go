// -------------------------------------------------------------------------------
// Execution Persistence
//
// Author: Alex Freidah
//
// One row per attempt, written only under its dispatch's lease. Each write is
// one statement under the ledger's serializable retry and carries the whole
// record, so repeating one changes nothing.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// -------------------------------------------------------------------------
// WRITES
// -------------------------------------------------------------------------

// Create records a new execution while its dispatch is held under lease.
// Recording an ID twice fails; a lease no longer held is execution.ErrStale.
func (s *Store) Create(ctx context.Context, r *execution.Record, lease execution.Lease) error {
	row := rowOf(r)

	params := db.CreateExecutionParams{
		ID:            row.ID,
		Namespace:     row.Namespace,
		Job:           row.Job,
		JobVersion:    row.JobVersion,
		Task:          row.Task,
		Provider:      row.Provider,
		Attempt:       row.Attempt,
		PreviousID:    row.PreviousID,
		State:         row.State,
		ProviderID:    row.ProviderID,
		Failure:       row.Failure,
		CreatedAt:     row.CreatedAt,
		StartedAt:     row.StartedAt,
		EndedAt:       row.EndedAt,
		UpdatedAt:     row.UpdatedAt,
		ExitCode:      row.ExitCode,
		DurationMs:    row.DurationMs,
		BilledCpu:     row.BilledCpu,
		BilledMemory:  row.BilledMemory,
		BilledMs:      row.BilledMs,
		LogsTruncated: row.LogsTruncated,
		DispatchID:    row.DispatchID,
		Cpu:           row.Cpu,
		Memory:        row.Memory,
		ReleasedAt:    row.ReleasedAt,
		Reason:        row.Reason,
		LogsStart:     row.LogsStart,
		LogsAt:        row.LogsAt,
		TimeoutMs:     row.TimeoutMs,
		Owner:         lease.Owner,
		Epoch:         lease.Epoch,
	}

	return s.serializable(ctx, func(q *db.Queries) error {
		created, err := q.CreateExecution(ctx, params)
		if err != nil {
			return fmt.Errorf("create execution %s: %w", r.ID, err)
		}

		return heldWrite(created, r.ID)
	})
}

// Save writes r over the stored record while r's dispatch is still held under
// lease, whatever state the stored record is in.
func (s *Store) Save(ctx context.Context, r *execution.Record, lease execution.Lease) error {
	row := rowOf(r)

	params := db.SaveExecutionParams{
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
		LogsTruncated: row.LogsTruncated,
		Reason:        row.Reason,
		LogsStart:     row.LogsStart,
		LogsAt:        row.LogsAt,
		ReleasedAt:    row.ReleasedAt,
		ID:            row.ID,
		Owner:         lease.Owner,
		Epoch:         lease.Epoch,
	}

	return s.serializable(ctx, func(q *db.Queries) error {
		saved, err := q.SaveExecution(ctx, params)
		if err != nil {
			return fmt.Errorf("save execution %s: %w", r.ID, err)
		}

		return heldWrite(saved, r.ID)
	})
}

// heldWrite turns a write that changed nothing into execution.ErrStale: the
// dispatch is no longer held under the caller's lease.
func heldWrite(changed int64, id execution.ID) error {
	if changed == 0 {
		return fmt.Errorf("%w: %s is no longer held under this lease", execution.ErrStale, id)
	}

	return nil
}

// -------------------------------------------------------------------------
// READS
// -------------------------------------------------------------------------

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

// JobExecutions returns a job's most recent executions, newest first, at most
// limit of them.
func (s *Store) JobExecutions(ctx context.Context, namespace, job string, limit int) ([]*execution.Record, error) {
	rows, err := s.queries.ListJobExecutions(ctx, db.ListJobExecutionsParams{
		Namespace: namespace,
		Job:       job,
		MaxRows:   int32(min(limit, math.MaxInt32)), //nolint:gosec // bounded above
	})
	if err != nil {
		return nil, fmt.Errorf("list executions of %s: %w", job, err)
	}

	return recordsOf(rows)
}

// DispatchExecutions returns every execution of one dispatch, oldest first.
func (s *Store) DispatchExecutions(ctx context.Context, dispatch execution.ID) ([]*execution.Record, error) {
	rows, err := s.queries.ListDispatchExecutions(ctx, dispatch.String())
	if err != nil {
		return nil, fmt.Errorf("list executions of dispatch %s: %w", dispatch, err)
	}

	return recordsOf(rows)
}

// recordsOf maps a list of rows.
func recordsOf(rows []db.Execution) ([]*execution.Record, error) {
	records := make([]*execution.Record, 0, len(rows))

	for i := range rows {
		r, err := recordOf(&rows[i])
		if err != nil {
			return nil, err
		}

		records = append(records, r)
	}

	return records, nil
}

// -------------------------------------------------------------------------
// MAPPING
// -------------------------------------------------------------------------

// rowOf flattens a record into columns. Zero times and an absent result are
// nulls. The output is stored apart, so a result's own copy of it is not.
func rowOf(r *execution.Record) db.Execution {
	row := db.Execution{
		ID:            r.ID.String(),
		Namespace:     r.Namespace,
		Job:           r.Job,
		JobVersion:    r.JobVersion,
		Task:          r.Task,
		Provider:      r.Provider,
		Attempt:       int64(r.Attempt),
		Cpu:           int64(r.CPU),
		Memory:        int64(r.Memory),
		TimeoutMs:     r.Timeout.Milliseconds(),
		State:         string(r.State),
		ProviderID:    r.ProviderID,
		Failure:       r.Failure,
		CreatedAt:     r.ID.Created(),
		StartedAt:     nullTime(r.StartedAt),
		EndedAt:       nullTime(r.EndedAt),
		UpdatedAt:     r.UpdatedAt,
		ReleasedAt:    nullTime(r.Released),
		LogsAt:        nullTime(r.LogsAt),
		LogsStart:     r.LogsStart,
		LogsTruncated: r.LogsTruncated,
	}

	if !r.Previous.IsZero() {
		row.PreviousID = r.Previous.String()
	}

	if !r.Dispatch.IsZero() {
		row.DispatchID = r.Dispatch.String()
	}

	result := r.Result
	if result == nil {
		return row
	}

	row.DurationMs = new(result.Duration.Milliseconds())
	row.Reason = result.Reason

	if result.ExitCode != nil {
		row.ExitCode = new(int64(*result.ExitCode))
	}

	if b := result.Billed; b != nil {
		row.BilledCpu = new(int64(b.CPU))
		row.BilledMemory = new(int64(b.Memory))
		row.BilledMs = new(b.Duration.Milliseconds())
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
		ID:         id,
		State:      execution.State(row.State),
		ProviderID: row.ProviderID,
		StartedAt:  ptr.Deref(row.StartedAt),
		EndedAt:    ptr.Deref(row.EndedAt),
		UpdatedAt:  row.UpdatedAt,
		Namespace:  row.Namespace,
		Job:        row.Job,
		JobVersion: row.JobVersion,
		Task:       row.Task,
		Provider:   row.Provider,
		Attempt:    int(row.Attempt),
		CPU:        int(row.Cpu),
		Memory:     int(row.Memory),
		Timeout:    time.Duration(row.TimeoutMs) * time.Millisecond,
		Failure:    row.Failure,
		Released:   ptr.Deref(row.ReleasedAt),

		LogsAt:        ptr.Deref(row.LogsAt),
		LogsStart:     row.LogsStart,
		LogsTruncated: row.LogsTruncated,
	}

	if row.PreviousID != "" {
		if r.Previous, err = execution.ParseID(row.PreviousID); err != nil {
			return nil, fmt.Errorf("execution row previous: %w", err)
		}
	}

	if row.DispatchID != "" {
		if r.Dispatch, err = execution.ParseID(row.DispatchID); err != nil {
			return nil, fmt.Errorf("execution row dispatch: %w", err)
		}
	}

	if row.DurationMs == nil {
		return r, nil
	}

	r.Result = &execution.Result{
		ID:       id,
		Duration: time.Duration(*row.DurationMs) * time.Millisecond,
		Reason:   row.Reason,
	}

	if row.ExitCode != nil {
		r.Result.ExitCode = new(int(*row.ExitCode))
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
