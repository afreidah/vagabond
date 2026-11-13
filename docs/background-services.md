---
title: "Background Services"
seoTitle: "Background Services and Periodic Loops"
description: "Every periodic and long-running goroutine the server and agent run: what each does, how often, what it touches, how it fails and what it logs."
weight: 440
---

The server keeps its view of providers, quota and dispatches current, and its
stored output within its retention, with five upkeep loops, and runs each
dispatch, execution, lease, agent session and followed log stream in its own
goroutine. The agent holds one connection to the server and reports its
workloads on a timer. None of the loops apply jitter, and none take a lock
across servers: where two servers could collide, the store statement itself
decides (see [Multiple servers](#multiple-servers)).

Interactive version, with notes on each loop:
[Background services](https://vagabond.munchbox.cc/diagrams/background-services/).

## Summary

**Server:**

| Service | Interval | At startup | Touches | On failure |
|---|---|---|---|---|
| [Capability refresh](#capability-refresh) | 1m | Yes, before serving | Every non-pool provider's `Capabilities` | Provider marked unhealthy, warn |
| [Usage snapshot refresh](#usage-snapshot-refresh) | 15s | Yes, required | Store: quota usage and reservations | Last snapshot kept, warn |
| [Reservation reaper](#reservation-reaper) | 5m | Yes, before serving | Store: reservations over 1h old and their execution records | Reservation kept, warn |
| [Dispatch claim](#dispatch-claim) | 30s | Yes, before serving | Store: dispatches not done with a lapsed lease | Warn, retried next tick |
| [Output retention](#output-retention) | 1h | No | Store: stored output of executions that ended over `log_retention` ago | Warn, retried next tick |
| [Dispatch runner](#dispatch-runner) | Per dispatch | | Store, ledger | Dispatch ends `unanswered` |
| [Execution runner](#execution-runner) | Per execution, polling 2s to 15s | | Provider, store, ledger | Write retried, warn; refused write stops it |
| [Lease renewal](#lease-renewal) | 20s per open dispatch | | Store: the dispatch's lease | Ignored; stale lease stops every runner |
| [Live output follow](#live-output-follow) | 1s record re-read, per followed request | | Store: one execution; provider `StreamLogs` | Response ends, warn |
| [Agent listener](#agent-listener) | Long-running | Yes | Agent connections | Session dropped, node forgotten |
| [Store health](#store-health) | Per request | | Store ping | 503 from `GET /v1/health` |

**Agent:**

| Service | Interval | Touches | On failure |
|---|---|---|---|
| [Server connection](#server-connection) | Reconnect backoff 1s to 30s | Server's agent address | Warn, dial again |
| [Workload report](#workload-report) | 10s, and on every change | containerd; server `Register` | Warn, next report carries everything |
| [Log stream](#log-stream) | 250ms, per followed workload | Log file; containerd task | Stream ends with an error |
| [Timeout watcher](#timeout-watcher) | Per workload | containerd task | Workload left running |

**Not loops:** pool reservation expiry and the departed-node grace period are
checked when read, not swept ([Pool reservations](#pool-reservations)).

## Server

The five upkeep loops start when the server begins serving and stop when it
shuts down. Each tick runs synchronously; a run longer than its interval is
followed immediately by the next, and ticks do not pile up. The first tick is
one interval after startup. Capability refresh, usage refresh, the reaper and
the claim each ran once before serving; output retention has no startup pass,
so its first pass is 1h in.
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

Drops quota reservations whose execution was never recorded: a process that
died, or a store that refused the `pending` record, between reserving and
recording. Every recorded execution's reservation is settled by its
[execution runner](#execution-runner). See [Quotas](quotas.md) for reservation
and settlement.

| | |
|---|---|
| Interval | 5m |
| At startup | Once, before serving |
| Selects | Reservations created more than 1h ago |
| Touches | Store: reservations and each one's execution record |

| Execution record | Verdict |
|---|---|
| None | Dropped, charging nothing |
| Exists | Kept; its runner settles it |
| Cannot be read | Kept, looked at again next tick |

- The hour covers the gap between reserving and recording, so a reservation
  whose record is about to be written is never dropped.
- On Postgres the claim locks the rows with `FOR UPDATE SKIP LOCKED` and holds
  them while each record is read, so two servers never resolve the same
  reservation. The whole pass is one serializable transaction.
- A serialization failure retries the whole pass, up to 10 attempts
  ([Transactions](database.md#transactions)).
- No provider is called.
- Only providers with quota pools reserve anything.

**Logs:** info `resolved abandoned quota reservations` with `count` when any
were dropped; warn `resolving abandoned quota reservations` on error.

### Output retention

Deletes the stored output of executions that ended longer ago than the
server's `log_retention` (default `168h`,
[Configuration](configuration.md#server-block)). The execution records stay.

| | |
|---|---|
| Interval | 1h |
| At startup | No; the first pass is one interval in |
| Selects | Output of executions whose `ended_at` is before now minus `log_retention` |
| Touches | Store: `execution_logs` ([Database](database.md#execution_logs)) |

- One statement per pass, driven from the stored chunks, so its cost follows
  the output retention keeps rather than every execution ever recorded.
- An execution still running, or ended within the retention, keeps its
  output.
- Once pruned, `GET /v1/execution/{id}/logs` answers 200 with an empty body.
- Idempotent, so several servers running it at once is harmless.

**Logs:** info `pruned stored output` with `executions` when any were pruned;
warn `pruning stored output` with the error.

### Dispatch claim

Takes over dispatches whose owner stopped renewing their lease before every
execution was released, and resumes each ([Resuming](dispatch.md#resuming)).

| | |
|---|---|
| Interval | 30s |
| At startup | Once, before serving |
| Selects | Dispatches not done (`done_at` null) whose lease expired, running or ended, except those this server is running |
| Sets | Owner to this server, epoch up by one, lease to 60s from now |

- The claim is one statement, so two servers never claim the same dispatch.
- The new epoch makes the store refuse every later write under the old
  lease, so the previous owner, if it is still alive, stops at its next write.
- Each claimed dispatch is resumed in its own goroutine, on a context that
  outlives server shutdown. A cancel requested before the claim is acted on
  at once.
- A dispatch whose executions cannot be read stays open; its new lease lapses
  and a later claim tries again.

**Logs:** warn `claiming abandoned dispatches` on error; per dispatch, info
`dispatch resumed` with `epoch`, then info `resumed dispatch finished` with
`state` and `reason` once every execution is released, or warn `resuming
dispatch` with the error.

### Dispatch runner

One goroutine per dispatch started through `POST /v1/jobs/run` or
`POST /v1/job/{name}/dispatch`. Runs the job's tasks in order, starting an
[execution runner](#execution-runner) for each attempt, records how it ended,
then waits for every execution to be released and marks the dispatch done
([Dispatch](dispatch.md)).

- Runs on a context detached from the request, so the request returns the
  dispatch ID at once.
- Cancelled only by `DELETE /v1/execution/{id}` on one of its executions: at
  once on this server, or through its lease from another
  ([Cancellation](dispatch.md#cancellation)). Server shutdown does not stop
  it; the process exit does, and the dispatch is resumed after its lease
  lapses.
- Retry submissions wait `backoff.initial` doubling to `backoff.max`
  ([Backoff and polling](dispatch.md#backoff-and-polling)).
- The dispatch's ending and done mark are retried until the store takes them,
  250ms doubling to 10s.

**Logs:** info `dispatch started`; info `dispatch finished` with `tasks` and
`succeeded`, or warn `dispatch finished without an answer` with `tasks` and
`error`, once every execution is released. Every line carries `dispatch`,
`job`, `version` and `namespace`.

### Execution runner

One goroutine per execution: started for each attempt a dispatch makes, and
for each unfinished execution a claim restores. It submits, watches, collects,
settles and releases its execution, in that order, and is the only writer of
its record ([Execution runners](dispatch.md#execution-runners)).

| | |
|---|---|
| Polling | `Status` from 2s doubling to 15s while the execution runs or is `lost` |
| Store writes | Checked against the dispatch's lease, 10s timeout each; one that fails otherwise is retried, 250ms doubling to 10s, until it lands |
| Provider calls | `Status`, `Result`, `Cancel` and `Release` 30s timeout each; `Submit` none |
| `Release` | Retried, 250ms doubling to 10s, until it succeeds or the provider reports the execution unknown |
| Stops | Once its execution is released, or when the store refuses a write under the lease |

- Its task waits only for its first answer; settling and releasing go on
  after the task has moved on.
- A `lost` execution's runner polls until a terminal state, until the
  provider no longer knows it, or until a `Status` failure 24 hours after it
  became `lost` fails it ([Lost executions](dispatch.md#lost-executions)).
- A cancelled dispatch stops the watch: the runner cancels the execution at
  its provider, then collects, settles and releases it.
- A refused write means another server claimed the dispatch; the runner stops
  without another provider call.

**Logs:** warn `store write failed, retrying` with `error` for each failed
write; warn `releasing provider leftovers` with `execution` and `error` for
each failed `Release`.

### Lease renewal

One goroutine per open dispatch, started or resumed, extending its lease until
every execution is released ([Leases](dispatch.md#leases)).

| | |
|---|---|
| Interval | 20s, first renewal 20s after the dispatch starts |
| Extends to | 60s from the renewal |
| Timeout | 10s per write |
| Stops | When the dispatch is done, or when the lease is found taken |

- Any failure but a stale lease is ignored; two renewals can fail before the
  lease lapses.
- A stale lease (the dispatch is done, or another server claimed it) marks the
  lease lost: every runner under it stops, the dispatch starts no further task
  and does not write its ending.
- Each renewal returns any cancel requested from another server; one that
  carries a request cancels the dispatch.
- Runs on a context detached from the dispatch, so a cancelled dispatch keeps
  its lease until every execution is released.

**Logs:** none.

### Live output follow

Two goroutines per `GET /v1/execution/{id}/logs?follow=true` request on an
execution that has not ended, when its provider streams output (`cloud-run`,
`code-engine`, `pool`): one copies the provider's stream to the client, flushing every write;
the other re-reads the record to end the stream.

| | |
|---|---|
| Interval | Record re-read every 1s |
| Ends | 5s after the record is terminal; at once when the client goes or shutdown begins |
| Touches | Store: the execution record; provider `StreamLogs` |

- A record that cannot be read is retried the next second.
- A provider that cannot place its stream at `offset` is answered 416, if no
  output was written yet.
- The stream never writes the record.

**Logs:** warn `log stream ended early` with `execution` and `error`.

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
- A failed accept, such as one out of file descriptors, is retried, waiting
  5ms doubling to 1s while accepts keep failing.
- The listener closing while the server runs stops the server, API included,
  with `serving agents: agent listener closed`, since no agent could connect
  again.
- On shutdown the listener closes and every session is closed.

**Logs:** info `serving agents` with `address`; info `node registered` with
`node`, `pool`, `address`, `cpu`, `memory` and `held`, only when the node was
not already connected; warn `node disconnected` with `node`; warn `agent
connection` when a session cannot be opened; warn `accepting agent connections
failed, retrying` with `error` for each failed accept.

### Store health

There is no store health loop. `GET /v1/health` pings the store on each
request with a 5s timeout: 200 with `Store` `ok`, or 503 with `Store`
`unreachable` and `StoreError`. The server recovers from an outage without a
loop of its own: runners retry their writes until the store takes them, each
upkeep loop retries on its next tick, and the claim takes dispatches whose
lease lapsed meanwhile ([Store outage](dispatch.md#store-outage)).

### Pool reservations

A `pool` provider holds room on a node between submitting a workload and the
node reporting it. Nothing sweeps these; they are checked when a node's free
room is computed.

| | |
|---|---|
| Reservation covers | 2m, or until the node reports the workload |
| Removed | On `Release`, or when sending the workload to the node fails for any reason: the node left, refused it for room, or returned any other error |
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
| Also | On connecting, after every submit that succeeds, and after every cancel or release, whether or not it succeeded |
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
- A workload without a timeout, or without a recorded start, gets no watcher.
- A watcher that cannot load the task exits and leaves the workload running.

**Logs:** none.

### Log stream

One goroutine per server `StreamLogs` call, copying the workload's log file
from the requested offset as it grows.

| | |
|---|---|
| Interval | 250ms between reads, each followed by a task status check |
| Ends | After a last read once the workload is terminal or released, or when the server stops listening |
| Touches | `<data-dir>/logs/<id>.log`; containerd task status |

**Logs:** none on the agent; the server logs a broken stream.

## Multiple servers

Several servers can share one store. Every loop runs on every server; the
store keeps them apart:

| Service | Coordination |
|---|---|
| Dispatch claim | One `UPDATE ... RETURNING` statement: a lapsed dispatch goes to exactly one server, under a new epoch |
| Execution runners | Every execution and output write is checked against the dispatch's lease, so only the current holder's land; a runner whose write is refused stops |
| Reservation reaper | Row locks with `SKIP LOCKED`: each stale reservation is resolved by one server |
| Output retention | Deleting what another server already deleted deletes nothing |
| Usage refresh, capability refresh | Per-server reads; nothing to coordinate |
| Agent sessions | Per server. An agent connects to one server, and its pools exist only there |
