---
title: "vagabond-firecracker"
seoTitle: "vagabond-firecracker: Workloads in Firecracker MicroVMs"
description: "The vagabond-firecracker daemon and its guest init: requirements, flags, deployment, kernel and initramfs, microVM sizing, networking, restarts, the executor protocol, logging, troubleshooting and testing."
weight: 520
---

`vagabond-firecracker` runs beside the agent on a KVM node and runs each
workload in a Firecracker microVM of its own, with its own kernel, where a
container shares the host's. It serves the executor protocol on a Unix socket:
the caller hands it a process, its CPU, memory and timeout, a root drive
holding the image's files, and the cgroup to run under. Each microVM boots
into `vagabond-init`, which mounts the root drive, runs the process and serves
its output and exit back over vsock.

MicroVMs belong to their Firecracker processes, not to the daemon: they keep
running when the daemon stops, and the daemon takes them back when it starts.

The agent drives it when started with `-firecracker <socket>`: it pulls each
workload's image through the node's containerd into a devmapper snapshot,
resolves the image's process and user as containerd would, and hands the
daemon the snapshot's block device as the root drive ([Agent](agent.md)).

![The agent pulls each image through containerd into a devmapper snapshot and resolves its process and user, then calls vagabond-firecracker over the executor protocol, ttRPC on a socket only root can open. The daemon gives each workload a jail, a cgroup, and a tap behind NAT, and starts Firecracker through the jailer, its memory limited to what the workload declared plus Firecracker's overhead. The microVM boots into vagabond-init, which mounts the snapshot as its root drive, runs the workload as the image's user, and talks to the daemon over vsock.](assets/firecracker.svg)

Both programs build from one Rust crate, `rust/vagabond-firecracker`:

| Program | Runs | Does |
|---|---|---|
| `vagabond-firecracker` | On the host, as root | Builds each microVM's jail, cgroup and tap device, starts Firecracker through its jailer, follows the workload, serves the executor protocol |
| `vagabond-init` | Inside each microVM, as PID 1 | Mounts the root drive and switches into it, runs the process, serves the guest protocol on vsock |

## Requirements

