---
title: "Running Jobs in Containers on Agent Nodes"
linkTitle: "Containers on Agent Nodes"
description: "Give the server an agent listener with mutual TLS from its own CA, run the agent on a node as a Nomad job with a delegated cgroup, and route jobs to the pool through a containerd provider."
weight: 20
---

This guide adds a machine of your own as compute: an agent on the node dials
the server, joins a pool, and runs the pool's workloads in containers on the
node's containerd. The [containerd reference](../docs/containerd.md) covers
how each task becomes a container, and [Agent](../docs/agent.md) the agent
and its pools.

## Overview

The agent is the same `vagabond` binary, started as `vagabond agent`. It
dials the server's agent listener over mutual TLS, reports the node's capacity
and executors, and runs what the server sends it. The server reaches the pool
through a `containerd` provider, which places each task on one of the pool's
nodes by the room each has left.

| Piece | Where | Does |
|---|---|---|
| Agent listener | The server, `agent_bind` | Accepts agents, verifying each certificate against `agent_tls.ca` |
| Agent | The node | Runs workloads in containers on the node's containerd, under a cgroup of its own |
| `containerd` provider | The server's configuration | Routes tasks to the pool's agents |

Certificates come from a CA that signs nothing else, since the server trusts
every certificate its CA signs: the certificate's common name is the node's
name, so whoever can issue one can join a pool.

![The agent dials one mutual TLS connection to the server, multiplexed with yamux, and presents a certificate whose common name is its node name. The agent calls the server's Node service to register under that name and its executors; the server calls the agent's AgentExecutions service to run workloads. The agent starts each workload on the node's containerd, or through vagabond-firecracker in a microVM, inside the agent's cgroup.](../docs/assets/agent-connection.svg)

The examples come from a node named `nomad-client-04` in pool `homelab`, with
the agent as a Nomad job, the server's certificates from Vault PKI, and both
reached through Consul at `vagabond.service.consul`.

## Prerequisites

- A Vagabond server ([Deployment](../docs/deployment.md))
- A Linux node on cgroup v2 running containerd, such as Docker's
- A CA for the agent listener and its agents; Vault PKI here
- The `vagabond` image, or the binary on the node

## Step 1: Issue the Certificates

A PKI mount of its own, `pki_vagabond`, with one role for the server's agent
listener and one for agents. In Vault:

```shell
$ vault write pki_vagabond/roles/vagabond-server \
    allowed_domains=vagabond.service.consul allow_bare_domains=true allow_subdomains=false \
    allow_localhost=false allow_ip_sans=false server_flag=true client_flag=false \
    require_cn=true key_bits=2048 ttl=2160h max_ttl=2160h

$ vault write pki_vagabond/roles/vagabond-agent \
    allowed_domains=nomad-client-04 allow_bare_domains=true allow_subdomains=false \
    allow_localhost=false allow_ip_sans=false server_flag=false client_flag=true \
    require_cn=true key_bits=2048 ttl=2160h max_ttl=2160h
```

| Setting | Why |
|---|---|
| `allowed_domains` on `vagabond-agent` | The nodes allowed to join, by name. A node joins under its certificate's common name, so the role issues only the names listed |
| `server_flag`, `client_flag` | The listener's certificate serves, and an agent's only authenticates as a client |
| `ttl=2160h` | 90 days. Each side reads its certificate at startup, so a renewal restarts it |

## Step 2: Open the Agent Listener

In the server's configuration, the address agents dial and its certificate:

```hcl
server {
  bind       = "0.0.0.0:4747"
  agent_bind = "0.0.0.0:4748"

  agent_tls {
    cert = "/secrets/agent-listener.pem"
    key  = "/secrets/agent-listener-key.pem"
    ca   = "/secrets/agent-ca.pem"
  }
}
```

`agent_bind` is loopback-only by default, so agents on other machines need it
set. `ca` is the CA agents' certificates must chain to; with Vault, the
`issuing_ca` that comes back with the listener's certificate.

