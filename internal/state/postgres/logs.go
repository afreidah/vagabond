// -------------------------------------------------------------------------------
// Output Persistence
//
// Author: Alex Freidah
//
// An execution's output, in chunks apart from its row, written as it arrives.
// A write replaces the chunk it names and drops every one after it and the
// front past the ceiling in the same transaction, so the last chunk grows in
// place and no read sees two versions of one. A read takes one chunk at a
// time, so serving 16 MiB holds 256 KiB.
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

// AppendLog stores chunk seq of id's output under lease, dropping every chunk
// before keepFrom and every one from seq on, so a write at 0 starts over.
func (s *Store) AppendLog(
	ctx context.Context, id execution.ID, seq int64, data []byte, keepFrom int64, lease execution.Lease,
) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		holds, err := q.HoldsExecution(ctx, db.HoldsExecutionParams{ID: id.String(), Owner: lease.Owner, Epoch: lease.Epoch})
		if err != nil {
			return fmt.Errorf("check the lease of %s: %w", id, err)
		}

		if !holds {
			return heldWrite(0, id)
		}

		err = q.DropExecutionLogs(ctx, db.DropExecutionLogsParams{ExecutionID: id.String(), KeepFrom: keepFrom, Seq: seq})
		if err != nil {
			return fmt.Errorf("drop output of %s: %w", id, err)
		}

		err = q.InsertExecutionLog(ctx, db.InsertExecutionLogParams{ExecutionID: id.String(), Seq: seq, Data: data})
		if err != nil {
			return fmt.Errorf("store output of %s: %w", id, err)
		}

		return nil
	})
}

// ReadLog hands each of id's stored chunks from fromSeq on to each, in order,
// with its number. An execution with none hands over nothing.
func (s *Store) ReadLog(ctx context.Context, id execution.ID, fromSeq int64, each func(seq int64, data []byte) error) error {
	seq := fromSeq

	for {
		chunk, err := s.queries.NextExecutionLog(ctx, db.NextExecutionLogParams{ExecutionID: id.String(), Seq: seq})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("read output of %s: %w", id, err)
		}

		if err := each(chunk.Seq, chunk.Data); err != nil {
			return err
		}

		seq = chunk.Seq + 1
	}
}

// PruneLogs deletes the output of every execution that ended before before,
// returning how many lost theirs.
func (s *Store) PruneLogs(ctx context.Context, before time.Time) (int, error) {
	var pruned int64

	err := s.serializable(ctx, func(q *db.Queries) error {
		var err error

		if pruned, err = q.PruneExecutionLogs(ctx, &before); err != nil {
			return fmt.Errorf("prune output: %w", err)
		}

		return nil
	})

	return int(pruned), err
}
