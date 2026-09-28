// -------------------------------------------------------------------------------
// Pool Executions
//
// Author: Alex Freidah
//
// Status, results, cancellation and release, each sent to the node holding the
// execution. Which node that is comes from this process's own placements, or
// from what the nodes report holding, so it survives a server restart without
// being stored. A node that drops is given a grace period to come back before
// its executions are given up on.
// -------------------------------------------------------------------------------

package pool

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
)

// leftGrace is how long a node may be gone before its executions are given up
// on. Its executions report lost meanwhile, which keeps a run waiting for an
// agent that is only restarting.
const leftGrace = 5 * time.Minute

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// Status reports where an execution stands on its node. While the node is gone
// within the grace period it is lost; after, the provider no longer answers.
func (p *Provider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	conn, gone, err := p.holder(id)

	switch {
	case err != nil:
		return execution.Status{}, err
	case gone:
		return execution.Status{ID: id, State: execution.StateLost, UpdatedAt: p.now()}, nil
	}

	st, err := conn.Executions.Status(ctx, &agentrpc.ExecutionRequest{ExecutionId: id.String()})
	if err != nil {
		return execution.Status{}, p.failure(id, err)
	}

	out := execution.Status{
		ID:         id,
		State:      execution.State(st.GetState()),
		ProviderID: conn.Node.GetName(),
		UpdatedAt:  p.now(),
	}

	if st.GetStartedAt() != nil {
		out.StartedAt = st.GetStartedAt().AsTime()
	}

	if st.GetEndedAt() != nil {
		out.EndedAt = st.GetEndedAt().AsTime()
	}

	return out, nil
}

// Result returns what a finished execution produced on its node.
func (p *Provider) Result(ctx context.Context, id execution.ID) (*execution.Result, error) {
	conn, err := p.connected(id)
	if err != nil {
		return nil, err
	}

	res, err := conn.Executions.Result(ctx, &agentrpc.ExecutionRequest{ExecutionId: id.String()})
	if err != nil {
		return nil, p.failure(id, err)
	}

	out := &execution.Result{
		ID:            id,
		Duration:      res.GetDuration().AsDuration(),
		Logs:          res.GetLogs(),
		LogsTruncated: res.GetLogsTruncated(),
	}

	// Unset means the workload never exited, which is not the same as zero.
	if res.ExitCode != nil {
		out.ExitCode = new(int(res.GetExitCode()))
	}

	return out, nil
}

// Cancel stops an execution on its node.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	conn, err := p.connected(id)
	if err != nil {
		return err
	}

	if _, err := conn.Executions.Cancel(ctx, &agentrpc.ExecutionRequest{ExecutionId: id.String()}); err != nil {
		return p.failure(id, err)
	}

	return nil
}

// Release deletes a finished execution from its node and forgets where it was.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	conn, err := p.connected(id)
	if err != nil {
		return err
	}

	if _, err := conn.Executions.Release(ctx, &agentrpc.ExecutionRequest{ExecutionId: id.String()}); err != nil {
		return p.failure(id, err)
	}

	p.mu.Lock()
	delete(p.placed, id.String())
	delete(p.reserved, id.String())
	p.mu.Unlock()

	return nil
}

// -------------------------------------------------------------------------
// LOCATING
// -------------------------------------------------------------------------

// holder finds the node holding id. gone is a node that left within the grace
// period, or one not heard from since this process started, which a node may
// still be about to report. Past either, the execution is unknown or the
// node's absence is a failure.
func (p *Provider) holder(id execution.ID) (*nodes.Conn, bool, error) {
	node, known := p.locate(id.String())

	if !known {
		// Nodes reconnect after a server restart within seconds of each other;
		// one holding this execution may not have reported yet.
		if p.now().Sub(p.created) < leftGrace {
			return nil, true, nil
		}

		return nil, false, fmt.Errorf("%w: %s in pool %q", plugin.ErrUnknownExecution, id, p.name)
	}

	if conn, ok := p.conns.Get(node); ok {
		return conn, false, nil
	}

	if left, ok := p.conns.LeftAt(node); ok && p.now().Sub(left) < leftGrace {
		return nil, true, nil
	}

	return nil, false, plugin.Infrastructure(fmt.Errorf("node %s holding %s is gone", node, id))
}

// connected is holder for calls that need the node now: a node that is gone,
// for however long, cannot be asked.
func (p *Provider) connected(id execution.ID) (*nodes.Conn, error) {
	conn, gone, err := p.holder(id)

	switch {
	case err != nil:
		return nil, err
	case gone:
		return nil, plugin.Infrastructure(fmt.Errorf("the node holding %s is not connected", id))
	}

	return conn, nil
}

// locate returns the node id was placed on: this process's own record first,
// then whichever node in the pool reports holding it.
func (p *Provider) locate(id string) (string, bool) {
	p.mu.Lock()
	node, ok := p.placed[id]
	p.mu.Unlock()

	if ok {
		return node, true
	}

	for _, conn := range p.conns.InPool(p.name) {
		for _, h := range conn.Node.GetHeld() {
			if h.GetExecutionId() == id {
				return conn.Node.GetName(), true
			}
		}
	}

	return "", false
}

// failure maps a node's answer to the plugin's errors: a node that does not
// hold the execution is ErrUnknownExecution, anything else is the node failing
// to answer.
func (p *Provider) failure(id execution.ID, err error) error {
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("%w: %s in pool %q", plugin.ErrUnknownExecution, id, p.name)
	}

	return plugin.Infrastructure(fmt.Errorf("pool %q: %w", p.name, err))
}
