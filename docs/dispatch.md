---
title: "Dispatch"
seoTitle: "Dispatch: Execution Records, Leases and Retries"
description: "How the server runs a job: execution records and states, rerouting, retries, polling, output, release, cancellation, leases and store outages."
weight: 330
---

Dispatch runs what [admission and ranking](scheduling.md) selected: reserve
quota, record the attempt, submit to the provider, poll until the execution is
terminal, fetch the result, settle quota and release what the provider left
behind. An infrastructure failure moves the task to the next ranked provider
when the task's retry policy allows it.

The server does all dispatching. `POST /v1/jobs/run` and
`POST /v1/job/{name}/dispatch` record the dispatch, start it in a background
goroutine and return its ID; the run is read back from `GET /v1/dispatch/{id}`
(see [API](api.md)).

![A job file is validated, admitted against every provider and ranked. Dispatch then reserves quota, submits to the selected provider, watches the execution, settles what it used and releases what it left behind. An infrastructure failure moves the task to the next ranked provider.](assets/task-path.svg)

## Dispatch records

One dispatch record per run of a job. It is written before anything is
reserved or submitted, so a run that never reached a provider still has a
record saying why.

| Field | Description |
|---|---|
| `DispatchID` | UUIDv7, minted by the server |
| `Namespace`, `Job`, `JobVersion` | What ran. `JobVersion` is `0` for a job file run without registering |
| `State` | See below |
| `Error` | Why an `unanswered` run got no answer. Empty otherwise |
| `Created`, `Ended` | `Ended` is unset while running |
| `Executions` | Every attempt of every task, oldest first |

