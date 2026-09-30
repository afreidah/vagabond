// -------------------------------------------------------------------------------
// Test Server
//
// Author: Alex Freidah
//
// Every command but job validate talks to a server, so these tests start one
// in-process: the configured fake providers over memory stores, as server -dev
// runs them, behind httptest. Nothing has credentials and nothing contacts
// anything outside the process.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/server"
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

	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	// Nothing may come from the environment, or a developer's own settings
	// would change what these tests assert.
	t.Setenv(addressEnv, "")
	t.Setenv(namespaceEnv, "")

	return &testServer{address: httpServer.URL, registry: reg}
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
