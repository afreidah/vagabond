// -------------------------------------------------------------------------------
// Dispatch Persistence
//
// Author: Alex Freidah
//
// One row per run of a job. Finishing is conditional on the row still running,
// so how a run ended is written once.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/afreidah/vagabond/internal/execution"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// CreateDispatch records a run as it starts. The ID is the primary key, so
// recording one twice fails.
func (s *Store) CreateDispatch(ctx context.Context, d *execution.Dispatch) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		err := q.CreateDispatch(ctx, db.CreateDispatchParams{
			DispatchID: d.ID.String(),
			Namespace:  d.Namespace,
			Job:        d.Job,
			JobVersion: d.JobVersion,
			State:      string(d.State),
			CreatedAt:  d.Created,
		})
		if err != nil {
			return fmt.Errorf("create dispatch %s: %w", d.ID, err)
		}

		return nil
	})
}

// FinishDispatch records how a run ended, only while it is still running.
func (s *Store) FinishDispatch(ctx context.Context, d *execution.Dispatch) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		finished, err := q.FinishDispatch(ctx, db.FinishDispatchParams{
			State:      string(d.State),
			Error:      d.Error,
			EndedAt:    nullTime(d.Ended),
			DispatchID: d.ID.String(),
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

// GetDispatch reads the record of one run.
func (s *Store) GetDispatch(ctx context.Context, id execution.ID) (*execution.Dispatch, error) {
	row, err := s.queries.GetDispatch(ctx, id.String())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: dispatch %s", execution.ErrNotFound, id)
	}

	if err != nil {
		return nil, fmt.Errorf("get dispatch %s: %w", id, err)
	}

	d := &execution.Dispatch{
		ID:         id,
		Namespace:  row.Namespace,
		Job:        row.Job,
		JobVersion: row.JobVersion,
		State:      execution.DispatchState(row.State),
		Error:      row.Error,
		Created:    row.CreatedAt,
	}

	if row.EndedAt != nil {
		d.Ended = *row.EndedAt
	}

	return d, nil
}
