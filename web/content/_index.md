---
title: "Vagabond"
description: "Run serverless jobs on Cloud Run, Lambda and your own nodes from one Nomad-style job file, with explained placement and enforced quotas."
layout: hextra-home
---

<div class="vb-hero hx:w-full hx:flex hx:flex-col hx:items-center hx:text-center">

<img class="vb-hero-logo hx:hidden hx:dark:block" src="images/logo-dark.png" alt="Vagabond" width="900" height="247">
<img class="vb-hero-logo hx:block hx:dark:hidden" src="images/logo-light.png" alt="Vagabond" width="900" height="247">

<div class="hx:mt-8 hx:mb-6">
{{< hextra/hero-headline >}}
  Serverless jobs across public clouds&nbsp;<br class="hx:sm:block hx:hidden" />and your own infrastructure
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-10">
{{< hextra/hero-subtitle style="margin: 0 auto;" >}}
  Describe a job once in HCL. Vagabond decides which backend can run it, from Cloud Run and Lambda&nbsp;<br class="hx:sm:block hx:hidden" />to machines you run yourself, runs it there, tracks what it used, and tells you why it chose that backend.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-10 vb-homage">
  An homage to <a href="https://www.nomadproject.io/" target="_blank" rel="noopener">HashiCorp Nomad</a>, built to feel familiar to Nomad users.
</div>

<div class="hx:flex hx:flex-wrap hx:gap-3 hx:justify-center hx:mb-6">
{{< hextra/hero-button text="Quickstart" link="docs/quickstart" >}}
{{< hextra/hero-button text="Documentation" link="docs" style="background: transparent; border: 1px solid var(--vb-border); color: var(--vb-text);" >}}
{{< hextra/hero-button text="GitHub" link="https://github.com/afreidah/vagabond" style="background: transparent; border: 1px solid var(--vb-border); color: var(--vb-text);" >}}
</div>

</div>

<div class="vb-diagram">
<img src="images/architecture.svg" width="1200" height="560" alt="The vagabond CLI calls the server's HTTP API. The server admits, ranks, dispatches and accounts for each task, keeps its records in Postgres or CockroachDB, and runs work on Cloud Run Jobs, AWS Lambda, or agent nodes.">
</div>

<div class="vb-sections">

<div class="vb-row">
<div class="vb-text">

## Job files

Jobs, tasks, constraints, resources and parameters in Nomad-style HCL. Files
are validated with line-and-column diagnostics before they are sent.
Registered jobs are versioned on change and dispatched by name.

<a class="vb-more" href="docs/job-specification/">Job specification →</a>

</div>
<div class="vb-example">

```hcl
job "go-test" {
  routing {
    max_cost_usd = 0
  }

  task "test" {
    driver = "container"

    config {
      image   = "golang:1.27"
      command = "go"
      args    = ["test", "./..."]
    }

    resources {
      cpu    = 1000
      memory = 2048
    }

    timeout = "15m"
  }
}
```

</div>
</div>

<div class="vb-row vb-flip">
<div class="vb-text">

## Admission

13 checks across policy, availability, capability and quota decide which
backends can run a task. `job plan` lists each backend with its score, or the
check that rejected it and why. Planning reserves and runs nothing.

<a class="vb-more" href="docs/scheduling/#admission">Admission →</a>

</div>
<div class="vb-example">

<pre><code>$ vagabond job plan go-test.vagabond.hcl
go-test.test (container)
homelab        <span class="vb-ok">admitted</span>  tier 0              score 90  observed 2026-09-28 07:30:59Z
gcp-cloud-run  <span class="vb-ok">admitted</span>  tier 0              score 23  observed 2026-09-28 07:30:59Z
aws-lambda     <span class="vb-no">rejected  driver-unsupported  The task uses the container driver and this provider offers function.</span>
Tiers: strict
<span class="vb-pick">Selected: homelab</span>
Estimated cost: free</code></pre>

</div>
</div>

<div class="vb-row">
<div class="vb-text">

## Quotas

Limits per backend in executions, GB-seconds, vCPU-seconds or seconds, reset
daily or monthly, with optional shares per namespace. A backend the task would
push past a limit is rejected. `max_cost_usd` defaults to 0, so a job runs
only on free capacity unless it sets a budget.

<a class="vb-more" href="docs/quotas/">Quotas →</a>

</div>
<div class="vb-example">

```hcl
provider "aws-lambda" {
  type = "lambda"

  pool "requests" {
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}
```

</div>
</div>

<div class="vb-row vb-flip">
<div class="vb-text">

## Agent nodes

`vagabond agent` registers a machine into a pool, and a `pool` provider
schedules onto it like any other backend. Workloads run on the node's
containerd, limited to the agent's CPU and memory, and are placed on the node
with the most free memory. Agents connect over mutual TLS and register under
their certificate's name.

<a class="vb-more" href="docs/agent/">Agent →</a>

</div>
<div class="vb-example">

```shell
$ vagabond agent -server 10.0.0.5:4748 -pool homelab \
    -tls-ca ca.pem -tls-cert box1.pem -tls-key box1-key.pem

$ vagabond node status
NAME  POOL     ADDRESS         ARCH   CPU USED    MEMORY USED    WORKLOADS  CONNECTED
box1  homelab  10.0.0.21:4211  amd64  2000/8000m  4096/16384MiB  2          2026-09-28 07:12:40Z
box2  homelab  10.0.0.22:3877  arm64  0/4000m     0/8192MiB      0          2026-09-28 07:14:02Z
```

</div>
</div>

</div>

<div class="vb-more-list">

<div>

### Execution history

Every dispatch and attempt is recorded in Postgres or CockroachDB, with its
output. Dispatches are leased, so a server resumes those a dead one left
running.

</div>
<div>

### Provider plugins

Cloud Run Jobs and AWS Lambda today. A plugin translates a task and classifies
failures; scheduling, retries and accounting stay in Vagabond.

</div>
<div>

### Store outages

While the store is unreachable, new work is refused with 503 and running work
finishes. What the outage lost is repaired when the store returns.

</div>

</div>
