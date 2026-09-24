//go:build integration

// -------------------------------------------------------------------------------
// Store Integration Tests
//
// Author: Alex Freidah
//
// Every case runs against Postgres and CockroachDB. Cockroach is not redundant
// coverage: it is serializable by default and rejects sequences and triggers, so
// a schema or a statement that only works under Postgres' defaults fails here
// and nowhere else.
// -------------------------------------------------------------------------------

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/state/postgres"
)

const (
	postgresImage  = "postgres:17"
	cockroachImage = "cockroachdb/cockroach:latest-v24.3"

	gbSeconds = 1024 * 1000
)

// engine is one database to run the whole suite against.
type engine struct {
	name  string
	start func(context.Context, *testing.T) string
}

func engines() []engine {
	return []engine{
		{name: "postgres", start: startPostgres},
		{name: "cockroach", start: startCockroach},
	}
}

// startPostgres brings up Postgres and returns its DSN.
func startPostgres(ctx context.Context, t *testing.T) string {
	t.Helper()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("vagabond"),
		tcpostgres.WithUsername("vagabond"),
		tcpostgres.WithPassword("vagabond"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}

	return dsn
}

// startCockroach brings up a single insecure node and returns its DSN.
//
// No testcontainers module for it, so the generic container API drives the
// image directly. The readiness probe is the HTTP endpoint rather than a log
// line, because the SQL port accepts connections before the node will serve.
func startCockroach(ctx context.Context, t *testing.T) string {
	t.Helper()

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
		t.Fatalf("start cockroach: %v", err)
	}

	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("cockroach host: %v", err)
	}

	port, err := container.MappedPort(ctx, "26257/tcp")
	if err != nil {
		t.Fatalf("cockroach port: %v", err)
	}

	return fmt.Sprintf("postgres://root@%s:%s/defaultdb?sslmode=disable", host, port.Port())
}

// open brings up the engine, migrates it, and hands back a ready store.
func open(ctx context.Context, t *testing.T, e engine) *postgres.Store {
	t.Helper()

	store, err := postgres.Open(ctx, e.start(ctx, t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(store.Close)

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return store
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

var (
	fnRequests = ledger.Key{Provider: "fn", Pool: "requests", Period: "2026-09"}
	fnCompute  = ledger.Key{Provider: "fn", Pool: "compute", Period: "2026-09"}
)

func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// fnReservation asks for one request and some compute, against limits of 10
// requests and 1000 GB-seconds.
func fnReservation(t *testing.T, created time.Time, compute int64) ledger.Reservation {
	t.Helper()

	return ledger.Reservation{
		ID:       newID(t),
		Provider: "fn",
		CPU:      1000,
		Memory:   1024,
		Created:  created,
		Charges: []ledger.Charge{
			{Pool: "requests", Period: "2026-09", Amount: 1, Limit: 10},
			{Pool: "compute", Period: "2026-09", Amount: compute, Limit: 1000 * gbSeconds},
		},
	}
}

func mustReserve(ctx context.Context, t *testing.T, s *postgres.Store, r ledger.Reservation) {
	t.Helper()

	fits, _, err := s.Reserve(ctx, r)
	if err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	if !fits {
		t.Fatal("Reserve() refused, want it to fit")
	}
}

func readSeptember(ctx context.Context, t *testing.T, s *postgres.Store) ledger.Usage {
	t.Helper()

	usage, err := s.ReadUsage(ctx, []string{"2026-09"})
	if err != nil {
		t.Fatalf("ReadUsage() = %v", err)
	}

	return usage
}

// -------------------------------------------------------------------------
// MIGRATIONS
// -------------------------------------------------------------------------

// A deployment restarts more often than it migrates, so applying an
// already-applied migration has to be a no-op rather than an error.
func TestMigrate_IsIdempotent(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			if err := store.Migrate(ctx); err != nil {
				t.Errorf("second Migrate() = %v", err)
			}
		})
	}
}

// SchemaVersion is raised by hand with each migration, so this is what catches
// a migration added without it.
func TestVerifySchema_MatchesAfterMigrating(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			if err := store.VerifySchema(ctx); err != nil {
				t.Errorf("VerifySchema() = %v", err)
			}
		})
	}
}

// -------------------------------------------------------------------------
// RESERVE
// -------------------------------------------------------------------------

