// -------------------------------------------------------------------------------
// Client Connection Tests
//
// Author: Alex Freidah
//
// A real client dialling a real server over TCP, with a fake executor behind
// the client: registration, calling executions back down the session, node
// listing, and a node leaving when its client stops.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/client"
	"github.com/afreidah/vagabond/internal/client/executor"
	"github.com/afreidah/vagabond/internal/client/fingerprint"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// DOUBLES
// -------------------------------------------------------------------------

// fakeExecutor holds workloads in memory: each finishes the moment it is
// submitted, exiting with exitCode and printing output.
type fakeExecutor struct {
	mu        sync.Mutex
	workloads map[string]*executor.Spec
	cancelled map[string]bool
	exitCode  int
	output    string
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{workloads: map[string]*executor.Spec{}, cancelled: map[string]bool{}, output: "hello\n"}
}

func (f *fakeExecutor) Submit(_ context.Context, id string, spec *executor.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.workloads[id] = spec

	return nil
}

func (f *fakeExecutor) Status(_ context.Context, id string) (execution.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.workloads[id]; !ok {
		return execution.Status{}, executor.ErrUnknown
	}

	state := execution.StateSucceeded
	if f.cancelled[id] {
		state = execution.StateCancelled
	}

	return execution.Status{State: state, ProviderID: id}, nil
}

func (f *fakeExecutor) Result(ctx context.Context, id string) (*execution.Result, error) {
	if _, err := f.Status(ctx, id); err != nil {
		return nil, err
	}

	return &execution.Result{ExitCode: new(f.exitCode), Duration: time.Second, Logs: []byte(f.output)}, nil
}

func (f *fakeExecutor) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cancelled[id] = true

	return nil
}

func (f *fakeExecutor) Release(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.workloads, id)

	return nil
}

func (f *fakeExecutor) Logs(_ context.Context, _ string, w io.Writer) error {
	_, err := io.WriteString(w, f.output)

	return err
}

func (f *fakeExecutor) List(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.workloads))
	for id := range f.workloads {
		ids = append(ids, id)
	}

	return ids, nil
}

func (f *fakeExecutor) Runtimes() []string { return []string{executor.DefaultRuntime} }

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// clientHarness is a server accepting clients on a real port.
type clientHarness struct {
	srv     *Server
	address string
	api     string
}

// newClientHarness starts a server serving clients and its API, stopped when
// the test ends.
func newClientHarness(t *testing.T) *clientHarness {
	t.Helper()

	reg, led := fixtures(t)
	srv := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = srv.ServeClients(ctx, listener)
	}()

	httpServer := httptest.NewServer(srv.Handler())

	t.Cleanup(func() {
		httpServer.Close()
		cancel()
		<-done
	})

	return &clientHarness{srv: srv, address: listener.Addr().String(), api: httpServer.URL}
}

