// -------------------------------------------------------------------------------
// Agent TLS Tests
//
// Author: Alex Freidah
//
// Mutual TLS on the agent listener, against certificates minted per test: an
// agent with a certificate the CA signed registers under its common name; one
// with no certificate, one from another CA, and one claiming another node's
// name are refused.
// -------------------------------------------------------------------------------

package server

import (
	"crypto/tls"
	"log/slog"
	"testing"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/tlsconfig"
	"github.com/afreidah/vagabond/internal/tlsconfig/tlstest"
)

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// tlsHarness is a server accepting agents over mutual TLS, trusting ca.
type tlsHarness struct {
	*agentHarness
	ca *tlstest.CA
}

// newTLSHarness starts a server whose agent listener requires certificates
// signed by a fresh CA.
func newTLSHarness(t *testing.T) *tlsHarness {
	t.Helper()

	ca := tlstest.NewCA(t)
	certFile, keyFile := ca.Server(t)

	serverTLS, err := tlsconfig.Server(certFile, keyFile, ca.File, tls.RequireAndVerifyClientCert)
	if err != nil {
		t.Fatalf("Server() = %v", err)
	}

	reg, led := fixtures(t)
	srv := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	return &tlsHarness{agentHarness: serveAgents(t, srv, serverTLS), ca: ca}
}

// clientTLS is an agent's TLS with a certificate for commonName from ca.
func (h *tlsHarness) clientTLS(t *testing.T, ca *tlstest.CA, commonName string) *tls.Config {
	t.Helper()

	certFile, keyFile := ca.Client(t, commonName)

	cfg, err := tlsconfig.Client(h.ca.File, certFile, keyFile, "127.0.0.1")
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}

	return cfg
}

// register dials the harness and registers a node called name, returning what
// the server answered. The session stays open until the test ends, since
// closing it is the node leaving.
func (h *tlsHarness) register(t *testing.T, cfg *tls.Config, name string) error {
	t.Helper()

	session, err := agentrpc.Dial(t.Context(), h.address, cfg)
	if err != nil {
		return err
	}

	t.Cleanup(func() { _ = session.Close() })

	_, err = agentrpc.NewNodeClient(session.Peer()).Register(t.Context(), &agentrpc.NodeRegisterRequest{
		Name: name, Pool: "homelab", Capacity: &agentrpc.Resources{Cpu: 1000, Memory: 1024},
	})

	return err
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// An agent with a certificate the CA signed registers under its common name.
func TestAgentTLS_Registers(t *testing.T) {
	h := newTLSHarness(t)

	if err := h.register(t, h.clientTLS(t, h.ca, "box1"), "box1"); err != nil {
		t.Fatalf("register = %v", err)
	}

	if _, ok := h.srv.nodeConns.Get("box1"); !ok {
		t.Error("box1 is not connected")
	}
}

// An agent that presents no certificate, or one another CA signed, never gets
// to register.
func TestAgentTLS_RefusesUntrustedAgents(t *testing.T) {
	h := newTLSHarness(t)

	tests := map[string]*tls.Config{
		"no TLS":            nil,
		"no certificate":    {RootCAs: h.clientTLS(t, h.ca, "unused").RootCAs, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13},
		"another CA's cert": h.clientTLS(t, tlstest.NewCA(t), "box1"),
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := h.register(t, cfg, "box1"); err == nil {
				t.Error("registered, want refused")
			}

			if _, ok := h.srv.nodeConns.Get("box1"); ok {
				t.Error("box1 is connected")
			}
		})
	}
}

// An agent cannot register under a name its certificate does not carry, which
// is what would let it replace another node.
func TestAgentTLS_NameMustMatchTheCertificate(t *testing.T) {
	h := newTLSHarness(t)

	err := h.register(t, h.clientTLS(t, h.ca, "box1"), "box2")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("register as box2 with box1's certificate = %v, want PermissionDenied", err)
	}

	if _, ok := h.srv.nodeConns.Get("box2"); ok {
		t.Error("box2 is connected")
	}
}
