---
title: "Database Schema"
linkTitle: "Database Schema"
description: "Interactive map of the Postgres store's tables and how they relate, with columns, keys, indexes and the queries that read and write each table."
weight: 9
---

The tables of the Postgres store. CockroachDB runs the same migrations against the same schema; seven of them run outside a transaction so CockroachDB applies each schema change on its own, and `execution_logs.data` reads back as `BYTES` there. Under `-dev` in-memory stores replace the database and none of these tables exist. **Hover over any table** for column details and usage context.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  // Tables and their relationships only, grouped by area; each table's
  // columns, keys and indexes are in its hover panel.
  roles: {
    registry: 'storage', runs: 'entry', ledger: 'filter', schema: 'background',
  },

  source: [
    'flowchart TB',
    '    subgraph REGISTRY [Registered jobs]',
    '        jobs[jobs]:::registry',
    '        job_versions[job_versions]:::registry',
    '    end',
    '    subgraph RUNS [Runs]',
    '        dispatches[dispatches]:::runs',
    '        executions[executions]:::runs',
    '        execution_logs[execution_logs]:::runs',
    '    end',
    '    subgraph LEDGER [Quota ledger]',
    '        quota_reservations[quota_reservations]:::ledger',
    '        quota_usage[quota_usage]:::ledger',
    '    end',
    '    subgraph SCHEMA [Migrations]',
    '        goose_db_version[goose_db_version]:::schema',
    '    end',
    '',
    '    jobs -->|namespace, name| job_versions',
    '    job_versions -->|namespace, job, job_version| dispatches',
    '    dispatches -->|dispatch_id| executions',
    '    executions -->|previous_id| executions',
    '    executions -->|execution_id| execution_logs',
    '    jobs -.->|namespace, job| executions',
    '    executions -->|execution_id| quota_reservations',
    '    quota_reservations -->|namespace, provider, pool, period| quota_usage'
  ].join('\n'),

  info: {
    jobs: {
      title: 'jobs',
      badge: 'registry', badgeText: 'job registry',
      body: '<p>One row per registered job: which version is current and whether the job is stopped. Registering an unchanged running job writes nothing; a changed one, or any register of a stopped job, inserts the next <code>job_versions</code> row and points this row at it. A job run from a file through <code>POST /v1/jobs/run</code> is never registered and has no row.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk">namespace</td><td>TEXT</td><td>PRIMARY KEY (composite)</td></tr>' +
        '<tr><td class="pk">name</td><td>TEXT</td><td>PRIMARY KEY (composite)</td></tr>' +
        '<tr><td>version</td><td>BIGINT</td><td>Current version, a key into <code>job_versions</code></td></tr>' +
        '<tr><td>stopped</td><td>BOOLEAN</td><td>Set by <code>DELETE /v1/job/{name}</code>; cleared by the next register</td></tr>' +
        '<tr><td>updated_at</td><td>TIMESTAMPTZ</td><td>Last register or stop</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK (namespace, name), which serves the single-job read, the per-namespace listing ordered by name, and the register upsert</div>' +
        '<p>Used by: <code>Store.Register</code> (CurrentJobVersion, then PointJobAt, an upsert on <code>(namespace, name)</code> that sets the version and clears <code>stopped</code>) &bull; <code>Store.Job</code> (GetJob, for job status, a stop, and before a <a href="../task-path/">dispatch</a>, where <code>jobs.Current</code> refuses a stopped job) &bull; <code>Store.Jobs</code> (ListJobs, for <code>GET /v1/jobs</code>) &bull; <code>Store.Stop</code> (StopJob; zero rows is not found). See <a href="../../docs/database/#jobs-and-job_versions">Database</a>.</p>' +
        '<p class="dg-metric">Key queries: CurrentJobVersion, PointJobAt, GetJob, ListJobs, StopJob</p>'
    },
    job_versions: {
      title: 'job_versions',
      badge: 'registry', badgeText: 'job registry',
      body: '<p>Every version of every registered job, kept after a stop. The source is stored as submitted; the fingerprint is the SHA-256 of its <code>hclwrite</code>-formatted form, so a register that only reformats the file creates no version. Register reads the current version and inserts the next in one serializable transaction, so two registers racing cannot take the same number: they conflict and one is retried whole.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk">namespace</td><td>TEXT</td><td>PRIMARY KEY (composite)</td></tr>' +
        '<tr><td class="pk">name</td><td>TEXT</td><td>PRIMARY KEY (composite)</td></tr>' +
        '<tr><td class="pk">version</td><td>BIGINT</td><td>PRIMARY KEY (composite); counts from 1 per job</td></tr>' +
        '<tr><td>source</td><td>TEXT</td><td>Job file as submitted</td></tr>' +
        '<tr><td>fingerprint</td><td>TEXT</td><td>Hex SHA-256 of the formatted source; compared with the new one on register</td></tr>' +
        '<tr><td>created_at</td><td>TIMESTAMPTZ</td><td>Register time</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK (namespace, name, version), which serves the join in CurrentJobVersion, the single-version read, and the listing newest first</div>' +
        '<p>Used by: <code>Store.Register</code> (CurrentJobVersion joins <code>jobs</code> to its current row for the fingerprint; InsertJobVersion) &bull; <code>Store.Version</code> (GetJobVersion: <code>jobs.Current</code> loads the current source to dispatch it through <code>POST /v1/job/{name}/dispatch</code>) &bull; <code>Store.Versions</code> (ListJobVersions, without the source, for <code>GET /v1/job/{name}</code>).</p>' +
        '<p class="dg-metric">Key queries: CurrentJobVersion, InsertJobVersion, GetJobVersion, ListJobVersions</p>'
    },
    dispatches: {
      title: 'dispatches',
      badge: 'runs', badgeText: 'run record',
      body: '<p>One row per run of a job, above its executions. A run refused before any task was submitted has no executions, so this row is its only trace. The server running the dispatch holds a lease on the row and renews it until every execution of the run is released; another server claims a row whose lease lapsed and resumes the run.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk">dispatch_id</td><td>TEXT</td><td>PRIMARY KEY; UUIDv7 minted when the run starts</td></tr>' +
        '<tr><td>namespace</td><td>TEXT</td><td>Namespace the job runs in</td></tr>' +
        '<tr><td>job</td><td>TEXT</td><td>Job name</td></tr>' +
        '<tr><td>job_version</td><td>BIGINT</td><td>Registered version; 0 for a job run from a file</td></tr>' +
        '<tr><td>state</td><td>TEXT</td><td><code>running</code>, <code>succeeded</code>, <code>failed</code>, <code>unanswered</code></td></tr>' +
        '<tr><td>error</td><td>TEXT</td><td>Why an unanswered run got no answer; <code>\'\'</code> otherwise, and on insert</td></tr>' +
        '<tr><td>created_at</td><td>TIMESTAMPTZ</td><td>Run start</td></tr>' +
        '<tr><td>ended_at</td><td>TIMESTAMPTZ</td><td>Nullable; null while running</td></tr>' +
        '<tr><td>tasks</td><td>BIGINT</td><td>Tasks the job declares; resume compares it with the executions to decide how the run ended. DEFAULT 0, the value on rows from before migration 6</td></tr>' +
        '<tr><td>owner</td><td>TEXT</td><td>Lease holder, <code>server:&lt;host&gt;:&lt;pid&gt;</code>. DEFAULT <code>\'\'</code></td></tr>' +
        '<tr><td>lease_until</td><td>TIMESTAMPTZ</td><td>Lease expiry, renewed every 20s to 60s ahead. DEFAULT <code>1970-01-01 00:00:00+00</code>, so a row left running before migration 6 is claimed at once</td></tr>' +
        '<tr><td>epoch</td><td>BIGINT</td><td>Claims of the row, 1 at insert; each claim adds one, so every write under an earlier lease is refused. DEFAULT 1</td></tr>' +
        '<tr><td>cancel_requested_at</td><td>TIMESTAMPTZ</td><td>Nullable; when a cancel was first requested by a server not holding the lease, for the owner to act on</td></tr>' +
        '<tr><td>done_at</td><td>TIMESTAMPTZ</td><td>Nullable; when every execution finished through release, which ends the lease. Migration 10 backfilled it for ended dispatches whose executions were all released</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK on dispatch_id &bull; dispatches_lease (lease_until) WHERE done_at IS NULL, a partial index holding only the rows a claim can take</div>' +
        '<p>Every write under the lease matches the caller&rsquo;s owner and epoch: finishing also requires <code>state = \'running\'</code>, renewing and closing <code>done_at IS NULL</code>. Zero rows is <code>ErrStale</code>, so a server whose lease was taken over cannot overwrite how the run ended.</p>' +
        '<p>Used by: the dispatcher (CreateDispatch as a run starts, FinishDispatch as it ends, CloseDispatch once every execution is released; <a href="../task-path/">task path</a>) &bull; <a href="../background-services/">lease renewal</a> (RenewDispatch every 20s, returning <code>cancel_requested_at</code>; stale marks the lease lost and every runner under it stops) &bull; <a href="../background-services/">dispatch claim</a> (ClaimDispatches at startup and every 30s: one <code>UPDATE ... RETURNING</code> takes every row with no <code>done_at</code> whose <code>lease_until</code> is past, except the ones this server runs, under <code>epoch + 1</code>, so two servers never claim the same row) &bull; the API (RequestCancel for <code>DELETE /v1/execution/{id}</code> on a dispatch held elsewhere, keeping the first request&rsquo;s time; GetDispatch for <code>GET /v1/dispatch/{id}</code>). See <a href="../../docs/database/#dispatches">Database</a> and <a href="../../docs/dispatch/#leases">leases</a>.</p>' +
        '<p class="dg-metric">Key queries: CreateDispatch, FinishDispatch, CloseDispatch, RenewDispatch, RequestCancel, ClaimDispatches, GetDispatch</p>'
    },
    executions: {
      title: 'executions',
      badge: 'runs', badgeText: 'run record',
      body: '<p>One row per attempt: one task submitted to one provider. The row is written before <code>Submit</code>, so a crash between dispatching and recording leaves a row to resume from. Every write after it is the execution runner&rsquo;s, carries every column that changes after creation, and applies only while the dispatch&rsquo;s owner and epoch match and it is not done, so a server whose lease was taken over cannot overwrite the new owner&rsquo;s record, and repeating a write changes nothing.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk">id</td><td>TEXT</td><td>PRIMARY KEY; UUIDv7, also the submission idempotency key</td></tr>' +
        '<tr><td>namespace</td><td>TEXT</td><td>Namespace of the run</td></tr>' +
        '<tr><td>job</td><td>TEXT</td><td>Job name</td></tr>' +
        '<tr><td>job_version</td><td>BIGINT</td><td>As on the dispatch</td></tr>' +
        '<tr><td>task</td><td>TEXT</td><td>Task name</td></tr>' +
        '<tr><td>provider</td><td>TEXT</td><td>Provider the attempt was submitted to</td></tr>' +
        '<tr><td>node</td><td>TEXT</td><td>The agent node it runs on, for a <code>containerd</code> provider; <code>\'\'</code> otherwise</td></tr>' +
        '<tr><td>attempt</td><td>BIGINT</td><td>1 for a task\'s first submission</td></tr>' +
        '<tr><td class="fk">previous_id</td><td>TEXT</td><td>The <code>id</code> of the attempt before this one on the same task; <code>\'\'</code> on the first</td></tr>' +
        '<tr><td>state</td><td>TEXT</td><td><code>pending</code>, <code>submitted</code>, <code>accepted</code>, <code>running</code>, <code>succeeded</code>, <code>failed</code>, <code>cancelled</code>, <code>lost</code></td></tr>' +
        '<tr><td>provider_id</td><td>TEXT</td><td>The provider\'s own identifier; <code>\'\'</code> until Submit returns</td></tr>' +
        '<tr><td>failure</td><td>TEXT</td><td><code>infrastructure</code> or <code>internal</code> when the attempt ended without an answer; <code>\'\'</code> otherwise</td></tr>' +
        '<tr><td>created_at</td><td>TIMESTAMPTZ</td><td>Read from the UUIDv7 in id</td></tr>' +
        '<tr><td>started_at</td><td>TIMESTAMPTZ</td><td>Nullable; set on reaching running</td></tr>' +
        '<tr><td>ended_at</td><td>TIMESTAMPTZ</td><td>Nullable; set on a terminal state</td></tr>' +
        '<tr><td>updated_at</td><td>TIMESTAMPTZ</td><td>When the state last changed; the 24 hours a <code>lost</code> execution is given count from it</td></tr>' +
        '<tr><td>exit_code</td><td>BIGINT</td><td>Nullable</td></tr>' +
        '<tr><td>duration_ms</td><td>BIGINT</td><td>Nullable; null means no result is recorded</td></tr>' +
        '<tr><td>billed_cpu</td><td>BIGINT</td><td>Nullable; billed millicores, 0 when the provider bills no CPU</td></tr>' +
        '<tr><td>billed_memory</td><td>BIGINT</td><td>Nullable; billed MiB</td></tr>' +
        '<tr><td>billed_ms</td><td>BIGINT</td><td>Nullable; billed duration. All three are null unless the provider reported a billed amount</td></tr>' +
        '<tr><td>logs_truncated</td><td>BOOLEAN</td><td>The provider gave less output than the task printed</td></tr>' +
        '<tr><td class="fk">dispatch_id</td><td>TEXT</td><td>The <code>dispatches</code> row it belongs to. DEFAULT <code>\'\'</code>, the value on rows from before migration 4</td></tr>' +
        '<tr><td>cpu</td><td>BIGINT</td><td>Declared millicores, so a resumed execution is charged without its job. DEFAULT 0</td></tr>' +
        '<tr><td>memory</td><td>BIGINT</td><td>Declared MiB. DEFAULT 0</td></tr>' +
        '<tr><td>released_at</td><td>TIMESTAMPTZ</td><td>Nullable; when what the provider left behind was released. Migration 7 backfilled it to <code>updated_at</code> for rows with a result, and migration 10 for rows with no dispatch</td></tr>' +
        '<tr><td>reason</td><td>TEXT</td><td>Why the platform ended the workload, in its words. DEFAULT <code>\'\'</code>, the value on rows from before migration 8 and when the platform did not say</td></tr>' +
        '<tr><td>logs_start</td><td>BIGINT</td><td>First byte of output still in <code>execution_logs</code>; above 0 once the front was dropped past <code>max_log_mib</code>. DEFAULT 0</td></tr>' +
        '<tr><td>logs_at</td><td>TIMESTAMPTZ</td><td>Nullable; when the output was captured. Null tells a restored runner the output is still to be captured. Migration 9 backfilled it for rows with a result</td></tr>' +
        '<tr><td>timeout_ms</td><td>BIGINT</td><td>Declared timeout, what an execution that never answered is charged for. DEFAULT 0, the value on rows from before migration 10</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK on id &bull; executions_job (namespace, job, created_at) for the job\'s newest executions</div>' +
        '<p>Used by: the dispatcher (CreateExecution after the quota reservation and before Submit, where a failure means nothing is submitted) &bull; execution runners (SaveExecution at every step, retried until the store takes it: each state change, the result with <code>logs_at</code> once the output is stored, then <code>released_at</code>; <a href="../task-path/">task path</a>, <a href="../execution-states/">execution states</a>) &bull; resume (ListDispatchExecutions on a claimed dispatch, restoring a runner for every row not yet terminal, collected and released) &bull; the API (GetExecution for <code>GET /v1/execution/{id}</code>, where its stored logs begin, and a cancel; ListJobExecutions, the newest 20, for <code>GET /v1/job/{name}</code>; ListDispatchExecutions for <code>GET /v1/dispatch/{id}</code>) &bull; the <a href="../background-services/">reservation reaper</a> (GetExecution, to find reservations with no record) &bull; <a href="../live-output/">live output</a> (GetExecution every second, to see the execution end). See <a href="../../docs/database/#executions">Database</a>.</p>' +
        '<p class="dg-metric">Key queries: CreateExecution and SaveExecution (each applied only under the dispatch\'s lease; zero rows is ErrStale), HoldsExecution, GetExecution, ListDispatchExecutions, ListJobExecutions</p>'
    },
    execution_logs: {
      title: 'execution_logs',
      badge: 'runs', badgeText: 'run output',
      body: '<p>An execution\'s output in 256 KiB chunks, kept off its <code>executions</code> row so the reads of a record every second never carry it. Chunk <code>seq</code> holds bytes <code>seq &times; 256 KiB</code> onward of the output, so a byte offset names its chunk and a read from any offset needs no index beyond the key. Migration 9 moved each older row\'s output, at most 64 KiB, into chunk 0.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk fk">execution_id</td><td>TEXT</td><td>PRIMARY KEY (composite); the <code>executions.id</code> the output belongs to</td></tr>' +
        '<tr><td class="pk">seq</td><td>BIGINT</td><td>PRIMARY KEY (composite); chunk number from the start of the output</td></tr>' +
        '<tr><td>data</td><td>BYTEA</td><td>Up to 256 KiB. BYTEA because output need not be valid UTF-8</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK (execution_id, seq), which serves the chunk-at-a-time read and both deletes</div>' +
        '<p>Used by: the execution runner\'s capture once an execution ends (HoldsExecution checks the dispatch\'s lease, then DropExecutionLogs and InsertExecutionLog, in one transaction per chunk: the chunks from <code>seq</code> on go, so a capture that starts again at 0 replaces the last, and those before <code>keep_from</code> go past <code>max_log_mib</code>) &bull; the API (NextExecutionLog, one chunk per query, for <code>GET /v1/execution/{id}/logs</code>; <a href="../live-output/">live output</a>) &bull; <a href="../background-services/">output retention</a> (PruneExecutionLogs every hour, for executions that ended over <code>log_retention</code> ago). See <a href="../../docs/database/#execution_logs">Database</a>.</p>' +
        '<p class="dg-metric">Key queries: HoldsExecution, DropExecutionLogs, InsertExecutionLog, NextExecutionLog, PruneExecutionLogs</p>'
    },
    quota_reservations: {
      title: 'quota_reservations',
      badge: 'ledger', badgeText: 'quota ledger',
      body: '<p>What running executions are expected to cost: one row per pool the execution charges, in both layers, the provider\'s total and the namespace\'s share. Zero amounts are included so settling knows the period each pool was charged in. Inserted before each submission attempt, ahead of the execution row; deleted when the execution&rsquo;s runner settles, or by the reaper when no execution was recorded. A provider with no pools reserves nothing.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk fk">execution_id</td><td>TEXT</td><td>PRIMARY KEY (composite); the <code>executions.id</code> of the attempt</td></tr>' +
        '<tr><td class="pk">namespace</td><td>TEXT</td><td>PRIMARY KEY (composite); <code>\'\'</code> for the provider\'s total, otherwise that namespace\'s share. DEFAULT <code>\'\'</code></td></tr>' +
        '<tr><td class="pk">pool</td><td>TEXT</td><td>PRIMARY KEY (composite); pool label from the configuration</td></tr>' +
        '<tr><td>provider</td><td>TEXT</td><td>Provider label from the configuration</td></tr>' +
        '<tr><td>period</td><td>TEXT</td><td>Calendar key current at reserve, UTC: <code>2026-09</code> monthly, <code>2026-09-22</code> daily</td></tr>' +
        '<tr><td>amount</td><td>BIGINT</td><td>Declared charge in the pool\'s base unit</td></tr>' +
        '<tr><td>created_at</td><td>TIMESTAMPTZ</td><td>Reserve time; the reaper takes rows older than 1 hour</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK (execution_id, namespace, pool), which serves the settle delete and the reserved check &bull; quota_reservations_pool (namespace, provider, pool, period) for the per-pool sum in ReserveQuota</div>' +
        '<p><b>Reserve</b> (ReserveQuota) is one <code>INSERT ... SELECT</code> that inserts every row only if, for every pool with a non-zero amount, settled + reserved + amount &le; limit. Zero rows is a refusal, and ReadQuotaUsage then runs in the same serializable transaction so the refusal names a pool that was full. <b>Settle</b> (SettleQuota) deletes the rows and adds the actual amounts to <code>quota_usage</code> in the periods they were reserved in, in one statement; settling twice, or after the reaper, finds no rows and charges nothing.</p>' +
        '<p>Used by: the ledger\'s Reserve and Settle (<a href="../quotas/">quotas</a>, <a href="../admission/">admission</a>) &bull; execution runners (ExecutionReserved before settling, so a restored runner settles once) &bull; the <a href="../background-services/">reservation reaper</a> (ClaimStaleReservations at startup and every 5m: rows older than 1 hour, <code>FOR UPDATE SKIP LOCKED</code>, held while each execution record is looked up, so two servers never resolve the same execution; one with no record is dropped through SettleQuota with no amounts). See <a href="../../docs/database/#quota_usage-and-quota_reservations">Database</a>.</p>' +
        '<p class="dg-metric">Key queries: ReserveQuota, SettleQuota, ClaimStaleReservations, ExecutionReserved, ReadQuotaUsage</p>'
    },
    quota_usage: {
      title: 'quota_usage',
      badge: 'ledger', badgeText: 'quota ledger',
      body: '<p>What settled executions cost: one counter per pool per period, in both layers. A new period is a key nothing has written yet, so rollover needs no job; past periods stay and are no longer read. Settlement is not checked against the limit, so <code>used</code> can pass it, and later tasks are refused.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk">namespace</td><td>TEXT</td><td>PRIMARY KEY (composite); <code>\'\'</code> for the provider\'s total. DEFAULT <code>\'\'</code></td></tr>' +
        '<tr><td class="pk">provider</td><td>TEXT</td><td>PRIMARY KEY (composite)</td></tr>' +
        '<tr><td class="pk">pool</td><td>TEXT</td><td>PRIMARY KEY (composite)</td></tr>' +
        '<tr><td class="pk">period</td><td>TEXT</td><td>PRIMARY KEY (composite); <code>YYYY-MM</code> or <code>YYYY-MM-DD</code>, UTC</td></tr>' +
        '<tr><td>used</td><td>BIGINT</td><td>Settled total in base units: executions; MiB&middot;ms for <code>gb_seconds</code>; millicore&middot;ms for <code>cpu_seconds</code>; ms for <code>seconds</code></td></tr>' +
        '<tr><td>updated_at</td><td>TIMESTAMPTZ</td><td>Last settle into the counter, the database\'s <code>NOW()</code></td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK (namespace, provider, pool, period), which serves the lookup in ReserveQuota and the upsert in SettleQuota</div>' +
        '<p>Used by: SettleQuota (<code>INSERT ... ON CONFLICT DO UPDATE</code> adding the amount; zero amounts write nothing) &bull; ReserveQuota (settled usage in the headroom test) &bull; ReadQuotaUsage, which sums this table and <code>quota_reservations</code> per pool in one statement, so a settle moving an amount between them is seen whole. That read fills the usage snapshot at startup, every 15s and before each task, and <a href="../admission/">admission</a> and ranking read the snapshot. See <a href="../quotas/">quotas</a> and <a href="../../docs/quotas/#ledger">Ledger</a>.</p>' +
        '<p class="dg-metric">Key queries: SettleQuota, ReserveQuota, ReadQuotaUsage</p>'
    },
    goose_db_version: {
      title: 'goose_db_version',
      badge: 'schema', badgeText: 'migrations',
      body: '<p>goose\'s record of applied migrations. goose creates it on the first migrate; no migration defines it, and its layout is the goose v3.29 Postgres dialect\'s.</p>' +
        '<table class="dg-cols"><tr><th>Column</th><th>Type</th><th>Notes</th></tr>' +
        '<tr><td class="pk">id</td><td>INTEGER</td><td>PRIMARY KEY, GENERATED BY DEFAULT AS IDENTITY</td></tr>' +
        '<tr><td>version_id</td><td>BIGINT</td><td>Migration number, 1 to 10 in this release</td></tr>' +
        '<tr><td>is_applied</td><td>BOOLEAN</td><td>Whether the migration is applied</td></tr>' +
        '<tr><td>tstamp</td><td>TIMESTAMP</td><td>DEFAULT now(); no time zone</td></tr></table>' +
        '<div class="dg-idx"><b>Indexes:</b> PK on id</div>' +
        '<p>Used by: <code>Store.Migrate</code> (goose applies the embedded migrations not yet recorded, at every server start, over a <code>database/sql</code> connection opened for it) &bull; <code>Store.VerifySchema</code> (reads <code>MAX(version_id)</code> where <code>is_applied</code> and requires it to equal <code>SchemaVersion</code>, 10; lower means a migration failed partway, higher means the binary is older than the database, and either stops the server from starting). See <a href="../../docs/database/#migrations">Migrations</a>.</p>'
    }
  }
};
</script>

