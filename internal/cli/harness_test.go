// -------------------------------------------------------------------------------
// Test Server
//
// Author: Alex Freidah
//
// Every command but job validate talks to a server, so these tests start one
// in-process: the configured fake providers over memory stores, as server -dev
// runs them, behind httptest or on a loopback TLS listener. Nothing has
// credentials and nothing contacts anything outside the process.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/tlsconfig"
	"github.com/afreidah/vagabond/internal/tlsconfig/tlstest"
)

// testServer is a running server and the registry behind it, so a test can
// change how a fake provider answers.
type testServer struct {
	address  string
	registry *registry.Registry
}

// serve starts a server over the providers cfg configures, with every store in
// memory, and stops it when the test ends.
func serve(t *testing.T, cfg string) *testServer {
	t.Helper()

	srv, reg := devFixture(t, cfg)

	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	return &testServer{address: httpServer.URL, registry: reg}
}

// serveTLS starts the same server over TLS with a certificate from ca,
// requiring client certificates ca signed.
func serveTLS(t *testing.T, cfg string, ca *tlstest.CA) *testServer {
	t.Helper()

	srv, reg := devFixture(t, cfg)
	certFile, keyFile := ca.Server(t)

	tlsConfig, err := tlsconfig.Server(certFile, keyFile, ca.File, tls.VerifyClientCertIfGiven)
	if err != nil {
		t.Fatalf("Server() = %v", err)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- srv.ServeListener(ctx, listener, tlsConfig) }()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	return &testServer{address: "https://" + listener.Addr().String(), registry: reg}
}

// devFixture builds the providers cfg configures and a server over them with
// every store in memory, as server -dev does, and clears the environment the
// client commands read so a developer's own settings cannot leak in.
func devFixture(t *testing.T, cfg string) (*server.Server, *registry.Registry) {
	t.Helper()

	ctx := context.Background()

	file, diags := config.Load("test.hcl", []byte(cfg))
	if diags.HasErrors() {
		t.Fatalf("config: %s", diags.Error())
	}

	conns := nodes.New()

	reg, diags := registry.New(ctx, file, registry.WithNodes(conns))
	if diags.HasErrors() {
		t.Fatalf("registry: %s", diags.Error())
	}

	if err := reg.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}

	srv, err := devServer(ctx, reg, slog.New(slog.DiscardHandler), server.WithNodes(conns))
	if err != nil {
		t.Fatalf("devServer() = %v", err)
	}

	clearClientEnv(t)

	return srv, reg
}

// clearClientEnv empties every variable the client commands read.
func clearClientEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{addressEnv, namespaceEnv, tlsCAEnv, tlsCertEnv, tlsKeyEnv, tlsServerNameEnv} {
		t.Setenv(name, "")
	}
}

// at runs a command against the server, with -address placed after the
// command's own words.
func (s *testServer) at(words []string, args ...string) (int, string, string) {
	full := append(append(append([]string{}, words...), "-address", s.address), args...)

	return run(full...)
}

// writeConfig puts configuration in a temporary file.
func writeConfig(t *testing.T, src string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vagabond.hcl")

	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	return path
}
