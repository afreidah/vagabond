---
title: "Running Jobs in Firecracker MicroVMs"
linkTitle: "Firecracker MicroVMs"
description: "Prepare an agent node with KVM and a devmapper thin pool, start the agent with -firecracker, add a firecracker provider, and run a job in a microVM."
weight: 30
---

This guide takes an agent node that already runs containers and has it run
jobs in Firecracker microVMs as well: each task in its own kernel, booted from
its image, on the same node and in the same pool.

## Overview

A `firecracker` provider sends tasks to the agents of a pool, as a
`containerd` provider does. An agent started with `-firecracker` runs each one
in a microVM:

1. The agent pulls the task's image through the node's containerd into a
   devmapper snapshot, which is a block device, and resolves the image's
   process and user.
2. It hands the snapshot to `vagabond-firecracker`, a daemon the agent starts
   itself as a child process.
3. The daemon builds a jail, a cgroup sized to the task, and a tap device
   behind NAT, and boots Firecracker on the guest kernel with the snapshot as
   the root drive.
4. Inside the microVM, `vagabond-init` mounts the root drive, runs the
   process as the image's user, and reports its output and exit back to the
   daemon.

![The agent pulls each image through containerd into a devmapper snapshot and resolves its process and user, then calls vagabond-firecracker over the executor protocol, ttRPC on a socket only root can open. The daemon gives each workload a jail, a cgroup, and a tap behind NAT, and starts Firecracker through the jailer, its memory limited to what the workload declared plus Firecracker's overhead. The microVM boots into vagabond-init, which mounts the snapshot as its root drive, runs the workload as the image's user, and talks to the daemon over vsock.](../docs/assets/firecracker.svg)

Everything the daemon boots ships in the amd64 `vagabond` image: the daemon,
Firecracker and its jailer, the guest kernel and the initramfs. The node
provides KVM and containerd's devmapper snapshotter; the steps below set those
up. The [Firecracker reference](../docs/firecracker.md) covers every flag and
the [Firecracker diagram](../diagrams/firecracker.md) the path through the
daemon.

The examples come from a node named `nomad-client-04`: a Proxmox VM running
Debian 12, Docker's containerd 2.2, and the agent as a Nomad job in pool
`homelab`.

## Prerequisites

- A Vagabond server, and an agent on the node already connected to it in a
  pool ([Running Jobs in Containers on Agent Nodes](containers-on-agent-nodes.md))
- The node on x86_64 with KVM. A virtual machine needs nested virtualization:
  on Proxmox, CPU type `host`
- containerd on the node, such as the one Docker's `containerd.io` package
  installs
- Root on the node, and the `dmsetup` and `losetup` tools
- Free disk for the thin pool the microVMs' root drives are carved from
- The amd64 `vagabond` image, v0.1.32 or later

## Step 1: Check KVM

```shell
$ ls -l /dev/kvm
crw-rw---- 1 root kvm 10, 232 Jul 27 11:01 /dev/kvm
```

## Step 2: Make a Thin Pool

containerd's devmapper snapshotter turns each image into a block device, the
microVM's root drive, carved from a device-mapper thin pool. A pool over two
sparse files needs no spare disk partition: `data` holds the snapshots and
`meta` the pool's bookkeeping. Both use disk only as they are written.

