// -------------------------------------------------------------------------------
// Pool Provider Tests
//
// Author: Alex Freidah
//
// Real agents over TCP, with fake executors, registered into a node set the
// pool reads: capabilities following the nodes, placement by room, the room a
// reservation holds, and a node that leaves mid-run.
// -------------------------------------------------------------------------------

package pool

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"google.golang.org/grpc"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// DOUBLES
// -------------------------------------------------------------------------

// fakeExecutor holds workloads in memory, each running until released.
type fakeExecutor struct {
	mu        sync.Mutex
	workloads map[string]*executor.Spec
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{workloads: map[string]*executor.Spec{}}
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

	return execution.Status{State: execution.StateRunning, ProviderID: id}, nil
}

func (f *fakeExecutor) Result(context.Context, string) (*execution.Result, error) {
	return nil, errors.New("still running")
}

func (f *fakeExecutor) Cancel(context.Context, string) error { return nil }

func (f *fakeExecutor) Release(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.workloads, id)

	return nil
}

func (f *fakeExecutor) Logs(context.Context, string, io.Writer) error { return nil }

// Held reports every workload as running with what it declared.
func (f *fakeExecutor) Held(context.Context) ([]executor.Held, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	held := make([]executor.Held, 0, len(f.workloads))
	for id, spec := range f.workloads {
		held = append(held, executor.Held{ID: id, CPU: spec.CPU, Memory: spec.Memory, Running: true})
	}

	return held, nil
}

func (f *fakeExecutor) Runtimes() []string { return []string{executor.DefaultRuntime} }

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// harness is a listener registering agents into conns, as a server does, and
// the pool over them.
type harness struct {
	conns   *nodes.Conns
	pool    *Provider
	address string
}

// registrar records a session's registrations into conns.
type registrar struct {
	agentrpc.UnimplementedNodeServer

	conns   *nodes.Conns
	session *agentrpc.Session
}

func (r *registrar) Register(_ context.Context, req *agentrpc.NodeRegisterRequest) (*agentrpc.NodeRegisterResponse, error) {
	r.conns.Report(req, r.session)

	return &agentrpc.NodeRegisterResponse{}, nil
}

// newHarness accepts agents on a real port into a pool named homelab.
func newHarness(t *testing.T) *harness {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	conns := nodes.New()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				session, err := agentrpc.Accept(conn)
				if err != nil {
					return
				}

				srv := grpc.NewServer()
				agentrpc.RegisterNodeServer(srv, &registrar{conns: conns, session: session})

				go func() { _ = session.Serve(srv) }()

				<-session.Done()
				srv.Stop()
				conns.Remove(session)
			}()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()
		conns.CloseAll()
	})

	return &harness{conns: conns, pool: New("homelab", conns), address: listener.Addr().String()}
}

// join runs an agent named name with the given memory in the pool until the
// returned function stops it.
func (h *harness) join(t *testing.T, name string, memory int64, exec *fakeExecutor) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	a := agent.New(&agent.Config{Server: h.address, Pool: "homelab", Name: name},
		fingerprint.Node{Architecture: "amd64", CPU: 8000, Memory: memory},
		exec, slog.New(slog.DiscardHandler))

	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()

	stop := func() {
		cancel()
		<-done
	}

	t.Cleanup(stop)

	await(t, name+" to join", func() bool {
		_, ok := h.conns.Get(name)

		return ok
	})

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

// task is a container task declaring memory MiB.
func task(t *testing.T, memory int) *job.Task {
	t.Helper()

	return &job.Task{
		Name:      "work",
		Driver:    job.DriverContainer,
		Config:    rawBlock(t, `image = "alpine:3.20"`),
		Resources: &job.Resources{CPU: new(500), Memory: new(memory)},
	}
}

// rawBlock parses a driver config the way the job parser leaves it: decoded as
// a block, with its attributes still expressions.
func rawBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing config failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

