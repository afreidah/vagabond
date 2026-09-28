# Agent

`vagabond agent` runs on a node eligible for execution, apart from the server.
It dials the server, registers the node into a pool, and runs the workloads the
server sends it on the node's containerd. The server schedules onto the pool
through a [`pool` provider](configuration.md#types); `vagabond node status`
lists the connected nodes and how much of each is in use.

## Running it

```bash
vagabond agent -server 10.0.0.5:4748 -pool homelab -label gpu=no
```

| Flag | Default | |
|---|---|---|
| `-server` | `127.0.0.1:4748` | The server's `agent_bind` address |
| `-pool` | `default` | Pool the node joins |
| `-name` | hostname | Node name; an agent reconnecting under it replaces its old connection |
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

## Pools

A pool is declared on the server as a provider, and nodes join it by name:

```hcl
provider "homelab" {
  type = "pool"
}
```

```bash
vagabond agent -server 10.0.0.5:4748 -pool homelab -label gpu=yes
```

**Admission:** each node is judged on its own, as if it were the whole
provider, with the pool's quota, allowlist and health. The pool is admitted
when at least one node passes every check; otherwise it is rejected with the
reason of the node that failed the fewest, as `closest node <name>: ...`. A task
needing arm64 and 8 GiB is never admitted because one node has arm64 and
another has 8 GiB.

**Node attributes:** each node offers the `container` driver, its own
architecture, and as its resource limit the CPU and memory it has left. Labels
from `-label` are constraint attributes under `node.label.`:

```hcl
constraint {
  attribute = "node.label.gpu"
  value     = "yes"
}
```

**Placement:** the task goes to the admitted node with the most free memory.
The server holds the room it placed there until the node's next report
accounts for the workload, or for 2 minutes at most, so two dispatches cannot
both take the last room. A node that refuses for lack of room is passed over for
the next; no node left is an infrastructure failure, and dispatch tries the next
provider. The node's name is the execution's provider ID.

**Room:** a node's room is its capacity less what its running workloads
declared. Declared sizes rather than live usage: each workload is capped at what
it declared, so their sum is what the node can be asked for at once. A task
that declares no resources is sized at 1000 millicores and 1024 MiB.

**A node leaving mid-run:** its executions report `lost` for 5 minutes while
the agent may be restarting. A node that returns reports its workloads and they
finish normally; after 5 minutes the pool gives up on them. For the first 5
minutes after a server starts, an execution no node has reported yet is also
`lost`, since the node holding it may not have reconnected.

## Workloads

- One container per execution, in containerd namespace `vagabond`; the
  container ID is the execution ID
- runc runtime; host network namespace, resolv.conf and hosts
- Output is written to `<data-dir>/logs/<id>.log` by containerd
- Timeouts, cancellation, start times and declared resources are kept as
  container labels, so a restarted agent finds its workloads where it left them
- The agent refuses a workload that does not fit what its running workloads
  leave; the server then tries another node
- Cancel sends SIGTERM, then SIGKILL after 10 seconds

## Protocol

One TCP connection from agent to server, multiplexed with yamux. Each side
serves gRPC on the streams the other opens:

| Service | Served by | Calls |
|---|---|---|
| `Node` | server | `Register`: node, pool, labels, capacity, runtimes, and every workload held with its declared resources |
| `AgentExecutions` | agent | `Submit`, `Status`, `Result`, `Cancel`, `Release`, `StreamLogs` |

The agent registers when it connects, again whenever it starts, cancels or
releases a workload, and every 10 seconds, which catches a workload finishing
on its own. The connection dropping is the heartbeat: the node leaves
`node status` and the agent dials again with backoff.

Definitions: `internal/agentrpc/agent.proto`; regenerate with `make generate`.

## Testing

`make containerd-test` runs real workloads on the host's containerd. It builds
the test binary as the user and runs it with `sudo`.
