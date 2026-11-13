---
title: "Dispatch"
seoTitle: "Dispatch: Execution Records, Leases and Retries"
description: "How the server runs a job: execution records and states, rerouting, retries, polling, output, release, cancellation, leases and store outages."
weight: 330
---

Dispatch runs what [admission and ranking](scheduling.md) selected: reserve
quota and record the attempt, then hand it to an
[execution runner](#execution-runners), which submits to the provider, polls
until the execution is terminal, collects the result and output, settles quota
and releases what the provider left behind. An infrastructure failure moves the
task to the next ranked provider when the task's retry policy allows it.

The server does all dispatching. `POST /v1/jobs/run` and
`POST /v1/job/{name}/dispatch` record the dispatch, start it in a background
goroutine and return its ID; the run is read back from `GET /v1/dispatch/{id}`
(see [API](api.md)).

![A job file is validated, admitted against every provider and ranked. Dispatch then reserves quota, submits to the selected provider, watches the execution, settles what it used and releases what it left behind. An infrastructure failure moves the task to the next ranked provider.](assets/task-path.svg)

Interactive version, with notes on each stage:
[Task path](https://vagabond.munchbox.cc/diagrams/task-path/).

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

The stored record also holds the task count, the lease owner, epoch and expiry,
the time of a requested cancel, and when the dispatch was done
([Leases](#leases)).

| State | Meaning |
|---|---|
| `running` | In progress, leased to a server |
| `succeeded` | Every task ran and exited successfully |
| `failed` | A task ran and failed: a non-zero exit code |
| `unanswered` | No verdict: no candidates, every attempt failed, cancelled, interrupted, or the store refused a write |

A record that cannot be created stops the run before it starts: the request
fails and nothing is reserved. The ending is written only while the dispatch is
`running` and held by the caller under its current epoch, so it is written
once, by the current owner. The dispatch is done once every execution under it
is released; until then its lease is held.

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
| `HasResult`, `ExitCode`, `Duration`, `Billed`, `Reason` | The result, once fetched |
| `LogsStart`, `LogsTruncated` | Where the stored output begins, and whether the provider gave all of it. See [Output](#output) |

The stored record also keeps the task's declared CPU, memory and timeout, so
any ending can be charged without the job ([Quotas](quotas.md#settlement)),
and when what the provider left behind was released ([Release](#release)).

## Execution states

Every transition, with what records it:
[Execution states](https://vagabond.munchbox.cc/diagrams/execution-states/).

| State | Terminal | Meaning |
|---|---|---|
| `pending` | no | Recorded and reserved; `Submit` not yet called |
| `submitted` | no | `Submit` returned |
| `accepted` | no | The provider queued the work but has not started it |
| `running` | no | The work started |
| `succeeded` | yes | The provider reported success |
| `failed` | yes | The provider reported failure, the submission failed, the provider no longer knows the execution, a `lost` execution was given up on, or its provider is no longer configured |
| `cancelled` | yes | Stopped by Vagabond or by the provider |
| `lost` | no | Submitted, then a `Status` call failed, or `Status` reported `lost` (a `pool` whose node has been gone less than 5 minutes). The work may still be running; its runner keeps polling it ([Lost executions](#lost-executions)) |

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

## Execution runners

Each attempt is driven by one runner: a goroutine in the server that holds the
dispatch's lease and is the only writer of the execution's record. It takes
the execution through five steps, each leaving a mark on the record:

| Step | Does | Leaves |
|---|---|---|
| Submit | `Submit`, or for a [restored](#resuming) runner one `Status` call | A state past `pending` |
| Watch | Polls `Status` until a terminal state | Each state before the terminal one |
| Collect | `Result`, then the output ([Output](#output)), recorded in one write | The terminal state, the result, `logs_at` |
| Settle | Replaces the quota reservation with what the run cost ([Quotas](quotas.md#settlement)) | No `quota_reservations` rows |
| Release | `Release`, on a provider that implements it ([Release](#release)) | `released_at` |

- The task gets the runner's first answer: the terminal state once its result
  and output are recorded, the execution going `lost`, or a failed
  submission. Rerouting starts then; settling and releasing go on in the
  background.
- Settle and release run for every ending: a result, a cancel, a failed
  submission, a `lost` execution once it is resolved.
- A runner restored after a claim starts at the first step its record does not
  show done ([Resuming](#resuming)).
- The dispatch's lease is held until every runner under it has released its
  execution ([Leases](#leases)).

## Recording

| Written | When |
|---|---|
| `pending` | After the quota reservation, before `Submit`. A failed write stops the task |
| `submitted`, then the state `Submit` reported unless terminal | `Submit` returned |
| Each polled state | On change only, the terminal state excepted |
| The terminal state, the result and `logs_at` | `Result` read and the output captured, in one write, so a client that sees the execution end finds its output |
| `failed` with `Failure` | `Submit` failed |
| `lost` with `Failure` | A `Status` call failed after submission |
| `lost`, no `Failure` | `Status` reported `lost`. Written on change; polling continues |
| `cancelled` | The dispatch was cancelled while polling; written with the output, as any terminal state |
| `released_at` | `Release` succeeded, the provider reported nothing left, or there was nothing to release |

- Every write is checked against the dispatch's lease: it lands only while the
  dispatch is held by this server under the same epoch and is not done. A
  write refused that way means another server claimed the dispatch; the runner
  stops, with no further provider call and no settle, and leaves the rest to
  the claimer.
- A write that fails for any other reason, such as an unreachable store, is
  made again, waiting 250ms doubling to 10s, until it lands or the lease is
  lost. Each try has a 10s timeout and logs warn `store write failed,
  retrying` with `error`. The runner does not move past a write until it
  lands. Quota writes (settle, drop) and the dispatch's ending and close are
  retried the same way.
- Each write carries the whole record, so writing it again under the same
  lease changes nothing.
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

A task that returns a result, with any exit code, ends there: a workload
failure would repeat on any provider. Only an attempt that gets no answer from
the provider is retried or rerouted.

| Failure | Behaviour |
|---|---|
| Provider reports `failed` with a result (a non-zero exit, a function error) | Ends the task and the job. Never retried |
| `infrastructure` | Next ranked provider, if the retry budget allows |
| `internal` | Stops the task immediately. When `Submit` fails this way the provider ran nothing, so the quota reservation is dropped at zero |
| Unclassified error | Treated as not reroutable, and recorded with `Failure` `internal`. From `Submit` the run may exist; see [Failed submissions](#failed-submissions) |
| Credential rejected (HTTP 401 or 403) | The credential is resolved again and the same call is made once more on the same provider. A second rejection is `internal`. See [`credentials` block](configuration.md#credentials-block) |
| `Result` fails | Classified like any error. `infrastructure` reroutes; the terminal state is recorded without a result and charged for the time it was seen running |
| `Status`, `Result`, `Cancel` or `Release` takes over 30s | `infrastructure`: the call is given up on. `Submit` has no timeout, since a synchronous provider answers it only once the task is done |
| Driver config fails to evaluate | Stops the task before admission: `building the request: <diagnostics>` |
| Provider reports `cancelled` | `infrastructure`: the provider gave no verdict |
| Quota reservation refused | Provider skipped. Not a submission, no record, no budget spent |
| Ranked provider not registered | Stops the task with `ErrNoProvider` |

A `Status` call that fails records the execution `lost` and ends the attempt
at once, so the task can reroute. The run is not cancelled on that provider;
its runner keeps polling it ([Lost executions](#lost-executions)). A `Status`
that reports the execution unknown records it `failed` instead, unless the
provider classifies that `infrastructure`, as Cloud Run does for a listing
that may lag the run. A `lost` that `Status` reports does not end the
attempt: polling continues.

### Failed submissions

A `Submit` that returns an error records the execution `failed` with its
`Failure` class and nothing to collect, and ends the attempt. What follows
depends on whether the run may exist:

| `Submit` error | Then | Charged |
|---|---|---|
| `internal` | Nothing: Vagabond's request was refused or never sent | Nothing; the reservation is dropped |
| `infrastructure`, or unclassified | `Cancel` (30s timeout, error ignored), then one `Status` call | Unknown execution: nothing. Times reported: the declared shape over them. `Status` fails: the declared shape over the declared timeout, the amount reserved |

The execution is then released as any other.

### Lost executions

The runner of a `lost` execution keeps polling `Status` on its schedule, up to
every 15s. A `lost` execution leaves only for a terminal state:

| Provider answer | Record |
|---|---|
| A terminal state | That state, its result read and its output captured, charged like any other ending; `Failure` is cleared |
| Not terminal | Stays `lost` |
| Unknown execution, not classified `infrastructure` | `failed` at once, charged for the time it was seen running |
| `Status` fails, under 24 hours after it became `lost` | Stays `lost` |
| `Status` fails, 24 hours or more after it became `lost` (`LostGracePeriod`) | `failed`, charged as reserved: the declared shape over the declared timeout |

- `Failure` keeps the class that made it `lost` until a terminal state clears
  it.
- A `lost` that `Status` reports (a `pool` node gone under 5 minutes) does not
  end the attempt; the first `Status` failure after it does.
- The dispatch's verdict comes from the task's answer; what a `lost`
  execution does afterwards does not change it.
- The dispatch stays open, its lease renewed, until the execution is resolved,
  settled and released.

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

Four schedules, each doubling to a ceiling.

| Schedule | Starts | Ceiling | Applies to |
|---|---|---|---|
| Retry backoff | `backoff.initial`, default 5s | `backoff.max`, default 30s | Wait before each submission after the first |
| Status poll | 2s | 15s | Wait before each `Status` call on a running or `lost` execution |
| Store write retry | 250ms | 10s | Wait before a record, quota or dispatch write is made again |
| Release retry | 250ms | 10s | Wait before a failed `Release` is made again |

With the defaults, the second submission waits 5s, the third 10s, the fourth
20s, then 30s each. Polls happen 2s, 6s, 14s and 29s after submission, then
every 15s.

- The backoff wait happens before the next attempt is reserved and recorded
  `pending`, once per submission: a provider whose reservation is then
  refused does not make the next one wait again.
- A `duration` that does not parse at dispatch falls back to the default.
- A synchronous provider, whose `Submit` returns the result, is not polled.

## Output

Once an execution ends, its runner reads the result, captures the output into
the store, and records both in one write; releasing comes after settling. In
that order: recording the result is what tells a waiting client to read the
output, and releasing may destroy it.

| Provider | Where the output comes from |
|---|---|
| Implements `plugin.LogReader` (Cloud Run, Code Engine, `pool`) | `ReadLogs`, which writes the whole output |
| Any other (Lambda) | `Result.Logs`, the output that came with the answer |

- The output is written in 256 KiB chunks as it is read, so a capture holds one
  chunk whatever the task printed ([Database](database.md#execution_logs)).
- Past the server's `max_log_mib` (16 MiB by default) the oldest chunks are
  dropped, so the end is kept. `LogsStart` is then the first byte still stored.
  Offsets count from the first byte the task printed either way.
- A capture runs on its own context with a 5 minute timeout, through the same
  credential retry as every provider call. A retry starts the output over at
  chunk 0, so nothing is stored twice.
- A capture never fails the execution. A `ReadLogs` error keeps what arrived
  and sets `LogsTruncated`, as does `Result.LogsTruncated` on an answer that
  carried only part of the output. A store that refuses the chunks also sets
  it.
- Every ending is captured: a result, a run cancelled by Vagabond, one whose
  result could not be read, one `failed` after 24 hours `lost`. `Result` is
  not called for a cancelled run or one that failed without an answer. A
  failed submission has nothing to capture. A runner restored after a claim
  captures an ended execution whose `logs_at` is null.
- The server deletes stored output `log_retention` (7 days by default) after
  the execution ends ([Background services](background-services.md#output-retention)).
- Read it from `GET /v1/execution/{id}/logs` (plain text, empty until it is
  captured) or with `vagabond execution logs` ([CLI](cli.md)).
- A runner never streams. While an execution runs, a client streams its
  output with `GET /v1/execution/{id}/logs?follow=true`, which the server
  serves from the provider's `LogStreamer` ([API](api.md#read-execution-logs)).
  `vagabond job run` and `vagabond execution logs -f` follow it that way.

## Release

The last step of every execution, however it ended. The runner calls `Release`
on providers that implement `plugin.Releaser`:

| Provider | Release |
|---|---|
| Cloud Run | Deletes the execution's Job resource |
| Code Engine | Deletes the execution's job run, and its pod with it |
| `pool` | Deletes the workload's task, container, snapshot and log file from the node |

- Called after `Result`, the capture and the settle, because releasing may
  delete what `Result` and the capture read.
- Each call has a 30s timeout. A release that fails is made again, waiting
  250ms doubling to 10s, until it succeeds or the provider reports the
  execution unknown; each failure logs warn `releasing provider leftovers`
  with `execution` and `error`. Either sets the record's `released_at`.
- A provider without `Release` is marked released with no call, as is an
  execution whose provider is no longer configured.
- A failed release does not fail the execution or change its charge. It holds
  the dispatch open: a `pool` node that stays offline keeps its dispatch's
  lease renewed until the node returns.

## Cancellation

`DELETE /v1/execution/{id}` stops an execution. Interrupting `vagabond job
run` sends it for every execution of the run that has not ended.

| Execution | Effect | Response |
|---|---|---|
| Terminal | Nothing | The record |
| Its dispatch is held by this server, started or resumed here | The whole dispatch is cancelled at once | The record as read before cancelling |
| Otherwise | A cancel is requested on the dispatch (`cancel_requested_at`; the first request's time is kept). The server holding it sees the request at its next lease renewal, within 20s, and cancels the dispatch. A dispatch no server holds is cancelled by the server that claims it, right after the claim | The record as read, unchanged |

Only the server holding the dispatch calls the provider. A request that cannot
be recorded fails: 503 while the store is unreachable.

When a dispatch is cancelled:

- Each runner still watching its execution first writes its record, which
  confirms it still holds the lease, then cancels the execution on its
  provider on a separate context with a 30s timeout, so it stops billing. The
  call's error is ignored.
- The execution is recorded `cancelled`, and one `Status` call gives its
  `StartedAt` and `EndedAt`, so it is charged for what it ran. An execution
  the provider no longer knows, as on Cloud Run and Code Engine, whose
  `Cancel` deletes it, is charged for the time it was seen running, or
  nothing if it was never seen running. A `Status` that fails charges the
  time it was seen running, or the amount reserved if it was never seen
  running.
- Its output is still captured, and it is settled and released as any other.
- No later task runs. The dispatch ends `unanswered`, `Error` carrying the
  context error, or `the dispatch was cancelled` for a cancel requested
  through the lease.
- Cancelled during a retry backoff, the next attempt is never prepared:
  nothing is reserved or recorded for it.

## Leases

A dispatch is leased to the server running it from the moment it is recorded
until every execution under it is released. The lease is an owner,
`server:<hostname>:<pid>`, and an epoch that counts claims: 1 when the
dispatch starts, one more at each claim.

| Setting | Value |
|---|---|
| Lease duration | 60s from each renewal |
| Renewal | Every 20s, first one 20s after the dispatch starts |
| Claim of lapsed leases | At startup, then every 30s |
| Held until | Every execution is released; the dispatch is then done (`done_at`) |

- A renewal that fails for any reason but a stale lease is ignored; two can
  fail before the lease lapses.
- A renewal that finds the dispatch done, or held under another owner or
  epoch, marks the lease lost. Every runner under it stops with no further
  provider call, the dispatch starts no further task (`ErrLeaseLost`), and the
  ending is not written.
- Every execution and output write is checked against the lease: owner,
  epoch, and the dispatch not done. A server cut off from the store past its
  lease, whose dispatch another server claimed, has its first write refused
  when the store returns, and its runners stop there.
- A renewal also returns any cancel requested since ([Cancellation](#cancellation)).
- A claim takes every dispatch not yet done whose lease expired, whether
  still running or ended with executions still to release. It sets this
  server as owner, increments the epoch and leases it for 60s, in one
  statement, so two servers never claim the same dispatch.
- A server never claims a dispatch it is running itself, even if its lease
  lapsed during a store outage.
- Renewal runs on its own context, so a cancelled dispatch keeps its lease
  until every execution is released.

Shutting a server down drains HTTP requests for up to 10s and leaves running
dispatches as they are. Their executions continue on the providers; the leases
lapse and another server, or this one restarted, resumes them.

## Resuming

A claimed dispatch is resumed in the background. Its job file and metadata are
not stored, so only executions already created are finished; tasks not yet
reached are not run. A cancel requested before the claim is acted on at once.

A runner is restored for every execution that has not finished all its steps:
terminal, `logs_at` set and `released_at` set. It starts at the first step its
record does not show done:

| Record | Restored runner |
|---|---|
| `pending` | Not submitted again, since the task is not stored. One `Status` call: an answer moves it to `submitted` and it is watched; any error records it `failed`, `Failure` `infrastructure`, then charges it as a [failed submission](#failed-submissions) (an unknown execution charges nothing) |
| `submitted`, `accepted`, `running` | Watched to a terminal state, then collected |
| `lost` | Answers `lost` for the verdict at once, then watched as any [lost execution](#lost-executions) |
| Terminal, no `logs_at` | Result and output collected |
| Terminal, `logs_at` set | Settled, then released |
| Provider no longer configured | `failed`, `Failure` `internal`, if not terminal; charged for the time it was seen running; marked released with no provider call |

- Every restored runner settles only a reservation still held, so a run is
  charged once.
- A dispatch whose ending was already written waits for its runners, then is
  closed with the verdict it had.
- A running dispatch waits for each runner's first answer, then ends by its
  last execution:

| Last execution | Dispatch | `Error` |
|---|---|---|
| None | `unanswered` | `interrupted before any task was submitted` |
| `succeeded` with a result and no `Failure`, every task has a record | `succeeded` | |
| `succeeded` with a result and no `Failure`, tasks remain | `unanswered` | `interrupted after <n> of <m> tasks` |
| `failed` with a result and no `Failure` | `failed` | |
| Anything else | `unanswered` | `interrupted; execution <id> ended <state>` |

If the executions cannot be read, the resume stops and the dispatch stays
open; its lease lapses and the next claim tries again.

Server log lines: `dispatch resumed` (with `epoch`, the claim's), then
`resumed dispatch finished` (with `state` and `reason`) once every execution
is released, or `resuming dispatch` at warn with the error.

## Store outage

The server fails closed on writes while Postgres is unreachable. A connection
that cannot be opened (5s connect timeout unless the DSN sets one), breaks, or
times out is reported as `the store is unreachable`.

| Operation | During the outage |
|---|---|
| `POST /v1/jobs/run` | 503. Nothing is recorded, reserved or submitted |
| `POST /v1/job/{name}/dispatch`, reads of jobs, dispatches and executions | 503 |
| `POST /v1/jobs/plan` with a job file | Answers from the last usage snapshot |
| `POST /v1/jobs/plan` of a registered job | 503: the job is read from the store |
| `GET /v1/health` | 503, `Store` is `unreachable`, `StoreError` set, `UsageRefreshed` ages |
| Execution being polled | Carries on until its next record write, which is retried until the store answers; the runner waits on it ([Recording](#recording)) |
| Next attempt or next task | Stops the dispatch: reserving quota on a provider with pools, or creating the `pending` record, fails. A reservation whose record was never created is dropped by the [reaper](background-services.md#reservation-reaper) an hour later |
| Dispatch ending | Retried until the store answers |
| Lease renewal | Fails; the lease lapses after 60s |
| Background loops | Log a warning each tick and retry on the next ([Background Services](background-services.md)) |

Recovery once the store answers:

- Back within the 60s lease: the writes being retried land, the next renewal
  extends the lease, and the run carries on.
- Back after the lease lapsed, with no other server: this server renews its
  own lease and carries on. It never claims a dispatch it is running.
- Back after the lease lapsed, with another server on the store: that server
  may claim the dispatch first. Its restored runners continue from the
  records; this server's next write under the old lease is refused, and its
  runners stop with no further provider call.

## Errors

| Error | Message | Meaning |
|---|---|---|
| `ErrNoCandidates` | `no provider can run this task` | Nothing admitted, or every reservation refused. The outcome carries the rejections |
| `ErrExhausted` | `every attempt failed` | Submissions were made, every one failed with `infrastructure`, and nothing was left to try |
| `ErrNoProvider` | `provider is not registered` | A ranked provider has no plugin in the registry |
| `ErrLeaseLost` | `dispatch was taken over by another process` | Another server claimed the dispatch |
| `ErrCancelRequested` | `the dispatch was cancelled` | A cancel was requested through the dispatch's lease |

A dispatch that ends `unanswered` records the error in `Error`, prefixed with
the task: `task "build": every attempt failed: <last failure>`. Once every
execution is released, the server logs `dispatch finished` at info, or
`dispatch finished without an answer` at warn with the error.