// A reservation counts as usage before it settles.
func TestReserve_CountsAsUsage(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			mustReserve(ctx, t, store, fnReservation(t, time.Now(), 10*gbSeconds))

			usage := readSeptember(ctx, t, store)

			if usage[fnRequests] != 1 || usage[fnCompute] != 10*gbSeconds {
				t.Errorf("usage = %v, want 1 request and 10 GB-seconds", usage)
			}
		})
	}
}

// A refusal inserts nothing, and hands back the usage it was decided against.
func TestReserve_RefusesPastTheLimit(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			mustReserve(ctx, t, store, fnReservation(t, time.Now(), 995*gbSeconds))

			fits, standing, err := store.Reserve(ctx, fnReservation(t, time.Now(), 10*gbSeconds))
			if err != nil {
				t.Fatalf("Reserve() = %v", err)
			}

			if fits {
				t.Fatal("Reserve() fit past the compute limit")
			}

			if standing[fnCompute] != 995*gbSeconds {
				t.Errorf("standing compute = %d, want %d", standing[fnCompute], 995*gbSeconds)
			}

			if got := readSeptember(ctx, t, store)[fnRequests]; got != 1 {
				t.Errorf("requests = %d after a refusal, want the first reservation's 1", got)
			}
		})
	}
}

// A pool the execution charges nothing cannot refuse it, however full.
func TestReserve_ZeroChargeIgnoresAFullPool(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			mustReserve(ctx, t, store, fnReservation(t, time.Now(), 1000*gbSeconds))
			mustReserve(ctx, t, store, fnReservation(t, time.Now(), 0))
		})
	}
}

// The case serializable isolation exists for. Twenty processes race for a pool
// with room for ten, and exactly ten win.
func TestReserve_ConcurrentReservationsStopAtTheLimit(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			const attempts = 20

			var (
				wg       sync.WaitGroup
				mu       sync.Mutex
				admitted int
				failures []error
			)

			for range attempts {
				r := fnReservation(t, time.Now(), 0)

				wg.Go(func() {
					fits, _, err := store.Reserve(ctx, r)

					mu.Lock()
					defer mu.Unlock()

					if err != nil {
						failures = append(failures, err)
					}

					if fits {
						admitted++
					}
				})
			}

			wg.Wait()

			for _, err := range failures {
				t.Errorf("Reserve() = %v", err)
			}

			if admitted != 10 {
				t.Errorf("admitted %d of %d, want 10", admitted, attempts)
			}

			if got := readSeptember(ctx, t, store)[fnRequests]; got != 10 {
				t.Errorf("requests = %d, want 10", got)
			}
		})
	}
}

// -------------------------------------------------------------------------
// SETTLE
// -------------------------------------------------------------------------

// Settling swaps the reservation for what the run cost, once however many
// times it is called.
func TestSettle_ReplacesTheReservationOnce(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			r := fnReservation(t, time.Now(), 900*gbSeconds)
			mustReserve(ctx, t, store, r)

			actual := map[string]int64{"requests": 1, "compute": 10 * gbSeconds}

			for range 2 {
				if err := store.Settle(ctx, r.ID, actual); err != nil {
					t.Fatalf("Settle() = %v", err)
				}
			}

			usage := readSeptember(ctx, t, store)

			if usage[fnRequests] != 1 || usage[fnCompute] != 10*gbSeconds {
				t.Errorf("usage = %v, want 1 request and 10 GB-seconds", usage)
			}
		})
	}
}

// Settled in the period it was reserved in, whatever the date is now.
func TestSettle_ChargesTheReservedPeriod(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			r := fnReservation(t, time.Now(), 900*gbSeconds)
			mustReserve(ctx, t, store, r)

			if err := store.Settle(ctx, r.ID, map[string]int64{"compute": 10 * gbSeconds}); err != nil {
				t.Fatalf("Settle() = %v", err)
			}

			usage, err := store.ReadUsage(ctx, []string{"2026-09", "2026-10"})
			if err != nil {
				t.Fatalf("ReadUsage() = %v", err)
			}

			if len(usage) != 1 || usage[fnCompute] != 10*gbSeconds {
				t.Errorf("usage = %v, want only september compute", usage)
			}
		})
	}
}

