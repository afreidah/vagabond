# HTTP API

`vagabond server` serves JSON under `/v1`. Field names are the JSON keys as
written in `internal/api`; durations are nanoseconds.

Every route takes `?namespace=`, defaulting to `default`. An undeclared
namespace is a 400.

## Routes

| Route | Body | Returns |
|---|---|---|
| `POST /v1/jobs` | `{"Source": "<hcl>"}` | `RegisterResponse`: namespace, name, version, whether it changed |
| `GET /v1/jobs` | | `[]Job` |
| `GET /v1/job/{name}` | | `JobStatus`: job, versions newest first, 20 most recent executions |
| `DELETE /v1/job/{name}` | | `Job`, stopped |
| `POST /v1/job/{name}/dispatch` | `{"Meta": {...}}` | `DispatchResponse` |
| `POST /v1/jobs/run` | `{"Source": "<hcl>", "Meta": {...}}` | `DispatchResponse`; nothing registered |
| `POST /v1/jobs/plan` | `{"Source"}` or `{"Name"}`, plus `Meta` | `Plan` |
| `GET /v1/dispatch/{id}` | | `Dispatch`: the run's state and its executions so far, oldest first |
| `GET /v1/execution/{id}` | | `Execution` |
| `GET /v1/execution/{id}/logs` | | Stored output, `text/plain` |
| `DELETE /v1/execution/{id}` | | `Execution` |

## Dispatch

- Dispatch and run return once the run is recorded. The run continues in the
  server; read it from `/v1/dispatch/{id}`.
- A dispatch's `State`:

  | State | Meaning |
  |---|---|
  | `running` | Still going |
  | `succeeded` | Every task ran and passed |
  | `failed` | A task ran and failed |
  | `unanswered` | No answer: refused on quota or capacity, every provider failed, or interrupted; `Error` says which |

- An unanswered run may have no executions at all, when it was refused before
  any task was submitted.
- A task's execution exists once it has been reserved and recorded, so a
  multi-task job's later tasks appear as earlier ones finish.
- Cancelling an execution whose dispatch is running in this server stops the
  whole dispatch. Otherwise the provider is asked to stop it and the record is
  marked `cancelled`.
- Stopping the server leaves running executions recorded as they were.

## Status codes

| Status | When |
|---|---|
| 200 | Success |
| 400 | Malformed body, invalid job (with `Diagnostics`), refused metadata, undeclared namespace |
| 404 | No such job or execution |
| 409 | Stopped job dispatched; execution changed while being updated |
| 500 | Anything else; logged by the server |

Errors are `{"Error": "...", "Diagnostics": [...]}`.

## Not yet

- Authentication (#64)
- Live log streaming and blocking queries (chunk 7)
