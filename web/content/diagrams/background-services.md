---
title: "Background Services"
linkTitle: "Background Services"
description: "Interactive diagram of every loop and long-running goroutine the server and agent run, with its interval, what it touches and how it fails."
weight: 9
---

Every loop the server and the agent run, with its interval and what it touches. The server runs seven upkeep loops plus one goroutine per dispatch, lease, execution, output capture, agent session and followed log; the agent holds one connection to the server and reports its workloads on a timer. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart LR',
    '    SRV([vagabond<br>server]):::entry',
    '    CAPS[Capability<br>refresh 1m]:::background',
    '    USAGE[Usage<br>refresh 15s]:::background',
    '    DISABLED[Disabled provider<br>read 5s]:::background',
    '    REAP[Reservation<br>reaper 5m]:::background',
    '    CLAIM[Dispatch<br>claim 30s]:::background',
    '    PRUNE[Output<br>retention 1h]:::background',
    '    LAUNCH[Periodic<br>launches]:::background',
    '    API([HTTP<br>requests]):::entry',
    '    HEALTH[Store<br>health]:::filter',
    '    FOLLOW[Output<br>follow]:::process',
    '    CAPTURE[Output<br>capture]:::process',
    '    DISPATCH[Dispatch<br>run]:::process',
    '    LEASE[Lease<br>renewal 20s]:::process',
    '    RUNNER[Execution<br>runner]:::process',
    '    POOLRES[Node<br>reservations]:::filter',
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
    '    FIRECRACKER[(vagabond-firecracker:<br>supervision, timeouts)]:::data',
    '',
    '    SRV --> CAPS',
    '    SRV --> USAGE',
    '    SRV --> DISABLED',
    '    SRV --> REAP',
    '    SRV --> CLAIM',
    '    SRV --> PRUNE',
    '    SRV --> LAUNCH',
    '    SRV --> LISTENER',
    '',
    '    API --> DISPATCH',
    '    API --> HEALTH',
    '    API --> FOLLOW',
    '    API -->|cancel, disable, enable| STORE',
    '    CLAIM -->|resume| DISPATCH',
    '    LAUNCH -->|start| DISPATCH',
    '    API -->|register, stop: wake| LAUNCH',
    '    DISPATCH --> LEASE',
    '    DISPATCH -->|per attempt| RUNNER',
    '    LEASE -->|cancel, lease lost| RUNNER',
    '    DISPATCH -->|reserve| POOLRES',
    '    RUNNER -->|give back| POOLRES',
    '',
    '    CAPS --> PROVIDERS',
    '    CAPS --> REGSNAP',
    '    USAGE --> STORE',
    '    USAGE --> LEDSNAP',
    '    DISABLED --> STORE',
    '    DISABLED --> REGSNAP',
    '    REAP --> STORE',
    '    CLAIM --> STORE',
    '    PRUNE --> STORE',
    '    LAUNCH -->|claim| STORE',
    '    DISPATCH --> STORE',
    '    RUNNER --> STORE',
    '    RUNNER --> PROVIDERS',
    '    RUNNER --> CAPTURE',
    '    CAPTURE --> PROVIDERS',
    '    CAPTURE --> STORE',
    '    LEASE --> STORE',
    '    HEALTH --> STORE',
    '    FOLLOW --> STORE',
    '    LISTENER --> NODES',
    '    POOLRES --> NODES',
    '',
    '    AGENT --> CONN',
    '    AGENT --> TIMEOUT',
    '    CONN -->|dials| LISTENER',
    '    CONN --> REPORT',
    '    REPORT -->|Node.Register| LISTENER',
    '    REPORT --> CONTAINERD',
    '    REPORT -->|Held| FIRECRACKER',
    '    TIMEOUT --> CONTAINERD',
  ].join('\n'),

  info: {
    SRV: {
      title: 'vagabond server',
      badge: 'entry', badgeText: 'process',
      body: '<p><code>internal/cli/server.go</code> builds the registry and refreshes every provider, opens, migrates and verifies the store (or builds memory stores under <code>-dev</code>), and reads quota usage. A server that cannot read usage does not start.</p><p>It then starts the agent listener and listens on the API address. <code>Server.ServeListener</code> reads the disabled providers, claims lapsed dispatches and reaps stale reservations once, in that order, starts the seven upkeep loops in <code>internal/server/upkeep.go</code>, and serves HTTP. Periodic launches get their first pass at once, beside serving.</p><p>Each tick runs synchronously; a tick longer than its interval is followed at once by the next, and ticks do not pile up. A failure is logged and waits for the next tick, and one loop failing never stops another. No loop applies jitter or takes a lock across servers: where two servers could collide, the store statement decides.</p><p><a href="../../docs/background-services/">Background services reference &rarr;</a></p>'
    },
    API: {
      title: 'HTTP API',
      badge: 'entry', badgeText: 'per request',
      body: '<p>Requests start the per-dispatch and per-request goroutines: <code>POST /v1/jobs/run</code> and <code>POST /v1/job/{name}/dispatch</code> start a dispatch, <code>GET /v1/health</code> pings the store, and <code>GET /v1/execution/{id}/logs?follow=true</code> opens a live output stream.</p><p><code>DELETE /v1/execution/{id}</code> cancels a dispatch this server runs at once. For one held elsewhere it writes <code>cancel_requested_at</code> on the dispatch, keeping the first request&rsquo;s time, and returns the execution as it stands; the owner sees the request at its next lease renewal.</p><p>On SIGINT or SIGTERM in-flight requests get 10s and every live stream ends at once. Running dispatches are not cancelled.</p><p><a href="../architecture/">Architecture diagram &rarr;</a></p>'
    },
    CAPS: {
      title: 'Capability refresh',
      badge: 'background', badgeText: 'every 1 min',
      body: '<p><code>registry.Refresh</code> asks every enabled provider for <code>Capabilities</code> in parallel, each under a 30s timeout, and applies every answer at once.</p><p><b>At startup:</b> once while loading configuration, before the server is built.<br><b>Skips:</b> providers with <code>enabled = false</code>, and <code>containerd</code> and <code>firecracker</code> providers, whose capabilities are built from connected nodes on every read. A provider disabled through the API is still asked, so its health is current when it is enabled again.</p><p>A provider that answers is marked healthy and its snapshot replaced. One that fails is marked unhealthy and keeps its last snapshot; admission rejects it with <code>provider-unhealthy</code> until a refresh succeeds. Errors from every provider are collected and joined.</p><p class="dg-metric">Logs: warn <code>refreshing provider capabilities</code>. At startup the CLI prints <code>Some providers did not answer: ...</code> instead, and the server still starts.</p><p><a href="../../docs/background-services/#capability-refresh">Reference &rarr;</a></p>'
    },
    USAGE: {
      title: 'Usage snapshot refresh',
      badge: 'background', badgeText: 'every 15 s',
      body: '<p><code>ledger.Refresh</code> re-reads settled usage plus outstanding reservations for every quota pool&rsquo;s current period, from <code>quota_usage</code> and <code>quota_reservations</code>, so plans see what every server has charged.</p><p><b>At startup:</b> required. <code>ledger.New</code> loads the first snapshot, and a server that cannot read usage does not start (<code>could not read quota usage</code>).<br><b>Also:</b> before every task a dispatch runs.</p><p>A failed read keeps the last snapshot. Plans keep answering from it, and the reservation at dispatch is what enforces the limit. <code>UsageRefreshed</code> in <code>GET /v1/health</code> is when it was last read, and ages while reads fail.</p><p class="dg-metric">Logs: warn <code>refreshing quota usage</code></p><p><a href="../../docs/background-services/#usage-snapshot-refresh">Reference &rarr;</a></p>'
    },
    DISABLED: {
      title: 'Disabled provider read',
      badge: 'background', badgeText: 'every 5 s',
      body: '<p><code>Server.refreshDisabled</code> reads the whole <code>disabled_providers</code> table and hands it to the registry, so this server rejects a provider that any server sharing the store disabled with <code>vagabond provider disable</code>.</p><p><b>At startup:</b> once, before serving and before the dispatch claim, so resumed dispatches already avoid those providers.<br><b>Also:</b> after every disable or enable this server takes, before it answers.</p><p>A failed read keeps the last set. Under <code>-dev</code> the set is in memory and only this server sees it.</p><p class="dg-metric">Logs: warn <code>reading disabled providers</code></p><p><a href="../../docs/background-services/#disabled-provider-read">Reference &rarr;</a></p>'
    },
    REAP: {
      title: 'Reservation reaper',
      badge: 'background', badgeText: 'every 5 min',
      body: '<p><code>Dispatcher.Reap</code> drops quota reservations no execution was recorded for: the process died, or the store refused the record, between reserving and recording. It selects reservations created more than 1h ago (<code>ledger.StaleAfter</code>) and drops, charging nothing, each one whose execution record does not exist.</p><p><b>At startup:</b> once, before serving.</p><p>A reservation whose execution is recorded is left to that execution&rsquo;s runner, and one whose record cannot be read is kept for the next tick. No provider is called.</p><p>On Postgres the pass is one serializable transaction that claims rows <code>FOR UPDATE SKIP LOCKED</code> and holds them while each record is looked up, so two servers never resolve the same reservation.</p><p class="dg-metric">Logs: info <code>resolved abandoned quota reservations</code> with <code>count</code>; warn <code>resolving abandoned quota reservations</code></p><p><a href="../quotas/">Quotas diagram &rarr;</a></p>'
    },
    PRUNE: {
      title: 'Output retention',
      badge: 'background', badgeText: 'every 1h',
      body: '<p><code>Server.prune</code> deletes the stored output of executions that ended longer ago than the server&rsquo;s <code>log_retention</code> (default <code>168h</code>), through <code>PruneLogs</code>. The execution records stay; once pruned, <code>GET /v1/execution/{id}/logs</code> answers 200 with an empty body.</p><p><b>At startup:</b> no; the first pass is one interval in.</p><p>One statement per pass, driven from the <code>execution_logs</code> chunks retention keeps, joined to each chunk&rsquo;s execution for its <code>ended_at</code>. Deleting what another server already deleted deletes nothing, so several servers running it is harmless.</p><p class="dg-metric">Logs: info <code>pruned stored output</code> with <code>executions</code>; warn <code>pruning stored output</code></p><p><a href="../../docs/background-services/#output-retention">Output retention &rarr;</a></p>'
    },
    LAUNCH: {
      title: 'Periodic launches',
      badge: 'background', badgeText: 'at each launch, 1 min at most',
      body: '<p><code>Server.launchPass</code> reads every job with a schedule that is not stopped, through <code>PeriodicJobs</code>, and launches each whose next match after its last launch has come. Several missed matches make one launch.</p><p><b>At startup:</b> at once, without holding up serving. Each pass returns when the next should run: the earliest next launch, a minute away at most, so a job registered through another server is seen within a minute. A register or stop on this server wakes it at once.</p><p>Each launch is claimed with <code>ClaimLaunch</code>, one <code>UPDATE</code> that moves the job&rsquo;s <code>periodic_launches.launched_at</code> from the value read to now while the job is at the version read and not stopped. Only the server whose claim changed the row goes on, so the launch is made once however many servers run the loop. With <code>prohibit_overlap</code> it then asks <code>RunningDispatch</code> and skips the launch, still counted as made, while a dispatch of the job runs. Otherwise it loads the claimed version with no metadata and starts it through <code>Server.launch</code>, as <code>job dispatch</code> does.</p><p>One job failing does not hold up the others, and a failed launch waits for the job&rsquo;s next match.</p><p class="dg-metric">Logs: info <code>periodic launch</code> with <code>dispatch</code>; info <code>periodic launch skipped</code>; warn <code>launching periodic jobs</code></p><p><a href="../../docs/background-services/#periodic-launches">Periodic launches &rarr;</a></p>'
    },
    CLAIM: {
      title: 'Dispatch claim',
      badge: 'background', badgeText: 'every 30 s',
      body: '<p><code>Server.claim</code> takes over dispatches whose owner stopped renewing the lease. <code>ClaimDispatches</code> gives every dispatch with no <code>done_at</code> whose lease expired, except those this server is running, this server as owner, the next epoch and a lease 60s from now, in one statement, so two servers never claim the same dispatch. That covers running dispatches and ended ones whose executions are still settling or releasing. From the claim on, every write under the previous epoch is refused.</p><p><b>At startup:</b> once, before serving.</p><p>Each claimed dispatch is resumed in its own goroutine on a context that outlives server shutdown. The lease is held, a cancel requested before the claim is acted on at once, and a runner is restored for every execution not yet terminal, collected and released, starting at the first step its record says is left. An ended dispatch keeps its ending; a running one is given one from its executions. Either is recorded done once every runner has finished. A dispatch whose executions cannot be read stays open; its new lease lapses and a later claim tries again.</p><p class="dg-metric">Logs: warn <code>claiming abandoned dispatches</code>; per dispatch, info <code>dispatch resumed</code> with <code>epoch</code>, then info <code>resumed dispatch finished</code> with <code>state</code> and <code>reason</code>, or warn <code>resuming dispatch</code></p><p><a href="../task-path/">Task path diagram &rarr;</a> &middot; <a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    },
    DISPATCH: {
      title: 'Dispatch run',
      badge: 'process', badgeText: 'per dispatch',
      body: '<p>One goroutine per dispatch started through <code>POST /v1/jobs/run</code> or <code>POST /v1/job/{name}/dispatch</code>, started by a periodic launch, or resumed by a claim. Runs the job&rsquo;s tasks in order, starting an execution runner for each attempt and waiting for its first answer, and records how the dispatch ended: <code>succeeded</code>, <code>failed</code> or <code>unanswered</code>. It then waits for every runner to finish through release and records the dispatch done (<code>done_at</code>), which ends the lease.</p><p>Runs on a context detached from the request, so the request returns the dispatch ID at once. Cancelled by <code>DELETE /v1/execution/{id}</code> on one of its executions: at once on this server, or through a cancel request seen at the next lease renewal. Shutdown does not stop it; the process exit does, and the dispatch is resumed once its lease lapses.</p><p>Retry submissions wait <code>backoff.initial</code> (default 5s) doubling to <code>backoff.max</code> (default 30s). A resumed dispatch follows only the executions already created: the job file is not stored, so tasks not yet reached are not run.</p><p class="dg-metric">Logs: info <code>dispatch started</code>; once the dispatch is done, info <code>dispatch finished</code> with <code>tasks</code> and <code>succeeded</code>, or warn <code>dispatch finished without an answer</code> with <code>tasks</code> and <code>error</code></p><p>A panic in it ends the dispatch <code>unanswered</code> with the panic as its error, logged as <code>recovered a panic</code> with the stack. A dispatch claimed past 5 resumes is ended without running anything.</p><p><a href="../task-path/">Task path diagram &rarr;</a> &middot; <a href="../execution-states/">Execution states diagram &rarr;</a> &middot; <a href="../../docs/dispatch/#panics">Panics &rarr;</a></p>'
    },
    LEASE: {
      title: 'Lease renewal',
      badge: 'process', badgeText: 'every 20 s per dispatch',
      body: '<p>One goroutine per held dispatch, started or claimed, extending its lease to 60s from each renewal through <code>RenewDispatch</code>. The first renewal is 20s after the lease is taken, and each write has a 10s timeout. A renewal applies only while the owner and epoch match and the dispatch is not done, and returns any cancel requested for it.</p><p>A cancel request stops every runner under the lease, each cancelling its execution, and the dispatch starts no further task. A refused renewal means another server claimed the dispatch: the lease is lost, every runner stops without cancelling or releasing its execution (<code>ErrLeaseLost</code>), and the dispatch writes no ending. Any other failure waits for the next tick: two renewals can fail before the lease lapses.</p><p>Runs on a context detached from the dispatch, so a cancelled dispatch keeps its lease until its executions are released. Stops once the dispatch is recorded done or the lease is lost. A panic in it loses the lease.</p><p class="dg-metric">Logs: none, but <code>recovered a panic</code> for a panic</p><p><a href="../../docs/dispatch/#leases">Leases &rarr;</a></p>'
    },
    RUNNER: {
      title: 'Execution runner',
      badge: 'process', badgeText: 'per execution',
      body: '<p>One goroutine per execution, under its dispatch&rsquo;s lease and the only writer of its record: submit, watch, collect, settle, release. Each step leaves a mark on the record, so a runner restored after a claim starts at the first step not done. The attempt takes the runner&rsquo;s first answer, once the execution is terminal, lost or failed to submit; settling and releasing follow.</p><p><b>Watch:</b> <code>Status</code> polls back off from 2s to 15s. A failed poll makes the execution <code>lost</code>, and polling goes on; one the provider no longer knows, or still failing after 24h lost, is <code>failed</code>.<br><b>Cancel:</b> the provider&rsquo;s <code>Cancel</code>, then one <code>Status</code> for the times it ran.<br><b>Release:</b> retried 250ms doubling to 10s until it succeeds or the provider no longer knows the execution.<br>Every <code>Status</code>, <code>Result</code>, <code>Cancel</code> and <code>Release</code> call has a 30s timeout.</p><p>Every write is checked against the lease. A refused one means another server claimed the dispatch: the runner stops and calls the provider no further. Any other store failure is retried, 250ms doubling to 10s with a 10s timeout per try, until the write lands.</p><p class="dg-metric">Logs: warn <code>releasing provider leftovers</code> with <code>execution</code> and <code>error</code>, per failed release; warn <code>store write failed, retrying</code> with <code>error</code></p><p>A panic in it records the execution <code>failed</code> as <code>internal</code> and answers its task, skipping settle and release.</p><p><a href="../task-path/">Task path diagram &rarr;</a> &middot; <a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    },
    POOLRES: {
      title: 'Node reservations',
      badge: 'filter', badgeText: 'checked on read',
      body: '<p>Not a loop. <code>nodes.Conns</code> holds room on a node between placing a workload and the node reporting it, so two dispatches on this server cannot both take the last room. Counted once per node, whatever providers draw on it. In memory, per server process.</p><p><b>Taken:</b> by dispatch, after the quota reservation and before the execution is recorded. A node without room, or not connected, is refused with <code>plugin.ErrNoRoom</code>, and dispatch goes on to the next candidate.<br><b>Dropped:</b> when the node&rsquo;s report includes the workload, after 2m, when the task does not translate or <code>Submit</code> fails, and on <code>Release</code>.</p><p>The 2m is compared against the clock when a node&rsquo;s room is read, and a reservation past it is dropped then.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    LISTENER: {
      title: 'Agent listener',
      badge: 'process', badgeText: 'long-running',
      body: '<p><code>Server.ServeAgents</code> runs <code>nodes.Conns.Serve</code> (<code>internal/nodes/serve.go</code>), which accepts agent connections on <code>agent_bind</code> and runs one goroutine per session: on an <code>agent_tls</code> listener a TLS handshake within 10s, then a yamux session serving the <code>Node</code> gRPC endpoint. Every registration is answered with the held workloads the server has no use for, from each one&rsquo;s execution record.</p><p>A node is registered while its session holds. yamux sends a keepalive every 30s on both sides and closes a session whose keepalive fails, which is how a dead peer is detected. When the session ends, the node is removed. An agent reconnecting under the same name replaces and closes its old session. A failed accept, such as one out of file descriptors, is retried, 5ms doubling to 1s; the listener closing while the server runs stops the server, API included. On shutdown the listener closes and every session is closed.</p><p class="dg-metric">Logs: info <code>serving agents</code>; info <code>node registered</code>, only for a node not already connected; warn <code>node disconnected</code>; warn <code>agent connection</code> when a session cannot be opened; warn <code>accepting agent connections failed, retrying</code> per failed accept</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    HEALTH: {
      title: 'Store health',
      badge: 'filter', badgeText: 'per request',
      body: '<p>Not a loop. <code>GET /v1/health</code> pings the store on each request with a 5s timeout: 200 with <code>Store</code> <code>ok</code>, or 503 with <code>Store</code> <code>unreachable</code> and <code>StoreError</code>. It also reports <code>UsageRefreshed</code> and how many dispatches this server is running.</p><p>The server recovers from an outage on its own: execution runners retry each write until the store takes it, a dispatch whose lease lapsed meanwhile is claimed and its runners restored while the old owner&rsquo;s writes are refused, the loops retry on their next tick, and the reaper drops reservations whose execution was never recorded.</p><p><a href="../../docs/dispatch/#store-outage">Store outage &rarr;</a></p>'
    },
    FOLLOW: {
      title: 'Output follow',
      badge: 'process', badgeText: 'per request',
      body: '<p><code>Server.sendLogs</code>, one per open <code>?follow=true</code> request, on whichever server it reaches. Sends the execution&rsquo;s stored output from <code>offset</code>, then reads the record and the store again every 1s and sends what was stored since, flushing every write. It never reaches the provider.</p><p>It ends after a read made once the record was terminal, which the runner writes only after the last of the output; at once when the client goes or shutdown begins. A record that cannot be read is tried again.</p><p class="dg-metric">Logs: warn <code>reading stored output ended early</code> when a read fails after the body began</p><p><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    CAPTURE: {
      title: 'Output capture',
      badge: 'process', badgeText: 'per execution',
      body: '<p><code>capture.run</code>, one per running execution whose provider holds its output (<code>cloud-run</code>, <code>cloud-build</code>, <code>code-engine</code>, <code>containerd</code>, <code>firecracker</code>), started by its runner once the provider has the work and ended by <code>collect</code>. Calls <code>CaptureLogs</code> until the output is whole, and a second goroutine stores what has arrived every 1s, so the store is never more than a second behind.</p><p>A call that returns early is made again after 1s doubling to 30s; once the execution has ended, three failed calls or 5 minutes end it with what it has, marked truncated. A capture a claimed dispatch restores begins from what the store holds. A write the lease refused stops it.</p><p class="dg-metric">Logs: warn <code>capturing output</code> with <code>execution</code> and <code>error</code> per call that returned early; warn <code>reading stored output back</code>; warn <code>output left incomplete</code></p><p><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    AGENT: {
      title: 'vagabond agent',
      badge: 'entry', badgeText: 'node process',
      body: '<p><code>internal/cli/agent.go</code> reads the node (<code>agent/fingerprint</code>), delegates its cgroup unless <code>-cgroup-parent</code> is set, connects to containerd, re-arms the timeout of every workload already in the <code>vagabond</code> namespace, asks each executor what it is, vagabond-firecracker too with <code>-firecracker</code>, then dials the server until SIGINT or SIGTERM.</p><p>A failure before the dial exits with status 1. A server that cannot be reached is not a startup failure; the agent keeps retrying.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    CONN: {
      title: 'Server connection',
      badge: 'process', badgeText: 'reconnect 1 s to 30 s',
      body: '<p><code>Agent.Run</code> holds one session to the server and dials again whenever it drops. A dial, TLS handshake or registration that fails counts as a failed connection and doubles the wait, from 1s to 30s. A session that registered and then dropped resets it to 1s.</p><p>Keepalive is yamux&rsquo;s, every 30s. Workloads keep running while disconnected; they belong to containerd, and the agent reports them when it reconnects.</p><p class="dg-metric">Logs: info <code>connected</code> with <code>server</code>, <code>name</code> and <code>pool</code>; warn <code>lost the server</code>; warn <code>connecting to the server</code> with <code>error</code> and <code>retry</code>, the next wait</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    REPORT: {
      title: 'Workload report',
      badge: 'background', badgeText: 'every 10 s',
      body: '<p>Re-registers the node through <code>Node.Register</code> with its executors and every workload they hold, containerd&rsquo;s containers and vagabond-firecracker&rsquo;s <code>Held</code>, each with what it takes, its declared CPU and memory plus its executor&rsquo;s overhead, and whether it is still running. The server sizes the node&rsquo;s free room from these reports.</p><p><b>Interval:</b> 10s, which catches a workload finishing on its own.<br><b>Also:</b> on connecting, after a <code>Submit</code> that started a workload, and after every <code>Cancel</code> or <code>Release</code>.</p><p>Change signals coalesce: one pending signal covers any number of changes. A failed report is not fatal; the next one carries everything, and a connection that is really gone ends through the session closing.</p><p>Each report is answered with the held workloads the server has no use for: released, never recorded, or not an execution ID. The agent releases each through its own <code>Release</code>; a failed one is answered again on the next report.</p><p class="dg-metric">Logs: warn <code>reporting workloads</code> with <code>error</code>; info <code>released a workload the server has no use for</code> with <code>execution</code>; warn <code>releasing a workload the server has no use for</code> with <code>execution</code> and <code>error</code></p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    TIMEOUT: {
      title: 'Timeout watcher',
      badge: 'process', badgeText: 'per workload',
      body: '<p>One goroutine per container that declares a timeout, stopping it once it has run that long: SIGTERM, then SIGKILL 10s later if it has not exited. A microVM&rsquo;s timeout is vagabond-firecracker&rsquo;s to watch.</p><p><b>Deadline:</b> the workload&rsquo;s start plus its timeout, both stored as container labels (<code>vagabond.started</code>, <code>vagabond.timeout</code>), so a restarted agent watches the same deadline.<br><b>Started:</b> on submit, and for every held workload when the agent starts.</p><p>A workload without a timeout gets no watcher. A watcher that cannot load the task exits and leaves the workload running.</p><p class="dg-metric">Logs: none</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    STORE: {
      title: 'Store (Postgres or CockroachDB)',
      badge: 'storage', badgeText: 'shared state',
      body: '<p><code>state/postgres</code>, or memory stores under <code>-dev</code>. The loops and runners read and write <code>dispatches</code> (owner, epoch, lease, cancel request, <code>done_at</code>), <code>executions</code> (the record and its <code>logs_at</code> and <code>released_at</code> marks), <code>execution_logs</code>, <code>quota_usage</code>, <code>quota_reservations</code>, and for periodic launches <code>jobs</code> and <code>periodic_launches</code>.</p><p>Several servers can share one store. Every loop runs on every server, and the store keeps them apart: the claim is one <code>UPDATE ... RETURNING</code> that moves the dispatch to its next epoch, so a lapsed dispatch goes to exactly one server and every write under the old lease is refused; the reaper resolves each stale reservation under a <code>SKIP LOCKED</code> row lock; a periodic launch is one <code>UPDATE</code> guarded by the last launch the server read, so one server makes it; the refreshes are per-server reads.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    PROVIDERS: {
      title: 'Providers',
      badge: 'data', badgeText: 'external',
      body: '<p>The configured plugins and the platforms behind them: <code>cloud-run</code>, <code>cloud-build</code>, <code>code-engine</code>, <code>lambda</code>, <code>containerd</code> and <code>firecracker</code>. The capability refresh calls <code>Capabilities</code> (<code>containerd</code> and <code>firecracker</code> providers excepted); execution runners call <code>Submit</code>, <code>Status</code>, <code>Cancel</code>, <code>Result</code> and <code>Release</code>, and their output captures <code>CaptureLogs</code>. The reaper and the output follow call none.</p><p>A call made through dispatch that the platform rejects as unauthorized has the provider&rsquo;s credential resolved again from its source, at most once a minute, and is made once more.</p>'
    },
    REGSNAP: {
      title: 'Capability snapshots',
      badge: 'storage', badgeText: 'server memory',
      body: '<p>Held by the registry: each provider&rsquo;s capabilities with their observation time, and whether it answered its last refresh. Admission reads them on every plan and dispatch without calling anything. Beside them, the providers operators disabled through the API, as the disabled provider read last found them.</p><p>Not persisted; rebuilt by the startup refresh and read. A <code>containerd</code> provider&rsquo;s capabilities are built from connected nodes on every read instead.</p>'
    },
    LEDSNAP: {
      title: 'Usage snapshot',
      badge: 'storage', badgeText: 'server memory',
      body: '<p>Held by the ledger: settled usage plus outstanding reservations per pool and period. Admission reads it for the quota check and <code>provider.free_quota_percent</code>, and <code>GET /v1/providers</code> reports it.</p><p>Up to 15s old in a plan. Reserving is decided by the store in one statement, so a stale snapshot never lets two dispatches over-commit a pool.</p><p><a href="../quotas/">Quotas diagram &rarr;</a></p>'
    },
    NODES: {
      title: 'Connected nodes',
      badge: 'storage', badgeText: 'server memory',
      body: '<p><code>nodes.Conns</code>: each connected node with its last report and session, and the room reserved on each. Filled by the agent listener, read and reserved on by the <code>containerd</code> providers, read by <code>GET /v1/nodes</code>.</p><p>Not persisted. After a server restart, agents reconnect with backoff and report again.</p>'
    },
    CONTAINERD: {
      title: 'containerd',
      badge: 'data', badgeText: 'node runtime',
      body: '<p>The node&rsquo;s containerd, <code>vagabond</code> namespace. It owns the workloads, so they keep running while the agent is disconnected. The workload report lists its containers; the timeout watcher stops their tasks.</p>'
    },
    FIRECRACKER: {
      title: 'vagabond-firecracker',
      badge: 'data', badgeText: 'microVMs',
      body: '<p>The daemon the agent starts to run its microVMs, with loops of its own, so a microVM&rsquo;s deadline holds and its output is kept while the agent is down:</p><p><b>Supervision:</b> one task per running microVM, copying its output to the execution&rsquo;s log, saving its end, and shutting the microVM down.<br><b>Timeout:</b> one per microVM with a timeout, from its saved start: SIGTERM, then SIGKILL 10 s later.<br><b>Recovery:</b> once at start, taking back what a previous daemon left.</p><p>The workload report reads its <code>Held</code>.</p><p><a href="../firecracker/">Firecracker diagram &rarr;</a></p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Processes and the API that start the loops |
| <span class="dg-swatch dg-background"></span> | Periodic loop |
| <span class="dg-swatch dg-process"></span> | Goroutine per dispatch, execution, session, request or workload |
| <span class="dg-swatch dg-filter"></span> | Checked when read; not a loop |
| <span class="dg-swatch dg-storage"></span> | State the loops read and write |
| <span class="dg-swatch dg-success"></span> | External systems: provider platforms, containerd, vagabond-firecracker |

## Service summary

| Service | Runs in | Interval | At startup | Code |
|---------|---------|----------|------------|------|
| Capability refresh | Server | 1m | Yes, before the server is built | `registry.Refresh` |
| Usage snapshot refresh | Server | 15s, and before each task | Yes, required | `ledger.Refresh` |
| Disabled provider read | Server | 5s, and after each disable or enable | Yes, before serving | `Server.refreshDisabled` |
| Reservation reaper | Server | 5m | Yes, before serving | `Dispatcher.Reap` |
| Dispatch claim | Server | 30s | Yes, before serving | `Server.claim` |
| Output retention | Server | 1h | No | `Server.prune` |
| Periodic launches | Server | At the next launch, 1m at most, and after each register or stop | Yes, beside serving | `Server.launchPass` |
| Dispatch run | Server | Per dispatch | | `Dispatcher.Run`, `Dispatcher.Resume` |
| Execution runner | Server | Per execution, `Status` every 2s to 15s | | `runner.run` |
| Lease renewal | Server | 20s per dispatch, until it is done | | `Dispatcher.hold` |
| Agent listener | Server | Long-running | Yes | `Server.ServeAgents`, `Conns.Serve` |
| Output capture | Server | Per running execution, storing every 1s | For a claimed dispatch's running executions | `capture.run` |
| Output follow | Server | Per request, record and output read every 1s | | `Server.sendLogs` |
| Server connection | Agent | Backoff 1s to 30s | Yes | `Agent.Run` |
| Workload report | Agent | 10s, and on every change | On connecting | `Agent.connect` |
| Timeout watcher | Agent | Per container | For every held container | `Containerd.watch` |
| MicroVM supervision and timeout | vagabond-firecracker | Per microVM | Taken back at start | `Executor::supervise`, `Executor::watch` |
