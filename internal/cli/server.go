// -------------------------------------------------------------------------------
// vagabond server
//
// Author: Alex Freidah
//
// Runs the server until interrupted. Everything the command does beyond flags
// and configuration is in internal/server, so it can be started and stopped
// from a test.
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
	"github.com/afreidah/vagabond/internal/server"
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
  and store, until interrupted. Requires a store block.

  Listens on the server block's bind address, 127.0.0.1:4747 by default, with
  TLS when the block names a certificate and key.

  Stopping the server drains requests but leaves running executions recorded
  as they were.

Server Options:

  -config <path>
    Configuration file or directory. Defaults as for job run.

  -log-level <level>
    debug, info, warn or error. Defaults to info. Requests are logged at
    debug; dispatches at info.
`

	return strings.TrimSpace(text)
}

// Run starts the server and blocks until an interrupt or SIGTERM, then shuts
// it down.
func (c *ServerCommand) Run(args []string) int {
	var configPath, logLevel string

	flags := c.FlagSet("server")
	flags.StringVar(&configPath, "config", "", "configuration file or directory")
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

	if cfg.Store == nil {
		return c.Errorf("The server needs a store block: it keeps jobs and executions in the database.")
	}

	s, finish, err := openStores(ctx, cfg.Store.DSN, reg)
	if err != nil {
		return c.Errorf("Could not open the store: %s", err)
	}

	defer finish()

	tlsConfig, err := serverTLS(cfg.Server)
	if err != nil {
		return c.Errorf("%s", err)
	}

	logger := slog.New(slog.NewTextHandler(c.ErrStream, &slog.HandlerOptions{Level: level}))
	srv := server.New(reg, s.ledger, s.db, s.db, logger)

	if err := srv.Serve(ctx, cfg.Server.Address(), tlsConfig); err != nil {
		return c.Errorf("%s", err)
	}

	return ExitSuccess
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
