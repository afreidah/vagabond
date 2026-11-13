---
title: "Execution States"
linkTitle: "Execution States"
description: "Interactive state machine of an execution record and its dispatch: every legal transition, what causes it, and which component writes it."
weight: 7
---

The execution state machine as `execution.State` allows it, and the four states of the dispatch record above it. The machine is acyclic: a retry is a new execution record starting at `pending`, never a transition back, and a transition the table refuses is not written. After `prepare` creates a record, every write to it is its execution runner's, checked against the dispatch's lease. **Hover over any state** for what moves a record into it and which component writes it.

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
    '    PENDING --> CANCELLED',
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
    '    LOST -->|terminal Status| SUCCEEDED',
    '    LOST -->|or 24h| FAILED',
    '    LOST --> CANCELLED',
    '    FAILED -.->|infrastructure| REROUTE',
    '    LOST -.->|infrastructure| REROUTE',
    '    CANCELLED -.->|by the provider| REROUTE',
    '    REROUTE[Next attempt<br>new record at pending]:::storage',
    '',
    '    subgraph DISP [Dispatch record]',
    '        DRUN([running]):::entry',
    '        CLAIM[Claimed and resumed<br>lease lapsed, not done]:::storage',
    '        DSUCC([succeeded]):::success',
    '        DFAIL([failed]):::reject',
    '        DUNANS([unanswered]):::background',
    '        DONE[Done<br>every execution released]:::storage',
    '    end',
    '    DRUN -->|every task succeeded| DSUCC',
    '    DRUN -->|a task failed| DFAIL',
    '    DRUN -->|a task returned an error| DUNANS',
    '    DRUN -->|lease lapsed| CLAIM',
    '    CLAIM -->|by its last execution| DSUCC',
    '    CLAIM --> DFAIL',
    '    CLAIM --> DUNANS',
    '    DSUCC --> DONE',
    '    DFAIL --> DONE',
    '    DUNANS --> DONE',
    '    REROUTE ~~~ DRUN',
  ].join('\n'),

  info: {
    PENDING: {
      title: 'pending',
      badge: 'entry', badgeText: 'initial state',
      body: '<p>Written by <code>prepare</code> when the record is created: after the quota reservation, before <code>Submit</code>, under the dispatch&rsquo;s lease. A failure stops the task: the execution ID is the submission&rsquo;s idempotency key, so it has to exist durably first.</p><p>Leaves when the runner submits: to the state <code>Submit</code> reported when it returns, by way of <code>submitted</code> in the same write, a terminal one written later with the output, and to <code>failed</code> with the failure class when it fails. The retry backoff comes before the reservation and the record, so a dispatch cancelled during it leaves neither. A runner restored after a claim has no task to submit: it asks <code>Status</code>, and moves the record to <code>submitted</code> when the provider answers, to <code>failed</code> when it does not or is no longer configured.</p><p>The move to <code>cancelled</code> is legal, but no component writes it: a cancel acts on executions their runners are watching.</p>'
    },
    SUBMITTED: {
      title: 'submitted',
      badge: 'process', badgeText: 'Submit returned',
      body: '<p>Reached when <code>Submit</code> returns, with the provider&rsquo;s own ID. The runner moves on to the state <code>Submit</code> reported in the same write: <code>accepted</code> or <code>running</code>, or <code>succeeded</code> or <code>failed</code> for a synchronous provider such as AWS Lambda, which skips both middle states. The stored record therefore holds <code>submitted</code> only in the case below.</p><p>A restored runner writes it for a <code>pending</code> record whose provider answers <code>Status</code>, since the submission may have gone out before the previous owner died, then polls as usual.</p>'
    },
    ACCEPTED: {
      title: 'accepted',
      badge: 'process', badgeText: 'queued',
      body: '<p>The provider queued the work but has not started it; a Cloud Run Job and a Code Engine job run report it from <code>Submit</code>. Optional: a provider may go straight to <code>running</code> or a terminal state.</p><p>Written on change by the runner&rsquo;s status poll, 2s doubling to 15s. Capacity is consumed from <code>running</code> on, so work a provider accepted and dropped before starting cost nothing.</p>'
    },
    RUNNING: {
      title: 'running',
      badge: 'process', badgeText: 'started',
      body: '<p>The work started. Reported by <code>Submit</code>, as a pool node can, or by a later <code>Status</code> poll, and written on change only, so a job that runs for minutes is recorded once. Reaching <code>running</code> sets <code>StartedAt</code>.</p><p>There is no timed-out state: the provider enforces the task&rsquo;s timeout and reports <code>failed</code>.</p>'
    },
    LOST: {
      title: 'lost',
      badge: 'filter', badgeText: 'not terminal',
      body: '<p>Written by the runner when a <code>Status</code> call fails after submission, with <code>Failure</code> set to the error&rsquo;s class, and the attempt is answered at once so the task can reroute. Also written on change when the provider&rsquo;s <code>Status</code> reports it, as a pool does for a node gone less than 5 minutes; that does not answer the attempt, the first <code>Status</code> failure after it does. The work may still be running, so it is not cancelled, and its reservation stands.</p><p>The runner keeps polling. A poll that sees a state short of terminal leaves the record <code>lost</code>; a terminal one clears <code>Failure</code> and is collected with its result. One the provider no longer knows, an unknown not classified <code>infrastructure</code>, is <code>failed</code> at once and charged for the time it was seen running. One whose <code>Status</code> still fails after 24 hours lost (<code>LostGracePeriod</code>) is <code>failed</code> and charged as reserved.</p><p>Once it ends, its output is captured, its quota settled and the execution released, and its dispatch is done only after that.</p><p><a href="../background-services/">Background services diagram &rarr;</a><br><a href="../../docs/dispatch/#lost-executions">Dispatch: lost executions &rarr;</a></p>'
    },
    SUCCEEDED: {
      title: 'succeeded',
      badge: 'success', badgeText: 'terminal',
      body: '<p>The provider reported success. Written by the runner as <code>Submit</code> returns for a synchronous provider, otherwise once a poll sees it. The runner then reads the result, captures the output into <code>execution_logs</code>, and writes both in one write, so the output is there when the result is seen.</p><p>When <code>Result</code> fails the record keeps no result, and the attempt ends with the <code>Result</code> error. Only <code>succeeded</code> with a result passes the task.</p>'
    },
    FAILED: {
      title: 'failed',
      badge: 'reject', badgeText: 'terminal',
      body: '<p><b>The provider reported failure</b>, written with the result: a non-zero exit, a function error, a timeout. This is the task&rsquo;s answer; the job ends and nothing is retried.</p><p><b>Before submission</b>, written from <code>pending</code> with <code>Failure</code> set: <code>Submit</code> failed, or returned a submission that breaks its rules (<code>internal</code>). A restored runner does the same for a <code>pending</code> record whose provider does not answer <code>Status</code>, and for any record not yet terminal whose provider is no longer configured (<code>internal</code>).</p><p><b>Unknown to the provider</b>: a <code>Status</code> saying the provider has no record of a watched execution, unless classified <code>infrastructure</code>, fails it at once, charged for the time it was seen running.</p><p><b>Lost for 24 hours</b>: a <code>lost</code> execution whose <code>Status</code> still fails is recorded <code>failed</code> by its runner and charged as reserved.</p>'
    },
    CANCELLED: {
      title: 'cancelled',
      badge: 'background', badgeText: 'terminal, no verdict',
      body: '<p><b>By Vagabond</b>: <code>DELETE /v1/execution/{id}</code> cancels the whole dispatch. The server running it stops its runners at once. Any other server records a cancel request on the dispatch and returns the execution as it stands; the owner acts on it at its next lease renewal (within 20s), and a server that claims the dispatch acts on it right after the claim. The runner then confirms the lease with a write, asks the provider to <code>Cancel</code> (30s timeout, error ignored), and writes <code>cancelled</code> with the start and end times one <code>Status</code> call gives, so the run is charged for what it ran. Its output is still captured and the execution released.</p><p><b>By the provider</b>: a poll sees <code>cancelled</code>, written with the result. The provider gave no verdict, so it is an <code>infrastructure</code> failure and the task may be rerouted.</p>'
    },
    REROUTE: {
      title: 'Next Attempt',
      badge: 'storage', badgeText: 'new record',
      body: '<p>Not a state. An attempt answered with an <code>infrastructure</code> failure (<code>Submit</code> failed, <code>Status</code> failed and it went <code>lost</code>, the provider cancelled it, or <code>Result</code> failed) sends the task to the next ranked provider within its retry budget, as a new record that starts at <code>pending</code> with a fresh execution ID and <code>PreviousID</code> pointing at this one. The runner of the attempt left behind carries on to its end.</p><p>An <code>internal</code> failure is not rerouted, and each provider is tried at most once per task.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    },
    DRUN: {
      title: 'running (dispatch)',
      badge: 'entry', badgeText: 'dispatch state',
      body: '<p>Written by <code>Begin</code> before anything is reserved, owned by <code>server:&lt;hostname&gt;:&lt;pid&gt;</code> under epoch 1 with a 60s lease. The owner renews it every 20s on its own context until the dispatch is done; any failure but a refused renewal is ignored, so two renewals can fail before the lease lapses.</p><p>The ending is written by <code>FinishDispatch</code>, which applies only while the dispatch is <code>running</code> and held under the caller&rsquo;s owner and epoch. An owner whose lease was taken starts no further task and writes nothing. While the store is unreachable the write is retried; if the lease lapses meanwhile, a claim takes the dispatch and the old owner&rsquo;s writes are refused.</p><p><a href="../../docs/dispatch/#leases">Dispatch: leases &rarr;</a></p>'
    },
    CLAIM: {
      title: 'Claimed and Resumed',
      badge: 'storage', badgeText: 'takeover',
      body: '<p>Not a state: the dispatch keeps its state under a new owner. At startup and every 30s, each server claims every dispatch not yet done whose lease expired, except those it is running itself, in one statement, under the next epoch, and leases each for 60s. That takes running dispatches and ended ones whose executions are still settling or releasing; the previous owner&rsquo;s writes are refused from then on.</p><p>A cancel requested before the claim is acted on at once. A runner is restored for every execution not yet terminal, collected and released, and starts at the first step its record says is left. A <code>pending</code> record is not submitted again: the provider is asked about it, an answer moves it to <code>submitted</code>, and no answer makes it <code>failed</code>, charged nothing when the provider never heard of it. A provider no longer configured makes the execution <code>failed</code> as <code>internal</code>. Tasks not yet reached are not run: the job file is not stored.</p><p>A running dispatch then ends by its last execution: <code>succeeded</code> when that passed and every task has a record, <code>failed</code> when it failed with a result, otherwise <code>unanswered</code>. An ended one keeps its ending. Either is done once every runner has finished. If the executions cannot be read, the dispatch stays open for the next claim.</p><p><a href="../background-services/">Background services diagram &rarr;</a><br><a href="../task-path/">Task path diagram &rarr;</a></p>'
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
      body: '<p>No verdict. <code>Finish</code> writes it when <code>Run</code> returned an error: no candidates, every attempt failed, an <code>internal</code> failure, a cancellation, or a store write refused. <code>Error</code> carries it, prefixed with the task: <code>task "build": every attempt failed: &lt;last failure&gt;</code>.</p><p>A resume writes it with <code>interrupted before any task was submitted</code>, <code>interrupted after &lt;n&gt; of &lt;m&gt; tasks</code>, or <code>interrupted; execution &lt;id&gt; ended &lt;state&gt;</code>. A <code>lost</code> execution that ends later does not change it.</p>'
    },
    DONE: {
      title: 'Done (dispatch)',
      badge: 'storage', badgeText: 'done_at',
      body: '<p>Not a state: <code>done_at</code> on the dispatch record. Once the ending is written, the owner waits for every execution runner of the dispatch to finish through release, then <code>CloseDispatch</code> sets it and lease renewal stops. A claim takes only dispatches without it.</p><p>A dispatch with an execution still <code>lost</code> is not done until that execution ends, its provider no longer knows it, or it fails 24 hours later.</p>'
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
| <span class="dg-swatch dg-storage"></span> | Not a state: a new record, a takeover, or a dispatch done |
