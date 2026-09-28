// -------------------------------------------------------------------------------
// Node Endpoint
//
// Author: Alex Freidah
//
// What clients call on the server over their session: registering their node,
// and GET /v1/nodes, which lists the nodes connected now.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"net/http"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/api"
)

// nodeEndpoint serves one session's Node calls.
type nodeEndpoint struct {
	agentrpc.UnimplementedNodeServer

	srv     *Server
	session *agentrpc.Session
}

// Register records the node on this session, replacing what it registered
// before.
func (n *nodeEndpoint) Register(
	ctx context.Context, req *agentrpc.NodeRegisterRequest,
) (*agentrpc.NodeRegisterResponse, error) {
	n.srv.addNodeConn(ctx, req, n.session)

	return &agentrpc.NodeRegisterResponse{}, nil
}

// listNodes answers with every connected node, by name.
func (s *Server) listNodes(_ http.ResponseWriter, _ *http.Request) (any, error) {
	conns := s.connectedNodes()
	out := make([]api.NodeListStub, 0, len(conns))

	for _, conn := range conns {
		node := conn.Node

		out = append(out, api.NodeListStub{
			Name:         node.GetName(),
			Pool:         node.GetPool(),
			Address:      conn.Address,
			Labels:       node.GetLabels(),
			Architecture: node.GetArchitecture(),
			CPU:          node.GetCapacity().GetCpu(),
			Memory:       node.GetCapacity().GetMemory(),
			Runtimes:     node.GetRuntimes(),
			Version:      node.GetVersion(),
			Executions:   len(node.GetExecutions()),
			Connected:    conn.Established.UTC(),
		})
	}

	return out, nil
}
