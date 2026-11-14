// -------------------------------------------------------------------------------
// Node Executions
//
// Author: Alex Freidah
//
// Status, results, live output, cancellation and release, each sent to the
// node an execution is on. A node that is not connected cannot answer, which
// leaves its executions lost until it returns or the runner gives up on them;
// releasing one is left to the node, which is told when it next registers.
// -------------------------------------------------------------------------------

package pool

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// Status reports where an execution stands on its node.
func (p *Provider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	conn, err := p.connected()
	if err != nil {
		return execution.Status{}, err
	}

	st, err := conn.Executions.Status(ctx, request(id))
	if err != nil {
		return execution.Status{}, p.failure(id, err)
	}

	return st.Execution(id, p.now()), nil
}

// Result returns a finished execution's exit code and run time from its node.
// The output is read apart, by ReadLogs.
func (p *Provider) Result(ctx context.Context, id execution.ID) (*execution.Result, error) {
	conn, err := p.connected()
	if err != nil {
		return nil, err
	}

	res, err := conn.Executions.Result(ctx, request(id))
	if err != nil {
		return nil, p.failure(id, err)
	}

	return res.Execution(id), nil
}

// Cancel stops an execution on its node.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	conn, err := p.connected()
	if err != nil {
		return err
	}

	if _, err := conn.Executions.Cancel(ctx, request(id)); err != nil {
		return p.failure(id, err)
	}

	return nil
}

// Release deletes a finished execution from its node. A node not connected
// is told to when it next registers, since its execution is released by then.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	p.conns.Unreserve(id.String())

	conn, ok := p.conns.Get(p.node)
	if !ok {
		return nil
	}

	if _, err := conn.Executions.Release(ctx, request(id)); err != nil {
		return p.failure(id, err)
	}

	return nil
}

// StreamLogs copies an execution's output from byte offset on, from its node to
// w as it is written, until the workload ends or ctx does.
func (p *Provider) StreamLogs(ctx context.Context, id execution.ID, offset int64, w io.Writer) error {
	conn, err := p.connected()
	if err != nil {
		return err
	}

	// Cancelled on the way out, which closes a stream left unread on error.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := conn.Executions.StreamLogs(ctx, &agentrpc.LogsRequest{ExecutionId: id.String(), Offset: offset})
	if err != nil {
		return p.failure(id, err)
	}

	out := plugin.LogOutput(w)

	for {
		chunk, err := stream.Recv()

		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return p.failure(id, err)
		}

		if _, err := out.Write(chunk.GetData()); err != nil {
			return err
		}
	}
}

// ReadLogs copies a finished execution's whole output from its node. The
// node's stream ends once the workload has, so this is the stream from the
// start. Release deletes the log, so this precedes it.
func (p *Provider) ReadLogs(ctx context.Context, id execution.ID, w io.Writer) error {
	return p.StreamLogs(ctx, id, 0, w)
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// connected is the node's connection, or the node failing to answer while it
// is not connected.
func (p *Provider) connected() (*nodes.Conn, error) {
	conn, ok := p.conns.Get(p.node)
	if !ok {
		return nil, plugin.Infrastructure(fmt.Errorf("node %s is not connected", p.node))
	}

	return conn, nil
}

// failure maps a node's answer to the plugin's errors: a node that does not
// hold the execution is ErrUnknownExecution, anything else is the node failing
// to answer.
func (p *Provider) failure(id execution.ID, err error) error {
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("%w: %s on node %s", plugin.ErrUnknownExecution, id, p.node)
	}

	return plugin.Infrastructure(fmt.Errorf("node %s: %w", p.node, err))
}

// request names id to its node.
func request(id execution.ID) *agentrpc.ExecutionRequest {
	return &agentrpc.ExecutionRequest{ExecutionId: id.String()}
}
