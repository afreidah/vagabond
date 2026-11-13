// -------------------------------------------------------------------------------
// Agent Connections
//
// Author: Alex Freidah
//
// Where agents connect. Each connection is a multiplexed session the agent
// registers its node on; the node is connected for as long as the session
// holds, and an agent reconnecting under the same name replaces its old
// session. The server calls a node's executions back down its session. On a
// TLS listener a node may register only under its certificate's common name.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/backoff"
	"github.com/afreidah/vagabond/internal/nodes"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// handshakeTimeout bounds an agent's TLS handshake.
const handshakeTimeout = 10 * time.Second

// How long a failed accept waits before the next, doubling up to the maximum
// while accepts keep failing.
const (
	acceptRetry    = 5 * time.Millisecond
	acceptRetryMax = time.Second
)

// -------------------------------------------------------------------------
// SERVING
// -------------------------------------------------------------------------

// ServeAgents accepts agent connections on listener until ctx is done, then
// closes every session. A failed accept, such as one out of file
// descriptors, is retried; the listener closing while ctx is live is the
// only error, since no agent could connect again.
func (s *Server) ServeAgents(ctx context.Context, listener net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		s.nodeConns.CloseAll()
	}()

	s.logger.InfoContext(ctx, "serving agents", "address", listener.Addr().String())

	failures := 0

	for {
		conn, err := listener.Accept()

		switch {
		case err == nil:
			failures = 0

			go s.handleAgent(ctx, conn)

			continue
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, net.ErrClosed):
			return fmt.Errorf("agent listener closed: %w", err)
		}

		s.logger.WarnContext(ctx, "accepting agent connections failed, retrying", "error", err)

		if backoff.Sleep(ctx, backoff.Exponential(acceptRetry, acceptRetryMax, failures)) != nil {
			return nil
		}

		failures++
	}
}

// handleAgent runs one agent's session: serves the Node endpoint on it, and
// forgets the node when the session ends.
func (s *Server) handleAgent(ctx context.Context, conn net.Conn) {
	// A peer that never finishes the TLS handshake is dropped, not held open.
	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	session, err := agentrpc.Accept(handshake, conn)

	cancel()

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
