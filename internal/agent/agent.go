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
	"github.com/afreidah/vagabond/internal/backoff"
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
	for failures := 0; ; failures++ {
		err := a.connect(ctx)
		if ctx.Err() != nil {
			return nil
		}

		// A session that got going and then dropped starts the backoff over
		if err == nil {
			a.logger.WarnContext(ctx, "lost the server", "server", a.cfg.Server)
			failures = 0
		}

		wait := backoff.Exponential(reconnectMin, reconnectMax, failures)

		if err != nil {
			a.logger.WarnContext(ctx, "connecting to the server", "server", a.cfg.Server, "error", err, "retry", wait)
		}

		if backoff.Sleep(ctx, wait) != nil {
			return nil
		}
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

	changed := make(chan struct{}, 1)
	local := newExecutions(a.exec, &agentrpc.Resources{Cpu: a.node.CPU, Memory: a.node.Memory}, changed)

	// The server calls this endpoint down the session the agent opened.
	srv := grpc.NewServer()
	agentrpc.RegisterAgentExecutionsServer(srv, local)

	go func() { _ = session.Serve(srv) }()
	defer srv.Stop()

	if err := a.register(ctx, session, local); err != nil {
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

		// Not fatal: the next report carries everything; a dead session ends via Done.
		if err := a.register(ctx, session, local); err != nil {
			a.logger.WarnContext(ctx, "reporting workloads", "error", err)
		}
	}
}

// register reports the node and every workload it holds to the server, then
// releases the workloads the server answers it has no use for. Sent once on
// connecting and again on every change, so the server's view of what is
// placed where, and how much room is left, follows the node's.
func (a *Agent) register(ctx context.Context, session *agentrpc.Session, local *executions) error {
	held, err := a.exec.Held(ctx)
	if err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}

	answer, err := agentrpc.NewNodeClient(session.Peer()).Register(ctx, &agentrpc.NodeRegisterRequest{
		Name:         a.cfg.Name,
		Pool:         a.cfg.Pool,
		Labels:       a.cfg.Labels,
		Architecture: a.node.Architecture,
		Capacity:     local.capacity,
		Runtimes:     a.exec.Runtimes(),
		Version:      a.cfg.Version,
		Held:         held,
	})
	if err != nil {
		return fmt.Errorf("registering: %w", err)
	}

	// Released as the server would, so the next report shows the room free.
	for _, id := range answer.GetRelease() {
		if _, err := local.Release(ctx, &agentrpc.ExecutionRequest{ExecutionId: id}); err != nil {
			a.logger.WarnContext(ctx, "releasing a workload the server has no use for", "execution", id, "error", err)

			continue
		}

		a.logger.InfoContext(ctx, "released a workload the server has no use for", "execution", id)
	}

	return nil
}
