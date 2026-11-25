// -------------------------------------------------------------------------------
// vagabond agent
//
// Author: Alex Freidah
//
// Runs the agent on a node eligible for execution until interrupted. The same
// command runs on bare metal and in a container; what differs is only what the
// environment gives it, and a missing piece fails here, before the node
// registers, rather than at the first workload.
// -------------------------------------------------------------------------------

package cli

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/logging"
	"github.com/afreidah/vagabond/internal/version"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Defaults for where the agent finds containerd and keeps workload output, and
// the containerd namespaces its containers and its microVMs' snapshots live in.
const (
	defaultContainerd    = "/run/containerd/containerd.sock"
	defaultDataDir       = "/var/lib/vagabond"
	workloadNamespace    = "vagabond"
	firecrackerNamespace = "vagabond-firecracker"
)

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

// AgentCommand implements `vagabond agent`.
type AgentCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *AgentCommand) Synopsis() string {
	return "Run the agent that executes workloads on this node"
}

// Help returns the full usage text.
func (c *AgentCommand) Help() string {
	text := `
Usage: vagabond agent [options]

  Runs the agent on this node: dials the server, registers the node into a
  pool, and runs the workloads the server sends it on the node's containerd,
  and with -firecracker in Firecracker microVMs, through a vagabond-firecracker
  it starts itself, until interrupted. Workloads keep running if the agent
  stops, and it finds them again when it starts.

  Requires cgroup v2, access to containerd's socket, and permission to create
  cgroups under its own: Delegate=yes under systemd, or --cgroupns=host with
  /sys/fs/cgroup writable in a container. -firecracker also requires root,
  /dev/kvm, containerd's devmapper snapshotter, and in a container the host's
  PID namespace and /dev.

  Capacity is the smallest of the host's CPU and memory, the limits on the
  agent's cgroup, and -cpu and -memory.

Agent Options:

  -server <addr>
    The server's agent address. Defaults to 127.0.0.1:4748.

  -pool <name>
    The pool the node joins. Defaults to "default".

  -name <name>
    The node's name. Defaults to the certificate's common name with TLS, and
    to the hostname without. With TLS it must match the certificate.

  -label <key>=<value>
    A label on the node, repeatable.

  -cpu <millicores>, -memory <MiB>
    Caps on what the agent may use.

  -containerd <path>
    containerd's socket. Defaults to /run/containerd/containerd.sock.

  -firecracker
    Run workloads in Firecracker microVMs as well as in containers, through a
    vagabond-firecracker the agent starts, keeping its state and jails under
    <data-dir>/firecracker. The agent stops when it exits. Off by default.

  -firecracker-uid <uid>, -firecracker-gid <gid>
    The user and group Firecracker runs as, given to nothing else on the node.
    Default 990.

  -firecracker-uplink <interface>
    The interface microVM traffic leaves through. Defaults to the default
    route's.

  -data-dir <path>
    Where workload output is kept. Defaults to /var/lib/vagabond; in a
    container it must be the same path inside and out.

  -max-log-mib <MiB>
    How much of each workload's output the node keeps, its front dropped past
    it. At least the server's max_log_mib, or the server keeps less. Default 16.

  -tls-ca <path>, -tls-cert <path>, -tls-key <path>
    Mutual TLS with a server that has agent_tls: the CA file or directory the
    server's certificate chains to, and this agent's certificate and key. All
    three or none.

  -tls-server-name <name>
    The name the server's certificate must carry. Defaults to the host part of
    -server.

  -cgroup-parent <path>
    A cgroup to create workloads under, prepared by the operator, in place of
    the agent's own.

  -log-level <level>
    debug, info, warn or error. Defaults to info. vagabond-firecracker logs at
    the same level.

  -log-json
    Write each log line as JSON, for a log collector, in place of text.
    vagabond-firecracker's own lines stay text.
`

	return strings.TrimSpace(text)
}

