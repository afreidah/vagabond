// -------------------------------------------------------------------------------
// vagabond agent
//
// Author: Alex Freidah
//
// Runs a client on a node eligible for execution until interrupted. The same
// command runs on bare metal and in a container; what differs is only what the
// environment gives it, and a missing piece fails here, before the node
// registers, rather than at the first workload.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/afreidah/vagabond/internal/client"
	"github.com/afreidah/vagabond/internal/client/executor"
	"github.com/afreidah/vagabond/internal/client/fingerprint"
	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/version"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Defaults for where the agent finds containerd and keeps workload output, and
// the containerd namespace its workloads live in.
const (
	defaultContainerd = "/run/containerd/containerd.sock"
	defaultDataDir    = "/var/lib/vagabond"
	workloadNamespace = "vagabond"
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
	return "Run a client that executes workloads on this node"
}

// Help returns the full usage text.
func (c *AgentCommand) Help() string {
	text := `
Usage: vagabond agent [options]

  Runs a client on this node: dials the server, registers the node into a
  pool, and runs the workloads the server sends it on the node's containerd,
  until interrupted. Workloads keep running if the agent stops, and it finds
  them again when it starts.

  Requires cgroup v2, access to containerd's socket, and permission to create
  cgroups under its own: Delegate=yes under systemd, or --cgroupns=host with
  /sys/fs/cgroup writable in a container.

  Capacity is the smallest of the host's CPU and memory, the limits on the
  agent's cgroup, and -cpu and -memory.

Agent Options:

  -server <addr>
    The server's client address. Defaults to 127.0.0.1:4748.

  -pool <name>
    The pool the node joins. Defaults to "default".

  -name <name>
    The node's name. Defaults to the hostname.

  -label <key>=<value>
    A label on the node, repeatable.

  -cpu <millicores>, -memory <MiB>
    Caps on what the agent may use.

  -containerd <path>
    containerd's socket. Defaults to /run/containerd/containerd.sock.

  -data-dir <path>
    Where workload output is kept. Defaults to /var/lib/vagabond; in a
    container it must be the same path inside and out.

  -cgroup-parent <path>
    A cgroup to create workloads under, prepared by the operator, in place of
    the agent's own.

  -log-level <level>
    debug, info, warn or error. Defaults to info.
`

	return strings.TrimSpace(text)
}

// Run prepares the node and stays connected to the server until an interrupt
// or SIGTERM.
func (c *AgentCommand) Run(args []string) int {
	var (
		server, pool, name, socket, dataDir, cgroupParent, logLevel string
		labels                                                      metaFlags
		caps                                                        fingerprint.Caps
	)

	host, _ := os.Hostname()

	flags := c.FlagSet("agent")
	flags.StringVar(&server, "server", config.DefaultAgentBind, "the server's client address")
	flags.StringVar(&pool, "pool", "default", "the pool the node joins")
	flags.StringVar(&name, "name", host, "the node's name")
	flags.Var(&labels, "label", "a label on the node as key=value, repeatable")
	flags.Int64Var(&caps.CPU, "cpu", 0, "millicores the agent may use")
	flags.Int64Var(&caps.Memory, "memory", 0, "MiB the agent may use")
	flags.StringVar(&socket, "containerd", defaultContainerd, "containerd's socket")
	flags.StringVar(&dataDir, "data-dir", defaultDataDir, "where workload output is kept")
	flags.StringVar(&cgroupParent, "cgroup-parent", "", "a cgroup to create workloads under")
	flags.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(logLevel)); err != nil {
		return c.Errorf("Invalid -log-level %q: use debug, info, warn or error.", logLevel)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	node, err := fingerprint.Take(os.DirFS("/"), caps)
	if err != nil {
		return c.Errorf("Reading the node: %s", err)
	}

	parent := cgroupParent
	if parent == "" {
		if parent, err = client.Delegate(node.Cgroup); err != nil {
			return c.Errorf("%s", err)
		}
	}

	exec, err := executor.New(executor.Config{
		Socket: socket, Namespace: workloadNamespace, DataDir: dataDir, Cgroup: parent,
	})
	if err != nil {
		return c.Errorf("%s", err)
	}

	defer func() { _ = exec.Close() }()

	if err := exec.Check(ctx); err != nil {
		return c.Errorf("%s", err)
	}

	if err := exec.Recover(ctx); err != nil {
		return c.Errorf("Finding workloads from before: %s", err)
	}

	logger := slog.New(slog.NewTextHandler(c.ErrStream, &slog.HandlerOptions{Level: level}))
	logger.InfoContext(ctx, "node", "name", name, "pool", pool, "cpu", node.CPU, "memory", node.Memory, "cgroup", parent)

	cfg := client.Config{Server: server, Pool: pool, Name: name, Labels: labels, Version: version.String()}

	if err := client.New(&cfg, node, exec, logger).Run(ctx); err != nil {
		return c.Errorf("%s", err)
	}

	return ExitSuccess
}
