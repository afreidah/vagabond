// -------------------------------------------------------------------------------
// vagabond-firecracker, Run by the Agent
//
// Author: Alex Freidah
//
// Starts vagabond-firecracker as the agent's child, so a node runs microVMs
// from the agent alone. The daemon's socket, records and jails sit under one
// directory of the agent's; it stops with the agent, and its microVMs keep
// running past both.
// -------------------------------------------------------------------------------

package executor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Where the agent image keeps the guest kernel and initramfs every microVM
// boots.
const (
	guestKernel    = "/usr/share/vagabond-firecracker/vmlinux"
	guestInitramfs = "/usr/share/vagabond-firecracker/initramfs.cpio.gz"
)

// How long the daemon has to start serving and to exit once told to, and the
// filesystem the routing table is read from; variables so tests can shorten
// and replace them.
var (
	daemonStartup       = 30 * time.Second
	daemonStop          = 10 * time.Second
	routes        fs.FS = os.DirFS("/")
)

// DaemonConfig is how the agent runs vagabond-firecracker: the binary, looked
// up on PATH when bare; the directory its socket, records and jails go under;
// the user and group Firecracker runs as; the interface microVM traffic leaves
// through, the default route's when empty; the name server microVMs resolve
// through, DefaultDNS when unset; its log level; and how many MiB of each
// workload's output it keeps.
type DaemonConfig struct {
	Binary    string
	Dir       string
	UID, GID  int
	Uplink    string
	DNS       netip.Addr
	LogLevel  string
	MaxLogMiB int
}

// Daemon is a vagabond-firecracker the agent started.
type Daemon struct {
	Socket string

	cmd    *exec.Cmd
	exited chan struct{}
	err    error
}

// StartDaemon starts vagabond-firecracker with its log on logs, and returns
// once it serves on its socket. One that exits first, or does not serve within
// daemonStartup, is an error.
func StartDaemon(ctx context.Context, cfg *DaemonConfig, logs io.Writer) (*Daemon, error) {
	uplink := cfg.Uplink
	if uplink == "" {
		var err error
		if uplink, err = defaultUplink(routes); err != nil {
			return nil, err
		}
	}

	d := &Daemon{Socket: filepath.Join(cfg.Dir, "executor.sock"), exited: make(chan struct{})}

	// Ended by Stop with SIGTERM, not by ctx, whose cancelling would kill it
	d.cmd = exec.CommandContext(context.WithoutCancel(ctx), cfg.Binary, //nolint:gosec // the agent's own binary and flags
		"--socket", d.Socket,
		"--state-dir", filepath.Join(cfg.Dir, "state"),
		"--chroot-base", filepath.Join(cfg.Dir, "jails"),
		"--kernel", guestKernel,
		"--initramfs", guestInitramfs,
		"--uid", strconv.Itoa(cfg.UID),
		"--gid", strconv.Itoa(cfg.GID),
		"--uplink", uplink,
		"--dns", cmp.Or(cfg.DNS, DefaultDNS).String(),
		"--log-level", cfg.LogLevel,
		"--max-log-mib", strconv.Itoa(cfg.MaxLogMiB),
	)
	d.cmd.Stdout, d.cmd.Stderr = logs, logs

	// Stopped with the agent even when the agent is killed outright
	d.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}

	if err := d.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting vagabond-firecracker: %w", err)
	}

	go func() {
		d.err = d.cmd.Wait()
		close(d.exited)
	}()

	if err := d.serving(ctx); err != nil {
		_ = d.Stop()

		return nil, err
	}

	return d, nil
}

// serving waits until the daemon takes a connection on its socket, which also
// passes over a socket a previous daemon left behind.
func (d *Daemon) serving(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, daemonStartup)
	defer cancel()

	var dialer net.Dialer
	for {
		if conn, err := dialer.DialContext(ctx, "unix", d.Socket); err == nil {
			return conn.Close()
		}

		select {
		case <-d.exited:
			return fmt.Errorf("vagabond-firecracker exited while starting: %w", d.err)
		case <-ctx.Done():
			return fmt.Errorf("vagabond-firecracker did not serve on %s within %s", d.Socket, daemonStartup)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Done is closed once the daemon has exited, after which Err says why.
func (d *Daemon) Done() <-chan struct{} {
	return d.exited
}

// Err is how the daemon exited, once Done is closed.
func (d *Daemon) Err() error {
	return d.err
}

// Stop tells the daemon to exit and waits for it, killing it after daemonStop.
// Its microVMs keep running.
func (d *Daemon) Stop() error {
	_ = d.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-d.exited:
	case <-time.After(daemonStop):
		_ = d.cmd.Process.Kill()
		<-d.exited
	}

	// Asked to stop, so a SIGTERM exit is a clean one
	if exit, ok := errors.AsType[*exec.ExitError](d.err); ok && !exit.Exited() {
		return nil
	}

	return d.err
}

// defaultUplink is the interface the default route leaves through, read from
// the kernel's routing table.
func defaultUplink(fsys fs.FS) (string, error) {
	table, err := fs.ReadFile(fsys, "proc/net/route")
	if err != nil {
		return "", fmt.Errorf("reading the routing table: %w", err)
	}

	for line := range strings.Lines(string(table)) {
		if fields := strings.Fields(line); len(fields) > 1 && fields[1] == "00000000" {
			return fields[0], nil
		}
	}

	return "", errors.New("no default route for microVM traffic to leave through; set -firecracker-uplink")
}
