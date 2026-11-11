---
title: "Execution States"
linkTitle: "Execution States"
description: "Interactive state machine of an execution record and its dispatch: every legal transition, what causes it, and which component writes it."
weight: 7
---

The execution state machine as `execution.State` allows it, and the four states of the dispatch record above it. The machine is acyclic: a retry is a new execution record starting at `pending`, never a transition back, and a transition the table refuses is not written. **Hover over any state** for what moves a record into it and which component writes it.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    subgraph EXEC [Execution record]',
    '        PENDING([pending]):::entry',
    '        SUBMITTED[submitted]:::process',
    '        ACCEPTED[accepted]:::process',
    '        RUNNING[running]:::process',
    '        LOST[lost]:::filter',
    '        SUCCEEDED([succeeded]):::success',
    '        FAILED([failed]):::reject',
    '        CANCELLED([cancelled]):::background',
    '    end',
    '    PENDING -->|Submit returned| SUBMITTED',
    '    PENDING -->|Submit failed| FAILED',
    '    PENDING -->|cancel request| CANCELLED',
    '    SUBMITTED -->|queued| ACCEPTED',
    '    SUBMITTED -->|started| RUNNING',
    '    SUBMITTED --> SUCCEEDED',
    '    SUBMITTED --> FAILED',
    '    SUBMITTED --> CANCELLED',
    '    SUBMITTED -->|Status fails| LOST',
    '    ACCEPTED -->|started| RUNNING',
    '    ACCEPTED --> SUCCEEDED',
    '    ACCEPTED --> FAILED',
    '    ACCEPTED --> CANCELLED',
    '    ACCEPTED --> LOST',
    '    RUNNING --> SUCCEEDED',
    '    RUNNING --> FAILED',
    '    RUNNING --> CANCELLED',
    '    RUNNING --> LOST',
    '    LOST -->|reconciled| SUCCEEDED',
    '    LOST --> FAILED',
    '    LOST --> CANCELLED',
    '    FAILED -.->|infrastructure| REROUTE',
    '    LOST -.->|infrastructure| REROUTE',
    '    CANCELLED -.->|by the provider| REROUTE',
    '    REROUTE[Next attempt<br>new record at pending]:::storage',
    '',
    '    subgraph DISP [Dispatch record]',
    '        DRUN([running]):::entry',
    '        CLAIM[Claimed and resumed<br>lease lapsed]:::storage',
    '        DSUCC([succeeded]):::success',
    '        DFAIL([failed]):::reject',
    '        DUNANS([unanswered]):::background',
    '    end',
    '    DRUN -->|every task succeeded| DSUCC',
    '    DRUN -->|a task failed| DFAIL',
    '    DRUN -->|a task returned an error| DUNANS',
    '    DRUN -->|lease lapsed| CLAIM',
    '    CLAIM -->|by its last execution| DSUCC',
    '    CLAIM --> DFAIL',
    '    CLAIM --> DUNANS',
    '    REROUTE ~~~ DRUN',
  ].join('\n'),

  info: {
    PENDING: {
      title: 'pending',
      badge: 'entry', badgeText: 'initial state',
      body: '<p>Written by <code>prepare</code> when the record is created: after the quota reservation, before <code>Submit</code>. This write must land, and a failure stops the task: the execution ID is the submission&rsquo;s idempotency key, so it has to exist durably first.</p><p>Leaves on <code>Submit</code>: to <code>submitted</code> when it returns, to <code>failed</code> with the failure class when it fails. A <code>Submit</code> refused as <code>internal</code> ran nothing, so its quota reservation is dropped; any other failure leaves it to the reaper. A dispatch cancelled during the retry backoff leaves the record <code>pending</code> and its reservation to the reaper. A resumed dispatch moves a <code>pending</code> record to <code>submitted</code> once its provider answers <code>Status</code>, and to <code>failed</code> when it does not or is no longer configured.</p><p>To <code>cancelled</code> only from <code>DELETE /v1/execution/{id}</code> when this server is not running the execution&rsquo;s dispatch: the provider is asked to cancel it (30s timeout), then the record is written <code>cancelled</code>.</p>'
    },
    SUBMITTED: {
      title: 'submitted',
      badge: 'process', badgeText: 'Submit returned',
      body: '<p>Written by <code>tracked.submitted</code> as soon as <code>Submit</code> returns, with the provider&rsquo;s own ID. The state <code>Submit</code> reported is written right after: <code>accepted</code> or <code>running</code>, or <code>succeeded</code> or <code>failed</code> with the result for a synchronous provider such as AWS Lambda, which skips both middle states.</p><p>A resumed dispatch also writes it for a <code>pending</code> record whose provider answers <code>Status</code>, since the submission may have gone out before the previous owner died.</p>'
    },
    ACCEPTED: {
      title: 'accepted',
      badge: 'process', badgeText: 'queued',
      body: '<p>The provider queued the work but has not started it; a Cloud Run Job and a Code Engine job run report it from <code>Submit</code>. Optional: a provider may go straight to <code>running</code> or a terminal state.</p><p>Written on change by the status poll, 2s doubling to 15s. Capacity is consumed from <code>running</code> on, so work a provider accepted and dropped before starting cost nothing.</p>'
    },
    RUNNING: {
      title: 'running',
      badge: 'process', badgeText: 'started',
      body: '<p>The work started. Reported by <code>Submit</code>, as a pool node can, or by a later <code>Status</code> poll, and written on change only, so a job that runs for minutes is recorded once. Reaching <code>running</code> sets <code>StartedAt</code>.</p><p>There is no timed-out state: the provider enforces the task&rsquo;s timeout and reports <code>failed</code>.</p>'
    },
    LOST: {
      title: 'lost',
      badge: 'filter', badgeText: 'not terminal',
      body: '<p>Written when a <code>Status</code> call fails after submission, ending the attempt with <code>Failure</code> set to the error&rsquo;s class, or on change when the provider&rsquo;s <code>Status</code> reports it, as a pool does for a node gone less than 5 minutes, and polling continues. The work may still be running, so it is not cancelled, and its reservation stands until the reaper resolves it.</p><p>A poll that later sees <code>running</code> is refused by the state machine, so the record stays <code>lost</code> until it reaches a terminal state.</p><p>The provider release loop reconciles it once it has gone 10 minutes unchanged, then every 5 minutes. A terminal state, <code>cancelled</code> included, whose <code>Result</code> answers is written with the result. An unknown execution, or a provider no longer configured, is <code>failed</code> at once. Anything else stays <code>lost</code>, and is <code>failed</code> after 24 hours (<code>LostGracePeriod</code>). <code>Failure</code> keeps the class that made it <code>lost</code>.</p><p><a href="../background-services/">Background services diagram &rarr;</a><br><a href="../../docs/dispatch/#lost-executions">Dispatch: lost executions &rarr;</a></p>'
    },
    SUCCEEDED: {
      title: 'succeeded',
      badge: 'success', badgeText: 'terminal',
      body: '<p>The provider reported success. Written by <code>tracked.finish</code> with the result: from <code>Submit</code> for a synchronous provider, otherwise once a poll sees the terminal state and <code>Result</code> is called. The result keeps the last 64 KiB of output.</p><p>Written even when <code>Result</code> failed, then without a result, and the attempt ends with the <code>Result</code> error. Only <code>succeeded</code> with a result passes the task.</p>'
    },
    FAILED: {
      title: 'failed',
      badge: 'reject', badgeText: 'terminal',
      body: '<p><b>The provider reported failure</b>, written with the result: a non-zero exit, a function error, a timeout. This is the task&rsquo;s answer; the job ends and nothing is retried.</p><p><b>Before submission</b>, written from <code>pending</code> with <code>Failure</code> set: <code>Submit</code> failed, or returned a submission that breaks its rules (<code>internal</code>). A resumed dispatch does the same when the provider is no longer configured or <code>Status</code> fails.</p><p><b>Reconciliation</b> of a <code>lost</code> execution: the provider has no record of it, the provider is no longer configured, or it stayed <code>lost</code> for 24 hours.</p>'
    },
    CANCELLED: {
      title: 'cancelled',
      badge: 'background', badgeText: 'terminal, no verdict',
      body: '<p><b>By Vagabond</b>: <code>DELETE /v1/execution/{id}</code> cancels the whole dispatch when this server is running it; the execution being watched is cancelled at the provider (30s timeout, error ignored) and written <code>cancelled</code>. When this server is not running the dispatch, the provider is asked to cancel the execution, then the record is written <code>cancelled</code> unless another writer got there first.</p><p><b>By the provider</b>: a poll sees <code>cancelled</code>, written with the result. The provider gave no verdict, so it is an <code>infrastructure</code> failure and the task may be rerouted.</p>'
    },
    REROUTE: {
      title: 'Next Attempt',
      badge: 'storage', badgeText: 'new record',
      body: '<p>Not a state. An attempt that ended on an <code>infrastructure</code> failure (<code>Submit</code> failed, <code>Status</code> failed, the provider cancelled it, or <code>Result</code> failed) sends the task to the next ranked provider within its retry budget, as a new record that starts at <code>pending</code> with a fresh execution ID and <code>PreviousID</code> pointing at this one.</p><p>An <code>internal</code> failure is not rerouted, and each provider is tried at most once per task.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    DRUN: {
      title: 'running (dispatch)',
      badge: 'entry', badgeText: 'dispatch state',
      body: '<p>Written by <code>Begin</code> before anything is reserved, owned by <code>server:&lt;hostname&gt;:&lt;pid&gt;</code> with a 60s lease. The owner renews it every 20s on its own context; any failure but a stale lease is ignored, so two renewals can fail before the lease lapses.</p><p>The ending is written by <code>FinishDispatch</code>, which applies only while the dispatch is <code>running</code> and held by the caller. An owner whose renewal found the lease taken starts no further task and writes nothing. While the store is unreachable the ending is not written, and the dispatch stays <code>running</code> until a claim takes it.</p><p><a href="../../docs/dispatch/#leases">Dispatch: leases &rarr;</a></p>'
    },
    CLAIM: {
      title: 'Claimed and Resumed',
      badge: 'storage', badgeText: 'takeover',
      body: '<p>Not a state: the dispatch stays <code>running</code> under a new owner. At startup and every 30s, each server claims every <code>running</code> dispatch whose lease expired, except those it is running itself, in one statement, and leases each for 60s.</p><p>The resume follows each execution that is not terminal. A provider no longer configured makes it <code>failed</code> if <code>pending</code>, else <code>lost</code>, as <code>internal</code>; a failed <code>Status</code> does the same as <code>infrastructure</code>. Otherwise it is moved from <code>pending</code> to <code>submitted</code> if needed, polled to its end, released and settled. Tasks not yet reached are not run: the job file is not stored.</p><p>It then ends by its last execution: <code>succeeded</code> when that passed and every task has a record, <code>failed</code> when it failed with a result, otherwise <code>unanswered</code>. If the executions cannot be read, it stays <code>running</code> for the next claim.</p><p><a href="../background-services/">Background services diagram &rarr;</a><br><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    DSUCC: {
      title: 'succeeded (dispatch)',
      badge: 'success', badgeText: 'dispatch state',
      body: '<p>Every task ran and ended <code>succeeded</code> with a result. Written by <code>Finish</code> when <code>Run</code> returns without an error and every task passed, or by a resume whose last execution passed with every declared task recorded.</p>'
    },
    DFAIL: {
      title: 'failed (dispatch)',
      badge: 'reject', badgeText: 'dispatch state',
      body: '<p>A task ran and the provider reported <code>failed</code> with a result, with or without an exit code. The job gave an answer; the tasks after it were not run. Written by <code>Finish</code>, or by a resume whose last execution failed with a result.</p>'
    },
    DUNANS: {
      title: 'unanswered (dispatch)',
      badge: 'background', badgeText: 'dispatch state',
      body: '<p>No verdict. <code>Finish</code> writes it when <code>Run</code> returned an error: no candidates, every attempt failed, an <code>internal</code> failure, a cancellation, or a store write refused. <code>Error</code> carries it, prefixed with the task: <code>task "build": every attempt failed: &lt;last failure&gt;</code>.</p><p>A resume writes it with <code>interrupted before any task was submitted</code>, <code>interrupted after &lt;n&gt; of &lt;m&gt; tasks</code>, or <code>interrupted; execution &lt;id&gt; ended &lt;state&gt;</code>. Reconciling a <code>lost</code> execution later does not change it.</p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Initial state |
| <span class="dg-swatch dg-process"></span> | In progress |
| <span class="dg-swatch dg-filter"></span> | Lost: unresolved, not terminal |
| <span class="dg-swatch dg-success"></span> | Succeeded |
| <span class="dg-swatch dg-reject"></span> | Failed |
| <span class="dg-swatch dg-background"></span> | No verdict: cancelled, unanswered |
| <span class="dg-swatch dg-storage"></span> | Not a state: a new record or a takeover |
