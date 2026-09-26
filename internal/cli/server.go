// -------------------------------------------------------------------------------
// vagabond server
//
// Author: Alex Freidah
//
// Runs the server until interrupted. Everything the command does beyond flags
// and configuration is in internal/server, so it can be started and stopped
// from a test. With -dev every store is in memory, so the configured providers
// can be driven with no database.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/state/postgres"
)

// ServerCommand implements `vagabond server`.
type ServerCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *ServerCommand) Synopsis() string {
	return "Run the Vagabond server"
}

// Help returns the full usage text.
func (c *ServerCommand) Help() string {
	text := `
Usage: vagabond server [options]

  Runs the server: the HTTP API under /v1, backed by the configured providers
  and store, until interrupted. Requires a store block unless -dev is given.

  Listens on the server block's bind address, 127.0.0.1:4747 by default, with
  TLS when the block names a certificate and key.

  Stopping the server drains requests and leaves running dispatches to the
  next server, which resumes them once their leases lapse.

Server Options:

  -config <path>
    Configuration file or directory. A directory loads every .hcl file inside
    it.

    Defaults to $VAGABOND_CONFIG, then the first of these that exists:
    ./vagabond.hcl, the user configuration directory, /etc/vagabond.d.

  -dev
    Keep jobs, executions and quota usage in memory, losing them on exit. A
    store block is ignored.

  -log-level <level>
    debug, info, warn or error. Defaults to info. Requests are logged at
    debug; dispatches at info.
`

	return strings.TrimSpace(text)
}

// Run starts the server and blocks until an interrupt or SIGTERM, then shuts
// it down.
func (c *ServerCommand) Run(args []string) int {
	var (
		configPath, logLevel string
		dev                  bool
	)

	flags := c.FlagSet("server")
	flags.StringVar(&configPath, "config", "", "configuration file or directory")
	flags.BoolVar(&dev, "dev", false, "keep every store in memory")
	flags.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(logLevel)); err != nil {
		return c.Errorf("Invalid -log-level %q: use debug, info, warn or error.", logLevel)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg, cfg, code := c.loadRegistry(ctx, configPath)
	if reg == nil {
		return code
	}

	if reg.Len() == 0 {
		return c.Errorf("No providers are configured, so there is nothing to run on.")
	}

	logger := slog.New(slog.NewTextHandler(c.ErrStream, &slog.HandlerOptions{Level: level}))

	var (
		srv *server.Server
		err error
	)

	switch {
	case dev:
		if cfg.Store != nil {
			c.Ui.Warn("Running with -dev: the store block is ignored and everything is kept in memory.")
		}

		srv, err = devServer(ctx, reg, logger)

	case cfg.Store == nil:
		return c.Errorf("The server needs a store block, or -dev to keep everything in memory.")

	default:
		var closeStore func()

		srv, closeStore, err = storeServer(ctx, cfg.Store.DSN, reg, logger)
		if err == nil {
			defer closeStore()
		}
	}

	if err != nil {
		return c.Errorf("%s", err)
	}

	tlsConfig, err := serverTLS(cfg.Server)
	if err != nil {
		return c.Errorf("%s", err)
	}

	if err := srv.Serve(ctx, cfg.Server.Address(), tlsConfig); err != nil {
		return c.Errorf("%s", err)
	}

	return ExitSuccess
}

// -------------------------------------------------------------------------
// PROVIDERS AND STORES
// -------------------------------------------------------------------------

// loadRegistry finds configuration, builds the providers, and refreshes them.
// The configuration comes back beside the registry for its store and server
// blocks.
//
// A refresh failure is reported but does not stop the server. A provider that
// did not answer is marked unhealthy and rejected by name until it does.
func (m *Meta) loadRegistry(
	ctx context.Context, configPath string,
) (*registry.Registry, *config.File, int) {
	path, err := config.Discover(configPath)
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	cfg, diags := config.LoadPath(path)
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, nil, diags, m.color())

		return nil, nil, ExitFailure
	}

	reg, diags := registry.New(ctx, cfg)
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, nil, diags, m.color())

		return nil, nil, ExitFailure
	}

	if err := reg.Refresh(ctx); err != nil {
		m.Ui.Warn(fmt.Sprintf("Some providers did not answer: %s", err))
	}

	return reg, cfg, ExitSuccess
}

// devServer builds a server over memory stores, empty at every start.
func devServer(ctx context.Context, reg *registry.Registry, logger *slog.Logger) (*server.Server, error) {
	led, err := ledger.New(ctx, reg.Budgets(), ledger.NewMemory(nil))
	if err != nil {
		return nil, err
	}

	executions := memory.NewExecutions()

	return server.New(reg, led, executions, memory.NewJobs(executions), logger), nil
}

// storeServer connects to the store, migrates it, and builds a server over it.
// The function returned closes the connection.
func storeServer(
	ctx context.Context, dsn string, reg *registry.Registry, logger *slog.Logger,
) (*server.Server, func(), error) {
	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("could not open the store: %w", err)
	}

	err = db.Migrate(ctx)
	if err == nil {
		err = db.VerifySchema(ctx)
	}

	if err != nil {
		db.Close()

		return nil, nil, fmt.Errorf("could not prepare the store: %w", err)
	}

	led, err := ledger.New(ctx, reg.Budgets(), db)
	if err != nil {
		db.Close()

		return nil, nil, fmt.Errorf("could not read quota usage: %w", err)
	}

	return server.New(reg, led, db, db, logger), db.Close, nil
}

// serverTLS loads the certificate and key the server block names, or returns
// nil to serve plain HTTP.
func serverTLS(block *config.ServerBlock) (*tls.Config, error) {
	if block == nil || block.TLS == nil {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(block.TLS.Cert, block.TLS.Key)
	if err != nil {
		return nil, fmt.Errorf("loading the server certificate: %w", err)
	}

	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
