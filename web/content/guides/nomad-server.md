---
title: "Deploying the Server on Nomad"
linkTitle: "Server on Nomad"
description: "Run the Vagabond server as a Nomad job with its state in Postgres, its configuration and credentials rendered from Vault, and its health checked through Consul."
weight: 10
---

This guide runs the Vagabond server as a Nomad service job: its state in
Postgres, its configuration and every credential rendered from Vault at
start, and its API registered in Consul with a health check.

## Overview

The server is the `vagabond` image started as `vagabond server -config
<file>`. Nothing in it is Nomad-specific; the job supplies:

| Piece | From |
|---|---|
| The configuration file | A `template` rendered from Vault into the task's `secrets/` |
| The Postgres connection string | Vault KV, inside that template |
| Cloud credentials | Vault KV, rendered to files the configuration names |
| The agent listener's certificate | Vault PKI ([Running Jobs in Containers on Agent Nodes](containers-on-agent-nodes.md)) |
| The address clients and agents use | Consul, as `vagabond.service.consul` |

![Nomad's templates render the server's files into the task's secrets directory: config.hcl with the database credentials from Vault KV, the cloud credentials, and the agent listener's certificate from Vault PKI. The vagabond server reads them at start, stores its state in Postgres through HAProxy on port 5433, and serves the API on 4747 and agents on 4748. Consul registers it as vagabond and checks /v1/health every 10 seconds. Clients reach the API and agents the agent listener at vagabond.service.consul.](../docs/assets/nomad-server.svg)

The examples come from a deployment where the job runs on a Nomad cluster with
Consul and Vault, and Postgres is reached through HAProxy at
`haproxy-postgres.service.consul:5433`.

## Prerequisites

- Nomad with the Docker driver, Consul, and Vault with Nomad's workload
  identity set up
- Postgres or CockroachDB ([Database](../docs/database.md))
- The `vagabond` image in a registry the Nomad clients pull from

## Step 1: Create the Database

A login role owning one database. The server creates its tables itself,
applying its migrations at startup:

```sql
CREATE ROLE vagabond LOGIN PASSWORD '<password>';
CREATE DATABASE vagabond OWNER vagabond;
```

The credentials go in Vault KV, where the job's template reads them:

```shell
$ vault kv put secret/vagabond/postgres db_username=vagabond db_password='<password>'
```

## Step 2: Give the Job Its Vault Access

The task authenticates to Vault with Nomad's workload identity. The policies
its Vault role carries must allow:

| Path | Capabilities | For |
|---|---|---|
| `secret/data/vagabond/*` | `read` | The database credentials and each cloud provider's secrets |
| `pki_vagabond/issue/vagabond-server` | `create`, `update` | The agent listener's certificate |

In the job, the task names the role and asks for an identity Vault accepts:

```hcl
vault {
  role        = "vagabond"
  change_mode = "noop"
}

identity {
  env  = true
  file = true
  aud  = ["vault.io"]
}
```

`change_mode = "noop"` because the templates, not the Vault token, decide
when the task restarts.

## Step 3: Write the Job

The group pins both listeners to static ports and registers the API in Consul
with the server's health route as its check:

```hcl
variable "vagabond_version" {
  type = string
}

job "vagabond" {
  type = "service"

  # No canary: migrations take no lock, so two servers must not start at once
  update {
    max_parallel     = 1
    canary           = 0
    health_check     = "checks"
    min_healthy_time = "30s"
    healthy_deadline = "5m"
    auto_revert      = true
  }

  group "vagabond" {
    count = 1

    network {
      mode = "host"
      port "http" {
        static = 4747
      }
      port "agent" {
        static = 4748
      }
    }

    service {
      name     = "vagabond"
      port     = "http"
      provider = "consul"

      check {
        type     = "http"
        path     = "/v1/health"
        interval = "10s"
        timeout  = "2s"
      }
    }

    task "vagabond" {
      driver = "docker"

      # vault and identity blocks from step 2

      config {
        image        = "registry.example.com/vagabond:${var.vagabond_version}"
        ports        = ["http", "agent"]
        network_mode = "host"
        args         = ["server", "-config", "/secrets/config.hcl"]
      }

      # The configuration and certificate templates of steps 4 and 5

      resources {
        cpu        = 200
        memory     = 128
        memory_max = 256
      }

      # The server drains HTTP requests for up to 10s
      kill_timeout = "15s"
      kill_signal  = "SIGTERM"
    }
  }
}
```

| Setting | Why |
|---|---|
| `canary = 0`, `max_parallel = 1` | A new version's migrations run at its start; the old server stops before the new one starts |
| `health_check = "checks"`, `auto_revert` | A new version that never passes `/v1/health` is rolled back |
| Static ports | Agents and clients dial fixed ports; Consul gives the address |
| `/v1/health` check | Answers 200 while the store answers, and 503 when it does not, so Consul stops routing to a server that cannot take work |
| `kill_timeout = "15s"` | Longer than the server's 10 s drain, so in-flight requests finish |

## Step 4: Render the Configuration

One template renders the whole configuration file from Vault. The `store`
block carries the connection string with the credentials from step 1:

