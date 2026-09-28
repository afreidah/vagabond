// -------------------------------------------------------------------------------
// Pool Provider
//
// Author: Alex Freidah
//
// A provider made of the agent nodes that joined a pool. Its capabilities are
// its connected nodes, each judged on its own by admission, and built fresh
// every time they are read, so a node that joins is schedulable at once.
// Submitting picks the node with the most room among those admission passed,
// and holds a short reservation there until the node's own report accounts
// for the workload.
// -------------------------------------------------------------------------------

package pool

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Type is the provider type a pool is declared with.
const Type = "pool"

// reservationTTL bounds how long a reservation covers for a node that has not
// reported the workload, so one lost to a failed call cannot hold room forever.
const reservationTTL = 2 * time.Minute

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Provider is one declared pool. placed remembers which node this process sent
// each execution to; reserved holds room on a node between placing work and
// the node reporting it.
type Provider struct {
	name    string
	conns   *nodes.Conns
	now     func() time.Time
	created time.Time

	mu       sync.Mutex
	placed   map[string]string
	reserved map[string]reservation
}

// reservation is room taken on a node for an execution the node has not yet
// reported.
type reservation struct {
	node   string
	cpu    int64
	memory int64
	at     time.Time
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds the pool named name over the server's connected nodes.
func New(name string, conns *nodes.Conns) *Provider {
	now := time.Now

	return &Provider{
		name:     name,
		conns:    conns,
		now:      now,
		created:  now(),
		placed:   make(map[string]string),
		reserved: make(map[string]reservation),
	}
}

// Name returns the pool's name, which agents join it by.
func (p *Provider) Name() string {
	return p.name
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities returns the pool as it stands now. Reading it calls nothing, so
// it is also what LiveCapabilities returns.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return p.LiveCapabilities()
}

// LiveCapabilities builds the pool from its connected nodes: one member per
// node, sized by the room it has left, and a pool-wide summary for display. A
// pool with no nodes is an error, which marks it unhealthy.
func (p *Provider) LiveCapabilities() (plugin.Capabilities, error) {
	conns := p.conns.InPool(p.name)
	if len(conns) == 0 {
		return plugin.Capabilities{}, fmt.Errorf("no nodes have joined pool %q", p.name)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	out := plugin.Capabilities{ObservedAt: p.now()}

	for _, conn := range conns {
		member := p.member(conn)
		out.Members = append(out.Members, member)

		// The summary is what any node offers, for a plan's display; admission
		// judges the members, not this.
		out.Drivers = union(out.Drivers, member.Capabilities.Drivers)
		out.Architectures = union(out.Architectures, member.Capabilities.Architectures)
		out.MaxResources.CPU = max(out.MaxResources.CPU, member.Capabilities.MaxResources.CPU)
		out.MaxResources.Memory = max(out.MaxResources.Memory, member.Capabilities.MaxResources.Memory)
		out.InternetEgress, out.PrivateNetwork, out.ArbitraryImages = true, true, true
	}

	return out, nil
}

// member describes one node as admission sees it. MaxResources is the room it
// has left, not its size, so a busy node is rejected for a task that would fit
// it empty. Called with mu held.
func (p *Provider) member(conn *nodes.Conn) plugin.Member {
	cpu, memory := p.free(conn)

	return plugin.Member{
		Name:   conn.Node.GetName(),
		Labels: conn.Node.GetLabels(),
		Capabilities: plugin.Capabilities{
			Drivers:       []job.DriverName{job.DriverContainer},
			Architectures: []job.Arch{job.Arch(conn.Node.GetArchitecture())},
			MaxResources:  plugin.Resources{CPU: int(cpu), Memory: int(memory)},
			// Workloads share the node's network, so both are reachable.
			InternetEgress:  true,
			PrivateNetwork:  true,
			ArbitraryImages: true,
			ObservedAt:      p.now(),
		},
	}
}

// free is the room a node has left: its capacity, less what its running
// workloads declared, less reservations it has not yet reported. Called with
// mu held.
func (p *Provider) free(conn *nodes.Conn) (cpu, memory int64) {
	usedCPU, usedMemory, _ := conn.Used()
	cpu = conn.Node.GetCapacity().GetCpu() - usedCPU
	memory = conn.Node.GetCapacity().GetMemory() - usedMemory

	reported := make(map[string]bool, len(conn.Node.GetHeld()))
	for _, h := range conn.Node.GetHeld() {
		reported[h.GetExecutionId()] = true
	}

	for id, r := range p.reserved {
		// A workload the node reported is already in its own numbers.
		if r.node != conn.Node.GetName() || reported[id] || p.now().Sub(r.at) > reservationTTL {
			continue
		}

		cpu, memory = cpu-r.cpu, memory-r.memory
	}

	return cpu, memory
}

// -------------------------------------------------------------------------
// SUBMITTING
// -------------------------------------------------------------------------

// Submit places a task on any node in the pool with room for it.
func (p *Provider) Submit(ctx context.Context, id execution.ID, task *job.Task) (plugin.Submission, error) {
	return p.SubmitTo(ctx, id, task, nil)
}

// SubmitTo places a task on one of members, the nodes admission passed for it,
// or on any node when members is empty. Nodes are tried most room first; a node
// that refuses for lack of room is passed over for the next, and no node left
// is an infrastructure failure, so dispatch tries another provider.
func (p *Provider) SubmitTo(
	ctx context.Context, id execution.ID, task *job.Task, members []string,
) (plugin.Submission, error) {
	workload, err := workloadOf(task)
	if err != nil {
		return plugin.Submission{}, err
	}

	var tried []string

	for {
		node, ok := p.reserve(id.String(), workload.GetResources(), members, tried)
		if !ok {
			return plugin.Submission{}, plugin.Infrastructure(
				fmt.Errorf("no node in pool %q has room for %d millicores and %d MiB",
					p.name, workload.GetResources().GetCpu(), workload.GetResources().GetMemory()))
		}

		sub, err := p.send(ctx, node, id, workload)
		if err == nil {
			return sub, nil
		}

		p.unreserve(id.String())

		// The node's own count was tighter than ours: try the next one.
		if status.Code(err) == codes.ResourceExhausted {
			tried = append(tried, node)

			continue
		}

		return plugin.Submission{}, err
	}
}

// reserve picks the node with the most free memory that fits resources, among
// members and not in tried, and holds that room for id. Choosing and reserving
// are one step under mu, so two submissions cannot take the same last room.
func (p *Provider) reserve(id string, resources *agentrpc.Resources, members, tried []string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var (
		best     string
		bestFree int64 = -1
	)

	for _, conn := range p.conns.InPool(p.name) {
		name := conn.Node.GetName()

		if (len(members) > 0 && !slices.Contains(members, name)) || slices.Contains(tried, name) {
			continue
		}

		cpu, memory := p.free(conn)
		if cpu < resources.GetCpu() || memory < resources.GetMemory() {
			continue
		}

		if memory > bestFree {
			best, bestFree = name, memory
		}
	}

	if best == "" {
		return "", false
	}

	p.reserved[id] = reservation{node: best, cpu: resources.GetCpu(), memory: resources.GetMemory(), at: p.now()}

	return best, true
}

// unreserve releases the room held for id.
func (p *Provider) unreserve(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.reserved, id)
}

// send submits the workload to node and records where it went. A node that
// refuses for room comes back wrapped, for SubmitTo to recognise.
func (p *Provider) send(
	ctx context.Context, node string, id execution.ID, workload *agentrpc.Workload,
) (plugin.Submission, error) {
	conn, ok := p.conns.Get(node)
	if !ok {
		return plugin.Submission{}, plugin.Infrastructure(fmt.Errorf("node %s left before the workload reached it", node))
	}

	sub, err := conn.Executions.Submit(ctx, &agentrpc.SubmitRequest{ExecutionId: id.String(), Workload: workload})
	if err != nil {
		return plugin.Submission{}, plugin.Infrastructure(fmt.Errorf("submitting to node %s: %w", node, err))
	}

	p.mu.Lock()
	p.placed[id.String()] = node
	p.mu.Unlock()

	// A workload that already finished is reported as running: a submission
	// may not carry a terminal state without its result, and the next status
	// call finds it finished.
	state := execution.State(sub.GetState())
	if state.Terminal() {
		state = execution.StateRunning
	}

	return plugin.Submission{ProviderID: node, State: state}, nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// union appends the values of add not already in to.
func union[T comparable](to, add []T) []T {
	for _, v := range add {
		if !slices.Contains(to, v) {
			to = append(to, v)
		}
	}

	return to
}
