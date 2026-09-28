---
title: "Background Services"
seoTitle: "Background Services and Periodic Loops"
description: "Every periodic and long-running goroutine the server and agent run: what each does, how often, what it touches, how it fails and what it logs."
weight: 440
---

The server keeps its view of providers, quota and dispatches current with four
upkeep loops, and runs each dispatch, lease and agent session in its own
goroutine. The agent holds one connection to the server and reports its
workloads on a timer. None of the loops apply jitter, and none take a lock
across servers: where two servers could collide, the store statement itself
decides (see [Multiple servers](#multiple-servers)).

## Summary

**Server:**

| Service | Interval | At startup | Touches | On failure |
|---|---|---|---|---|
| [Capability refresh](#capability-refresh) | 1m | Yes, before serving | Every non-pool provider's `Capabilities` | Provider marked unhealthy, warn |
| [Usage snapshot refresh](#usage-snapshot-refresh) | 15s | Yes, required | Store: quota usage and reservations | Last snapshot kept, warn |
| [Reservation reaper](#reservation-reaper) | 5m | Yes, before serving | Store: reservations over 1h old; provider `Status` | Reservation kept, warn |
| [Dispatch claim](#dispatch-claim) | 30s | Yes, before serving | Store: dispatches with a lapsed lease | Warn, retried next tick |
| [Dispatch runner](#dispatch-runner) | Per dispatch | | Providers, store, ledger | Dispatch ends `unanswered` |
| [Lease renewal](#lease-renewal) | 20s per running dispatch | | Store: the dispatch's lease | Ignored; stale lease stops the run |
| [Agent listener](#agent-listener) | Long-running | Yes | Agent connections | Session dropped, node forgotten |
| [Store health](#store-health) | Per request | | Store ping | 503 from `GET /v1/health` |

**Agent:**

| Service | Interval | Touches | On failure |
|---|---|---|---|
| [Server connection](#server-connection) | Reconnect backoff 1s to 30s | Server's agent address | Warn, dial again |
| [Workload report](#workload-report) | 10s, and on every change | containerd; server `Register` | Warn, next report carries everything |
| [Timeout watcher](#timeout-watcher) | Per workload | containerd task | Workload left running |

**Not loops:** pool reservation expiry and the departed-node grace period are
checked when read, not swept ([Pool reservations](#pool-reservations)).

## Server

The four upkeep loops start when the server begins serving and stop when it
shuts down. Each tick runs synchronously; a run longer than its interval is
followed immediately by the next, and ticks do not pile up. The first tick is
one interval after startup, because each was already run once before serving.
A failure is logged and waits for the next tick; one loop failing never stops
another.

### Capability refresh

Asks every enabled provider for its capabilities, which admission reads on
every plan and dispatch.

| | |
|---|---|
| Interval | 1m |
| At startup | Once while loading configuration, before the server is built |
| Timeout | 30s per provider; providers are asked in parallel |
| Skips | Disabled providers, and `pool` providers, whose capabilities are built from connected nodes on every read |

- A provider that answers is marked healthy and its capabilities replaced.
- A provider that fails is marked unhealthy and keeps its last capabilities.
  Admission rejects it by name until a later refresh succeeds
  ([reason codes](scheduling.md#reason-codes)).
- Errors from every provider are collected, not stopped at the first.

**Logs:** warn `refreshing provider capabilities`, with every provider's error
joined. At startup the CLI prints `Some providers did not answer: <error>`
instead and the server still starts.

### Usage snapshot refresh

Re-reads settled usage plus outstanding reservations for every quota pool's
current period, so plans see what every server has charged.

| | |
|---|---|
| Interval | 15s |
| At startup | Required: a server that cannot read usage does not start (`could not read quota usage`) |
| Also | Before every task a dispatch runs |
| Touches | Store: `quota_usage` and `quota_reservations` ([Database](database.md)) |

- A failed read keeps the last snapshot. Plans keep answering from it; the
  reservation at dispatch is what enforces the limit.
- `UsageRefreshed` in `GET /v1/health` is when the snapshot was last read,
  and ages while reads fail.

**Logs:** warn `refreshing quota usage`.

### Reservation reaper

Resolves quota reservations a process left when it died, or failed to settle,
between reserving and settling. See [Quotas](quotas.md) for reservation and
settlement.

| | |
|---|---|
| Interval | 5m |
| At startup | Once, before serving |
| Selects | Reservations created more than 1h ago |
| Touches | Store: reservations and usage; each reservation's provider `Status` |

Each stale reservation's provider is asked about the execution:

| Provider answer | Verdict |
|---|---|
| Provider no longer configured | Kept |
| Unknown execution | Dropped: it never ran |
| `Status` not supported (synchronous providers) | Settled at the reserved amount |
| Error, or not terminal | Kept, asked again next tick |
| Terminal, never started | Settled as a run of zero duration |
| Terminal, started, no end time | Settled at the reserved amount |
| Terminal, started and ended | Settled at the declared shape over the time it ran |

- On Postgres the claim locks the rows with `FOR UPDATE SKIP LOCKED` and holds
  them while providers are asked, so two servers never resolve the same
  reservation. The whole pass is one serializable transaction.
- Provider calls have no timeout of their own; a slow provider holds the pass.
- Only providers with quota pools reserve anything.

**Logs:** info `resolved abandoned quota reservations` with `count` when any
were settled; warn `resolving abandoned quota reservations` on error.

### Dispatch claim

Takes over running dispatches whose owner stopped renewing their lease, and
resumes each ([Resuming](dispatch.md#resuming)).

| | |
|---|---|
| Interval | 30s |
| At startup | Once, before serving |
| Selects | Dispatches in `running` whose lease expired, except those this server is running |
| Sets | Owner to this server, lease to 60s from now |

- The claim is one statement, so two servers never claim the same dispatch.
- Each claimed dispatch is resumed in its own goroutine, on a context that
  outlives server shutdown.
- A dispatch whose executions cannot be read stays `running`; its new lease
  lapses and a later claim tries again.

**Logs:** warn `claiming abandoned dispatches` on error; per dispatch, info
`dispatch resumed` with `previous_owner`, then info `resumed dispatch
finished` with `state` and `reason`, or warn `resuming dispatch` with the
error.

### Dispatch runner

One goroutine per dispatch started through `POST /v1/jobs/run` or
`POST /v1/job/{name}/dispatch`. Runs the job's tasks in order and records how
it ended ([Dispatch](dispatch.md)).

- Runs on a context detached from the request, so the request returns the
  dispatch ID at once.
- Cancelled only by `DELETE /v1/execution/{id}` on one of its executions.
  Server shutdown does not stop it; the process exit does, and the dispatch
  is resumed after its lease lapses.
- Inside a dispatch, polling a provider backs off from 2s to 15s, and retry
  submissions wait `backoff.initial` doubling to `backoff.max`
  ([Backoff and polling](dispatch.md#backoff-and-polling)).

**Logs:** info `dispatch started`; info `dispatch finished` with `tasks` and
`succeeded`, or warn `dispatch finished without an answer` with `tasks` and
`error`. Every line carries `dispatch`, `job`, `version` and `namespace`.

### Lease renewal

One goroutine per running or resumed dispatch, extending its lease
([Leases](dispatch.md#leases)).

| | |
|---|---|
| Interval | 20s, first renewal 20s after the dispatch starts |
| Extends to | 60s from the renewal |
| Timeout | 10s per write |
| Stops | When the dispatch ends, or when the lease is found taken |

- Any failure but a stale lease is ignored; two renewals can fail before the
  lease lapses.
- A stale lease (the dispatch ended, or another server claimed it) marks the
  lease lost: the dispatch starts no further task and does not write its
  ending.
- Runs on a context detached from the dispatch, so a cancelled dispatch keeps
  its lease until its ending is recorded.

**Logs:** none.

### Agent listener

Accepts agent connections on the `server` block's `agent_bind` address
([configuration](configuration.md#server-block)) and runs one goroutine per
session ([agent](agent.md)).

- Each connection is a yamux session. yamux sends a keepalive every 30s on
  both sides and closes a session whose keepalive fails, which is how a dead
  peer is detected.
- A node is registered while its session holds. When the session ends, the
  node is removed and its departure time recorded for the
  [pool](#pool-reservations) grace period.
- An agent reconnecting under the same name replaces its old session.
- On shutdown the listener closes and every session is closed.

**Logs:** info `serving agents` with `address`; info `node registered` with
`node`, `pool`, `address`, `cpu`, `memory` and `held`, only when the node was
not already connected; warn `node disconnected` with `node`; warn `agent
connection` when a session cannot be opened.

### Store health

There is no store health loop. `GET /v1/health` pings the store on each
request with a 5s timeout: 200 with `Store` `ok`, or 503 with `Store`
`unreachable` and `StoreError`. The server recovers from an outage through its
loops: each retries on its next tick, the claim picks up dispatches whose
ending was not written, and the reaper settles reservations that were not
([Store outage](dispatch.md#store-outage)).

### Pool reservations

A `pool` provider holds room on a node between submitting a workload and the
node reporting it. Nothing sweeps these; they are checked when a node's free
room is computed.

| | |
|---|---|
| Reservation covers | 2m, or until the node reports the workload |
| Removed | On `Release`, or when the node refuses the submission |
| Departed node grace | 5m: its executions report `lost` while it may reconnect |
| After server start | 5m during which an execution no node has reported is `lost`, not unknown |

An expired reservation stops counting against the node but stays in memory
until its execution is released. Both grace periods are compared against the
clock when a status is read.

## Agent

### Server connection

Holds one session to the server and dials again whenever it drops.

| | |
|---|---|
| Backoff | 1s, doubling to 30s |
| Reset | To 1s after a session that registered and then dropped |
| Keepalive | yamux, 30s |

- A dial or registration failure counts as a failed connection and grows the
  backoff.
- Workloads keep running while disconnected; they belong to containerd, and
  the agent reports them when it reconnects.

**Logs:** info `connected` with `server`, `name` and `pool`; warn `lost the
server` with `server`; warn `connecting to the server` with `server`, `error`
and `retry` (the next wait).

### Workload report

Re-registers the node with every workload containerd holds, each with its
declared CPU and memory and whether it is still running. The server sizes the
node's free room from these reports.

| | |
|---|---|
| Interval | 10s |
| Also | On connecting, and after every submit, cancel or release |
| Touches | containerd container list; server `Register` |

- Change signals coalesce: one pending signal covers any number of changes.
- The 10s report catches a workload finishing on its own.
- A failed report is not fatal; the next one carries everything.

**Logs:** warn `reporting workloads` with `error`.

### Timeout watcher

One goroutine per workload that declares a timeout, stopping it once it has
run that long.

| | |
|---|---|
| Deadline | Workload start plus its timeout |
| Stop | SIGTERM, then SIGKILL 10s later if it has not exited |
| Started | On submit, and for every held workload when the agent starts |

- Start time and timeout are stored as container labels, so a restarted agent
  watches the same deadline.
- A workload without a timeout gets no watcher.
- A watcher that cannot load the task exits and leaves the workload running.

**Logs:** none.

## Multiple servers

Several servers can share one store. Every loop runs on every server; the
store keeps them apart:

| Service | Coordination |
|---|---|
| Dispatch claim | One `UPDATE ... RETURNING` statement: a lapsed dispatch goes to exactly one server |
| Reservation reaper | Row locks with `SKIP LOCKED`: each stale reservation is resolved by one server |
| Usage refresh, capability refresh | Per-server reads; nothing to coordinate |
| Agent sessions | Per server. An agent connects to one server, and its pools exist only there |
