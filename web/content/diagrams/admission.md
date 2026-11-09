---
title: "Admission and Ranking"
linkTitle: "Admission and Ranking"
description: "Interactive diagram of how one task is admitted against every provider through 13 checks in four tiers, then scored, ordered by tier mode and selected."
weight: 3
---

How one task is placed: every provider runs 13 checks in four tiers, the providers that pass every check are scored and ordered by the tier mode, and the first in the ranking is selected. `job plan` and dispatch run the same two steps. **Hover over any component** for implementation details.

{{< diagram >}}

<script>
window.vagabondDiagram = {
  roles: { scorer: 'background' },

  source: [
    'flowchart TD',
    '    TASK([Task request<br>scheduler.Request]):::entry --> ADMIT[Admit<br>every check, every provider]:::process',
    '    INPUTS[(Provider inputs<br>one per provider)]:::storage --> ADMIT',
    '    ADMIT -->|each provider| CK_ALLOW',
    '    ADMIT -->|pool with nodes| MEMBER[Pool node<br>judged as the provider]:::process',
    '    MEMBER -->|each node| CK_ALLOW',
    '',
    '    subgraph POLICY [Policy]',
    '        CK_ALLOW["allowlist<br>not-allowlisted"]:::check --> CK_COST["cost<br>cost-policy"]:::check',
    '    end',
    '    subgraph AVAIL [Availability]',
    '        CK_ENABLED["enabled<br>provider-disabled"]:::check --> CK_HEALTHY["healthy<br>provider-unhealthy"]:::check',
    '    end',
    '    subgraph MISMATCH [Mismatch]',
    '        CK_DRIVER["driver<br>driver-unsupported"]:::check --> CK_ARCH["arch<br>arch-unsupported"]:::check',
    '        CK_ARCH --> CK_IMAGE["image<br>image-unsupported"]:::check',
    '        CK_IMAGE --> CK_NETWORK["network<br>network-unsupported"]:::check',
    '        CK_NETWORK --> CK_RESOURCES["resources<br>resources-exceeded"]:::check',
    '        CK_RESOURCES --> CK_DURATION["duration<br>duration-exceeded"]:::check',
    '        CK_DURATION --> CK_ATTRIBUTE["attribute<br>attribute-unknown"]:::check',
    '        CK_ATTRIBUTE --> CK_CONSTRAINT["constraint<br>constraint-unmet"]:::check',
    '    end',
    '    subgraph CAPACITY [Capacity]',
    '        CK_QUOTA["quota<br>quota-exhausted"]:::check',
    '    end',
    '    CK_COST --> CK_ENABLED',
    '    CK_HEALTHY --> CK_DRIVER',
    '    CK_CONSTRAINT --> CK_QUOTA',
    '',
    '    CK_QUOTA -->|every check passed| CAND[Candidates<br>by provider name]:::success',
    '    CK_QUOTA -->|any check failed| REJ[Rejections<br>first reason, rest in Also]:::reject',
    '',
    '    CAND --> HEADROOM["headroom<br>free_quota_percent / 100"]:::scorer',
    '    CAND -->|affinities declared| AFFINITY["affinity<br>matched / total weight"]:::scorer',
    '    CAND -->|weighted mode| TIERSC["tier<br>place in the tier span"]:::scorer',
    '    HEADROOM --> MEAN[Score<br>mean of applied scorers]:::process',
    '    AFFINITY --> MEAN',
    '    TIERSC --> MEAN',
    '    MEAN --> MODE{Tier mode}:::decision',
    '    MODE -->|strict| STRICT[Tier ascending,<br>then score]:::process',
    '    MODE -->|weighted| WEIGHTED[Score alone]:::process',
    '    STRICT --> RANKING[Ranking<br>selected, then fallbacks]:::success',
    '    WEIGHTED --> RANKING',
    '',
    '    RANKING --> PLANOUT[job plan<br>prints, reserves nothing]:::process',
    '    REJ --> PLANOUT',
    '    RANKING -->|ranked order| DISPATCH[Dispatch<br>reserve and submit]:::process',
  ].join('\n'),

  info: {
    TASK: {
      title: 'Task Request',
      badge: 'entry', badgeText: 'entry point',
      body: '<p><code>scheduler.NewRequest</code> builds one request per task from the task, its job&rsquo;s <code>routing</code> block and the server&rsquo;s tier mode. Everything a checker reads is derived once, before any provider is considered.</p><p>From the job file: <code>driver</code>, the <code>image</code> in the driver config, <code>resources.cpu</code> (millicores), <code>resources.memory</code> (MiB), <code>timeout</code>, <code>network</code>, <code>execution.architecture</code>, and routing&rsquo;s <code>providers</code>, <code>max_cost_usd</code>, <code>strategy</code>, <code>tiers</code>, constraints and affinities.</p><p>The request also carries the task&rsquo;s <code>quota.Execution</code>, its declared CPU, memory and timeout, so the quota check, <code>provider.free_quota_percent</code> and the dispatch reservation price the same charge. An unparseable timeout counts as none.</p><p><a href="../../docs/job-specification/#routing-block">routing block &rarr;</a></p>'
    },
    INPUTS: {
      title: 'Provider Inputs',
      badge: 'storage', badgeText: 'snapshot',
      body: '<p><code>registry.Inputs</code> builds one <code>scheduler.Input</code> per configured provider: its capability snapshot, <code>enabled</code>, health, <code>meta</code> tags, <code>tier</code>, its quota pools with their usage, and the namespace&rsquo;s share of them with its usage.</p><p>Capabilities come from the last refresh, every minute on the server. A pool is read live from its connected nodes on every plan, so a node that just joined or filled up is seen at once.</p><p>Usage comes from the ledger&rsquo;s in-memory snapshot: dispatch re-reads it from the store before admitting each task, and <code>job plan</code> uses the copy the server re-reads every 15 seconds.</p><p><a href="../quotas/">Quota accounting diagram &rarr;</a></p>'
    },
    ADMIT: {
      title: 'Admit',
      badge: 'process', badgeText: 'pure function',
      body: '<p><code>scheduler.Admit</code> runs all 13 checkers against every provider, in a fixed order, without stopping at the first rejection. The first failure sets the rejection&rsquo;s reason and detail; each later failure is recorded as a code alone in <code>Also</code>.</p><p>It calls no provider, reads no clock and reserves nothing. <code>job plan</code>, <code>POST /v1/jobs/plan</code> and dispatch call the same function.</p><p>A pool provider with connected nodes is judged node by node. Every other provider, including a pool with no nodes, is judged once.</p><p><a href="../../docs/scheduling/#checkers">Checkers &rarr;</a></p>'
    },
    MEMBER: {
      title: 'Pool Node',
      badge: 'process', badgeText: 'per node',
      body: '<p>Each connected node of a <code>pool</code> provider runs all 13 checks as if it were the whole provider: its own capability snapshot and <code>node.label.*</code> attributes, with the pool&rsquo;s allowlist, <code>enabled</code>, health, tags and quota. A node&rsquo;s maximums are its free room, so a busy node fails <code>resources</code> for a task that fits it idle.</p><p>The pool is a candidate when at least one node passes. The candidate carries the passing nodes, and the pool places the task on the one with the most free memory.</p><p>When no node passes, the pool takes the rejection of the node with the fewest failed checks, the first on a tie, its detail prefixed <code>closest node &lt;name&gt;: </code>.</p><p><a href="../agents/">Agents and pools diagram &rarr;</a></p>'
    },
    CK_ALLOW: {
      title: 'allowlist',
      badge: 'check', badgeText: 'policy, check 1',
      body: '<p>Rejects with <code>not-allowlisted</code> when <code>routing.providers</code> is non-empty and does not name the provider. An absent or empty list admits every provider.</p><p>Policy runs first, so a provider the job excluded is reported as excluded rather than as unsuitable for a job that was never routed there.</p><p>Detail: <code>The job routes only to &lt;providers&gt;.</code></p>'
    },
    CK_COST: {
      title: 'cost',
      badge: 'check', badgeText: 'policy, check 2',
      body: '<p>Rejects with <code>cost-policy</code> when the provider&rsquo;s <code>estimated_cost</code> exceeds the job&rsquo;s <code>max_cost_usd</code> (default 0).</p><p>Every provider publishes an estimated cost of 0, so this check passes for every job.</p><p>Detail: <code>This provider charges &lt;cost&gt; per execution and the job allows &lt;max&gt;.</code></p>'
    },
    CK_ENABLED: {
      title: 'enabled',
      badge: 'check', badgeText: 'availability, check 3',
      body: '<p>Rejects with <code>provider-disabled</code> when the provider is configured <code>enabled = false</code>. Transient: turning it back on admits the same job.</p><p>A disabled provider is never refreshed, so its snapshot is empty. Availability runs before the capability checks so that it is reported as disabled, not as <code>driver-unsupported</code>.</p><p>Detail: <code>An operator disabled this provider in configuration.</code></p>'
    },
    CK_HEALTHY: {
      title: 'healthy',
      badge: 'check', badgeText: 'availability, check 4',
      body: '<p>Rejects with <code>provider-unhealthy</code> when the provider&rsquo;s last capability refresh failed. Transient. An unhealthy provider keeps its last snapshot, which the checks after this one would otherwise judge it on.</p><p>A pool with no connected nodes is unhealthy.</p><p>Detail: <code>This provider did not answer its last capability refresh.</code></p>'
    },
    CK_DRIVER: {
      title: 'driver',
      badge: 'check', badgeText: 'mismatch, check 5',
      body: '<p>Rejects with <code>driver-unsupported</code> when the provider does not offer the task&rsquo;s driver: <code>container</code>, <code>function</code> or <code>worker</code>.</p><p>A mismatch is permanent for the pairing; only a change to the job fixes it. The tier runs cheapest first: set membership, booleans, integer comparisons, then constraint evaluation.</p><p>Detail: <code>The task uses the &lt;driver&gt; driver and this provider offers &lt;drivers or none&gt;.</code></p>'
    },
    CK_ARCH: {
      title: 'arch',
      badge: 'check', badgeText: 'mismatch, check 6',
      body: '<p>Rejects with <code>arch-unsupported</code> when the task sets <code>execution.architecture</code> and the provider does not offer it. A task with no architecture passes everywhere.</p><p>Detail: <code>The task requires &lt;arch&gt; and this provider offers &lt;architectures or none&gt;.</code></p>'
    },
    CK_IMAGE: {
      title: 'image',
      badge: 'check', badgeText: 'mismatch, check 7',
      body: '<p>Rejects with <code>image-unsupported</code> when the task names an image and the provider cannot run arbitrary images. A task with no image passes everywhere.</p><p>Separate from <code>driver</code>: a provider can offer the task&rsquo;s driver and still run only its own images.</p><p>Detail: <code>The task runs &lt;image&gt; and this provider runs only its own images.</code></p>'
    },
    CK_NETWORK: {
      title: 'network',
      badge: 'check', badgeText: 'mismatch, check 8',
      body: '<p>Rejects with <code>network-unsupported</code> when the task sets <code>internet = true</code> and the provider offers no egress, or <code>private = true</code> and it offers no private networking. Internet is checked first.</p><p>Only an explicit <code>true</code> is a requirement. An absent value or <code>false</code> passes everywhere.</p>'
    },
    CK_RESOURCES: {
      title: 'resources',
      badge: 'check', badgeText: 'mismatch, check 9',
      body: '<p>Rejects with <code>resources-exceeded</code> when <code>resources.cpu</code> or <code>resources.memory</code> exceeds the provider&rsquo;s maximum. CPU is checked first and the detail names the first that fails.</p><p>A task that declares no CPU or memory is checked at the provider&rsquo;s default size where it publishes one (pools do), otherwise at 0. No advertised maximum admits any size; a maximum of 0 admits nothing.</p><p>Detail: <code>The task asks for &lt;n&gt; millicores and this provider allows &lt;max&gt;.</code>, or <code>declares none and is sized at</code> for a default size.</p>'
    },
    CK_DURATION: {
      title: 'duration',
      badge: 'check', badgeText: 'mismatch, check 10',
      body: '<p>Rejects with <code>duration-exceeded</code> when the task&rsquo;s <code>timeout</code> exceeds the provider&rsquo;s maximum duration, so a provider never kills a task before its own timeout.</p><p>Skipped when the provider publishes no maximum, the task sets no timeout, or the timeout does not parse; job validation reports that.</p><p>Detail: <code>The task\'s 20m0s timeout exceeds the 15m0s this provider allows.</code></p>'
    },
    CK_ATTRIBUTE: {
      title: 'attribute',
      badge: 'check', badgeText: 'mismatch, check 11',
      body: '<p>Rejects with <code>attribute-unknown</code> when a constraint or affinity names an attribute Vagabond does not publish. The verdict is the same on every provider, so a misspelling reads as a job file to fix rather than as a capacity problem.</p><p>Exempt: <code>provider.meta.*</code>, the provider&rsquo;s <code>meta</code> block, and <code>node.label.*</code>, a pool node&rsquo;s labels.</p><p>Runs before <code>constraint</code>, since an attribute that does not exist cannot fail to match. Job validation already refuses the same names as <code>Unknown constraint attribute</code> or <code>Unknown affinity attribute</code>, so this check is the backstop.</p>'
    },
    CK_CONSTRAINT: {
      title: 'constraint',
      badge: 'check', badgeText: 'mismatch, check 12',
      body: '<p>Rejects with <code>constraint-unmet</code> when a constraint evaluates false against the provider&rsquo;s attributes: the capability snapshot, <code>provider.free_quota_percent</code> for this task, <code>provider.tier</code>, <code>provider.meta.*</code> and, on a pool node, <code>node.label.*</code>.</p><p>Constraints are evaluated in declaration order and the first that fails is reported. An absent attribute satisfies only <code>!=</code> and <code>is_not_set</code>.</p><p><a href="../../docs/job-specification/#operators">Operators &rarr;</a></p>'
    },
    CK_QUOTA: {
      title: 'quota',
      badge: 'check', badgeText: 'capacity, check 13',
      body: '<p>Rejects with <code>quota-exhausted</code> when the task&rsquo;s charge does not fit a pool it charges, <code>used + charge &gt; limit</code>: the provider&rsquo;s pools first, then the namespace&rsquo;s share. Transient: a period reset can admit the same job.</p><p>Applies only when <code>max_cost_usd</code> is unset or 0; a job that will pay passes at any usage. A provider with no pools, or a task that charges none of them, passes.</p><p>Capacity runs last, so a provider that cannot run the task at all reports why rather than its remaining allowance.</p><p><a href="../quotas/">Quota accounting diagram &rarr;</a></p>'
    },
    CAND: {
      title: 'Candidates',
      badge: 'success', badgeText: 'admitted',
      body: '<p>Every provider that passed all 13 checks, sorted by provider name, so a plan of the same inputs is byte-identical and can be diffed in CI.</p><p>A candidate embeds the input it was admitted from, with the capability snapshot cloned, so scoring reads the same quota usage, tags and tier. It carries the estimated cost and, for a pool, the nodes that passed.</p>'
    },
    REJ: {
      title: 'Rejections',
      badge: 'reject', badgeText: 'rejected',
      body: '<p>One per provider that failed a check, sorted by provider name: <code>Reason</code>, the first failure&rsquo;s code; <code>Detail</code>, its sentence; and <code>Also</code>, every later code in check order.</p><p><code>quota-exhausted</code>, <code>provider-disabled</code> and <code>provider-unhealthy</code> are transient. When no provider is admitted and at least one rejection is transient, the plan is <code>Retryable</code>.</p><p><a href="../../docs/scheduling/#reason-codes">Reason codes &rarr;</a></p>'
    },
    HEADROOM: {
      title: 'headroom',
      badge: 'scorer', badgeText: 'scorer, always',
      body: '<p><code>clamp(free_quota_percent / 100)</code>, the base scorer of <code>free-first</code>, the only strategy.</p><p><code>free_quota_percent</code> is the lowest remaining percentage, floored, among the pools this task charges, across the provider&rsquo;s pools and the namespace&rsquo;s share. A provider with no pools, or none the task charges, scores 1.00.</p><p>Preferring the most allowance left spends widely eligible providers first and keeps the others for work that can run nowhere else.</p><p><a href="../../docs/quotas/#headroom">Headroom &rarr;</a></p>'
    },
    AFFINITY: {
      title: 'affinity',
      badge: 'scorer', badgeText: 'scorer, when declared',
      body: '<p>The summed weight of satisfied affinities over the summed weight of all of them. <code>weight</code> defaults to 50 and must be positive. An unsatisfied affinity adds nothing and never rejects.</p><p>Applied only when the job declares at least one affinity. A job with none has no affinity scorer, rather than a 0 averaged in.</p><p>Affinities weighted 75 and 25, where only the first matches, score 0.75.</p>'
    },
    TIERSC: {
      title: 'tier',
      badge: 'scorer', badgeText: 'scorer, weighted only',
      body: '<p><code>(highest tier &minus; tier) / (highest tier &minus; lowest tier)</code> over this task&rsquo;s candidates: the lowest tier scores 1.00 and the highest 0.00. When every candidate is in one tier, each scores 1.00.</p><p>Applied only under <code>weighted</code> tiers. Measured over candidates only, so a rejected provider&rsquo;s tier changes no one&rsquo;s score. Candidates in tiers 0, 1, 2 score 1.00, 0.50, 0.00.</p>'
    },
    MEAN: {
      title: 'Score',
      badge: 'process', badgeText: 'arithmetic mean',
      body: '<p>A candidate&rsquo;s score is the mean of the scorers that apply, each in [0, 1]: <code>mean(headroom, affinity, tier)</code>, affinity only when declared and tier only when weighted. The mean keeps scores on one scale however many scorers apply.</p><p>A plan prints <code>round(score &times; 100)</code>. <code>-verbose</code> prints each scorer to two decimals under its candidate.</p>'
    },
    MODE: {
      title: 'Tier Mode',
      badge: 'decision', badgeText: 'ordering',
      body: '<p>The first that is set: the job&rsquo;s <code>routing { tiers = ... }</code>, the server&rsquo;s <code>scheduling { tiers = ... }</code>, then <code>strict</code>.</p><p>A provider&rsquo;s <code>tier</code> is the operator&rsquo;s preference, lower first, default 0. These provider tiers are unrelated to the four check tiers above. With no <code>tier</code> set anywhere, every provider is in tier 0 and both modes rank the same.</p><p><a href="../../docs/scheduling/#tiers">Tiers &rarr;</a></p>'
    },
    STRICT: {
      title: 'strict',
      badge: 'process', badgeText: 'default mode',
      body: '<p>Tier ascending, then score descending. Every candidate in tier 0 ranks ahead of every one in tier 1, whatever their scores; scores order providers within a tier.</p><p>A higher tier is reached only when every lower-tier provider was rejected or, at dispatch, refused a reservation or failed.</p>'
    },
    WEIGHTED: {
      title: 'weighted',
      badge: 'process', badgeText: 'tier as a scorer',
      body: '<p>Score descending, with the <code>tier</code> scorer averaged in, so a higher tier with more headroom can outrank a lower one.</p><p>Candidates in tiers 0, 1 and 2 with 16%, 80% and 90% headroom score 58, 65 and 45, and tier 1 is selected. Under <code>strict</code> the tier 0 provider would be.</p>'
    },
    RANKING: {
      title: 'Ranking',
      badge: 'success', badgeText: 'selection',
      body: '<p>Every candidate, best first. The first is selected; the rest are kept so dispatch can fall back to them in order.</p><p>The sort is stable over candidates already ordered by name, so ties rank by provider name. With no candidates nothing is selected.</p><p>The estimated cost is the selected provider&rsquo;s <code>estimated_cost</code>, 0 for every provider, printed as <code>free</code>.</p>'
    },
    PLANOUT: {
      title: 'job plan Output',
      badge: 'process', badgeText: 'cli',
      body: '<p>Per task, a header <code>&lt;job&gt;.&lt;task&gt; (&lt;driver&gt;)</code>, candidates in ranked order (<code>admitted</code>, <code>tier &lt;n&gt;</code>, <code>score &lt;n&gt;</code>, <code>observed &lt;time&gt;</code>), then rejections by name (<code>rejected</code>, reason, detail).</p><p>Then <code>Tiers: &lt;mode&gt;</code>, <code>Selected: &lt;provider&gt;</code> and <code>Estimated cost: free</code>; or <code>No provider can run this task.</code>, followed by <code>At least one rejection is transient, so this may be admitted later.</code> when the plan is retryable.</p><p>Nothing is reserved. Exit status is 0 when every task has a selection and 1 otherwise. <code>-verbose</code> adds each scorer and each rejection&rsquo;s other codes.</p><p><a href="../../docs/scheduling/#job-plan-output">job plan output &rarr;</a></p>'
    },
    DISPATCH: {
      title: 'Dispatch',
      badge: 'process', badgeText: 'hand-off',
      body: '<p>Dispatch re-reads the usage snapshot, calls the same <code>Admit</code> and <code>Rank</code>, and tries candidates in ranked order, each at most once.</p><p>Every attempt reserves quota before submitting. A refused reservation moves to the next candidate without spending a retry attempt; when every candidate refuses, the task fails with <code>no provider can run this task</code>.</p><p><a href="../task-path/">Task path diagram &rarr;</a></p>'
    }
  }
};
</script>

## Legend

| Node | Meaning |
|------|---------|
| <span class="dg-swatch dg-entry"></span> | Entry point |
| <span class="dg-swatch dg-storage"></span> | Snapshot input |
| <span class="dg-swatch dg-process"></span> | Processing step |
| <span class="dg-swatch dg-filter"></span> | Admission check and its reason code |
| <span class="dg-swatch dg-decision"></span> | Decision |
| <span class="dg-swatch dg-background"></span> | Scorer |
| <span class="dg-swatch dg-success"></span> | Admitted / ranked |
| <span class="dg-swatch dg-reject"></span> | Rejected |
