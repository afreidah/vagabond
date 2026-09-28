// -------------------------------------------------------------------------------
// Agent Session
//
// Author: Alex Freidah
//
// One agent connection, multiplexed. Each side serves gRPC on the streams its
// peer opens and calls the peer on streams it opens, so an agent that can only
// dial out is still called by the server. The connection closing is the only
// liveness signal either side needs.
// -------------------------------------------------------------------------------

package agentrpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"

	"github.com/hashicorp/yamux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Session is one agent connection: the multiplexer both sides run gRPC over,
// and a client for calling the peer.
type Session struct {
	mux  *yamux.Session
	peer *grpc.ClientConn
}

// -------------------------------------------------------------------------
// OPENING
// -------------------------------------------------------------------------

// Dial connects an agent to a server at addr, over TLS when tlsConfig is set.
func Dial(ctx context.Context, addr string, tlsConfig *tls.Config) (*Session, error) {
	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}

	if tlsConfig != nil {
		conn = tls.Client(conn, tlsConfig)
	}

	mux, err := yamux.Client(conn, muxConfig())
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("opening a session to %s: %w", addr, err)
	}

	return newSession(mux)
}

// Accept wraps a connection the server accepted from an agent. TLS, when
// configured, is the listener's.
func Accept(conn net.Conn) (*Session, error) {
	mux, err := yamux.Server(conn, muxConfig())
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("opening a session from %s: %w", conn.RemoteAddr(), err)
	}

	return newSession(mux)
}

// newSession builds the client for calling the peer, whose every connection is
// a new stream on the session. TLS is already below the multiplexer.
func newSession(mux *yamux.Session) (*Session, error) {
	peer, err := grpc.NewClient("passthrough:///peer",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return mux.Open() }),
	)
	if err != nil {
		_ = mux.Close()

		return nil, fmt.Errorf("creating the peer client: %w", err)
	}

	return &Session{mux: mux, peer: peer}, nil
}

// muxConfig is yamux's defaults without its logging, which would otherwise
// write to stderr behind the logger's back.
func muxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard

	return cfg
}

// -------------------------------------------------------------------------
// USING
// -------------------------------------------------------------------------

// Serve runs srv on the streams the peer opens until the session closes.
func (s *Session) Serve(srv *grpc.Server) error {
	return srv.Serve(s.mux)
}

// Peer is a connection for calling the other side's services.
func (s *Session) Peer() grpc.ClientConnInterface {
	return s.peer
}

// Done is closed when the connection is gone.
func (s *Session) Done() <-chan struct{} {
	return s.mux.CloseChan()
}

// RemoteAddr is where the other side connected from.
func (s *Session) RemoteAddr() net.Addr {
	return s.mux.RemoteAddr()
}

// Close ends the session and the client on it.
func (s *Session) Close() error {
	_ = s.peer.Close()

	return s.mux.Close()
}
