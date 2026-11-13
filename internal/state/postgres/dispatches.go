// -------------------------------------------------------------------------------
// Dispatch Persistence
//
// Author: Alex Freidah
//
// One row per run of a job. The owner's writes are conditional on the row
// still held under the caller's lease, its owner and epoch, so how a run ended
// is written once, by whoever holds it. The lease is held until the dispatch
// is done; a lapsed one is claimed in one statement, under the next epoch.
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
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// -------------------------------------------------------------------------
// WRITES
// -------------------------------------------------------------------------

// CreateDispatch records a run as it starts, leased to its owner. The ID is
// the primary key, so recording one twice fails.
func (s *Store) CreateDispatch(ctx context.Context, d *execution.Dispatch) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		err := q.CreateDispatch(ctx, db.CreateDispatchParams{
			DispatchID: d.ID.String(),
			Namespace:  d.Namespace,
			Job:        d.Job,
			JobVersion: d.JobVersion,
			State:      string(d.State),
			CreatedAt:  d.Created,
			Tasks:      int64(d.Tasks),
			Owner:      d.Owner,
			LeaseUntil: d.LeaseUntil,
			Epoch:      d.Epoch,
		})
		if err != nil {
			return fmt.Errorf("create dispatch %s: %w", d.ID, err)
		}

		return nil
	})
}

// FinishDispatch records how a run ended, only while it is still running and
// still held under d's lease.
func (s *Store) FinishDispatch(ctx context.Context, d *execution.Dispatch) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		finished, err := q.FinishDispatch(ctx, db.FinishDispatchParams{
			State:      string(d.State),
			Error:      d.Error,
			EndedAt:    nullTime(d.Ended),
			DispatchID: d.ID.String(),
			Owner:      d.Owner,
			Epoch:      d.Epoch,
		})
		if err != nil {
			return fmt.Errorf("finish dispatch %s: %w", d.ID, err)
		}

		if finished == 0 {
			return fmt.Errorf("%w: dispatch %s", execution.ErrStale, d.ID)
		}

		return nil
	})
}

// CloseDispatch records under lease that every execution of the dispatch
// finished through release, which ends the lease.
func (s *Store) CloseDispatch(ctx context.Context, id execution.ID, lease execution.Lease, at time.Time) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		closed, err := q.CloseDispatch(ctx, db.CloseDispatchParams{
			DoneAt:     &at,
			DispatchID: id.String(),
			Owner:      lease.Owner,
			Epoch:      lease.Epoch,
		})
		if err != nil {
			return fmt.Errorf("close dispatch %s: %w", id, err)
		}

		if closed == 0 {
			return fmt.Errorf("%w: dispatch %s", execution.ErrStale, id)
		}

		return nil
	})
}

// RenewDispatch extends lease on an open dispatch to until and returns when a
// cancel was requested, zero if none. Stale once done or claimed.
func (s *Store) RenewDispatch(
	ctx context.Context, id execution.ID, lease execution.Lease, until time.Time,
) (time.Time, error) {
	var requested time.Time

	err := s.serializable(ctx, func(q *db.Queries) error {
		at, err := q.RenewDispatch(ctx, db.RenewDispatchParams{
			LeaseUntil: until,
			DispatchID: id.String(),
			Owner:      lease.Owner,
			Epoch:      lease.Epoch,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: dispatch %s", execution.ErrStale, id)
		}

		if err != nil {
			return fmt.Errorf("renew dispatch %s: %w", id, err)
		}

		requested = ptr.Deref(at)

		return nil
	})

	return requested, err
}

// RequestCancel asks whoever holds an open dispatch to cancel it. The first
// request's time stands.
func (s *Store) RequestCancel(ctx context.Context, id execution.ID, at time.Time) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		if _, err := q.RequestCancel(ctx, db.RequestCancelParams{RequestedAt: &at, DispatchID: id.String()}); err != nil {
			return fmt.Errorf("request cancel of dispatch %s: %w", id, err)
		}

		return nil
	})
}

// ClaimDispatches takes over every dispatch not yet done whose lease lapsed
// before now, except those in skip, leasing each to owner until until under
// its next epoch, and returns them.
func (s *Store) ClaimDispatches(
	ctx context.Context, owner string, now, until time.Time, skip []execution.ID,
) ([]*execution.Dispatch, error) {
	var claimed []*execution.Dispatch

	skipped := make([]string, 0, len(skip))
	for _, id := range skip {
		skipped = append(skipped, id.String())
	}

	err := s.serializable(ctx, func(q *db.Queries) error {
		rows, err := q.ClaimDispatches(ctx, db.ClaimDispatchesParams{
			Owner: owner, LeaseUntil: until, Now: now, Skip: skipped,
		})
		if err != nil {
			return fmt.Errorf("claim dispatches: %w", err)
		}

		claimed = make([]*execution.Dispatch, 0, len(rows))

		for i := range rows {
			d, err := dispatchOf(&rows[i])
			if err != nil {
				return err
			}

			claimed = append(claimed, d)
		}

		return nil
	})

	return claimed, err
}

// -------------------------------------------------------------------------
// READS
// -------------------------------------------------------------------------

// GetDispatch reads the record of one run.
func (s *Store) GetDispatch(ctx context.Context, id execution.ID) (*execution.Dispatch, error) {
	row, err := s.queries.GetDispatch(ctx, id.String())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: dispatch %s", execution.ErrNotFound, id)
	}

	if err != nil {
		return nil, fmt.Errorf("get dispatch %s: %w", id, err)
	}

	return dispatchOf(&row)
}

// dispatchOf rebuilds a dispatch from its row. A null end is a run still
// going, a null done one whose executions are still finishing.
func dispatchOf(row *db.Dispatch) (*execution.Dispatch, error) {
	id, err := execution.ParseID(row.DispatchID)
	if err != nil {
		return nil, fmt.Errorf("dispatch row: %w", err)
	}

	d := &execution.Dispatch{
		ID:         id,
		Namespace:  row.Namespace,
		Job:        row.Job,
		JobVersion: row.JobVersion,
		Tasks:      int(row.Tasks),
		State:      execution.DispatchState(row.State),
		Error:      row.Error,
		Owner:      row.Owner,
		Epoch:      row.Epoch,
		LeaseUntil: row.LeaseUntil,
		Created:    row.CreatedAt,
	}

	d.Ended = ptr.Deref(row.EndedAt)
	d.CancelRequested = ptr.Deref(row.CancelRequestedAt)
	d.Done = ptr.Deref(row.DoneAt)

	return d, nil
}