On Nomad, the server's task renders the three files from the
`vagabond-server` role:

```hcl
template {
  data        = <<EOH
{{ with secret "pki_vagabond/issue/vagabond-server" "common_name=vagabond.service.consul" "ttl=2160h" }}{{ .Data.certificate }}{{ end }}
EOH
  destination = "secrets/agent-listener.pem"
  change_mode = "restart"
  perms       = "0400"
}
```

and the same with `.Data.private_key` into `agent-listener-key.pem` and
`.Data.issuing_ca` into `agent-ca.pem`.

## Step 3: Give Workloads a cgroup

Workloads run under a cgroup outside the agent's own, so stopping or
redeploying the agent leaves them running, and the kernel holds their total to
that cgroup's limits:

![Under /sys/fs/cgroup, the agent runs in its Nomad task's cgroup, whose cpu.max from cpu_hard_limit and memory limit are the node's capacity: 2010 millicores and 4608 MiB. Workloads run under /vagabond instead, made by the prestart task with cpu.max 200000 100000, memory.max 4608 MiB, and cpu and memory handed to its children. runc creates one cgroup per workload under /vagabond, named by its execution ID.](../docs/assets/agent-cgroups.svg)

A prestart task makes it before the agent starts, with `cpu` and `memory`
handed to its children:

```hcl
task "cgroup" {
  driver = "docker"

  lifecycle {
    hook    = "prestart"
    sidecar = false
  }

  config {
    image      = "busybox:1.38.0"
    privileged = true
    cgroupns   = "host"
    command    = "sh"
    args = ["-ec", <<-EOT
      cg=/sys/fs/cgroup/vagabond
      mkdir -p "$cg"
      echo "+cpu +memory" > "$cg/cgroup.subtree_control"
      echo "200000 100000" > "$cg/cpu.max"
      echo $((4608 * 1024 * 1024)) > "$cg/memory.max"
    EOT
    ]
  }

  resources {
    cpu    = 50
    memory = 32
  }
}
```

`cpu.max` of `200000 100000` holds every workload together to two cores, and
`memory.max` to 4608 MiB. The name has no `.slice` suffix, so systemd leaves
it alone. Running it again with workloads inside changes nothing but the
limits.

## Step 4: Run the Agent

The agent's task, beside the prestart task in the same group:

```hcl
task "agent" {
  driver = "docker"
  user   = "root"

  vault {
    role        = "vagabond-agent"
    change_mode = "noop"
  }

  config {
    image          = "registry.example.com/vagabond:v0.1.34"
    network_mode   = "host"
    pid_mode       = "host"
    privileged     = true
    cgroupns       = "host"
    cpu_hard_limit = true

    args = [
      "agent",
      "-server", "vagabond.service.consul:4748",
      "-pool", "homelab",
      "-tls-ca", "/secrets/ca.pem",
      "-tls-cert", "/secrets/agent.pem",
      "-tls-key", "/secrets/agent-key.pem",
      "-cgroup-parent", "/vagabond",
    ]

    volumes = [
      "/run/containerd/containerd.sock:/run/containerd/containerd.sock",
      "/var/lib/vagabond:/var/lib/vagabond",
      "/var/lib/containerd:/var/lib/containerd:ro",
    ]
  }

  template {
    data        = <<EOH
{{ with secret "pki_vagabond/issue/vagabond-agent" (printf "common_name=%s" (env "node.unique.name")) "ttl=2160h" }}{{ .Data.certificate }}{{ end }}
EOH
    destination = "secrets/agent.pem"
    change_mode = "restart"
    perms       = "0400"
  }

  # The same with .Data.private_key into agent-key.pem and .Data.issuing_ca into ca.pem

  resources {
    cpu    = 5000
    memory = 4608
  }
}
```

