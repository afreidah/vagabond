// -------------------------------------------------------------------------------
// Node Providers
//
// Author: Alex Freidah
//
// A provider made of the agent nodes that joined a pool and run its type's
// executor: containerd's containers, or vagabond-firecracker's microVMs. Its
// capabilities are those nodes, each judged on its own by admission and placed
// as a candidate of its own, in the order its placement setting gives them.
// Target returns the same provider pointed at one node, which every call for an
// execution on that node goes through.
// -------------------------------------------------------------------------------

package pool

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// What a provider block names to select this plugin: the executor its nodes
// run its work with.
const (
	TypeContainerd  = agentrpc.ExecutorContainerd
	TypeFirecracker = agentrpc.ExecutorFirecracker
)

// How a provider orders its nodes among equally scored candidates: spread
// puts the most free memory first, pack the least.
const (
	PlacementSpread = "spread"
	PlacementPack   = "pack"
)

// isolation is how each type keeps a workload from the others on its node.
var isolation = map[string]string{
	TypeContainerd:  plugin.IsolationContainer,
	TypeFirecracker: plugin.IsolationVM,
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Compile-time proof the contract is met.
var (
	_ plugin.Provider    = (*Provider)(nil)
	_ plugin.Live        = (*Provider)(nil)
	_ plugin.Targeter    = (*Provider)(nil)
	_ plugin.Reserver    = (*Provider)(nil)
	_ plugin.LogCapturer = (*Provider)(nil)
	_ plugin.Releaser    = (*Provider)(nil)
)

// Config is the provider's optional block. Pool names the agents it draws
// on, those started with that -pool, and defaults to the provider's label.
type Config struct {
	Pool      string `hcl:"pool,optional"`
	Placement string `hcl:"placement,optional"`
}

// Provider is one declared provider over a pool of nodes, running its work
// with executor. node is the node its executions are on, set by Target and
// empty on the provider itself.
type Provider struct {
	name     string
	executor string
	cfg      Config
	conns    *nodes.Conns
	node     string
	now      func() time.Time
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds the provider named name of providerType, TypeContainerd or
// TypeFirecracker, over the server's connected nodes.
func New(name, providerType string, body hcl.Body, conns *nodes.Conns) (*Provider, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, providerType, body, "")
	if cfg == nil {
		return nil, diags
	}

	cfg.Pool = cmp.Or(cfg.Pool, name)
	cfg.Placement = cmp.Or(cfg.Placement, PlacementSpread)

	if cfg.Placement != PlacementSpread && cfg.Placement != PlacementPack {
		return nil, append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unknown placement",
			Detail: fmt.Sprintf("Provider %q sets placement to %q; it is %q or %q.",
				name, cfg.Placement, PlacementSpread, PlacementPack),
		})
	}

	return &Provider{name: name, executor: providerType, cfg: *cfg, conns: conns, now: time.Now}, diags
}

// Name returns the provider's label.
func (p *Provider) Name() string {
	return p.name
}

// Target returns the provider for executions on node.
func (p *Provider) Target(node string) plugin.Provider {
	on := *p
	on.node = node

	return &on
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities returns the provider as it stands now. Reading it calls
// nothing, so it is also what LiveCapabilities returns.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return p.LiveCapabilities()
}

// LiveCapabilities builds the provider from its connected nodes: one member
// per node, sized by the room it has left and in placement order. No nodes is
// an error, which marks the provider unhealthy.
func (p *Provider) LiveCapabilities() (plugin.Capabilities, error) {
	conns := p.conns.InPool(p.cfg.Pool, p.executor)
	if len(conns) == 0 {
		return plugin.Capabilities{}, fmt.Errorf("no nodes running %s have joined pool %q", p.executor, p.cfg.Pool)
	}

	out := plugin.Capabilities{ObservedAt: p.now()}

	for _, conn := range conns {
		out.Members = append(out.Members, p.member(conn))
	}

	// Stable, so nodes with the same room stay in name order
	slices.SortStableFunc(out.Members, func(a, b plugin.Member) int {
		order := cmp.Compare(*b.Capabilities.MaxResources.Memory, *a.Capabilities.MaxResources.Memory)
		if p.cfg.Placement == PlacementPack {
			return -order
		}

		return order
	})

	return out, nil
}

