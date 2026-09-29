// -------------------------------------------------------------------------------
// Node Endpoint
//
// Author: Alex Freidah
//
// What agents call on the server over their session: registering their node,
// and GET /v1/nodes, which lists the nodes connected now.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
// before. Over TLS the name must be the certificate's, so an agent cannot
// register as, and so replace, a node it holds no certificate for.
func (n *nodeEndpoint) Register(
	ctx context.Context, req *agentrpc.NodeRegisterRequest,
) (*agentrpc.NodeRegisterResponse, error) {
	if identity := n.session.Identity(); identity != "" && req.GetName() != identity {
		return nil, status.Errorf(codes.PermissionDenied,
			"node %q cannot register as %q: its certificate names %q", identity, req.GetName(), identity)
	}

	n.srv.addNodeConn(ctx, req, n.session)

	return &agentrpc.NodeRegisterResponse{}, nil
}

// listNodes answers with every connected node, by name.
func (s *Server) listNodes(_ http.ResponseWriter, _ *http.Request) (any, error) {
	conns := s.connectedNodes()
	out := make([]api.NodeListStub, 0, len(conns))

	for _, conn := range conns {
		node := conn.Node
		usedCPU, usedMemory, running := conn.Used()

		out = append(out, api.NodeListStub{
			Name:         node.GetName(),
			Pool:         node.GetPool(),
			Address:      conn.Address,
			Labels:       node.GetLabels(),
			Architecture: node.GetArchitecture(),
			CPU:          node.GetCapacity().GetCpu(),
			Memory:       node.GetCapacity().GetMemory(),
			UsedCPU:      usedCPU,
			UsedMemory:   usedMemory,
			Runtimes:     node.GetRuntimes(),
			Version:      node.GetVersion(),
			Executions:   running,
			Connected:    conn.Established.UTC(),
		})
	}

	return out, nil
}