![Two sparse files, data and meta, are attached to loop devices. At boot, before containerd starts, dmsetup builds the thin pool containerd-thinpool over them. containerd's devmapper snapshotter carves one thin snapshot per image from the pool, and each microVM boots with its snapshot as its root drive.](../docs/assets/thin-pool.svg)

Size `data` to what the disk can hold once microVMs fill it. This node has
106G free, so the pool below takes 50G:

```shell
$ df -h /
Filesystem      Size  Used Avail Use% Mounted on
/dev/sda1       138G   27G  106G  21% /
```

The pool must exist before containerd starts, after every boot. This script
makes the files the first time, attaches them to loop devices, and creates
the pool, leaving a pool that is already up as it is:

```bash
#!/bin/bash
# /usr/local/sbin/containerd-thinpool
set -euo pipefail

dir=/var/lib/containerd-thinpool
pool=containerd-thinpool

dmsetup status "${pool}" > /dev/null 2>&1 && exit 0

[[ -e "${dir}/data" ]] || truncate -s 50G "${dir}/data"
[[ -e "${dir}/meta" ]] || truncate -s 1G "${dir}/meta"

# The loop device a file is already on, or a new one
attach() {
  local loop
  loop=$(losetup --associated "$1" --noheadings --output NAME | head -n 1)
  echo "${loop:-$(losetup --find --show "$1")}"
}

data=$(attach "${dir}/data")
meta=$(attach "${dir}/meta")
dmsetup create "${pool}" --table "0 $(blockdev --getsz "${data}") thin-pool ${meta} ${data} 128 32768"
```

The table's `128` is the pool's block size in 512-byte sectors (64 KiB) and
`32768` the low-water mark in blocks.

A oneshot unit runs it ahead of containerd. `WantedBy` rather than
`RequiredBy` keeps containerd, and Docker with it, starting if the pool does
not come up:

```ini
# /etc/systemd/system/containerd-thinpool.service
[Unit]
Description=containerd's devmapper thin pool
After=local-fs.target
Before=containerd.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStartPre=/sbin/modprobe dm_thin_pool
ExecStart=/usr/local/sbin/containerd-thinpool

[Install]
WantedBy=containerd.service
```

```shell
$ mkdir -m 0700 /var/lib/containerd-thinpool
$ chmod 0755 /usr/local/sbin/containerd-thinpool
$ systemctl daemon-reload
$ systemctl enable --now containerd-thinpool
$ dmsetup status containerd-thinpool
0 104857600 thin-pool 0 4171/262144 0/819200 - rw discard_passdown queue_if_no_space - 1024
$ ls -ls /var/lib/containerd-thinpool
total 312
  0 -rw-r--r-- 1 root root 53687091200 Nov 19 12:26 data
312 -rw-r--r-- 1 root root  1073741824 Nov 19 12:27 meta
```

`0/819200` is data blocks used of 819200 (50 GiB at 64 KiB) and `4171/262144`
the metadata blocks. The first column of `ls -s` is the disk each file takes:
none yet for 50 GiB of data.

## Step 3: Turn On containerd's Devmapper Snapshotter

Point containerd's devmapper snapshotter at the pool in
`/etc/containerd/config.toml`. Docker's `containerd.io` package ships that
file with one setting, `disabled_plugins = ["cri"]`, and no `version`; the
file below keeps CRI's server disabled as it does:

```toml
version = 3

disabled_plugins = ["io.containerd.grpc.v1.cri"]

[plugins."io.containerd.snapshotter.v1.devmapper"]
  pool_name = "containerd-thinpool"
  base_image_size = "10GB"
  fs_type = "ext4"
  discard_blocks = true
```

| Setting | Effect |
|---|---|
| `version = 3` | containerd 2.x's own format, read without migration |
| `disabled_plugins` | CRI's server, as Docker's file disables it. With a `version` set, containerd takes only full plugin IDs, not the short `"cri"` |
| `pool_name` | The pool from step 2 |
| `base_image_size` | Each image's snapshot, and so each microVM's disk. Thin, so it costs only what is written |
| `fs_type` | The filesystem made on each snapshot. The guest kernel has `ext4` built in |
| `discard_blocks` | Blocks a deleted snapshot frees go back to the pool, and from there out of the sparse `data` file |

Restart containerd. Containers Docker runs keep running across the restart.

```shell
$ systemctl restart containerd
$ ctr plugins ls | grep devmapper
io.containerd.snapshotter.v1              devmapper                linux/amd64    ok
```

`ok` means the snapshotter found the pool.

## Step 4: Reserve Firecracker's User

Firecracker runs inside its jail as a user and group given to nothing else on
the node, 990 unless the agent's `-firecracker-uid` and `-firecracker-gid` say
otherwise. Creating them reserves the IDs:

```shell
$ groupadd --system --gid 990 firecracker
$ useradd --system --uid 990 --gid 990 --shell /usr/sbin/nologin --home-dir /nonexistent firecracker
$ id firecracker
uid=990(firecracker) gid=990(firecracker) groups=990(firecracker)
```

## Step 5: Start the Agent with -firecracker

`-firecracker` starts the daemon beside the agent's containerd executor. Its
container needs three more things than a containers-only agent:

| Setting | Why |
|---|---|
| The host's PID namespace | Each microVM's record keeps Firecracker's host pid, so a restarted agent finds its microVMs, and they are not ended with the container |
| The host's `/dev` | Each root drive is a devmapper device made after the container started, which a privileged container's own `/dev` does not show |
| The host network | The taps, the NAT rules and the Docker firewall rules belong on the host |

`-data-dir` must be on a filesystem that allows device files, since the jails
under `<data-dir>/firecracker/jails` hold device nodes; a tmpfs mounted
`nodev` does not.

As a Nomad task with the Docker driver, the agent's `config` block:

```hcl
config {
  image        = "registry.example.com/vagabond:v0.1.34"
  network_mode = "host"
  pid_mode     = "host"
  privileged   = true
  cgroupns     = "host"

  args = [
    "agent",
    "-server", "vagabond.service.consul:4748",
    "-pool", "homelab",
    "-tls-ca", "/secrets/ca.pem",
    "-tls-cert", "/secrets/agent.pem",
    "-tls-key", "/secrets/agent-key.pem",
    "-cgroup-parent", "/vagabond",
    "-firecracker",
  ]

  volumes = [
    "/run/containerd/containerd.sock:/run/containerd/containerd.sock",
    "/var/lib/vagabond:/var/lib/vagabond",
    "/var/lib/containerd:/var/lib/containerd:ro",
    "/dev:/dev",
  ]
}
```

With `docker run`, the same settings are `--network host --pid host
--privileged --cgroupns host -v /dev:/dev` beside the agent's other mounts.

The daemon logs first, then the agent:

```text
2026-11-19T23:23:50.162212Z  INFO serving socket=/var/lib/vagabond/firecracker/executor.sock
time=2026-11-19T23:23:50.233Z level=INFO msg="node read" component=agent node=nomad-client-04 pool=homelab cpu=2010 memory=4608 cgroup=/vagabond
time=2026-11-19T23:23:50.247Z level=INFO msg=connected component=agent server_addr=vagabond.service.consul:4748 node=nomad-client-04 pool=homelab
```

The node now reports both executors:

```shell
$ vagabond node status
NAME             POOL     ADDRESS              ARCH   EXECUTORS               CPU USED  MEMORY USED  WORKLOADS  CONNECTED
nomad-client-04  homelab  192.168.68.73:52788  amd64  containerd,firecracker  0/2010m   0/4608MiB    0          2026-11-19 23:23:50Z
```

## Step 6: Add a Firecracker Provider

In the server's configuration, a `firecracker` provider on the same pool as
the `containerd` one. `pool` defaults to the provider's label, so a second
provider on one pool names it:

```hcl
provider "homelab" {
  type = "containerd"
  tier = 1
}

provider "homelab-vm" {
  type = "firecracker"
  tier = 1

  config {
    pool = "homelab"
  }
}
```

Both providers draw on the same nodes and the same capacity. The job in step 7
names `homelab-vm` in its `routing` block to run in a microVM.

After the server restarts with the new configuration:

```shell
$ vagabond provider status
PROVIDER         TIER  STATUS   QUOTA LEFT
gcp-cloud-run    0     healthy  cpu 92%, memory 91%
ibm-code-engine  0     healthy  cpu 91%, memory 91%
homelab          1     healthy  no quota
homelab-vm       1     healthy  no quota

Usage as of 2026-11-19 23:38:34Z (1s ago).
```

## Step 7: Run a Job in a MicroVM

`firecracker-hello.vagabond.hcl` prints the kernel it booted:

```hcl
job "firecracker-hello" {
  routing {
    providers = ["homelab-vm"]
  }

  task "hello" {
    driver = "container"

    config {
      image   = "busybox:1.36"
      command = "sh"
      args    = ["-c", "uname -a; cat /proc/cpuinfo | grep -m1 'model name'; echo hello from a microVM"]
    }

    resources {
      cpu    = 500
      memory = 256
    }

    timeout = "5m"
  }
}
```

```shell
$ vagabond job run firecracker-hello.vagabond.hcl
==> dispatch 01a1f588-b90f-78d3-a6ef-34245d6a0286 of "firecracker-hello"
==> hello pending on homelab-vm (execution 01a1f588-b91a-70cd-bb85-3c9a1feb2ff0)
==> hello running on homelab-vm
Linux 172.16.0.2 6.1.155+ #1 SMP PREEMPT_DYNAMIC Thu Dec 18 15:17:16 UTC 2025 x86_64 GNU/Linux
model name      : Intel(R) Xeon(R) Processor
hello from a microVM
==> hello succeeded on homelab-vm in 14ms
```

- `6.1.155+` is the guest kernel the image carries, not the node's
  Debian kernel.
- `172.16.0.2` is the microVM's own address, the guest side of the `/30` the
  daemon gave it from `172.16.0.0/16`. The boot gives the kernel no hostname,
  so it names the guest by its address.
- The 500 millicores and 256 MiB become one vCPU and 256 MiB of guest memory,
  in a cgroup holding the microVM to half a core and 288 MiB, the declared
  memory plus Firecracker's 32 MiB.
- `14ms` is the process's run time inside the guest. Pulling the image into a
  snapshot and booting come before it.

## Step 8: Verify

The execution's record names the provider and the node:

```shell
$ vagabond execution status 01a1f588-b91a-70cd-bb85-3c9a1feb2ff0
ID          = 01a1f588-b91a-70cd-bb85-3c9a1feb2ff0
Namespace   = default
Job         = firecracker-hello (version 0)
Dispatch    = 01a1f588-b90f-78d3-a6ef-34245d6a0286
Task        = hello
Provider    = homelab-vm
Node        = nomad-client-04
Attempt     = 1
State       = succeeded
Provider ID = 01a1f588-b91a-70cd-bb85-3c9a1feb2ff0
Created     = 2026-11-19 20:39:00Z
Started     = 2026-11-19 20:39:04Z
Ended       = 2026-11-19 20:39:06Z
Duration    = 14ms
Exit Code   = 0
```

`Created` to `Started` is the image pull into a snapshot and the boot, about
four seconds here; `Started` to `Ended` the run and the shutdown.

Every microVM run on the provider, newest first:

```shell
$ vagabond execution list -provider homelab-vm
ID                                    JOB                TASK   PROVIDER    NODE             ATTEMPT  STATE      CREATED
01a1f588-b91a-70cd-bb85-3c9a1feb2ff0  firecracker-hello  hello  homelab-vm  nomad-client-04  1        succeeded  2026-11-19 20:39:00Z
```

The output is kept on the server after the microVM is gone:

```shell
$ vagabond execution logs 01a1f588-b91a-70cd-bb85-3c9a1feb2ff0
Linux 172.16.0.2 6.1.155+ #1 SMP PREEMPT_DYNAMIC Thu Dec 18 15:17:16 UTC 2025 x86_64 GNU/Linux
model name      : Intel(R) Xeon(R) Processor
hello from a microVM
```

## Troubleshooting

Errors at each step, and what fixes them, are in the references:
[Agent troubleshooting](../docs/agent.md#troubleshooting) for the agent's
startup and containerd's snapshotter, and
[Firecracker troubleshooting](../docs/firecracker.md#troubleshooting) for the
daemon, the boot and the microVMs' network.