// member describes one node as admission sees it. MaxResources is the room it
// has left for one more workload after the executor's overhead, not its size,
// so a busy node is rejected for a task that would fit it empty, and a full one
// for any task.
func (p *Provider) member(conn *nodes.Conn) plugin.Member {
	cpu, memory := p.conns.Free(conn.Node.GetName())
	overhead := p.overhead(conn)
	cpu, memory = cpu-overhead.GetCpu(), memory-overhead.GetMemory()

	return plugin.Member{
		Name:   conn.Node.GetName(),
		Labels: conn.Node.GetLabels(),
		Capabilities: plugin.Capabilities{
			Drivers:       []job.DriverName{job.DriverContainer},
			Architectures: []job.Arch{job.Arch(conn.Node.GetArchitecture())},
			// Reservations can take the room below zero; none left is zero.
			MaxResources: plugin.Limits{
				CPU:    new(int(max(cpu, 0))),
				Memory: new(int(max(memory, 0))),
			},
			DefaultResources: defaultResources,
			// Workloads share the node's network, so both are reachable.
			InternetEgress:  true,
			PrivateNetwork:  true,
			ArbitraryImages: true,
			Isolation:       isolation[p.executor],
			ObservedAt:      p.now(),
		},
	}
}

// -------------------------------------------------------------------------
// SUBMITTING
// -------------------------------------------------------------------------

// Reserve holds room on the node for the task, at the size it runs at with
// the executor's overhead, until the node reports the workload.
func (p *Provider) Reserve(id execution.ID, task *job.Task) error {
	conn, ok := p.conns.Get(p.node)
	if !ok {
		return fmt.Errorf("%w: node %s is not connected", plugin.ErrNoRoom, p.node)
	}

	if err := p.conns.Reserve(p.node, id.String(), agentrpc.Charge(resourcesOf(task), p.overhead(conn))); err != nil {
		return fmt.Errorf("%w: %w", plugin.ErrNoRoom, err)
	}

	return nil
}

// Submit starts the task on the node. A task that does not translate, or a
// node that refuses it, gives the reservation back.
func (p *Provider) Submit(ctx context.Context, id execution.ID, task *job.Task) (plugin.Submission, error) {
	sub, err := p.submit(ctx, id, task)
	if err != nil {
		p.conns.Unreserve(id.String())

		return plugin.Submission{}, err
	}

	return sub, nil
}

// submit translates the task, sends it to the node for the provider's
// executor, and reads the state it answers with. A finished workload reports
// running, since terminal needs a result.
func (p *Provider) submit(ctx context.Context, id execution.ID, task *job.Task) (plugin.Submission, error) {
	workload, err := workloadOf(task)
	if err != nil {
		return plugin.Submission{}, err
	}

	workload.Executor = p.executor

	conn, err := p.connected()
	if err != nil {
		return plugin.Submission{}, err
	}

	sub, err := conn.Executions.Submit(ctx, &agentrpc.SubmitRequest{ExecutionId: id.String(), Workload: workload})
	if err != nil {
		return plugin.Submission{}, p.failure(id, err)
	}

	state, err := execution.ParseState(sub.GetState())
	if err != nil {
		return plugin.Submission{}, plugin.Internal(fmt.Errorf("node %s: %w", p.node, err))
	}

	if state.Terminal() {
		state = execution.StateRunning
	}

	return plugin.Submission{ProviderID: sub.GetProviderId(), State: state}, nil
}

// overhead is what each workload of the provider's executor takes of conn's
// node beyond what it declared.
func (p *Provider) overhead(conn *nodes.Conn) *agentrpc.Resources {
	return conn.Node.Executor(p.executor).GetOverhead()
}
