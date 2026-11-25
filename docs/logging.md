---
title: "Logging"
seoTitle: "Logging: Format, Components, Fields and Messages"
description: "How the server and agent log: text or JSON, the component every line names, the IDs a dispatch's and an execution's lines carry, the standard keys, every message by level, and the conventions the code follows."
weight: 450
---

The server and the agent write their logs to standard error through one
logger, `internal/logging`, built from two flags. Every line names the part of
Vagabond that wrote it, and a line about a dispatch or an execution carries its
IDs wherever in the code it was written.

## Output

| Flag | Default | Description |
|---|---|---|
| `-log-level <level>` | `info` | `debug`, `info`, `warn` or `error`, in any case. Lines below it are not written |
| `-log-json` | off | One JSON object per line in place of `key=value` text |

Both `vagabond server` and `vagabond agent` take them. Any other level is
refused before anything starts:

```text
invalid value "loud" for flag -log-level: use debug, info, warn or error
```

Text, the default:

```text
time=2026-11-25T03:25:46.382-08:00 level=INFO msg=serving component=server listen_addr=127.0.0.1:4747 tls=false
time=2026-11-25T03:25:47.381-08:00 level=DEBUG msg=request component=server method=GET path=/v1/health status=200 duration=1.204ms
```

The same lines with `-log-json`:

```json
{"time":"2026-11-25T03:10:43.16853131-08:00","level":"INFO","msg":"serving","component":"server","listen_addr":"127.0.0.1:4747","tls":false}
{"time":"2026-11-25T03:10:44.678098135-08:00","level":"DEBUG","msg":"request","component":"server","method":"GET","path":"/v1/health","status":200,"duration":"1.204ms"}
```

- `level` is `DEBUG`, `INFO`, `WARN` or `ERROR`; `msg` is the message.
- A duration is written as text, such as `1.5s`, in both formats.
- An error is written as its message.
- The HTTP server's own errors, mostly failed TLS handshakes, are written at
  `debug` under `component=server`.

**Not in this format:**

| Lines | Written by | Format |
|---|---|---|
| Startup errors, configuration diagnostics, `-dev` and provider warnings | The CLI, before the logger starts | Plain text |
| vagabond-firecracker's own lines, on the agent's standard error | The daemon, at the agent's `-log-level` | Its own text ([Firecracker](firecracker.md#logging)) |
| gRPC's errors about agent sessions | gRPC's own logger | Plain text |
| containerd's warning when it skips an image label in its reserved namespace | containerd's client library | Its own text |

## Components

Every line carries `component`, the part of Vagabond that wrote it:

| `component` | Writes about | Package |
|---|---|---|
| `server` | The API, upkeep tasks, periodic launches, and each dispatch starting, resuming and finishing | `internal/server` |
| `dispatch` | Store writes retried, output capture, provider releases, abandoned dispatches | `internal/dispatch` |
| `nodes` | Agent connections to the server: serving, registering, disconnecting | `internal/nodes` |
| `agent` | The agent's node and its connection to the server; every line also carries `server_addr` | `internal/agent` |
| `executor` | The containerd executor on a node | `internal/agent/executor` |

## Context fields

A dispatch's and an execution's IDs are set once, on the context the work runs
on, and every line logged with that context carries them, including lines
written in another package:

| Fields | Set by | Carried by |
|---|---|---|
| `dispatch_id`, `namespace`, `job`, `job_version` | The server, where it launches or resumes a dispatch | Every line about the dispatch: the server's, the dispatcher's, its lease renewal's, and each execution's |
| `execution_id` | The dispatcher, as an execution's runner starts | Every line about the execution, its output capture included |

```text
time=... level=WARN msg="store write failed, retrying" component=dispatch error=... dispatch_id=... namespace=default job=go-test job_version=3 execution_id=...
```

Context fields come after the line's own. A job run from a file has
`job_version=0`.

An API request's `method` and `path` are not context fields. A dispatch
outlives the request that launched it, and its lines would carry that request
for the whole run, so request lines pass them themselves.

## Keys

