// -------------------------------------------------------------------------------
// Output Persistence
//
// Author: Alex Freidah
//
// An execution's output, in chunks apart from its row. A write replaces what it
// supersedes in the same transaction, so a capture that starts over or drops
// the front never leaves both versions readable. A read takes one chunk at a
// time, so serving 16 MiB holds 256 KiB.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/afreidah/vagabond/internal/execution"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// AppendLog stores chunk seq of id's output, dropping every chunk before
// keepFrom and every one from seq on, so a write at seq 0 starts the output
// over.
func (s *Store) AppendLog(ctx context.Context, id execution.ID, seq int64, data []byte, keepFrom int64) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		err := q.DropExecutionLogs(ctx, db.DropExecutionLogsParams{ExecutionID: id.String(), KeepFrom: keepFrom, Seq: seq})
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

// ReadLog writes id's stored output from chunk fromSeq on to w, in order. An
// execution with none writes nothing.
func (s *Store) ReadLog(ctx context.Context, id execution.ID, fromSeq int64, w io.Writer) error {
	seq := fromSeq

	for {
		chunk, err := s.queries.NextExecutionLog(ctx, db.NextExecutionLogParams{ExecutionID: id.String(), Seq: seq})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("read output of %s: %w", id, err)
		}

		if _, err := w.Write(chunk.Data); err != nil {
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
