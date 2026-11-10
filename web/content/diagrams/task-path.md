---
title: "Task Path"
linkTitle: "Task Path"
description: "Interactive diagram of one run of a job, from the request through admission, ranking, quota reservation, submission, watching, release, settlement and rerouting."
weight: 2
---

One run of a job, from the request to the dispatch's ending. Tasks run one at a time in declaration order, and each is admitted, ranked, reserved, submitted and watched on its own; an `infrastructure` failure moves the task to the next ranked provider within its retry budget. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  source: [
    'flowchart TD',
    '    FILE([Job file<br>run or dispatch]):::entry --> LOAD[Load the job<br>jobs]:::filter',
    '    LOAD -->|refused| REFUSED[Request refused<br>nothing recorded]:::reject',
    '    LOAD --> PARSE[Parse and validate<br>jobspec]:::filter',
    '    PARSE -->|diagnostics| REFUSED',
    '    PARSE -->|valid| BEGIN[Begin<br>dispatch record + lease]:::storage',
    '    BEGIN -->|store error| REFUSED',
    '    BEGIN -->|dispatch ID returned| TASK{Next task}:::decision',
    '',
    '    TASK -->|lease taken| LEASELOST[ErrLeaseLost<br>no ending written]:::reject',
    '    TASK -->|in declaration order| ADMIT[Admission<br>13 checks]:::filter',
    '    ADMIT -->|no candidates| NOCAND[ErrNoCandidates]:::reject',
    '    ADMIT -->|candidates| RANK[Ranking<br>tier and score]:::process',
    '    RANK --> CAND{Next ranked<br>candidate?}:::decision',
    '',
    '    CAND -->|within budget| RESERVE[Reserve quota<br>ledger]:::storage',
    '    CAND -->|none submitted| NOCAND',
    '    CAND -->|budget or ranking spent| EXHAUSTED[ErrExhausted]:::reject',
    '    RESERVE -->|refused, no budget spent| NEXTCAND',
    '    RESERVE -->|reserved| PENDING[Record execution<br>pending]:::storage',
    '    PENDING -->|first submission| SUBMIT',
    '    PENDING -->|later submission| BACKOFF[Retry backoff<br>5s doubling to 30s]:::process',
    '    BACKOFF --> SUBMIT[Submit<br>fresh execution ID]:::process',
    '',
    '    SUBMIT -->|Submit fails| CLASSIFY',
    '    SUBMIT --> SYNC{Synchronous?}:::decision',
    '    SYNC -->|no| WATCH[Watch<br>poll 2s to 15s]:::process',
    '    SYNC -->|yes| RELEASE',
    '    WATCH -->|Status fails, lost| CLASSIFY',
    '    WATCH -->|dispatch cancelled| STOP',
    '    WATCH -->|terminal| RESULT[Fetch result]:::process',
    '    RESULT -->|Result fails| CLASSIFY',
    '    RESULT --> RELEASE[Release<br>leftovers]:::background',
    '    RELEASE --> SETTLE[Settle quota<br>ledger]:::storage',
    '    SETTLE --> VERDICT{Provider<br>verdict}:::decision',
    '    VERDICT -->|succeeded, last task| DSUCC([Dispatch<br>succeeded]):::success',
    '    VERDICT -->|succeeded, tasks remain| NEXTTASK([Next task<br>back to the top]):::process',
    '    VERDICT -->|failed| DFAIL([Dispatch<br>failed]):::reject',
    '    VERDICT -->|cancelled by provider| CLASSIFY',
    '',
    '    CLASSIFY{Failure<br>class}:::decision',
    '    CLASSIFY -->|infrastructure, reroute| NEXTCAND([Next ranked candidate<br>back to the ranking]):::process',
    '    CLASSIFY -->|internal or unclassified| STOP[Task stops<br>error returned]:::reject',
    '    NOCAND --> DUNANS([Dispatch<br>unanswered]):::reject',
    '    EXHAUSTED --> DUNANS',
    '    STOP --> DUNANS',
  ].join('\n'),

  info: {
    FILE: {
      title: 'Job File',
      badge: 'entry', badgeText: 'entry point',
      body: '<p>A run starts from <code>POST /v1/jobs/run</code>, which carries a job file to run without registering it, or <code>POST /v1/job/{name}/dispatch</code>, which runs the current version of a registered job with the metadata the request supplies. <code>vagabond job run</code> and <code>vagabond job dispatch</code> call them.</p><p>The CLI parses and validates a job file locally before sending it, and the server does it again.</p><p><a href="../../docs/api/#dispatches">API: dispatches &rarr;</a></p>'
    },
    LOAD: {
      title: 'Load the Job',
      badge: 'filter', badgeText: 'jobs',
      body: '<p><code>jobs.Load</code> parses and validates the source with the supplied metadata. A dispatch first reads the job&rsquo;s current version (<code>jobs.Current</code>), refusing a stopped job, then checks the metadata against its <code>parameterized</code> block (<code>CheckDispatch</code>): none for a job that is not parameterized, no key declared in neither <code>meta_required</code> nor <code>meta_optional</code>, every required key present.</p><p>The namespace is the job&rsquo;s own, else the one requested, else the default. A conflict between the two, or a namespace the configuration does not declare, is refused.</p>'
    },
    PARSE: {
      title: 'Parse and Validate',
      badge: 'filter', badgeText: 'jobspec',
      body: '<p><code>jobspec.Parse</code> checks <code>meta_required</code> before decoding, then decodes the HCL with <code>${meta.*}</code> substituted. <code>jobspec.Validate</code> checks what decoding cannot: one job per file, at least one task, unique task names, the closed driver and type vocabularies, a non-negative <code>retry.attempts</code>, a backoff <code>max</code> not below <code>initial</code>.</p><p>Every problem is collected as a diagnostic rather than stopping at the first.</p><p><a href="../../docs/job-specification/#diagnostics">Job specification: diagnostics &rarr;</a></p>'
    },
    REFUSED: {
      title: 'Request Refused',
      badge: 'reject', badgeText: 'rejection',
      body: '<p>The request fails before the run starts, so nothing is reserved or submitted:</p><p><b>400</b>: diagnostics (<code>the job is not valid</code>, each against its line), refused metadata, or a namespace conflict<br><b>404</b>: no such registered job<br><b>409</b>: the job is stopped; register it again to dispatch it<br><b>503</b>: the store is unreachable, for example when the dispatch record is written (500 for any other store error)</p>'
    },
    BEGIN: {
      title: 'Begin',
      badge: 'storage', badgeText: 'dispatch record',
      body: '<p><code>Dispatcher.Begin</code> mints a UUIDv7 dispatch ID and writes the dispatch record as <code>running</code>, owned by <code>server:&lt;hostname&gt;:&lt;pid&gt;</code> and leased for 60s. It is written before anything is reserved, so a run that never reaches a provider still has a record saying why.</p><p>A goroutine renews the lease every 20s. The request returns the dispatch ID here; the tasks run in a background goroutine on a context detached from the request.</p><p class="dg-metric">Log: dispatch started</p>'
    },
    TASK: {
      title: 'Next Task',
      badge: 'decision', badgeText: 'job order',
      body: '<p><code>Dispatcher.Run</code> takes the tasks one at a time, in declaration order. There is no dependency graph: the first task that fails or gets no answer ends the job, and later tasks are not run.</p><p>Before each task it checks that the lease is still held.</p><p><a href="../../docs/dispatch/#job-execution-order">Dispatch: job execution order &rarr;</a></p>'
    },
    LEASELOST: {
      title: 'ErrLeaseLost',
      badge: 'reject', badgeText: 'taken over',
      body: '<p>A renewal found the dispatch ended or held by another owner: another server claimed it after the lease lapsed. This server finishes the task in flight, starts no further task (<code>dispatch was taken over by another process</code>) and writes no ending.</p><p>The new owner resumes the dispatch and records how it ended.</p><p><a href="../execution-states/">Execution states diagram &rarr;</a></p>'
    },
    ADMIT: {
      title: 'Admission',
      badge: 'filter', badgeText: 'scheduler',
      body: '<p><code>RunTask</code> builds the request, evaluating the driver config&rsquo;s image against the metadata, re-reads quota usage from the store, then calls <code>scheduler.Admit</code>: 13 checks in 4 tiers (policy, availability, mismatch, capacity) against every configured provider. A failed usage read keeps the last snapshot; the reservation is what enforces the limit.</p><p>It is the same <code>Admit</code> and <code>Rank</code> that <code>job plan</code> calls, so a plan and a run cannot drift apart. Each task is admitted when it runs, against what earlier tasks charged.</p><p><a href="../admission/">Admission and ranking diagram &rarr;</a></p>'
    },
    NOCAND: {
      title: 'ErrNoCandidates',
      badge: 'reject', badgeText: 'no candidates',
      body: '<p><code>no provider can run this task</code>. Either admission admitted nothing, or every admitted provider refused the reservation, so nothing was submitted.</p><p>The task outcome carries the admission rejections, so a task with nowhere to run explains itself the way a plan does.</p>'
    },
    RANK: {
      title: 'Ranking',
      badge: 'process', badgeText: 'scheduler',
      body: '<p><code>scheduler.Rank</code> scores each candidate with <code>headroom</code>, any affinities and, under <code>weighted</code> tiers, the tier, as their mean. Under <code>strict</code> tiers candidates are ordered by tier, then score; under <code>weighted</code>, by score alone. Ties keep provider-name order.</p><p>The full ordering is kept. Dispatch works down it, moving on when a reservation is refused or a provider fails to answer.</p><p><a href="../admission/">Admission and ranking diagram &rarr;</a></p>'
    },
    CAND: {
      title: 'Next Ranked Candidate?',
      badge: 'decision', badgeText: 'retry budget',
      body: '<p><code>attempt</code> walks the ranking while the submissions made are below the budget: <code>retry.attempts</code> when <code>reroute = true</code>, otherwise 1. No <code>retry</code> block, or <code>attempts</code> of 0, is 1. Each candidate is tried at most once; there is no retry on the same provider.</p><p>A ranked provider with no plugin in the registry stops the task with <code>ErrNoProvider</code>. When the ranking or the budget runs out, the error is <code>ErrNoCandidates</code> if nothing was submitted and <code>ErrExhausted</code> otherwise.</p><p><a href="../../docs/dispatch/#retry-budget">Dispatch: retry budget &rarr;</a></p>'
    },
    RESERVE: {
      title: 'Reserve Quota',
      badge: 'storage', badgeText: 'ledger',
      body: '<p><code>ledger.Reserve</code> charges the task&rsquo;s declared worst case (CPU, memory and timeout) under a freshly minted execution ID, against every pool the provider meters and the namespace&rsquo;s share of them. A provider with no pools is not reserved. A job with <code>max_cost_usd</code> above 0 is never refused.</p><p>A <code>*ledger.Refusal</code> passes the provider over without spending a submission: nothing was sent, so there is no outage to back off from. Any other error stops the task (<code>reserving quota on &lt;provider&gt;</code>).</p><p><a href="../quotas/">Quota accounting diagram &rarr;</a></p>'
    },
    PENDING: {
      title: 'Record Execution Pending',
      badge: 'storage', badgeText: 'execution record',
      body: '<p><code>prepare</code> writes the execution record in <code>pending</code> after the reservation and before <code>Submit</code>, with its task, provider, <code>Attempt</code> (counting submissions only) and <code>PreviousID</code>, the task&rsquo;s attempt before it. The execution ID is the submission&rsquo;s idempotency key, so it exists durably first.</p><p>This is the one write that must land: a failure stops the task (<code>recording execution &lt;id&gt;</code>). Every later write is best effort, on its own context with a 10s timeout.</p>'
    },
    BACKOFF: {
      title: 'Retry Backoff',
      badge: 'process', badgeText: 'wait',
      body: '<p>Every submission after the first waits, with its reservation held and its record already <code>pending</code>: <code>backoff.initial</code> (default 5s), doubling to <code>backoff.max</code> (default 30s). With the defaults the second submission waits 5s, the third 10s, the fourth 20s, then 30s each.</p><p>A duration that does not parse at dispatch falls back to the default. A dispatch cancelled during the wait stops here; the record stays <code>pending</code> and its reservation is left to the reaper.</p>'
    },
    SUBMIT: {
      title: 'Submit',
      badge: 'process', badgeText: 'providers/*',
      body: '<p>Hands the task to the plugin under the execution ID: <code>SubmitTo</code> with the members admission passed, for a provider made of members such as a <code>pool</code> of agent nodes, otherwise <code>Submit</code>. A call rejected as unauthorized has its credential resolved again and is made once more.</p><p>Success records <code>submitted</code> and the provider ID, then the state <code>Submit</code> reported. A submission may report only <code>accepted</code>, <code>running</code>, <code>succeeded</code> or <code>failed</code>, and a terminal state only with a result; one that breaks this is an <code>internal</code> failure. A failed <code>Submit</code> records the attempt <code>failed</code> with its failure class.</p><p><a href="../agents/">Agents and pools diagram &rarr;</a></p>'
    },
    SYNC: {
      title: 'Synchronous?',
      badge: 'decision', badgeText: 'synchronous',
      body: '<p>A function or worker finishes inside <code>Submit</code> and returns its result with it, as an AWS Lambda invocation does. There is nothing to poll: the record goes from <code>submitted</code> straight to the terminal state with the result, and the execution is released at once.</p><p>Everything else, such as a Cloud Run Job or a pool workload, reports <code>accepted</code> or <code>running</code> and is watched.</p>'
    },
    WATCH: {
      title: 'Watch',
      badge: 'process', badgeText: 'status poll',
      body: '<p><code>watch</code> sleeps, then calls <code>Status</code>: 2s doubling to a 15s ceiling, so polls land 2s, 6s, 14s and 29s after submission, then every 15s. A state is written only when it changes. Dispatch sets no deadline of its own; the provider enforces the task&rsquo;s timeout and reports <code>failed</code>.</p><p>A failed <code>Status</code> call records the execution <code>lost</code> with its failure class. The work may still be running, so it is not cancelled and its reservation stands. A cancelled dispatch asks the provider to <code>Cancel</code> (30s timeout, error ignored) and records <code>cancelled</code>.</p><p>Dispatch never streams output; a client follows it from the provider through the server.</p><p><a href="../execution-states/">Execution states diagram &rarr;</a><br><a href="../live-output/">Live output diagram &rarr;</a></p>'
    },
    RESULT: {
      title: 'Fetch Result',
      badge: 'process', badgeText: 'result',
      body: '<p>Once <code>Status</code> reports a terminal state, <code>Result</code> fetches the exit code, duration, any billed amount and the output, and the terminal state is written with it. The record keeps the last 64 KiB of output, with <code>LogsTruncated</code> set when anything was cut.</p><p>When <code>Result</code> fails, the terminal state is still written, without a result. The attempt ends with that error; nothing is released or settled, and the reservation is left to the reaper.</p>'
    },
    RELEASE: {
      title: 'Release',
      badge: 'background', badgeText: 'cleanup',
      body: '<p>For a provider implementing <code>plugin.Releaser</code>, <code>Release</code> deletes what the execution left behind: the Cloud Run Job resource, the Code Engine job run and its pod, or the pool workload&rsquo;s task, container, snapshot and log file on its node. Called after <code>Result</code>, because releasing may delete what the result reads, and as soon as <code>Submit</code> returns for a synchronous submission.</p><p>Own context, 30s timeout. Success, or an unknown execution, sets <code>released_at</code>; a failed release does not fail the execution. Endings that skip this step, and releases that failed, are left to the provider release loop.</p><p><a href="../background-services/">Background services diagram &rarr;</a></p>'
    },
    SETTLE: {
      title: 'Settle Quota',
      badge: 'storage', badgeText: 'ledger',
      body: '<p><code>charge</code> replaces the reservation with what the result says the run used: the platform&rsquo;s billed amount when the result carries one (AWS Lambda), otherwise the declared CPU and memory over the measured duration. It runs when the attempt returns, after the release.</p><p>Only a result settles. An attempt without one keeps its reservation at the declared worst case until the reaper resolves it, once it is over an hour old; so does a settle write that failed.</p><p><a href="../quotas/">Quota accounting diagram &rarr;</a></p>'
    },
    VERDICT: {
      title: 'Provider Verdict',
      badge: 'decision', badgeText: 'answer',
      body: '<p>The provider&rsquo;s terminal state is the verdict, not the exit code. <code>succeeded</code> with a result passes the task, and the job moves to the next one. <code>failed</code> with a result, such as a non-zero exit or a function error, is the task&rsquo;s answer and ends the job: a workload failure would repeat on any provider, so it is never retried.</p><p>A provider that reports <code>cancelled</code> gave no verdict about the work, so it is an <code>infrastructure</code> failure even though a result came back.</p>'
    },
    CLASSIFY: {
      title: 'Failure Class',
      badge: 'decision', badgeText: 'reroute',
      body: '<p><code>plugin.Reroutable</code> reads the error&rsquo;s class. <code>infrastructure</code> is the provider failing to answer (a 5xx, 408 or 429, no pool node with room, a node gone), and the task moves to the next ranked candidate within the budget.</p><p><code>internal</code> is Vagabond&rsquo;s own fault (another 4xx, an incoherent submission) and would fail the same way on every provider, so the task stops. An error that is not a <code>*plugin.Error</code> is treated the same way.</p><p><a href="../../docs/dispatch/#failure-classes-and-rerouting">Dispatch: failure classes and rerouting &rarr;</a></p>'
    },
    NEXTCAND: {
      title: 'Next Ranked Candidate',
      badge: 'process', badgeText: 'loop',
      body: '<p>Back to <i>Next Ranked Candidate?</i> with the next provider in the ranking. A refused reservation sent nothing, so it spends no submission. An <code>infrastructure</code> failure spent one, and the next submission waits out the retry backoff.</p><p>The next attempt gets a fresh execution ID, with <code>PreviousID</code> pointing at this one; reusing the ID would ask the provider to resume the run that just failed.</p>'
    },
    NEXTTASK: {
      title: 'Next Task',
      badge: 'process', badgeText: 'loop',
      body: '<p>Back to <i>Next Task</i>: the lease is checked, and the next task in declaration order is admitted, ranked and reserved from the start, against usage re-read from the store.</p>'
    },
    STOP: {
      title: 'Task Stops',
      badge: 'reject', badgeText: 'no reroute',
      body: '<p>The task ends with an error and no later task runs. Reached on an <code>internal</code> or unclassified failure, a ranked provider not in the registry (<code>ErrNoProvider</code>), a reservation or <code>pending</code> write the store refused, a driver config that fails to evaluate, or a dispatch cancelled while it waited or watched.</p>'
    },
    EXHAUSTED: {
      title: 'ErrExhausted',
      badge: 'reject', badgeText: 'no answer',
      body: '<p><code>every attempt failed</code>: at least one submission was made, each one failed with <code>infrastructure</code>, and the budget or the ranking ran out. The error carries the last failure.</p>'
    },
    DSUCC: {
      title: 'Dispatch Succeeded',
      badge: 'success', badgeText: 'dispatch state',
      body: '<p>Every task ended <code>succeeded</code> with a result. <code>Finish</code> stops the lease renewal and writes the ending, which applies only while the dispatch is <code>running</code> and held by this server, so it is written once.</p><p class="dg-metric">Log: dispatch finished, with tasks and succeeded</p>'
    },
    DFAIL: {
      title: 'Dispatch Failed',
      badge: 'reject', badgeText: 'dispatch state',
      body: '<p>A task ended <code>failed</code> with a result, with or without an exit code. The job ran and gave an answer; the tasks after it are not run.</p><p class="dg-metric">Log: dispatch finished</p>'
    },
    DUNANS: {
      title: 'Dispatch Unanswered',
      badge: 'reject', badgeText: 'dispatch state',
      body: '<p>A task returned an error, so the run has no verdict. <code>Error</code> records it prefixed with the task: <code>task "build": every attempt failed: &lt;last failure&gt;</code>.</p><p>An execution it left <code>lost</code> is reconciled later by the provider release loop, which does not change the dispatch.</p><p class="dg-metric">Log (warn): dispatch finished without an answer</p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Entry point |
| <span class="dg-swatch dg-filter"></span> | Validation and admission |
| <span class="dg-swatch dg-decision"></span> | Decision / branch |
| <span class="dg-swatch dg-process"></span> | Processing step |
| <span class="dg-swatch dg-storage"></span> | Store or ledger write |
| <span class="dg-swatch dg-background"></span> | Cleanup |
| <span class="dg-swatch dg-success"></span> | Success |
| <span class="dg-swatch dg-reject"></span> | Rejection / failure |
