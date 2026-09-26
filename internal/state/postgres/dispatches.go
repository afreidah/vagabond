// -------------------------------------------------------------------------------
// Dispatch Persistence
//
// Author: Alex Freidah
//
// One row per run of a job. Finishing and renewing are conditional on the row
// still running under the caller's lease, so how a run ended is written once,
// by whoever holds it. A lapsed lease is claimed in one statement.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/afreidah/vagabond/internal/execution"
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
		})
		if err != nil {
			return fmt.Errorf("create dispatch %s: %w", d.ID, err)
		}

		return nil
	})
}

// FinishDispatch records how a run ended, only while it is still running and
// still held by d.Owner.
func (s *Store) FinishDispatch(ctx context.Context, d *execution.Dispatch) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		finished, err := q.FinishDispatch(ctx, db.FinishDispatchParams{
			State:      string(d.State),
			Error:      d.Error,
			EndedAt:    nullTime(d.Ended),
			DispatchID: d.ID.String(),
			Owner:      d.Owner,
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

// RenewDispatch extends owner's lease on a running dispatch to until. Stale
// when the dispatch ended or another owner took it over.
func (s *Store) RenewDispatch(ctx context.Context, id execution.ID, owner string, until time.Time) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		renewed, err := q.RenewDispatch(ctx, db.RenewDispatchParams{
			LeaseUntil: until,
			DispatchID: id.String(),
			Owner:      owner,
		})
		if err != nil {
			return fmt.Errorf("renew dispatch %s: %w", id, err)
		}

		if renewed == 0 {
			return fmt.Errorf("%w: dispatch %s", execution.ErrStale, id)
		}

		return nil
	})
}

// ClaimDispatches takes over every running dispatch whose lease lapsed before
// now, leasing each to owner until until, and returns them.
func (s *Store) ClaimDispatches(
	ctx context.Context, owner string, now, until time.Time,
) ([]*execution.Dispatch, error) {
	var claimed []*execution.Dispatch

	err := s.serializable(ctx, func(q *db.Queries) error {
		rows, err := q.ClaimDispatches(ctx, db.ClaimDispatchesParams{
			Owner: owner, LeaseUntil: until, Now: now,
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
// going.
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
		LeaseUntil: row.LeaseUntil,
		Created:    row.CreatedAt,
	}

	if row.EndedAt != nil {
		d.Ended = *row.EndedAt
	}

	return d, nil
}