// Run prepares the node and stays connected to the server until an interrupt
// or SIGTERM.
func (c *AgentCommand) Run(args []string) int {
	var (
		server, pool, name, socket, dataDir, cgroupParent string
		labels                                            metaFlags
		caps                                              fingerprint.Caps
		certs                                             tlsFlags
		firecracker                                       bool
		vmm                                               executor.DaemonConfig
		maxLogMiB                                         int
		logs                                              logging.Options
	)

	host, _ := os.Hostname()

	flags := c.FlagSet("agent")
	flags.StringVar(&server, "server", config.DefaultAgentBind, "the server's agent address")
	flags.StringVar(&pool, "pool", "default", "the pool the node joins")
	flags.StringVar(&name, "name", host, "the node's name")
	flags.Var(&labels, "label", "a label on the node as key=value, repeatable")
	flags.Int64Var(&caps.CPU, "cpu", 0, "millicores the agent may use")
	flags.Int64Var(&caps.Memory, "memory", 0, "MiB the agent may use")
	flags.StringVar(&socket, "containerd", defaultContainerd, "containerd's socket")
	flags.BoolVar(&firecracker, "firecracker", false, "run workloads in Firecracker microVMs too")
	flags.IntVar(&vmm.UID, "firecracker-uid", 990, "the user Firecracker runs as")
	flags.IntVar(&vmm.GID, "firecracker-gid", 990, "the group Firecracker runs as")
	flags.StringVar(&vmm.Uplink, "firecracker-uplink", "", "the interface microVM traffic leaves through")
	flags.StringVar(&dataDir, "data-dir", defaultDataDir, "where workload output is kept")
	flags.IntVar(&maxLogMiB, "max-log-mib", int(execution.DefaultMaxLogBytes>>20), "MiB of each workload's output kept")
	flags.StringVar(&cgroupParent, "cgroup-parent", "", "a cgroup to create workloads under")
	logs.Register(flags)
	certs.register(flags, tlsFlags{})

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	tlsConfig, name, err := identify(&certs, server, name, flagSet(flags, "name"))
	if err != nil {
		return c.Errorf("%s", err)
	}

	if maxLogMiB < 1 {
		return c.Errorf("Invalid -max-log-mib %d: a node keeps at least 1 MiB of each workload's output.", maxLogMiB)
	}

	logger := logging.New(c.ErrStream, logs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	node, err := fingerprint.Take(os.DirFS("/"), caps)
	if err != nil {
		return c.Errorf("Reading the node: %s", err)
	}

	parent := cgroupParent
	if parent == "" {
		if parent, err = agent.Delegate(node.Cgroup); err != nil {
			return c.Errorf("%s", err)
		}
	}

	exec, err := executor.New(&executor.Config{
		Socket: socket, Namespace: workloadNamespace, DataDir: dataDir, Cgroup: parent,
		MaxLogBytes: int64(maxLogMiB) << 20, Logger: logger,
	})
	if err != nil {
		return c.Errorf("%s", err)
	}

	defer func() { _ = exec.Close() }()

	execs := []agent.Executor{exec}

	// MicroVMs as well, through a vagabond-firecracker of the agent's own, whose
	// exit stops the agent rather than leave it running without them.
	if firecracker {
		vmm.Binary, vmm.Dir = "vagabond-firecracker", filepath.Join(dataDir, "firecracker")
		vmm.LogLevel = strings.ToLower(logs.Level.String())
		vmm.MaxLogMiB = maxLogMiB

		var (
			vm   *executor.Firecracker
			done func()
		)

		if ctx, vm, done, err = startFirecracker(ctx, &vmm, c.ErrStream, socket, parent); err != nil {
			return c.Errorf("%s", err)
		}

		defer done()

		execs = append(execs, vm)
	}

	if err := exec.Recover(ctx); err != nil {
		return c.Errorf("Finding workloads from before: %s", err)
	}

	logging.Named(logger, "agent").InfoContext(ctx, "node read",
		"node", name, "pool", pool, "cpu", node.CPU, "memory", node.Memory, "cgroup", parent)

	cfg := agent.Config{
		Server: server, Pool: pool, Name: name, Labels: labels, TLS: tlsConfig, Version: version.String(),
	}

	a, err := agent.New(ctx, &cfg, node, logger, execs...)
	if err != nil {
		return c.Errorf("%s", err)
	}

	if err := ended(ctx, a.Run(ctx)); err != nil {
		return c.Errorf("%s", err)
	}

	return ExitSuccess
}

// errExitedZero is the daemon's exit when it ended cleanly, which the agent
// did not ask of it.
var errExitedZero = errors.New("exit status 0")

// startFirecracker starts the agent's vagabond-firecracker and an executor on
// it, and returns ctx ended when the daemon exits, with a function that closes
// the executor and stops the daemon.
func startFirecracker(
	ctx context.Context, vmm *executor.DaemonConfig, logs io.Writer, containerd, cgroup string,
) (context.Context, *executor.Firecracker, func(), error) {
	daemon, err := executor.StartDaemon(ctx, vmm, logs)
	if err != nil {
		return ctx, nil, nil, err
	}

	ctx, cancel := context.WithCancelCause(ctx)

	go func() {
		select {
		case <-daemon.Done():
			cancel(fmt.Errorf("vagabond-firecracker exited: %w", cmp.Or(daemon.Err(), errExitedZero)))
		case <-ctx.Done():
		}
	}()

	vm, err := executor.NewFirecracker(executor.FirecrackerConfig{
		Socket: daemon.Socket, Containerd: containerd, Namespace: firecrackerNamespace, Cgroup: cgroup,
	})
	if err != nil {
		cancel(nil)
		_ = daemon.Stop()

		return ctx, nil, nil, err
	}

	return ctx, vm, func() {
		_ = vm.Close()
		cancel(nil)
		_ = daemon.Stop()
	}, nil
}

// ended is what ended the agent: Run's own error, or the daemon's exit that
// ended its context, but not a signal.
func ended(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); err == nil && !errors.Is(cause, context.Canceled) {
		return cause
	}

	return err
}