```hcl
template {
  data        = <<EOH
server {
  bind       = "0.0.0.0:4747"
  agent_bind = "0.0.0.0:4748"

  agent_tls {
    cert = "/secrets/agent-listener.pem"
    key  = "/secrets/agent-listener-key.pem"
    ca   = "/secrets/agent-ca.pem"
  }
}

{{ with secret "secret/data/vagabond/postgres" }}
store {
  dsn = "postgres://{{ .Data.data.db_username }}:{{ .Data.data.db_password }}@haproxy-postgres.service.consul:5433/vagabond?sslmode=require&target_session_attrs=read-write"
}
{{ end }}

provider "homelab" {
  type = "containerd"
  tier = 1
}
EOH
  destination = "secrets/config.hcl"
  change_mode = "restart"
  uid         = 10001
  gid         = 10001
  perms       = "0400"
}
```

| Setting | Why |
|---|---|
| `target_session_attrs=read-write` | Connects only to the primary of a replicated Postgres |
| `destination = "secrets/config.hcl"` | The task's secrets directory, which `-config` names |
| `uid = 10001` | The image runs as `appuser`, 10001, which must read the file |
| `change_mode = "restart"` | A changed secret or configuration restarts the server on it |

The cloud providers' blocks, with their credentials rendered to files the same
way, are in [Running CI on Free Cloud Quota](ci-on-free-quota.md); every block
is in [Configuration](../docs/configuration.md).

## Step 5: Render the Agent Listener's Certificate

Three templates from one issue call, each with the same arguments so Vault
issues once:

```hcl
template {
  data        = <<EOH
{{ with secret "pki_vagabond/issue/vagabond-server" "common_name=vagabond.service.consul" "ttl=2160h" }}{{ .Data.certificate }}{{ end }}
EOH
  destination = "secrets/agent-listener.pem"
  change_mode = "restart"
  uid         = 10001
  gid         = 10001
  perms       = "0400"
}
```

and the same with `.Data.private_key` into `agent-listener-key.pem` and
`.Data.issuing_ca` into `agent-ca.pem`. The listener's common name is the name
agents dial, `vagabond.service.consul`.

## Step 6: Run It

```shell
$ nomad job run -var vagabond_version=v0.1.34 vagabond.nomad.hcl
```

```shell
$ nomad job status vagabond
ID            = vagabond
Name          = vagabond
Submit Date   = 2026-11-19T15:22:16-08:00
Type          = service
Priority      = 50
Datacenters   = munchbox
Namespace     = default
Node Pool     = default
Status        = running
Periodic      = false
Parameterized = false

Summary
Task Group  Queued  Starting  Running  Failed  Complete  Lost  Unknown
vagabond    0       0         1        1       29        0     0

Latest Deployment
ID          = 2f6039e2
Status      = successful
Description = Deployment completed successfully

Deployed
Task Group  Auto Revert  Desired  Placed  Healthy  Unhealthy  Progress Deadline
vagabond    true         1        1       1        0          2026-11-19T15:32:56-08:00

Allocations
ID        Node ID   Task Group  Version  Desired  Status   Created    Modified
475fb925  e8bd2f3b  vagabond    29       run      running  3h42m ago  3h42m ago
```

## Step 7: Verify

The server's log, from `nomad alloc logs -stderr <alloc>`:

```text
The API on 0.0.0.0:4747 is reachable beyond this machine and accepts any client. Name a ca in the server block's tls block so clients must present a certificate.
time=2026-11-19T23:22:19.703Z level=INFO msg="serving agents" component=nodes listen_addr=[::]:4748
time=2026-11-19T23:22:19.710Z level=INFO msg=serving component=server listen_addr=[::]:4747 tls=false
time=2026-11-19T23:22:31.716Z level=INFO msg="node registered" component=nodes node=nomad-client-04 pool=homelab remote_addr=192.168.68.73:52706 cpu=2010 memory=4608 held=0
```

- The first line is the server's warning that its API has no authentication.
  This deployment keeps the API on the LAN, with no route from outside;
  [Network exposure](../docs/deployment.md#network-exposure) shows the `tls`
  block that requires client certificates.
- `serving agents` and `serving` are the two listeners.
- `node registered` is an agent joining its pool over the agent listener.

The health route, through Consul's name, answers with the store reachable:

```shell
$ curl -s http://vagabond.service.consul:4747/v1/health
{"Store":"ok","UsageRefreshed":"2026-11-19T23:38:34.716051265Z","Running":0}
```

`UsageRefreshed` is when the server last read quota usage from the store, and
`Running` the dispatches it holds now. With `VAGABOND_ADDR` set to the same
address, every CLI command reaches the server:

```shell
$ export VAGABOND_ADDR=http://vagabond.service.consul:4747
$ vagabond provider status
PROVIDER         TIER  STATUS   QUOTA LEFT
gcp-cloud-run    0     healthy  cpu 92%, memory 91%
ibm-code-engine  0     healthy  cpu 91%, memory 91%
homelab          1     healthy  no quota
homelab-vm       1     healthy  no quota

Usage as of 2026-11-19 23:38:34Z (1s ago).
```

Startup errors from the store, the configuration and the listeners are in
[Database](../docs/database.md) and [Deployment](../docs/deployment.md).
