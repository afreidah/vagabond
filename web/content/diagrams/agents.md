---
title: "Agents and Pools"
linkTitle: "Agents and Pools"
description: "Interactive diagram of an agent node from startup to running workloads, and the containerd provider that admits, places and tracks work on it."
weight: 5
---

How an agent node joins a pool and runs workloads: startup and capacity, the connection to the server, live pool membership and per-node admission, ranking and the room reserved on a node, the `AgentExecutions` calls down to containerd, what happens when a node drops, and the workloads the server tells a node to release. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    subgraph STARTUP ["Node: agent startup"]',
    '        AGENT([vagabond agent]):::entry --> FP[Fingerprint:<br>arch, CPU, memory]:::process',
    '        FP --> CGROUP{"-cgroup-parent<br>set?"}:::decision',
    '        CGROUP -->|no| DELEGATE[Delegate own cgroup:<br>leaf + controllers]:::process',
    '        CGROUP -->|yes| CTRD',
    '        DELEGATE --> CTRD[containerd check,<br>log dir, Recover]:::process',
    '        CTRD --> DIAL[Dial the server:<br>mTLS + yamux]:::process',
    '        DIAL --> REPORT[Node.Register:<br>workload report]:::background',
    '    end',
    '',
    '    subgraph WORKLOADS ["Node: workloads"]',
    '        EXECEP[AgentExecutions<br>endpoint]:::process',
    '        EXECEP -->|Submit| FITS{Fits the<br>node room?}:::filter',
    '        FITS -->|no| REXH[ResourceExhausted]:::reject',
    '        FITS -->|yes| EXECUTOR',
    '        EXECEP -->|"Status, Result,<br>Cancel, Release"| EXECUTOR[containerd<br>executor]:::process',
    '        EXECEP -->|StreamLogs| STREAMLOGS["StreamLogs:<br>seek offset, poll 250 ms"]:::process',
    '        EXECEP -->|changed| REPORT',
    '        EXECUTOR --> TIMEOUT[Timeout<br>watcher]:::background',
    '        EXECUTOR --> WORKLOAD[Workload container:<br>vagabond namespace]:::success',
    '        TIMEOUT -->|SIGTERM, SIGKILL| WORKLOAD',
    '        WORKLOAD -->|stdout, stderr| LOGFILE[("Log file:<br>data-dir/logs")]:::storage',
    '        STREAMLOGS --> LOGFILE',
    '    end',
    '',
    '    subgraph SERVERSIDE [Server]',
    '        LISTEN[Agent listener:<br>agent_bind]:::process --> NODEEP{"Node.Register:<br>name = cert CN?"}:::filter',
    '        NODEEP -->|no| PERMDENIED[PermissionDenied]:::reject',
    '        NODEEP -->|yes| CONNS[(nodes.Conns:<br>nodes and their room)]:::storage',
    '        NODEEP -->|yes, answer| UNWANTED["release[]:<br>workloads nothing needs"]:::process',
    '        CONNS --> NODESTATUS["node status<br>GET /v1/nodes"]:::process',
    '        CONNS --> POOLCAPS[containerd provider:<br>one member per node]:::process',
    '        DISPATCH([Dispatch]):::entry --> ADMIT{Admission<br>per node}:::filter',
    '        POOLCAPS --> ADMIT',
    '        ADMIT -->|each passing node| PLACE[Ranked candidates:<br>labels, then placement]:::process',
    '        PLACE -->|in rank order| RESERVE{"Room on<br>the node?"}:::filter',
    '        CONNS -->|Reserve| RESERVE',
    '        RESERVE -->|"ErrNoRoom:<br>next candidate"| PLACE',
    '        RESERVE -->|"held: recorded<br>with its node"| RECORD[("Execution record:<br>node")]:::storage',
    '        UNWANTED -->|Get each| RECORD',
    '        DISPATCH -->|"Status, Result,<br>Cancel, Release"| POOLEXEC[Node executions:<br>the node on the record]:::process',
    '        RECORD -->|node| POOLEXEC',
    '        POOLEXEC --> GRACE{Node<br>connected?}:::decision',
    '        GRACE -->|no| LOST[lost until the node<br>returns, or 24 h]:::reject',
    '    end',
    '',
    '    DIAL -->|one TCP connection| LISTEN',
    '    REPORT -->|Node.Register| NODEEP',
    '    UNWANTED -->|"answer: release[]"| REPORT',
    '    REPORT -->|release each| EXECEP',
    '    RESERVE -->|AgentExecutions.Submit| EXECEP',
    '    REXH -->|"infrastructure:<br>reroute if allowed"| PLACE',
    '    GRACE -->|connected| EXECEP',
  ].join('\n'),

  info: {
    AGENT: {
      title: 'vagabond agent',
      badge: 'entry', badgeText: 'entry point',
      body: '<p>Runs on a node the operator owns, apart from the server (<code>internal/cli/agent.go</code>). Flags: <code>-server</code> (default <code>127.0.0.1:4748</code>), <code>-pool</code> (default <code>default</code>), <code>-name</code>, repeatable <code>-label key=value</code>, <code>-cpu</code>, <code>-memory</code>, <code>-containerd</code>, <code>-data-dir</code>, <code>-cgroup-parent</code>, <code>-log-level</code>.</p><p>With <code>-tls-ca</code>, <code>-tls-cert</code> and <code>-tls-key</code> (all three or none) the node name is the certificate&rsquo;s common name, and a <code>-name</code> that differs is refused. Without TLS the name defaults to the hostname.</p><p>Every step up to dialling exits with status 1 on failure, before the node registers. A server that cannot be reached is not a startup failure.</p><p><a href="../../docs/agent/#flags">Agent flags &rarr;</a></p>'
    },
    FP: {
      title: 'Fingerprint the Node',
      badge: 'process', badgeText: 'capacity',
      body: '<p><code>fingerprint.Take</code> reads the host: architecture is the binary&rsquo;s <code>GOARCH</code>, CPU is the CPUs available &times; 1000 millicores, memory is <code>MemTotal</code> from <code>/proc/meminfo</code> in MiB.</p><p>The agent&rsquo;s cgroup is the <code>0::</code> line of <code>/proc/self/cgroup</code>; no such line is a cgroup v1 host and a startup error. <code>cpu.max</code> (quota &times; 1000 / period) and <code>memory.max</code> are read from that cgroup and every ancestor to the root, tightest wins; <code>max</code> or a missing file is no limit.</p><p>Each dimension is the smallest of the host, the cgroup chain and the <code>-cpu</code> / <code>-memory</code> caps (0 is no cap). Read once at startup; a changed limit takes a restart.</p><p><a href="../../docs/agent/#capacity">Capacity &rarr;</a></p>'
    },
    CGROUP: {
      title: '-cgroup-parent Set?',
      badge: 'decision', badgeText: 'cgroup parent',
      body: '<p>Workloads are created under one parent cgroup, so the kernel holds their sum to its limits as well as each to its own.</p><p><b>Set</b>: workloads go under the operator&rsquo;s parent. The agent neither checks nor prepares it and skips delegation; runc enables controllers along the path as it creates each workload. A parent that holds processes fails the first <code>Submit</code> with <code>creating the task</code>.</p><p><b>Unset</b>: the agent delegates its own cgroup. Capacity is read from the agent&rsquo;s own cgroup chain either way.</p>'
    },
    DELEGATE: {
      title: 'Delegate Own Cgroup',
      badge: 'process', badgeText: 'cgroup v2',
      body: '<p><code>agent.Delegate</code>. cgroup v2 lets a group hold processes or hand controllers to children, not both, so the agent:</p><p>1. uses the parent when its cgroup already ends in <code>/agent</code> (restarted inside its own leaf)<br>2. refuses the root <code>/</code> (a container without <code>--cgroupns=host</code>)<br>3. creates <code>&lt;cgroup&gt;/agent</code> and moves its own PID into it<br>4. enables <code>cpu</code> and <code>memory</code> in each ancestor&rsquo;s <code>cgroup.subtree_control</code> down to <code>&lt;cgroup&gt;</code>&rsquo;s parent; runc enables them in <code>&lt;cgroup&gt;</code> itself when it creates a workload</p><p>Any refusal is <code>cannot manage cgroups under &lt;path&gt;</code>, naming the fixes: <code>Delegate=yes</code> under systemd, or a writable <code>/sys/fs/cgroup</code> with <code>--cgroupns=host</code>.</p><p><a href="../../docs/agent/#cgroup-delegation">Cgroup delegation &rarr;</a></p>'
    },
    CTRD: {
      title: 'containerd Check, Log Directory, Recover',
      badge: 'process', badgeText: 'executor',
      body: '<p><code>executor.New</code> connects to containerd on <code>-containerd</code> (default <code>/run/containerd/containerd.sock</code>) in the <code>vagabond</code> namespace and creates <code>&lt;data-dir&gt;/logs</code>, mode <code>0750</code> (default data dir <code>/var/lib/vagabond</code>). <code>Check</code> asks containerd for its version, so a socket nobody answers on fails here.</p><p><code>Recover</code> lists every container in the namespace and re-arms each timeout from its <code>vagabond.timeout</code> and <code>vagabond.started</code> labels; one already past its deadline is stopped at once. Workloads belong to containerd, so they outlive an agent restart.</p><p>The agent then logs <code>node</code> with its name, pool, cpu, memory and cgroup parent, and dials.</p>'
    },
    DIAL: {
      title: 'Dial the Server',
      badge: 'process', badgeText: 'connection',
      body: '<p><code>agentrpc.Dial</code> opens one TCP connection to <code>-server</code>, completes the TLS handshake up front (a certificate problem is a failed dial), and runs yamux over it. Each side serves gRPC on the streams the other opens, so the server calls the agent without the agent listening on anything. The agent verifies the server against <code>-tls-ca</code> for <code>-tls-server-name</code>, else the host of <code>-server</code>.</p><p><code>Agent.Run</code> dials again whenever the session drops or cannot be made: 1 s, doubling to 30 s, reset to 1 s after a session that registered and later dropped. Logs <code>connecting to the server</code> with <code>retry</code>, or <code>lost the server</code>.</p><p>The connection closing is the only liveness signal; yamux keepalives (30 s) detect a dead peer.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    REPORT: {
      title: 'Node.Register: Workload Report',
      badge: 'background', badgeText: 'agent loop',
      body: '<p>Sends <code>name</code>, <code>pool</code>, <code>labels</code>, <code>architecture</code>, <code>capacity</code>, <code>runtimes</code>, <code>version</code> and <code>held[]</code>: every container in the namespace, with its declared CPU and memory from its labels and whether its task still runs.</p><p>Sent on connecting, after a <code>Submit</code> starts a workload, after every <code>Cancel</code> or <code>Release</code>, and every 10 s, which catches a workload finishing on its own. One pending signal covers any number of changes. A failed first registration ends the session; a failed later one logs <code>reporting workloads</code> and the next report carries everything.</p><p>Each report is answered with <code>release[]</code>, the held workloads the server has no use for. The agent releases each through its own <code>Release</code> and logs <code>released a workload the server has no use for</code>; a failed one warns <code>releasing a workload the server has no use for</code> and is answered again on the next report.</p><p>The first report after an agent or server restart lists every workload still held, so the node&rsquo;s room counts them at once.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    EXECEP: {
      title: 'AgentExecutions Endpoint',
      badge: 'process', badgeText: 'agent',
      body: '<p>Served by the agent on the session it opened (<code>internal/agent/executions_endpoint.go</code>): <code>Submit</code>, <code>Status</code>, <code>Result</code>, <code>Cancel</code>, <code>Release</code> and <code>StreamLogs</code>, each translated to the executor.</p><p><code>Submit</code> carries the workload the server already decoded: <code>image</code>, <code>command</code>, <code>args</code>, <code>env</code>, <code>working_dir</code>, <code>resources</code>, <code>timeout</code>, and an empty <code>runtime</code> that defaults to <code>io.containerd.runc.v2</code>. It answers with the state reached and the provider ID.</p><p>A container the node does not hold is <code>NotFound</code>, which the server reads as an unknown execution. Any other executor error is <code>Internal</code>, an infrastructure failure on the server.</p><p><a href="../../docs/agent/#rpcs">Agent RPCs &rarr;</a></p>'
    },
    FITS: {
      title: 'Fits the Node Room?',
      badge: 'filter', badgeText: 'room check',
      body: '<p>The agent checks room itself, since its count can be tighter than the server&rsquo;s. Under a lock it computes what is free with <code>agentrpc.Free</code>, the function the server counts room with: capacity, less the declared CPU and memory of its running workloads, less in-flight submissions it does not report running. The new workload must fit what is left.</p><p>The in-flight reservation is recorded before the lock is released, so the image pull runs outside it, and dropped when <code>Submit</code> returns. A concurrent submission for the same execution waits for the one in flight.</p><p>An execution already held under the same ID is a resubmission: it always fits and starts nothing new. A finished workload keeps its container until released but no longer counts.</p>'
    },
    REXH: {
      title: 'ResourceExhausted',
      badge: 'reject', badgeText: 'node full',
      body: '<p><code>the node has &lt;cpu&gt; millicores and &lt;mem&gt; MiB free; the workload needs &lt;cpu&gt; and &lt;mem&gt;</code></p><p>The server drops its reservation on the node, and the attempt fails as infrastructure: <code>node &lt;name&gt;: rpc error: code = ResourceExhausted desc = ...</code>. Dispatch tries another candidate only when the task&rsquo;s <code>retry</code> block allows rerouting. Any other <code>Submit</code> error is handled the same way, as <code>node &lt;name&gt;: ...</code>.</p><p>The server&rsquo;s own room count normally refuses first, before anything is recorded, without spending an attempt.</p><p><a href="../../docs/agent/#refusal-and-retry">Refusal and retry &rarr;</a></p>'
    },
    EXECUTOR: {
      title: 'containerd Executor',
      badge: 'process', badgeText: 'executor',
      body: '<p><code>Containerd.Submit</code> leaves an existing container under the execution ID alone, since the ID is the idempotency key. Otherwise it normalises and pulls the image (<code>alpine</code> &rarr; <code>docker.io/library/alpine:latest</code>, no registry credentials), creates the container and snapshot under the execution ID, and starts the task.</p><p>The OCI spec is the image config, then <code>command</code> / <code>args</code>, <code>env</code> and working directory. <code>memory</code> MiB is the memory limit, <code>cpu</code> a CFS quota of <code>cpu &times; 100</code> &micro;s per 100000 &micro;s, in cgroup <code>&lt;parent&gt;/&lt;execution-id&gt;</code>, on the host network with the host&rsquo;s <code>resolv.conf</code> and <code>hosts</code>.</p><p><code>Cancel</code> labels <code>vagabond.cancelled=true</code> and sends <code>SIGTERM</code>, <code>SIGKILL</code> 10 s later. <code>Release</code> deletes the task, container, snapshot and log file; releasing one the node does not hold succeeds.</p>'
    },
    TIMEOUT: {
      title: 'Timeout Watcher',
      badge: 'background', badgeText: 'agent goroutine',
      body: '<p>One goroutine per workload that declares a <code>timeout</code>, started by <code>Submit</code> and, after a restart, by <code>Recover</code>. At <code>vagabond.started</code> plus the timeout it sends <code>SIGTERM</code>, then <code>SIGKILL</code> 10 s later if the process has not exited.</p><p>A timed-out workload is not labelled cancelled: it ends <code>failed</code> with its exit status, or <code>succeeded</code> if it exits 0 on <code>SIGTERM</code>. No timeout means no watcher.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    WORKLOAD: {
      title: 'Workload Container',
      badge: 'success', badgeText: 'containerd',
      body: '<p>One container per execution in the <code>vagabond</code> namespace. Its state lives on it as labels, <code>vagabond.started</code>, <code>vagabond.timeout</code>, <code>vagabond.cpu</code>, <code>vagabond.memory</code> and <code>vagabond.cancelled</code>, so it survives the agent.</p><p><code>Status</code>: no task or a created task is <code>accepted</code>; running, paused or other is <code>running</code>; stopped is <code>cancelled</code> when labelled, else <code>succeeded</code> on exit 0 and <code>failed</code> otherwise. <code>Result</code> carries the exit code and the run time from <code>vagabond.started</code>; the output is read through <code>StreamLogs</code>.</p><p>Under systemd, <code>KillMode=process</code> keeps workloads alive when the agent&rsquo;s unit restarts.</p>'
    },
    LOGFILE: {
      title: 'Log File',
      badge: 'storage', badgeText: 'node disk',
      body: '<p>containerd writes the task&rsquo;s stdout and stderr itself to <code>&lt;data-dir&gt;/logs/&lt;execution-id&gt;.log</code>, so output keeps landing while the agent is down.</p><p><code>StreamLogs</code> reads it from an offset, the server reads it whole from offset 0 once the workload ends, and <code>Release</code> deletes it after. A containerised agent must mount the data dir at the same path inside and out, since containerd on the host writes to the path the agent names.</p>'
    },
    STREAMLOGS: {
      title: 'StreamLogs',
      badge: 'process', badgeText: 'live output',
      body: '<p>Takes <code>execution_id</code> and <code>offset</code>, the bytes the server already has. <code>Containerd.Logs</code> opens the log file, seeks to <code>offset</code>, and sends what is there as <code>LogChunk</code>s, reading the workload&rsquo;s state between copies and waiting 250 ms while it runs.</p><p>Once the workload is terminal, or released mid-stream, it sends whatever the file still holds and ends the stream. No log file is <code>Internal</code>. An agent that predates <code>offset</code> streams from the start.</p><p>The provider&rsquo;s <code>ReadLogs</code> is this stream from offset 0, which ends once the workload has, so the server captures a workload&rsquo;s whole output through it before releasing.</p><p><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    LISTEN: {
      title: 'Agent Listener',
      badge: 'process', badgeText: 'server',
      body: '<p><code>Server.ServeAgents</code> runs <code>nodes.Conns.Serve</code> (<code>internal/nodes/serve.go</code>), which accepts on <code>agent_bind</code> (default <code>127.0.0.1:4748</code>). A bind beyond loopback without an <code>agent_tls</code> block stops the server at start; with one, every agent must present a certificate signed by <code>agent_tls.ca</code>.</p><p>Each connection gets 10 s for the TLS handshake, is wrapped in a yamux session (<code>agentrpc.Accept</code>), takes the peer&rsquo;s identity from its certificate&rsquo;s common name, and is served <code>Node</code> on it. A session that cannot be set up logs <code>agent connection</code>.</p><p>When the session closes, every node registered on it is removed (<code>node disconnected</code>). Shutdown closes the listener and every session.</p><p><a href="../architecture/">Architecture diagram &rarr;</a></p>'
    },
    NODEEP: {
      title: 'Node.Register: Name Check',
      badge: 'filter', badgeText: 'identity',
      body: '<p><code>Register</code> on the <code>Node</code> endpoint in <code>internal/nodes/serve.go</code>, served on the agent&rsquo;s own session. On a TLS session the reported <code>name</code> must be the certificate&rsquo;s common name, so only the holder of a node&rsquo;s certificate can register as that node, and so replace it.</p><p>A plain session on loopback has no identity, and any name is accepted.</p><p>An accepted report is recorded in <code>nodes.Conns</code> and answered with the held workloads to release.</p>'
    },
    PERMDENIED: {
      title: 'PermissionDenied',
      badge: 'reject', badgeText: 'wrong name',
      body: '<p><code>node "&lt;cn&gt;" cannot register as "&lt;name&gt;": its certificate names "&lt;cn&gt;"</code></p><p>Nothing is recorded. A refused first registration ends the agent&rsquo;s session, which dials again with backoff. The agent itself already refuses to start with a <code>-name</code> that differs from its certificate.</p>'
    },
    CONNS: {
      title: 'nodes.Conns',
      badge: 'storage', badgeText: 'in memory',
      body: '<p>Every connected node by name: its last report, an <code>AgentExecutions</code> client on its session, the address it connected from, and when that session registered; and the room reserved on each node, by execution ID. Per server process.</p><p><code>Report</code>: a first report adds the node, which the server logs as <code>node registered</code> with <code>held=&lt;n&gt;</code>; a later one on the same session replaces what it reported; one under the same name on a new session closes the old session and replaces it. A report drops the reservation of every workload it lists.</p><p>Room is counted here once per node, whatever providers draw on it. <code>Free</code> is capacity, less the declared size of running workloads, less reservations the node does not report running, through <code>agentrpc.Free</code>, the function the agent checks with. <code>Reserve</code> checks and holds in one locked step; <code>Unreserve</code> gives the room back.</p><p><code>Remove</code> on session close forgets the nodes registered on it. A node that already reconnected is not removed when its old session closes.</p>'
    },
    NODESTATUS: {
      title: 'node status / GET /v1/nodes',
      badge: 'process', badgeText: 'read',
      body: '<p><code>vagabond node status</code> and <code>GET /v1/nodes</code> list the connected nodes by name: pool, address, labels, architecture, capacity, used CPU and memory, runtimes, version, running workloads, and when the session registered, in UTC.</p><p>Used figures are capacity less <code>nodes.Conns.Free</code>: the declared sizes of running workloads as of the node&rsquo;s last report, plus this server&rsquo;s reservations on the node that it does not report running yet.</p><p><a href="../../docs/agent/#node-status">Node status &rarr;</a></p>'
    },
    POOLCAPS: {
      title: 'containerd Provider: Live Capabilities',
      badge: 'process', badgeText: 'containerd provider',
      body: '<p><code>pool.Provider.LiveCapabilities</code> builds the provider from <code>Conns.InPool</code> on every plan (<code>plugin.Live</code>), not from the periodic refresh, so a node is schedulable once it registers and gone once its session closes. The pool is the <code>config</code> block&rsquo;s <code>pool</code>, default the provider&rsquo;s label. No node is an error, <code>no nodes have joined pool "&lt;pool&gt;"</code>, and the provider is unhealthy.</p><p>Each node is one member with its own snapshot: driver <code>container</code>, its architecture, <code>max_cpu</code> and <code>max_memory</code> as the room <code>Conns.Free</code> says it has left (0 when full), internet, private network and arbitrary images, a default size of 1000 millicores / 1024 MiB, and its labels as <code>node.label.&lt;key&gt;</code>.</p><p>Members are in <code>placement</code> order: <code>spread</code> (default) puts the most free memory first, <code>pack</code> the least, ties by name. Two providers may draw on one pool; a node&rsquo;s room is counted once.</p><p><a href="../../docs/agent/#node-attributes">Node attributes &rarr;</a></p>'
    },
    DISPATCH: {
      title: 'Dispatch',
      badge: 'entry', badgeText: 'server',
      body: '<p>The dispatcher walks the ranked candidates. For a node it reserves quota, then room on the node through <code>plugin.Reserver</code> on <code>plugin.On(provider, node)</code>, records the execution with its node, and starts a runner on the node&rsquo;s view of the provider (<code>plugin.Nodes.On</code>).</p><p>The runner sends <code>Submit</code>, then polls <code>Status</code> (2 s doubling to 15 s), reads <code>Result</code> once the state is terminal, captures the output, settles, and calls <code>Release</code>. An infrastructure failure moves the task to the next ranked candidate, as far as the retry budget allows.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    ADMIT: {
      title: 'Admission per Node',
      badge: 'filter', badgeText: 'admission',
      body: '<p><code>scheduler.admitProvider</code> runs all 13 checks against each member as if it were the whole provider: its own snapshot and labels, with the provider&rsquo;s quota, <code>meta</code> tags, health and the job&rsquo;s allowlist.</p><p>Every node that passes is a candidate of its own (<code>scheduler.Candidate.Node</code>). None passes: the provider is rejected with the closest node&rsquo;s rejection, the one with the fewest failed checks, its detail prefixed <code>closest node &lt;name&gt;: </code>.</p><p>A node&rsquo;s maximums are its free room, so a busy or full node is rejected with <code>resources-exceeded</code>.</p><p><a href="../admission/">Admission diagram &rarr;</a></p>'
    },
    PLACE: {
      title: 'Ranked Candidates',
      badge: 'process', badgeText: 'ranking',
      body: '<p>Ranking scores each node with its own <code>node.label.*</code> attributes, so a label affinity puts the nodes carrying the label first; the rest of its score is its provider&rsquo;s. Among equal scores a provider&rsquo;s nodes keep its <code>placement</code> order, ties by name.</p><p><code>job plan</code> lists each node as <code>&lt;provider&gt;/&lt;node&gt;</code>. Dispatch takes the candidates in rank order, each at most once.</p><p><a href="../../docs/agent/#placement">Placement &rarr;</a></p>'
    },
    RESERVE: {
      title: 'Room on the Node?',
      badge: 'filter', badgeText: 'node room',
      body: '<p>After the candidate&rsquo;s quota is reserved, <code>Provider.Reserve</code> calls <code>Conns.Reserve</code>, which checks and holds the task&rsquo;s size on the node in one locked step: its <code>resources</code>, 1000 millicores and 1024 MiB for unset or zero.</p><p><b>No room, or not connected</b>: <code>plugin.ErrNoRoom</code>, <code>no room on the node: node &lt;name&gt; has &lt;cpu&gt; millicores and &lt;mem&gt; MiB free; the task needs &lt;cpu&gt; and &lt;mem&gt;</code> or <code>no room on the node: node &lt;name&gt; is not connected</code>. A refusal: the quota reservation is dropped, nothing is recorded, no attempt is spent, and dispatch goes on to the next candidate.</p><p><b>Held</b>: the execution is recorded with its node, then <code>Submit</code> is sent to it. The reservation is dropped when the node&rsquo;s report includes the workload, after 2 minutes, when the task does not translate or <code>Submit</code> fails, and on <code>Release</code>.</p><p><a href="../../docs/agent/#reservations">Reservations &rarr;</a></p>'
    },
    RECORD: {
      title: 'Execution Record: Node',
      badge: 'storage', badgeText: 'store',
      body: '<p><code>execution.Record.Node</code>, column <code>node</code> in <code>executions</code>: the node the execution was placed on, written with the <code>pending</code> record before <code>Submit</code>.</p><p>Every later call for the execution goes to that node through <code>plugin.On(provider, node)</code>: from its runner, from a runner restored after a claim, and from a live output stream. A server restart changes nothing, since the node is on the record.</p><p>The answer to <code>Node.Register</code> reads each held workload&rsquo;s record to decide what to release.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    UNWANTED: {
      title: 'Answer: release[]',
      badge: 'process', badgeText: 'cleanup',
      body: '<p>Every <code>Node.Register</code> is answered with <code>NodeRegisterResponse.release</code>: the held workloads whose execution is released, was never recorded, or whose ID is not an execution ID. The server reads each held execution&rsquo;s record with the store&rsquo;s <code>Get</code>.</p><p>A record it cannot read is kept, so a server that cannot reach its store releases nothing.</p><p>The agent releases each through its own <code>Release</code>: the task killed, the container, snapshot and log file deleted. It logs <code>released a workload the server has no use for</code>. An agent that predates <code>release</code> ignores it and keeps what it holds.</p><p><a href="../../docs/agent/#releasing-what-nothing-needs">Releasing what nothing needs &rarr;</a></p>'
    },
    POOLEXEC: {
      title: 'Node Executions',
      badge: 'process', badgeText: 'containerd provider',
      body: '<p><code>Status</code>, <code>Result</code>, <code>Cancel</code>, <code>Release</code> and <code>StreamLogs</code> each go to the node the execution&rsquo;s record names, through the provider <code>plugin.On</code> returns for that node (<code>internal/providers/pool/executions.go</code>), after a server restart as before it.</p><p><code>Release</code> drops any reservation first. A <code>NotFound</code> from the node is an unknown execution; any other error is infrastructure, <code>node &lt;name&gt;: ...</code>.</p>'
    },
    GRACE: {
      title: 'Node Connected?',
      badge: 'decision', badgeText: 'connected',
      body: '<p><b>Connected</b>: the call goes to the node.</p><p><b>Not connected</b>: <code>Status</code>, <code>Result</code>, <code>Cancel</code> and <code>StreamLogs</code> fail as infrastructure, <code>node &lt;name&gt; is not connected</code>. <code>Release</code> succeeds without calling the node; the node releases the workload when it returns, from the answer to its report.</p><p><a href="../../docs/agent/#node-leaving-mid-run">Node leaving mid-run &rarr;</a></p>'
    },
    LOST: {
      title: 'lost Until the Node Returns',
      badge: 'reject', badgeText: 'node away',
      body: '<p>The runner records the failed <code>Status</code> as <code>lost</code> and answers the attempt, so the task can reroute. <code>lost</code> is not terminal: the runner keeps polling, and the execution keeps its charge and stays unreleased while the node is away, after a server restart as before it, since the record names the node.</p><p>A node that reconnects reports its workloads, and they finish normally. Once the execution has been <code>lost</code> for 24 hours (<code>LostGracePeriod</code>) with <code>Status</code> still failing, it is recorded <code>failed</code>, charged as reserved, and released.</p><p><a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Entry point |
| <span class="dg-swatch dg-process"></span> | Processing step |
| <span class="dg-swatch dg-decision"></span> | Decision / branch |
| <span class="dg-swatch dg-filter"></span> | Check: identity, room, admission |
| <span class="dg-swatch dg-storage"></span> | State: connected nodes and their room, execution record, log file |
| <span class="dg-swatch dg-success"></span> | Running workload |
| <span class="dg-swatch dg-background"></span> | Agent loops |
| <span class="dg-swatch dg-reject"></span> | Refusal / failure |
