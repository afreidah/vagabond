---
title: "containerd"
seoTitle: "containerd: Workloads in Containers on Your Own Nodes"
description: "The containerd executor: the containerd provider, how a task becomes a container on an agent node, its process, labels, network, states, output, timeouts, cancellation, release and agent restarts."
weight: 515
---

The containerd executor runs each workload as a container on an agent node's
containerd, sharing the node's kernel, on a network of its own that reaches
the internet but not the node or its private networks unless the task asks
([Networking](#networking)). A provider of type
`containerd` sends tasks to the agents of its pool; every agent runs this
executor. The agent itself, its pools, and how the server places work on a
pool's nodes are in [Agent](agent.md). The other executor, for microVMs, is
[Firecracker](firecracker.md).

![The agent submits a workload to the node's containerd in the vagabond namespace, keyed by its execution ID. containerd pulls the image, unpacks it into an overlayfs snapshot, and runs the task's process under runc as the image's user, on the host network, in the cgroup parent/execution-id. containerd writes its output to data-dir/logs/execution-id.log, which the agent streams to the server, live and once the workload ends.](assets/container.svg)

## Provider

```hcl
provider "homelab" {
  type = "containerd"

  config {
    pool      = "homelab"
    placement = "spread"
  }
}
```

The settings, `pool` and `placement`, and how the provider behaves with no
nodes connected, are in [Declaring a provider](agent.md#declaring-a-provider).
Its nodes publish `provider.isolation` as `container`, and the executor adds no
overhead to what a workload takes from a node's room.

## Requirements

Beyond the agent's own [requirements](agent.md#requirements):

| Requirement | Why |
|---|---|
| containerd answering on `-containerd` | The agent exits at startup without it: `containerd at <socket> does not answer: ...` |
| In a container, `-data-dir` at the same path inside and out | containerd writes each log at the path the agent names, on the host, and the agent reads it back at the same path in its own view |
| Unless `-host-network`: the CNI plugins `ptp`, `host-local` and `loopback` in `-cni-path` | The agent exits at startup without them: `the CNI plugin <name>: ...`. The image has them in `/opt/cni/bin`, the default |
| Unless `-host-network`, in a container: `--network host` and `--pid host` | Each container's link and the firewall rules belong on the host's network, and the agent attaches each container through its process's host `/proc/<pid>/ns/net` |

## Containers

One container per execution, on the node's containerd.

| Property | Value |
|---|---|
| containerd namespace | `vagabond` |
| Container and snapshot ID | The execution ID |
| Runtime | `io.containerd.runc.v2` |
| cgroup | `<parent>/<execution-id>`, parent from [delegation](agent.md#cgroup-delegation) or `-cgroup-parent` |
| Network | A network namespace of its own; the host's with `-host-network` ([Networking](#networking)) |
| `/etc/resolv.conf` | `nameserver <-dns>`; the host's when the task sets `network.private` or with `-host-network` |
| `/etc/hosts` | The host's |
| Mounts | containerd's default OCI spec; no host paths or volumes from the job |
| Image | Pulled and unpacked on every new submission for the node's platform, name normalised (`alpine` -> `docker.io/library/alpine:latest`). Layers already on the node are not downloaded again, but the reference is always resolved against the registry, so an unreachable registry fails the submission even for a cached image. No registry credentials are passed. Images are never removed; delete them with `ctr -n vagabond images rm <ref>`. |
| Snapshotter | containerd's default, `overlayfs`, unless the `vagabond` namespace carries a `containerd.io/defaults/snapshotter` label |
| User | The image's |

### Process

| Task `config` | Process |
|---|---|
| `image` only | Image `ENTRYPOINT` + `CMD` |
| `args` | Image `ENTRYPOINT` + `args` |
| `command` | `command` alone |
| `command` and `args` | `command` + `args` |

- **Environment:** the image's environment, then the task's `env` block and
  `VAGABOND_META_*` variables from `meta`; the task's values win on the same
  name. See [`env` block](job-specification.md#env-block).
- **Working directory:** the task's `working_directory`, else the image's.
  One the image lacks is made by runc, owned by root with mode `0755`.
- **Limits:** `memory` MiB becomes the container's memory limit; `cpu`
  millicores becomes a CFS quota of `cpu * 100` us per 100000 us period.

### Labels

State lives on the container as labels, so it survives the agent:

| Label | Value |
|---|---|
| `vagabond.started` | RFC 3339 time, set just before the task starts |
| `vagabond.timeout` | Task timeout as a Go duration, `0s` for none |
| `vagabond.cpu` | Declared millicores |
| `vagabond.memory` | Declared MiB |
| `vagabond.private` | `true` when the task sets `network.private` |
| `vagabond.address` | The container's address, set just before the task starts; absent with `-host-network` |
| `vagabond.cancelled` | `true` once cancelled |

## Networking

IPv4 only. Each container gets a network namespace of its own, which runc
makes, and the agent connects it to the node through CNI after the task is
created and before its process starts:

| Piece | Value |
|---|---|
| Plugins | `ptp`, `host-local` and `loopback`, from `-cni-path`, `/opt/cni/bin` by default, where the image has them |
| Link | A veth pair: `eth0` in the container, its other end on the node. Each container has its own, with no bridge between them |
| Address | The next free one in `-container-pool`, `172.30.0.0/16` by default, recorded by `host-local` under `<data-dir>/cni/addresses` and on the container as `vagabond.address` |
| Default route | Through the node |
| Name server | `-dns`, `1.1.1.1` by default, from `<data-dir>/cni/resolv.conf`; the node's resolv.conf when the task sets `network.private` |

The pool must not overlap a network the node routes to. A container that
cannot be attached fails its submission, and leaves nothing behind.

### NAT and the firewall

The agent writes one nftables table for the whole pool, named for the
containerd namespace, `vagabond_containers`. It replaces the table whole, in
one transaction, when it starts and before every container starts. The set
`private` holds the address of every container whose task sets
`network.private = true`, read from the containers' labels. With the defaults
and one such container:

```text
table ip vagabond_containers {
    set private {
        type ipv4_addr
        elements = { 172.30.0.7 }
    }
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        ip saddr 172.30.0.0/16 ip daddr != 172.30.0.0/16 masquerade
    }
    chain forward {
        type filter hook forward priority filter; policy accept;
        ip daddr 172.30.0.0/16 ct state established,related accept
        ip daddr 172.30.0.0/16 drop
        ip saddr @private accept
        ip saddr 172.30.0.0/16 ip daddr 1.1.1.1 meta l4proto { tcp, udp } th dport 53 accept
        ip saddr 172.30.0.0/16 ip daddr { 10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16 } drop
    }
    chain input {
        type filter hook input priority filter; policy accept;
        ip saddr 172.30.0.0/16 ct state established,related accept
        ip saddr @private accept
        ip saddr 172.30.0.0/16 ip daddr 1.1.1.1 meta l4proto { tcp, udp } th dport 53 accept
        ip saddr 172.30.0.0/16 drop
    }
}
```

| Rule | Effect |
|---|---|
| `masquerade` | Container traffic leaving the node takes the node's address |
| `ct state established,related accept`, forward | Replies come back in |
| `ip daddr <pool> drop`, forward | Nothing new reaches a container, from the network or from another container |
| `ip saddr @private accept` | A container whose task asked passes the drops after it |
| `th dport 53 accept` | DNS to `-dns` passes, wherever the name server is |
| `ip daddr { ... } drop`, forward | No private range: the RFC 1918 ranges, shared address space and link-local |
| `ip saddr <pool> drop`, input | None of the node's addresses, except to answer what the node opened |

The table only drops, since a drop in any nftables table is final: no rule
here has to outrank an accept elsewhere. Nothing is undone when the agent
stops, since its containers outlive it.

| From | To | Result |
|---|---|---|
| A container | The internet | Reaches it, behind the node's address |
| A container | A private range, such as the LAN | Dropped, unless its task sets `network.private` |
| A container | The node, on any address | Dropped, unless its task sets `network.private` |
| A container | `-dns`, port 53 | Reaches it |
| A container | Another container | Dropped, private or not |
| Another machine, routed through the node | A container | Dropped |
| A process on the node | A container | Reaches it: the node's own traffic is not forwarded, and the table has no output chain |

### Docker

Where Docker manages the firewall with its iptables backend, it sets the
forward policy to drop. Where its `DOCKER-USER` chain exists, the agent
inserts the two rules container traffic needs, each only when it is missing:

```bash
iptables -w -I DOCKER-USER -s 172.30.0.0/16 -j ACCEPT
iptables -w -I DOCKER-USER -d 172.30.0.0/16 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

These accept everything the pool sends; the table's drops still apply.

### Private access

A task that sets `network { private = true }` reaches everything the node
reaches, the node itself included, but still not other containers or new
connections in, and resolves through the node's resolv.conf. Admission does
not change: every node publishes `provider.private_network = true`, since it
grants private access to any task that asks.

### Host network

With `-host-network`, containers run in the node's network namespace, with
its resolv.conf, and reach everything the node does. Nothing is attached and
no table is written, and the CNI plugins are not needed.

## States

| containerd | Execution state |
|---|---|
| Container without a task, never started | `accepted` |
| Container whose task went after it started, as after a host restart | `failed`, with no exit code |
| Task created, not started | `accepted` |
| Task running, paused or other | `running` |
| Task stopped, `vagabond.cancelled=true` | `cancelled` |
| Task stopped, exit 0 | `succeeded` |
| Task stopped, non-zero exit | `failed` |

`Submit` answers with the state the workload reached. The server records a
terminal answer as `running`; the next status poll reports the terminal state
with its result.

## Output

- containerd writes stdout and stderr to `<data-dir>/logs/<execution-id>.log`.
  Every 10 seconds the agent frees the front of a log longer than
  `-max-log-mib`, 16 MiB by default, in whole 256 KiB chunks, as the server
  drops stored output. The freed bytes are a hole: they take no disk, and the
  file keeps its length, so offsets still count from the first byte the
  workload printed. The rest goes when `Release` deletes the file.
- `Result` carries the exit code and run time only. While the workload runs,
  the server holding its dispatch copies its output through
  `AgentExecutions.StreamLogs` into the store, which every client reads
  ([Dispatch](dispatch.md#output)). A stream that breaks is opened again from
  the bytes stored, so it resumes where the last one stopped. When the front
  was dropped before the server read it, the stream begins where the kept part
  does, and the stored copy's `LogsStart` is there. `Release` deletes the file
  only after the capture has ended.
- The stream ends when the workload ends, or when it is released mid-stream,
  after copying whatever the log file still holds.

## Timeouts

A task `timeout` is enforced by the agent, measured from `vagabond.started`.
When it passes, the agent sends `SIGTERM` to the workload's first process, PID
1 in its own PID namespace, then `SIGKILL` 10 seconds later if it has not
exited. PID 1 ignores `SIGTERM` unless it installs a handler, so a plain
`sleep` or `sh -c` workload is killed at 10 seconds. A timed-out workload is
not labelled cancelled: it ends `failed` with its exit status, or `succeeded`
if it exits 0 on `SIGTERM`. No `timeout` means no limit.

## Cancellation

`Cancel` labels the container `vagabond.cancelled=true`, sends `SIGTERM`, and
returns. `SIGKILL` follows after 10 seconds if the process is still running.
The workload ends `cancelled` whatever its exit code. Cancelling a container
with no task only sets the label. Cancelling a workload that already exited
also sets the label, so its state becomes `cancelled` whatever it exited with.
`SIGTERM` goes to the first process only, as for [Timeouts](#timeouts).

## Release

`Release` deletes the task (killing it if still running), frees the
container's address through CNI, and deletes the container, its snapshot, and
its log file. The container's link went with its network namespace when the
task ended. A freed address can be given to the next container, whose start
rewrites the table first. Releasing an execution the node does not hold
succeeds. The execution's runner releases it last, after its output is
captured, however it ended ([Release](dispatch.md#release)). While the node is
offline the server records the execution released without calling it, and
the node releases the workload when it returns; see
[Releasing what nothing needs](agent.md#releasing-what-nothing-needs).

## Agent restarts

On startup the agent lists every container in the `vagabond` namespace and
re-arms each timeout from its labels, with the time remaining since
`vagabond.started`; one already past is stopped at once. It then rewrites the
firewall table from the containers' labels, since a node that rebooted has
lost it. Its first report to
the server lists every container held, so the server counts their room again
and answers with any it has no use for. Every container in the `vagabond`
namespace is reported as held; one without `vagabond.cpu` and
`vagabond.memory` labels counts as zero size.
