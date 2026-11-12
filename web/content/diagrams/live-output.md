---
title: "Live Output"
linkTitle: "Live Output"
description: "Interactive diagram of a running execution's output from the provider through the server's follow route to the CLI, with resume, the Cloud Run backfill, the stored copy and 416."
weight: 6
---

How a running execution's output reaches the terminal: the CLI follower, the server's follow route, the pool, Cloud Run and Code Engine streams behind it, and the stored copy that finishes what did not stream. Every stream starts at the first byte the task printed, so a byte count is a position in all of them. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    subgraph CLISIDE [CLI]',
    '        RUN(["job run /<br>execution logs -f"]):::entry --> POLL[Poll the run<br>every 1 s]:::process',
    '        POLL -->|no result yet| WATCH{Submitted,<br>not ended?}:::decision',
    '        WATCH -->|yes, once| FOLLOWER["FollowLogs from<br>bytes written"]:::process',
    '        POLL -->|has a result| REST[Print the rest<br>of the stored copy]:::process',
    '        FOLLOWER -->|response ends| REOPEN{Reopen<br>the stream?}:::decision',
    '        REOPEN -->|"still running:<br>after 1 s"| FOLLOWER',
    '        REOPEN -->|"416 or ended:<br>follow stops"| REST',
    '        REST --> VERDICT([Verdict line]):::success',
    '    end',
    '',
    '    subgraph SRVSIDE [Server]',
    '        ROUTE["GET /v1/execution/{id}/logs"]:::process --> STREAMSEL{"follow=true, not ended,<br>LogStreamer?"}:::decision',
    '        STREAMSEL -->|no| STORED[Stored copy<br>from offset]:::process',
    '        STORED --> RECORD[(Execution record:<br>logs_start)]:::storage',
    '        STORED --> CHUNKS[(execution_logs:<br>256 KiB chunks)]:::storage',
    '        STREAMSEL -->|yes| FOLLOW[Stream and<br>flush each write]:::process',
    '        ENDFOLLOW["End of follow:<br>poll 1 s, linger 5 s"]:::background -->|cancel| FOLLOW',
    '        ENDFOLLOW -->|re-read| RECORD',
    '        SHUTDOWN[Shutdown:<br>endStreams]:::background -->|stop at once| ENDFOLLOW',
    '        R416[416 Range Not<br>Satisfiable]:::reject',
    '    end',
    '',
    '    subgraph POOLSIDE [Pool provider and agent]',
    '        POOLSL[Pool StreamLogs]:::process -->|"StreamLogs<br>with offset"| AGENTSL["Agent: seek offset,<br>poll 250 ms"]:::process',
    '        AGENTSL --> LOGFILE[(Workload<br>log file)]:::storage',
    '    end',
    '',
    '    subgraph GCPSIDE [Cloud Run provider]',
    '        TAIL["Open entries:tail,<br>queue what arrives"]:::process --> BACKFILL["entries:list:<br>what was logged before"]:::process',
    '        BACKFILL --> PLACED{Backfill<br>complete?}:::decision',
    '        PLACED -->|"failed or<br>page cap"| NORESUME[plugin.ErrNoResume]:::reject',
    '        PLACED -->|yes| DRAIN["Tail on, dropping<br>entries listed"]:::process',
    '        TAIL --> CLOUDLOG[(Cloud Logging)]:::storage',
    '        BACKFILL --> CLOUDLOG',
    '    end',
    '',
    '    subgraph IBMSIDE [Code Engine provider]',
    '        CESL["Wait for the container:<br>pod lookup every 2 s"]:::process -->|started| CEFOLLOW["pods/log?follow=true,<br>skip offset bytes"]:::process',
    '        CEFOLLOW --> PODLOG[(Pod log)]:::storage',
    '    end',
    '',
    '    FOLLOWER -->|"follow=true,<br>offset=N"| ROUTE',
    '    REST -->|"offset=N"| ROUTE',
    '    FOLLOW -->|pool| POOLSL',
    '    FOLLOW -->|Cloud Run| TAIL',
    '    FOLLOW -->|Code Engine| CESL',
    '    NORESUME --> R416',
  ].join('\n'),

  info: {
    RUN: {
      title: 'job run / execution logs -f',
      badge: 'entry', badgeText: 'entry point',
      body: '<p><code>vagabond job run</code> and <code>job dispatch</code> start a run and follow it (<code>internal/cli/dispatching.go</code>). <code>vagabond execution logs -f &lt;id&gt;</code> follows one execution with the same follower (<code>internal/cli/execution_logs.go</code>).</p><p>Progress lines (<code>==&gt; ...</code>) go to stderr and task output to stdout, so redirecting stdout captures the build alone. With <code>-no-logs</code>, <code>job run</code> and <code>job dispatch</code> fetch and print no output.</p><p>Interrupting <code>job run</code> cancels the unfinished execution on the server (30 s timeout) and exits 2. Interrupting <code>execution logs -f</code> stops following and leaves the execution running.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    POLL: {
      title: 'Poll the Run',
      badge: 'process', badgeText: 'every 1 s',
      body: '<p><code>job run</code> reads <code>GET /v1/dispatch/{id}</code> every second and goes through its executions: one with a result goes to the rest and the verdict; any other goes to <code>watch</code>, and a state change prints <code>==&gt; &lt;task&gt; &lt;state&gt; on &lt;provider&gt;</code>. The command ends when the dispatch leaves <code>running</code>.</p><p><code>execution logs -f</code> reads <code>GET /v1/execution/{id}</code> every second instead, and once the execution has a result or an end time prints the rest and exits 0.</p><p>Dispatch stores an execution&rsquo;s output before it records the result, so the output is there by the time the result is seen.</p>'
    },
    WATCH: {
      title: 'Follow This Execution?',
      badge: 'decision', badgeText: 'watch',
      body: '<p><code>follower.watch</code> starts one follow per execution, once, when it has left <code>pending</code> and has no end time. A pending execution has not been submitted, so it has nothing to stream. <code>-no-logs</code> never starts one.</p><p>When a followed execution shows an end time, <code>watch</code> tells its follow the execution is over, so no new stream is opened. The open one runs until the server ends it.</p>'
    },
    FOLLOWER: {
      title: 'FollowLogs from Bytes Written',
      badge: 'process', badgeText: 'CLI follow',
      body: '<p><code>follower.start</code> runs a goroutine that calls <code>api.Client.FollowLogs</code> with <code>offset</code> set to the bytes this execution has written to stdout so far, and copies the response body to stdout as it arrives.</p><p>Every execution&rsquo;s output shares one stdout lock, so parallel follows never interleave a write. The byte count is where a reopened stream resumes, and where the stored copy is read from at the end.</p>'
    },
    REOPEN: {
      title: 'Reopen the Stream?',
      badge: 'decision', badgeText: 'reconnect',
      body: '<p>When a response ends: a <code>416</code> stops following at once, since the provider cannot place its stream, and the stored copy finishes the output. Otherwise the follow waits 1 s and reopens from the bytes written, unless the execution has ended or the command was interrupted.</p><p>Every streaming provider starts at the first byte and skips the <code>offset</code> it is given, so a reopened stream picks up where the last one ended. A <code>416</code> comes only from Cloud Run, when its backfill fails or stops at its page cap.</p>'
    },
    REST: {
      title: 'Print the Rest of the Stored Copy',
      badge: 'process', badgeText: 'stored copy',
      body: '<p><code>follower.rest</code> runs once the execution has a result. It waits for the follow to stop, so the stream&rsquo;s last lines come before the verdict, then calls <code>api.Client.Logs</code> with <code>offset</code> set to the bytes streamed and copies what comes back to stdout, ending it with a newline if needed. <code>execution logs</code> without <code>-f</code> does the same from offset 0.</p><p>When <code>LogsStart</code> is past the bytes streamed, output between them was dropped past the server&rsquo;s ceiling, and it warns <code>&lt;n&gt; bytes of output were not kept: the server keeps the end of long output.</code> When <code>LogsTruncated</code> is set it warns <code>The provider gave only part of the output.</code></p><p>A failed read warns <code>Could not read the output of &lt;id&gt;</code>.</p>'
    },
    VERDICT: {
      title: 'Verdict Line',
      badge: 'success', badgeText: 'job run',
      body: '<p><code>==&gt; &lt;task&gt; &lt;state&gt; on &lt;provider&gt; in &lt;duration&gt;</code> on stderr, once per execution, with <code>failed (exit &lt;n&gt;)</code> for a non-zero exit and <code>: &lt;reason&gt;</code> after the duration when the platform said why it ended the task.</p><p><code>job run</code> exits 0 when every task succeeded, 1 when a task failed, and 2 when the work never ran or was interrupted. <code>execution logs -f</code> prints no verdict and exits 0.</p>'
    },
    ROUTE: {
      title: 'GET /v1/execution/{id}/logs',
      badge: 'process', badgeText: 'server route',
      body: '<p><code>Server.executionLogs</code> (<code>internal/server/executions.go</code>) reads <code>offset</code>, the bytes the caller already has: default 0, and anything but a non-negative integer is 400 <code>offset "&lt;value&gt;" is not a byte count</code>. It then reads the execution record: 404 if there is none, 503 if the store is unreachable.</p><p>The body is <code>text/plain</code> with <code>X-Content-Type-Options: nosniff</code>. Errors use the JSON error body.</p><p><a href="../../docs/api/#read-execution-logs">Read execution logs &rarr;</a></p>'
    },
    STREAMSEL: {
      title: 'Stream or Stored?',
      badge: 'decision', badgeText: 'streamer',
      body: '<p><code>Server.streamer</code> streams only when the request has <code>follow=true</code>, the record is not terminal, the provider it ran on is still configured, and that provider implements <code>plugin.LogStreamer</code>: pools, Cloud Run and Code Engine.</p><p>Anything else gets the stored copy, so a client can always ask to follow. A <code>lost</code> execution is not terminal, so it is still streamed.</p><p><a href="../../docs/writing-a-provider/#logstreamer">LogStreamer &rarr;</a></p>'
    },
    STORED: {
      title: 'Stored Copy from Offset',
      badge: 'process', badgeText: 'stored output',
      body: '<p><code>execution.PlaceLogs</code> turns the record&rsquo;s <code>logs_start</code> and the request&rsquo;s <code>offset</code> into the chunk to start at and the bytes of it to skip. An offset in output dropped past the ceiling starts at the first byte kept. The chunks are then written one at a time through <code>plugin.SkipWriter</code>, always with status 200.</p><p>Before the output is captured, and after it is pruned, the body is empty. A read that fails before anything is written is answered with its error; one that fails later ends the body and logs <code>reading stored output ended early</code>.</p>'
    },
    RECORD: {
      title: 'Execution Record',
      badge: 'storage', badgeText: 'executions',
      body: '<p>Says where the stored output stands: <code>logs_at</code> when it was captured, <code>logs_start</code> the first byte still stored, <code>logs_truncated</code> when the provider gave less than the task printed. Dispatch writes these with the result, after it has stored the output.</p><p>The output itself is not on the record, so the follow&rsquo;s re-read every second and the CLI&rsquo;s poll never read it.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    CHUNKS: {
      title: 'execution_logs',
      badge: 'storage', badgeText: 'store',
      body: '<p>The whole output in 256 KiB chunks, chunk <code>seq</code> holding bytes <code>seq &times; 256 KiB</code> onward, so an offset names its chunk. Captured by dispatch once the execution ends, from the provider&rsquo;s <code>ReadLogs</code> or the output that came with its answer. Past <code>max_log_mib</code> (16 MiB) the oldest chunks are dropped; after <code>log_retention</code> (7 days) the output is pruned.</p><p>Streaming never writes here. The stream is a view; the stored copy is the record.</p><p><a href="../../docs/dispatch/#output">Dispatch output &rarr;</a></p>'
    },
    FOLLOW: {
      title: 'Stream and Flush Each Write',
      badge: 'process', badgeText: 'server stream',
      body: '<p><code>Server.follow</code> calls the provider&rsquo;s <code>StreamLogs</code> with the request&rsquo;s <code>offset</code> and a writer that flushes every write through <code>http.ResponseController</code>. Headers go out with the first write, so a stream refused before any output can still be answered with a status.</p><p>The response ends when the provider ends its stream, when the end of follow cancels it, when the client disconnects, or at shutdown. A stream that breaks ends the response early, still 200, and logs <code>log stream ended early</code>; the client reopens from the bytes it has.</p><p>The stream never touches the execution: a broken stream is not a failed run.</p>'
    },
    ENDFOLLOW: {
      title: 'End of Follow',
      badge: 'background', badgeText: 'per stream',
      body: '<p><code>Server.endFollow</code> runs beside each stream. It re-reads the execution record every second, retrying a read that fails. Once the record is terminal it waits 5 s more and cancels the stream, because some providers&rsquo; logs trail the work: Cloud Logging runs seconds behind the container.</p><p>A Cloud Run tail never ends by itself, so this is what ends it. A pool stream usually ends first, when the agent sees the workload exit, and a Code Engine stream when its container exits.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    SHUTDOWN: {
      title: 'Shutdown: endStreams',
      badge: 'background', badgeText: 'shutdown',
      body: '<p><code>Server.endStreams</code> is registered with <code>http.Server.RegisterOnShutdown</code> and closes the server&rsquo;s <code>stopping</code> channel once. Every end of follow sees it and cancels its stream at once, so a live stream does not hold the 10 s drain open.</p><p>To the CLI the response simply ended; it reopens from the bytes written a second later.</p>'
    },
    R416: {
      title: '416 Range Not Satisfiable',
      badge: 'reject', badgeText: 'cannot be placed',
      body: '<p><code>this provider&#39;s log stream cannot resume from an offset</code>, as the JSON error body, sent only when nothing has been written yet.</p><p>The CLI follow stops streaming that execution and does not reopen it. Once the execution has a result, the rest is read from the stored copy with <code>?offset=</code> set to the bytes streamed, which places it exactly.</p>'
    },
    POOLSL: {
      title: 'Pool StreamLogs',
      badge: 'process', badgeText: 'pool provider',
      body: '<p><code>pool.Provider.StreamLogs</code> finds the node holding the execution; one that is not connected fails <code>the node holding &lt;id&gt; is not connected</code> within 5 minutes of leaving and <code>node &lt;name&gt; holding &lt;id&gt; is gone</code> after, which ends the response early. Otherwise it opens <code>AgentExecutions.StreamLogs</code> with <code>execution_id</code> and <code>offset</code> down the agent&rsquo;s session and writes each <code>LogChunk</code> to the response until the agent ends the stream.</p><p>The pool honours <code>offset</code>, so a reopened stream resumes exactly where the last one stopped. Its <code>ReadLogs</code> is the same stream from offset 0, which ends once the workload has.</p><p><a href="../agents/">Agents and pools diagram &rarr;</a></p>'
    },
    AGENTSL: {
      title: 'Agent: Seek Offset, Poll 250 ms',
      badge: 'process', badgeText: 'agent',
      body: '<p><code>Containerd.Logs</code> opens <code>&lt;data-dir&gt;/logs/&lt;execution-id&gt;.log</code>, seeks to <code>offset</code>, and sends what is there. Between copies it reads the workload&rsquo;s state, waiting 250 ms while it runs.</p><p>Once the workload is terminal, or released since the last copy, it sends whatever the file still holds and returns, which ends the stream and the response. No log file is <code>Internal</code>. An agent that predates <code>offset</code> streams from byte 0.</p><p><a href="../../docs/agent/#output">Agent output &rarr;</a></p>'
    },
    LOGFILE: {
      title: 'Workload Log File',
      badge: 'storage', badgeText: 'node disk',
      body: '<p>containerd writes the container&rsquo;s stdout and stderr to the file itself, so output keeps landing while the agent or the server is down, and a reopened stream can read it from any offset.</p><p>Dispatch reads it whole through the same stream once the workload ends, before <code>Release</code> deletes it.</p>'
    },
    TAIL: {
      title: 'Open entries:tail, Queue What Arrives',
      badge: 'process', badgeText: 'Cloud Run provider',
      body: '<p><code>gcp.Provider.StreamLogs</code> opens the tail first, so nothing logged while the backfill is listed is missed. <code>openTail</code> POSTs to <code>/v2/entries:tail</code> a one-element JSON array, since a streaming method&rsquo;s body is a stream of requests, filtering on <code>resource.type="cloud_run_job"</code> and the job name, with a 1 s <code>bufferWindow</code> so stderr does not arrive before the stdout that preceded it.</p><p>A goroutine decodes the open response with <code>decodeTail</code> into a queue of up to 10,000 entries from the moment it opens, because Cloud Logging drops entries a client does not read quickly enough. Once open, every way the tail stops ends it without an error.</p>'
    },
    BACKFILL: {
      title: 'entries:list: What Was Logged Before',
      badge: 'process', badgeText: 'Cloud Logging',
      body: '<p>With the tail open, <code>listEntries</code> pages <code>entries:list</code> oldest first, 1000 entries a page, the same query <code>ReadLogs</code> uses, and writes each entry&rsquo;s <code>textPayload</code> plus a newline, skipping structural entries. Each entry&rsquo;s <code>timestamp</code> and <code>insertId</code> are kept to recognise its copy on the tail.</p><p>The writer skips the first <code>offset</code> bytes across the backfill and the tail, so the stream starts at the first byte the task printed and a reopened one picks up where the last ended.</p><p>Every page is one of the 60 <code>entries:list</code> requests a minute Cloud Logging allows per project.</p>'
    },
    PLACED: {
      title: 'Backfill Complete?',
      badge: 'decision', badgeText: 'placed',
      body: '<p>A backfill that was refused, or that reached 20 pages with more left, cannot say where the tail begins, so the stream would no longer start at the first byte. It returns <code>plugin.ErrNoResume</code> rather than start somewhere else and leave the client&rsquo;s count no longer a position.</p><p>A write that fails during the backfill is the client going away, and is returned as it is.</p>'
    },
    DRAIN: {
      title: 'Tail On, Dropping Entries Listed',
      badge: 'process', badgeText: 'live',
      body: '<p>The queue is drained, then the tail read as it arrives. An entry the backfill already wrote, by <code>timestamp</code> and <code>insertId</code>, is dropped; the rest are written, one line each.</p><p>An entry that reached Cloud Logging after the backfill listed later ones, with an earlier timestamp, is streamed after them but stored before them, so a stream reopened across it can repeat or skip its bytes.</p><p><a href="../../docs/providers/cloud-run/#live-output">Cloud Run live output &rarr;</a></p>'
    },
    NORESUME: {
      title: 'plugin.ErrNoResume',
      badge: 'reject', badgeText: 'sentinel',
      body: '<p>The <code>plugin</code> package&rsquo;s sentinel for a log stream that cannot be placed at an offset. <code>Server.follow</code> answers it with <code>416</code>, provided nothing has been written yet; otherwise the response ends and the client reopens, when nothing new is written and the <code>416</code> follows.</p>'
    },
    CLOUDLOG: {
      title: 'Cloud Logging',
      badge: 'storage', badgeText: 'Google Cloud',
      body: '<p>Where the Cloud Run job&rsquo;s container output lands, seconds behind the container. The tail reads it live and the backfill reads what came before; <code>ReadLogs</code> reads it whole with <code>entries:list</code> once the job finishes. The entries outlive the job, so <code>Release</code> destroys nothing these read.</p>'
    },
    CESL: {
      title: 'Code Engine: Wait for the Container',
      badge: 'process', badgeText: 'Code Engine provider',
      body: '<p><code>ibm.Provider.StreamLogs</code> looks up the run&rsquo;s pod by its <code>codeengine.cloud.ibm.com/job-run</code> label every 2 s until <code>user-container</code> is running or has exited. Before that the log cannot be opened, and asking answers 400.</p><p>Waiting on the pod&rsquo;s state, rather than retrying a 400, means a real 400 is not hidden. A container that never starts, such as one whose image cannot be pulled, is waited on until the end of follow cancels the stream, which is a normal end. A pod lookup that fails ends the response early.</p>'
    },
    CEFOLLOW: {
      title: 'pods/log?follow=true',
      badge: 'process', badgeText: 'Kubernetes API',
      body: '<p><code>openLog</code> opens <code>GET /api/v1/namespaces/{ns}/pods/{pod}/log?container=user-container&amp;follow=true</code> on the project&rsquo;s Kubernetes API with the same IAM token as every other call. A refusal is classified like any call and ends the response early.</p><p>The followed log starts at the container&rsquo;s first line, so <code>plugin.SkipWriter</code> discards the first <code>offset</code> bytes and <code>plugin.CopyLive</code> copies the rest as it arrives. A reopened stream resumes exactly where the last one stopped and is never answered <code>416</code>. A read that fails ends the copy quietly; the stream ends when the container exits.</p><p><a href="../../docs/providers/code-engine/#live-output">Code Engine live output &rarr;</a></p>'
    },
    PODLOG: {
      title: 'Pod Log',
      badge: 'storage', badgeText: 'Code Engine',
      body: '<p>The task container&rsquo;s stdout and stderr, interleaved, each in order but not always in order with each other, kept with the pod after the container exits. <code>ReadLogs</code> reads it whole, unfollowed, once the run ends; deleting the job run removes it.</p>'
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
| <span class="dg-swatch dg-storage"></span> | Where output is kept |
| <span class="dg-swatch dg-success"></span> | Verdict |
| <span class="dg-swatch dg-background"></span> | Stream lifetime |
| <span class="dg-swatch dg-reject"></span> | Cannot be placed |
