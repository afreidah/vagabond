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
// a client for calling the peer, and on the server, who the peer proved to be.
type Session struct {
	mux      *yamux.Session
	peer     *grpc.ClientConn
	identity string
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
		tlsConn := tls.Client(conn, tlsConfig)

		// Handshaking now reports a certificate problem as a failed dial,
		// rather than as a broken session on the first call.
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()

			return nil, fmt.Errorf("TLS with %s: %w", addr, err)
		}

		conn = tlsConn
	}

	mux, err := yamux.Client(conn, muxConfig())
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("opening a session to %s: %w", addr, err)
	}

	return newSession(mux, "")
}

// Accept wraps a connection the server accepted from an agent. On a TLS
// listener it completes the handshake within ctx and takes the peer's identity
// from its verified certificate.
func Accept(ctx context.Context, conn net.Conn) (*Session, error) {
	var identity string

	if tlsConn, ok := conn.(*tls.Conn); ok {
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()

			return nil, fmt.Errorf("TLS with %s: %w", conn.RemoteAddr(), err)
		}

		// The listener requires a verified client certificate, so there is one.
		identity = tlsConn.ConnectionState().PeerCertificates[0].Subject.CommonName
	}

	mux, err := yamux.Server(conn, muxConfig())
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("opening a session from %s: %w", conn.RemoteAddr(), err)
	}

	return newSession(mux, identity)
}

// newSession builds the client for calling the peer, whose every connection is
// a new stream on the session. TLS is already below the multiplexer.
func newSession(mux *yamux.Session, identity string) (*Session, error) {
	peer, err := grpc.NewClient("passthrough:///peer",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return mux.Open() }),
	)
	if err != nil {
		_ = mux.Close()

		return nil, fmt.Errorf("creating the peer client: %w", err)
	}

	return &Session{mux: mux, peer: peer, identity: identity}, nil
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

// Identity is the common name of the certificate the peer presented, or empty
// on a connection without TLS.
func (s *Session) Identity() string {
	return s.identity
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
