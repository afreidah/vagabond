// -------------------------------------------------------------------------------
// vagabond-firecracker Launch Tests
//
// Author: Alex Freidah
//
// Starting, watching and stopping the daemon, with the test binary standing in
// for vagabond-firecracker, and reading the default route's interface.
// -------------------------------------------------------------------------------

package executor

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/afreidah/vagabond/internal/agent/executor/executortest/fakedaemon"
)

// TestMain runs the fake daemon when asked to, and the tests otherwise.
func TestMain(m *testing.M) {
	fakedaemon.Run()
	os.Exit(m.Run())
}

// fakeConfig runs the test binary as the daemon, in mode, under a directory of
// the test's.
func fakeConfig(t *testing.T, mode string) *DaemonConfig {
	t.Setenv(fakedaemon.Env, mode)

	return &DaemonConfig{Binary: os.Args[0], Dir: t.TempDir(), UID: 990, GID: 990, Uplink: "lo", LogLevel: "info"}
}

// shorten sets the daemon's start and stop limits for one test.
func shorten(t *testing.T, startup, stop time.Duration) {
	oldStartup, oldStop := daemonStartup, daemonStop
	daemonStartup, daemonStop = startup, stop

	t.Cleanup(func() { daemonStartup, daemonStop = oldStartup, oldStop })
}

// The daemon is up once it serves on its socket, and stops cleanly when told.
func TestStartDaemon_ServesAndStops(t *testing.T) {
	d, err := StartDaemon(t.Context(), fakeConfig(t, "serve"), io.Discard)
	if err != nil {
		t.Fatalf("StartDaemon() = %v", err)
	}

	var dialer net.Dialer

	conn, err := dialer.DialContext(t.Context(), "unix", d.Socket)
	if err != nil {
		t.Fatalf("dialling %s = %v, want it served", d.Socket, err)
	}

	_ = conn.Close()

	if err := d.Stop(); err != nil {
		t.Errorf("Stop() = %v, want a clean exit", err)
	}

	select {
	case <-d.Done():
	default:
		t.Error("Done() still open after Stop()")
	}
}

// A daemon that exits before serving fails the start with how it exited.
func TestStartDaemon_ExitsWhileStarting(t *testing.T) {
	_, err := StartDaemon(t.Context(), fakeConfig(t, "exit"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exited while starting") {
		t.Fatalf("StartDaemon() = %v, want it to say the daemon exited while starting", err)
	}
}

// A daemon that never serves fails the start once daemonStartup passes, and
// is stopped.
func TestStartDaemon_NeverServes(t *testing.T) {
	shorten(t, 300*time.Millisecond, time.Second)

	_, err := StartDaemon(t.Context(), fakeConfig(t, "silent"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "did not serve") {
		t.Fatalf("StartDaemon() = %v, want it to say the daemon did not serve", err)
	}
}

// A binary that is not there fails the start.
func TestStartDaemon_NoBinary(t *testing.T) {
	cfg := fakeConfig(t, "serve")
	cfg.Binary = "/nonexistent/vagabond-firecracker"

	if _, err := StartDaemon(t.Context(), cfg, io.Discard); err == nil {
		t.Fatal("StartDaemon() = nil, want an error for a missing binary")
	}
}

// With no uplink given the default route's is used, and a node without one
// cannot start the daemon.
func TestStartDaemon_DefaultUplink(t *testing.T) {
	old := routes
	t.Cleanup(func() { routes = old })

	routes = fstest.MapFS{"proc/net/route": {Data: []byte("Iface\tDestination\neth0\t00000000\n")}}

	cfg := fakeConfig(t, "serve")
	cfg.Uplink = ""

	d, err := StartDaemon(t.Context(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("StartDaemon() = %v", err)
	}

	_ = d.Stop()

	routes = fstest.MapFS{}
	if _, err := StartDaemon(t.Context(), cfg, io.Discard); err == nil {
		t.Error("StartDaemon() without a routing table = nil, want an error")
	}
}

// A daemon that ignores SIGTERM is killed once daemonStop passes.
func TestDaemon_StopKillsAStubbornDaemon(t *testing.T) {
	shorten(t, 10*time.Second, 300*time.Millisecond)

	d, err := StartDaemon(t.Context(), fakeConfig(t, "stubborn"), io.Discard)
	if err != nil {
		t.Fatalf("StartDaemon() = %v", err)
	}

	if err := d.Stop(); err != nil {
		t.Errorf("Stop() = %v, want the kill taken as stopped", err)
	}
}

// A daemon that dies after starting closes Done, and Err says how.
func TestDaemon_DoneWhenItDies(t *testing.T) {
	d, err := StartDaemon(t.Context(), fakeConfig(t, "serve"), io.Discard)
	if err != nil {
		t.Fatalf("StartDaemon() = %v", err)
	}

	_ = d.cmd.Process.Kill()
	<-d.Done()

	if exit, ok := errors.AsType[*exec.ExitError](d.Err()); !ok || exit.Exited() {
		t.Errorf("Err() = %v, want the kill", d.Err())
	}
}

// The default route's interface is the one with an all-zero destination, and
// a table without one, or none at all, is an error.
func TestDefaultUplink(t *testing.T) {
	table := "Iface\tDestination\tGateway\tFlags\n" +
		"wg0\t0000FEA9\t00000000\t0001\n" +
		"eth0\t00000000\t0100A8C0\t0003\n"

	uplink, err := defaultUplink(fstest.MapFS{"proc/net/route": {Data: []byte(table)}})
	if err != nil || uplink != "eth0" {
		t.Errorf("defaultUplink() = %q, %v; want eth0", uplink, err)
	}

	noDefault := fstest.MapFS{"proc/net/route": {Data: []byte("Iface\tDestination\nwg0\t0000FEA9\n")}}
	if _, err := defaultUplink(noDefault); err == nil {
		t.Error("defaultUplink() without a default route = nil, want an error")
	}

	if _, err := defaultUplink(fstest.MapFS{}); err == nil {
		t.Error("defaultUplink() without a routing table = nil, want an error")
	}
}
