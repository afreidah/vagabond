// -------------------------------------------------------------------------------
// Node Endpoint
//
// Author: Alex Freidah
//
// The server's side of its agents: serving their connections, telling each
// node which of its workloads no execution needs any more, and GET /v1/nodes,
// which lists the nodes connected now.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
)

// ServeAgents accepts agent connections on listener until ctx is done, then
// closes every session.
func (s *Server) ServeAgents(ctx context.Context, listener net.Listener) error {
	return s.nodeConns.Serve(ctx, listener, s.unwanted, s.logger)
}

// unwanted names the workloads a node holds that nothing will ask for again.
func (s *Server) unwanted(ctx context.Context, held []*agentrpc.Held) []string {
	var release []string

	for _, h := range held {
		if s.releasable(ctx, h.GetExecutionId()) {
			release = append(release, h.GetExecutionId())
		}
	}

	return release
}

// releasable reports whether the workload id is no execution's, or its
// execution was released or never recorded. One the store cannot answer for
// is kept.
func (s *Server) releasable(ctx context.Context, id string) bool {
	parsed, err := execution.ParseID(id)
	if err != nil {
		return true
	}

	rec, err := s.executions.Get(ctx, parsed)

	switch {
	case errors.Is(err, execution.ErrNotFound):
		return true
	case err != nil:
		return false
	default:
		return !rec.Released.IsZero()
	}
}

// listNodes answers with every connected node, by name.
func (s *Server) listNodes(_ http.ResponseWriter, _ *http.Request) (any, error) {
	conns := s.nodeConns.All()
	out := make([]api.NodeListStub, 0, len(conns))

	for _, conn := range conns {
		node := conn.Node
		freeCPU, freeMemory := s.nodeConns.Free(node.GetName())

		running := 0
		for _, h := range node.GetHeld() {
			if h.GetRunning() {
				running++
			}
		}

		out = append(out, api.NodeListStub{
			Name:         node.GetName(),
			Pool:         node.GetPool(),
			Address:      conn.Address,
			Labels:       node.GetLabels(),
			Architecture: node.GetArchitecture(),
			CPU:          node.GetCapacity().GetCpu(),
			Memory:       node.GetCapacity().GetMemory(),
			UsedCPU:      node.GetCapacity().GetCpu() - freeCPU,
			UsedMemory:   node.GetCapacity().GetMemory() - freeMemory,
			Runtimes:     node.GetRuntimes(),
			Version:      node.GetVersion(),
			Executions:   running,
			Connected:    conn.Established.UTC(),
		})
	}

	return out, nil
}