| Key | Meaning |
|---|---|
| `component` | The part of Vagabond that wrote the line ([Components](#components)) |
| `dispatch_id` | A dispatch's ID |
| `execution_id` | An execution's ID |
| `namespace`, `job`, `job_version` | The job a dispatch runs, and its registered version |
| `node`, `pool` | An agent node's name and its pool |
| `provider` | A provider's name |
| `task` | The upkeep task that failed |
| `listen_addr` | An address the server listens on |
| `remote_addr` | The address a connection came from |
| `server_addr` | The server an agent dials |
| `method`, `path`, `status`, `duration` | An API request, its status and how long it took |
| `count` | How many were acted on |
| `epoch`, `state`, `reason` | A resumed dispatch's lease epoch, its verdict and why |
| `retry_in` | How long until the next attempt |
| `by` | Who disabled or enabled a provider: the client certificate's name |
| `in`, `panic`, `stack` | Where a recovered panic happened, its value and its stack |
| `error` | The error, as its message |

Every key is snake_case. The lint (`sloglint` in `.golangci.yml`) refuses
these, which earlier versions wrote:

| Refused | Use |
|---|---|
| `err` | `error` |
| `execution` | `execution_id` |
| `dispatch` | `dispatch_id` |
| `version` | `job_version` |
| `from` | `remote_addr` |
| `address` | `listen_addr` or `remote_addr` |
| `server` | `server_addr` |

## Messages

### Server

| Level | `component=server` | `component=dispatch` | `component=nodes` |
|---|---|---|---|
| `debug` | `request` | | |
| `info` | `serving`, `stopped`, `dispatch started`, `dispatch finished`, `dispatch resumed`, `resumed dispatch finished`, `periodic launch`, `periodic launch skipped`, `resolved abandoned quota reservations`, `pruned stored output`, `provider disabled`, `provider enabled` | | `serving agents`, `node registered` |
| `warn` | `dispatch finished without an answer`, `resuming the dispatch failed`, `upkeep task failed`, `store unreachable`, `reading stored output ended early` | `store write failed, retrying`, `releasing provider leftovers failed, retrying`, `capturing output failed`, `reading stored output back failed, retrying`, `output left incomplete` | `node disconnected`, `agent connection failed`, `accepting agent connections failed, retrying` |
| `error` | `request failed`: an API request that answered 500; `recovered a panic` | `abandoning dispatch`, `recovered a panic` | `recovered a panic` |

`upkeep task failed` names its task in `task`: `refreshing provider
capabilities`, `refreshing quota usage`, `reading disabled providers`,
`resolving abandoned quota reservations`, `claiming abandoned dispatches`,
`pruning stored output` or `launching periodic jobs`
([Background services](background-services.md)).

### Agent

| Level | `component=agent` | `component=executor` |
|---|---|---|
| `info` | `node read`, `connected`, `released a workload the server has no use for` | |
| `warn` | `lost the server`, `connecting to the server failed`, `reporting workloads failed`, `releasing a workload the server has no use for failed` | `trimming a workload's output failed` |
| `error` | `recovered a panic` | |

## Levels

| Level | For |
|---|---|
| `debug` | Every API request, and the HTTP server's own errors |
| `info` | Starting and stopping, and work that finished or was acted on |
| `warn` | A failure the process carries on through: retried, skipped, or left for the next pass |
| `error` | An operation given up on: an API request that answered 500, an abandoned dispatch, a recovered panic |

## Writing log lines

- **A message is fixed text.** Values go in fields, so one message finds every
  line of its kind: `node disconnected` with `node=box1`, never
  `node box1 disconnected`.
- **A failure says so.** `connecting to the server failed`; one that is tried
  again ends `failed, retrying`.
- **Something that happened is in the past tense**: `node registered`,
  `dispatch finished`.
- **Every call takes a context**, `InfoContext` and the rest, which the lint
  requires, so the context's fields reach the line.
- **A field the context carries is not passed again.** A runner's line never
  passes `execution_id`.
- **A new key goes in [the table](#keys)** in the same change.

A component's constructor takes a `*slog.Logger` and names itself with
`logging.Named`. It passes the logger it was given, not its named one, to
anything it builds, which names itself in turn:

```go
// New builds the component, logging to logger under its own name, or
// nowhere when logger is nil.
func New(logger *slog.Logger) *Thing {
	return &Thing{logger: logging.Named(logger, "thing")}
}
```

Work that every line about should name puts its fields on its context:

```go
ctx = logging.With(ctx, "execution_id", rec.ID)
```

In tests, a nil logger discards everything. A test that reads lines back
passes `logging.New(&buf, logging.Options{})` over a `testkit.Buffer`, which
the code under test can write while the test reads it.
