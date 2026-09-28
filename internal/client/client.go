// -------------------------------------------------------------------------------
// The Client
//
// Author: Alex Freidah
//
// The node side of vagabond agent. Dials the server, registers the node, and
// serves its executions on the same connection until it drops, then dials
// again. Workloads outlive the connection: they belong to containerd, and the
// client reports what it still holds each time it registers.
// -------------------------------------------------------------------------------

package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/client/fingerprint"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Reconnection backs off from reconnectMin to reconnectMax.
const (
	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is how a client reaches its server and what it calls its node.
type Config struct {
	Server  string
	Pool    string
	Name    string
	Labels  map[string]string
	TLS     *tls.Config
	Version string
}

// Client is one node's connection to its server.
type Client struct {
	cfg    Config
	node   fingerprint.Node
	exec   clientExecutor
	logger *slog.Logger
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds a client reporting node and running work on exec.
func New(cfg *Config, node fingerprint.Node, exec clientExecutor, logger *slog.Logger) *Client {
	return &Client{cfg: *cfg, node: node, exec: exec, logger: logger}
}

// -------------------------------------------------------------------------
// RUNNING
// -------------------------------------------------------------------------

// Run stays connected to the server until ctx is done, dialling again with
// backoff whenever the connection drops or cannot be made.
func (c *Client) Run(ctx context.Context) error {
	wait := reconnectMin

	for {
		err := c.connect(ctx)

		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			c.logger.WarnContext(ctx, "connecting to the server", "server", c.cfg.Server, "error", err, "retry", wait)
		default:
			c.logger.WarnContext(ctx, "lost the server", "server", c.cfg.Server)
			wait = reconnectMin
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		wait = min(wait*2, reconnectMax)
	}
}

// connect holds one session: serve executions, register, and wait for the
// connection or ctx to end. An error is a session that never got going.
func (c *Client) connect(ctx context.Context) error {
	session, err := agentrpc.Dial(ctx, c.cfg.Server, c.cfg.TLS)
	if err != nil {
		return err
	}

	defer func() { _ = session.Close() }()

	srv := grpc.NewServer()
	agentrpc.RegisterClientExecutionsServer(srv, &executions{exec: c.exec})

	go func() { _ = session.Serve(srv) }()
	defer srv.Stop()

	if err := c.register(ctx, session); err != nil {
		return err
	}

	c.logger.InfoContext(ctx, "connected", "server", c.cfg.Server, "name", c.cfg.Name, "pool", c.cfg.Pool)

	select {
	case <-ctx.Done():
	case <-session.Done():
	}

	return nil
}

// register announces the node and the workloads it still holds.
func (c *Client) register(ctx context.Context, session *agentrpc.Session) error {
	held, err := c.exec.List(ctx)
	if err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}

	_, err = agentrpc.NewNodeClient(session.Peer()).Register(ctx, &agentrpc.NodeRegisterRequest{
		Name:         c.cfg.Name,
		Pool:         c.cfg.Pool,
		Labels:       c.cfg.Labels,
		Architecture: c.node.Architecture,
		Capacity:     &agentrpc.Resources{Cpu: c.node.CPU, Memory: c.node.Memory},
		Runtimes:     c.exec.Runtimes(),
		Version:      c.cfg.Version,
		Executions:   held,
	})
	if err != nil {
		return fmt.Errorf("registering: %w", err)
	}

	return nil
}