// No amounts is a drop: the reservation goes and nothing is charged.
func TestSettle_NothingDrops(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			r := fnReservation(t, time.Now(), 900*gbSeconds)
			mustReserve(ctx, t, store, r)

			if err := store.Settle(ctx, r.ID, nil); err != nil {
				t.Fatalf("Settle() = %v", err)
			}

			if usage := readSeptember(ctx, t, store); len(usage) != 0 {
				t.Errorf("usage = %v after dropping, want none", usage)
			}
		})
	}
}

// -------------------------------------------------------------------------
// READ
// -------------------------------------------------------------------------

// Periods are how rollover works, so a read must not pick up a period it did
// not ask for.
func TestReadUsage_IsScopedToTheRequestedPeriods(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			september := fnReservation(t, time.Now(), 0)
			october := fnReservation(t, time.Now(), 0)

			for i := range october.Charges {
				october.Charges[i].Period = "2026-10"
			}

			mustReserve(ctx, t, store, september)
			mustReserve(ctx, t, store, october)

			usage := readSeptember(ctx, t, store)

			if usage[fnRequests] != 1 {
				t.Errorf("september requests = %d, want 1", usage[fnRequests])
			}

			if _, ok := usage[ledger.Key{Provider: "fn", Pool: "requests", Period: "2026-10"}]; ok {
				t.Error("ReadUsage() returned a period it was not asked for")
			}
		})
	}
}

func TestReadUsage_NoPeriodsIsNotAQuery(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			usage, err := store.ReadUsage(ctx, nil)
			if err != nil {
				t.Fatalf("ReadUsage() = %v", err)
			}

			if len(usage) != 0 {
				t.Errorf("ReadUsage() = %v, want empty", usage)
			}
		})
	}
}

// -------------------------------------------------------------------------
// REAP
// -------------------------------------------------------------------------

// Only reservations older than the cutoff are offered, each whole, and only
// those the callback accepts are settled.
func TestReap_SettlesWhatTheCallbackAccepts(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			now := time.Now()
			old := now.Add(-2 * time.Hour)

			dropped := fnReservation(t, old, 900*gbSeconds)
			kept := fnReservation(t, old, 900*gbSeconds)
			fresh := fnReservation(t, now, 0)

			for _, r := range []ledger.Reservation{dropped, kept, fresh} {
				mustReserve(ctx, t, store, r)
			}

			offered := map[execution.ID]ledger.Held{}

			reaped, err := store.Reap(ctx, now.Add(-time.Hour),
				func(_ context.Context, h ledger.Held) (map[string]int64, bool) {
					offered[h.ID] = h

					return nil, h.ID == dropped.ID
				})
			if err != nil {
				t.Fatalf("Reap() = %v", err)
			}

			if reaped != 1 {
				t.Errorf("Reap() settled %d, want 1", reaped)
			}

			if _, ok := offered[fresh.ID]; ok || len(offered) != 2 {
				t.Errorf("offered %d reservations, want the two old ones", len(offered))
			}

			if h := offered[kept.ID]; h.CPU != 1000 || h.Memory != 1024 || h.Amounts["compute"] != 900*gbSeconds {
				t.Errorf("held = %+v, want the reservation's shape and amounts", h)
			}

			usage := readSeptember(ctx, t, store)

			if usage[fnRequests] != 2 || usage[fnCompute] != 900*gbSeconds {
				t.Errorf("usage = %v, want the kept and fresh reservations only", usage)
			}
		})
	}
}

// -------------------------------------------------------------------------
// ROUND TRIP
// -------------------------------------------------------------------------

// What the ledger charged has to survive the trip through the database and come
// back as the same standing, or a restart forgets what was spent.
func TestLedgerSurvivesARestart(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			limits := map[string]quota.Limits{"fn": quota.FixtureFunction()}

			before, err := ledger.New(ctx, limits, store)
			if err != nil {
				t.Fatalf("New() = %v", err)
			}

			if err := before.Reserve(ctx, newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second}); err != nil {
				t.Fatalf("Reserve() = %v", err)
			}

			want := before.PoolUsage("fn")

			after, err := ledger.New(ctx, limits, store)
			if err != nil {
				t.Fatalf("New() = %v", err)
			}

			got := after.PoolUsage("fn")

			for pool, amount := range want {
				if got[pool] != amount {
					t.Errorf("pool %q = %d after restart, want %d", pool, got[pool], amount)
				}
			}
		})
	}
}
