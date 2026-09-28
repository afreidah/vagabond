// -------------------------------------------------------------------------------
// Agent Connections
//
// Author: Alex Freidah
//
// Where agents connect. Each connection is a multiplexed session the agent
// registers its node on; the node is connected for as long as the session
// holds, and an agent reconnecting under the same name replaces its old
// session. The server calls a node's executions back down its session.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"net"

	"google.golang.org/grpc"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/nodes"
)

// -------------------------------------------------------------------------
// SERVING
// -------------------------------------------------------------------------

// ServeAgents accepts agent connections on listener until ctx is done, then
// closes every session.
func (s *Server) ServeAgents(ctx context.Context, listener net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		s.nodeConns.CloseAll()
	}()

	s.logger.InfoContext(ctx, "serving agents", "address", listener.Addr().String())

	for {
		conn, err := listener.Accept()
		if err != nil {
			// Closed on purpose when ctx ends; anything else is a real failure.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}

			return err
		}

		go s.handleAgent(ctx, conn)
	}
}

// handleAgent runs one agent's session: serves the Node endpoint on it, and
// forgets the node when the session ends.
func (s *Server) handleAgent(ctx context.Context, conn net.Conn) {
	session, err := agentrpc.Accept(conn)
	if err != nil {
		s.logger.WarnContext(ctx, "agent connection", "from", conn.RemoteAddr().String(), "error", err)

		return
	}

	// The agent calls this endpoint down the session it opened.
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

// addNodeConn records what a node reported on session: a new node, an update
// from one already connected, or an agent reconnecting.
func (s *Server) addNodeConn(ctx context.Context, node *agentrpc.NodeRegisterRequest, session *agentrpc.Session) {
	_, known := s.nodeConns.Get(node.GetName())
	s.nodeConns.Report(node, session)

	// Only a node arriving is worth a log line; its periodic reports are not.
	if !known {
		s.logger.InfoContext(ctx, "node registered",
			"node", node.GetName(), "pool", node.GetPool(), "address", session.RemoteAddr().String(),
			"cpu", node.GetCapacity().GetCpu(), "memory", node.GetCapacity().GetMemory(),
			"held", len(node.GetHeld()))
	}
}

// removeNodeConn forgets every node registered on session.
func (s *Server) removeNodeConn(ctx context.Context, session *agentrpc.Session) {
	for _, name := range s.nodeConns.Remove(session) {
		s.logger.WarnContext(ctx, "node disconnected", "node", name)
	}
}

// connectedNodes returns every connected node, by name.
func (s *Server) connectedNodes() []*nodes.Conn {
	return s.nodeConns.All()
}
