// -------------------------------------------------------------------------------
// Pool End-to-End Test
//
// Author: Alex Freidah
//
// A server with a pool provider and an agent that joins it: a job run over the
// API is admitted to the pool, placed on the node, and finishes there.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// poolJob runs a container on the homelab pool.
const poolJob = `
job "local" {
  type = "batch"

  routing {
    providers = ["homelab"]
  }

  task "build" {
    driver = "container"

    config {
      image = "alpine:3.20"
    }
  }
}
`

// A job routed to a pool runs on the node that joined it, and records which
// node that was.
func TestPool_RunsAJobOnANode(t *testing.T) {
	ctx := t.Context()

	cfg, diags := config.Load("test.hcl", []byte(`provider "homelab" { type = "pool" }`))
	if diags.HasErrors() {
		t.Fatalf("config: %s", diags.Error())
	}

	// The registry's pool and the server's agent connections share one set of
	// nodes, as vagabond server wires them.
	conns := nodes.New()

	reg, diags := registry.New(ctx, cfg, registry.WithNodes(conns))
	if diags.HasErrors() {
		t.Fatalf("registry: %s", diags.Error())
	}

	led, err := ledger.New(ctx, reg.Budgets(), ledger.NewMemory(nil))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}

	srv := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)),
		slog.New(slog.DiscardHandler), WithNodes(conns))

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serveCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	go func() { _ = srv.ServeAgents(serveCtx, listener) }()

	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	agents := &agentHarness{srv: srv, address: listener.Addr().String(), api: httpServer.URL}
	agents.connect(t, "box1", newFakeExecutor())
	agents.node(t, "box1")

	h := &harness{url: httpServer.URL}

	var started api.DispatchResponse
	if status := h.call(t, http.MethodPost, "/v1/jobs/run", api.RunRequest{Source: poolJob}, &started); status != http.StatusOK {
		t.Fatalf("run status = %d", status)
	}

	e := h.awaitExecution(t, started.DispatchID, string(execution.StateSucceeded))
	if e.Provider != "homelab" || e.ProviderID != "box1" {
		t.Errorf("execution ran on %s/%s, want homelab/box1", e.Provider, e.ProviderID)
	}
}
