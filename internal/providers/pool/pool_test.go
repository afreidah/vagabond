// -------------------------------------------------------------------------------
// containerd Provider Tests
//
// Author: Alex Freidah
//
// Real agents over TCP, with fake executors, served by the node set the
// provider reads: capabilities following the nodes in placement order, the
// room a reservation holds, a node that leaves mid-run, workloads a returning
// node is told to release, and output streamed back.
// -------------------------------------------------------------------------------

package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/agenttest"
	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/executor/executortest"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// harness is a node set serving agents on a real port, and a provider over
// them named homelab.
type harness struct {
	conns   *nodes.Conns
	pool    *Provider
	address string
}

// newHarness serves agents into a node set, answering each registration with
// what unwanted says to release.
func newHarness(t *testing.T, unwanted nodes.Unwanted) *harness {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	conns := nodes.New()

	go func() { _ = conns.Serve(t.Context(), listener, unwanted, slog.New(slog.DiscardHandler)) }()

	return &harness{conns: conns, pool: provider(t, TypeContainerd, "", conns), address: listener.Addr().String()}
}

// provider builds homelab of providerType over conns from a config block's
// source, which may be empty.
func provider(t *testing.T, providerType, src string, conns *nodes.Conns) *Provider {
	t.Helper()

	var body hcl.Body

	if src != "" {
		f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatalf("parsing config failed: %s", diags.Error())
		}

		body = f.Body
	}

	p, diags := New("homelab", providerType, body, conns)
	if diags.HasErrors() {
		t.Fatalf("New() = %s", diags.Error())
	}

	return p
}

// join runs an agent named name with the given memory in the pool until the
// returned function stops it.
func (h *harness) join(t *testing.T, name string, memory int64, exec *executortest.Fake) func() {
	t.Helper()

	stop := agenttest.Run(t, &agent.Config{Server: h.address, Pool: "homelab", Name: name},
		fingerprint.Node{Architecture: "amd64", CPU: 8000, Memory: memory}, exec)

	testkit.Eventually(t, 5*time.Second, name+" to join", func() bool {
		_, ok := h.conns.Get(name)

		return ok
	})

	return stop
}

// task is a container task declaring memory MiB.
func task(t *testing.T, memory int) *job.Task {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(`image = "alpine:3.20"`), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing config failed: %s", diags.Error())
	}

	return &job.Task{
		Name:      "work",
		Driver:    job.DriverContainer,
		Config:    &job.RawBlock{Body: f.Body},
		Resources: &job.Resources{CPU: new(500), Memory: new(memory)},
	}
}

