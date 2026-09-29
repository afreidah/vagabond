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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// CERTIFICATES
// -------------------------------------------------------------------------

// testCA is a certificate authority written to a temporary directory.
type testCA struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	caFile string
	dir    string
}

// newTestCA mints a CA and writes its certificate to disk.
func newTestCA(t *testing.T) *testCA {
	t.Helper()

	key := newKey(t)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vagabond test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate(CA) = %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate(CA) = %v", err)
	}

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	writePEM(t, caFile, "CERTIFICATE", der)

	return &testCA{cert: cert, key: key, caFile: caFile, dir: dir}
}

// issue signs a certificate for commonName, for a server on 127.0.0.1 or for
// an agent, and returns its certificate and key files.
func (ca *testCA) issue(t *testing.T, commonName string, server bool) (certFile, keyFile string) {
	t.Helper()

	key := newKey(t)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("CreateCertificate(%s) = %v", commonName, err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey() = %v", err)
	}

	certFile = filepath.Join(ca.dir, commonName+".pem")
	keyFile = filepath.Join(ca.dir, commonName+"-key.pem")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)

	return certFile, keyFile
}

// newKey generates a P-256 key.
func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}

	return key
}

// writePEM writes one PEM block to path.
func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()

	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// tlsHarness is a server accepting agents over mutual TLS, trusting ca.
type tlsHarness struct {
	*agentHarness
	ca *testCA
}

// newTLSHarness starts a server whose agent listener requires certificates
// signed by a fresh CA.
func newTLSHarness(t *testing.T) *tlsHarness {
	t.Helper()

	ca := newTestCA(t)
	certFile, keyFile := ca.issue(t, "server", true)

	serverTLS, err := agentrpc.ServerTLS(certFile, keyFile, ca.caFile)
	if err != nil {
		t.Fatalf("ServerTLS() = %v", err)
	}

	reg, led := fixtures(t)
	srv := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	return &tlsHarness{agentHarness: serveAgents(t, srv, serverTLS), ca: ca}
}

// clientTLS is an agent's TLS with a certificate for commonName from ca.
func (h *tlsHarness) clientTLS(t *testing.T, ca *testCA, commonName string) *tls.Config {
	t.Helper()

	certFile, keyFile := ca.issue(t, commonName, false)

	cfg, err := agentrpc.ClientTLS(certFile, keyFile, h.ca.caFile, "127.0.0.1")
	if err != nil {
		t.Fatalf("ClientTLS() = %v", err)
	}

	return cfg
}

// register dials the harness and registers a node called name, returning what
// the server answered.
func (h *tlsHarness) register(t *testing.T, cfg *tls.Config, name string) error {
	t.Helper()

	session, err := agentrpc.Dial(t.Context(), h.address, cfg)
	if err != nil {
		return err
	}

	defer func() { _ = session.Close() }()

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
		"no certificate":    {RootCAs: h.clientTLS(t, h.ca, "unused").RootCAs, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12},
		"another CA's cert": h.clientTLS(t, newTestCA(t), "box1"),
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
