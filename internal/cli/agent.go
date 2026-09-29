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
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/afreidah/vagabond/internal/agent"
	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/fingerprint"
	"github.com/afreidah/vagabond/internal/agentrpc"
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
	return "Run the agent that executes workloads on this node"
}

// Help returns the full usage text.
func (c *AgentCommand) Help() string {
	text := `
Usage: vagabond agent [options]

  Runs the agent on this node: dials the server, registers the node into a
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

  -data-dir <path>
    Where workload output is kept. Defaults to /var/lib/vagabond; in a
    container it must be the same path inside and out.

  -tls-ca <path>, -tls-cert <path>, -tls-key <path>
    Mutual TLS with a server that has agent_tls: the CA the server's
    certificate chains to, and this agent's certificate and key. All three or
    none.

  -tls-server-name <name>
    The name the server's certificate must carry. Defaults to the host part of
    -server.

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
		certs                                                       agentCerts
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
	flags.StringVar(&dataDir, "data-dir", defaultDataDir, "where workload output is kept")
	flags.StringVar(&cgroupParent, "cgroup-parent", "", "a cgroup to create workloads under")
	flags.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")
	flags.StringVar(&certs.ca, "tls-ca", "", "the CA the server's certificate chains to")
	flags.StringVar(&certs.cert, "tls-cert", "", "this agent's certificate")
	flags.StringVar(&certs.key, "tls-key", "", "this agent's key")
	flags.StringVar(&certs.serverName, "tls-server-name", "", "the name the server's certificate carries")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	tlsConfig, name, err := certs.identify(server, name, flagSet(flags, "name"))
	if err != nil {
		return c.Errorf("%s", err)
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
		if parent, err = agent.Delegate(node.Cgroup); err != nil {
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

	cfg := agent.Config{
		Server: server, Pool: pool, Name: name, Labels: labels, TLS: tlsConfig, Version: version.String(),
	}

	if err := agent.New(&cfg, node, exec, logger).Run(ctx); err != nil {
		return c.Errorf("%s", err)
	}

	return ExitSuccess
}

// -------------------------------------------------------------------------
// TLS
// -------------------------------------------------------------------------

// agentCerts is what the -tls flags name.
type agentCerts struct {
	ca, cert, key, serverName string
}

// identify builds the agent's TLS and settles the node's name. With TLS the
// node is its certificate's name: the default when -name was not given, and
// the only name the server accepts when it was.
func (a *agentCerts) identify(server, name string, nameGiven bool) (*tls.Config, string, error) {
	tlsConfig, err := a.config(server)
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

// config builds the agent's TLS for dialing server, or nil when no -tls flag
// is set. The CA, certificate and key come together or not at all.
func (a *agentCerts) config(server string) (*tls.Config, error) {
	set := 0

	for _, path := range []string{a.ca, a.cert, a.key} {
		if path != "" {
			set++
		}
	}

	switch set {
	case 0:
		if a.serverName != "" {
			return nil, errors.New("-tls-server-name needs -tls-ca, -tls-cert and -tls-key")
		}

		return nil, nil
	case 3:
	default:
		return nil, errors.New("-tls-ca, -tls-cert and -tls-key go together: set all three or none")
	}

	// The server's certificate is checked against the name it was dialled by,
	// unless told otherwise.
	serverName := a.serverName
	if serverName == "" {
		host, _, err := net.SplitHostPort(server)
		if err != nil {
			return nil, err
		}

		serverName = host
	}

	return agentrpc.ClientTLS(a.cert, a.key, a.ca, serverName)
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
