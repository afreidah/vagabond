// -------------------------------------------------------------------------------
// Client Connections
//
// Author: Alex Freidah
//
// Where clients connect. Each connection is a multiplexed session the client
// registers its node on; the node is connected for as long as the session
// holds, and a client reconnecting under the same name replaces its old
// session. The server calls a node's executions back down its session.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/afreidah/vagabond/internal/agentrpc"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// nodeConnState is one connected node: what it registered, when, and a client
// for calling its executions down its session.
type nodeConnState struct {
	Node        *agentrpc.NodeRegisterRequest
	Executions  agentrpc.ClientExecutionsClient
	Address     string
	Established time.Time

	session *agentrpc.Session
}

// -------------------------------------------------------------------------
// SERVING
// -------------------------------------------------------------------------

// ServeClients accepts client connections on listener until ctx is done, then
// closes every session.
func (s *Server) ServeClients(ctx context.Context, listener net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		s.closeNodeConns()
	}()

	s.logger.InfoContext(ctx, "serving clients", "address", listener.Addr().String())

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}

			return err
		}

		go s.handleClient(ctx, conn)
	}
}

// handleClient runs one client's session: serves the Node endpoint on it, and
// forgets the node when the session ends.
func (s *Server) handleClient(ctx context.Context, conn net.Conn) {
	session, err := agentrpc.Accept(conn)
	if err != nil {
		s.logger.WarnContext(ctx, "client connection", "from", conn.RemoteAddr().String(), "error", err)

		return
	}

	srv := grpc.NewServer()
	agentrpc.RegisterNodeServer(srv, &nodeEndpoint{srv: s, session: session})

	go func() { _ = session.Serve(srv) }()

	<-session.Done()
	srv.Stop()
	s.removeNodeConn(ctx, session)
}

// -------------------------------------------------------------------------
// NODE CONNECTIONS
// -------------------------------------------------------------------------

// addNodeConn records a node registered on session, replacing a connection of
// the same name on another session, which is that client reconnecting.
func (s *Server) addNodeConn(ctx context.Context, node *agentrpc.NodeRegisterRequest, session *agentrpc.Session) {
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()

	name := node.GetName()

	if old, ok := s.nodeConns[name]; ok && old.session != session {
		_ = old.session.Close()
	}

	s.nodeConns[name] = &nodeConnState{
		Node:        node,
		Executions:  agentrpc.NewClientExecutionsClient(session.Peer()),
		Address:     session.RemoteAddr().String(),
		Established: s.now(),
		session:     session,
	}

	s.logger.InfoContext(ctx, "node registered",
		"node", name, "pool", node.GetPool(), "address", session.RemoteAddr().String(),
		"cpu", node.GetCapacity().GetCpu(), "memory", node.GetCapacity().GetMemory(),
		"executions", len(node.GetExecutions()))
}

// removeNodeConn forgets every node registered on session.
func (s *Server) removeNodeConn(ctx context.Context, session *agentrpc.Session) {
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()

	for name, conn := range s.nodeConns {
		if conn.session == session {
			delete(s.nodeConns, name)
			s.logger.WarnContext(ctx, "node disconnected", "node", name, "pool", conn.Node.GetPool())
		}
	}
}

// connectedNodes returns every connected node, by name.
func (s *Server) connectedNodes() []*nodeConnState {
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()

	out := make([]*nodeConnState, 0, len(s.nodeConns))
	for _, conn := range s.nodeConns {
		out = append(out, conn)
	}

	slices.SortFunc(out, func(a, b *nodeConnState) int {
		return strings.Compare(a.Node.GetName(), b.Node.GetName())
	})

	return out
}

// closeNodeConns ends every session, when the server stops serving clients.
func (s *Server) closeNodeConns() {
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()

	for _, conn := range s.nodeConns {
		_ = conn.session.Close()
	}
}