// place reserves room on node and submits the task there, as dispatch does.
func (h *harness) place(t *testing.T, node string, memory int) execution.ID {
	t.Helper()

	id := testkit.NewID(t)
	on := h.pool.Target(node)

	if err := on.(plugin.Reserver).Reserve(id, task(t, memory)); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	if _, err := on.Submit(t.Context(), id, task(t, memory)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	return id
}

// members is the provider's live members by name, in order.
func (h *harness) members(t *testing.T) []string {
	t.Helper()

	caps, err := h.pool.LiveCapabilities()
	if err != nil {
		t.Fatalf("LiveCapabilities() = %v", err)
	}

	names := make([]string, 0, len(caps.Members))
	for i := range caps.Members {
		names = append(names, caps.Members[i].Name)
	}

	return names
}

// freeMemory is a node's free memory as admission sees it.
func (h *harness) freeMemory(t *testing.T, name string) int {
	t.Helper()

	caps, err := h.pool.LiveCapabilities()
	if err != nil {
		t.Fatalf("LiveCapabilities() = %v", err)
	}

	for i := range caps.Members {
		if caps.Members[i].Name == name {
			return *caps.Members[i].Capabilities.MaxResources.Memory
		}
	}

	t.Fatalf("no member %s", name)

	return 0
}

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// The block is optional: the pool defaults to the label and placement to
// spread, and an unknown placement is refused.
func TestNew_Config(t *testing.T) {
	if p := provider(t, TypeContainerd, "", nodes.New()); p.cfg.Pool != "homelab" || p.cfg.Placement != PlacementSpread {
		t.Errorf("defaults = %+v, want pool homelab and spread", p.cfg)
	}

	if p := provider(t, TypeFirecracker, "pool = \"lab\"\nplacement = \"pack\"", nodes.New()); p.cfg.Pool != "lab" || p.cfg.Placement != PlacementPack {
		t.Errorf("config = %+v, want pool lab and pack", p.cfg)
	}

	for _, src := range []string{`placement = "random"`, `colour = "red"`} {
		f, _ := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
		if p, diags := New("homelab", TypeContainerd, f.Body, nodes.New()); p != nil || !diags.HasErrors() {
			t.Errorf("New(%s) = %v, %s; want refused", src, p, diags.Error())
		}
	}
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// An empty pool is an error, which marks it unhealthy; a node joining makes it
// a member at once.
func TestPool_CapabilitiesFollowNodes(t *testing.T) {
	h := newHarness(t, nil)

	if _, err := h.pool.LiveCapabilities(); err == nil {
		t.Error("an empty pool reported capabilities")
	}

	h.join(t, "box1", 4096, &executortest.Fake{})

	caps, err := h.pool.Capabilities(t.Context())
	if err != nil || len(caps.Members) != 1 || caps.Members[0].Name != "box1" || h.pool.Name() != "homelab" {
		t.Fatalf("Capabilities() of %s = %+v, %v; want box1 as a member", h.pool.Name(), caps, err)
	}

	member := &caps.Members[0].Capabilities
	if !member.SupportsArch(job.ArchAMD64) || *member.MaxResources.Memory != 4096 || member.Isolation != plugin.IsolationContainer {
		t.Errorf("member = %+v", member)
	}

	// Admission sizes an undeclared task as placement does.
	if member.DefaultResources != defaultResources {
		t.Errorf("default resources = %+v, want %+v", member.DefaultResources, defaultResources)
	}
}

// A firecracker provider is made of the pool's nodes running firecracker
// alone. It publishes their isolation and room after the microVM's overhead,
// charges the overhead when it reserves, and names its executor to the node.
func TestPool_FirecrackerTakesItsNodesAndOverhead(t *testing.T) {
	h := newHarness(t, nil)
	vm := &executortest.Fake{Name: TypeFirecracker, Overhead: &agentrpc.Resources{Memory: 32}}

	h.join(t, "box1", 4096, &executortest.Fake{})
	h.join(t, "box2", 4096, vm)

	micro := provider(t, TypeFirecracker, "", h.conns)

	caps, err := micro.LiveCapabilities()
	if err != nil || len(caps.Members) != 1 || caps.Members[0].Name != "box2" {
		t.Fatalf("LiveCapabilities() = %+v, %v; want box2 alone", caps, err)
	}

	member := &caps.Members[0].Capabilities
	if *member.MaxResources.Memory != 4064 || member.Isolation != plugin.IsolationVM {
		t.Errorf("member = %+v, want 4064 MiB of room and vm isolation", member)
	}

	id := testkit.NewID(t)
	on := micro.Target("box2")

	if err := on.(plugin.Reserver).Reserve(id, task(t, 4064)); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	if _, memory := h.conns.Free("box2"); memory != 0 {
		t.Errorf("free after reserving 4064 MiB with 32 overhead = %d MiB, want 0", memory)
	}

	if _, err := on.Submit(t.Context(), id, task(t, 4064)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if got := vm.Workload(id.String()).GetExecutor(); got != TypeFirecracker {
		t.Errorf("the node was sent a workload for %q, want %q", got, TypeFirecracker)
	}
}

// Members are listed most free memory first under spread, least under pack.
func TestPool_PlacementOrdersNodes(t *testing.T) {
	h := newHarness(t, nil)
	h.join(t, "small", 2048, &executortest.Fake{})
	h.join(t, "big", 8192, &executortest.Fake{})

	if got := h.members(t); !slices.Equal(got, []string{"big", "small"}) {
		t.Errorf("spread members = %v, want big first", got)
	}

	h.pool.cfg.Placement = PlacementPack

	if got := h.members(t); !slices.Equal(got, []string{"small", "big"}) {
		t.Errorf("pack members = %v, want small first", got)
	}
}

// A full node publishes zero room, a limit admission enforces, not an absent
// one it would read as unlimited.
func TestPool_FullNodePublishesZeroRoom(t *testing.T) {
	h := newHarness(t, nil)
	h.join(t, "box1", 4096, &executortest.Fake{})

	h.place(t, "box1", 4096)

	caps, err := h.pool.LiveCapabilities()
	if err != nil {
		t.Fatalf("LiveCapabilities() = %v", err)
	}

	room := caps.Members[0].Capabilities.MaxResources
	if room.Memory == nil || *room.Memory != 0 {
		t.Errorf("room = %+v; want a memory limit of zero", room)
	}
}

// -------------------------------------------------------------------------
// ROOM
// -------------------------------------------------------------------------

// A reservation takes room until the node reports the workload, which then
// takes it until released; a node without the room, or not connected, is
// ErrNoRoom.
func TestPool_RoomIsTakenAndReturned(t *testing.T) {
	h := newHarness(t, nil)
	h.join(t, "box1", 4096, &executortest.Fake{})

	id := h.place(t, "box1", 3072)

	if free := h.freeMemory(t, "box1"); free != 1024 {
		t.Errorf("free after placing = %d MiB, want 1024", free)
	}

	refused := []string{"box1", "gone"}
	for _, node := range refused {
		if err := h.pool.Target(node).(plugin.Reserver).Reserve(testkit.NewID(t), task(t, 2048)); !errors.Is(err, plugin.ErrNoRoom) {
			t.Errorf("Reserve() on %s = %v, want ErrNoRoom", node, err)
		}
	}

	if err := h.pool.Target("box1").(plugin.Releaser).Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	testkit.Eventually(t, 5*time.Second, "the room to come back", func() bool { return h.freeMemory(t, "box1") == 4096 })
}

// A node that refuses a submission, here for room its own count lacks, gives
// the reservation back, and so does a task that names no image, which fails
// as internal before anything is sent.
func TestPool_RefusedSubmissionUnreserves(t *testing.T) {
	h := newHarness(t, nil)
	h.join(t, "box1", 4096, &executortest.Fake{})

	on := h.pool.Target("box1")

	noImage := task(t, 1024)
	noImage.Config = &job.RawBlock{Body: hcl.EmptyBody()}

	submissions := map[string]struct {
		task     *job.Task
		internal bool
	}{
		"past the node's size": {task(t, 8192), false},
		"with no image":        {noImage, true},
	}

	for name, s := range submissions {
		id := testkit.NewID(t)

		if err := on.(plugin.Reserver).Reserve(id, task(t, 1024)); err != nil {
			t.Fatalf("Reserve() = %v", err)
		}

		_, err := on.Submit(t.Context(), id, s.task)
		if err == nil || plugin.Reroutable(err) == s.internal {
			t.Errorf("Submit() %s = %v, want internal %t", name, err, s.internal)
		}

		if free := h.freeMemory(t, "box1"); free != 4096 {
			t.Errorf("free after the submission %s = %d MiB, want all of it", name, free)
		}
	}
}

// A workload the node finds no node could run, such as one with an image
// reference that does not parse, fails as internal rather than moving on.
func TestPool_AWorkloadNoNodeCouldRunStays(t *testing.T) {
	h := newHarness(t, nil)
	h.join(t, "box1", 4096, &executortest.Fake{Pull: func(context.Context, string) error {
		return fmt.Errorf("%w: image %q", executor.ErrInvalid, "Not A Reference")
	}})

	_, err := h.pool.Target("box1").Submit(t.Context(), testkit.NewID(t), task(t, 1024))
	if failure, ok := errors.AsType[*plugin.Error](err); !ok || failure.Class != plugin.ClassInternal {
		t.Errorf("Submit() = %v, want an internal failure", err)
	}
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// A node that leaves mid-run cannot answer for its executions, which leaves
// them lost to the runner; releasing one waits for the node to return.
func TestPool_NodeLeavingMidRun(t *testing.T) {
	h := newHarness(t, nil)
	stop := h.join(t, "box1", 4096, &executortest.Fake{})

	id := h.place(t, "box1", 1024)
	on := h.pool.Target("box1")

	st, err := on.Status(t.Context(), id)
	if err != nil || st.State != execution.StateRunning || st.ProviderID != id.String() {
		t.Fatalf("Status() = %+v, %v; want running", st, err)
	}

	stop()
	testkit.Eventually(t, 5*time.Second, "box1 to leave", func() bool { _, ok := h.conns.Get("box1"); return !ok })

	if _, err := on.Status(t.Context(), id); !plugin.Reroutable(err) {
		t.Errorf("Status() of a gone node = %v, want an infrastructure failure", err)
	}

	if _, err := on.Result(t.Context(), id); !plugin.Reroutable(err) {
		t.Errorf("Result() of a gone node = %v, want an infrastructure failure", err)
	}

	if err := on.Cancel(t.Context(), id); !plugin.Reroutable(err) {
		t.Errorf("Cancel() of a gone node = %v, want an infrastructure failure", err)
	}

	if err := on.(plugin.Releaser).Release(t.Context(), id); err != nil {
		t.Errorf("Release() of a gone node = %v, want it left to the node", err)
	}
}

// A node registering with workloads the server has no use for is told to
// release them, and does; the rest it keeps.
func TestPool_UnwantedWorkloadsAreReleased(t *testing.T) {
	unwanted := func(_ context.Context, held []*agentrpc.Held) []string {
		var out []string

		for _, h := range held {
			if strings.HasPrefix(h.GetExecutionId(), "old-") {
				out = append(out, h.GetExecutionId())
			}
		}

		return out
	}

	h := newHarness(t, unwanted)
	exec := &executortest.Fake{}

	for _, id := range []string{"old-1", "kept"} {
		_ = exec.Submit(t.Context(), id, &agentrpc.Workload{})
	}

	h.join(t, "box1", 4096, exec)

	testkit.Eventually(t, 5*time.Second, "old-1 to be released", func() bool { return exec.Workload("old-1") == nil })

	if exec.Workload("kept") == nil {
		t.Error("a wanted workload was released")
	}
}

// Results and cancellation reach the node; output streams from it and ends
// with the workload, and reading it whole is the stream from the start. A
// node that no longer holds the execution answers unknown, and a writer that
// fails is internal.
func TestPool_Executions(t *testing.T) {
	h := newHarness(t, nil)
	exec := &executortest.Fake{Output: "hello world\n"}
	h.join(t, "box1", 4096, exec)

	id := h.place(t, "box1", 1024)
	on := h.pool.Target("box1")

	if err := on.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}

	if res, err := on.Result(t.Context(), id); err != nil || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("Result() = %+v, %v; want exit code 0", res, err)
	}

	streamer := on.(plugin.LogStreamer)

	var out strings.Builder

	if err := streamer.StreamLogs(t.Context(), id, 0, &out); err != nil || out.String() != "hello world\n" {
		t.Errorf("StreamLogs() = %q, %v; want the node's output", out.String(), err)
	}

	var rest strings.Builder

	if err := streamer.StreamLogs(t.Context(), id, int64(len("hello ")), &rest); err != nil || rest.String() != "world\n" {
		t.Errorf("StreamLogs() from an offset = %q, %v; want the rest of the line", rest.String(), err)
	}

	var whole strings.Builder

	if err := on.(plugin.LogReader).ReadLogs(t.Context(), id, &whole); err != nil || whole.String() != out.String() {
		t.Errorf("ReadLogs() = %q, %v; want the node's whole output", whole.String(), err)
	}

	err := streamer.StreamLogs(t.Context(), id, 0, failingWriter{})
	if failure, ok := errors.AsType[*plugin.Error](err); !ok || failure.Class != plugin.ClassInternal {
		t.Errorf("StreamLogs() into a failing writer = %v, want an internal failure", err)
	}

	// The node lets the workload go while its record still names the node.
	_ = exec.Release(t.Context(), id.String())

	if err := streamer.StreamLogs(t.Context(), id, 0, io.Discard); !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("StreamLogs() of a released workload = %v, want ErrUnknownExecution", err)
	}

	if _, err := on.Status(t.Context(), id); !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("Status() of a released workload = %v, want ErrUnknownExecution", err)
	}

	if err := h.pool.Target("gone").(plugin.LogStreamer).StreamLogs(t.Context(), id, 0, io.Discard); !plugin.Reroutable(err) {
		t.Errorf("StreamLogs() on a node not connected = %v, want an infrastructure failure", err)
	}
}

// failingWriter refuses every write.
type failingWriter struct{}

// Write fails.
func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
