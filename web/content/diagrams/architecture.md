---
title: "System Architecture"
linkTitle: "Architecture"
description: "Interactive diagram of the CLI, the server's API and internals, the store, and the providers and agent nodes it dispatches to, with implementation notes."
weight: 1
---

The components of a Vagabond deployment: the CLI and the server's `/v1` API, the server's internals, the store, and the providers it dispatches to, agent nodes included. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    CLI([vagabond CLI]):::entry',
    '    STORE[("Store<br>Postgres / CockroachDB")]:::storage',
    '',
    '    subgraph SRV [vagabond server]',
    '        API[HTTP API<br>/v1]:::process',
    '        JOBS[Job loading<br>jobspec, jobs]:::process',
    '        SCHED{Admission<br>and ranking}:::decision',
    '        DISP[Dispatcher]:::process',
    '        LEDGER[Quota<br>ledger]:::process',
    '        REG[Provider<br>registry]:::process',
    '        UPKEEP[Upkeep<br>loops]:::background',
    '        FOLLOW[Live output<br>streams]:::process',
    '        PLUGIN[plugin.Provider]:::filter',
    '        POOL[containerd<br>provider]:::process',
    '        NODES[(Connected<br>nodes)]:::storage',
    '        AGENTL[Agent listener<br>mTLS, yamux]:::filter',
    '    end',
    '',
    '    subgraph CLOUD [Cloud platforms]',
    '        CLOUDRUN[cloud-run<br>Cloud Run Jobs]:::data',
    '        CLOG[Cloud Logging]:::data',
    '        CODEENGINE[code-engine<br>Code Engine Jobs]:::data',
    '        CEKUBE[Code Engine<br>Kubernetes API]:::data',
    '        LAMBDA[lambda<br>AWS Lambda]:::data',
    '    end',
    '',
    '    subgraph AGENTNODE [Agent node]',
    '        AGENT([vagabond agent]):::entry',
    '        CONTAINERD[(containerd)]:::data',
    '    end',
    '',
    '    CLI -->|HTTP/JSON| API',
    '    API -->|register, run, plan| JOBS',
    '    JOBS -->|plan| SCHED',
    '    JOBS -->|run, dispatch| DISP',
    '    DISP -->|each task| SCHED',
    '    REG -->|capabilities| SCHED',
    '    LEDGER -->|usage snapshot| SCHED',
    '    DISP -->|reserve, settle| LEDGER',
    '    DISP -->|submit, poll, release| PLUGIN',
    '    API -->|logs follow| FOLLOW',
    '    FOLLOW -->|StreamLogs| PLUGIN',
    '    REG -->|Capabilities| PLUGIN',
    '    UPKEEP -->|refresh| REG',
    '    UPKEEP -->|refresh| LEDGER',
    '    UPKEEP -->|reap, claim| DISP',
    '    UPKEEP -->|prune output| STORE',
    '',
    '    PLUGIN --> CLOUDRUN',
    '    PLUGIN --> CODEENGINE',
    '    PLUGIN --> LAMBDA',
    '    PLUGIN --> POOL',
    '    CLOUDRUN -->|output| CLOG',
    '    CODEENGINE -->|exit code, output| CEKUBE',
    '    POOL -->|room, reserve| NODES',
    '    AGENTL -->|register, remove| NODES',
    '    AGENTL <-->|session the agent dials| AGENT',
    '    POOL -->|AgentExecutions gRPC| AGENT',
    '    AGENT -->|workloads| CONTAINERD',
    '',
    '    JOBS -->|jobs, versions| STORE',
    '    DISP -->|dispatches, executions| STORE',
    '    LEDGER -->|usage, reservations| STORE',
  ].join('\n'),

  info: {
    CLI: {
      title: 'vagabond CLI',
      badge: 'entry', badgeText: 'client',
      body: '<p>Every command is in <code>internal/cli</code>: <code>job validate</code>, <code>plan</code>, <code>run</code>, <code>register</code>, <code>dispatch</code>, <code>status</code> and <code>stop</code>; <code>execution status</code> and <code>logs</code>; <code>node status</code>; <code>provider status</code>; and the <code>server</code> and <code>agent</code> processes.</p><p>Parses and validates a job file locally with <code>jobspec</code> before sending it, and the server runs both again. <code>job validate</code> never contacts the server. Holds no state.</p><p>Calls the server through the <code>api</code> client at <code>-address</code>, then <code>VAGABOND_ADDR</code>, then <code>http://127.0.0.1:4747</code>. Over <code>https://</code> it verifies the server against <code>-tls-ca</code> or the system&rsquo;s roots and presents <code>-tls-cert</code> and <code>-tls-key</code> when given, each defaulting to its <code>VAGABOND_TLS_*</code> variable. <code>job run</code> follows the run to its end, streaming output through the logs route.</p><p><a href="../../docs/cli/">CLI reference &rarr;</a></p>'
    },
    API: {
      title: 'HTTP API (/v1)',
      badge: 'process', badgeText: 'server',
      body: '<p><code>net/http</code> mux in <code>internal/server</code>, on the <code>server</code> block&rsquo;s <code>bind</code> (default <code>127.0.0.1:4747</code>), with TLS when its <code>tls</code> block names a certificate and key. When the block also names a <code>ca</code>, every route but <code>/v1/health</code> answers 401 without a client certificate that chains to it. Routes that read or write jobs take <code>?namespace=</code>; routes addressed by an ID, <code>/v1/nodes</code>, <code>/v1/providers</code> and <code>/v1/health</code> ignore it.</p><p><b>Jobs:</b> <code>POST /v1/jobs</code>, <code>GET /v1/jobs</code>, <code>POST /v1/jobs/run</code>, <code>POST /v1/jobs/plan</code>, <code>GET</code> and <code>DELETE /v1/job/{name}</code>, <code>POST /v1/job/{name}/dispatch</code>.<br><b>Runs:</b> <code>GET /v1/dispatch/{id}</code>, <code>GET</code> and <code>DELETE /v1/execution/{id}</code>, <code>GET /v1/execution/{id}/logs</code>.<br><b>Fleet:</b> <code>GET /v1/nodes</code>, <code>GET /v1/providers</code>, <code>GET /v1/health</code>.</p><p>JSON request bodies are capped at 1 MiB and headers must arrive within 10s. <code>GET /v1/health</code> pings the store with a 5s timeout and answers 503 when it does not answer. On SIGINT or SIGTERM in-flight requests get 10s; running dispatches are left to their leases.</p><p><a href="../../docs/api/">API reference &rarr;</a></p>'
    },
    JOBS: {
      title: 'Job loading (jobspec, jobs)',
      badge: 'process', badgeText: 'parse and load',
      body: '<p><code>jobspec</code> decodes the HCL, substitutes <code>${meta.*}</code> and validates, reporting diagnostics against source lines. An invalid job is a 400 carrying them.</p><p><code>jobs</code> loads a job for <code>run</code>, <code>register</code> and <code>dispatch</code>, and checks dispatch metadata against <code>parameterized</code>. Registered jobs are stored as numbered versions; a file that differs only in formatting adds none. A stopped job cannot be dispatched until it is registered again.</p><p>A job file sent to <code>POST /v1/jobs/run</code> is not registered, and its dispatch records <code>JobVersion</code> 0.</p><p><a href="../../docs/job-specification/">Job specification &rarr;</a></p>'
    },
    SCHED: {
      title: 'Admission and ranking',
      badge: 'decision', badgeText: 'scheduler',
      body: '<p><code>scheduler.Admit</code> runs 13 checks in 4 tiers (policy, availability, mismatch, capacity) against every provider, each node of a <code>containerd</code> provider on its own. Every check runs; the first rejection carries the reason code and the rest ride along.</p><p><code>scheduler.Rank</code> scores each candidate with <code>headroom</code>, any affinities and, under weighted tiers, <code>tier</code>; the score is their mean. Under strict tiers candidates order by tier, then score.</p><p>A pure function over the registry&rsquo;s capability snapshots and the ledger&rsquo;s usage snapshot: no network call, so a plan needs no provider credentials. <code>job plan</code> stops here and reserves nothing; a dispatch admits and ranks each task again when it runs.</p><p><a href="../admission/">Admission diagram &rarr;</a></p>'
    },
    DISP: {
      title: 'Dispatcher',
      badge: 'process', badgeText: 'dispatch',
      body: '<p><code>dispatch.Dispatcher</code>. <code>Begin</code> records the dispatch <code>running</code>, leased to this server, and the request returns its ID; the run continues in its own goroutine on a context detached from the request.</p><p>Per task, in declaration order: re-read usage, admit and rank, then for each candidate reserve quota, and room on the node for a node of a <code>containerd</code> provider, record the attempt <code>pending</code> with its node and start an execution runner, which submits, polls <code>Status</code> from 2s backing off to 15s, collects the result and output, settles and releases. The task takes the runner&rsquo;s first answer, and the runner settles and releases after. A refused reservation moves to the next candidate without spending an attempt; an <code>infrastructure</code> failure moves to the next ranked candidate within the task&rsquo;s <code>retry</code> budget. An exit code is an answer and is never rerouted.</p><p>The lease runs 60s and is renewed every 20s until every execution is released, and every write to an execution is checked against it. A dispatch whose lease lapses is claimed by a server, which restores a runner for each execution not yet done.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    LEDGER: {
      title: 'Quota ledger',
      badge: 'process', badgeText: 'ledger',
      body: '<p><code>ledger.Ledger</code> charges each attempt&rsquo;s declared worst case against every pool it meters, in the provider total and the namespace share, before it is submitted. The store decides each reservation in one statement, so two dispatches cannot both take the last room. A refusal skips the provider without spending a retry.</p><p>Each execution&rsquo;s runner replaces the reservation with what the run cost: nothing when nothing ran, what the platform billed when it says, otherwise the declared shape over the time it ran, or over its timeout when it never answered. The reaper drops a reservation over 1h old whose execution was never recorded. Only providers with quota pools reserve anything.</p><p>Holds the usage snapshot admission reads, refreshed every 15s and before each task a dispatch runs.</p><p><a href="../quotas/">Quotas diagram &rarr;</a></p>'
    },
    REG: {
      title: 'Provider registry',
      badge: 'process', badgeText: 'registry',
      body: '<p><code>registry</code> builds every <code>provider</code> block into its plugin, the one package that imports them all: <code>cloud-run</code>, <code>code-engine</code>, <code>lambda</code>, <code>containerd</code>, and the <code>fake-container</code>, <code>fake-function</code> and <code>fake-worker</code> test providers. It holds each one&rsquo;s capability snapshot, health, tags, tier and quota pools. A disabled provider is kept, so a plan can say why it was not used.</p><p><code>Refresh</code> asks every enabled provider for <code>Capabilities</code> in parallel, 30s each, at startup and every minute. One that fails is marked unhealthy and keeps its last snapshot. <code>containerd</code> providers are skipped: their capabilities are built from connected nodes on every read.</p><p>Resolves a provider&rsquo;s credential again when the platform rejects it, at most once a minute.</p>'
    },
    UPKEEP: {
      title: 'Upkeep loops',
      badge: 'background', badgeText: 'timers',
      body: '<p>Five tickers in <code>internal/server/upkeep.go</code>, started when the server begins serving and stopped at shutdown: capability refresh (1m), usage snapshot refresh (15s), reservation reaper (5m), dispatch claim (30s) and output retention (1h).</p><p>Capabilities and usage are read while the server is built, and the claim and the reaper run once before HTTP is served, so their first tick is one interval in. Output retention has no startup pass. A failure is logged and waits for the next tick.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    FOLLOW: {
      title: 'Live output streams',
      badge: 'process', badgeText: 'per request',
      body: '<p><code>GET /v1/execution/{id}/logs?follow=true</code> on an execution that has not ended, whose provider implements <code>plugin.LogStreamer</code> (<code>cloud-run</code>, <code>code-engine</code>, <code>containerd</code>, on the node the record names), copies the provider&rsquo;s stream to the client from <code>offset</code>, flushing every write.</p><p>The record is re-read every second, and the response ends 5s after the execution does, when the client goes, or at once when shutdown begins. Every stream starts at the first byte the task printed; one that cannot be placed at <code>offset</code> is answered 416 before any output. Any other request gets the stored output from <code>execution_logs</code>, up to <code>max_log_mib</code>, the end of it.</p><p><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    PLUGIN: {
      title: 'plugin.Provider',
      badge: 'filter', badgeText: 'boundary',
      body: '<p>The interface every provider implements: <code>Name</code>, <code>Capabilities</code>, <code>Submit</code>, <code>Status</code>, <code>Result</code>, <code>Cancel</code>. Optional interfaces are found by type assertion: <code>LogStreamer</code>, <code>LogReader</code>, <code>Releaser</code>, <code>Live</code>, <code>Nodes</code>, <code>Reserver</code>, <code>Recredentialer</code>.</p><p>A plugin translates a task into its platform&rsquo;s API and classifies failures as <code>infrastructure</code> (rerouted) or <code>internal</code> (not). It never retries or picks another provider; retries, rerouting, quota, records and leases belong to the control plane.</p><p>depguard keeps cloud SDKs inside <code>internal/providers/</code>, and provider imports out of <code>scheduler</code>, <code>job</code> and <code>quota</code>.</p><p><a href="../../docs/writing-a-provider/">Writing a provider &rarr;</a></p>'
    },
    POOL: {
      title: 'containerd provider',
      badge: 'process', badgeText: 'provider',
      body: '<p><code>providers/pool</code>, provider type <code>containerd</code>: the agent nodes started with the <code>-pool</code> its <code>config</code> block names, default the provider&rsquo;s label. <code>LiveCapabilities</code> builds one member per connected node, sized by the room it has left, on every plan and dispatch. With no nodes the provider is unhealthy.</p><p>Admission judges each node on its own, and every passing node is a candidate of its own, ordered among equal scores by <code>placement</code>: <code>spread</code> (default) most free memory first, <code>pack</code> least. Dispatch reserves room on the node before recording the execution with its node; a node without room is refused without spending an attempt.</p><p>Every later call goes to the node the record names. While that node is not connected, calls fail as infrastructure and the execution is <code>lost</code>; <code>Release</code> succeeds, and the node releases the workload when it returns.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    NODES: {
      title: 'Connected nodes',
      badge: 'storage', badgeText: 'in memory',
      body: '<p><code>nodes.Conns</code>, shared by the server and every <code>containerd</code> provider: each connected node by name, with its last report, its session and a client for its executions, and the room reserved on each. A node&rsquo;s room is counted here once, whatever providers draw on it. Read by <code>GET /v1/nodes</code>.</p><p>A report on a new session replaces and closes the old one. When a session ends, the node is removed.</p><p>Server memory only. After a restart, agents reconnect with backoff and report again.</p>'
    },
    AGENTL: {
      title: 'Agent listener',
      badge: 'filter', badgeText: 'agent connections',
      body: '<p><code>Server.ServeAgents</code> runs <code>nodes.Conns.Serve</code>, which accepts on <code>agent_bind</code>, default <code>127.0.0.1:4748</code>. A non-loopback address requires <code>agent_tls</code>: mutual TLS 1.3, agent certificates signed by the configured CA, the handshake within 10s. Over TLS a node registers only under its certificate&rsquo;s common name.</p><p>Each connection is a yamux session carrying gRPC both ways: the agent calls <code>Node.Register</code>, answered with the held workloads the server has no use for, and the server calls <code>AgentExecutions</code> down the same session. The session closing is the liveness signal; yamux keepalives run every 30s.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    STORE: {
      title: 'Store (Postgres or CockroachDB)',
      badge: 'storage', badgeText: 'store',
      body: '<p><code>state/postgres</code>: one implementation for Postgres and CockroachDB over <code>pgxpool</code>. Opened, migrated and schema-checked at startup. Connections time out after 5s unless the DSN sets one, and writes run in serializable transactions.</p><p>Tables: <code>jobs</code>, <code>job_versions</code>, <code>dispatches</code> (with leases), <code>executions</code> (results), <code>execution_logs</code> (each execution&rsquo;s output in 256 KiB chunks, pruned after <code>log_retention</code>), <code>quota_usage</code>, <code>quota_reservations</code>.</p><p>Under <code>-dev</code>, <code>state/memory</code> and an in-memory ledger store replace it, empty at every start. An unreachable store is answered 503.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    CLOUDRUN: {
      title: 'cloud-run: Cloud Run Jobs',
      badge: 'data', badgeText: 'provider',
      body: '<p><code>providers/gcp</code>, for <code>container</code> tasks. One Job per execution, named <code>vagabond-&lt;execution id&gt;</code>: <code>Submit</code> creates it and calls <code>:run</code>, <code>Status</code> reads the Job&rsquo;s executions, and <code>Cancel</code> and <code>Release</code> delete the Job.</p><p>Stateless: every call derives the Job name from the execution ID, so a server that takes over a dispatch can poll a run it did not submit. Calls the Cloud Run and Cloud Logging REST APIs over HTTP.</p><p><a href="../../docs/providers/cloud-run/">Cloud Run provider &rarr;</a></p>'
    },
    CLOG: {
      title: 'Cloud Logging',
      badge: 'data', badgeText: 'output',
      body: '<p>Where a Cloud Run execution&rsquo;s output comes from. <code>ReadLogs</code> pages <code>entries:list</code> filtered on the Job name, oldest first, up to 20 pages, and reports one stopped at the cap as incomplete, which dispatch stores marked truncated.</p><p><code>StreamLogs</code> opens <code>entries:tail</code> with a 1s buffer window, so live output arrives in order, then lists what was logged before it and drops the tail&rsquo;s copies of it, so a stream starts at the first byte and resumes from an offset.</p><p><a href="../../docs/providers/cloud-run/#logs">Cloud Run logs &rarr;</a></p>'
    },
    CODEENGINE: {
      title: 'code-engine: Code Engine Jobs',
      badge: 'data', badgeText: 'provider',
      body: '<p><code>providers/ibm</code>, for <code>container</code> tasks. One job run per execution, named <code>vagabond-&lt;execution id&gt;</code>, with the image and settings inline: <code>Submit</code> creates it, <code>Status</code> reads it, and <code>Cancel</code> and <code>Release</code> delete it. A task runs at the smallest vCPU and memory size that holds what it declared.</p><p>Stateless: every call derives the run name from the execution ID. Calls the Code Engine, Kubernetes and IAM REST APIs over HTTP, all with one IAM token, refreshed from the API key.</p><p><a href="../../docs/providers/code-engine/">Code Engine provider &rarr;</a></p>'
    },
    CEKUBE: {
      title: 'Code Engine Kubernetes API',
      badge: 'data', badgeText: 'exit code, output',
      body: '<p>Where a job run&rsquo;s exit code and output come from, since the v2 API reports neither. The run&rsquo;s pod is found by its <code>codeengine.cloud.ibm.com/job-run</code> label in the project&rsquo;s namespace, read once from the kubeconfig.</p><p><code>Result</code> reads the container&rsquo;s exit code and <code>ReadLogs</code> its whole log while the pod exists; deleting the run removes both. <code>StreamLogs</code> waits for the container to start, then follows its log from the first line, so it can resume from an offset.</p><p><a href="../../docs/providers/code-engine/#logs">Code Engine logs &rarr;</a></p>'
    },
    LAMBDA: {
      title: 'lambda: AWS Lambda',
      badge: 'data', badgeText: 'provider',
      body: '<p><code>providers/aws</code>, for <code>function</code> tasks. Invokes a function the operator deployed; Vagabond creates none.</p><p>The invocation is synchronous: the answer, the last 4 KB of the log and the billed duration come back from <code>Submit</code>, so nothing is polled. The charge settles at the billed duration when the log tail reports it, otherwise at the declared shape over the measured invocation.</p><p><code>Status</code>, <code>Result</code> and <code>Cancel</code> are unsupported, so a <code>Submit</code> that fails other than as <code>internal</code> cannot be asked about and is charged as reserved.</p><p><a href="../../docs/providers/lambda/">Lambda provider &rarr;</a></p>'
    },
    AGENT: {
      title: 'vagabond agent',
      badge: 'entry', badgeText: 'node process',
      body: '<p><code>internal/agent</code>, on a node apart from the server. Reads the node&rsquo;s architecture, CPU and memory (<code>agent/fingerprint</code>), delegates its cgroup, then dials the server and stays connected, reconnecting with backoff from 1s doubling to 30s.</p><p>Registers the node with every workload it holds on connecting, after each submit, cancel or release, and every 10s. Checks room itself before starting a workload and refuses one that does not fit with <code>ResourceExhausted</code>. Releases the held workloads the server answers it has no use for.</p><p><a href="../agents/">Agents diagram &rarr;</a></p>'
    },
    CONTAINERD: {
      title: 'containerd',
      badge: 'data', badgeText: 'runtime',
      body: '<p><code>agent/executor</code> runs each execution as one container in the <code>vagabond</code> namespace with the <code>io.containerd.runc.v2</code> runtime, in cgroup <code>&lt;parent&gt;/&lt;execution id&gt;</code> with its declared CPU and memory as limits, on the host network.</p><p>Start time, timeout and size are container labels, so a restarted agent finds its workloads and re-arms their timeouts. A timeout stops the workload with SIGTERM, then SIGKILL 10s later. Output goes to a log file, which the server reads whole once the workload ends.</p><p>Workloads belong to containerd and keep running while the agent is disconnected.</p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Processes that dial the server: the CLI and the agent |
| <span class="dg-swatch dg-process"></span> | Server components |
| <span class="dg-swatch dg-decision"></span> | Admission and ranking |
| <span class="dg-swatch dg-filter"></span> | Boundaries: the plugin interface and the agent listener |
| <span class="dg-swatch dg-storage"></span> | State: the store and the connected nodes |
| <span class="dg-swatch dg-success"></span> | External platforms and runtimes |
| <span class="dg-swatch dg-background"></span> | Upkeep timers |
