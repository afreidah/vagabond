// -------------------------------------------------------------------------------
// Error Classification
//
// Author: Alex Freidah
//
// The pool as the generated queries see it, with every error a query returns
// passed through unavailable. Writes are classified again by serializable;
// this is what classifies reads, so a read against a database that is down
// reports state.ErrUnavailable as a write does, and a query added later cannot
// forget to.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// classified wraps the pool for db.New.
type classified struct {
	pool *pgxpool.Pool
}

// Exec runs a statement and classifies its error.
func (c classified) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := c.pool.Exec(ctx, sql, args...)

	return tag, unavailable(err)
}

// Query runs a query and classifies its error, and those of the rows it
// returns.
func (c classified) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := c.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, unavailable(err)
	}

	return classifiedRows{Rows: rows}, nil
}

// QueryRow runs a query for one row, whose error arrives with Scan.
func (c classified) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return classifiedRow{row: c.pool.QueryRow(ctx, sql, args...)}
}

// classifiedRows classifies the errors a result set reports as it is read.
type classifiedRows struct {
	pgx.Rows
}

// Scan reads the current row and classifies its error.
func (r classifiedRows) Scan(dest ...any) error {
	return unavailable(r.Rows.Scan(dest...))
}

// Err classifies the error that ended the result set, if any.
func (r classifiedRows) Err() error {
	return unavailable(r.Rows.Err())
}

// classifiedRow classifies the error of a single-row query, which Scan
// reports. No rows is not a connection failure and passes through unchanged.
type classifiedRow struct {
	row pgx.Row
}

// Scan reads the row and classifies its error.
func (r classifiedRow) Scan(dest ...any) error {
	return unavailable(r.row.Scan(dest...))
}
