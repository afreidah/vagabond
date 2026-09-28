// -------------------------------------------------------------------------------
// The Agent
//
// Author: Alex Freidah
//
// The process that runs on a node, apart from the server. Dials the server,
// registers the node, and serves its executions on the same connection until
// it drops, then dials again. Workloads outlive the connection: they belong to
// containerd, and the agent reports what it still holds each time it
// registers.
// -------------------------------------------------------------------------------

package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/agentrpc"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Reconnection backs off from reconnectMin to reconnectMax.
const (
	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// reportEvery is how often the agent reports its workloads when nothing has
// told it they changed, which catches a workload finishing on its own.
const reportEvery = 10 * time.Second

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is how an agent reaches its server and what it calls its node.
type Config struct {
	Server  string
	Pool    string
	Name    string
	Labels  map[string]string
	TLS     *tls.Config
	Version string
}

// Agent is one node's connection to its server.
type Agent struct {
	cfg    Config
	node   fingerprint.Node
	exec   agentExecutor
	logger *slog.Logger
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds an agent reporting node and running work on exec.
func New(cfg *Config, node fingerprint.Node, exec agentExecutor, logger *slog.Logger) *Agent {
	return &Agent{cfg: *cfg, node: node, exec: exec, logger: logger}
}

// -------------------------------------------------------------------------
// RUNNING
// -------------------------------------------------------------------------

// Run stays connected to the server until ctx is done, dialling again with
// backoff whenever the connection drops or cannot be made.
func (a *Agent) Run(ctx context.Context) error {
	wait := reconnectMin

	for {
		err := a.connect(ctx)

		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			a.logger.WarnContext(ctx, "connecting to the server", "server", a.cfg.Server, "error", err, "retry", wait)
		default:
			// A session that got going and then dropped starts the backoff over.
			a.logger.WarnContext(ctx, "lost the server", "server", a.cfg.Server)
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

// connect holds one session: serve executions, register, then report again
// whenever the workloads change, until the connection or ctx ends. An error is
// a session that never got going.
func (a *Agent) connect(ctx context.Context) error {
	session, err := agentrpc.Dial(ctx, a.cfg.Server, a.cfg.TLS)
	if err != nil {
		return err
	}

	defer func() { _ = session.Close() }()

	capacity := &agentrpc.Resources{Cpu: a.node.CPU, Memory: a.node.Memory}
	changed := make(chan struct{}, 1)

	// The server calls this endpoint down the session the agent opened.
	srv := grpc.NewServer()
	agentrpc.RegisterAgentExecutionsServer(srv, &executions{exec: a.exec, capacity: capacity, changed: changed})

	go func() { _ = session.Serve(srv) }()
	defer srv.Stop()

	if err := a.register(ctx, session, capacity); err != nil {
		return err
	}

	a.logger.InfoContext(ctx, "connected", "server", a.cfg.Server, "name", a.cfg.Name, "pool", a.cfg.Pool)

	ticker := time.NewTicker(reportEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-session.Done():
			return nil
		case <-changed:
		case <-ticker.C:
		}

		// A failed report is not fatal: the next one carries everything, and a
		// connection that is really gone ends through session.Done.
		if err := a.register(ctx, session, capacity); err != nil {
			a.logger.WarnContext(ctx, "reporting workloads", "error", err)
		}
	}
}

// register reports the node and every workload it holds to the server. Sent
// once on connecting and again on every change, so the server's view of what
// is placed where, and how much room is left, follows the node's.
func (a *Agent) register(ctx context.Context, session *agentrpc.Session, capacity *agentrpc.Resources) error {
	held, err := a.exec.Held(ctx)
	if err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}

	report := make([]*agentrpc.Held, 0, len(held))
	for _, h := range held {
		report = append(report, &agentrpc.Held{
			ExecutionId: h.ID,
			Resources:   &agentrpc.Resources{Cpu: h.CPU, Memory: h.Memory},
			Running:     h.Running,
		})
	}

	_, err = agentrpc.NewNodeClient(session.Peer()).Register(ctx, &agentrpc.NodeRegisterRequest{
		Name:         a.cfg.Name,
		Pool:         a.cfg.Pool,
		Labels:       a.cfg.Labels,
		Architecture: a.node.Architecture,
		Capacity:     capacity,
		Runtimes:     a.exec.Runtimes(),
		Version:      a.cfg.Version,
		Held:         report,
	})
	if err != nil {
		return fmt.Errorf("registering: %w", err)
	}

	return nil
}
