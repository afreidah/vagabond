---
title: "containerd"
seoTitle: "containerd: Workloads in Containers on Your Own Nodes"
description: "The containerd executor: the containerd provider, how a task becomes a container on an agent node, its process, labels, states, output, timeouts, cancellation, release and agent restarts."
weight: 515
---

The containerd executor runs each workload as a container on an agent node's
containerd, sharing the node's kernel and network. A provider of type
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

## Containers

One container per execution, on the node's containerd.

| Property | Value |
|---|---|
| containerd namespace | `vagabond` |
| Container and snapshot ID | The execution ID |
| Runtime | `io.containerd.runc.v2` |
| cgroup | `<parent>/<execution-id>`, parent from [delegation](agent.md#cgroup-delegation) or `-cgroup-parent` |
| Network | Host network namespace |
| `/etc/resolv.conf`, `/etc/hosts` | The host's |
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
| `vagabond.cancelled` | `true` once cancelled |

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
  The file is neither rotated nor capped; it grows until `Release` deletes it.
- `Result` carries the exit code and run time only. Once the workload ends the
  server reads its whole output through `AgentExecutions.StreamLogs` from
  offset 0, before `Release` deletes the file, and stores it
  ([Dispatch](dispatch.md#output)).
- While the workload runs, the server streams its output through
  `AgentExecutions.StreamLogs`, from the byte offset the client asks for, so a
  `?follow=true` request ([API](api.md#read-execution-logs)) shows it live and
  a reopened stream resumes where the last one stopped.
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

`Release` deletes the task (killing it if still running), the container, its
snapshot, and its log file. Releasing an execution the node does not hold
succeeds. The execution's runner releases it last, after its output is
captured, however it ended ([Release](dispatch.md#release)). While the node is
offline the server records the execution released without calling it, and
the node releases the workload when it returns; see
[Releasing what nothing needs](agent.md#releasing-what-nothing-needs).

## Agent restarts

On startup the agent lists every container in the `vagabond` namespace and
re-arms each timeout from its labels, with the time remaining since
`vagabond.started`; one already past is stopped at once. Its first report to
the server lists every container held, so the server counts their room again
and answers with any it has no use for. Every container in the `vagabond`
namespace is reported as held; one without `vagabond.cpu` and
`vagabond.memory` labels counts as zero size.