The stored record also holds the task count, the lease owner and the lease
expiry ([Leases](#leases)).

| State | Meaning |
|---|---|
| `running` | In progress, leased to a server |
| `succeeded` | Every task ran and exited successfully |
| `failed` | A task ran and failed: a non-zero exit code |
| `unanswered` | No verdict: no candidates, every attempt failed, cancelled, interrupted, or the store refused a write |

A record that cannot be created stops the run before it starts: the request
fails and nothing is reserved. Finishing and renewing a dispatch apply only
while it is `running` and held by the caller, so the ending is written once, by
the current owner.

## Execution records

One execution record per attempt: each task on each provider it was submitted
to. Records live in Postgres when a [`store`](configuration.md#store-block) is
configured, in memory with `vagabond server -dev`.

| Field | Description |
|---|---|
| `ID` | UUIDv7 minted per attempt. Also the submission idempotency key |
| `DispatchID`, `Task` | The run and task the attempt belongs to |
| `Provider`, `ProviderID` | The provider, and its own identifier once `Submit` returned |
| `Attempt` | 1 for the first submission of a task, counting only submissions |
| `PreviousID` | The attempt before this one on the same task. Empty on the first |
| `State` | See [Execution states](#execution-states) |
| `Failure` | `infrastructure` or `internal` when the attempt ended without an answer |
| `Created`, `Started`, `Ended`, `Updated` | `Created` is read from the UUIDv7. `Started` is set on reaching `running`, `Ended` on a terminal state |
| `HasResult`, `ExitCode`, `Duration`, `Billed`, `LogsTruncated` | The result, once fetched |

The record also keeps the task's declared CPU and memory, so a result can be
charged without the job ([Quotas](quotas.md)).

## Execution states

| State | Terminal | Meaning |
|---|---|---|
| `pending` | no | Recorded and reserved; `Submit` not yet called |
| `submitted` | no | `Submit` returned |
| `accepted` | no | The provider queued the work but has not started it |
| `running` | no | The work started |
| `succeeded` | yes | The provider reported success |
| `failed` | yes | The provider reported failure, or the attempt failed before submission |
| `cancelled` | yes | Stopped by Vagabond or by the provider |
| `lost` | no | Submitted, then a status call failed. The work may still be running |

Legal transitions:

| From | To |
|---|---|
| `pending` | `submitted`, `failed`, `cancelled` |
| `submitted` | `accepted`, `running`, `succeeded`, `failed`, `cancelled`, `lost` |
| `accepted` | `running`, `succeeded`, `failed`, `cancelled`, `lost` |
| `running` | `succeeded`, `failed`, `cancelled`, `lost` |
| `lost` | `succeeded`, `failed`, `cancelled` |
| `succeeded`, `failed`, `cancelled` | none |

- `accepted` and `running` are optional: a synchronous invocation goes from
  `submitted` straight to a terminal state.
- The machine is acyclic. A retry is a new record, never a state.
- There is no timed-out state. A task that exceeds its timeout is `failed`.
- A transition the table refuses is not written. A `lost` execution that is
  later polled as `running` stays `lost` until it reaches a terminal state.

`Submit` may report only `accepted`, `running`, `succeeded` or `failed`, and a
terminal state only with a result. A submission that breaks this is an
`internal` failure.

## Recording

| Written | When |
|---|---|
| `pending` | After the quota reservation, before `Submit`. A failed write stops the task |
| `submitted`, then the state `Submit` reported | `Submit` returned |
| Each polled state | On change only |
| Terminal state with the result | The provider reported a terminal state and `Result` was called |
| `failed` with `Failure` | `Submit` failed |
| `lost` with `Failure` | A `Status` call failed after submission |
| `cancelled` | The dispatch was cancelled while polling |

- Every write after `pending` is best effort, on its own context with a 10s
  timeout. The run has already happened; a failed write does not fail it.
- An update applies only if the stored record is still in the state it was read
  in, otherwise it fails as stale. A write lost to a store outage therefore
  makes every later update of that record stale, and the record stays at the
  last state written.
- A terminal state is written even when `Result` failed; the record then has
  no result.
- Stored output is capped. See [Output](#output).

## Job execution order

- Tasks run one at a time, in declaration order.
- The job stops at the first task that exits non-zero or gets no answer.
  Later tasks are not run.
- Before each task the quota snapshot is re-read, so admission sees what
  earlier tasks charged. A failed read keeps the last snapshot; the
  reservation is what enforces the limit.
- Each task is admitted and ranked again at the moment it runs.

| Outcome | Dispatch state |
|---|---|
| Every task ended in state `succeeded` with a result | `succeeded` |
| A task ended in state `failed` with a result, with or without an exit code | `failed` |
| A task returned an error | `unanswered`, with `Error` set |

## Failure classes and rerouting

Rule: an exit code is an answer and ends the task; only a provider that failed
to produce an answer is worth another provider, since a workload failure would
repeat anywhere.

| Failure | Behaviour |
|---|---|
| Non-zero exit code | Ends the task and the job. Never retried |
| `infrastructure` | Next ranked provider, if the retry budget allows |
| `internal` | Stops the task immediately |
| Unclassified error | Treated as not reroutable |
| Provider reports `cancelled` | `infrastructure`: the provider gave no verdict |
| Quota reservation refused | Provider skipped. Not a submission, no record, no budget spent |
| Ranked provider not registered | Stops the task with `ErrNoProvider` |

A provider that fails a `Status` call leaves the execution `lost`. The run is
not cancelled on that provider, and its quota reservation stands until the
[reaper](background-services.md#reservation-reaper) resolves it.

## Retry budget

Read from the task's [`retry` block](job-specification.md#retry-block):

```hcl
task "build" {
  # ...
  retry {
    attempts = 3     # total submissions, not retries after the first
    reroute  = true  # without this, a task gets one provider

    backoff {
      initial = "10s" # wait before the second submission
      max     = "1m"  # ceiling for later waits
    }
  }
}
```

| Configuration | Submissions |
|---|---|
| No `retry` block | 1 |
| `attempts = 3`, `reroute = true` | Up to 3, to the three highest-ranked providers whose reservation succeeds |
| `attempts = 3`, no `reroute` | 1 |
| `attempts = 0` or unset | 1 |
| `attempts` above the candidate count | One per candidate |

- Each candidate is tried at most once. There is no retry on the same
  provider.
- Only submissions count against the budget. A provider whose quota
  reservation is refused is skipped for free.
- Each attempt gets a fresh execution ID, recorded with `PreviousID` pointing
  at the one before. Reusing the ID would ask the provider to resume the run
  that just failed.
- A negative `attempts`, and a `max` below `initial`, are rejected by
  validation.

| Error | When |
|---|---|
| `ErrNoCandidates` | Admission admitted nothing, or every admitted provider refused the reservation |
| `ErrExhausted` | At least one submission was made, none answered, and the budget or the candidates ran out |

## Backoff and polling

Two schedules, both doubling to a ceiling.

| Schedule | Starts | Ceiling | Applies to |
|---|---|---|---|
| Retry backoff | `backoff.initial`, default 5s | `backoff.max`, default 30s | Wait before each submission after the first |
| Status poll | 2s | 15s | Wait before each `Status` call on a running execution |

With the defaults, the second submission waits 5s, the third 10s, the fourth
20s, then 30s each. Polls happen 2s, 6s, 14s and 29s after submission, then
every 15s.

- The backoff wait happens after the attempt is reserved and recorded
  `pending`, before `Submit`.
- A `duration` that does not parse at dispatch falls back to the default.
- A synchronous provider, whose `Submit` returns the result, is not polled.

## Output

- A record keeps the last 64 KiB (65536 bytes) of output. `LogsTruncated` is
  `true` when anything before that was cut.
- Output is fetched with the result, once the execution is terminal. Read it
  from `GET /v1/execution/{id}/logs` (plain text, empty until there is a
  result) or with `vagabond execution logs` ([CLI](cli.md)).
- The server does not stream output while an execution runs. `vagabond job
  run` prints each execution's output once its result is recorded.
- Agent nodes cap a workload's output at the same 64 KiB tail
  ([agent](agent.md)).

## Release

After the result is fetched, dispatch calls `Release` on providers that
implement `plugin.Releaser`:

| Provider | Release |
|---|---|
| Cloud Run | Deletes the execution's Job resource |
| `pool` | Deletes the workload's task, container, snapshot and log file from the node |

- Called after `Result`, because releasing may delete what the result reads.
  A synchronous submission is released as soon as `Submit` returns.
- Own context, 30s timeout. A release that succeeds, or finds nothing left,
  sets the record's `released_at`. A failed release does not fail the
  execution.
- Not called by dispatch when `Result` failed, when the execution was
  cancelled by Vagabond, when the provider stopped answering, or when the
  server died mid-run. The [provider release](background-services.md#provider-release)
  loop releases those once the provider reports them over and their quota is
  settled, and retries any release that failed.

## Cancellation

`DELETE /v1/execution/{id}` stops an execution. Interrupting `vagabond job
run` sends it for every execution of the run that has not ended.

| Execution | Effect | Response |
|---|---|---|
| Terminal | Nothing | The record |
| Its dispatch is running on this server | The whole dispatch is cancelled | The record as read before cancelling |
| Otherwise | The provider is asked to cancel it (30s timeout), then the record is set `cancelled` | The record, `cancelled` |

When a dispatch is cancelled:

- The execution being polled is cancelled on its provider on a separate
  context with a 30s timeout, so it stops billing. The call's error is
  ignored.
- The execution is recorded `cancelled`, and no later task runs.
- The dispatch ends `unanswered`, `Error` carrying the context error.
- Cancelled during a retry backoff, the next attempt's record stays `pending`
  and its reservation is left to the reaper.

Cancelling an execution whose dispatch another server is running stops it at
the provider. That server's next poll sees `cancelled`, an `infrastructure`
failure, and may reroute the task if its budget allows. A provider that is no
longer configured, or a failed provider `Cancel`, fails the request with 500.

## Leases

A running dispatch is leased to the server running it. The owner is
`server:<hostname>:<pid>`.

| Setting | Value |
|---|---|
| Lease duration | 60s from each write |
| Renewal | Every 20s, first one 20s after the dispatch starts |
| Claim of lapsed leases | At startup, then every 30s |

- A renewal that fails for any reason but a stale lease is ignored; two can
  fail before the lease lapses.
- A renewal that finds the dispatch ended or held by another owner marks the
  lease lost. The old owner finishes the task in flight, starts no further
  task (`ErrLeaseLost`), and does not write the ending.
- A claim takes every `running` dispatch whose lease expired, sets this
  server as owner and leases it for 60s, in one statement, so two servers
  never claim the same dispatch.
- A server never claims a dispatch it is running itself, even if its lease
  lapsed during a store outage.
- Renewal runs on its own context, so a cancelled dispatch keeps its lease
  until its ending is recorded.

Shutting a server down drains HTTP requests for up to 10s and leaves running
dispatches as they are. Their executions continue on the providers; the leases
lapse and another server, or this one restarted, resumes them.

## Resuming

A claimed dispatch is resumed in the background. Its job file and metadata are
not stored, so only executions already created are finished; tasks not yet
reached are not run.

For each execution that is not terminal:

| Condition | Result |
|---|---|
| Provider no longer configured | `failed` if `pending`, else `lost`; `Failure` is `internal` |
| `Status` call fails, including an unknown execution | `failed` if `pending`, else `lost`; `Failure` is `infrastructure` |
| `Status` answers | Moved from `pending` to `submitted` if needed, polled to a terminal state, result recorded, quota settled, released |

The reservation of a `failed` or `lost` execution is left to the reaper.

The dispatch then ends by its last execution:

| Last execution | Dispatch | `Error` |
|---|---|---|
| None | `unanswered` | `interrupted before any task was submitted` |
| Result with success, every task has a record | `succeeded` | |
| Result with success, tasks remain | `unanswered` | `interrupted after <n> of <m> tasks` |
| Result with non-zero exit | `failed` | |
| Anything else | `unanswered` | `interrupted; execution <id> ended <state>` |

If the executions cannot be read, the resume stops and the dispatch stays
`running`; its lease lapses and the next claim tries again.

Server log lines: `dispatch resumed` (with `previous_owner`), then
`resumed dispatch finished` (with `state` and `reason`), or `resuming
dispatch` at warn with the error.

## Store outage

The server fails closed on writes while Postgres is unreachable. A connection
that cannot be opened (5s connect timeout unless the DSN sets one), breaks, or
times out is reported as `the store is unreachable`.

| Operation | During the outage |
|---|---|
| `POST /v1/jobs/run` | 503. Nothing is recorded, reserved or submitted |
| `POST /v1/job/{name}/dispatch`, reads of jobs, dispatches and executions | 500: reading the store fails |
| `POST /v1/jobs/plan` with a job file | Answers from the last usage snapshot |
| `POST /v1/jobs/plan` of a registered job | 500: reading the job fails |
| `GET /v1/health` | 503, `Store` is `unreachable`, `StoreError` set, `UsageRefreshed` ages |
| Execution being polled | Carries on. Polling needs no store; record writes fail silently |
| Next attempt or next task | Stops the dispatch: reserving quota on a provider with pools, or creating the `pending` record, fails |
| Dispatch ending | Not written; the dispatch stays `running` in the store |
| Lease renewal | Fails; the lease lapses after 60s |
| Background loops | Log a warning each tick and retry on the next ([Background Services](background-services.md)) |

Recovery once the store answers:

- A dispatch whose ending was lost still reads `running` with a lapsed lease.
  The next claim takes it (the server that ran it no longer skips it once the
  run returned), asks the provider again, records the result, settles quota
  and writes the ending.
- A settle that failed leaves the reservation standing. The reaper settles it
  once it is over an hour old.
- An execution record left behind by a lost update is not repaired if its
  dispatch's ending was written.

## Errors

| Error | Message | Meaning |
|---|---|---|
| `ErrNoCandidates` | `no provider can run this task` | Nothing admitted, or every reservation refused. The outcome carries the rejections |
| `ErrExhausted` | `every attempt failed` | Submissions were made, every one failed with `infrastructure`, and nothing was left to try |
| `ErrNoProvider` | `provider is not registered` | A ranked provider has no plugin in the registry |
| `ErrLeaseLost` | `dispatch was taken over by another process` | Another server claimed the dispatch |

A dispatch that ends `unanswered` records the error in `Error`, prefixed with
the task: `task "build": every attempt failed: <last failure>`. The server logs
`dispatch finished` at info, or `dispatch finished without an answer` at warn
with the error.
