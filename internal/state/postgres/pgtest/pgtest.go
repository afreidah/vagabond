//go:build integration

// -------------------------------------------------------------------------------
// Shared Test Databases
//
// Author: Alex Freidah
//
// One container per database engine for a whole test binary, started on first
// use and migrated once. Each test opens a store on it with every table
// emptied, so tests stay independent without paying a container boot and a
// full migration each. Tests using it must not run in parallel.
// -------------------------------------------------------------------------------

package pgtest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/afreidah/vagabond/internal/state/postgres"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	postgresImage  = "postgres:17"
	cockroachImage = "cockroachdb/cockroach:latest-v24.3"
)

// -------------------------------------------------------------------------
// ENGINES
// -------------------------------------------------------------------------

// Engine is one database a suite runs against, started once per binary.
type Engine struct {
	Name  string
	start func(context.Context) (string, testcontainers.Container, error)

	once      sync.Once
	dsn       string
	container testcontainers.Container
	err       error
}

// engines are started lazily, so a binary that uses only Postgres never pulls
// CockroachDB.
var engines = []*Engine{
	{Name: "postgres", start: startPostgres},
	{Name: "cockroach", start: startCockroach},
}

// Engines returns every engine, for a suite that runs each case against both.
func Engines() []*Engine {
	return engines
}

// Postgres returns the Postgres engine, for a suite that needs only one.
func Postgres() *Engine {
	return engines[0]
}

// Main runs the tests and terminates every container they started. Called from
// a package's TestMain.
func Main(m *testing.M) {
	code := m.Run()

	for _, e := range engines {
		if e.container != nil {
			_ = testcontainers.TerminateContainer(e.container)
		}
	}

	os.Exit(code)
}

// -------------------------------------------------------------------------
// STORES
// -------------------------------------------------------------------------

// Open returns a store on e's shared database with every table emptied,
// closed when the test ends.
func Open(t *testing.T, e *Engine) *postgres.Store {
	t.Helper()

	ctx := context.Background()
	dsn := e.ready(t)

	if err := empty(ctx, dsn); err != nil {
		t.Fatalf("%s: emptying tables: %v", e.Name, err)
	}

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("%s: open: %v", e.Name, err)
	}

	t.Cleanup(store.Close)

	return store
}

// ready starts and migrates the engine on first use and returns its DSN.
func (e *Engine) ready(t *testing.T) string {
	t.Helper()

	e.once.Do(func() {
		ctx := context.Background()

		e.dsn, e.container, e.err = e.start(ctx)
		if e.err == nil {
			e.err = migrate(ctx, e.dsn)
		}
	})

	if e.err != nil {
		t.Fatalf("%s: %v", e.Name, e.err)
	}

	return e.dsn
}

// migrate applies every migration once, on a connection closed afterwards.
func migrate(ctx context.Context, dsn string) error {
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	defer store.Close()

	return store.Migrate(ctx)
}

// empty deletes every row of every table but goose's own. DELETE rather than
// TRUNCATE, which CockroachDB runs as a slow schema change.
func empty(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name <> 'goose_db_version'`)
	if err != nil {
		return err
	}

	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}

	for _, table := range tables {
		if _, err := conn.Exec(ctx, "DELETE FROM "+pgx.Identifier{table}.Sanitize()); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
	}

	return nil
}

// -------------------------------------------------------------------------
// CONTAINERS
// -------------------------------------------------------------------------

// startPostgres brings up Postgres and returns its DSN.
func startPostgres(ctx context.Context) (string, testcontainers.Container, error) {
	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("vagabond"),
		tcpostgres.WithUsername("vagabond"),
		tcpostgres.WithPassword("vagabond"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", nil, fmt.Errorf("start postgres: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", container, fmt.Errorf("postgres dsn: %w", err)
	}

	return dsn, container, nil
}

// startCockroach brings up a single insecure node and returns its DSN.
//
// No testcontainers module for it, so the generic container API drives the
// image directly. The readiness probe is the HTTP endpoint rather than a log
// line, because the SQL port accepts connections before the node will serve.
func startCockroach(ctx context.Context) (string, testcontainers.Container, error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        cockroachImage,
			Cmd:          []string{"start-single-node", "--insecure"},
			ExposedPorts: []string{"26257/tcp", "8080/tcp"},
			WaitingFor: wait.ForHTTP("/health?ready=1").
				WithPort("8080/tcp").
				WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start cockroach: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		return "", container, fmt.Errorf("cockroach host: %w", err)
	}

	port, err := container.MappedPort(ctx, "26257/tcp")
	if err != nil {
		return "", container, fmt.Errorf("cockroach port: %w", err)
	}

	return fmt.Sprintf("postgres://root@%s:%s/defaultdb?sslmode=disable", host, port.Port()), container, nil
}