| Requirement | Checked at | Failure |
|---|---|---|
| Linux on x86_64 with KVM, `/dev/kvm` present, and a host kernel Firecracker supports | first `Submit` | `Internal` `Firecracker exited while booting: ...` with Firecracker's reason |
| Root | startup | `failed error=enabling IPv4 forwarding: Permission denied (os error 13)` |
| `nft` (nftables) | startup | `failed error=nft: No such file or directory (os error 2)` |
| `ip` (iproute2) | first `Submit` | `Internal` `ip: No such file or directory (os error 2)` |
| `iptables`, where Docker's `DOCKER-USER` chain exists | startup | `failed error=iptables -w -I DOCKER-USER ...: <what iptables printed>` |
| cgroup v2 mounted at `/sys/fs/cgroup` | first `Submit` | `Internal` `creating the cgroup /sys/fs/cgroup/...: ...` |
| `cpu` and `memory` in the `cgroup.subtree_control` of every cgroup a `Submit` names | each `Submit` | `Internal` `writing /sys/fs/cgroup/<cgroup>/<id>/memory.max: ...` |
| Firecracker and its jailer from the same release | first `Submit` | `Internal` `/usr/bin/jailer: No such file or directory (os error 2)` |
| A guest kernel with the options in [Kernel](#kernel) | first `Submit` | A file Firecracker cannot load: `Firecracker exited while booting: ...`. A kernel missing an option: [Troubleshooting](#troubleshooting) |
| `--chroot-base` on a filesystem that allows device files (not mounted `nodev`) | first `Submit` | `Internal` `Firecracker exited while booting: ...` |
| `<chroot-base>/firecracker/<execution ID>/root/v.sock` within 107 bytes | each `Submit` | `InvalidArgument` `<path> is too long for a Unix socket; shorten --chroot-base` |
| The root drive a block device | each `Submit` | `InvalidArgument` `root drive <path> is not a block device` |
| The root drive's filesystem type one plain word, such as `ext4` | each `Submit` | `InvalidArgument` `root drive filesystem "<type>" is not one word, such as ext4` |

The default `--chroot-base` of `/srv/jailer` leaves room for execution IDs of
up to 71 characters, past the 64 the jailer accepts. A tmpfs `/tmp` is
usually mounted `nodev`, so jails cannot live there.

## Flags

```bash
vagabond-firecracker --kernel /usr/share/vagabond-firecracker/vmlinux \
  --initramfs /usr/share/vagabond-firecracker/initramfs.cpio.gz \
  --uid 990 --gid 990 --uplink eth0
```

| Flag | Default | Description |
|---|---|---|
| `--socket <path>` | `/run/vagabond-firecracker/executor.sock` | Unix socket the executor protocol is served on. Mode `0600`, in a directory created mode `0700`. |
| `--state-dir <path>` | `/var/lib/vagabond-firecracker` | Each execution's record, output and console, one directory per execution. Created if missing. |
| `--kernel <path>` | required | Guest kernel every microVM boots. Hard-linked into each jail, or copied when the jail is on another filesystem. |
| `--initramfs <path>` | required | Initramfs holding `vagabond-init` as `/init`. Linked or copied like the kernel. |
| `--firecracker <path>` | `/usr/bin/firecracker` | Firecracker binary the jailer runs. Its file name is part of every jail's path. |
| `--jailer <path>` | `/usr/bin/jailer` | Jailer each microVM starts through. |
| `--uid <uid>` | required | User Firecracker runs as inside its jail. Give it to nothing else on the node. |
| `--gid <gid>` | required | Group Firecracker runs as inside its jail. |
| `--chroot-base <path>` | `/srv/jailer` | Directory the jailer builds each microVM's jail under. See [Requirements](#requirements) for the filesystem and path length it needs. |
| `--vmm-overhead <MiB>` | `32` | Memory added to each microVM's declared memory in its cgroup's `memory.max`, for Firecracker itself. |
| `--pool <cidr>` | `172.16.0.0/16` | IPv4 range each microVM's `/30` is taken from. A `/16` holds 16384 microVMs. Must not overlap a network the node routes to. |
| `--uplink <interface>` | required | Host interface microVM traffic leaves through, behind NAT. |
| `--dns <address>` | `1.1.1.1` | Name server each microVM resolves through. |
| `--log-level <level>` | `info` | `error`, `warn`, `info`, `debug` or `trace`. Logs are text to stderr. |

### Startup sequence

1. Parse flags and set the log level. A flag error prints usage and exits 2.
2. Turn on IPv4 forwarding, replace the `vagabond_firecracker` nftables table,
   and add the Docker rules where Docker's chain exists
   ([Networking](#networking)).
3. Create `--state-dir` and read every record in it. A record that cannot be
   read stops the daemon, rather than lose the microVM it describes.
4. Take back the microVMs a previous daemon left ([Restarts](#restarts)).
5. Create the socket's directory, remove a socket a crash left behind, bind,
   and set the socket to mode `0600`.
6. Log `serving`, then serve until `SIGTERM` or `SIGINT`. Either logs
   `stopped` and exits 0; microVMs keep running.

```text
2026-11-18T03:42:08.763234Z  INFO recovered execution=runs-across
2026-11-18T03:42:08.763408Z  INFO serving socket=/run/vagabond-firecracker/executor.sock
```

Steps 2 to 5 log `failed error=...` and exit 1 on failure.

## Deployment

The `vagabond-firecracker` image, amd64 only, holds the daemon,
`vagabond-init` packed as `/usr/share/vagabond-firecracker/initramfs.cpio.gz`,
Firecracker and its jailer at `/usr/bin`, where the flags look by default, and
Firecracker's test kernel at `/usr/share/vagabond-firecracker/vmlinux`: the
same pinned files the tests boot. Its entrypoint passes `--kernel` and
`--initramfs`; the deployment gives the rest.

```bash
docker run -d --name vagabond-firecracker --restart unless-stopped \
  --privileged --network host --pid host --cgroupns host \
  -v /dev:/dev \
  -v /run/vagabond-firecracker:/run/vagabond-firecracker \
  -v /var/lib/vagabond-firecracker:/var/lib/vagabond-firecracker \
  -v /srv/jailer:/srv/jailer \
  <registry>/vagabond-firecracker:<version> --uid 990 --gid 990 --uplink eth0
```

| Setting | Why |
|---|---|
| `--privileged` | `/dev/kvm`, a writable `/sys/fs/cgroup`, device nodes in the jails, and the network changes in [Networking](#networking) |
| `--network host` | The taps, the nftables table and the Docker rules belong on the host, where microVM traffic flows |
| `--pid host` | Each record keeps Firecracker's host pid, so a restarted daemon finds it, and the microVMs are not ended with the container |
| `--cgroupns host` | Each microVM's cgroup is made under the path the agent names, a host path |
| `/dev` mounted | Each root drive is a devmapper device made after the container started, which a privileged container's own `/dev` does not show |
| `/run/vagabond-firecracker` mounted | The socket, which the agent names with `-firecracker` |
| `--state-dir` and `--chroot-base` mounted | Records and output outlive the container, and the jails, which hold device nodes, sit on a filesystem that allows them |

Start the agent with `-firecracker /run/vagabond-firecracker/executor.sock`
once the daemon serves: it refuses to start when the daemon does not answer
([Agent](agent.md#flags)). Under systemd, order the agent's unit after the
daemon's, with `Requires=`.

## Kernel

Firecracker boots an uncompressed `vmlinux` or a `bzImage` on x86_64, and
recommends `vmlinux`. The guest kernel needs these built in, not as modules,
since the initramfs holds none:

| Option | For |
|---|---|
| `CONFIG_BLK_DEV_INITRD`, `CONFIG_RD_GZIP` | The gzip initramfs holding `vagabond-init` |
| `CONFIG_VIRTIO_MMIO`, `CONFIG_VIRTIO_BLK`, `CONFIG_VIRTIO_NET` | Firecracker's root drive and network device |
| `CONFIG_VSOCKETS`, `CONFIG_VIRTIO_VSOCKETS` | The guest protocol |
| `CONFIG_IP_PNP` | The static address and name server given in `ip=` |
| `CONFIG_DEVTMPFS`, `CONFIG_TMPFS`, `CONFIG_PROC_FS`, `CONFIG_SYSFS` | The filesystems `vagabond-init` mounts |
| `CONFIG_EXT4_FS`, or the filesystem the agent's devmapper snapshotter makes (its `fs_type`) | The root drive |
| `CONFIG_SERIAL_8250`, `CONFIG_SERIAL_8250_CONSOLE` | The guest's console in `console.log` |

Firecracker's own test kernels have all of them. The tests boot
[`vmlinux-6.1.155`](https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155),
built from Firecracker's
[`microvm-kernel-ci-x86_64-6.1.config`](https://github.com/firecracker-microvm/firecracker/blob/main/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config).

The daemon boots every microVM with these arguments:

```text
console=ttyS0 reboot=k panic=1 root=/dev/vda rootfstype=ext4 ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off:1.1.1.1
```

| Argument | Effect |
|---|---|
| `console=ttyS0` | The guest's console on the serial port, which Firecracker writes to `console.log` |
| `reboot=k` | A guest reboot ends Firecracker, which is how Shutdown, and `vagabond-init` exiting, end the microVM |
| `panic=1` | A kernel panic reboots after 1 second, ending Firecracker the same way |
| `root=/dev/vda`, `rootfstype=` | The root drive and the filesystem the `Submit` names for it, read by `vagabond-init`; the kernel runs the initramfs's `/init` and mounts nothing itself |
| `ip=<guest>::<host>:<netmask>::eth0:off:<dns>` | Kernel IP autoconfiguration: the microVM's address, its gateway, no DHCP, and `--dns` as its name server |

## Initramfs

The initramfs is `vagabond-init` alone, as `/init`, built static against musl
so it needs no libraries, and packed with `cpio -R 0:0`, which makes `/init`
root's without building as root. The image builds it in its own build stage;
`make rust/vagabond-firecracker/target/kvm/initramfs.cpio.gz` builds the same
for the tests, with the compiler and musl target `rust-toolchain.toml` pins.
The kernel's own built-in initramfs supplies `/dev/console` underneath.

## Inside the microVM

`vagabond-init` runs as PID 1 and, in order:

1. Mounts `proc` on `/proc` and `sysfs` on `/sys` (`nosuid,nodev,noexec`),
   `devtmpfs` on `/dev` (`nosuid`) and `tmpfs` on `/dev/shm`
   (`nosuid,nodev,noexec`).
2. Mounts the `root=` device at `/mnt/root`, read-write, as `rootfstype=`.
3. Moves `/proc`, `/sys` and `/dev` into it and switches root, so the image's
   files are `/`.
4. Writes `/etc/resolv.conf` from `/proc/net/pnp`, where the kernel publishes
   the `ip=` name server, replacing the image's own, a symlink included.
5. Serves the guest protocol on vsock port 1024 and collects every process
   that exits, as PID 1 must.

A failure in any step ends `vagabond-init`, which panics the kernel and ends
the microVM. It first writes one line to the console naming the step, and
waits for the console to send it; the `Submit` that booted the microVM fails
with that line:

```text
Firecracker exited while booting: vagabond-init: mounting /dev/vda at /mnt/root as ext4: Invalid argument (os error 22)
```

The workload is the request's `process`:

| Field | Effect |
|---|---|
| `args` | The first is the program, the rest its arguments. A program without a `/` is looked up in the `PATH` that `env` sets, so give one or an absolute path. |
| `env` | The whole environment; nothing is inherited from `vagabond-init` |
| `working_dir` | Where it starts; `/` when empty. One the image lacks is made, parents included, owned by root with mode `0755`, so a non-root user can enter it but not write to it. |
| `uid`, `gid`, `additional_gids` | The user and groups it runs as: the image's `USER` as the agent resolves it, root when the image names none. The groups are set first, then the group, then the user. |

Standard input is `/dev/null`; standard output and error share one pipe, so the output
is both, in the order written. Its exit is its code, or 128 plus the signal
that killed it: 143 for `SIGTERM`, 137 for `SIGKILL`.

## MicroVMs

### Sizing

| Request | MicroVM | cgroup |
|---|---|---|
| `resources.cpu`, millicores | `vcpu_count`: whole cores rounded up, then up to an even count above 1, the counts Firecracker accepts | `cpu.max`: `cpu` x 100 per 100000 microsecond period, or `max 100000` for 0 |
| `resources.memory`, MiB | `mem_size_mib`: the declared memory | `memory.max`: declared memory plus `--vmm-overhead` |

| `cpu` | `memory` | vCPUs | `cpu.max` | `memory.max`, default overhead |
|---|---|---|---|---|
| 500 | 256 | 1 | `50000 100000` | 301989888 (288 MiB) |
| 1000 | 128 | 1 | `100000 100000` | 167772160 (160 MiB) |
| 1500 | 512 | 2 | `150000 100000` | 570425344 (544 MiB) |
| 2500 | 1024 | 4 | `250000 100000` | 1107296256 (1056 MiB) |

A guest sees more vCPUs than it declared whenever its share is not a whole,
even count; `cpu.max` still holds it to the declared share.

### cgroup

Each microVM's cgroup is `<cgroup>/<execution ID>`, under the `cgroup` the
request names, relative to `/sys/fs/cgroup`. The daemon creates it with the
limits above, the jailer moves Firecracker into it, and Release removes it.
Everything Firecracker uses, guest memory included, is charged there and so
to the cgroup the request named.

### Jail

```text
<chroot-base>/<firecracker file name>/<execution ID>/root/
  vmlinux       hard link to --kernel, or a copy across filesystems
  initramfs     hard link to --initramfs, or a copy
  rootfs        block device node with the root drive's numbers, mode 0600, owned --uid:--gid
  config.json   the configuration Firecracker boots from
  v.sock        the guest's vsock, created by Firecracker
  dev/kvm, dev/net/tun   created by the jailer
```

The jailer creates the device nodes, moves into the cgroup, chroots into
`root`, drops to `--uid` and `--gid`, and becomes Firecracker, which boots
from `config.json` without API calls:

```json
{
  "boot-source": {
    "kernel_image_path": "/vmlinux",
    "initrd_path": "/initramfs",
    "boot_args": "console=ttyS0 reboot=k panic=1 root=/dev/vda rootfstype=ext4 ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off:1.1.1.1"
  },
  "drives": [{ "drive_id": "rootfs", "path_on_host": "/rootfs", "is_root_device": false, "is_read_only": false }],
  "machine-config": { "vcpu_count": 1, "mem_size_mib": 512 },
  "network-interfaces": [{ "iface_id": "eth0", "host_dev_name": "vfc0" }],
  "vsock": { "guest_cid": 3, "uds_path": "/v.sock" }
}
```

The root drive is attached read-write: what the workload writes lands on the
drive the caller handed over.

## Networking

IPv4 only. Each microVM gets a slot: the lowest one no execution holds,
counting from 0.

| Slot `n` | Example, `n` = 1 |
|---|---|
| The `n`th `/30` of `--pool` | `172.16.0.4/30` |
| Tap device `vfc<n>`, owned by `--uid` and `--gid` so the jailed Firecracker can open it | `vfc1` |
| The host's address on the tap, the `/30`'s first | `172.16.0.5` |
| The microVM's address, its gateway the host's | `172.16.0.6` |
| vsock CID `3 + n`; 0 to 2 are reserved | `4` |

An execution holds its slot until it is released, ended or not. With every
slot held, `Submit` is refused with `ResourceExhausted`
`the address pool is used up`.

### NAT and the firewall

At every start the daemon sets `net.ipv4.ip_forward` to 1 and replaces its
own nftables table whole, in one transaction, so a restart never doubles a
rule. With the default `--pool` and `--uplink eth0`:

```text
table ip vagabond_firecracker {
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        ip saddr 172.16.0.0/16 oifname "eth0" masquerade
    }
    chain forward {
        type filter hook forward priority filter; policy accept;
        iifname "vfc*" oifname "eth0" accept
        iifname "eth0" oifname "vfc*" ct state established,related accept
        iifname "vfc*" drop
        oifname "vfc*" drop
    }
    chain input {
        type filter hook input priority filter; policy accept;
        iifname "vfc*" ct state established,related accept
        iifname "vfc*" drop
    }
}
```

| Rule | Effect |
|---|---|
| `masquerade` | MicroVM traffic leaving the uplink takes the uplink's address |
| `iifname "vfc*" oifname "eth0" accept` | MicroVMs reach out through the uplink |
| `ct state established,related accept`, forward | Replies come back in |
| `iifname "vfc*" drop`, forward | A microVM reaches no other microVM and no other interface |
| `oifname "vfc*" drop`, forward | Nothing routed through the host reaches a microVM |
| `iifname "vfc*" drop`, input | A microVM reaches none of the host's addresses, except to answer what the host opened |

Nothing else on the host is touched, and neither the table nor forwarding is
undone at exit, since the microVMs outlive the daemon.

| From | To | Result |
|---|---|---|
| A microVM | Anything the uplink reaches: the internet, and the uplink's own network | Reaches it, behind the uplink's address |
| A microVM | Another microVM | Dropped |
| A microVM | The host, on any address | Dropped |
| Another machine, routed through the host | A microVM | Dropped |
| A process on the host | A microVM | Reaches it: the host's own traffic is not forwarded, and the table has no output chain |

A microVM reaches the uplink's local network as it reaches the internet. Keep
anything it must not reach off that network, or filter it upstream.

### Docker

Where Docker manages the firewall with its iptables backend, it sets the
forward policy to drop, and a drop in any table outweighs an accept in this
one. Where its `DOCKER-USER` chain exists, the chain Docker leaves for others'
rules, the daemon inserts the two rules microVM traffic needs, each only when
it is missing:

```bash
iptables -w -I DOCKER-USER -i vfc+ -o eth0 -j ACCEPT
iptables -w -I DOCKER-USER -i eth0 -o vfc+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

Everything else forwarded stays dropped. Docker's nftables backend sets no
drop policy, so microVM traffic needs nothing from it. A host without the
chain gets no rules.

### DNS

The microVM resolves through `--dns`. The kernel takes it from `ip=` and
publishes it in `/proc/net/pnp`, and `vagabond-init` writes that to the
image's `/etc/resolv.conf` before the workload starts.

## Workloads

### Submit

`Submit` checks the request, takes a slot, saves the execution's record, then
builds the jail, cgroup and tap, starts the jailer, connects to the guest and
starts the workload. It returns `running` once the workload has started,
waiting up to 30 seconds for the guest to answer.

The execution ID is the idempotency key: a `Submit` for an execution already
held does nothing and returns its state as it stands. A `Submit` that fails
after its record was saved removes what it made and forgets the execution, so
the same ID can be submitted again. When that removal fails too, the record
stays, listed by `Held`, for `Release` to finish.

A Firecracker that exits while booting fails the `Submit` at once, with
`vagabond-init`'s line when it wrote one, or else the console's last three
lines, where the jailer and Firecracker say why.

### States

| State | When |
|---|---|
| `accepted` | Saved, not started; seen only while `Submit` runs, or after a crash in it |
| `running` | Started and not yet ended |
| `succeeded` | Exited 0 |
| `failed` | Exited non-zero, or ended without an exit: the microVM died, or the daemon could not take it back after a restart |
| `cancelled` | `Cancel` was called, whatever the exit |

### Output

Standard output and error are copied from the guest to
`<state-dir>/<execution ID>/output.log` as they are written, each chunk
written before the next is read. `ReadLogs` returns what the log holds from
an offset, up to 64 KiB a call, and `ended` once the workload has ended and
the offset has reached the log's end. An execution not started yet reads as
empty. The output is kept whole until `Release`.

### Timeouts

A workload's timeout runs from its saved start, so a restarted daemon keeps
the same deadline; zero means none. At the deadline the workload is sent
`SIGTERM`, then `SIGKILL` 10 seconds later if it still runs. It ends `failed`,
with exit 143 or 137.

### Cancel

`Cancel` marks the execution cancelled, then stops its workload as a timeout
does. An execution that has already ended is marked all the same. One
cancelled while its microVM boots, before the workload can be signalled, is
stopped as soon as the workload starts.

### End

Once the workload exits, its end and exit code are saved, and the guest is
told to shut down: it flushes the root drive and reboots, which ends
Firecracker. The tap device, jail and cgroup stay until `Release`.

### Release

`Release` kills a Firecracker that still runs and waits up to 10 seconds for
it to exit, then removes the tap device, the jail, the cgroup and
`<state-dir>/<execution ID>`, record and output included. Each step passes
over what is already gone, so `Release` can be repeated, and a `Release` for
an execution the daemon does not hold succeeds.

## Restarts

```text
<state-dir>/<execution ID>/
  record.json   the declared resources and timeout, the slot, the cgroup,
                Firecracker's pid and start time, the start and end, the exit
                code, and whether it was cancelled
  output.log    the workload's output
  console.log   the jailer's errors and Firecracker's output, the guest's console included
```

Each change to a record is written to a temporary file and renamed over the
last, so a crash leaves the old record whole, and a record is saved before
anything it describes is made.

At start, the daemon takes back every execution that has not ended:

- One that started, whose Firecracker still runs and whose guest answers
  within 5 seconds, is supervised again: its output is followed from the
  guest and appended where the log ends, and its timeout runs to the saved
  deadline. It logs `recovered`.
- Any other has its microVM removed and is ended without an exit, so it
  reports `failed` and `Release` clears it. It logs `lost the microVM`.

An execution that ended, but whose Firecracker still runs because the daemon
stopped between saving the end and shutting the guest down, has its microVM
removed, so it holds no more of the node. It logs `removing an ended microVM`.
Finished executions report as saved. Firecracker is known by its pid and its
start time from `/proc/<pid>/stat` together, since a pid is reused once its
process is gone but never with the same start time; the daemon never signals
a process that merely took over a Firecracker's pid.

## Protocol

### Executor

Package `vagabond.agent.v1`, defined in `internal/agentrpc/executor.proto`
with the agent protocol's messages wherever they mean the same thing. ttRPC on
the Unix socket at `--socket`. The crate's `build.rs` generates the Rust code,
and `make generate` the Go client the agent's firecracker executor uses.

| RPC | Request | Response |
|---|---|---|
| `Submit` | `execution_id`, `process` (`args`, `env`, `working_dir`, `uid`, `gid`, `additional_gids`), `resources` (`cpu` millicores, `memory` MiB), `timeout`, `root_drive`, `root_fstype`, `cgroup` | `state`, `provider_id`: the execution ID |
| `ReadLogs` | `execution_id`, `offset` | `data`, `ended` |
| `Held` | empty | `held[]`: every execution held, ended or not, with its declared `resources` plus `--vmm-overhead` of memory, and `running` until its Firecracker has exited |
| `Info` | empty | `name`, `firecracker`, and `overhead`, `--vmm-overhead` as memory, which the agent registers and the server adds to each reservation |

`Status`, `Result`, `Cancel` and `Release` take an `execution_id` and answer
as the agent's own do ([RPCs](agent.md#rpcs)).

`Submit` refuses, with `InvalidArgument`, an execution ID that is not 1 to 64
letters, digits and hyphens; no `memory`; a negative `timeout`; no `args`; no
`root_drive` or `cgroup`; a `root_fstype` that is not one plain word; and,
from the steps that follow, a socket path too long and a root drive that is
no block device.

| Code | Returned when |
|---|---|
| `InvalidArgument` | The request cannot be acted on, as above, or `ReadLogs` with a negative `offset` |
| `ResourceExhausted` | `Submit` finds the pool used up |
| `NotFound` | `Status`, `Result`, `Cancel` or `ReadLogs` for an execution the daemon does not hold: `no execution <id>` |
| `FailedPrecondition` | `Result` before the workload has ended: `execution <id> has not finished` |
| `Internal` | Any other failure, a boot included |

### Guest

Package `vagabond.guest.v1`, defined in
`rust/vagabond-firecracker/proto/guest.proto`. ttRPC on vsock port 1024
inside the microVM. The daemon reaches it through Firecracker's vsock socket,
`v.sock` in the jail, by writing `CONNECT 1024` and reading `OK <port>`.

| RPC | Does |
|---|---|
| `Start` | Runs the process, once; a second call starts nothing, so a retry after a lost reply never runs it twice |
| `Read` | Returns the next output written, up to `max` bytes and at most 64 KiB, waiting until there is some; `ended` once all of it is read |
| `Wait` | Returns the exit, waiting for it |
| `Signal` | Sends a signal to the workload while it runs |
| `Shutdown` | Flushes the root drive and reboots, ending Firecracker; it never answers |

`InvalidArgument` for no program or an unknown signal, `FailedPrecondition`
for a call before `Start`, `Internal` for anything else.

## Security

- ttRPC has no authentication, so access is file permissions alone. The
  executor socket is mode `0600`, root's, in a directory of mode `0700`;
  whoever can open it can run microVMs.
- The guest protocol is reached only through `v.sock`, inside the jail,
  created by Firecracker as `--uid`. Give `--uid` and `--gid` to nothing else
  on the node.
- Firecracker runs jailed: chrooted into the jail, as `--uid` and `--gid`,
  in the microVM's cgroup, under Firecracker's default seccomp filters.
- The workload runs as the image's user, root when it names none. Its
  boundary is the microVM and the network rules in [Networking](#networking).
- Output is kept whole, with no cap: a workload can fill the filesystem
  `--state-dir` is on.
- Run one daemon per node. A second removes the first's socket as it starts,
  and both then follow the same microVMs, splitting their output.

## Logging

Text to stderr at `--log-level`, uncoloured, one line per event with
`execution=` where one applies. ttRPC's own messages pass through at their
levels.

| Message | Level | When |
|---|---|---|
| `serving socket=` | info | Serving has begun |
| `stopped` | info | `SIGTERM` or `SIGINT` ended serving |
| `failed error=` | error | Startup failed; exits 1 |
| `recovered` | info | A microVM was taken back at start |
| `lost the microVM` | warn | A microVM could not be taken back and is ended |
| `removing an ended microVM` | info | An ended execution's microVM still ran at start |
| `reconnecting to the guest error=` | warn | Why taking it back failed |
| `started cid= tap=` | info | A workload started |
| `starting the microVM error=` | warn | A `Submit` failed after its record was saved |
| `removing the microVM error=`, `forgetting the execution error=` | warn | Cleaning up after a failure, or a `Release`, failed |
| `cancelling` | info | `Cancel` |
| `stopping a workload cancelled before it was supervised` | info | A `Cancel` came while the microVM booted, or before a restart |
| `timed out` | info | A workload reached its timeout |
| `killing the workload` | info, warn with `error=` | The 10 second grace passed with the workload still running |
| `stopping the workload error=` | warn | Signalling a timed-out workload failed |
| `following the workload error=` | warn | The guest's connection broke while the workload ran: the microVM died |
| `ended exit_code=` | info | A workload ended; no `exit_code` when it never exited |
| `saving the end error=` | warn | Its end could not be saved |
| `released` | info | `Release` finished |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `failed error=enabling IPv4 forwarding: Permission denied (os error 13)` | Not root | Run as root. |
| `failed error=nft: No such file or directory (os error 2)` | nftables not installed | Install it. |
| `failed error=reading <state-dir>/<id>/record.json: ...` | A record that cannot be read | Move the directory out of `--state-dir`, then stop the microVM it described by hand: its Firecracker, tap device `vfc<n>` and cgroup. |
| `InvalidArgument ... is too long for a Unix socket; shorten --chroot-base` | The jail's path leaves no room for the ID | A shorter `--chroot-base`. |
| `InvalidArgument root drive <path> is not a block device` | The caller passed a file | Pass a block device: a loop device or a snapshot. |
| `ResourceExhausted the address pool is used up` | Every slot held, ended executions included | Release what has ended, or widen `--pool`. |
| `Internal /usr/bin/jailer: No such file or directory (os error 2)` | No jailer there | Set `--jailer`. |
| `Internal creating the cgroup /sys/fs/cgroup/<cgroup>/<id>: No such file or directory ...` | The request's cgroup does not exist | Create it, or name an existing one. |
| `Internal writing /sys/fs/cgroup/<cgroup>/<id>/memory.max: ...` | `cpu` and `memory` not delegated to the request's cgroup | Add them to its parent's `cgroup.subtree_control`; under systemd, `Delegate=yes`. |
| `Firecracker exited while booting: ... Cannot load kernel ...` | `--kernel` is not a kernel Firecracker can load | Point it at a `vmlinux`. |
| `Firecracker exited while booting: ...` naming `/dev/kvm` | No KVM, or `--chroot-base` mounted `nodev` | Load `kvm_intel` or `kvm_amd`; move `--chroot-base`. |
| `... as xfs: No such device (os error 19)` | The guest kernel lacks that filesystem | Build it in. |
| `... vagabond-init: reading /proc/net/pnp: No such file or directory (os error 2)` | Guest kernel without `CONFIG_IP_PNP` | Build it in. |
| `... vagabond-init: serving vsock port 1024: ...` | Guest kernel without vsock | Build in `CONFIG_VSOCKETS` and `CONFIG_VIRTIO_VSOCKETS`. |
| `following the workload error=...` and an execution `failed` without an exit | The microVM died. Firecracker killed for passing `memory.max` shows in the kernel log as an OOM kill in its cgroup | Raise `--vmm-overhead`; check `console.log` before `Release`. |
| MicroVMs cannot reach the internet on a host with Docker | `iptables -S DOCKER-USER` lacks the two rules, after a firewall reload or Docker starting after the daemon | Restart the daemon, which adds them again; order it after Docker. |
| MicroVMs cannot resolve names | `--dns` unreachable from the uplink | Set a name server the uplink reaches. |

## Testing

```bash
make rust-test              # unit tests; the microVM tests are skipped
make rust-coverage          # the same, into rust-coverage.lcov, as CI runs it
make firecracker-test       # the microVM tests, booting real microVMs
make firecracker-coverage   # the same, into firecracker-coverage.lcov, as CI runs it
make executor-test          # the agent's executors against a containerd and daemon of their own
make executor-coverage      # the same, into executor-coverage.out, as CI runs it
```

`firecracker-test` fetches Firecracker v1.18.0, its jailer, Firecracker's
test kernel and Alpine 3.20's root filesystem into
`rust/vagabond-firecracker/target/kvm`, each checked against a pinned
SHA-256, and builds the initramfs and an ext4 root drive from Alpine without
root. cargo builds the tests as your user and runs them through `sudo`.

They need root, `/dev/kvm`, and the internet, reached by `example.com`. Each
test starts the real daemon and drives it through the executor protocol, one
test at a time: a command and its exit, cancellation, release, a restart,
memory accounting, the internet, isolation, and boots that fail. While they
run they make, and remove when each test ends:

- the `vagabond-firecracker-test` cgroup, with `cpu` and `memory` delegated
- a loop device per root drive, and jails under `/var/tmp`
- taps `vfc0` and up, the `vagabond_firecracker` table and, under Docker, its
  `DOCKER-USER` rules
- for isolation, the `vfc-test-outside` network namespace, a veth pair on
  `198.18.0.0/30`, and `DOCKER-USER` rules opening Docker's drop to the taps

`firecracker-coverage` keeps `LLVM_PROFILE_FILE` through `sudo`, so the daemon
the tests start writes its coverage too, and hands the profiles written as
root back to your user.

`executor-test` builds the daemon, then `scripts/executor-env.sh` starts it
beside a containerd of its own, with the devmapper snapshotter on a thin pool
over loop devices, apart from anything the host runs. The agent's containerd
and firecracker executor tests run against both as one binary through
`sudo`, and the environment is cleared after, pass or fail;
`make executor-env-down` clears what an interrupted run left. They need root,
`/dev/kvm`, `containerd` and `dmsetup`.

CI's `firecracker` job runs `make firecracker-coverage`, then
`make executor-coverage`, on the runner's KVM, and fails first if `/dev/kvm`
is missing, so a run cannot pass having booted nothing. Its reports join
`rust-coverage.lcov` and the Go profiles in SonarCloud.
