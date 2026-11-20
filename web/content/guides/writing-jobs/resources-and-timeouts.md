---
title: "Resources and Timeouts"
description: "Declare a task's CPU, memory and timeout, see a plan reject providers that cannot hold them, and see what they charge against quota pools."
weight: 20
---

A task's `resources` and `timeout` say what it needs. Admission checks them
against each provider's limits, and quota pools are charged by them.

## Step 1: Declare Them

The `static-analysis` task runs make targets against a commit, on Cloud Run,
Code Engine or a homelab pool:

```hcl
task "make" {
  driver = "container"

  # ...

  resources {
    cpu    = 2000
    memory = 3814
  }

  timeout = "20m"
}
```

| Setting | Unit | Here |
|---|---|---|
| `cpu` | Millicores; 1000 is one vCPU | Two vCPUs |
| `memory` | MiB | 3814 MiB, Code Engine's 4G memory at 2 vCPUs |
| `timeout` | A duration: `90s`, `20m`, `1h30m` | Twenty minutes at most |

## Step 2: See What a Plan Checks

A task larger than every provider allows is rejected everywhere, each provider
with its own limit:

```hcl
task "build" {
  driver  = "container"
  timeout = "48h"

  config {
    image   = "golang:1.27"
    command = "go"
    args    = ["build", "./..."]
  }

  resources {
    cpu    = 16000
    memory = 65536
  }
}
```

```shell
$ vagabond job plan big.vagabond.hcl
big.build (container)
gcp-cloud-run    rejected  resources-exceeded  The task asks for 16000 millicores and this provider allows 8000.
homelab          rejected  resources-exceeded  closest node nomad-client-04: The task asks for 16000 millicores and this provider allows 2010.
homelab-vm       rejected  resources-exceeded  closest node nomad-client-04: The task asks for 16000 millicores and this provider allows 2010.
ibm-code-engine  rejected  resources-exceeded  The task asks for 16000 millicores and this provider allows 12000.
No provider can run this task.
```

| Provider | CPU it allows |
|---|---|
| `gcp-cloud-run` | 8000 millicores, Cloud Run's largest task |
| `ibm-code-engine` | 12000 millicores, Code Engine's largest |
| `homelab`, `homelab-vm` | 2010 millicores, the room nomad-client-04 has left |

Each line names the first check the provider failed, so the 64 GiB of memory
and the 48 hour timeout are not mentioned once the CPU is too large. A plan
that admits nothing exits 1.

## Step 3: See What It Charges

A provider's score in a plan is the share of its quota left for this task: the
tightest of the pools the task would charge. A task that declares no
resources and no timeout charges nothing, so every provider scores 100:

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

`static-analysis`, with 2 vCPUs, 3814 MiB and 20 minutes, charges the clouds'
CPU and memory pools, and scores what those pools have left:

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

The homelab declares no quota pools, so it scores 100 either way. Each run
reserves its CPU and memory for its whole `timeout` when it starts, and is
charged for its real run time when it ends ([Quotas](../../docs/quotas.md)).