// connect runs a client named name against the harness until the returned
// function stops it.
func (h *clientHarness) connect(t *testing.T, name string, exec *fakeExecutor) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	c := client.New(
		&client.Config{Server: h.address, Pool: "homelab", Name: name, Labels: map[string]string{"gpu": "no"}},
		fingerprint.Node{Architecture: "amd64", CPU: 4000, Memory: 8192},
		exec, slog.New(slog.DiscardHandler),
	)

	go func() {
		defer close(done)
		_ = c.Run(ctx)
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
func (h *clientHarness) node(t *testing.T, name string) *nodeConnState {
	t.Helper()

	var found *nodeConnState

	await(t, "node "+name, func() bool {
		for _, conn := range h.srv.connectedNodes() {
			if conn.Node.GetName() == name {
				found = conn

				return true
			}
		}

		return false
	})

	return found
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// A client registers its node with what it reported, including the workloads
// it already holds.
func TestClient_Registers(t *testing.T) {
	h := newClientHarness(t)
	exec := newFakeExecutor()
	exec.workloads["held-before"] = &executor.Spec{}

	h.connect(t, "box1", exec)
	conn := h.node(t, "box1")

	node := conn.Node
	if node.GetPool() != "homelab" || node.GetCapacity().GetCpu() != 4000 || node.GetLabels()["gpu"] != "no" {
		t.Errorf("registered = %+v", node)
	}

	if got := node.GetExecutions(); len(got) != 1 || got[0] != "held-before" {
		t.Errorf("executions = %v, want the held workload", got)
	}
}

// The server calls a node's executions back down its session: submit, read,
// stream output, cancel, release, and NotFound for what it does not hold.
func TestClient_ExecutionsAreCalledBack(t *testing.T) {
	h := newClientHarness(t)
	exec := newFakeExecutor()
	exec.exitCode = 3

	h.connect(t, "box1", exec)
	calls := h.node(t, "box1").Executions
	ctx := t.Context()

	sub, err := calls.Submit(ctx, &agentrpc.SubmitRequest{
		ExecutionId: "run-1",
		Workload:    &agentrpc.Workload{Image: "alpine:3.20", Args: []string{"echo", "hi"}},
	})
	if err != nil || sub.GetState() != string(execution.StateSucceeded) {
		t.Fatalf("Submit() = %+v, %v", sub, err)
	}

	if exec.workloads["run-1"].Image != "alpine:3.20" {
		t.Errorf("the executor got %+v", exec.workloads["run-1"])
	}

	res, err := calls.Result(ctx, &agentrpc.ExecutionRequest{ExecutionId: "run-1"})
	if err != nil || res.GetExitCode() != 3 || string(res.GetLogs()) != "hello\n" {
		t.Errorf("Result() = %+v, %v; want exit 3 with output", res, err)
	}

	stream, err := calls.StreamLogs(ctx, &agentrpc.ExecutionRequest{ExecutionId: "run-1"})
	if err != nil {
		t.Fatalf("StreamLogs() = %v", err)
	}

	chunk, err := stream.Recv()
	if err != nil || string(chunk.GetData()) != "hello\n" {
		t.Errorf("first chunk = %q, %v", chunk.GetData(), err)
	}

	if _, err := calls.Cancel(ctx, &agentrpc.ExecutionRequest{ExecutionId: "run-1"}); err != nil {
		t.Errorf("Cancel() = %v", err)
	}

	st, err := calls.Status(ctx, &agentrpc.ExecutionRequest{ExecutionId: "run-1"})
	if err != nil || st.GetState() != string(execution.StateCancelled) {
		t.Errorf("Status() after cancel = %+v, %v", st, err)
	}

	if _, err := calls.Release(ctx, &agentrpc.ExecutionRequest{ExecutionId: "run-1"}); err != nil {
		t.Errorf("Release() = %v", err)
	}

	_, err = calls.Status(ctx, &agentrpc.ExecutionRequest{ExecutionId: "run-1"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("Status() after release = %v, want NotFound", err)
	}
}

// GET /v1/nodes lists connected nodes, and a node whose client stops leaves the
// list.
func TestClient_NodesListAndLeave(t *testing.T) {
	h := newClientHarness(t)
	stop := h.connect(t, "box1", newFakeExecutor())
	h.connect(t, "box2", newFakeExecutor())

	h.node(t, "box1")
	h.node(t, "box2")

	apiClient, err := api.NewClient(h.api, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	nodes, err := apiClient.Nodes(t.Context())
	if err != nil || len(nodes) != 2 || nodes[0].Name != "box1" || nodes[1].Pool != "homelab" {
		t.Fatalf("Nodes() = %+v, %v; want box1 and box2", nodes, err)
	}

	stop()

	await(t, "box1 to leave", func() bool { return len(h.srv.connectedNodes()) == 1 })
}

// A client reconnecting under its name replaces its old connection rather than
// adding a second node.
func TestClient_ReconnectReplaces(t *testing.T) {
	h := newClientHarness(t)

	stop := h.connect(t, "box1", newFakeExecutor())
	first := h.node(t, "box1")

	stop()
	await(t, "box1 to leave", func() bool { return len(h.srv.connectedNodes()) == 0 })

	h.connect(t, "box1", newFakeExecutor())

	await(t, "box1 to return", func() bool {
		nodes := h.srv.connectedNodes()

		return len(nodes) == 1 && nodes[0] != first
	})
}
