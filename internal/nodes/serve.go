// -------------------------------------------------------------------------------
// Serving Agents
//
// Author: Alex Freidah
//
// Where agents connect. Each connection is a multiplexed session the agent
// registers its node on; the node is connected for as long as the session
// holds, and an agent reconnecting under the same name replaces its old
// session. A node's executions are called back down its session. On a TLS
// listener a node may register only under its certificate's common name.
// -------------------------------------------------------------------------------

package nodes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/backoff"
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

// Unwanted names which of a node's held workloads it should release, and
// names none when it cannot tell.
type Unwanted func(ctx context.Context, held []*agentrpc.Held) []string

// Serve accepts agent connections on listener until ctx is done, then closes
// every session. Each registration is answered with what unwanted says the
// node should release; nil releases nothing. A failed accept, such as one out
// of file descriptors, is retried; the listener closing while ctx is live is
// the only error, since no agent could connect again.
func (c *Conns) Serve(ctx context.Context, listener net.Listener, unwanted Unwanted, logger *slog.Logger) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		c.CloseAll()
	}()

	logger.InfoContext(ctx, "serving agents", "address", listener.Addr().String())

	failures := 0

	for {
		conn, err := listener.Accept()

		switch {
		case err == nil:
			failures = 0

			go c.handle(ctx, conn, &endpoint{conns: c, unwanted: unwanted, logger: logger})

			continue
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, net.ErrClosed):
			return fmt.Errorf("agent listener closed: %w", err)
		}

		logger.WarnContext(ctx, "accepting agent connections failed, retrying", "error", err)

		if backoff.Sleep(ctx, backoff.Exponential(acceptRetry, acceptRetryMax, failures)) != nil {
			return nil
		}

		failures++
	}
}

// handle runs one agent's session: serves the Node endpoint on it, and
// forgets the node when the session ends.
func (c *Conns) handle(ctx context.Context, conn net.Conn, e *endpoint) {
	// A peer that never finishes the TLS handshake is dropped, not held open.
	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	session, err := agentrpc.Accept(handshake, conn)

	cancel()

	if err != nil {
		e.logger.WarnContext(ctx, "agent connection", "from", conn.RemoteAddr().String(), "error", err)

		return
	}

	// The agent calls this endpoint down the session it opened.
	e.session = session
	srv := grpc.NewServer()
	agentrpc.RegisterNodeServer(srv, e)

	go func() { _ = session.Serve(srv) }()

	<-session.Done()
	srv.Stop()

	for _, name := range c.Remove(session) {
		e.logger.WarnContext(ctx, "node disconnected", "node", name)
	}
}

// -------------------------------------------------------------------------
// NODE ENDPOINT
// -------------------------------------------------------------------------

// endpoint serves one session's Node calls.
type endpoint struct {
	agentrpc.UnimplementedNodeServer

	conns    *Conns
	session  *agentrpc.Session
	unwanted Unwanted
	logger   *slog.Logger
}

// Register records the node on this session, replacing what it registered
// before, and answers with the workloads it should release. Over TLS the name
// must be the certificate's, so an agent cannot register as, and so replace, a
// node it holds no certificate for.
func (e *endpoint) Register(
	ctx context.Context, req *agentrpc.NodeRegisterRequest,
) (*agentrpc.NodeRegisterResponse, error) {
	if identity := e.session.Identity(); identity != "" && req.GetName() != identity {
		return nil, status.Errorf(codes.PermissionDenied,
			"node %q cannot register as %q: its certificate names %q", identity, req.GetName(), identity)
	}

	_, known := e.conns.Get(req.GetName())
	e.conns.Report(req, e.session)

	// Only a node arriving is worth a log line; its periodic reports are not.
	if !known {
		e.logger.InfoContext(ctx, "node registered",
			"node", req.GetName(), "pool", req.GetPool(), "address", e.session.RemoteAddr().String(),
			"cpu", req.GetCapacity().GetCpu(), "memory", req.GetCapacity().GetMemory(),
			"held", len(req.GetHeld()))
	}

	if e.unwanted == nil {
		return &agentrpc.NodeRegisterResponse{}, nil
	}

	return &agentrpc.NodeRegisterResponse{Release: e.unwanted(ctx, req.GetHeld())}, nil
}