// -------------------------------------------------------------------------
// TLS
// -------------------------------------------------------------------------

// identify builds the agent's TLS and settles the node's name: with TLS it is
// the certificate's common name, which a given -name must match.
func identify(certs *tlsFlags, server, name string, nameGiven bool) (*tls.Config, string, error) {
	tlsConfig, err := agentTLS(certs, server)
	if err != nil || tlsConfig == nil {
		return tlsConfig, name, err
	}

	identity, err := agentrpc.Identity(tlsConfig)
	if err != nil {
		return nil, "", err
	}

	if nameGiven && name != identity {
		return nil, "", fmt.Errorf("-name %q does not match the certificate's common name %q", name, identity)
	}

	return tlsConfig, identity, nil
}

// agentTLS builds the agent's TLS for dialing server, or nil when no -tls flag
// is set. The CA, certificate and key come together or not at all, since the
// agent listener always verifies agents.
func agentTLS(certs *tlsFlags, server string) (*tls.Config, error) {
	switch {
	case !certs.set():
		return nil, nil
	case certs.ca == "" && certs.cert == "" && certs.key == "":
		return nil, errors.New("-tls-server-name needs -tls-ca, -tls-cert and -tls-key")
	case certs.ca == "" || certs.cert == "" || certs.key == "":
		return nil, errors.New("-tls-ca, -tls-cert and -tls-key go together: set all three or none")
	}

	// The server's certificate must carry the dialled host by default, since
	// the session dials TCP and handshakes itself
	host := certs.serverName
	if host == "" {
		var err error

		if host, _, err = net.SplitHostPort(server); err != nil {
			return nil, err
		}
	}

	return certs.config(host)
}

// flagSet reports whether name was given on the command line, as opposed to
// left at its default.
func flagSet(flags *flag.FlagSet, name string) bool {
	set := false

	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})

	return set
}
