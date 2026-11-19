// -------------------------------------------------------------------------------
// Fake vagabond-firecracker
//
// Author: Alex Freidah
//
// Lets a test binary stand in for vagabond-firecracker, so tests of what starts
// the daemon run it as a real child process: started with Env set, the binary
// acts out the mode it names instead of running its tests.
// -------------------------------------------------------------------------------

package fakedaemon

import (
	"context"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// Env names the fake daemon's mode: "serve" serves on --socket until SIGTERM,
// "stubborn" serves and ignores SIGTERM, "brief" serves for half a second and
// exits 0, "silent" never serves, and "exit" exits 3 at once.
const Env = "VAGABOND_FAKE_DAEMON"

// Run acts as the daemon when Env is set, never returning, and returns at once
// otherwise. TestMain calls it first.
func Run() {
	mode := os.Getenv(Env)
	if mode == "" {
		return
	}

	if mode == "exit" {
		os.Exit(3)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)

	if mode == "stubborn" {
		signal.Ignore(syscall.SIGTERM)
	}

	if mode != "silent" {
		serve()
	}

	select {
	case <-stop:
	case <-briefly(mode):
	}

	os.Exit(0)
}

// briefly ends half a second from now in "brief" mode, and never otherwise.
func briefly(mode string) <-chan time.Time {
	if mode != "brief" {
		return nil
	}

	return time.After(500 * time.Millisecond)
}

// serve accepts and closes connections on the socket after --socket, making
// its directory first, as the daemon does.
func serve() {
	var lc net.ListenConfig

	socket := os.Args[slices.Index(os.Args, "--socket")+1]
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil { //nolint:gosec // a path the test gave
		os.Exit(1)
	}

	listener, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		os.Exit(1)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			_ = conn.Close()
		}
	}()
}