## Legend

| Node | Area |
|------|------|
| <span class="dg-swatch dg-storage"></span> | Registered jobs |
| <span class="dg-swatch dg-entry"></span> | Runs |
| <span class="dg-swatch dg-filter"></span> | Quota ledger |
| <span class="dg-swatch dg-background"></span> | Migrations |

An arrow joins two tables on the columns in its label, from the table that holds the key to the one that carries it. The schema has no foreign keys, so nothing enforces these joins. The dotted arrow is the job status read, which goes from a job to its executions without its dispatches. The arrow from `quota_reservations` to `quota_usage` is settlement, which moves a reservation's amounts into the counter with the same key. In a table's hover panel, every column is `NOT NULL` unless its notes say nullable, and a column holding another table's key has no constraint behind it.

### Table Summary

| Table | Purpose | Primary Key |
|-------|---------|-------------|
| **jobs** | Each registered job's current version and whether it is stopped | `(namespace, name)` |
| **job_versions** | Every registered version's source and fingerprint | `(namespace, name, version)` |
| **dispatches** | One row per run of a job, with its lease | `dispatch_id` |
| **executions** | One row per attempt of one task on one provider, with its result and where its output stands | `id` |
| **execution_logs** | Each execution's output in 256 KiB chunks, up to `max_log_mib`, for `log_retention` | `(execution_id, seq)` |
| **quota_reservations** | Declared charges held by executions not yet settled | `(execution_id, namespace, pool)` |
| **quota_usage** | Settled usage per pool per period | `(namespace, provider, pool, period)` |
| **goose_db_version** | Applied migrations, written by goose | `id` |