| Setting | Why |
|---|---|
| `user = "root"` | containerd's socket is root's |
| `network_mode`, `pid_mode = "host"` | Each workload's link to the node, and the firewall rules that stop it reaching your network, belong on the host. The agent reaches each workload's network through its process on the host |
| `privileged`, `cgroupns = "host"` | The agent reads its real cgroup and creates workloads' cgroups under `/vagabond` |
| `cpu_hard_limit` | Nomad writes the task's CPU as a `cpu.max`, which the agent reads as capacity |
| `-cgroup-parent /vagabond` | Workloads go under the cgroup from step 3 rather than the agent's own |
| `common_name=<node name>` | The node joins under its Nomad node name, which the role from step 1 must allow |
| `/var/lib/vagabond` at the same path | containerd on the host writes each workload's output where the agent names it, and the agent reads it back from there |
| `/var/lib/containerd` read-only | The agent reads each image's `/etc/group` from containerd's snapshots to resolve its user |
| `resources` | The node's capacity: the agent reports the smaller of the task's limits and the host's |

The agent logs the node it found and its connection to the server:

```text
time=2026-11-19T23:23:50.233Z level=INFO msg="node read" component=agent node=nomad-client-04 pool=homelab cpu=2010 memory=4608 cgroup=/vagabond
time=2026-11-19T23:23:50.247Z level=INFO msg=connected component=agent server_addr=vagabond.service.consul:4748 node=nomad-client-04 pool=homelab
```

`cpu=2010` is the 5000 MHz Nomad reserved, converted to millicores on this
node's CPU: room for one workload of `cpu = 2000`. `memory=4608` is the task's
memory.

## Step 5: Add a containerd Provider

In the server's configuration:

```hcl
# Tier 1: the clouds' free quota goes first, and the homelab takes what it can't.
provider "homelab" {
  type = "containerd"
  tier = 1
}
```

The provider's label is its pool, so `homelab` draws on the agents started
with `-pool homelab`.

## Step 6: Verify

The node is connected, with its capacity and what it has running:

```shell
$ vagabond node status
NAME             POOL     ADDRESS              ARCH   EXECUTORS               CPU USED  MEMORY USED  WORKLOADS  CONNECTED
nomad-client-04  homelab  192.168.68.73:52788  amd64  containerd,firecracker  0/2010m   0/4608MiB    0          2026-11-19 23:23:50Z
```

This node also runs Firecracker microVMs, so its executors list
`firecracker`; an agent as configured above lists `containerd` alone
([Running Jobs in Firecracker MicroVMs](firecracker-microvms.md)).

The provider is healthy:

```shell
$ vagabond provider status homelab
Name   = homelab
Tier   = 1
Status = healthy

No quota pools.

Usage as of 2026-11-20 02:57:49Z (6s ago).
```

A plan lists each of the pool's nodes as its own candidate:

```shell
$ vagabond job plan -meta repo=afreidah/vagabond -meta commit=5fbb16230b8b \
    -meta targets="vet lint fmt-check govulncheck" static-analysis
static-analysis.make (container)
gcp-cloud-run            admitted  tier 0           score 91   observed 2026-11-20 00:53:19Z
ibm-code-engine          admitted  tier 0           score 91   observed 2026-11-20 00:53:19Z
homelab/nomad-client-04  admitted  tier 1           score 100  observed 2026-11-20 00:53:20Z
homelab-vm               rejected  not-allowlisted  closest node nomad-client-04: The job routes only to gcp-cloud-run, ibm-code-engine, homelab.
Tiers: strict
Selected: gcp-cloud-run
Estimated cost: free
```

`homelab/nomad-client-04` is admitted: the node has room for the task's 2000
millicores and 3814 MiB. It is not selected because the job's tier 0 clouds
come first ([Running CI on Free Cloud Quota](ci-on-free-quota.md)).

Startup errors and their causes are in
[Agent troubleshooting](../docs/agent.md#troubleshooting).