// freeMemory is a member's free memory in the pool's live capabilities.
func (h *harness) freeMemory(t *testing.T, name string) int {
	t.Helper()

	caps, err := h.pool.LiveCapabilities()
	if err != nil {
		t.Fatalf("LiveCapabilities() = %v", err)
	}

	for i := range caps.Members {
		if caps.Members[i].Name == name {
			return caps.Members[i].Capabilities.MaxResources.Memory
		}
	}

	t.Fatalf("no member %s", name)

	return 0
}

// newExecutionID mints an execution ID.
func newExecutionID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// An empty pool is an error, which marks it unhealthy; a node joining makes it
// a member at once.
func TestPool_CapabilitiesFollowNodes(t *testing.T) {
	h := newHarness(t)

	if _, err := h.pool.LiveCapabilities(); err == nil {
		t.Error("an empty pool reported capabilities")
	}

	h.join(t, "box1", 4096, newFakeExecutor())

	caps, err := h.pool.LiveCapabilities()
	if err != nil || len(caps.Members) != 1 || caps.Members[0].Name != "box1" {
		t.Fatalf("LiveCapabilities() = %+v, %v; want box1 as a member", caps, err)
	}

	if !caps.Members[0].Capabilities.SupportsArch(job.ArchAMD64) || caps.Members[0].Capabilities.MaxResources.Memory != 4096 {
		t.Errorf("member = %+v", caps.Members[0].Capabilities)
	}
}

// Work goes to the node with the most room, and only to members admission
// passed.
func TestPool_PlacesOnMostRoom(t *testing.T) {
	h := newHarness(t)
	h.join(t, "small", 2048, newFakeExecutor())
	h.join(t, "big", 8192, newFakeExecutor())

	sub, err := h.pool.Submit(t.Context(), newExecutionID(t), task(t, 1024))
	if err != nil || sub.ProviderID != "big" {
		t.Errorf("Submit() = %+v, %v; want it on big", sub, err)
	}

	sub, err = h.pool.SubmitTo(t.Context(), newExecutionID(t), task(t, 1024), []string{"small"})
	if err != nil || sub.ProviderID != "small" {
		t.Errorf("SubmitTo(small) = %+v, %v; want it on small", sub, err)
	}
}

// Placed work takes its room until released, and a pool with no room left is
// an infrastructure failure, so dispatch tries elsewhere.
func TestPool_RoomIsTakenAndReturned(t *testing.T) {
	h := newHarness(t)
	h.join(t, "box1", 4096, newFakeExecutor())

	id := newExecutionID(t)

	if _, err := h.pool.Submit(t.Context(), id, task(t, 3072)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if free := h.freeMemory(t, "box1"); free != 1024 {
		t.Errorf("free after placing = %d MiB, want 1024", free)
	}

	_, err := h.pool.Submit(t.Context(), newExecutionID(t), task(t, 2048))
	if !plugin.Reroutable(err) {
		t.Errorf("Submit() with no room = %v, want an infrastructure failure", err)
	}

	if err := h.pool.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	await(t, "the room to come back", func() bool { return h.freeMemory(t, "box1") == 4096 })
}

// A node that leaves mid-run is lost within the grace period, and gone after.
func TestPool_NodeLeavingMidRun(t *testing.T) {
	h := newHarness(t)
	stop := h.join(t, "box1", 4096, newFakeExecutor())

	id := newExecutionID(t)

	if _, err := h.pool.Submit(t.Context(), id, task(t, 1024)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	st, err := h.pool.Status(t.Context(), id)
	if err != nil || st.State != execution.StateRunning || st.ProviderID != "box1" {
		t.Fatalf("Status() = %+v, %v; want running on box1", st, err)
	}

	stop()
	await(t, "box1 to leave", func() bool { _, ok := h.conns.Get("box1"); return !ok })

	if st, err := h.pool.Status(t.Context(), id); err != nil || st.State != execution.StateLost {
		t.Errorf("Status() within the grace = %+v, %v; want lost", st, err)
	}

	// Past the grace, the node is given up on.
	h.pool.now = func() time.Time { return time.Now().Add(leftGrace + time.Minute) }

	if _, err := h.pool.Status(t.Context(), id); !plugin.Reroutable(err) {
		t.Errorf("Status() after the grace = %v, want an infrastructure failure", err)
	}
}
