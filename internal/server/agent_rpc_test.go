// -------------------------------------------------------------------------------
// Agent Connection Tests
//
// Author: Alex Freidah
//
// A real agent dialling a real server over TCP, with a fake executor behind
// the agent: registration, the workloads a node is told to release, calling
// executions back down the session, node listing, and a node leaving when its
// agent stops.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/executor/executortest"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// DOUBLES
// -------------------------------------------------------------------------

// flakyListener fails its first failures accepts as a process out of file
// descriptors does, and closes the next drops connections it accepts, as a
// session cut off before it got going is; then it accepts as the listener it
// wraps.
type flakyListener struct {
	net.Listener

	failures atomic.Int32
	drops    atomic.Int32
}

// Accept fails while failures remain, then hands back closed connections
// while drops remain, then accepts.
func (l *flakyListener) Accept() (net.Conn, error) {
	if l.failures.Add(-1) >= 0 {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}
	}

	conn, err := l.Listener.Accept()
	if err == nil && l.drops.Add(-1) >= 0 {
		_ = conn.Close()
	}

	return conn, err
}

// finishing is an executor whose workloads end at once, printing hello.
func finishing() *executortest.Fake {
	return &executortest.Fake{Finish: true, Output: "hello\n"}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// agentHarness is a server accepting agents on a real port.
type agentHarness struct {
	srv     *Server
	address string
	api     string
}

// newAgentHarness starts a server serving agents and its API, stopped when the
// test ends.
func newAgentHarness(t *testing.T) *agentHarness {
	t.Helper()

	return serveAgents(t, newAgentServer(t), nil)
}

// newAgentServer builds a server over the test fixtures and memory stores,
// for a test to serve agents from.
func newAgentServer(t *testing.T) *Server {
	t.Helper()

	reg, led := fixtures(t)

	return New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))
}

// serveAgents serves srv's agent listener on a free loopback port, over TLS
// when tlsConfig is set, and its API, until the test ends.
func serveAgents(t *testing.T, srv *Server, tlsConfig *tls.Config) *agentHarness {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}

	return serveOn(t, srv, listener)
}

// serveOn serves srv's agents on listener, and its API, until the test ends.
func serveOn(t *testing.T, srv *Server, listener net.Listener) *agentHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = srv.ServeAgents(ctx, listener)
	}()

	httpServer := httptest.NewServer(srv.Handler())

	t.Cleanup(func() {
		httpServer.Close()
		cancel()
		<-done
	})

	return &agentHarness{srv: srv, address: listener.Addr().String(), api: httpServer.URL}
}

// flakyLoopback opens a loopback listener on a free port that fails failures
// accepts and drops drops sessions, then accepts as any listener.
func flakyLoopback(t *testing.T, failures, drops int32) *flakyListener {
	t.Helper()

	var lc net.ListenConfig

	inner, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	listener := &flakyListener{Listener: inner}
	listener.failures.Store(failures)
	listener.drops.Store(drops)

	return listener
}

// connect runs an agent named name against the harness until the returned
// function stops it.
func (h *agentHarness) connect(t *testing.T, name string, exec *executortest.Fake) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	a := agent.New(
		&agent.Config{Server: h.address, Pool: "homelab", Name: name, Labels: map[string]string{"gpu": "no"}},
		fingerprint.Node{Architecture: "amd64", CPU: 4000, Memory: 8192},
		exec, slog.New(slog.DiscardHandler),
	)

	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()

	stop := func() {
		cancel()
		<-done
	}

	t.Cleanup(stop)

	return stop
}

