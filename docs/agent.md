# Agent

`vagabond agent` runs a client on a node eligible for execution. It dials the
server, registers the node into a pool, and runs the workloads the server sends
it on the node's containerd.

Scheduling onto nodes arrives with pools (#90). Until then a connected node is
listed by `vagabond node status` and nothing is dispatched to it.

## Running it

```bash
vagabond agent -server 10.0.0.5:4748 -pool homelab -label gpu=no
```

| Flag | Default | |
|---|---|---|
| `-server` | `127.0.0.1:4748` | The server's `agent_bind` address |
| `-pool` | `default` | Pool the node joins |
| `-name` | hostname | Node name; a client reconnecting under it replaces its old connection |
| `-label k=v` | | Repeatable |
| `-cpu`, `-memory` | | Caps, in millicores and MiB |
| `-containerd` | `/run/containerd/containerd.sock` | containerd's socket |
| `-data-dir` | `/var/lib/vagabond` | Workload output |
| `-cgroup-parent` | the agent's own cgroup | Operator-prepared cgroup to create workloads under |
| `-log-level` | `info` | |

## Capacity

The smallest of:

- the host's CPUs and `MemTotal`
- `cpu.max` and `memory.max` on the agent's cgroup and every cgroup above it
- `-cpu` and `-memory`

Workloads are created under the agent's cgroup, so the kernel holds them to
that capacity. At startup the agent moves itself into a leaf, `<cgroup>/agent`,
and enables the `cpu` and `memory` controllers for its children.

## Requirements

- cgroup v2
- Access to containerd's socket, and permission to create cgroups under its own

It runs as a systemd unit, in a container, or even as a containerized or
`raw_exec` job within your Nomad cluster.

Bare metal, as a systemd unit:

```ini
[Service]
ExecStart=/usr/local/bin/vagabond agent -server 10.0.0.5:4748 -pool homelab
Delegate=yes
```

`Delegate=yes` lets the agent manage the cgroups below its own. `CPUQuota=` and
`MemoryMax=` on the unit become its capacity.

In a container, the work runs on the host's containerd, so the container needs:

| | Why |
|---|---|
| `/run/containerd/containerd.sock` mounted | Starting workloads |
| `--cgroupns=host`, `/sys/fs/cgroup` writable | Its cgroup path must be the host's, for containerd to place workloads under it |
| `-data-dir` mounted at the same path inside and out | containerd writes workload output there |
| Privileged, or `CAP_SYS_ADMIN` | Creating cgroups and containers |

A missing requirement fails at startup, naming the fix.

## Workloads

- One container per execution, in containerd namespace `vagabond`; the
  container ID is the execution ID
- runc runtime; host network namespace, resolv.conf and hosts
- Output is written to `<data-dir>/logs/<id>.log` by containerd
- Timeouts, cancellation and start times are kept as container labels, so a
  restarted agent finds its workloads and their timeouts where it left them
- Cancel sends SIGTERM, then SIGKILL after 10 seconds

## Protocol

One TCP connection from agent to server, multiplexed with yamux. Each side
serves gRPC on the streams the other opens:

| Service | Served by | Calls |
|---|---|---|
| `Node` | server | `Register`: node, pool, labels, capacity, runtimes, workloads held |
| `ClientExecutions` | agent | `Submit`, `Status`, `Result`, `Cancel`, `Release`, `StreamLogs` |

The connection dropping is the heartbeat: the node leaves `node status` and the
agent dials again with backoff. TLS on this connection is not built yet.

Definitions: `internal/agentrpc/agent.proto`; regenerate with `make generate`.

## Testing

`make containerd-test` runs real workloads on the host's containerd. It builds
the test binary as the user and runs it with `sudo`.
