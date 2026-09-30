---
title: "HTTP API"
seoTitle: "HTTP API Reference for vagabond server"
description: "Every route vagabond server serves under /v1: parameters, request and response bodies, status codes, and the Go client."
weight: 230
---

`vagabond server` serves a JSON API under `/v1`. The [CLI](cli.md) is a client
of this API; everything it does to a server goes through the routes below.

## Conventions

**Address:** the server block's `bind`, default `127.0.0.1:4747`. See
[configuration](configuration.md#server-block).

**Field names:** the Go field names of the types in `internal/api`, untagged,
so keys are `PascalCase` (`DispatchID`, `JobVersion`). Only `Health.StoreError`,
`Error.Diagnostics` and `Diagnostic.Range` are omitted when empty; every other
field is always present, as `null` when unset.

**Decoding:** request bodies are decoded with Go's `encoding/json`. Keys match
case-insensitively, unknown keys are ignored, and the body is capped at 1 MiB;
a larger body is a 400.

**Types:**

| Value | Encoding |
|---|---|
| Times | RFC 3339 strings in UTC, e.g. `"2026-09-28T14:03:11.52Z"` |
| Durations | Integer nanoseconds (`Duration`, `Billed.Duration`) |
| IDs | Dispatch and execution IDs are UUIDv7 strings, time-sortable |
| Optional times and results | `null` until set (`Ended`, `Started`, `ExitCode`, `Billed`) |

**Content types:**

| Direction | Content type |
|---|---|
| Request body | JSON. `Content-Type` is not checked; the Go client sends `application/json` |
| Response body | `application/json`, except `GET /v1/execution/{id}/logs` |
| Logs response | `text/plain; charset=utf-8`, with `X-Content-Type-Options: nosniff` |

**Authentication:** none. The API has no authentication or authorization;
anyone who can reach the listener can register, run and cancel jobs. The
default bind is loopback for this reason. Bind to a wider address only behind a
network or proxy you control.

**Logging:** every request is logged at `debug` with method, path, status and
duration. 500s are logged at `error`, 503s at `warn`.

## Namespaces

Routes that read or write jobs take `?namespace=<name>`. Routes addressed by a
dispatch or execution ID, `/v1/nodes` and `/v1/health` ignore it: IDs are
unique across namespaces.

| Route | Namespace used |
|---|---|
| `POST /v1/jobs`, `POST /v1/jobs/run`, `POST /v1/jobs/plan` with `Source` | The job file's `namespace` attribute, else `?namespace=`, else `default` |
| `GET /v1/jobs`, `GET` and `DELETE /v1/job/{name}`, `POST /v1/job/{name}/dispatch`, `POST /v1/jobs/plan` with `Name` | `?namespace=`, else `default` |

- A job file that names a namespace, sent with a different `?namespace=`, is a
  400: `job "build" names namespace "ci", but "team-a" was requested; remove one`.
  The same value in both is accepted.
- A namespace not declared in the configuration is a 400:
  `namespace "ci" is not declared in the configuration`. `default` always exists.
- A registered job lives in the namespace it was registered in. Reading,
  stopping or dispatching it from another namespace is a 404.

Namespace declaration and quota shares: [configuration](configuration.md#namespace-block),
[quotas](quotas.md).

## Errors

Every non-2xx response from a route in the table below has this body:

```json
{
  "Error": "the job is not valid",
  "Diagnostics": [
    {
      "Severity": "error",
      "Summary": "Missing required argument",
      "Detail": "The argument \"driver\" is required, but no definition was found.",
      "Range": "job:4,3-9"
    }
  ]
}
```

| Field | Description |
|---|---|
| `Error` | Message. Always set |
| `Diagnostics` | Job file parse and validation problems; present only for an invalid job |
| `Diagnostics[].Severity` | `error` or `warning` |
| `Diagnostics[].Summary` | One-line problem |
| `Diagnostics[].Detail` | Explanation |
| `Diagnostics[].Range` | `file:line,col-col`, omitted when the problem has no location. The file is `job` for a submitted `Source`, and `<name> (version <n>)` for a registered job |

### Status codes

| Status | Cause |
|---|---|
| 200 | Success. Every route answers 200; none use 201 or 204 |
| 400 | Unreadable or oversized body; invalid job file (with `Diagnostics`); metadata the job does not permit; conflicting or undeclared namespace; malformed dispatch or execution ID; plan with both `Source` and `Name` |
| 404 | Job not registered in the namespace; no such dispatch or execution |
| 409 | Dispatching or planning (by `Name`) a stopped job |
| 500 | Anything else: a provider refusing a cancel, a provider no longer configured, an internal error. Logged by the server |
| 503 | The store is unreachable: connection refused, broken or timed out. Nothing was written; the same request can be retried once the store is back. Other store errors are 500 |

Error messages carry the identifier, for example
`job not registered: "build" in namespace "default"`,
`execution not found: dispatch 0192a4c8-7f3e-7b21-9c4d-5e6f7a8b9c0d`,
`job is stopped: "build"; register it again to dispatch it`.

An unknown path answers 404 and a known path with the wrong method answers 405
with an `Allow` header. Both come from Go's router with a `text/plain` body, not
the JSON error body.

### Store outage

Every route that reads or writes the store answers 503 while the store is
unreachable. Routes that do not touch it keep working:

| Route | During an outage |
|---|---|
| `POST /v1/jobs/plan` with `Source` | 200, priced from the last usage snapshot |
| `POST /v1/jobs/plan` with `Name` | 503, the job is read from the store |
| `POST /v1/jobs/run`, `POST /v1/job/{name}/dispatch` | 503, the dispatch record cannot be written |
| All other job, dispatch and execution routes | 503 |
| `GET /v1/nodes` | 200, held in server memory |
| `GET /v1/health` | 503 with `Store: "unreachable"` |

A server started with `-dev` keeps everything in memory and never answers 503.
What happens to dispatches already running: [dispatch](dispatch.md).

## Routes

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/jobs` | [Register a job](#register-a-job) |
| `GET` | `/v1/jobs` | [List jobs](#list-jobs) |
| `GET` | `/v1/job/{name}` | [Read a job](#read-a-job) |
| `DELETE` | `/v1/job/{name}` | [Stop a job](#stop-a-job) |
| `POST` | `/v1/job/{name}/dispatch` | [Dispatch a registered job](#dispatch-a-registered-job) |
| `POST` | `/v1/jobs/run` | [Run a job file](#run-a-job-file) |
| `GET` | `/v1/dispatch/{id}` | [Read a dispatch](#read-a-dispatch) |
| `POST` | `/v1/jobs/plan` | [Plan a job](#plan-a-job) |
| `GET` | `/v1/execution/{id}` | [Read an execution](#read-an-execution) |
| `GET` | `/v1/execution/{id}/logs` | [Read execution logs](#read-execution-logs) |
| `DELETE` | `/v1/execution/{id}` | [Cancel an execution](#cancel-an-execution) |
| `GET` | `/v1/nodes` | [List nodes](#list-nodes) |
| `GET` | `/v1/health` | [Read health](#read-health) |

## Jobs

### Register a job

`POST /v1/jobs`

Parses and validates the job file in full, then stores it as a new version when
its source differs from the current version. Declared `${meta.<key>}` references
are validated as placeholders; values arrive at dispatch. Registering a stopped
job always creates a new version and un-stops it.

| Parameter | In | Description |
|---|---|---|
| `namespace` | query | See [namespaces](#namespaces) |

Request, `RegisterRequest`:

```json
{
  "Source": "job \"build\" {\n  task \"compile\" {\n    driver = \"container\"\n    ...\n  }\n}\n"
}
```

| Field | Description |
|---|---|
| `Source` | The job file's HCL, one job |

Response, `RegisterResponse`:

```json
{
  "Namespace": "default",
  "Name": "build",
  "Version": 3,
  "Changed": true
}
```

| Field | Description |
|---|---|
| `Namespace` | Namespace the job was registered in |
| `Name` | Job name from the file |
| `Version` | Current version after the call |
| `Changed` | `false` when the source matched the current version and nothing was stored |

Errors: 400 (invalid job, namespace), 503.

### List jobs

`GET /v1/jobs`

Every job registered in the namespace, stopped ones included, sorted by name.
An empty namespace returns `[]`.

| Parameter | In | Description |
|---|---|---|
| `namespace` | query | Default `default` |

Response, `[]Job`:

```json
[
  {
    "Namespace": "default",
    "Name": "build",
    "Version": 3,
    "Stopped": false,
    "Updated": "2026-09-28T14:03:11.52Z"
  }
]
```

| Field | Description |
|---|---|
| `Namespace` | Namespace |
| `Name` | Job name |
| `Version` | Current version |
| `Stopped` | `true` after [stop](#stop-a-job) until registered again |
| `Updated` | Last register or stop |

Errors: 400 (namespace), 503.

### Read a job

`GET /v1/job/{name}`

The job, every version newest first, and its 20 most recent executions newest
first, across all dispatches.

| Parameter | In | Description |
|---|---|---|
| `name` | path | Job name |
| `namespace` | query | Default `default` |

Response, `JobStatus`: the `Job` fields inline, plus `Versions` and `Executions`.

```json
{
  "Namespace": "default",
  "Name": "build",
  "Version": 3,
  "Stopped": false,
  "Updated": "2026-09-28T14:03:11.52Z",
  "Versions": [
    { "Version": 3, "Registered": "2026-09-28T14:03:11.52Z" },
    { "Version": 2, "Registered": "2026-09-27T09:12:40.1Z" }
  ],
  "Executions": [ { "ID": "0192a4c8-8a01-7c55-a1b2-c3d4e5f60718", "...": "see Execution" } ]
}
```

| Field | Description |
|---|---|
| `Versions[].Version` | Version number |
| `Versions[].Registered` | When it was stored. Sources are not returned |
| `Executions` | [`Execution`](#execution-object) objects, newest first, at most 20 |

Errors: 400 (namespace), 404, 503.

### Stop a job

`DELETE /v1/job/{name}`

Marks the job stopped so it can no longer be dispatched. Versions and
executions are kept; running dispatches are not cancelled. Registering the job
again revives it. Stopping a stopped job succeeds and changes `Updated`.

| Parameter | In | Description |
|---|---|---|
| `name` | path | Job name |
| `namespace` | query | Default `default` |

Response: the [`Job`](#list-jobs) after stopping, `Stopped: true`.

Errors: 400 (namespace), 404, 503.

```bash
# Register, list, read, stop
jq -Rs '{Source: .}' build.hcl | curl -s -X POST --data-binary @- http://127.0.0.1:4747/v1/jobs
curl -s 'http://127.0.0.1:4747/v1/jobs?namespace=default'
curl -s http://127.0.0.1:4747/v1/job/build
curl -s -X DELETE http://127.0.0.1:4747/v1/job/build
```

## Dispatches

A dispatch is one run of a job: every task, each with one or more executions
(attempts). Both dispatch routes return as soon as the run is recorded; the run
continues in the server after the response.

**Following a run:** poll `GET /v1/dispatch/{id}` until `State` is not
`running`. Executions appear as each task is reserved and recorded, so a
multi-task job's later tasks show up as earlier ones finish. Read an execution's
output from [`/logs`](#read-execution-logs) once `HasResult` is `true`. The CLI
polls once a second. There is no streaming or blocking query.

**Ownership:** the server that accepted the request runs the dispatch and holds
a lease on it. The run is not tied to the HTTP request: a client disconnecting
does not stop it. If the server stops, the lease lapses and another server
resumes the run; see [dispatch](dispatch.md).

### Dispatch a registered job

`POST /v1/job/{name}/dispatch`

Starts a run of the job's current version with the given metadata. Metadata is
checked against the job's `meta_required` and `meta_optional` before anything
is recorded.

| Parameter | In | Description |
|---|---|---|
| `name` | path | Job name |
| `namespace` | query | Default `default` |

Request, `DispatchRequest`. A body is required; `{}` dispatches with no
metadata.

```json
{
  "Meta": { "commit": "4f1c2ab", "branch": "main" }
}
```

| Field | Description |
|---|---|
| `Meta` | String map of dispatch metadata. `null` or omitted means none |

Response, `DispatchResponse`:

```json
{
  "Namespace": "default",
  "Job": "build",
  "JobVersion": 3,
  "DispatchID": "0192a4c8-7f3e-7b21-9c4d-5e6f7a8b9c0d"
}
```

| Field | Description |
|---|---|
| `Namespace` | Namespace the run belongs to |
| `Job` | Job name |
| `JobVersion` | Version dispatched |
| `DispatchID` | ID to read with `GET /v1/dispatch/{id}` |

Errors:

| Status | Cause |
|---|---|
| 400 | Empty or malformed body; metadata the job does not permit (`job "build" is not parameterized, so dispatch takes no metadata`, undeclared key, missing required key); job invalid with this metadata (with `Diagnostics`); namespace |
| 404 | Job not registered in the namespace |
| 409 | Job is stopped |
| 503 | Store unreachable; no run was started |

Quota and capacity refusals are not errors on this route. They happen after the
response, and the dispatch ends `unanswered` with the reason in `Error`.

### Run a job file

`POST /v1/jobs/run`

Runs a job file once without registering it. The dispatch records
`JobVersion: 0`.

| Parameter | In | Description |
|---|---|---|
| `namespace` | query | See [namespaces](#namespaces) |

Request, `RunRequest`:

```json
{
  "Source": "job \"build\" {\n  ...\n}\n",
  "Meta": { "commit": "4f1c2ab" }
}
```

| Field | Description |
|---|---|
| `Source` | Job file HCL |
| `Meta` | Metadata the file's `${meta.<key>}` references resolve against |

Response: `DispatchResponse`, as for [dispatch](#dispatch-a-registered-job).

Errors: 400 (invalid job or metadata, with `Diagnostics`; namespace), 503.

### Read a dispatch

`GET /v1/dispatch/{id}`

The run's record and every execution it has created so far, oldest first.

| Parameter | In | Description |
|---|---|---|
| `id` | path | `DispatchID` from a dispatch or run |

Response, `Dispatch`:

```json
{
  "DispatchID": "0192a4c8-7f3e-7b21-9c4d-5e6f7a8b9c0d",
  "Namespace": "default",
  "Job": "build",
  "JobVersion": 3,
  "State": "succeeded",
  "Error": "",
  "Created": "2026-09-28T14:05:00.001Z",
  "Ended": "2026-09-28T14:05:42.87Z",
  "Executions": [ { "ID": "0192a4c8-8a01-7c55-a1b2-c3d4e5f60718", "...": "see Execution" } ]
}
```

| Field | Description |
|---|---|
| `State` | See table below |
| `Error` | Why an `unanswered` run got no answer; empty otherwise |
| `Created` | When the run was recorded |
| `Ended` | `null` while `running` |
| `Executions` | [`Execution`](#execution-object) objects, oldest first. Empty when the run was refused before any task was submitted |

| `State` | Meaning |
|---|---|
| `running` | In progress |
| `succeeded` | Every task ran and passed |
| `failed` | A task ran and failed |
| `unanswered` | No answer: refused on quota or capacity, every admitted provider failed, or the run was interrupted or cancelled |

Errors: 400 (malformed ID), 404, 503.

```bash
# Run a file, dispatch a registered job, follow the run
jq -Rs '{Source: ., Meta: {commit: "4f1c2ab"}}' build.hcl \
  | curl -s -X POST --data-binary @- http://127.0.0.1:4747/v1/jobs/run
curl -s -X POST -d '{"Meta":{"commit":"4f1c2ab"}}' http://127.0.0.1:4747/v1/job/build/dispatch
id=0192a4c8-7f3e-7b21-9c4d-5e6f7a8b9c0d
until [ "$(curl -s http://127.0.0.1:4747/v1/dispatch/$id | jq -r .State)" != running ]; do sleep 1; done
```

## Plans

### Plan a job

`POST /v1/jobs/plan`

Runs admission and ranking for every task and reports where each would run.
Nothing is dispatched or reserved. Usage comes from the server's last usage
snapshot. Admission and ranking rules: [scheduling](scheduling.md).

| Parameter | In | Description |
|---|---|---|
| `namespace` | query | See [namespaces](#namespaces). Namespace shares apply to admission |

Request, `PlanRequest`. Exactly one of `Source` and `Name`; both is a 400.
Neither is planned as an empty `Source` and fails with
`A job file must declare a job.`

```json
{
  "Name": "build",
  "Meta": { "commit": "4f1c2ab" }
}
```

| Field | Description |
|---|---|
| `Source` | Job file HCL to plan |
| `Name` | Registered job to plan, at its current version |
| `Meta` | Dispatch metadata; with `Name` it is checked as a dispatch would check it |

Response, `Plan`:

```json
{
  "Tasks": [
    {
      "Job": "build",
      "Task": "compile",
      "Driver": "container",
      "Candidates": [
        {
          "Provider": "cloud-run",
          "Tier": 0,
          "Score": 82,
          "Scores": [ { "Name": "headroom", "Value": 0.82 } ],
          "Observed": "2026-09-28T14:00:03Z"
        }
      ],
      "Rejections": [
        {
          "Provider": "lambda",
          "Reason": "driver-unsupported",
          "Detail": "The task uses the container driver and this provider offers function.",
          "Also": null
        }
      ],
      "Selected": "cloud-run",
      "EstimatedCost": 0,
      "Retryable": false,
      "Tiers": "strict"
    }
  ]
}
```

| Field | Description |
|---|---|
| `Tasks` | One entry per task, in job order |
| `Driver` | The task's driver |
| `Candidates` | Admitted providers, best first. `[]` when none |
| `Candidates[].Tier` | The provider's configured [tier](configuration.md#tiers) |
| `Candidates[].Score` | Overall score as a percentage, 0 to 100 |
| `Candidates[].Scores` | Each scorer's contribution in `[0,1]`: `headroom`, `affinity`, `tier` (see [scorers](scheduling.md#scorers)) |
| `Candidates[].Observed` | When the provider's capabilities were last observed |
| `Rejections` | Providers that cannot run the task. `[]` when none |
| `Rejections[].Reason` | First rule failed; a [reason code](scheduling.md#reason-codes) |
| `Rejections[].Detail` | Human-readable explanation |
| `Rejections[].Also` | Other reason codes the provider also failed; `null` when none |
| `Selected` | Provider the task would run on; empty when no candidate |
| `EstimatedCost` | Estimated cost of `Selected`; always `0` |
| `Retryable` | With no `Selected`: `true` when the refusal may clear on its own (quota recovering, provider healthy again) |
| `Tiers` | Tier mode the candidates were ordered under: `strict` or `weighted` (see [tiers](scheduling.md#tiers)) |

Errors: 400 (invalid job or metadata, both `Source` and `Name`, namespace), 404
and 409 (with `Name`: not registered, stopped), 503 (with `Name`).

```bash
jq -Rs '{Source: .}' build.hcl | curl -s -X POST --data-binary @- http://127.0.0.1:4747/v1/jobs/plan
curl -s -X POST -d '{"Name":"build"}' 'http://127.0.0.1:4747/v1/jobs/plan?namespace=ci'
```

## Executions

### Execution object

One attempt of one task. Returned by the execution routes and inside `Dispatch`
and `JobStatus`.

```json
{
  "ID": "0192a4c8-8a01-7c55-a1b2-c3d4e5f60718",
  "Namespace": "default",
  "Job": "build",
  "JobVersion": 3,
  "DispatchID": "0192a4c8-7f3e-7b21-9c4d-5e6f7a8b9c0d",
  "Task": "compile",
  "Provider": "cloud-run",
  "Attempt": 1,
  "PreviousID": "",
  "State": "succeeded",
  "ProviderID": "build-compile-x7k2p",
  "Failure": "",
  "Created": "2026-09-28T14:05:00.113Z",
  "Started": "2026-09-28T14:05:04.9Z",
  "Ended": "2026-09-28T14:05:42.8Z",
  "Updated": "2026-09-28T14:05:42.86Z",
  "HasResult": true,
  "ExitCode": 0,
  "Duration": 37900000000,
  "Billed": null,
  "LogsTruncated": false
}
```

| Field | Description |
|---|---|
| `ID` | Execution ID, minted before submission |
| `DispatchID` | Owning dispatch |
| `Task` | Task name |
| `Provider` | Provider name from configuration |
| `Attempt` | 1 for the first attempt; increments on retry or reroute |
| `PreviousID` | The attempt this one follows; empty on attempt 1 |
| `State` | See table below |
| `ProviderID` | The provider's own identifier; empty until submitted |
| `Failure` | `infrastructure` or `internal` when a provider operation failed; empty otherwise. A non-zero exit is not a failure here |
| `Created` | When the ID was minted, read from the UUIDv7 |
| `Started`, `Ended` | `null` until the provider reports them |
| `Updated` | Last change to the record |
| `HasResult` | `true` once the result fields below are set |
| `ExitCode` | Process exit code; `null` without a result or for a driver with no process |
| `Duration` | Run time in nanoseconds |
| `Billed` | What the platform reported it charged, `{"CPU": <millicores>, "Memory": <MiB>, "Duration": <ns>}`; `null` when the platform does not report it |
| `LogsTruncated` | Stored output was cut to its last 64 KiB |

| `State` | Terminal | Meaning |
|---|---|---|
| `pending` | no | Recorded, not yet submitted |
| `submitted` | no | Sent to the provider |
| `accepted` | no | Provider accepted it, not yet running |
| `running` | no | Running |
| `succeeded` | yes | Exited successfully |
| `failed` | yes | Exited unsuccessfully or the provider failed |
| `cancelled` | yes | Cancelled |
| `lost` | no | Stopped answering after submission; resolved by the server within 24 hours, to its real outcome or `failed` |

### Read an execution

`GET /v1/execution/{id}`

| Parameter | In | Description |
|---|---|---|
| `id` | path | Execution ID |

Response: [`Execution`](#execution-object).

Errors: 400 (malformed ID), 404, 503.

### Read execution logs

`GET /v1/execution/{id}/logs`

The execution's stored output as `text/plain`: the last 64 KiB of combined
output, stored once the execution has a result. Before then the body is empty
with status 200. Errors use the JSON error body.

| Parameter | In | Description |
|---|---|---|
| `id` | path | Execution ID |

Errors: 400 (malformed ID), 404, 503.

### Cancel an execution

`DELETE /v1/execution/{id}`

| Execution | Effect |
|---|---|
| Terminal (`succeeded`, `failed`, `cancelled`) | Nothing; returns the record |
| Its dispatch is running in this server | Cancels the whole dispatch; the run stops its tasks and ends `unanswered`. Returns the record as read before cancelling |
| Otherwise (dispatch running in another server, or not running) | Asks the provider to stop it, with a 30 s timeout, then records it `cancelled` unless another writer recorded it first. Returns the updated record |

| Parameter | In | Description |
|---|---|---|
| `id` | path | Execution ID |

Response: [`Execution`](#execution-object). Read it again, or its dispatch, to
see the final state.

Errors:

| Status | Cause |
|---|---|
| 400 | Malformed ID |
| 404 | No such execution |
| 500 | `execution <id> ran on "<provider>", which is no longer configured`; `cancelling <id> on <provider>: <error>` |
| 503 | Store unreachable |

```bash
curl -s http://127.0.0.1:4747/v1/execution/0192a4c8-8a01-7c55-a1b2-c3d4e5f60718
curl -s http://127.0.0.1:4747/v1/execution/0192a4c8-8a01-7c55-a1b2-c3d4e5f60718/logs
curl -s -X DELETE http://127.0.0.1:4747/v1/execution/0192a4c8-8a01-7c55-a1b2-c3d4e5f60718
```

## Nodes

### List nodes

`GET /v1/nodes`

Every [agent](agent.md) node connected to this server now, sorted by name.
Held in server memory: each server lists only the agents connected to it, and a
node disappears when its connection closes. An empty list is `[]`.

Response, `[]NodeListStub`:

```json
[
  {
    "Name": "rack-01",
    "Pool": "homelab",
    "Address": "10.0.0.21:51844",
    "Labels": { "zone": "basement" },
    "Architecture": "amd64",
    "CPU": 4000,
    "Memory": 8192,
    "UsedCPU": 1000,
    "UsedMemory": 2048,
    "Runtimes": [ "io.containerd.runc.v2" ],
    "Version": "0.4.0",
    "Executions": 2,
    "Connected": "2026-09-28T09:00:12Z"
  }
]
```

| Field | Description |
|---|---|
| `Name` | Node name the agent registered |
| `Pool` | Pool the node joins |
| `Address` | Remote address of the agent's connection |
| `Labels` | Labels the agent reported |
| `Architecture` | CPU architecture |
| `CPU` | Millicores the agent may use |
| `Memory` | MiB the agent may use |
| `UsedCPU`, `UsedMemory` | Declared by the node's running workloads, as last reported |
| `Runtimes` | Container runtimes the agent offers |
| `Version` | Agent version |
| `Executions` | Workloads running on the node, as last reported |
| `Connected` | When the current connection was established |

Errors: none specific; the route does not touch the store.

```bash
curl -s http://127.0.0.1:4747/v1/nodes
```

## Health

### Read health

`GET /v1/health`

Whether the server can take new work. Pings the store with a 5 s timeout.

| Status | Store |
|---|---|
| 200 | Store answered |
| 503 | Store ping failed for any reason; the body is still `Health`, not the error body |

Response, `Health`:

```json
{
  "Store": "unreachable",
  "StoreError": "the store is unreachable: failed to connect to `user=vagabond database=vagabond`: 10.0.0.5:5432 (10.0.0.5): dial error: dial tcp 10.0.0.5:5432: connect: connection refused",
  "UsageRefreshed": "2026-09-28T14:02:30Z",
  "Running": 1
}
```

| Field | Description |
|---|---|
| `Store` | `ok` or `unreachable` |
| `StoreError` | Why the ping failed; omitted when `ok` |
| `UsageRefreshed` | When the usage snapshot plans are priced from was last read. Ages while the store is unreachable |
| `Running` | Dispatches running in this server |

Use the status code for load balancer checks: a 503 server still answers plans
from its snapshot and finishes running dispatches, but refuses new ones.

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:4747/v1/health
```

## TLS

A `tls` block in the `server` block serves the API over HTTPS:

```hcl
server {
  bind = "0.0.0.0:4747"

  tls {
    cert = "/etc/vagabond/server.crt" # PEM certificate, chain included
    key  = "/etc/vagabond/server.key" # PEM private key
  }
}
```

- Both attributes are required. Minimum protocol TLS 1.2.
- The pair is loaded once at startup. A missing or mismatched file stops the
  server with `loading the server certificate: <error>`.
- No client certificates are requested or verified. TLS does not add
  authentication.
- The block covers the API listener only, not `agent_bind`.
- Without the block the API is plain HTTP.

```bash
curl -s --cacert /etc/vagabond/ca.crt https://vagabond.internal:4747/v1/health
```

## Go client

`internal/api` has a client with one method per route, taking and returning the
types above.

```go
client, err := api.NewClient("vagabond.internal:4747", nil)
if err != nil {
	return err
}

started, err := client.Dispatch(ctx, "default", "build", map[string]string{"commit": "4f1c2ab"})
if failure, ok := errors.AsType[*api.ResponseError](err); ok {
	fmt.Println(failure.Status, failure.Body.Error, len(failure.Body.Diagnostics))
}
```

**Constructor:** `NewClient(address string, httpClient *http.Client) (*Client, error)`.

| Input | Behaviour |
|---|---|
| `address` empty | `api.DefaultAddress`, `http://127.0.0.1:4747` |
| `address` without `://` | Prefixed with `http://`; pass `https://...` for TLS |
| `address` unparseable | Error `server address "<addr>": <error>` |
| `httpClient` nil | A pooled client of its own (`go-cleanhttp`), not `http.DefaultClient` |
| `httpClient` set | Used as given; pass one with a `tls.Config` for a private CA |

The client does not read environment variables. The CLI passes `-address`,
defaulting to `$VAGABOND_ADDR`; see [CLI](cli.md).

**Methods:**

| Method | Route |
|---|---|
| `Register(ctx, namespace, source)` | `POST /v1/jobs` |
| `Jobs(ctx, namespace)` | `GET /v1/jobs` |
| `JobStatus(ctx, namespace, name)` | `GET /v1/job/{name}` |
| `StopJob(ctx, namespace, name)` | `DELETE /v1/job/{name}` |
| `Dispatch(ctx, namespace, name, meta)` | `POST /v1/job/{name}/dispatch` |
| `Run(ctx, namespace, source, meta)` | `POST /v1/jobs/run` |
| `Plan(ctx, namespace, PlanRequest)` | `POST /v1/jobs/plan` |
| `DispatchStatus(ctx, id)` | `GET /v1/dispatch/{id}` |
| `Execution(ctx, id)` | `GET /v1/execution/{id}` |
| `Logs(ctx, id)` | `GET /v1/execution/{id}/logs`, as `[]byte` |
| `Cancel(ctx, id)` | `DELETE /v1/execution/{id}` |
| `Nodes(ctx)` | `GET /v1/nodes` |
| `Health(ctx)` | `GET /v1/health` |

An empty `namespace` sends no `?namespace=`, so the server applies its default.

**Errors:**

| Case | Error |
|---|---|
| Non-2xx response | `*api.ResponseError` with `Status` and the decoded `Body` (`api.Error`, diagnostics included). `Error()` returns `Body.Error`. A body that is not the JSON error, such as a router 404 or 405, gives `Body.Error` as the status line, e.g. `405 Method Not Allowed` |
| Connection failure | `contacting the server at <address>: <error>` |
| Undecodable 2xx body | `<METHOD> <path>: decoding the answer: <error>` |
| `Health` with 503 | Returns the decoded `*Health` and a `*ResponseError` whose `Body.Error` is `StoreError` |
