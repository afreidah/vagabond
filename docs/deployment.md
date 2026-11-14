---
title: "Deployment"
seoTitle: "Deploying the Server and Agents"
description: "Building the binary, running vagabond server under systemd, network exposure, agents, sizing, multiple servers, upgrades, health checks and logs."
weight: 420
---

Vagabond ships as one binary, `vagabond`, that runs the server, the agent and
the CLI. The repository has a `Dockerfile` for an image of it but publishes no
image, package or systemd unit; build the binary or the image and run it under
a supervisor.

## Building

Requires the Go version in `go.mod` (1.27.4).

```bash
make build                       # ./vagabond, version from .version (dev if the file is missing)
make build VERSION=v0.4.0        # stamps the version
./vagabond -version
```

`make build` runs `go build -ldflags "-s -w -X .../version.Version=$(VERSION)
-X .../version.Commit=<short commit>" -o vagabond ./cmd/vagabond`. A binary
built with plain `go build` reports `dev` and the VCS revision Go embedded.

Install it where the unit files below expect it:

```bash
sudo install -m 0755 vagabond /usr/local/bin/vagabond
```

Or `make install`, which runs `make build` and installs to `$(BINDIR)` (default
`/usr/local/bin`); `make uninstall` removes it. `sudo make install` builds as
root; `make install BINDIR=$HOME/.local/bin` needs no sudo.

### Container image

```bash
make docker                                   # $(REGISTRY)/vagabond:$(VERSION), local architecture
make push DOCKER_REGISTRY=registry.internal   # linux/amd64 and linux/arm64, tagged $(VERSION) and latest
```

| | |
|---|---|
| Build / run base | `golang:1.27.4-alpine` / `alpine:3.21` with `ca-certificates` |
| User | `appuser`, UID 10001 |
| Entrypoint | `vagabond`; pass `server ...` or `agent ...` |
| Ports | `EXPOSE 4747 4748` |
| Health check | None; use `GET /v1/health` for a server |
| Registry | `REGISTRY`, else `$DOCKER_REGISTRY`, else `registry.example.com` |

- `bind` and `agent_bind` default to loopback; set a non-loopback address to
  reach them from outside the container, and add `agent_tls` for `agent_bind`.
