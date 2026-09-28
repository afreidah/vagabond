// -------------------------------------------------------------------------------
// server Tests
//
// Author: Alex Freidah
//
// What the server command refuses before it listens. Serving itself is covered
// by internal/server and by every command test here, which runs against one.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"
	"testing"
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

// The agent's CA, certificate and key come together or not at all.
func TestAgentCerts_AllOrNone(t *testing.T) {
	tests := map[string]agentCerts{
		"only a CA":               {ca: "ca.pem"},
		"a certificate, no key":   {ca: "ca.pem", cert: "agent.pem"},
		"a server name, no certs": {serverName: "vagabond"},
	}

	for name, certs := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := certs.config("10.0.0.5:4748"); err == nil {
				t.Error("config() = nil error, want the flags refused")
			}
		})
	}

	if cfg, err := (&agentCerts{}).config("10.0.0.5:4748"); cfg != nil || err != nil {
		t.Errorf("no flags: config() = %v, %v; want no TLS", cfg, err)
	}
}

// Without -dev the server keeps everything in the store, so it needs one.
func TestServer_NeedsAStore(t *testing.T) {
	code, stderr := startServer(t, `provider "fn" { type = "fake-function" }`)

	if code != ExitFailure || !strings.Contains(stderr, "store block, or -dev") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}
