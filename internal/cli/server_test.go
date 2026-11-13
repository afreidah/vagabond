// -------------------------------------------------------------------------------
// server Tests
//
// Author: Alex Freidah
//
// What the server command refuses before it listens, and that agent serving
// stopping on its own stops the server. Serving itself is covered by
// internal/server and by every command test here, which runs against one.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// startServer runs the server command with a temporary configuration, from a
// directory where nothing is discovered.
func startServer(t *testing.T, cfg string, extra ...string) (int, string) {
	t.Helper()

	t.Setenv("VAGABOND_CONFIG", "")
	t.Chdir(t.TempDir())

	code, _, stderr := run(append([]string{"server", "-config", writeConfig(t, cfg)}, extra...)...)

	return code, stderr
}

// A server with no configuration names where it looked.
func TestServer_NoConfiguration(t *testing.T) {
	t.Setenv("VAGABOND_CONFIG", "")
	t.Chdir(t.TempDir())

	code, _, stderr := run("server")

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	for _, want := range []string{"no configuration found", "VAGABOND_CONFIG", "vagabond.hcl"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error is missing %q:\n%s", want, stderr)
		}
	}
}

// A configuration that parses and declares no provider is refused, since there
// is nothing to run on.
func TestServer_NoProviders(t *testing.T) {
	code, stderr := startServer(t, "# nothing here\n", "-dev")

	if code != ExitFailure || !strings.Contains(stderr, "No providers are configured") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// A malformed configuration reports why.
func TestServer_BadConfiguration(t *testing.T) {
	code, stderr := startServer(t, `provider "broken" { type = }`, "-dev")

	if code != ExitFailure || stderr == "" {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// An agent listener reachable beyond this machine needs agent_tls, since an
// agent without a certificate could register as any node.
func TestServer_AgentBindBeyondLoopbackNeedsTLS(t *testing.T) {
	code, stderr := startServer(t, `
provider "fn" { type = "fake-function" }
server { agent_bind = "0.0.0.0:0" }
`, "-dev")

	if code != ExitFailure || !strings.Contains(stderr, "agent_tls") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// An API address already taken stops the server command with why, after it
// opened the agent listener.
func TestServer_APIAddressInUse(t *testing.T) {
	taken := listen(t)

	code, stderr := startServer(t, fmt.Sprintf(`
provider "fn" { type = "fake-function" }

server {
  bind       = %q
  agent_bind = "127.0.0.1:0"
}
`, taken.Addr().String()), "-dev")

	if code != ExitFailure || !strings.Contains(stderr, "listening on") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// The server stops with whichever part stops first: agent serving that stops
// on its own takes the API down and says why, so the server never answers
// while no agent can connect, and the caller giving up is a clean stop.
func TestRunServer_StopsWithEitherPart(t *testing.T) {
	tests := []struct {
		name string
		stop func(t *testing.T, agents net.Listener, api string, cancel context.CancelFunc)
		want string // in the error; empty for none
	}{
		{"agent listener closed", func(_ *testing.T, agents net.Listener, _ string, _ context.CancelFunc) {
			_ = agents.Close()
		}, "serving agents"},
		{"caller gives up", func(t *testing.T, _ net.Listener, api string, cancel context.CancelFunc) {
			accepting(t, api)
			cancel()
		}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := devFixture(t, `provider "fn" { type = "fake-function" }`)
			agents := listen(t)
			api := freeAddress(t)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := make(chan error, 1)

			go func() { done <- runServer(ctx, srv, agents, api, nil) }()

			tt.stop(t, agents, api, cancel)

			select {
			case err := <-done:
				if (tt.want == "") != (err == nil) || !strings.Contains(fmt.Sprint(err), tt.want) {
					t.Errorf("runServer() = %v, want an error naming %q", err, tt.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("runServer() kept running")
			}
		})
	}
}

// listen opens a loopback listener on a free port, closed when the test ends.
// Failing to open one fails the test.
func listen(t *testing.T) net.Listener {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = l.Close() })

	return l
}

// freeAddress is a loopback address nothing listens on, for a server to
// take.
func freeAddress(t *testing.T) string {
	t.Helper()

	l := listen(t)
	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

// accepting waits until addr accepts connections, failing the test after
// five seconds.
func accepting(t *testing.T, addr string) {
	t.Helper()

	var d net.Dialer

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if conn, err := d.DialContext(t.Context(), "tcp", addr); err == nil {
			_ = conn.Close()

			return
		}
	}

	t.Fatalf("%s never accepted a connection", addr)
}

// Only addresses that accept connections from this machine alone count as
// loopback.
func TestLoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:4748": true,
		"[::1]:4748":     true,
		"localhost:4748": true,
		"0.0.0.0:4748":   false,
		":4748":          false,
		"10.0.0.5:4748":  false,
		"not an address": false,
	}

	for addr, want := range tests {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Without -dev the server keeps everything in the store, so it needs one.
func TestServer_NeedsAStore(t *testing.T) {
	code, stderr := startServer(t, `provider "fn" { type = "fake-function" }`)

	if code != ExitFailure || !strings.Contains(stderr, "store block, or -dev") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}
