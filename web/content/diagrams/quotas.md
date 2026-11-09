---
title: "Quota Accounting"
linkTitle: "Quota Accounting"
description: "Interactive diagram of how a task is charged against provider pools and namespace shares, admitted on headroom, reserved, settled and reaped in the ledger."
weight: 4
---

How a task's declared shape becomes a charge against every pool of a provider and the namespace's share of it, and how the ledger admits, reserves, settles and reaps that charge. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    subgraph CFG [Configuration]',
    '        TOTAL[Provider pools<br>provider pool blocks]:::storage',
    '        SHARE[Namespace share<br>namespace quota blocks]:::storage',
    '    end',
    '    TOTAL --> LIMITS[Compiled pools<br>meter, limit, period]:::process',
    '    SHARE --> LIMITS',
    '    TASK([Task declaration<br>cpu, memory, timeout]):::entry --> CHARGE[Charge per pool<br>meter over declared shape]:::process',
    '    LIMITS --> CHARGE',
    '',
    '    REFRESH[Usage snapshot refresh<br>start, every 15 s, each task]:::background --> SNAP[(Usage snapshot<br>settled + reserved)]:::storage',
    '    CHARGE --> HEADROOM[free_quota_percent<br>tightest pool free]:::check',
    '    CHARGE --> QCHECK[Admission quota check<br>used + charge within limit]:::check',
    '    SNAP --> HEADROOM',
    '    SNAP --> QCHECK',
    '    QCHECK -->|does not fit, will not pay| EXHAUSTED[quota-exhausted<br>rejection]:::reject',
    '    QCHECK -->|fits, or job pays| RESERVE[Reserve<br>declared charge]:::process',
    '',
    '    RESERVE -->|zero rows| REFUSAL[Refusal<br>next ranked provider]:::reject',
    '    RESERVE -->|rows inserted| RESV[(quota_reservations)]:::storage',
    '    RESV --> RESULTQ{Result<br>returned?}:::decision',
    '    RESULTQ -->|yes| BILLED{Billed<br>amount?}:::decision',
    '    BILLED -->|yes| BILLEDCHG[Billed memory and CPU<br>over billed duration]:::process',
    '    BILLED -->|no| DECLCHG[Declared CPU and memory<br>over measured duration]:::process',
    '    BILLEDCHG --> SETTLE[Settle<br>reservation replaced]:::process',
    '    DECLCHG --> SETTLE',
    '    SETTLE --> USAGE[(quota_usage)]:::storage',
    '',
    '    RESULTQ -->|no: reservation stands| REAPER[Reservation reaper<br>every 5 min, over 1 h old]:::background',
    '    SETTLE -->|write failed| REAPER',
    '    REAPER -->|settled or dropped| USAGE',
    '    REFRESH -->|reads| USAGE',
  ].join('\n'),

  info: {
    TOTAL: {
      title: 'Provider Pools',
      badge: 'storage', badgeText: 'configuration',
      body: '<p>Each <code>pool</code> block inside a <code>provider</code> is one budget: a <code>meter</code>, a positive whole-number <code>limit</code> in the meter&rsquo;s unit, and a <code>period</code>. All three are required and nothing has a default; no provider ships preset pools.</p><p>Pools are additive. An execution charges every pool whose meter it touches and must fit all of them, so a daily pool can sit inside a monthly one. A provider with no pools is unlimited and is never reserved, settled or reaped.</p><p>Usage is counted under the pool&rsquo;s name, so renaming a pool starts its count from zero.</p><p><a href="../../docs/configuration/#pool-block">pool block &rarr;</a></p>'
    },
    SHARE: {
      title: 'Namespace Share',
      badge: 'storage', badgeText: 'configuration',
      body: '<p><code>namespace &quot;ci&quot; { quota &quot;aws-lambda&quot; { pool ... } }</code> declares the namespace&rsquo;s own slice of one provider, in the same syntax as a provider pool. It is counted under the namespace on that provider.</p><p>An execution charges the provider total and its namespace&rsquo;s share, and must fit both. Shares may add up past the total; the total still binds, so a namespace can be refused by the total while its share has room. A namespace with no share is limited by the provider&rsquo;s pools alone.</p><p>Every execution runs in a namespace, <code>default</code> unless the job names one. A job naming an undeclared namespace is refused.</p><p><a href="../../docs/configuration/#namespace-block">namespace block &rarr;</a></p>'
    },
    LIMITS: {
      title: 'Compiled Pools',
      badge: 'process', badgeText: 'quota.NewLimits',
      body: '<p><code>quota.NewLimits</code> validates each pool when the configuration loads and multiplies its limit into the meter&rsquo;s base unit, so it compares directly against an integer counter: &times; 1,024,000 for <code>gb_seconds</code>, &times; 1,000,000 for <code>cpu_seconds</code>, &times; 1000 for <code>seconds</code>. A limit that would overflow the counter is refused.</p><p><code>quota.Budgets</code> holds every provider&rsquo;s total and every namespace&rsquo;s shares.</p><p>Periods are <code>daily</code> (key <code>YYYY-MM-DD</code>) and <code>monthly</code> (key <code>YYYY-MM</code>), both UTC. A new period is a key nothing is counted under yet, so reset needs no job.</p>'
    },
    TASK: {
      title: 'Task Declaration',
      badge: 'entry', badgeText: 'entry point',
      body: '<p>A charge is computed from what the task declares, not from what it uses: <code>resources.cpu</code> (millicores), <code>resources.memory</code> (MiB) and <code>timeout</code>, held as a <code>quota.Execution</code>. That is what lets <code>job plan</code> price a task it is not going to run.</p><p>The same value prices the admission check, <code>free_quota_percent</code> and the dispatch reservation. An unparseable timeout counts as none.</p>'
    },
    CHARGE: {
      title: 'Charge per Pool',
      badge: 'process', badgeText: 'meters',
      body: '<p><b>executions</b>: 1.<br><b>gb_seconds</b>: memory &times; timeout, counted in MiB-milliseconds.<br><b>cpu_seconds</b>: CPU &times; timeout, counted in millicore-milliseconds.<br><b>seconds</b>: timeout, counted in milliseconds.</p><p>The timeout is rounded up to a whole millisecond. GB means GiB: 1024 MiB for one second is one GB-second.</p><p>A task with no <code>timeout</code> charges 0 to every meter but <code>executions</code>; one with no memory charges 0 to <code>gb_seconds</code>, one with no CPU 0 to <code>cpu_seconds</code>. A pool charged 0 is skipped: it cannot reject the task and does not lower <code>free_quota_percent</code>.</p>'
    },
    HEADROOM: {
      title: 'free_quota_percent',
      badge: 'check', badgeText: 'headroom',
      body: '<p>Per pool: 100 when nothing is counted, otherwise <code>floor((limit &minus; used) &times; 100 / limit)</code>, 0 once used reaches the limit. <code>free_quota_percent</code> is the minimum over the pools this task charges, in the provider&rsquo;s pools and the namespace&rsquo;s share; 100 when it charges none.</p><p>It depends on the task, so one provider reports different values to tasks that meter differently. <code>used</code> includes open reservations.</p><p>Published as <code>provider.free_quota_percent</code> for constraints and affinities, and the <code>headroom</code> scorer ranks on <code>clamp(free_quota_percent / 100)</code>.</p><p><a href="../admission/">Admission and ranking diagram &rarr;</a></p>'
    },
    QCHECK: {
      title: 'Admission Quota Check',
      badge: 'check', badgeText: 'check 13',
      body: '<p>The <code>quota</code> checker, last of the 13 admission checks, finds the first pool where <code>used + charge &gt; limit</code>: the provider&rsquo;s pools in configured order, then the namespace&rsquo;s share. Exactly reaching the limit fits.</p><p>It applies only when <code>max_cost_usd</code> is unset or 0. A job with <code>max_cost_usd &gt; 0</code> passes at any usage. It reads the usage snapshot, not the store.</p><p><a href="../admission/">Admission and ranking diagram &rarr;</a></p>'
    },
    EXHAUSTED: {
      title: 'quota-exhausted',
      badge: 'reject', badgeText: 'transient',
      body: '<p>The provider is rejected for this task. The detail names the pool, in its unit:</p><p><code>Pool &quot;compute&quot; has 1000 of 200000 GB-seconds left and this task needs 1800, and the job will not pay for capacity beyond it.</code> A share begins <code>Namespace &quot;ci&quot;\'s pool</code>. Amounts print with Go&rsquo;s <code>%g</code>, so one million prints as <code>1e+06</code>.</p><p>Transient: when no provider is admitted, the plan is marked retryable, since a period reset can admit the same job.</p>'
    },
    RESERVE: {
      title: 'Reserve',
      badge: 'process', badgeText: 'ledger.Reserve',
      body: '<p>Before each submission attempt, dispatch reserves the declared charge under the attempt&rsquo;s execution ID: one row per pool of both layers, zero charges included, so settlement knows each pool&rsquo;s period. The period key is the one current at reservation.</p><p>The headroom test and the insert are one <code>INSERT ... SELECT</code>: rows are written only if every pool with a non-zero charge has <code>settled + reserved + charge &le; limit</code>. It runs in a serializable transaction, up to 10 attempts on a serialization failure with jittered backoff capped at 500 ms, so two servers cannot both see room. Without a <code>store</code> block the in-memory store applies the same rule under one mutex.</p><p>A job that pays is reserved with no ceiling and never refused; its usage still counts.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    REFUSAL: {
      title: 'Refusal',
      badge: 'reject', badgeText: 'ledger.Refusal',
      body: '<p>Zero rows inserted. The store reads usage in the same transaction and the ledger names the first pool without room, the provider&rsquo;s total before the namespace&rsquo;s share:</p><p><code>pool &quot;compute&quot; of provider &quot;aws-lambda&quot; has 1000 of 400000 GB-seconds left; this task needs 1800</code></p><p>Dispatch moves to the next ranked provider without spending a retry attempt. When every candidate refuses, the task fails with <code>no provider can run this task</code>. This is how a task admitted on a snapshot that missed another server&rsquo;s charges is caught.</p>'
    },
    RESV: {
      title: 'quota_reservations',
      badge: 'storage', badgeText: 'table',
      body: '<p>Open reservations: <code>execution_id</code>, <code>namespace</code> (<code>&#39;&#39;</code> for the provider&rsquo;s own pools), <code>provider</code>, <code>pool</code>, <code>period</code>, <code>amount</code> in base units, the declared <code>cpu</code> and <code>memory</code> so the reaper can price a run it did not dispatch, and <code>created_at</code>.</p><p>Counted in the usage snapshot alongside settled usage, so <code>free_quota_percent</code> and the next reservation see a run while it is open. Never delete these rows by hand; the reaper resolves them.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    RESULTQ: {
      title: 'Result Returned?',
      badge: 'decision', badgeText: 'dispatch.charge',
      body: '<p>Only a result says what a run cost, so only an attempt that returns one is settled. Without one the reservation stands at the declared charge: the run may still be going, and the declared worst case is the over-count the ledger errs toward.</p>'
    },
    BILLED: {
      title: 'Billed Amount?',
      badge: 'decision', badgeText: 'result.Billed',
      body: '<p>A result can carry what the platform billed. When it does, it supersedes the estimate, above the reservation included.</p><p>AWS Lambda reports one when the invocation&rsquo;s <code>REPORT</code> log line carries both <code>Billed Duration</code> and <code>Memory Size</code>. Every other provider settles at the declared shape.</p>'
    },
    BILLEDCHG: {
      title: 'Billed Charge',
      badge: 'process', badgeText: 'platform figure',
      body: '<p>Billed memory &times; billed duration, and billed CPU &times; billed duration, with CPU 0 when the platform reports none. <code>executions</code> pools are charged 1.</p><p>On Lambda the memory is the function&rsquo;s configured size, not the task&rsquo;s declaration, and no CPU is reported, so <code>cpu_seconds</code> pools are charged 0 for that execution.</p>'
    },
    DECLCHG: {
      title: 'Declared Charge',
      badge: 'process', badgeText: 'estimate',
      body: '<p>The declared CPU and memory &times; the execution&rsquo;s measured duration, rather than its timeout. <code>executions</code> pools are charged 1.</p><p>A task with no timeout reserved 0 on duration meters and is charged its real duration here.</p>'
    },
    SETTLE: {
      title: 'Settle',
      badge: 'process', badgeText: 'ledger.Settle',
      body: '<p>One statement deletes the execution&rsquo;s reservation rows and adds the actual charge to <code>quota_usage</code>, in the periods the reservation was made in. A run that ends after midnight UTC is charged to the day it was reserved in.</p><p>Not checked against the limit: settled usage can pass it, and later tasks are refused. Settling an execution with no reservation rows does nothing, so a settle after the reaper, or twice, charges once.</p><p>A failed settle write leaves the reservation for the reaper.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    USAGE: {
      title: 'quota_usage',
      badge: 'storage', badgeText: 'table',
      body: '<p>Settled usage, one row per <code>(namespace, provider, pool, period)</code>, in base units. Settlement upserts it additively.</p><p>Past periods stay in the table and are no longer read; rows for ended periods are safe to prune by hand. In the in-memory store (no <code>store</code> block, or <code>server -dev</code>) usage is empty at every start.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    REAPER: {
      title: 'Reservation Reaper',
      badge: 'background', badgeText: 'every 5 min',
      body: '<p>On <code>vagabond server</code> start, then every 5 minutes, claims reservations created more than 1 hour ago (<code>FOR UPDATE SKIP LOCKED</code>, one serializable transaction) and asks each execution&rsquo;s provider for its status.</p><p><b>Unknown execution</b>: dropped, nothing charged.<br><b>Status unsupported</b>, or <b>terminal, started, no end time</b>: settled at the reserved amounts.<br><b>Terminal, never started</b>: settled at duration 0.<br><b>Terminal, started and ended</b>: declared shape &times; (end &minus; start).<br><b>Provider gone, error, not terminal</b>: kept for the next pass.</p><p>Logs <code>resolved abandoned quota reservations</code> with a count.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    REFRESH: {
      title: 'Usage Snapshot Refresh',
      badge: 'background', badgeText: 'every 15 s',
      body: '<p>Re-reads settled plus reserved usage for every pool&rsquo;s current period, in one statement over both tables. Read at server start, which fails if the read does (<code>could not read quota usage</code>); every 15 seconds; and before each task a dispatch runs.</p><p>A failed read keeps the previous snapshot; the timer logs a warning, <code>refreshing quota usage</code>. Reserving, settling and reaping do not update it.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    SNAP: {
      title: 'Usage Snapshot',
      badge: 'storage', badgeText: 'in memory',
      body: '<p>The ledger&rsquo;s in-memory copy of settled plus reserved usage, which admission and ranking read instead of the store. <code>job plan</code> uses it as it stands; dispatch refreshes it first.</p><p>A plan can be up to 15 seconds behind other servers&rsquo; charges, or older while the store is unreachable. <code>GET /v1/health</code> reports when it was last read as <code>UsageRefreshed</code>.</p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Entry point |
| <span class="dg-swatch dg-storage"></span> | Declared budget or stored usage |
| <span class="dg-swatch dg-process"></span> | Processing step |
| <span class="dg-swatch dg-filter"></span> | Admission headroom and quota check |
| <span class="dg-swatch dg-decision"></span> | Decision |
| <span class="dg-swatch dg-reject"></span> | Rejection or refusal |
| <span class="dg-swatch dg-background"></span> | Background service |
