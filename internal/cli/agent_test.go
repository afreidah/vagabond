// -------------------------------------------------------------------------------
// Agent Command Tests
//
// Author: Alex Freidah
//
// The agent's own vagabond-firecracker: started and stopped with it, its exit
// ending the agent, with the test binary standing in for the daemon.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/cli"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/executor/executortest/fakedaemon"
	"github.com/afreidah/vagabond/internal/testkit"
)

// TestMain runs the fake daemon when asked to, and the tests otherwise.
func TestMain(m *testing.M) {
	fakedaemon.Run()
	os.Exit(m.Run())
}

// daemonConfig runs the test binary as the daemon, in mode.
func daemonConfig(t *testing.T, mode string) *executor.DaemonConfig {
	t.Setenv(fakedaemon.Env, mode)

	return &executor.DaemonConfig{Binary: os.Args[0], Dir: t.TempDir(), UID: 990, GID: 990, Uplink: "lo", LogLevel: "info"}
}

// The daemon starts with an executor on it, and stopping them ends the agent's
// context as a stop, not a failure.
func TestStartFirecracker_StartsAndStops(t *testing.T) {
	containerd := filepath.Join(t.TempDir(), "containerd.sock")

	ctx, vm, done, err := startFirecracker(t.Context(), daemonConfig(t, "serve"), io.Discard, containerd, "/vagabond-test")
	if err != nil {
		t.Fatalf("startFirecracker() = %v", err)
	}

	if vm == nil {
		t.Fatal("startFirecracker() gave no executor")
	}

	done()
	<-ctx.Done()

	if err := ended(ctx, nil); err != nil {
		t.Errorf("ended() after done = %v, want nil", err)
	}
}

// A daemon that exits on its own ends the agent's context with the reason.
func TestStartFirecracker_DaemonExitEndsTheAgent(t *testing.T) {
	containerd := filepath.Join(t.TempDir(), "containerd.sock")

	ctx, _, done, err := startFirecracker(t.Context(), daemonConfig(t, "brief"), io.Discard, containerd, "/vagabond-test")
	if err != nil {
		t.Fatalf("startFirecracker() = %v", err)
	}

	defer done()
	<-ctx.Done()

	if err := ended(ctx, nil); err == nil || !strings.Contains(err.Error(), "vagabond-firecracker exited: exit status 0") {
		t.Errorf("ended() = %v, want the daemon's exit", err)
	}
}

// onPath puts the test binary on PATH as vagabond-firecracker, acting in mode.
func onPath(t *testing.T, mode string) {
	dir := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(dir, "vagabond-firecracker")); err != nil {
		t.Fatalf("linking the fake daemon: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakedaemon.Env, mode)
}

// runAgent runs the agent with -firecracker against a containerd that is not
// there, so it gets as far as re-arming container timeouts, and returns its
// exit code and stderr.
func runAgent(t *testing.T) (int, string) {
	// Written by the agent and its daemon's output copier at once
	var stderr testkit.Buffer

	args := []string{
		"agent", "-containerd", filepath.Join(t.TempDir(), "containerd.sock"),
		"-cgroup-parent", "/vagabond-test", "-data-dir", t.TempDir(),
		"-firecracker", "-firecracker-uplink", "lo",
	}
	code := Run(args, strings.NewReader(""), io.Discard, &stderr)

	return code, stderr.String()
}

// The agent starts its daemon before it needs containerd, and stops it when
// it then fails.
func TestAgent_StartsItsDaemon(t *testing.T) {
	onPath(t, "serve")

	code, stderr := runAgent(t)
	if code != ExitFailure || !strings.Contains(stderr, "Finding workloads from before") {
		t.Errorf("agent = %d, %q; want it past the daemon, failing on containerd", code, stderr)
	}
}

// A daemon that does not start stops the agent before anything else.
func TestAgent_DaemonFailureStopsIt(t *testing.T) {
	onPath(t, "exit")

	code, stderr := runAgent(t)
	if code != ExitFailure || !strings.Contains(stderr, "exited while starting") {
		t.Errorf("agent = %d, %q; want the daemon's failure", code, stderr)
	}
}

// A cap on kept output under a MiB is refused before the agent reads the
// node.
func TestAgent_RefusesAMaxLogUnderAMiB(t *testing.T) {
	var stderr bytes.Buffer

	code := Run([]string{"agent", "-max-log-mib", "0"}, strings.NewReader(""), io.Discard, &stderr)
	if code != ExitFailure || !strings.Contains(stderr.String(), "Invalid -max-log-mib 0") {
		t.Errorf("agent -max-log-mib 0 = %d, %q; want it refused", code, stderr.String())
	}
}

// An executor that cannot be made stops the daemon it was for.
func TestStartFirecracker_NoContainerd(t *testing.T) {
	if _, _, _, err := startFirecracker(t.Context(), daemonConfig(t, "serve"), io.Discard, "", ""); err == nil {
		t.Fatal("startFirecracker() without containerd = nil, want an error")
	}
}

// A group word on its own asks the library for its help, subcommands listed.
func TestGroupCommand_AsksForHelp(t *testing.T) {
	if code := (&groupCommand{}).Run(nil); code != cli.RunResultHelp {
		t.Errorf("Run() = %d, want cli.RunResultHelp", code)
	}
}

// A daemon that cannot start is the agent's error.
func TestStartFirecracker_DaemonFails(t *testing.T) {
	cfg := daemonConfig(t, "exit")

	if _, _, _, err := startFirecracker(t.Context(), cfg, io.Discard, "", ""); err == nil {
		t.Fatal("startFirecracker() = nil, want the daemon's failure")
	}
}

// ended passes Run's own error through, takes a cancel cause in its place, and
// reports nothing for a signal.
func TestEnded(t *testing.T) {
	own := errors.New("run failed")
	if err := ended(t.Context(), own); !errors.Is(err, own) {
		t.Errorf("ended() = %v, want Run's error", err)
	}

	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("daemon gone")
	cancel(cause)

	if err := ended(ctx, nil); !errors.Is(err, cause) {
		t.Errorf("ended() = %v, want the cause", err)
	}

	signalled, stop := context.WithCancel(t.Context())
	stop()

	if err := ended(signalled, nil); err != nil {
		t.Errorf("ended() after a signal = %v, want nil", err)
	}

	if err := ended(t.Context(), nil); err != nil {
		t.Errorf("ended() of a live context = %v, want nil", err)
	}
}
