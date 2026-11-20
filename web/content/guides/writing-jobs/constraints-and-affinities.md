---
title: "Constraints and Affinities"
description: "Require a provider attribute with a constraint, which rejects the providers that lack it, or prefer one with an affinity, which only ranks them."
weight: 50
---

A constraint is a requirement: a provider that fails it is rejected. An
affinity is a preference: a provider that fails it stays a candidate and
scores lower. Both test an attribute every provider publishes. The examples
below test `provider.isolation`, which is `vm` for a provider running each
task in a virtual machine of its own and `container` for one sharing a kernel.

![Every provider is checked in four tiers: policy, availability, mismatch and capacity. A provider that fails a check is rejected with that check's reason code. The providers that pass every check are the candidates, which ranking scores to select one.](../../docs/assets/admission.svg)

## Step 1: Require an Attribute

A task running code nobody reviewed can require a virtual machine:

```hcl
job "untrusted" {
  routing {
    constraint {
      attribute = "provider.isolation"
      operator  = "="
      value     = "vm"
    }
  }

  task "run" {
    driver = "container"

    config {
      image   = "busybox:1.36"
      command = "echo"
      args    = ["in a virtual machine of its own"]
    }
  }
}
```

```shell
$ vagabond job plan vm-only.vagabond.hcl
untrusted.run (container)
gcp-cloud-run               admitted  tier 0            score 100  observed 2026-11-20 03:32:19Z
homelab-vm/nomad-client-04  admitted  tier 1            score 100  observed 2026-11-20 03:32:47Z
homelab                     rejected  constraint-unmet  closest node nomad-client-04: The job requires provider.isolation = "vm" and this provider publishes "container".
ibm-code-engine             rejected  constraint-unmet  The job requires provider.isolation = "vm" and this provider publishes "container".
Tiers: strict
Selected: gcp-cloud-run
Estimated cost: free
```

Cloud Run jobs and Firecracker microVMs publish `vm`; Code Engine and the
`containerd` homelab provider publish `container` and are rejected with
`constraint-unmet`, naming what they publish.

## Step 2: Prefer an Attribute

The same test as an affinity:

```hcl
routing {
  affinity {
    attribute = "provider.isolation"
    operator  = "="
    value     = "vm"
    weight    = 50
  }
}
```

```shell
$ vagabond job plan prefer-vm.vagabond.hcl
prefer-vm.run (container)
gcp-cloud-run               admitted  tier 0  score 100  observed 2026-11-20 03:32:19Z
ibm-code-engine             admitted  tier 0  score 50   observed 2026-11-20 03:32:19Z
homelab-vm/nomad-client-04  admitted  tier 1  score 100  observed 2026-11-20 03:32:47Z
homelab/nomad-client-04     admitted  tier 1  score 50   observed 2026-11-20 03:32:47Z
Tiers: strict
Selected: gcp-cloud-run
Estimated cost: free
```

Every provider is admitted. A provider's score is the average of its quota
score, 100 here since the task charges no pool, and its affinity score: 1 for
a provider matching every affinity, 0 for one matching none. The two
`container` providers average 100 and 0 to 50, and rank below the `vm`
provider in their tier.

`weight` matters with more than one affinity: the affinity score is the
weights of the matched affinities over the weights of all of them.

## Writing the Test

| Setting | Values |
|---|---|
| `attribute` | `provider.<name>` published for every provider, `provider.meta.<key>` from a provider's `meta` block, or `node.label.<key>` from an agent's `-label` flags |
| `operator` | `=`, `!=`, `<`, `<=`, `>`, `>=`, `set_contains`, `is_set`, `is_not_set` |
| `value` | The right-hand side, as a string; omitted for `is_set` and `is_not_set` |

Every attribute, and how each operator compares, is in the
[job specification](../../docs/job-specification.md#attributes).
