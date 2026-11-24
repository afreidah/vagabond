---
title: "Live Output"
linkTitle: "Live Output"
description: "Interactive diagram of a running execution's output: captured into the store by the server holding its dispatch, from agent nodes, Cloud Run, Cloud Build and Code Engine, and followed from the store by the CLI through any server."
weight: 7
---

How a running execution's output reaches the terminal: the execution's runner captures it into the store while it runs, from an agent node, Cloud Logging for Cloud Run and Cloud Build, or the pod log for Code Engine, and the CLI follows the stored copy through whichever server answers. There is one copy, written once and read by every client, and positions in it count from the first byte the task printed. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    subgraph RUNNER [Execution runner, on the server holding the dispatch]',
    '        SUBMITTED(["Provider has<br>the work"]):::entry --> CAPTURE["CaptureLogs<br>into the log"]:::process',
    '        CAPTURE -->|returned early| AGAIN{"Output<br>whole?"}:::decision',
    '        AGAIN -->|"no: after 1 s<br>doubling to 30 s"| CAPTURE',
    '        CAPTURE --> SINK["Log: chunks,<br>stored every 1 s"]:::process',
    '        SINK --> CHUNKS[(execution_logs:<br>256 KiB chunks)]:::storage',
    '        WATCHRUN["Watch: Status<br>until terminal"]:::background -->|ended closes| CAPTURE',
    '        AGAIN -->|"yes, or given up"| COLLECT["Collect: result<br>and record"]:::process',
    '        COLLECT --> RECORD[(Execution record:<br>terminal, logs_at)]:::storage',
    '        TAKEOVER["Dispatch claimed<br>by another server"]:::background -->|resume from the store| SINK',
    '    end',
    '',
    '    subgraph POOLSIDE [Agent pools]',
    '        POOLCAP["Node stream<br>from log.Len()"]:::process --> AGENTSL["Agent: the executor<br>holding it, poll 250 ms"]:::process',
    '        AGENTSL -->|container| LOGFILE[(Workload<br>log file)]:::storage',
    '        AGENTSL -->|"microVM:<br>ReadLogs"| VMLOG[("vagabond-firecracker:<br>output.log")]:::storage',
    '    end',
    '',
    '    subgraph GCPSIDE [Cloud Run and Cloud Build]',
    '        TAIL["Open entries:tail,<br>queue what arrives"]:::process --> CATCHUP["Catch-up: entries:list,<br>append missing lines"]:::process',
    '        CATCHUP --> DRAIN["Tail on, dropping<br>entries listed"]:::process',
    '        DRAIN -->|ended closes| SETTLE["Wait 5 s,<br>catch up again"]:::process',
    '        TAIL --> CLOUDLOG[(Cloud Logging)]:::storage',
    '        CATCHUP --> CLOUDLOG',
    '    end',
    '',
    '    subgraph IBMSIDE [Code Engine]',
    '        CEWAIT["Wait for the container:<br>pod lookup every 2 s"]:::process -->|started| CEFOLLOW["pods/log?follow=true,<br>skip what is held"]:::process',
    '        CEFOLLOW -->|follow ends| CECHECK{"Container<br>exited?"}:::decision',
    '        CECHECK -->|"no: a cut"| CERETRY["Error: the runner<br>calls again"]:::reject',
    '        CECHECK -->|"not seen:<br>once ended"| CEREST["Read the whole log,<br>skip what is held"]:::process',
    '        CEFOLLOW --> PODLOG[(Pod log)]:::storage',
    '        CEREST --> PODLOG',
    '    end',
    '',
    '    subgraph SRVSIDE [Any server]',
    '        ROUTE["GET /v1/execution/{id}/logs"]:::process --> SEND["Send from offset,<br>Vagabond-Logs-Start"]:::process',
    '        SEND -->|follow=true| TERMINAL{"Record terminal<br>before this read?"}:::decision',
    '        TERMINAL -->|"no: read again<br>after 1 s"| SEND',
    '        SHUTDOWN[Shutdown:<br>endStreams]:::background -->|end at once| SEND',
    '    end',
    '',
    '    subgraph CLISIDE [CLI]',
    '        RUN(["job run /<br>execution logs -f"]):::entry --> POLL[Poll the run<br>every 1 s]:::process',
    '        POLL -->|left pending| FOLLOWER["FollowLogs from<br>the bytes printed"]:::process',
    '        FOLLOWER -->|response ends| ASKAGAIN{"Asked after<br>the end?"}:::decision',
    '        ASKAGAIN -->|"no: at the end,<br>or after 1 s"| FOLLOWER',
    '        ASKAGAIN -->|yes| VERDICT([Verdict line]):::success',
    '    end',
    '',
    '    CAPTURE -->|"containerd,<br>firecracker"| POOLCAP',
    '    CAPTURE -->|"Cloud Run,<br>Cloud Build"| TAIL',
    '    CAPTURE -->|Code Engine| CEWAIT',
    '    FOLLOWER -->|"follow=true,<br>offset=N"| ROUTE',
    '    SEND --> CHUNKS',
    '    TERMINAL -->|re-read| RECORD',
  ].join('\n'),

  info: {
    SUBMITTED: {
      title: 'Provider Has the Work',
      badge: 'entry', badgeText: 'runner',
      body: '<p>Once <code>Submit</code> has returned, or a restored runner has found the execution at its provider, the runner (<code>internal/dispatch/runner.go</code>) starts one capture for it, on a provider that implements <code>plugin.LogCapturer</code>: <code>containerd</code>, <code>firecracker</code>, Cloud Run, Cloud Build and Code Engine. A synchronous provider such as Lambda hands its output over with the answer, which is stored once it arrives.</p><p>No capture starts for a failed <code>Submit</code>, or for an execution whose output was already collected.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    CAPTURE: {
      title: 'CaptureLogs into the Log',
      badge: 'process', badgeText: 'one per execution',
      body: '<p><code>capture.run</code> (<code>internal/dispatch/logs.go</code>) calls the provider&rsquo;s <code>CaptureLogs</code> with the execution&rsquo;s log and an <code>ended</code> channel. The provider writes from <code>log.Len()</code> on, since the log may hold what an earlier call or another server captured. It returns <code>nil</code> only once <code>ended</code> has closed and the log holds all of the output.</p><p>This is the only reader of the provider&rsquo;s output. No client and no other server opens the provider&rsquo;s stream, so Cloud Logging&rsquo;s 10 open tails a project count running executions, not viewers.</p><p><a href="../../docs/writing-a-provider/#logcapturer">LogCapturer &rarr;</a></p>'
    },
    AGAIN: {
      title: 'Output Whole?',
      badge: 'decision', badgeText: 'retry',
      body: '<p>A call that returns anything but <code>nil</code> is logged as <code>capturing output</code> with its error, which on Cloud Run names why the tail ended, and made again after 1 s doubling to 30 s, back to 1 s after a call that added output. A backoff in progress ends early when the execution ends.</p><p>Once <code>ended</code> has closed, three failed calls, or 5 minutes, end the capture with what it has, and the record is marked <code>logs_truncated</code>. The execution is never failed over its output.</p>'
    },
    SINK: {
      title: 'Log: Chunks, Stored Every 1 s',
      badge: 'process', badgeText: 'logSink',
      body: '<p>What the provider writes is cut into 256 KiB chunks. A chunk is stored as it fills, and the one filling is stored again at least every second while output arrives, so the store is never more than a second behind. Past <code>max_log_mib</code> (16 MiB) the front is dropped a chunk at a time.</p><p>A store write that fails keeps the output for the next one. A write the dispatch&rsquo;s lease refused ends the capture, since another server now holds the execution. However the capture ends, what arrived is stored.</p>'
    },
    CHUNKS: {
      title: 'execution_logs',
      badge: 'storage', badgeText: 'store',
      body: '<p>The output in 256 KiB chunks, chunk <code>seq</code> holding bytes <code>seq &times; 256 KiB</code> onward, so an offset names its chunk. The last chunk grows in place: each write replaces it and drops any after it in one transaction, so no read sees two versions of it. A read hands each chunk over with its number, so a reader sees where a dropped front ends.</p><p>The only copy. Every client, on every server, reads these bytes in this order. After <code>log_retention</code> (7 days) past the execution&rsquo;s end they are pruned.</p><p><a href="../database-schema/">Database schema diagram &rarr;</a></p>'
    },
    WATCHRUN: {
      title: 'Watch: Status Until Terminal',
      badge: 'background', badgeText: 'runner',
      body: '<p>The runner polls the provider&rsquo;s <code>Status</code> as it always has. When it sees the execution terminal, or cancels it, it moves on to collect, which closes the capture&rsquo;s <code>ended</code> channel: the provider then finishes what it still holds, such as Cloud Logging&rsquo;s late entries or the end of a pod log a follow missed.</p>'
    },
    COLLECT: {
      title: 'Collect: Result and Record',
      badge: 'process', badgeText: 'runner',
      body: '<p><code>collect</code> reads the result, then waits for the capture, up to 5 minutes after the end, then writes the terminal state, the result, <code>logs_at</code>, <code>logs_start</code> and <code>logs_truncated</code> in one write. The terminal state is written only after the last of the output is stored, which is what tells a follower the output is whole.</p><p><code>Release</code> comes after, since it may destroy what the capture reads.</p>'
    },
    RECORD: {
      title: 'Execution Record',
      badge: 'storage', badgeText: 'executions',
      body: '<p>Says where the stored output stands once the execution ends: <code>logs_at</code> when the capture finished, <code>logs_start</code> the first byte still stored, <code>logs_truncated</code> when the provider gave less than the task printed.</p><p>The output itself is not on the record, so a follow&rsquo;s re-read every second and the CLI&rsquo;s poll never read it.</p>'
    },
    TAKEOVER: {
      title: 'Dispatch Claimed by Another Server',
      badge: 'background', badgeText: 'resume',
      body: '<p>A server that claims a dispatch whose holder died restores a runner for each execution, and a running one gets a new capture. It reads the stored chunks back first: the first one stored is where the output now starts, and a last chunk not yet full is the one filling. The provider is then asked from <code>log.Len()</code>, so the capture carries on where the last server&rsquo;s stopped.</p><p>A follower on the old server sees its response end and asks again, reaching a server that reads the same store.</p><p><a href="../../docs/dispatch/#resuming">Resuming &rarr;</a></p>'
    },
    POOLCAP: {
      title: 'Node Stream from log.Len()',
      badge: 'process', badgeText: 'pool provider',
      body: '<p><code>pool.Provider.CaptureLogs</code>, for <code>containerd</code> and <code>firecracker</code> providers alike, opens <code>AgentExecutions.StreamLogs</code> on the node the record names, with <code>offset</code> set to <code>log.Len()</code>, and writes each <code>LogChunk</code> into the log. A chunk that begins past where the last ended, the node having dropped the front past its <code>-max-log-mib</code>, is placed through <code>log.StartsAt</code>.</p><p>The node&rsquo;s stream ends when the workload does, so the output is then whole and the call waits for <code>ended</code>. A node that is not connected fails the call, and the runner calls again.</p><p><a href="../agents/">Agents and pools diagram &rarr;</a></p>'
    },
    AGENTSL: {
      title: 'Agent: the Executor Holding It',
      badge: 'process', badgeText: 'agent',
      body: '<p>The agent asks each executor in turn; one that does not hold the execution says so before writing anything. For a container, <code>Containerd.Logs</code> opens <code>&lt;data-dir&gt;/logs/&lt;execution-id&gt;.log</code>, seeks to <code>offset</code>, and sends what is there, reading the workload&rsquo;s state between copies and waiting 250 ms while it runs. For a microVM, the firecracker executor reads vagabond-firecracker&rsquo;s <code>ReadLogs</code> from <code>offset</code> until it reports the output ended.</p><p>Once the workload is terminal, or released since the last copy, it sends what is left and returns, which ends the stream.</p><p><a href="../../docs/containerd/#output">containerd output &rarr;</a></p>'
    },
    LOGFILE: {
      title: 'Workload Log File',
      badge: 'storage', badgeText: 'node disk',
      body: '<p>containerd writes the container&rsquo;s stdout and stderr to the file itself, so output keeps landing while the agent or the server is down, and a capture made again reads it from any offset. <code>Release</code> deletes it, after the capture has ended.</p>'
    },
    VMLOG: {
      title: 'MicroVM Output',
      badge: 'storage', badgeText: 'node disk',
      body: '<p>vagabond-firecracker copies a microVM&rsquo;s stdout and stderr from its guest to <code>&lt;state-dir&gt;/&lt;execution-id&gt;/output.log</code> as they are written. <code>ReadLogs</code> returns it from an offset, up to 64 KiB a call, and <code>ended</code> once the workload has ended and the offset reached the end. Kept until <code>Release</code>.</p><p><a href="../firecracker/">Firecracker diagram &rarr;</a></p>'
    },
    TAIL: {
      title: 'Open entries:tail, Queue What Arrives',
      badge: 'process', badgeText: 'Cloud Logging tail',
      body: '<p><code>gcp.API.CaptureEntries</code>, which both Google providers&rsquo; <code>CaptureLogs</code> call, opens the tail first, so nothing logged while the catch-up lists is missed. <code>openTail</code> POSTs to <code>/v2/entries:tail</code> a one-element JSON array, since a streaming method&rsquo;s body is a stream of requests, with the job&rsquo;s or build&rsquo;s filter and a 5 s <code>bufferWindow</code>, which Cloud Logging holds entries for to deliver them in timestamp order.</p><p>A goroutine decodes the open response into a queue of up to 10,000 entries, because Cloud Logging drops entries a client does not read quickly enough. A tail never ends of its own accord, so its end is an error saying why: the array closing, a read failing, or the error Google sends as the last element.</p>'
    },
    CATCHUP: {
      title: 'Catch-up: Append Missing Lines',
      badge: 'process', badgeText: 'entries:list',
      body: '<p><code>catchUp</code> reads the log back, counts its lines by text, then pages <code>entries:list</code> oldest first and appends each entry whose line the log does not hold. A line held twice matches two entries. Nothing lives only in one server&rsquo;s memory, so a capture another server began is finished from the log alone.</p><p>It runs each time a tail opens and once at the end. A log ending mid-line, as a server that stopped mid-write leaves one, gets a line break first. Each page is one of the 60 <code>entries:list</code> requests a minute Cloud Logging allows a project; past 20 pages it returns an error after appending what it read.</p>'
    },
    DRAIN: {
      title: 'Tail On, Dropping Entries Listed',
      badge: 'process', badgeText: 'live',
      body: '<p>The queue is drained, then the tail read as it arrives. An entry the catch-up listed, by <code>timestamp</code> and <code>insertId</code>, is dropped; the rest are written, one line each.</p><p>An entry later than the buffer window is delivered when it arrives and written there, after lines printed later. The log is append-only, so it keeps that order for every reader; <code>entries:list</code> and the Cloud Console show the timestamp order.</p><p><a href="../../docs/providers/cloud-run/#output">Cloud Run output &rarr;</a></p>'
    },
    SETTLE: {
      title: 'Wait 5 s, Catch Up Again',
      badge: 'process', badgeText: 'the end',
      body: '<p>When <code>ended</code> closes, the tail is closed, and after 5 s for entries still on their way the capture catches up once more. Anything the tail left out, or delivered after the run ended, is appended then, and the call returns <code>nil</code>.</p><p>A capture made after the run is over, by a runner restored after a claim, does only this.</p>'
    },
    CLOUDLOG: {
      title: 'Cloud Logging',
      badge: 'storage', badgeText: 'Google Cloud',
      body: '<p>Where a Cloud Run job&rsquo;s container output lands, and a Cloud Build step&rsquo;s, one <code>textPayload</code> per line, seconds behind the container. The entries outlive the job and the build, so <code>Release</code> and <code>Cancel</code> destroy nothing the capture reads.</p>'
    },
    CEWAIT: {
      title: 'Code Engine: Wait for the Container',
      badge: 'process', badgeText: 'Code Engine provider',
      body: '<p><code>ibm.Provider.CaptureLogs</code> looks up the run&rsquo;s pod by its <code>codeengine.cloud.ibm.com/job-run</code> label every 2 s until <code>user-container</code> is running or has exited. Before that the log cannot be opened, and asking answers 400. A run that ends before its container starts stops the wait.</p>'
    },
    CEFOLLOW: {
      title: 'pods/log?follow=true',
      badge: 'process', badgeText: 'Kubernetes API',
      body: '<p><code>openLog</code> opens <code>GET /api/v1/namespaces/{ns}/pods/{pod}/log?container=user-container&amp;follow=true</code> on the project&rsquo;s Kubernetes API. The log starts at the container&rsquo;s first line, so <code>plugin.SkipWriter</code> discards the bytes the log already holds and <code>plugin.CopyAll</code> copies the rest as it arrives, reporting a failed read.</p><p><a href="../../docs/providers/code-engine/#output">Code Engine output &rarr;</a></p>'
    },
    CECHECK: {
      title: 'Container Exited?',
      badge: 'decision', badgeText: 'cut or end',
      body: '<p>A follow ends when the container exits, and also when something on the way cuts a quiet connection, so the pod is looked up again. A container that exited means the log was followed to its end, and once <code>ended</code> closes nothing more is read.</p>'
    },
    CERETRY: {
      title: 'Cut: Called Again',
      badge: 'reject', badgeText: 'error',
      body: '<p>A container still running means the follow was cut. The call returns <code>the log of job run &lt;run&gt; ended while its container runs</code>, the runner logs it, and the next call follows again from what the log holds.</p>'
    },
    CEREST: {
      title: 'Read the Whole Log',
      badge: 'process', badgeText: 'once ended',
      body: '<p>When the follow did not see the container exit, or the call began after the run was over, the log is read whole, unfollowed, and the bytes already held are skipped. A pod already gone, as a cancel leaves it, is output lost: three such calls leave the output marked truncated.</p>'
    },
    PODLOG: {
      title: 'Pod Log',
      badge: 'storage', badgeText: 'Code Engine',
      body: '<p>The task container&rsquo;s stdout and stderr, interleaved, each in order but not always in order with each other, kept with the pod after the container exits. Deleting the job run, which <code>Cancel</code> and <code>Release</code> do, removes it.</p>'
    },
    ROUTE: {
      title: 'GET /v1/execution/{id}/logs',
      badge: 'process', badgeText: 'any server',
      body: '<p><code>Server.executionLogs</code> (<code>internal/server/executions.go</code>) reads <code>offset</code>, the bytes the caller already has: default 0, and anything but a non-negative integer is 400. It then reads the execution record: 404 if there is none, 503 if the store is unreachable.</p><p>Any server answers, since every one reads the same store. The body is <code>text/plain</code>; errors use the JSON error body.</p><p><a href="../../docs/api/#read-execution-logs">Read execution logs &rarr;</a></p>'
    },
    SEND: {
      title: 'Send from Offset',
      badge: 'process', badgeText: 'sendLogs',
      body: '<p><code>Server.sendLogs</code> (<code>internal/server/follow.go</code>) reads the chunks from <code>offset</code>&rsquo;s on through <code>execution.Place</code>, which skips what of the first chunk precedes the offset and flushes each write to the client. The <code>Vagabond-Logs-Start</code> header says where the body begins: the offset, or the first byte kept when the output there was dropped past the ceiling.</p><p>Output dropped after the body began ends the response, so the client asks again and is told the new start. A read that fails before anything is sent is answered with its error; one that fails later ends the body and logs <code>reading stored output ended early</code>.</p>'
    },
    TERMINAL: {
      title: 'Record Terminal Before This Read?',
      badge: 'decision', badgeText: 'follow',
      body: '<p>With <code>follow=true</code>, the server reads the record and the store again every second, sending what was stored since. It reads the record before the output, and the runner writes the terminal state only after the last of the output, so a read made once the record was terminal holds all of it; the response then ends.</p><p>A follow of an execution that has already ended is a plain read.</p>'
    },
    SHUTDOWN: {
      title: 'Shutdown: endStreams',
      badge: 'background', badgeText: 'shutdown',
      body: '<p><code>Server.endStreams</code> is registered with <code>http.Server.RegisterOnShutdown</code> and closes the server&rsquo;s <code>stopping</code> channel once. Every follow ends at once, so it does not hold the 10 s drain open.</p><p>To the CLI the response simply ended before the execution did; it asks again from the bytes printed.</p>'
    },
    RUN: {
      title: 'job run / execution logs -f',
      badge: 'entry', badgeText: 'entry point',
      body: '<p><code>vagabond job run</code> and <code>job dispatch</code> start a run and follow it (<code>internal/cli/dispatching.go</code>). <code>vagabond execution logs -f &lt;id&gt;</code> follows one execution with the same follower (<code>internal/cli/execution_logs.go</code>); without <code>-f</code> it prints what is stored so far.</p><p>Progress lines (<code>==&gt; ...</code>) go to stderr and task output to stdout, so redirecting stdout captures the build alone. With <code>-no-logs</code>, no output is fetched.</p>'
    },
    POLL: {
      title: 'Poll the Run',
      badge: 'process', badgeText: 'every 1 s',
      body: '<p><code>job run</code> reads <code>GET /v1/dispatch/{id}</code> every second. An execution that has left <code>pending</code> gets one follow; one that shows an end time tells its follow the output is whole; one with a result waits for its follow to finish, then prints the verdict.</p><p><code>execution logs -f</code> reads <code>GET /v1/execution/{id}</code> every second instead.</p>'
    },
    FOLLOWER: {
      title: 'FollowLogs from the Bytes Printed',
      badge: 'process', badgeText: 'CLI follow',
      body: '<p><code>follower.start</code> runs a goroutine that calls <code>api.Client.FollowLogs</code> with <code>offset</code> set to where this execution&rsquo;s output has got to, and copies the body to stdout as it arrives. Every byte is printed once, in the order the server stored it.</p><p>When <code>Vagabond-Logs-Start</code> is past the offset asked for, output between them was dropped past the server&rsquo;s or the node&rsquo;s ceiling: the CLI warns <code>&lt;n&gt; bytes of output were not kept: only the end of long output is kept.</code> and carries on from there.</p>'
    },
    ASKAGAIN: {
      title: 'Asked After the End?',
      badge: 'decision', badgeText: 'reconnect',
      body: '<p>A response asked for once the execution had ended ends with the last of the output, so the follow is done. Any other response that ends is the output not yet whole, or a server shutting down, so the follow asks again from the bytes printed: at once when the execution ends meanwhile, otherwise after 1 s. A response that broke waits 1 s.</p><p>Once the execution has ended, three broken reads in a row give up with <code>Could not read the output of &lt;id&gt;</code>.</p>'
    },
    VERDICT: {
      title: 'Verdict Line',
      badge: 'success', badgeText: 'job run',
      body: '<p>Printed after all of the output. <code>==&gt; &lt;task&gt; &lt;state&gt; on &lt;provider&gt; in &lt;duration&gt;</code> on stderr, once per execution, with <code>failed (exit &lt;n&gt;)</code> for a non-zero exit and <code>: &lt;reason&gt;</code> when the platform said why it ended the task. <code>The provider gave only part of the output.</code> comes first when the capture ended short.</p><p><code>job run</code> exits 0 when every task succeeded, 1 when a task failed, and 2 when the work never ran or was interrupted. <code>execution logs -f</code> prints no verdict and exits 0.</p>'
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
| <span class="dg-swatch dg-background"></span> | Runs beside: watch, takeover, shutdown |
| <span class="dg-swatch dg-reject"></span> | Returned for the runner to call again |
