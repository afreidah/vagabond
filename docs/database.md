---
title: "Database"
seoTitle: "Database: Postgres, CockroachDB and Migrations"
description: "The PostgreSQL or CockroachDB store: connection strings, migrations, the schema, the in-memory -dev store, outages and backups."
weight: 430
---

`vagabond server` keeps registered jobs, dispatch and execution records, and
quota usage in PostgreSQL or CockroachDB. One implementation serves both over
the Postgres wire protocol, through pgx v5. Every server sharing a deployment
points at the same database.

## `store` block

```hcl
store {
  dsn = "postgres://vagabond@db.internal:5432/vagabond?sslmode=verify-full"
}
```

| Name | Type | Required | Description |
|---|---|---|---|
| `dsn` | string | yes | Connection string, parsed by `pgxpool.ParseConfig` |

The block is required unless the server runs with `-dev`. Full syntax and
diagnostics: [configuration](configuration.md#store-block).

### DSN forms

URL and `key=value` forms are both accepted:

```hcl
dsn = "postgres://vagabond@db.internal:5432/vagabond?sslmode=require"
dsn = "host=db.internal port=5432 user=vagabond dbname=vagabond sslmode=require"

# CockroachDB
dsn = "postgres://vagabond@crdb.internal:26257/vagabond?sslmode=verify-full&sslrootcert=/etc/vagabond/ca.crt"
```

| Parameter | Default here | Notes |
|---|---|---|
| `connect_timeout` | 5s when the DSN sets none | Bounds each connection attempt, so an unreachable database fails a request quickly |
| `sslmode` | libpq default (`prefer`) | Use `verify-full` across an untrusted network |
| `pool_max_conns` | pgx default: the greater of 4 and the CPU count | pgxpool parameter |
| `pool_min_conns` | 0 | pgxpool parameter |
| `pool_max_conn_lifetime` | 1h | pgxpool parameter |
| `pool_max_conn_idle_time` | 30m | pgxpool parameter |

Standard libpq environment variables fill anything the DSN leaves out
(`PGHOST`, `PGUSER`, `PGSSLMODE`, ...).

### Passwords

Keep the password out of the configuration file:

| Source | Example |
|---|---|
| `PGPASSWORD` in the server's environment | `Environment=PGPASSWORD=...` or `EnvironmentFile=` in the systemd unit |
| `~/.pgpass` of the user the server runs as, mode `0600` | `db.internal:5432:vagabond:vagabond:secret` |
| `PGPASSFILE` naming another passfile | `PGPASSFILE=/etc/vagabond/pgpass` |

A password in the DSN also works and ends up in the configuration file.

## Startup

In order, before the server listens:

| Step | Failure |
|---|---|
| Parse the DSN | `could not open the store: parse dsn: ...` |
| Connect and ping | `could not open the store: the store is unreachable: ...` |
| Apply pending migrations | `could not prepare the store: apply migrations: ...` |
| Verify the schema version | `could not prepare the store: database schema is at version N, ...` |
| Read current quota usage | `could not read quota usage: ...` |

Any failure exits with status 1. There is no retry loop at startup; run the
server under a supervisor that restarts it.

## Migrations

| | |
|---|---|
| Tool | [goose](https://github.com/pressly/goose) v3, Postgres dialect |
| Source | `internal/state/postgres/migrations/*.sql`, embedded in the binary |
| Applied | At every `vagabond server` start, pending ones only |
| Version table | `goose_db_version`, created by goose |
| Expected version | `7` in this release (`SchemaVersion`) |
| Connection | A separate `database/sql` connection opened for the migration and closed after |

After migrating, the server reads `MAX(version_id)` from `goose_db_version`
where `is_applied` is true, and requires it to equal the version it was built
for:

| Database version | Result |
|---|---|
| Equal | Starts |
| Lower | Refuses: `older than the 6 this binary expects; a migration may have failed partway` |
| Higher | Refuses: `newer than the 6 this binary expects; upgrade vagabond` |

- There is no command to migrate down. The files carry `Down` sections, but
  the binary never runs them. Rolling back a release means restoring a backup
  taken before the upgrade.
- Migrations take no lock. After an upgrade that adds migrations, start one
  server first and the rest after it has started.
- The version is checked at startup only. A server already running when a
  newer binary migrates the database keeps running.

| Version | File | Change |
|---|---|---|
| 1 | `00001_quota.sql` | `quota_usage`, `quota_reservations` |
| 2 | `00002_quota_namespaces.sql` | `namespace` on both quota tables, in their primary keys |
| 3 | `00003_executions.sql` | `executions` |
| 4 | `00004_jobs.sql` | `jobs`, `job_versions`, `executions.dispatch_id` |
| 5 | `00005_dispatches.sql` | `dispatches` |
| 6 | `00006_dispatch_leases.sql` | `dispatches.tasks`, `owner`, `lease_until`; `executions.cpu`, `memory` |
| 7 | `00007_execution_release.sql` | `executions.released_at`, backfilled for rows with a result; index `executions_unreleased` |

## Schema

No sequences, triggers or foreign keys; relationships are by ID only. Every
timestamp is `TIMESTAMPTZ`.

| Table | Primary key | Holds |
|---|---|---|
| `jobs` | `(namespace, name)` | Each registered job's current version and whether it is stopped |
| `job_versions` | `(namespace, name, version)` | Every registered version's source as written, and the fingerprint of its formatted form |
| `dispatches` | `dispatch_id` | One row per run of a job: state, error, task count, lease owner and expiry |
| `executions` | `id` | One row per attempt of one task on one provider, with its result and stored output |
| `quota_usage` | `(namespace, provider, pool, period)` | Settled usage per pool per period |
| `quota_reservations` | `(execution_id, namespace, pool)` | Usage held by executions not yet settled |
| `goose_db_version` | goose's | Applied migrations |

### `jobs` and `job_versions`

| Column | Meaning |
|---|---|
| `jobs.version` | Current version, pointing into `job_versions` |
| `jobs.stopped` | Set by `DELETE /v1/job/{name}`; cleared by the next register, which always creates a version |
| `job_versions.source` | Job file as submitted |
| `job_versions.fingerprint` | SHA-256 of the `hclwrite`-formatted source; registering an unchanged, running job creates no version |

Registering reads the current version and inserts the next in one
serializable transaction.

### `dispatches`

| Column | Meaning |
|---|---|
| `state` | `running`, `succeeded`, `failed`, `unanswered` |
| `error` | Why an `unanswered` run got no answer; `''` otherwise |
| `tasks` | Tasks the job declares |
| `owner` | Process holding the lease, `server:<host>:<pid>` |
| `lease_until` | Lease expiry; renewed every 20s to 60s ahead |
| `ended_at` | Null while running |

Index `dispatches_lease (state, lease_until)` serves the claim query. Finishing
and renewing match `state = 'running' AND owner = <caller>`, so only the lease
holder can end a run. Claiming is one `UPDATE ... RETURNING`. See
[dispatch](dispatch.md#leases).

### `executions`

| Column | Meaning |
|---|---|
| `dispatch_id` | The run it belongs to |
| `task`, `provider`, `attempt` | What ran where; attempts count from 1 |
| `previous_id` | The attempt this one rerouted from; `''` on the first |
| `state` | `pending`, `submitted`, `accepted`, `running`, `succeeded`, `failed`, `cancelled`, `lost` |
| `provider_id` | The provider's own identifier for the run |
| `failure` | Failure class that ended it without an answer; `''` otherwise |
| `cpu`, `memory` | Declared millicores and MiB, for charging without the job |
| `exit_code`, `duration_ms` | Null until a result is recorded |
| `billed_cpu`, `billed_memory`, `billed_ms` | What the provider reported billing, when it reports it |
| `logs`, `logs_truncated` | Last 64 KiB of output (`BYTEA`), and whether it was cut |
| `released_at` | When what the provider left behind was released; null until then |

Updates match the state the writer read, so two writers cannot overwrite each
other, and never touch `released_at`, which only the release path writes.
Index `executions_job (namespace, job, created_at)` serves job status; the
partial index `executions_unreleased (updated_at) WHERE released_at IS NULL`
serves the [release loop](background-services.md#provider-release).

### `quota_usage` and `quota_reservations`

| Column | Meaning |
|---|---|
| `namespace` | `''` for the provider's own pools; otherwise that namespace's share |
| `provider`, `pool` | Labels from the configuration |
| `period` | Calendar key: `2026-09` for monthly, `2026-09-22` for daily, UTC |
| `used`, `amount` | Base units: executions; MiB·ms for `gb_seconds`; millicore·ms for `cpu_seconds`; ms for `seconds` |
| `quota_reservations.cpu`, `memory` | Declared shape, so the reaper can price a run it did not dispatch |
| `quota_reservations.created_at` | Reservations older than 1 hour are reaped |

A new period is a key no row has yet, so rollover needs no job. Reserving is
one `INSERT ... SELECT` that writes rows only when every pool has room;
settling deletes the reservation and upserts `quota_usage` in one statement.
Semantics: [Quotas](quotas.md).

### Retention

Nothing is deleted except settled reservations. `jobs`, `job_versions`,
`dispatches`, `executions` and past periods of `quota_usage` grow without
bound. `executions` dominates: up to 64 KiB of output per attempt. Pruning old
rows by hand is safe for:

- `executions` and `dispatches` that are not `running`, older than any history
  you want from `vagabond job status`
- `quota_usage` rows for periods that have ended

Never delete `quota_reservations` rows by hand; the reaper resolves them.

## Transactions

Every write runs in a `SERIALIZABLE` transaction. Serializable is what makes
the quota reservation exact on Postgres: under its default isolation two
reservations could both read room.

A serialization failure (SQLSTATE `40001`) is retried whole, up to 10
attempts, waiting a random time up to 5 ms doubling to a 500 ms ceiling.
After 10 the call fails with `gave up after 10 serialization failures`.

## sqlc

Queries are written in SQL and compiled to Go with [sqlc](https://sqlc.dev):

| | |
|---|---|
| Configuration | `sqlc.yaml` at the repository root |
| Queries | `internal/state/postgres/sqlc/queries/*.sql` |
| Schema input | The goose migrations |
| Output | `internal/state/postgres/sqlc/`, package `db`, pgx/v5 |
| Generator version | v1.30.0 |

`make generate` does not run sqlc. After changing a query or adding a
migration, run `sqlc generate` from the repository root and commit the output.
A new migration also raises `SchemaVersion` in
`internal/state/postgres/store.go`.

## In-memory store (`-dev`)

`vagabond server -dev` replaces every store with an in-memory one and ignores
any `store` block (with a warning). The memory stores follow the same rules as
Postgres: conditional updates, lease claims, reservations that refuse past a
limit.

What is lost:

| | With `-dev` |
|---|---|
| Registered jobs | Gone at exit |
| Dispatch and execution records | Gone at exit; `execution status` for an old ID is 404 |
| Quota usage | Starts at zero every start, so every budget resets on restart |
| Running dispatches | Not resumed by the next server; work still running at a provider is left untracked and unsettled |
| Multiple servers | Each has its own state; nothing is shared |
| `GET /v1/health` | Always `"Store": "ok"` |

Use it for trying configuration, fakes and pools. Against a real provider, a
restart forgets what was spent.

## Store outage

The server fails closed while the database is unreachable. A connection that
cannot be opened, breaks, or times out is reported as `the store is
unreachable`.

| | During the outage |
|---|---|
| New dispatch or run, register, stop | 503 |
| Status, list, logs | 503, as every query's connection failure is classified unreachable |
| Plan | Answers from the last usage snapshot |
| Dispatch already running | Continues; record writes after `pending` are best effort |
| `GET /v1/health` | 503, `"Store": "unreachable"` |
| Usage refresh (15s) | Fails with a warning, keeps the last snapshot |

Recovery needs no action. The pool reconnects on the next call; a dispatch
whose finish was not written has a lapsed lease and is claimed and resumed
within 30 s; an unsettled reservation is settled by the reaper after it is an
hour old. Details: [dispatch](dispatch.md#store-outage),
[background services](background-services.md).

## Backups

Any Postgres backup works (`pg_dump`, base backups with WAL archiving); on
CockroachDB use `BACKUP`. Back up the whole database.

```bash
pg_dump --format=custom --file=vagabond-$(date +%F).dump \
  "postgres://vagabond@db.internal:5432/vagabond"
pg_restore --clean --if-exists --dbname="postgres://vagabond@db.internal:5432/vagabond" \
  vagabond-2026-09-28.dump
```

| Tables | Loss means |
|---|---|
| `quota_usage`, `quota_reservations` | Budgets count from zero; the current period can be overspent |
| `jobs`, `job_versions` | Registered jobs must be registered again |
| `dispatches`, `executions` | History, stored output, and resumption of running dispatches |
| `goose_db_version` | The server re-runs migrations against existing tables and fails |

Restoring an older backup:

- Usage settled after the backup is not counted, so the period under-counts.
- Dispatches `running` at backup time have lapsed leases and are claimed and
  resumed; each unfinished execution is asked about by ID.
- Stop every server before restoring, and start them on a binary whose
  `SchemaVersion` matches the backup or is newer.

## CockroachDB

Supported with the same code and migrations. Differences the code accounts for:

- Migrations 2, 4 and 6 run outside a transaction (`-- +goose NO
  TRANSACTION`). CockroachDB refuses a primary key change in the same
  transaction as the column it adds.
- The schema uses no sequences, triggers or foreign keys.
- CockroachDB raises `40001` for any contended transaction, not only under
  serializable conflicts on Postgres; the same retry covers both.
- `BYTEA` is read as `BYTES`.

The integration tests run the store against `postgres:17` and
`cockroachdb/cockroach:latest-v24.3` (`make integration-test`, requires
Docker).