// await polls cond until it holds, or fails the test after five seconds.
func await(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// node waits for the named node to connect and returns its connection.
func (h *agentHarness) node(t *testing.T, name string) *nodes.Conn {
	t.Helper()

	var found *nodes.Conn

	await(t, "node "+name, func() bool {
		found, _ = h.srv.nodeConns.Get(name)

		return found != nil
	})

	return found
}

// recordExecution stores an execution under a running dispatch, released or
// not, and returns its ID.
func (h *agentHarness) recordExecution(t *testing.T, released bool) string {
	t.Helper()

	ids := [2]execution.ID{}
	for i := range ids {
		id, err := execution.NewID()
		if err != nil {
			t.Fatalf("NewID() = %v", err)
		}

		ids[i] = id
	}

	d := &execution.Dispatch{
		ID: ids[0], State: execution.DispatchRunning, Owner: "test", Epoch: 1, LeaseUntil: time.Now().Add(time.Hour),
	}
	if err := h.srv.executions.CreateDispatch(t.Context(), d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	rec := &execution.Record{ID: ids[1], State: execution.StateRunning, Dispatch: d.ID}
	if released {
		rec.Released = time.Now()
	}

	if err := h.srv.executions.Create(t.Context(), rec, d.Lease()); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return rec.ID.String()
}

// -------------------------------------------------------------------------
// SERVING
// -------------------------------------------------------------------------

// Accepts that fail, as when the process runs out of file descriptors, are
// retried, so an agent still registers once they pass.
func TestServeAgents_RetriesFailedAccepts(t *testing.T) {
	h := serveOn(t, newAgentServer(t), flakyLoopback(t, 3, 0))
	h.connect(t, "box1", finishing())
	h.node(t, "box1")
}

// A server shutting down while accepts keep failing stops cleanly, from the
// wait between them.
func TestServeAgents_StopsDuringTheRetryWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if err := newAgentServer(t).ServeAgents(ctx, flakyLoopback(t, math.MaxInt32, 0)); err != nil {
		t.Errorf("ServeAgents() = %v, want nil once ctx ended", err)
	}
}

// An agent whose session is cut off before it got going dials again after
// its backoff, and registers.
func TestAgent_DialsAgainAfterAFailedSession(t *testing.T) {
	h := serveOn(t, newAgentServer(t), flakyLoopback(t, 0, 1))
	h.connect(t, "box1", finishing())
	h.node(t, "box1")
}

// An agent whose registered session the server drops reconnects and
// registers again.
func TestAgent_ReconnectsAfterTheServerDropsIt(t *testing.T) {
	h := newAgentHarness(t)
	h.connect(t, "box1", finishing())
	h.node(t, "box1")

	h.srv.nodeConns.CloseAll()

	await(t, "box1 to leave", func() bool {
		conn, _ := h.srv.nodeConns.Get("box1")

		return conn == nil
	})

	h.node(t, "box1")
}

// A listener closed while serving is an error, since no agent could connect
// again; ctx ending is not.
func TestServeAgents_ClosedListenerIsAnError(t *testing.T) {
	srv := newAgentServer(t)

	closed := flakyLoopback(t, 0, 0)
	_ = closed.Close()

	if err := srv.ServeAgents(t.Context(), closed); !errors.Is(err, net.ErrClosed) {
		t.Errorf("ServeAgents() on a closed listener = %v, want net.ErrClosed", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := srv.ServeAgents(ctx, flakyLoopback(t, 0, 0)); err != nil {
		t.Errorf("ServeAgents() after ctx ended = %v, want nil", err)
	}
}

// -------------------------------------------------------------------------
// REGISTRATION
// -------------------------------------------------------------------------

// An agent registers its node with what it reported.
func TestAgent_Registers(t *testing.T) {
	h := newAgentHarness(t)

	h.connect(t, "box1", finishing())
	node := h.node(t, "box1").Node

	if node.GetPool() != "homelab" || node.GetCapacity().GetCpu() != 4000 || node.GetLabels()["gpu"] != "no" {
		t.Errorf("registered = %+v", node)
	}
}

// A node registering is told to release what no execution needs: a workload
// whose execution was released, one never recorded, and one that is no
// execution's. One whose execution is still live it keeps.
func TestAgent_ReleasesWhatNoExecutionNeeds(t *testing.T) {
	h := newAgentHarness(t)

	live := h.recordExecution(t, false)
	released := h.recordExecution(t, true)

	unrecorded, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	exec := finishing()
	for _, id := range []string{live, released, unrecorded.String(), "not-an-id"} {
		_ = exec.Submit(t.Context(), id, &executor.Spec{})
	}

	h.connect(t, "box1", exec)

	for _, id := range []string{released, unrecorded.String(), "not-an-id"} {
		await(t, id+" to be released", func() bool { return exec.Workload(id) == nil })
	}

	if exec.Workload(live) == nil {
		t.Error("the workload of a live execution was released")
	}
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// The server calls a node's executions back down its session: submit, read,
// stream output, cancel, release, and NotFound for what it does not hold.
func TestAgent_ExecutionsAreCalledBack(t *testing.T) {
	h := newAgentHarness(t)
	exec := finishing()
	exec.ExitCode = 3

	h.connect(t, "box1", exec)
	calls := h.node(t, "box1").Executions
	ctx := t.Context()

	// Live in the store, so registering does not release it mid-test
	run := h.recordExecution(t, false)

	sub, err := calls.Submit(ctx, &agentrpc.SubmitRequest{
		ExecutionId: run,
		Workload: &agentrpc.Workload{
			Image: "alpine:3.20", Args: []string{"echo", "hi"},
			Resources: &agentrpc.Resources{Cpu: 1000, Memory: 512},
		},
	})
	if err != nil || sub.GetState() != string(execution.StateSucceeded) {
		t.Fatalf("Submit() = %+v, %v", sub, err)
	}

	if spec := exec.Workload(run); spec == nil || spec.Image != "alpine:3.20" {
		t.Errorf("the executor got %+v", spec)
	}

	res, err := calls.Result(ctx, &agentrpc.ExecutionRequest{ExecutionId: run})
	if err != nil || res.GetExitCode() != 3 {
		t.Errorf("Result() = %+v, %v; want exit 3", res, err)
	}

	stream, err := calls.StreamLogs(ctx, &agentrpc.LogsRequest{ExecutionId: run, Offset: 2})
	if err != nil {
		t.Fatalf("StreamLogs() = %v", err)
	}

	chunk, err := stream.Recv()
	if err != nil || string(chunk.GetData()) != "llo\n" {
		t.Errorf("first chunk from offset 2 = %q, %v", chunk.GetData(), err)
	}

	if _, err := calls.Cancel(ctx, &agentrpc.ExecutionRequest{ExecutionId: run}); err != nil {
		t.Errorf("Cancel() = %v", err)
	}

	st, err := calls.Status(ctx, &agentrpc.ExecutionRequest{ExecutionId: run})
	if err != nil || st.GetState() != string(execution.StateCancelled) {
		t.Errorf("Status() after cancel = %+v, %v", st, err)
	}

	if _, err := calls.Release(ctx, &agentrpc.ExecutionRequest{ExecutionId: run}); err != nil {
		t.Errorf("Release() = %v", err)
	}

	_, err = calls.Status(ctx, &agentrpc.ExecutionRequest{ExecutionId: run})
	if status.Code(err) != codes.NotFound {
		t.Errorf("Status() after release = %v, want NotFound", err)
	}
}

// A workload bigger than the node has room for is refused by the agent itself.
func TestAgent_RefusesWhatDoesNotFit(t *testing.T) {
	h := newAgentHarness(t)

	h.connect(t, "box1", finishing())
	calls := h.node(t, "box1").Executions

	_, err := calls.Submit(t.Context(), &agentrpc.SubmitRequest{
		ExecutionId: "too-big",
		Workload:    &agentrpc.Workload{Image: "alpine:3.20", Resources: &agentrpc.Resources{Cpu: 1000, Memory: 16384}},
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("Submit() = %v, want ResourceExhausted", err)
	}
}

// -------------------------------------------------------------------------
// NODES
// -------------------------------------------------------------------------

// GET /v1/nodes lists connected nodes, and a node whose agent stops leaves the
// list.
func TestAgent_NodesListAndLeave(t *testing.T) {
	h := newAgentHarness(t)
	stop := h.connect(t, "box1", finishing())
	h.connect(t, "box2", finishing())

	h.node(t, "box1")
	h.node(t, "box2")

	apiClient, err := api.NewClient(h.api, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	listed, err := apiClient.Nodes(t.Context())
	if err != nil || len(listed) != 2 || listed[0].Name != "box1" || listed[1].Pool != "homelab" {
		t.Fatalf("Nodes() = %+v, %v; want box1 and box2", listed, err)
	}

	stop()

	await(t, "box1 to leave", func() bool { return len(h.srv.nodeConns.All()) == 1 })
}

// An agent reconnecting under its name replaces its old connection rather than
// adding a second node.
func TestAgent_ReconnectReplaces(t *testing.T) {
	h := newAgentHarness(t)

	stop := h.connect(t, "box1", finishing())
	first := h.node(t, "box1")

	stop()
	await(t, "box1 to leave", func() bool { return len(h.srv.nodeConns.All()) == 0 })

	h.connect(t, "box1", finishing())

	await(t, "box1 to return", func() bool {
		connected := h.srv.nodeConns.All()

		return len(connected) == 1 && connected[0] != first
	})
}
