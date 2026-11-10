---
title: "Background Services"
linkTitle: "Background Services"
description: "Interactive diagram of every loop and long-running goroutine the server and agent run, with its interval, what it touches and how it fails."
weight: 8
---

Every loop the server and the agent run, with its interval and what it touches. The server runs five upkeep tickers plus one goroutine per dispatch, lease, agent session and followed log; the agent holds one connection to the server and reports its workloads on a timer. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart LR',
    '    SRV([vagabond<br>server]):::entry',
    '    CAPS[Capability<br>refresh 1m]:::background',
    '    USAGE[Usage<br>refresh 15s]:::background',
    '    REAP[Reservation<br>reaper 5m]:::background',
    '    RELEASE[Provider<br>release 5m]:::background',
    '    CLAIM[Dispatch<br>claim 30s]:::background',
    '    API([HTTP<br>requests]):::entry',
    '    HEALTH[Store<br>health]:::filter',
    '    FOLLOW[Live output<br>follow]:::process',
    '    RUNNER[Dispatch<br>runner]:::process',
    '    LEASE[Lease<br>renewal 20s]:::process',
    '    POOLRES[Pool<br>reservations]:::filter',
    '    REGSNAP[(Capability<br>snapshots)]:::storage',
    '    LEDSNAP[(Usage<br>snapshot)]:::storage',
    '    PROVIDERS[Providers]:::data',
    '    STORE[(Store)]:::storage',
    '    LISTENER[Agent<br>listener]:::process',
    '    NODES[(Connected<br>nodes)]:::storage',
    '    AGENT([vagabond<br>agent]):::entry',
    '    CONN[Server<br>connection]:::process',
    '    REPORT[Workload<br>report 10s]:::background',
    '    TIMEOUT[Timeout<br>watcher]:::process',
    '    CONTAINERD[(containerd)]:::data',
    '',
    '    SRV --> CAPS',
    '    SRV --> USAGE',
    '    SRV --> REAP',
    '    SRV --> RELEASE',
    '    SRV --> CLAIM',
    '    SRV --> LISTENER',
    '',
    '    API --> RUNNER',
    '    API --> HEALTH',
    '    API --> FOLLOW',
    '    CLAIM -->|resume| RUNNER',
    '    RUNNER --> LEASE',
    '    RUNNER --> POOLRES',
    '',
    '    CAPS --> PROVIDERS',
    '    CAPS --> REGSNAP',
    '    USAGE --> STORE',
    '    USAGE --> LEDSNAP',
    '    REAP --> STORE',
    '    REAP --> PROVIDERS',
    '    RELEASE --> STORE',
    '    RELEASE --> PROVIDERS',
    '    CLAIM --> STORE',
    '    RUNNER --> STORE',
    '    RUNNER --> PROVIDERS',
    '    LEASE --> STORE',
    '    HEALTH --> STORE',
    '    FOLLOW --> STORE',
    '    FOLLOW --> PROVIDERS',
    '    LISTENER --> NODES',
    '    POOLRES --> NODES',
    '',
    '    AGENT --> CONN',
    '    AGENT --> TIMEOUT',
    '    CONN -->|dials| LISTENER',
    '    CONN --> REPORT',
    '    REPORT -->|Node.Register| LISTENER',
    '    REPORT --> CONTAINERD',
    '    TIMEOUT --> CONTAINERD',
  ].join('\n'),

  info: {
    SRV: {
      title: 'vagabond server',
      badge: 'entry', badgeText: 'process',
      body: '<p><code>internal/cli/server.go</code> builds the registry and refreshes every provider, opens, migrates and verifies the store (or builds memory stores under <code>-dev</code>), and reads quota usage. A server that cannot read usage does not start.</p><p>It then starts the agent listener and listens on the API address. <code>Server.ServeListener</code> claims lapsed dispatches and reaps stale reservations once, starts the five upkeep tickers in <code>internal/server/upkeep.go</code>, and serves HTTP.</p><p>Each tick runs synchronously; a tick longer than its interval is followed at once by the next, and ticks do not pile up. A failure is logged and waits for the next tick, and one loop failing never stops another. No loop applies jitter or takes a lock across servers: where two servers could collide, the store statement decides.</p><p><a href="../../docs/background-services/">Background services reference &rarr;</a></p>'
    },
    API: {
      title: 'HTTP API',
      badge: 'entry', badgeText: 'per request',
      body: '<p>Requests start the per-dispatch and per-request goroutines: <code>POST /v1/jobs/run</code> and <code>POST /v1/job/{name}/dispatch</code> start a dispatch runner, <code>GET /v1/health</code> pings the store, and <code>GET /v1/execution/{id}/logs?follow=true</code> opens a live output stream.</p><p>On SIGINT or SIGTERM in-flight requests get 10s and every live stream ends at once. Running dispatches are not cancelled.</p><p><a href="../architecture/">Architecture diagram &rarr;</a></p>'
    },
    CAPS: {
      title: 'Capability refresh',
      badge: 'background', badgeText: 'every 1 min',
      body: '<p><code>registry.Refresh</code> asks every enabled provider for <code>Capabilities</code> in parallel, each under a 30s timeout, and applies every answer at once.</p><p><b>At startup:</b> once while loading configuration, before the server is built.<br><b>Skips:</b> disabled providers, and <code>pool</code> providers, whose capabilities are built from connected nodes on every read.</p><p>A provider that answers is marked healthy and its snapshot replaced. One that fails is marked unhealthy and keeps its last snapshot; admission rejects it with <code>provider-unhealthy</code> until a refresh succeeds. Errors from every provider are collected and joined.</p><p class="dg-metric">Logs: warn <code>refreshing provider capabilities</code>. At startup the CLI prints <code>Some providers did not answer: ...</code> instead, and the server still starts.</p><p><a href="../../docs/background-services/#capability-refresh">Reference &rarr;</a></p>'
    },
    USAGE: {
      title: 'Usage snapshot refresh',
      badge: 'background', badgeText: 'every 15 s',
      body: '<p><code>ledger.Refresh</code> re-reads settled usage plus outstanding reservations for every quota pool&rsquo;s current period, from <code>quota_usage</code> and <code>quota_reservations</code>, so plans see what every server has charged.</p><p><b>At startup:</b> required. <code>ledger.New</code> loads the first snapshot, and a server that cannot read usage does not start (<code>could not read quota usage</code>).<br><b>Also:</b> before every task a dispatch runs.</p><p>A failed read keeps the last snapshot. Plans keep answering from it, and the reservation at dispatch is what enforces the limit. <code>UsageRefreshed</code> in <code>GET /v1/health</code> is when it was last read, and ages while reads fail.</p><p class="dg-metric">Logs: warn <code>refreshing quota usage</code></p><p><a href="../../docs/background-services/#usage-snapshot-refresh">Reference &rarr;</a></p>'
    },
    REAP: {
      title: 'Reservation reaper',
      badge: 'background', badgeText: 'every 5 min',
      body: '<p><code>Dispatcher.Reap</code> resolves quota reservations a process left when it died, or failed to settle, between reserving and settling. It selects reservations created more than 1h ago (<code>ledger.StaleAfter</code>) and asks each one&rsquo;s provider about the execution.</p><p><b>At startup:</b> once, before serving.</p><p><b>Verdicts:</b> provider no longer configured, an error, or not terminal: kept for the next tick. Unknown execution: dropped. <code>Status</code> unsupported (synchronous providers) or no end time: settled at the reserved amount. Terminal and never started: settled as zero duration. Started and ended: settled at the declared shape over the time it ran.</p><p>On Postgres the pass is one serializable transaction that claims rows <code>FOR UPDATE SKIP LOCKED</code> and holds them while providers are asked, so two servers never resolve the same reservation. Provider calls have no timeout of their own; a slow provider holds the pass.</p><p class="dg-metric">Logs: info <code>resolved abandoned quota reservations</code> with <code>count</code>; warn <code>resolving abandoned quota reservations</code></p><p><a href="../quotas/">Quotas diagram &rarr;</a></p>'
    },
    RELEASE: {
      title: 'Provider release',
      badge: 'background', badgeText: 'every 5 min',
      body: '<p><code>Dispatcher.ReleaseLeftovers</code> releases what providers still hold for executions that are over: a Cloud Run Job, a Code Engine job run, a stopped container on a pool node. Dispatch releases after it reads a result; a cancelled run, a provider that stopped answering, a failed <code>Result</code>, a server that died mid-run or a failed release leaves it to this loop.</p><p><b>At startup:</b> no; the first pass is one interval in.<br><b>Selects:</b> executions with no <code>released_at</code>, unchanged for more than 10m (<code>dispatch.ReleaseAfter</code>), every one each pass with no batch limit.</p><p><b>Per execution:</b> a <code>lost</code> one is reconciled first, its result recorded if the provider reports it over and <code>failed</code> if the provider no longer knows it or 24h passed; one still unresolved is skipped. One still holding a reservation is skipped until the reaper settles it. A provider no longer configured, or with no <code>Release</code>, is marked released. Without a recorded result, the provider&rsquo;s <code>Status</code> must be terminal first. Then <code>Release</code>, and the record is marked released.</p><p>Releases and marks are idempotent, so several servers running the loop is harmless. A pool node that is offline fails <code>Release</code> and is retried every pass.</p><p class="dg-metric">Logs: info <code>released provider leftovers</code> with <code>count</code>; warn <code>releasing provider leftovers</code> with the joined errors</p><p><a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    },
    CLAIM: {
      title: 'Dispatch claim',
      badge: 'background', badgeText: 'every 30 s',
      body: '<p><code>Server.claim</code> takes over running dispatches whose owner stopped renewing the lease. <code>ClaimDispatches</code> gives every <code>running</code> dispatch whose lease expired, except those this server is running, this server as owner and a lease 60s from now, in one statement, so two servers never claim the same dispatch.</p><p><b>At startup:</b> once, before serving.</p><p>Each claimed dispatch is resumed in its own goroutine on a context that outlives server shutdown. A dispatch whose executions cannot be read stays <code>running</code>; its new lease lapses and a later claim tries again.</p><p class="dg-metric">Logs: warn <code>claiming abandoned dispatches</code>; per dispatch, info <code>dispatch resumed</code> with <code>previous_owner</code>, then info <code>resumed dispatch finished</code> with <code>state</code> and <code>reason</code>, or warn <code>resuming dispatch</code></p><p><a href="../task-path/">Task path diagram &rarr;</a> &middot; <a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    },
    RUNNER: {
      title: 'Dispatch runner',
      badge: 'process', badgeText: 'per dispatch',
      body: '<p>One goroutine per dispatch started through <code>POST /v1/jobs/run</code> or <code>POST /v1/job/{name}/dispatch</code>, or resumed by a claim. Runs the job&rsquo;s tasks in order and records how the dispatch ended: <code>succeeded</code>, <code>failed</code> or <code>unanswered</code>.</p><p>Runs on a context detached from the request, so the request returns the dispatch ID at once. Cancelled only by <code>DELETE /v1/execution/{id}</code> on one of its executions. Shutdown does not stop it; the process exit does, and the dispatch is resumed once its lease lapses.</p><p>Inside it, <code>Status</code> polls back off from 2s to 15s, and retry submissions wait <code>backoff.initial</code> (default 5s) doubling to <code>backoff.max</code> (default 30s). A resumed dispatch follows only the executions already created: the job file is not stored, so tasks not yet reached are not run.</p><p class="dg-metric">Logs: info <code>dispatch started</code>; info <code>dispatch finished</code> with <code>tasks</code> and <code>succeeded</code>, or warn <code>dispatch finished without an answer</code> with <code>tasks</code> and <code>error</code></p><p><a href="../task-path/">Task path diagram &rarr;</a> &middot; <a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    },
    LEASE: {
      title: 'Lease renewal',
      badge: 'process', badgeText: 'every 20 s per dispatch',
      body: '<p>One goroutine per running or resumed dispatch, extending its lease to 60s from each renewal through <code>RenewDispatch</code>. The first renewal is 20s after the dispatch starts, and each write has a 10s timeout.</p><p>Any failure but a stale lease is ignored: two renewals can fail before the lease lapses. A stale lease (the dispatch ended, or another server claimed it) marks the lease lost: the dispatch starts no further task (<code>ErrLeaseLost</code>) and does not write its ending.</p><p>Runs on a context detached from the dispatch, so a cancelled dispatch keeps its lease until its ending is recorded. Stops when the dispatch ends or the lease is found taken.</p><p class="dg-metric">Logs: none</p><p><a href="../../docs/dispatch/#leases">Leases &rarr;</a></p>'
    },
    POOLRES: {
      title: 'Pool reservations',
      badge: 'filter', badgeText: 'checked on read',
      body: '<p>Not a loop. A <code>pool</code> provider holds room on a node between submitting a workload and the node reporting it, so two dispatches on this server cannot both take the last room. Reservations are in memory, per server process.</p><p><b>Covers:</b> 2m, or until the node reports the workload.<br><b>Removed:</b> on <code>Release</code>, or when <code>Submit</code> to the node fails.<br><b>Departed node grace:</b> 5m, during which its executions report <code>lost</code>.<br><b>After the pool is built:</b> 5m during which an execution no node has reported is <code>lost</code>, not unknown.</p><p>An expired reservation stops counting against the node but stays in memory until its execution is released. Both grace periods are compared against the clock when a status is read.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    LISTENER: {
      title: 'Agent listener',
      badge: 'process', badgeText: 'long-running',
      body: '<p><code>Server.ServeAgents</code> accepts agent connections on <code>agent_bind</code> and runs one goroutine per session: on an <code>agent_tls</code> listener a TLS handshake within 10s, then a yamux session serving the <code>Node</code> gRPC endpoint.</p><p>A node is registered while its session holds. yamux sends a keepalive every 30s on both sides and closes a session whose keepalive fails, which is how a dead peer is detected. When the session ends, the node is removed and its departure time recorded for the pool grace period. An agent reconnecting under the same name replaces and closes its old session. On shutdown the listener closes and every session is closed.</p><p class="dg-metric">Logs: info <code>serving agents</code>; info <code>node registered</code>, only for a node not already connected; warn <code>node disconnected</code>; warn <code>agent connection</code> when a session cannot be opened</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    HEALTH: {
      title: 'Store health',
      badge: 'filter', badgeText: 'per request',
      body: '<p>Not a loop. <code>GET /v1/health</code> pings the store on each request with a 5s timeout: 200 with <code>Store</code> <code>ok</code>, or 503 with <code>Store</code> <code>unreachable</code> and <code>StoreError</code>. It also reports <code>UsageRefreshed</code> and how many dispatches this server is running.</p><p>The server recovers from an outage through its loops: each retries on its next tick, the claim picks up dispatches whose ending was not written, and the reaper settles reservations that were not.</p><p><a href="../../docs/dispatch/#store-outage">Store outage &rarr;</a></p>'
    },
    FOLLOW: {
      title: 'Live output follow',
      badge: 'process', badgeText: 'per request',
      body: '<p><code>Server.follow</code>, one per open <code>?follow=true</code> request on an execution that has not ended, whose provider is a <code>LogStreamer</code> (<code>cloud-run</code>, <code>code-engine</code>, <code>pool</code>). Copies <code>StreamLogs</code> output to the client, flushing every write.</p><p>A second goroutine re-reads the execution record every 1s; once it has ended, the stream ends 5s later, since some providers&rsquo; logs trail the work. A record that cannot be read is retried. It ends at once when the client goes or shutdown begins.</p><p class="dg-metric">Logs: warn <code>log stream ended early</code> when the provider stream breaks</p><p><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    AGENT: {
      title: 'vagabond agent',
      badge: 'entry', badgeText: 'node process',
      body: '<p><code>internal/cli/agent.go</code> reads the node (<code>agent/fingerprint</code>), delegates its cgroup unless <code>-cgroup-parent</code> is set, connects to containerd, re-arms the timeout of every workload already in the <code>vagabond</code> namespace, then dials the server until SIGINT or SIGTERM.</p><p>A failure before the dial exits with status 1. A server that cannot be reached is not a startup failure; the agent keeps retrying.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    CONN: {
      title: 'Server connection',
      badge: 'process', badgeText: 'reconnect 1 s to 30 s',
      body: '<p><code>Agent.Run</code> holds one session to the server and dials again whenever it drops. A dial, TLS handshake or registration that fails counts as a failed connection and doubles the wait, from 1s to 30s. A session that registered and then dropped resets it to 1s.</p><p>Keepalive is yamux&rsquo;s, every 30s. Workloads keep running while disconnected; they belong to containerd, and the agent reports them when it reconnects.</p><p class="dg-metric">Logs: info <code>connected</code> with <code>server</code>, <code>name</code> and <code>pool</code>; warn <code>lost the server</code>; warn <code>connecting to the server</code> with <code>error</code> and <code>retry</code>, the next wait</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    REPORT: {
      title: 'Workload report',
      badge: 'background', badgeText: 'every 10 s',
      body: '<p>Re-registers the node through <code>Node.Register</code> with every workload containerd holds, each with its declared CPU and memory and whether it is still running. The server sizes the node&rsquo;s free room from these reports.</p><p><b>Interval:</b> 10s, which catches a workload finishing on its own.<br><b>Also:</b> on connecting, after a <code>Submit</code> that started a workload, and after every <code>Cancel</code> or <code>Release</code>.</p><p>Change signals coalesce: one pending signal covers any number of changes. A failed report is not fatal; the next one carries everything, and a connection that is really gone ends through the session closing.</p><p class="dg-metric">Logs: warn <code>reporting workloads</code> with <code>error</code></p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    TIMEOUT: {
      title: 'Timeout watcher',
      badge: 'process', badgeText: 'per workload',
      body: '<p>One goroutine per workload that declares a timeout, stopping it once it has run that long: SIGTERM, then SIGKILL 10s later if it has not exited.</p><p><b>Deadline:</b> the workload&rsquo;s start plus its timeout, both stored as container labels (<code>vagabond.started</code>, <code>vagabond.timeout</code>), so a restarted agent watches the same deadline.<br><b>Started:</b> on submit, and for every held workload when the agent starts.</p><p>A workload without a timeout gets no watcher. A watcher that cannot load the task exits and leaves the workload running.</p><p class="dg-metric">Logs: none</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    STORE: {
      title: 'Store (Postgres or CockroachDB)',
      badge: 'storage', badgeText: 'shared state',
      body: '<p><code>state/postgres</code>, or memory stores under <code>-dev</code>. The loops read and write <code>dispatches</code> (owner and lease), <code>executions</code> (<code>released_at</code>), <code>quota_usage</code> and <code>quota_reservations</code>.</p><p>Several servers can share one store. Every loop runs on every server, and the store keeps them apart: the claim is one <code>UPDATE ... RETURNING</code>, so a lapsed dispatch goes to exactly one server; the reaper resolves each stale reservation under a <code>SKIP LOCKED</code> row lock; releases are idempotent; the refreshes are per-server reads.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    PROVIDERS: {
      title: 'Providers',
      badge: 'data', badgeText: 'external',
      body: '<p>The configured plugins and the platforms behind them: <code>cloud-run</code>, <code>code-engine</code>, <code>lambda</code> and <code>pool</code>. The loops call <code>Capabilities</code> (capability refresh, pools excepted), <code>Status</code> (reaper, release), <code>Result</code> and <code>Release</code> (release), and <code>StreamLogs</code> (live output).</p><p>A call made through dispatch that the platform rejects as unauthorized has the provider&rsquo;s credential resolved again from its source, at most once a minute, and is made once more.</p>'
    },
    REGSNAP: {
      title: 'Capability snapshots',
      badge: 'storage', badgeText: 'server memory',
      body: '<p>Held by the registry: each provider&rsquo;s capabilities with their observation time, and whether it answered its last refresh. Admission reads them on every plan and dispatch without calling anything.</p><p>Not persisted; rebuilt by the startup refresh. A <code>pool</code> provider&rsquo;s capabilities are built from connected nodes on every read instead.</p>'
    },
    LEDSNAP: {
      title: 'Usage snapshot',
      badge: 'storage', badgeText: 'server memory',
      body: '<p>Held by the ledger: settled usage plus outstanding reservations per pool and period. Admission reads it for the quota check and <code>provider.free_quota_percent</code>, and <code>GET /v1/providers</code> reports it.</p><p>Up to 15s old in a plan. Reserving is decided by the store in one statement, so a stale snapshot never lets two dispatches over-commit a pool.</p><p><a href="../quotas/">Quotas diagram &rarr;</a></p>'
    },
    NODES: {
      title: 'Connected nodes',
      badge: 'storage', badgeText: 'server memory',
      body: '<p><code>nodes.Conns</code>: each connected node with its last report and session, and when each departed node left. Filled by the agent listener, read by the pool providers and <code>GET /v1/nodes</code>.</p><p>Not persisted. After a server restart, agents reconnect with backoff and report again.</p>'
    },
    CONTAINERD: {
      title: 'containerd',
      badge: 'data', badgeText: 'node runtime',
      body: '<p>The node&rsquo;s containerd, <code>vagabond</code> namespace. It owns the workloads, so they keep running while the agent is disconnected. The workload report lists its containers; the timeout watcher stops their tasks.</p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Processes and the API that start the loops |
| <span class="dg-swatch dg-background"></span> | Periodic loop |
| <span class="dg-swatch dg-process"></span> | Goroutine per dispatch, session, request or workload |
| <span class="dg-swatch dg-filter"></span> | Checked when read; not a loop |
| <span class="dg-swatch dg-storage"></span> | State the loops read and write |
| <span class="dg-swatch dg-success"></span> | External systems: provider platforms, containerd |

## Service summary

| Service | Runs in | Interval | At startup | Code |
|---------|---------|----------|------------|------|
| Capability refresh | Server | 1m | Yes, before the server is built | `registry.Refresh` |
| Usage snapshot refresh | Server | 15s, and before each task | Yes, required | `ledger.Refresh` |
| Reservation reaper | Server | 5m | Yes, before serving | `Dispatcher.Reap` |
| Provider release | Server | 5m | No | `Dispatcher.ReleaseLeftovers` |
| Dispatch claim | Server | 30s | Yes, before serving | `Server.claim` |
| Dispatch runner | Server | Per dispatch | | `Dispatcher.Run`, `Dispatcher.Resume` |
| Lease renewal | Server | 20s per dispatch | | `Dispatcher.hold` |
| Agent listener | Server | Long-running | Yes | `Server.ServeAgents` |
| Live output follow | Server | Per request, record re-read every 1s | | `Server.follow` |
| Server connection | Agent | Backoff 1s to 30s | Yes | `Agent.Run` |
| Workload report | Agent | 10s, and on every change | On connecting | `Agent.connect` |
| Timeout watcher | Agent | Per workload | For every held workload | `Containerd.watch` |