### Schema Migrations

| Migration | Description |
|-----------|-------------|
| `00001_quota` | `quota_usage` and `quota_reservations`, and index `quota_reservations_pool` |
| `00002_quota_namespaces` | `namespace` on both quota tables, moved into their primary keys and into `quota_reservations_pool`. Outside a transaction |
| `00003_executions` | `executions` |
| `00004_jobs` | `jobs`, `job_versions`, `executions.dispatch_id`, and index `executions_job`. Outside a transaction |
| `00005_dispatches` | `dispatches` |
| `00006_dispatch_leases` | `dispatches.tasks`, `owner` and `lease_until`, index `dispatches_lease`, and `executions.cpu` and `memory`. Outside a transaction |
| `00007_execution_release` | `executions.released_at`, backfilled for rows with a result, and partial index `executions_unreleased`. Outside a transaction |
| `00008_execution_reason` | `executions.reason`, `''` for existing rows. Outside a transaction |
| `00009_execution_logs` | `execution_logs`, holding each existing row's output as chunk 0; `executions.logs_start` and `logs_at`, backfilled for rows with a result; `executions.logs` dropped. Outside a transaction |
| `00010_dispatch_ownership` | `dispatches.epoch`, `cancel_requested_at` and `done_at`, `done_at` backfilled for ended dispatches whose executions are all released; `executions.timeout_ms`; `quota_reservations.cpu` and `memory` dropped; executions with no dispatch marked released; `dispatches_lease` rebuilt as `(lease_until) WHERE done_at IS NULL`; `executions_unreleased` dropped. Outside a transaction |
