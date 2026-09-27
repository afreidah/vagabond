//go:build integration

// -------------------------------------------------------------------------------
// End-to-End Harness
//
// Author: Alex Freidah
//
// A server as `vagabond server` runs one, on the shared Postgres container and
// the fake providers a test configures. Usage and abandoned dispatches are
// seeded into the store before the server starts, since it reads both at
// startup. Tests drive it through the API client and the CLI.
// -------------------------------------------------------------------------------

package integration

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/cli"
	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/state/postgres"
	"github.com/afreidah/vagabond/internal/state/postgres/pgtest"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Seconds-metered pools count milliseconds.
const second = 1000

// runTimeout bounds waiting for a run to end.
const runTimeout = 30 * time.Second

// TestMain starts Postgres once for the package and stops it after.
func TestMain(m *testing.M) {
	pgtest.Main(m)
}

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// harness is the providers a test configured and the store they charge. Servers
// started from it share both.
type harness struct {
	t        *testing.T
	registry *registry.Registry
	store    *postgres.Store
}

// newHarness builds the providers cfg declares, refreshed, and an empty store.
func newHarness(t *testing.T, cfg string) *harness {
	t.Helper()

	ctx := context.Background()

	file, diags := config.Load("test.hcl", []byte(cfg))
	if diags.HasErrors() {
		t.Fatalf("config: %s", diags.Error())
	}

	reg, diags := registry.New(ctx, file)
	if diags.HasErrors() {
		t.Fatalf("registry: %s", diags.Error())
	}

	if err := reg.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}

	return &harness{t: t, registry: reg, store: pgtest.Open(t, pgtest.Postgres())}
}

// serve starts a server on the harness's providers and store, points the CLI
// at it, and returns a client for it. It stops when the test ends.
func (h *harness) serve(opts ...server.Option) *api.Client {
	h.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	led, err := ledger.New(ctx, h.registry.Budgets(), h.store)
	if err != nil {
		h.t.Fatalf("ledger: %v", err)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatalf("listen: %v", err)
	}

	srv := server.New(h.registry, led, h.store, h.store, slog.New(slog.DiscardHandler), opts...)
	done := make(chan error, 1)

	go func() { done <- srv.ServeListener(ctx, listener, nil) }()

	h.t.Cleanup(func() {
		cancel()
		<-done
	})

	address := "http://" + listener.Addr().String()
	h.t.Setenv("VAGABOND_ADDR", address)
	h.t.Setenv("VAGABOND_NAMESPACE", "")

	client, err := api.NewClient(address, nil)
	if err != nil {
		h.t.Fatalf("client: %v", err)
	}

	return client
}

// -------------------------------------------------------------------------
// SEEDING
// -------------------------------------------------------------------------

// spend reserves amount of provider's pool this period, as standing usage the
// server reads at startup.
func (h *harness) spend(provider, pool string, amount, limit int64) {
	h.t.Helper()

	id := newID(h.t)
	period := quota.PeriodMonthly.Key(time.Now())

	fits, _, err := h.store.Reserve(context.Background(), &ledger.Reservation{
		ID: id, Provider: provider, Created: time.Now(),
		Charges: []ledger.Charge{{Pool: pool, Period: period, Amount: amount, Limit: limit}},
	})
	if err != nil || !fits {
		h.t.Fatalf("Reserve() = %t, %v", fits, err)
	}
}

// used reads what provider's pool has charged this period.
func (h *harness) used(provider, pool string) int64 {
	h.t.Helper()

	period := quota.PeriodMonthly.Key(time.Now())

	usage, err := h.store.ReadUsage(context.Background(), []string{period})
	if err != nil {
		h.t.Fatalf("ReadUsage() = %v", err)
	}

	return usage[ledger.Key{Provider: provider, Pool: pool, Period: period}]
}

// -------------------------------------------------------------------------
// DRIVING
// -------------------------------------------------------------------------

// run executes the CLI against the most recently started server and returns
// its exit code and what it wrote to each stream.
func run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(args, strings.NewReader(""), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

// await polls a dispatch until it ends, or fails the test after runTimeout.
func await(t *testing.T, client *api.Client, id string) *api.Dispatch {
	t.Helper()

	deadline := time.Now().Add(runTimeout)

	for time.Now().Before(deadline) {
		d, err := client.DispatchStatus(context.Background(), id)
		if err != nil {
			t.Fatalf("DispatchStatus() = %v", err)
		}

		if d.State != string(execution.DispatchRunning) {
			return d
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("dispatch %s did not end within %s", id, runTimeout)

	return nil
}

// newID mints an execution ID.
func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// writeJob puts a job file in a temporary directory and returns its path.
func writeJob(t *testing.T, src string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "job.vagabond.hcl")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing the job: %v", err)
	}

	return path
}
