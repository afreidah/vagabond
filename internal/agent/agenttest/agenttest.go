// -------------------------------------------------------------------------------
// Agent Test Runner
//
// Author: Alex Freidah
//
// Runs a real agent for tests of what it connects to: the server, a pool
// provider, or the whole system, with its node and executors given.
// -------------------------------------------------------------------------------

package agenttest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
)

// Run starts an agent reporting node to cfg's server and running work on
// execs, until the returned function or the end of the test stops it. An agent
// that cannot start fails the test.
func Run(t *testing.T, cfg *agent.Config, node fingerprint.Node, execs ...agent.Executor) (stop func()) {
	t.Helper()

	a, err := agent.New(t.Context(), cfg, node, slog.New(slog.DiscardHandler), execs...)
	if err != nil {
		t.Fatalf("agent.New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer cancel()
		_ = a.Run(ctx)
	}()

	stop = func() {
		cancel()
		<-done
	}

	t.Cleanup(stop)

	return stop
}
