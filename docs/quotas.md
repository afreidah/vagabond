---
title: "Quotas"
seoTitle: "Quotas: Usage Pools, Namespace Shares, Ledger"
description: "Quota pools, meters and periods, namespace shares, free_quota_percent, and how the ledger reserves, settles and reaps usage."
weight: 320
---

A quota pool is a usage budget on one provider, counted in a unit the provider
meters and reset on a calendar period. Admission rejects a task that does not
fit, ranking prefers the provider with the most allowance left, and dispatch
reserves the charge in the ledger before submitting. The ledger is Vagabond's
own account; no provider is asked for its remaining allowance.

![An execution in namespace ci on aws-lambda charges the provider's pools and the namespace's share of them. It is admitted only if every pool it touches has room in both layers, unless the job pays (max_cost_usd above 0). Dispatch reserves the charge in one statement and settles it with what the execution used.](assets/quota.svg)

Interactive version, with notes on each step:
[Quota accounting](https://vagabond.munchbox.cc/diagrams/quotas/).

## Declaring pools

Pools are declared on a provider, and optionally as a namespace's share of a
provider. Full attribute reference:
[`pool` block](configuration.md#pool-block) and
[`namespace` block](configuration.md#namespace-block).

```hcl
provider "aws-lambda" {
  type = "lambda"
  # ...

  # Every pool needs meter, limit and period; there are no defaults.
  pool "requests" {
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}

# The ci namespace may use at most 100000 GB-seconds of aws-lambda's 400000.
namespace "ci" {
  quota "aws-lambda" {
    pool "compute" {
      meter  = "gb_seconds"
      limit  = 100000
      period = "monthly"
    }
  }
}
```

- A provider with no pools is unlimited. Capabilities still gate it.
- Pools are additive. An execution charges every pool whose meter it touches
  and must fit all of them, so a daily pool can sit inside a monthly one.
- Pool names are unique per provider (and per namespace share); the name is
  what usage is counted under. Renaming a pool starts its count from zero.
- A limit is the most you are willing to use, not a published figure. A limit
  above a provider's free tier permits paid usage up to that limit.

Configuration errors, reported when the server loads the file:

| Condition | Diagnostic |
|---|---|
| Pool name repeated | `Provider "<p>" pool "<name>" is declared twice. ...` |
| Unknown meter | `... meters "<m>", which is not something Vagabond counts. Known meters are ...` |
| Unknown period | `... resets "<p>". Known periods are ...` |
| `limit <= 0` | `... has a limit of <n>. A pool exists to refuse an execution, so its limit must be positive.` |
| Limit overflows the counter | `quota pool "<name>" limit of <n> <unit> is too large to count` |
| Namespace share of an unconfigured provider | `Unknown provider in namespace quota` |
| Two shares of one provider in one namespace | `Duplicate namespace quota` |
| Namespace declared twice | `Duplicate namespace` |

## Meters

A charge is computed from what the task declares, not from what it uses, so
`job plan` can price a task without running it. The inputs are
`resources.cpu` (millicores), `resources.memory` (MiB) and `timeout`.

| Meter | Unit | Charge per execution | Counted in |
|---|---|---|---|
| `executions` | executions | 1 | executions |
| `gb_seconds` | GB-seconds | `memory / 1024 * timeout` | MiB-milliseconds |
| `cpu_seconds` | vCPU-seconds | `cpu / 1000 * timeout` | millicore-milliseconds |
| `seconds` | seconds | `timeout` | milliseconds |

- GB means GiB: 1024 MiB is one GB-second per second.
- Counters are integers in the base unit. A limit is multiplied into base units
  when the configuration loads (for `gb_seconds`, by 1,024,000).
- The timeout is rounded up to a whole millisecond.
- A task with no `timeout` charges 0 to `gb_seconds`, `cpu_seconds` and
  `seconds` pools. A pool charged 0 is skipped: it cannot reject the task and
  does not lower `free_quota_percent`. The execution is still charged its real
  duration at settlement.
- A task with no `resources.memory` charges 0 to `gb_seconds`; one with no
  `resources.cpu` charges 0 to `cpu_seconds`.
- `executions` always charges 1.

Declare a pool for every quantity the platform meters. A provider with a
memory pool and no CPU pool keeps admitting work after its CPU allowance is
gone. Each provider page under `providers/` lists the meters that apply.

## Periods

| Period | Counter key | Resets |
|---|---|---|
| `daily` | `YYYY-MM-DD` | 00:00 UTC |
| `monthly` | `YYYY-MM` | 00:00 UTC on the first of the month |

Usage is stored under the period key current when the charge is reserved. A new
period is a key with nothing counted yet, so reset needs no job. Old periods
stay in the store and are no longer read.

A reservation settles into the period it was reserved in. A run that starts
before midnight UTC and ends after it is charged to the earlier day.

## Namespace shares

Every execution runs in a namespace (`default` unless the job names one). Pools
come in two layers:

| Layer | Declared in | Counted under |
|---|---|---|
| Provider total | `provider` -> `pool` | the provider |
| Namespace share | `namespace` -> `quota "<provider>"` -> `pool` | the namespace, on that provider |

- An execution charges both layers and must fit both.
- The provider total is checked first; the first pool without room is the one
  reported.
- A namespace with no share of a provider is limited only by the provider's
  pools.
- Shares may add up past the provider's total. The total still binds, so one
  namespace can be refused by the total while its own share has room.
- A share's pools need not mirror the provider's. A share may meter something
  the provider does not.
- A job naming a namespace that is not declared is refused.

## Headroom

`provider.free_quota_percent` is the provider's remaining allowance for one
specific task:

```
pool free %  = 100                                  if nothing is counted
             = floor((limit - used) * 100 / limit)  otherwise, 0 when used >= limit
free_quota_percent = min(pool free % over every pool the task charges,
                         in the provider's pools and the namespace's share)
```

- Only pools the task charges count, so the same provider reports different
  values to tasks that meter differently.
- A provider with no pools, or none the task charges, reports 100.
- The value is floored: a pool with a sliver left reports 0.
- `used` includes open reservations.

It is published as an attribute, so a job can constrain or set an affinity on
it, and it drives the `headroom` scorer, `clamp(free_quota_percent / 100)`,
under the default `free-first` strategy. See
[Scheduling](scheduling.md#ranking).

## Admission check

The `quota` check, last in [admission](scheduling.md#admission), rejects a
provider with `quota-exhausted` when the task's charge does not fit a pool:
`used + charge > limit` for any pool the task charges. Exactly reaching the
limit fits.

```
Pool "compute" has 1000 of 200000 GB-seconds left and this task needs 1800, and the job will not pay for capacity beyond it.
Namespace "ci"'s pool "compute" has 100 of 100000 GB-seconds left and this task needs 150, and the job will not pay for capacity beyond it.
```

Amounts print in the pool's unit with Go's `%g`, so one million or more prints
as `1e+06`.

- The check applies only when `max_cost_usd` is unset or 0. A job with
  `max_cost_usd > 0` passes it at any usage.
- It reads the ledger snapshot, not the store. See
  [Usage snapshot](#usage-snapshot).
- `quota-exhausted` is transient: the plan is marked retryable, since the
  period reset can admit the same job.

## Ledger

The ledger records charges against every pool. With a `store` block it lives in
PostgreSQL or CockroachDB (tables `quota_usage` and `quota_reservations`; see
[Database](database.md)). Without one, or under `server -dev`, it is held in
memory under one mutex with the same rules, and is empty at every start.
`vagabond provider status` shows what each pool has used this period.

An execution's life in the ledger:

| Step | When | Effect |
|---|---|---|
| Reserve | Before each submission attempt | Declared charge held against every pool of both layers |
| Settle | When the execution's runner has collected it, however it ended | Reservation replaced with what the execution cost, or dropped when nothing ran |
| Reap | Server start and every 5 minutes | Reservations older than one hour with no execution record dropped |

A provider with no pools in either layer is never reserved, settled or reaped.

### Reservation

Dispatch reserves each attempt under its execution ID before calling the
provider. The reservation holds one row per pool of both layers, including
pools charged 0, so settlement knows each pool's period.

The headroom test and the insert are one SQL statement: rows are inserted only
if, for every pool with a non-zero charge,
`settled + reserved + charge <= limit`. Zero rows inserted means refused. The
statement runs in a serializable transaction. A serialization failure re-runs
the whole transaction, up to 10 attempts in all, after a random wait whose
ceiling starts at 10 ms and doubles to 500 ms. The tenth failure fails the
reservation (`gave up after 10 serialization failures`) and stops the task. Two
servers reserving against the same pool cannot both see room.

A refused reservation reads usage in the same transaction and reports the pool,
provider total first:

```
pool "compute" of provider "aws-lambda" has 1000 of 400000 GB-seconds left; this task needs 1800
pool "compute" of namespace "ci" on provider "aws-lambda" has 100 of 100000 GB-seconds left; this task needs 150
```

Dispatch then moves to the next ranked provider without spending a retry
attempt. If every candidate refuses, the task fails with
`no provider can run this task`. See [Dispatch](dispatch.md).

A job with `max_cost_usd > 0` is never refused here, as admission never
rejects it for quota. Its charges are written with no ceiling, so it runs
past a spent allowance, and what it used still counts: later jobs that will
not pay see the pool spent until the period resets.

### Settlement

Every execution is settled by its runner once it has ended and been collected,
however it ended: its reservation rows are deleted and what it cost is added
to settled usage, in one statement, in the periods the reservation was made
in. The first row that applies decides the cost:

| Execution | Charged as |
|---|---|
| Nothing ran: `Submit` refused as `internal`, or the provider reports the execution unknown after a failed `Submit`, a cancel or a restart, and it was never seen running | Nothing; the reservation is dropped |
| Result with a billed amount | Billed memory * billed duration, billed CPU (0 when not reported) * billed duration |
| Result, no billed amount | Declared CPU and memory * the result's duration |
| Never answered: `failed` after 24 hours `lost`, or `Status` failed when asked what a cancelled run or failed `Submit` ran, and it was never seen running | Declared CPU and memory * declared timeout: the amount reserved |
| Anything else: cancelled, a failed `Submit` the provider reports times for, unknown to the provider after it was seen running, `Result` unreadable, provider no longer configured | Declared CPU and memory * the time from the record's `StartedAt` to its `EndedAt`, the provider's own after a cancel or failed `Submit`; 0 when it never started |

`executions` pools are charged 1 on every row but the first, and `seconds`
pools the duration the row uses.

- **AWS Lambda** reports a billed amount when the invocation's `REPORT` log
  line carries both `Billed Duration` and `Memory Size`. Memory is the
  function's configured size, not the task's declaration, and no CPU is
  reported, so `cpu_seconds` pools are charged 0 for that execution. See
  [Lambda](providers/lambda.md).
- Every other provider's result settles at declared resources * the result's
  duration.

Edge cases:

- The actual charge may exceed the reservation. Settlement is not checked
  against the limit, so settled usage can pass it; later tasks are refused.
- A `Submit` the provider refused with an `internal` error, such as an HTTP
  400, ran nothing. Its reservation is dropped and charges nothing, the
  execution count included.
- A `Submit` that failed as `infrastructure` or with an unclassified error may
  have left a run. The runner cancels it and asks `Status` what ran
  ([Failed submissions](dispatch.md#failed-submissions)).
- A `lost` execution holds its reservation until its runner resolves it
  ([Lost executions](dispatch.md#lost-executions)).
- A settle write that fails is made again until the store takes it. A runner
  restored after a claim settles only a reservation still held.
- Settling an execution with no reservation rows does nothing, so settling
  twice charges once.

### Reaper

The reaper drops reservations no runner will settle: those whose execution
was never recorded, because the process died, or the store refused the
`pending` record, between reserving and recording.

| Setting | Value |
|---|---|
| Runs | On `vagabond server` start, then every 5 minutes |
| Considers | Reservations created more than 1 hour ago |
| Locking | Rows claimed `FOR UPDATE SKIP LOCKED` in a serializable transaction, so two servers never resolve the same execution |

| Execution record | Outcome |
|---|---|
| None | Dropped; nothing charged |
| Exists | Kept; its runner settles it |
| Cannot be read | Kept, looked at again on the next pass |

No provider is called. The server logs `resolved abandoned quota reservations`
with a count when any were dropped, and `resolving abandoned quota
reservations` at warn when a pass fails. The reaper runs only in `vagabond
server`. All periodic tasks:
[Background services](background-services.md#reservation-reaper).

### Usage snapshot

Admission and ranking read an in-memory snapshot of settled plus reserved usage
for each pool's current period, not the store.

| Event | Snapshot |
|---|---|
| Server start | Read from the store; the server fails to start if the read fails |
| Every 15 seconds | Re-read; a failed read keeps the previous snapshot and logs a warning |
| Before each task is dispatched | Re-read; a failed read keeps the previous snapshot |
| `job plan` | Not re-read; the plan uses the current snapshot |

Reserving, settling and reaping do not update the snapshot. A plan can
therefore be up to 15 seconds behind every charge, this server's included, or
older while the store is unreachable. The error runs both ways: a stale
snapshot can admit a task that dispatch then refuses, or reject with
`quota-exhausted` a task that would fit, once a reservation it counted has
settled lower or been reaped. The reservation is what enforces the limit.
`GET /v1/health` reports when the snapshot was last read as `UsageRefreshed`;
it stops advancing while the store is unreachable.

## Worked example

Configuration as in [Declaring pools](#declaring-pools). A function task in
namespace `ci`:

```hcl
task "resize" {
  driver  = "function"
  timeout = "5m"

  resources {
    memory = 512
  }
}
```

**Charge:**

| Pool | Formula | Charge | Base units |
|---|---|---|---|
| `requests` | 1 | 1 execution | 1 |
| `compute` (total and share) | 512 / 1024 * 300 s | 150 GB-seconds | 512 * 300000 = 153,600,000 |

**Usage** this month:

| Layer | Pool | Limit | Used | Fits 1 / 150 more | Free % |
|---|---|---|---|---|---|
| `aws-lambda` total | `requests` | 1000000 | 42000 | yes | floor(958000 * 100 / 1000000) = 95 |
| `aws-lambda` total | `compute` | 400000 | 310000 | yes (310150 <= 400000) | floor(90000 * 100 / 400000) = 22 |
| `ci` share | `compute` | 100000 | 99900 | no (100050 > 100000) | floor(100 * 100 / 100000) = 0 |

**In namespace `ci`**, `free_quota_percent` is min(95, 22, 0) = 0, headroom
0.00, and admission rejects `aws-lambda`:

```
aws-lambda  rejected  quota-exhausted  Namespace "ci"'s pool "compute" has 100 of 100000 GB-seconds left and this task needs 150, and the job will not pay for capacity beyond it.
```

**In namespace `default`**, which has no share, `free_quota_percent` is
min(95, 22) = 22, headroom 0.22, and the task is admitted.

**Dispatch** in `default` reserves 1 against `requests` and 150 GB-seconds
against `compute`. Total `compute` usage reads 310150 while the reservation is
open.

**Settlement.** Lambda's `REPORT` line states `Billed Duration: 42300 ms` and
`Memory Size: 1024 MB`. The reservation is replaced by:

| Pool | Charge |
|---|---|
| `requests` | 1 |
| `compute` | 1024 / 1024 * 42.3 s = 42.3 GB-seconds |

Total `compute` usage settles at 310042.3. Had the platform reported nothing,
the charge would have been the declared 512 MiB times the measured duration.
