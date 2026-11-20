---
title: "Routing"
description: "Choose which providers a job may use with its providers list, see how tiers order them, and keep a job within free quota with max_cost_usd."
weight: 40
---

The `routing` block decides which providers may run a job's tasks. Without
one, every provider is a candidate.

## Step 1: Plan a Job With No Routing

`hello.vagabond.hcl` has no `routing` block:

```hcl
job "hello" {
  task "greet" {
    driver = "container"

    config {
      image   = "busybox:1.36"
      command = "echo"
      args    = ["hello from vagabond"]
    }
  }
}
```

```shell
$ vagabond job plan hello.vagabond.hcl
hello.greet (container)
gcp-cloud-run               admitted  tier 0  score 100  observed 2026-11-20 03:50:19Z
ibm-code-engine             admitted  tier 0  score 100  observed 2026-11-20 03:50:19Z
homelab/nomad-client-04     admitted  tier 1  score 100  observed 2026-11-20 03:50:33Z
homelab-vm/nomad-client-04  admitted  tier 1  score 100  observed 2026-11-20 03:50:33Z
Tiers: strict
Selected: gcp-cloud-run
Estimated cost: free
```

Every provider that runs containers is admitted. `Tiers: strict` ranks every
tier 0 provider ahead of every tier 1 one, so a cloud is selected; the server's
configuration gives each provider its tier.

## Step 2: Name the Providers

`providers` is an allowlist. `firecracker-hello`, from
[Running Jobs in Firecracker MicroVMs](../firecracker-microvms.md), names one:

```hcl
routing {
  providers = ["homelab-vm"]
}
```

```shell
$ vagabond job plan firecracker-hello.vagabond.hcl
firecracker-hello.hello (container)
homelab-vm/nomad-client-04  admitted  tier 1           score 100  observed 2026-11-20 03:27:24Z
gcp-cloud-run               rejected  not-allowlisted  The job routes only to homelab-vm.
homelab                     rejected  not-allowlisted  closest node nomad-client-04: The job routes only to homelab-vm.
ibm-code-engine             rejected  not-allowlisted  The job routes only to homelab-vm.
Tiers: strict
Selected: homelab-vm
Estimated cost: free
```

Every provider left out is rejected with `not-allowlisted`. The order of the
list carries no meaning: the providers in it are ranked as without it, by tier
and then score.

## Step 3: Stay Within Free Quota

`static-analysis` names three providers and sets `max_cost_usd = 0`:

```hcl
routing {
  providers    = ["gcp-cloud-run", "ibm-code-engine", "homelab"]
  max_cost_usd = 0
}
```

```shell
$ vagabond job plan -meta repo=afreidah/vagabond -meta commit=5fbb16230b8b \
    -meta targets="vet lint fmt-check govulncheck" static-analysis
static-analysis.make (container)
gcp-cloud-run            admitted  tier 0           score 91   observed 2026-11-20 00:53:19Z
ibm-code-engine          admitted  tier 0           score 91   observed 2026-11-20 00:53:19Z
homelab/nomad-client-04  admitted  tier 1           score 100  observed 2026-11-20 00:53:20Z
homelab-vm               rejected  not-allowlisted  closest node nomad-client-04: The job routes only to gcp-cloud-run, ibm-code-engine, homelab.
Tiers: strict
Selected: gcp-cloud-run
Estimated cost: free
```

`max_cost_usd = 0`, also the default, means the job does not pay: a provider
is admitted only while its quota pools have room for the run, and
`Estimated cost: free` says the selected one does. Leaving `homelab-vm` out
keeps the job in containers, though it shares the homelab's nodes.

Every routing setting, `tiers` among them, is in the
[`routing` block](../../docs/job-specification.md#routing-block) reference.