- The server is PID 1; give each server container a distinct hostname
  ([Multiple servers](#multiple-servers)).
- An agent runs as root and privileged ([agent](agent.md)), so it needs the
  user overridden.

## Running the server

```bash
vagabond server -config /etc/vagabond.d
```

| Flag | Default | Description |
|---|---|---|
| `-config <path>` | discovery order | File or directory; see [configuration](configuration.md#discovery) |
| `-dev` | off | Keep everything in memory; the `store` block is ignored |
| `-log-level <level>` | `info` | `debug`, `info`, `warn` or `error` |

Full flag reference: [CLI](cli.md).

What startup does, in order:

1. Discovers and decodes the configuration; any diagnostic exits 1.
2. Builds every provider, resolving credentials.
3. Asks every enabled provider except pools for capabilities. Failures print
   `Some providers did not answer: ...` and the server continues with those
   providers unhealthy.
4. Opens the store, applies migrations, verifies the schema version, reads
   quota usage ([database](database.md#startup)).
5. Loads the `tls` certificate and CA, warns when the API is reachable beyond
   loopback without a `ca`, then listens for agents on `agent_bind`.
6. Binds `bind`, claims dispatches whose lease lapsed, reaps stale quota
   reservations, starts the upkeep loops, then serves the API.

On `SIGINT` or `SIGTERM` the server stops accepting requests and gives
in-flight ones 10 seconds to finish. Dispatches it is running are not
cancelled: their executions keep running at the providers, their leases lapse,
and the next server to claim them follows them to the end. See
[dispatch](dispatch.md#resuming). Live output streams end at once, the upkeep
loops stop, and the agent listener closes every session; agents reconnect with
backoff.

### systemd

```ini
# /etc/systemd/system/vagabond.service
[Unit]
Description=Vagabond server
Wants=network-online.target
After=network-online.target postgresql.service

[Service]
User=vagabond
Group=vagabond
ExecStart=/usr/local/bin/vagabond server -config /etc/vagabond.d
# PGPASSWORD=..., and any variables credentials { env = ... } names.
EnvironmentFile=/etc/vagabond/server.env
Restart=on-failure
RestartSec=5
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd --system --no-create-home vagabond
sudo install -d -o root -g vagabond -m 0750 /etc/vagabond /etc/vagabond.d
sudo install -o root -g vagabond -m 0640 server.env /etc/vagabond/server.env
sudo systemctl daemon-reload
sudo systemctl enable --now vagabond
```

- `Restart=on-failure` covers a database that is not up yet: the server does
  not retry the store at startup, it exits 1.
- Credential files (`credentials { file = ... }`) must be readable by the
  service user. `credentials { exec = ... }` runs as the service user.
- The server needs no root, no containerd and no cgroup delegation. Those are
  agent requirements.

### `-dev`

`vagabond server -dev` runs with in-memory stores and no database. Jobs,
records and quota usage are lost at exit, so every budget resets on restart.

| Use `-dev` for | Do not use it for |
|---|---|
| Trying a configuration with fake providers | Anything with a real `pool` limit that matters |
| Developing against the API | More than one server |
| Running agents on a workstation | Work that must survive a restart |

Details: [database](database.md#in-memory-store--dev).

## Network exposure

| Listener | Default | Protocol | TLS | Authentication |
|---|---|---|---|---|
| API, `bind` | `127.0.0.1:4747` | HTTP, JSON | Optional, `server { tls { ... } }` | Client certificate when `tls` names a `ca`; otherwise none |
| Agents, `agent_bind` | `127.0.0.1:4748` | gRPC over a multiplexed TCP session | Mutual, `server { agent_tls { ... } }`; required beyond loopback | Agent certificate; node name is its common name |

**Without a `ca` the API has no authentication.** Anyone who can reach `bind`
can register, run, stop and cancel jobs in any namespace, and spend every
configured provider's quota. The server warns at startup when `bind` is beyond
loopback and the `tls` block names no `ca`. With one, every route but
`/v1/health` needs a certificate that chains to it. There is no authorization:
a valid certificate can use every route.

```hcl
server {
  bind       = "0.0.0.0:4747"
  agent_bind = "0.0.0.0:4748"

  tls {
    cert = "/etc/vagabond/tls/api.pem"
    key  = "/etc/vagabond/tls/api-key.pem"
    ca   = "/etc/vagabond/tls/clients-ca.pem" # CLI and CI certificates
  }

  agent_tls {
    cert = "/etc/vagabond/tls/agents.pem"
    key  = "/etc/vagabond/tls/agents-key.pem"
    ca   = "/etc/vagabond/tls/agents-ca.pem"  # agent certificates only
  }
}
```

```bash
vagabond job status -address https://vagabond.internal:4747 \
  -tls-ca /etc/vagabond/ca.pem -tls-cert ci.pem -tls-key ci-key.pem
```

**Issue agent certificates and API client certificates from different CAs.**
Each listener accepts any certificate its CA signed:

- The agent listener registers a node under the certificate's common name. A
  client certificate from the agent CA could join a pool as a node named for
  it and be sent workloads, with their environment.
- The API accepts any certificate from its `ca`. An agent certificate from the
  same CA could submit and dispatch jobs.

Two CAs, such as two Vault PKI mounts or two intermediates, keep each kind of
certificate to its own listener. The server does not check this.

- Keep the API on localhost, or bind it to an interface only trusted hosts
  reach, unless the `tls` block names a `ca`.
- Clients use `-address https://host:4747`, verify the server against `-tls-ca`
  or the system trust store, and present `-tls-cert` and `-tls-key`; see
  [CLI](cli.md#connecting-to-the-server).
- The agent listener binds beyond loopback only with `agent_tls`: every agent
  presents a certificate from its CA and registers under that certificate's
  common name. See [agent](agent.md#security).

## Running agents

Agents run on the nodes that execute `pool` workloads, not on the server host
(though they can). Each dials one server's `agent_bind` address, over mutual
TLS unless both are on the same host:

```bash
vagabond agent -server 10.0.0.5:4748 -pool homelab \
  -tls-ca /etc/vagabond/ca.pem -tls-cert /etc/vagabond/box1.pem -tls-key /etc/vagabond/box1-key.pem
```

Certificates: [agent](agent.md#certificates).

The server needs a `provider "homelab" { type = "containerd" }` block for
anything to schedule there. Requirements, systemd unit, capacity and flags:
[agent](agent.md).

## Sizing

The server is one process. What it costs grows with running dispatches and
configured providers, not with registered jobs.

| Load | Per | Cost |
|---|---|---|
| Lease renewal | Dispatch not yet done | One `UPDATE` every 20s, until every execution is released |
| Execution polling | Running or `lost` execution on an asynchronous provider | One provider `Status` call, interval doubling from 2s to 15s |
| Capability refresh | Enabled provider not made of agent nodes | One provider call per minute, 30s timeout |
| Agent report | Connected node, every 10s and on change | One execution record read per workload it holds |
| Usage refresh | Server | One read of current-period quota rows every 15s |
| Lease claim | Server | One `UPDATE ... RETURNING` every 30s |
| Reaper | Server | One locking read of reservations older than 1 hour every 5 minutes, plus one execution record read per stale reservation |
| Record writes | Execution | One conditional `INSERT` or `UPDATE` per state change, plus one each for the result and the release |
| Output capture | Ended execution | One provider `ReadLogs`, and one transaction per 256 KiB of output, up to `max_log_mib`. On Cloud Run, one `entries:list` request per page, against a quota of 60 a minute per project |
| Settle and release | Ended execution | A reservation lookup and a settle; one provider `Release`, retried from 250ms doubling to 10s while it fails |
| Output retention | Server | One `DELETE` of output past `log_retention` every hour |
| Live output | Followed request | One execution read per second, plus the provider's stream |
| Dispatch follow | `job run` / `job dispatch` | One `GET /v1/dispatch/{id}` per second: the dispatch and all of its executions |

- Database connections: pgxpool, at most `pool_max_conns` (default the
  greater of 4 and the CPU count). Every write is a serializable transaction;
  contention shows as `40001` retries, not errors, up to 10 attempts.
- Database growth: `executions` grows by one row per attempt, a few hundred
  bytes each, and nothing prunes it. Output is kept apart, up to `max_log_mib`
  (16 MiB) per attempt, and deleted `log_retention` (7 days) after the
  execution ends. See [database](database.md#retention).
- Request bodies are capped at 1 MiB. Request headers must arrive within 10
  seconds.
- A reservation holds room on a node for up to 2 minutes after placing a
  workload, until the agent's report accounts for it.

The full list of periodic loops: [background services](background-services.md).

## Multiple servers

Several servers can share one database. Each runs every background loop; the
store's single-statement operations keep them from acting on the same row
twice.

Safe:

| Operation | Why |
|---|---|
| Quota reservation | One `INSERT ... SELECT` in a serializable transaction; two servers cannot both see room |
| Settling | Deleting the reservation and adding usage is one statement; settling twice charges once |
| Resuming a dead server's dispatches | Claiming is one `UPDATE ... RETURNING` of lapsed leases; one server gets each |
| Finishing and renewing a dispatch | Conditional on `owner` and `epoch`; a server whose lease was taken stops every runner, starts no task and does not record the end |
| Execution writes | Conditional on the dispatch's lease (`owner`, `epoch`, not done); a runner whose write is refused stops, with no further provider call |
| Reaping | `FOR UPDATE SKIP LOCKED`; two reapers never resolve the same reservation |
| Registering a job | Serializable; concurrent registers are retried |

Not safe, or not shared:

- **Configuration must be identical.** Each server passes its own pool limits
  into the reservation statement, so servers with different limits enforce
  different ceilings over the same usage. A server claiming a dispatch on a
  provider it does not have configured records its unfinished executions
  `failed`, charges them for the time they were seen running, and marks them
  released without a provider call.
- **Agents belong to one server.** An agent dials one `-server` address, and
  a `containerd` provider sees only the agents connected to its own server.
  The same pool name on two servers is two disjoint sets of nodes. When a
  server dies, another server that claims its dispatches cannot reach those
  nodes: their executions stay `lost`, charged and unreleased, until the
  nodes connect to it or 24 hours pass
  ([Agent](agent.md#node-leaving-mid-run)).
- **Owners need distinct hostnames.** A lease owner is
  `server:<hostname>:<pid>`. Two servers with the same hostname and PID, such
  as containers both running as PID 1 under one hostname, share an owner and
  cannot tell their leases apart.
- **Cancelling goes through the lease.** `DELETE /v1/execution/{id}` on the
  server holding the dispatch stops it at once. On any other server it records
  the request on the dispatch, and the holder cancels the whole dispatch at
  its next renewal, within 20 seconds.
- **Plans can lag.** Each server's usage snapshot refreshes every 15 seconds,
  so `job plan` on two servers can disagree briefly. Reservations are still
  exact.
- **Health is per server.** `Running` in `/v1/health` counts this server's
  dispatches only.
- **Migrations take no lock.** Start one server on a new release first.
- `-dev` servers share nothing.

## Upgrading

1. Take a backup ([database](database.md#backups)).
2. Stop one server, replace the binary, start it. It applies any new
   migrations and verifies the schema version before listening.
3. Replace the others.

- Dispatches running on a stopped server keep running at the providers and
  are claimed once their lease lapses: up to 60 seconds after the last
  renewal, plus up to 30 seconds until the next claim.
- A `job run` or `job dispatch` following a run, as in CI, retries its polls
  for about two minutes while the server is away, then reports the run's real
  result. One still waiting past that exits 1, naming the dispatch
  ([CLI](cli.md#job-run)).
- A server whose binary expects an older schema than the database refuses to
  start: `database schema is at version N, newer than the M this binary
  expects; upgrade vagabond`. A server already running is not rechecked.
- There is no downgrade path other than restoring the backup.
- Agents reconnect on their own when the server restarts. `GET /v1/nodes`
  reports each node's agent `Version`.

## Health checking

`GET /v1/health` answers whether the server can take new work.

```shell
$ curl -s http://127.0.0.1:4747/v1/health
{"Store":"ok","UsageRefreshed":"2026-09-28T14:02:15.301Z","Running":2}
```

With the database down, the same request answers 503:

```shell
$ curl -s http://127.0.0.1:4747/v1/health
{"Store":"unreachable","StoreError":"the store is unreachable: ...","UsageRefreshed":"2026-09-28T14:02:15.301Z","Running":2}
```

| Field | Meaning |
|---|---|
| `Store` | `ok`, or `unreachable` when a ping within 5 seconds fails |
| `StoreError` | Present only when unreachable |
| `UsageRefreshed` | When the quota usage snapshot was last read, UTC. Older than about 15 seconds means refreshes are failing. |
| `Running` | Dispatches this server holds, started or resumed, until every one of their executions is released |

| Status | When |
|---|---|
| 200 | Store answers |
| 503 | Store does not answer; new dispatches are refused ([dispatch](dispatch.md#store-outage)) |

- Provider health is not part of it. A plan shows unhealthy providers.
- With `-dev` the store always answers.
- Use it as a load balancer or supervisor check; 503 means send new work
  elsewhere, not restart the process.

## Logs

The server and agent log to standard error through Go's `log/slog` text
handler, one `key=value` line per event. There is no JSON format option.
Under systemd, read them with `journalctl -u vagabond`.

```text
time=2026-09-28T14:02:00.114Z level=INFO msg="serving agents" address=127.0.0.1:4748
time=2026-09-28T14:02:00.131Z level=INFO msg=serving address=127.0.0.1:4747 tls=false
time=2026-09-28T14:03:12.540Z level=INFO msg="dispatch started" dispatch=... job=go-test version=3 namespace=default
```

| Level | Server events |
|---|---|
| `debug` | Every API request: `method`, `path`, `status`, `duration` |
| `info` | `serving`, `serving agents`, `stopped`, `node registered`, `dispatch started`, `dispatch finished`, `dispatch resumed`, `resumed dispatch finished`, `resolved abandoned quota reservations`, `pruned stored output` |
| `warn` | `dispatch finished without an answer`, `store unreachable`, `store write failed, retrying`, `node disconnected`, `agent connection`, `accepting agent connections failed, retrying`, `refreshing provider capabilities`, `refreshing quota usage`, `resolving abandoned quota reservations`, `releasing provider leftovers`, `claiming abandoned dispatches`, `resuming dispatch`, `pruning stored output`, `reading stored output ended early`, `log stream ended early` |
| `error` | `request failed`: an API request that answered 500 |

Startup errors, configuration diagnostics, and the `Some providers did not
answer` and `-dev` warnings are printed as plain text on standard error before
the structured log starts.
