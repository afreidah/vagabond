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

// Without -dev the server keeps everything in the store, so it needs one.
func TestServer_NeedsAStore(t *testing.T) {
	code, stderr := startServer(t, `provider "fn" { type = "fake-function" }`)

	if code != ExitFailure || !strings.Contains(stderr, "store block, or -dev") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}
