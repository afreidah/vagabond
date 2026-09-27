// -------------------------------------------------------------------------------
// Postgres Store
//
// Author: Alex Freidah
//
// Persistence for the usage ledger. One implementation for Postgres and
// CockroachDB: Cockroach speaks the same wire protocol, and this schema stays
// clear of the places the two disagree.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose
	"github.com/pressly/goose/v3"

	"github.com/afreidah/vagabond/internal/state"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// SchemaVersion is the migration version this binary expects. Raised with every
// migration added.
const SchemaVersion = 6

// connectTimeout bounds opening a connection when the DSN sets none, so an
// unreachable database fails a request quickly rather than holding it.
const connectTimeout = 5 * time.Second

// Store holds the connection pool and the generated queries.
type Store struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	dsn     string
}

// -------------------------------------------------------------------------
// LIFECYCLE
// -------------------------------------------------------------------------

// Open connects and verifies the connection before returning.
//
// Verified here rather than on first use, so a bad DSN is an error at startup
// instead of a dispatch that fails for reasons nothing in the job explains.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("%w: %w", state.ErrUnavailable, err)
	}

	return &Store{pool: pool, queries: db.New(pool), dsn: dsn}, nil
}

// Close releases the pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping reports whether the database answers, as state.ErrUnavailable when it
// does not.
func (s *Store) Ping(ctx context.Context) error {
	return unavailable(s.pool.Ping(ctx))
}

// Migrate applies the embedded migrations, skipping the ones already recorded.
//
// goose wants a database/sql handle, so one is opened for the duration rather
// than kept alongside the pool for the life of the process.
func (s *Store) Migrate(ctx context.Context) error {
	handle, err := sql.Open("pgx", s.dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}

	defer handle.Close()

	migrations, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("migration filesystem: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, handle, migrations)
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}

// -------------------------------------------------------------------------
// TRANSACTIONS
// -------------------------------------------------------------------------

// Serialization failures are retried this many times, the backoff doubling from
// serializationBackoff to serializationBackoffMax with jitter so contenders
// spread out instead of colliding again in lockstep.
const (
	serializationAttempts   = 10
	serializationBackoff    = 5 * time.Millisecond
	serializationBackoffMax = 500 * time.Millisecond
)

// serializationFailure is SQLSTATE 40001. Postgres raises it under serializable
// isolation; CockroachDB raises it for any contended transaction.
const serializationFailure = "40001"

// serializable runs fn in a serializable transaction, retrying it whole on a
// serialization failure.
func (s *Store) serializable(ctx context.Context, fn func(*db.Queries) error) error {
	var err error

	for attempt := range serializationAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay(attempt)):
			}
		}

		err = s.transaction(ctx, fn)
		if !isSerializationFailure(err) {
			return unavailable(err)
		}
	}

	return fmt.Errorf("gave up after %d serialization failures: %w", serializationAttempts, err)
}

// unavailable marks err as state.ErrUnavailable when it is the database not
// answering: a connection that could not be opened, one that broke or that the
// driver already closed, or a timeout. Anything else is returned unchanged.
func unavailable(err error) error {
	_, connect := errors.AsType[*pgconn.ConnectError](err)
	_, network := errors.AsType[net.Error](err)

	switch {
	case err == nil:
		return nil
	case connect, network, pgconn.Timeout(err), errors.Is(err, pgconn.ErrConnClosed),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: %w", state.ErrUnavailable, err)
	default:
		return err
	}
}

// retryDelay is a random wait up to the attempt's exponential ceiling. Full
// jitter, so transactions that failed together do not retry together.
func retryDelay(attempt int) time.Duration {
	ceiling := min(serializationBackoff<<attempt, serializationBackoffMax)

	return rand.N(ceiling) + 1 //nolint:gosec // jitter, not a secret
}

// transaction runs fn in one serializable transaction. Rollback after a commit
// is a no-op, so it is deferred unconditionally.
func (s *Store) transaction(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == serializationFailure
}

// -------------------------------------------------------------------------
// SCHEMA
// -------------------------------------------------------------------------

// VerifySchema checks that the database is at exactly the version this binary
// expects.
//
// Older means a migration failed partway. Newer means this binary is outdated
// and would write rows a newer one no longer understands.
func (s *Store) VerifySchema(ctx context.Context) error {
	var version int64

	err := s.pool.QueryRow(ctx,
		"SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = true",
	).Scan(&version)
	if err != nil {
		return fmt.Errorf("query schema version: %w", err)
	}

	switch {
	case version < SchemaVersion:
		return fmt.Errorf("database schema is at version %d, older than the %d this binary expects; a migration may have failed partway",
			version, SchemaVersion)

	case version > SchemaVersion:
		return fmt.Errorf("database schema is at version %d, newer than the %d this binary expects; upgrade vagabond",
			version, SchemaVersion)
	}

	return nil
}
