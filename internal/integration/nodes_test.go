//go:build integration

// -------------------------------------------------------------------------------
// Agent Nodes
//
// Author: Alex Freidah
//
// A server on the shared store with a containerd provider, and real agents
// with fake executors joining it. An execution a dead server left on a node
// is lost while the node is away, still charged and unreleased, and resolves
// once it returns; a node returning with workloads nothing needs any more is
// told to release them.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/executor/executortest"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/state/postgres/pgtest"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// nodeProvider is made of the agents that join pool homelab, with a runtime
// pool large enough never to refuse.
const nodeProvider = `
provider "homelab" {
  type = "containerd"

  pool "runtime" {
    meter  = "seconds"
    limit  = 100000
    period = "monthly"
  }
}
`

// newNodeHarness is a harness whose servers serve agents on a port of their
// own, into the one node set the provider is made of, and the server option
// sharing that set.
func newNodeHarness(t *testing.T) (*harness, server.Option) {
	t.Helper()

	var lc net.ListenConfig

	agents, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	conns := nodes.New()
	h := &harness{
		t:        t,
		registry: providers(t, nodeProvider, registry.WithNodes(conns)),
		store:    pgtest.Open(t, pgtest.Postgres()),
		agents:   agents,
	}

	return h, server.WithNodes(conns)
}

// join runs an agent named name in pool homelab against the harness's agent
// port, over exec, until the test ends.
func (h *harness) join(name string, exec *executortest.Fake) {
	h.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	a := agent.New(&agent.Config{Server: h.agents.Addr().String(), Pool: "homelab", Name: name},
		fingerprint.Node{Architecture: "amd64", CPU: 4000, Memory: 8192},
		exec, slog.New(slog.DiscardHandler))

	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()

	h.t.Cleanup(func() {
		cancel()
		<-done
	})
}

// eventually polls cond until it holds, or fails the test after runTimeout.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(runTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// An execution left on a node that is away when its dispatch is resumed is
// lost, keeping its charge and its record unreleased however long the node
// stays away; once the node returns it finishes, is settled at what it ran,
// and is released from the node.
func TestNodes_LostWhileAwayResolvesOnReturn(t *testing.T) {
	h, withNodes := newNodeHarness(t)

	id := newID(t)
	exec := &executortest.Fake{Finish: true, Output: "done\n"}
	_ = exec.Submit(t.Context(), id.String(), &executor.Spec{CPU: 1000, Memory: 512})

	h.seed(id, "homelab", "box1", time.Now().Add(-time.Hour))
	h.serve(server.WithOwner("server-a"), withNodes)

	eventually(t, "the execution to be lost", func() bool {
		rec, err := h.store.Get(t.Context(), id)

		return err == nil && rec.State == execution.StateLost
	})

	if rec, _ := h.store.Get(t.Context(), id); !rec.Released.IsZero() || h.used("homelab", "runtime") != reserved {
		t.Fatalf("lost execution = %+v with %d used; want unreleased and still charged %d",
			rec, h.used("homelab", "runtime"), reserved)
	}

	h.join("box1", exec)

	// The dispatch answered lost; the execution resolves in the background
	eventually(t, "the execution to finish and be released", func() bool {
		rec, err := h.store.Get(t.Context(), id)

		return err == nil && rec.State == execution.StateSucceeded && !rec.Released.IsZero()
	})

	eventually(t, "the workload to be released", func() bool { return exec.Workload(id.String()) == nil })

	// The fake reports one second.
	if got := h.used("homelab", "runtime"); got != second {
		t.Errorf("runtime used = %d, want the settled %d", got, second)
	}
}

// A node returning with the workload of an execution already released, and
// one no execution was ever recorded for, is told to release both, and keeps
// the workload of a live one.
func TestNodes_ReturningNodeReleasesWhatNothingNeeds(t *testing.T) {
	h, withNodes := newNodeHarness(t)

	live, released, unrecorded := newID(t), newID(t), newID(t)

	// Leased to a live owner far ahead, so the server leaves the dispatch be.
	h.seed(live, "homelab", "box1", time.Now().Add(time.Hour))
	d := h.seed(released, "homelab", "box1", time.Now().Add(time.Hour))

	rec, err := h.store.Get(t.Context(), released)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}

	rec.Released = time.Now()
	if err := h.store.Save(t.Context(), rec, execution.Lease{Owner: "cli:dead:1", Epoch: 1}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	exec := &executortest.Fake{}
	for _, id := range []execution.ID{live, released, unrecorded} {
		_ = exec.Submit(t.Context(), id.String(), &executor.Spec{})
	}

	h.serve(withNodes)
	h.join("box1", exec)

	eventually(t, "the unneeded workloads to be released", func() bool {
		return exec.Workload(released.String()) == nil && exec.Workload(unrecorded.String()) == nil
	})

	if exec.Workload(live.String()) == nil {
		t.Errorf("the workload of %s, still live under dispatch %s, was released", live, d)
	}
}
