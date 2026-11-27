---
title: "Running CI on Free Cloud Quota"
linkTitle: "CI on Free Cloud Quota"
description: "Declare Cloud Run and Code Engine with quota pools under their free allowances, register a parameterized CI job, plan it, and dispatch it from a CI workflow."
weight: 40
---

This guide runs a repository's static analysis on Cloud Run Jobs and IBM Code
Engine within each one's monthly free allowance, with a homelab agent pool
behind them, dispatched from CI on every push.

## Overview

Each provider declares quota pools: how many vCPU-seconds and GB-seconds it
may use in a month. Vagabond records what every execution uses against its
provider's pools, and before each run it admits only the providers whose pools
can still hold the task. Providers in tier 0 are tried before tier 1, so the
clouds take the work first and the homelab pool takes what they cannot.

The CI job is one registered, parameterized job, `static-analysis`. A CI
workflow dispatches it by name with the repository, the commit and the make
targets, and follows the run to its result.

![A CI workflow dispatches the static-analysis job on every push. The server admits only the providers whose pools hold the run, and under strict tiers tries tier 0 first. In tier 0, gcp-cloud-run has used 15,307 of 192,000 vCPU-seconds and 29,897 of 360,000 GB-seconds this month, and ibm-code-engine 6,886 of 80,000 vCPU-seconds and 12,824 of 149,011 GB-seconds. In tier 1, homelab has no quota pools.](../docs/assets/ci-quota.svg)

The examples come from a deployment running `static-analysis` against this
repository on every pushed branch.

## Prerequisites

- A Vagabond server with a Postgres store, so recorded usage survives
  restarts ([Deployment](../docs/deployment.md))
- A Google Cloud project set up for the
  [Cloud Run provider](../docs/providers/cloud-run.md), and an IBM Cloud
  project for the [Code Engine provider](../docs/providers/code-engine.md),
  each with the credentials their pages describe
- Optionally, an agent pool to fall back to
  ([Running Jobs in Containers on Agent Nodes](containers-on-agent-nodes.md))
- A CI system whose runners can reach the server's API

## Step 1: Declare the Providers and Their Pools

In the server's configuration, each cloud provider with a pool per resource,
and the homelab pool in tier 1:

```hcl
provider "gcp-cloud-run" {
  type = "cloud-run"

  config {
    project                 = "my-project"
    region                  = "us-central1"
    runtime_service_account = "vagabond-runtime@my-project.iam.gserviceaccount.com"
  }

  credentials {
    file = "/secrets/gcp-dispatcher.json"
  }

  # 80% of the free tier for Jobs: 240,000 vCPU-seconds and 450,000 GiB-seconds.
  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 192000
    period = "monthly"
  }

  pool "memory" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}

provider "ibm-code-engine" {
  type = "code-engine"

  config {
    region     = "us-south"
    project_id = "<project-id>"
  }

  credentials {
    file = "/secrets/ibm-api-key"
  }

  # 80% of the free tier: 100,000 vCPU-seconds and 200,000 GB-seconds, which is
  # 186,264 in gb_seconds, since Vagabond counts GiB and IBM bills decimal GB.
  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 80000
    period = "monthly"
  }

  pool "memory" {
    meter  = "gb_seconds"
    limit  = 149011
    period = "monthly"
  }
}

# Tier 1: the clouds' free quota goes first, and the homelab takes what it can't.
provider "homelab" {
  type = "containerd"
  tier = 1
}
```

| Setting | Effect |
|---|---|
| `meter = "cpu_seconds"` | Each execution reserves its vCPUs times its timeout when it starts, and is charged its vCPUs times its run time when it ends |
| `meter = "gb_seconds"` | The same for its memory in GiB |
| `limit` | The most the pool holds in a period. Each limit here is 80% of the allowance in its comment, leaving room for usage Vagabond does not see |
| `period = "monthly"` | The pool starts empty at 00:00 UTC on the first of each month |
| no `tier` | Tier 0, tried first |
| `tier = 1` on `homelab` | Tried only when no tier 0 provider admits the task |

Every meter and period is in [Quotas](../docs/quotas.md).

## Step 2: Check the Pools

`provider status` lists each provider with the share of its tightest pool
left:

```shell
$ vagabond provider status
PROVIDER         TIER  STATUS   QUOTA LEFT
gcp-cloud-run    0     healthy  cpu 92%, memory 91%
ibm-code-engine  0     healthy  cpu 91%, memory 91%
homelab          1     healthy  no quota
homelab-vm       1     healthy  no quota

Usage as of 2026-11-19 23:38:34Z (1s ago).
```

With a name, each pool's usage in its own unit:

```shell
$ vagabond provider status gcp-cloud-run
Name   = gcp-cloud-run
Tier   = 0
Status = healthy

POOL    USED    LIMIT    UNIT          LEFT  PERIOD
cpu     15,307  192,000  vCPU-seconds  92%   monthly
memory  29,897  360,000  GB-seconds    91%   monthly

Usage as of 2026-11-20 00:53:19Z (1s ago).
```

`USED` is what this month's executions have been charged so far; the figures
above are from the 19th of the month.

## Step 3: Write the CI Job

`static-analysis.vagabond.hcl` runs make targets against one commit. Its
`parameterized` block makes `repo`, `commit` and `targets` required at
dispatch, and each reaches the task as `VAGABOND_META_<KEY>`:

```hcl
job "static-analysis" {
  parameterized {
    meta_required = ["repo", "commit", "targets"]
  }

  routing {
    providers    = ["gcp-cloud-run", "ibm-code-engine", "homelab"]
    max_cost_usd = 0
  }

  task "make" {
    driver = "container"

    # Shallow-fetches the commit, since clone cannot name an arbitrary SHA.
    # One line: Code Engine refuses an argument containing a newline.
    config {
      image   = "golang:1.27"
      command = "sh"
      args = [
        "-ec",
        "git init -q /src; cd /src; git remote add origin \"https://github.com/$VAGABOND_META_REPO.git\"; git fetch -q --depth 1 origin \"$VAGABOND_META_COMMIT\"; git checkout -q FETCH_HEAD; make $VAGABOND_META_TARGETS",
      ]
    }

    # Sized to what both clouds bill: whole vCPUs for Cloud Run, and 3814 MiB
    # is Code Engine's 4G at 2 vCPU.
    resources {
      cpu    = 2000
      memory = 3814
    }

    timeout = "20m"
  }
}
```

| Setting | Effect |
|---|---|
| `providers` | Only these three may take it, which keeps it off the `homelab-vm` provider on the same pool |
| `max_cost_usd = 0` | The job does not pay: a provider is admitted only while its pools have room for the run |
| `resources` | What each run is charged against the pools: 2 vCPUs and 3814 MiB for as long as it runs |
| `timeout` | The longest one run may take, and how long each run's reservation counts it for until it ends |

The repository must be public on GitHub, since neither cloud can reach a
private Git server.

## Step 4: Register It

```shell
$ vagabond job register static-analysis.vagabond.hcl
```

`job status` lists the versions it has been registered at:

```shell
$ vagabond job status static-analysis
Name      = static-analysis
Namespace = default
Version   = 4
Status    = registered

Versions
VERSION  REGISTERED
4        2026-11-11 06:00:39Z
3        2026-11-10 17:26:40Z
2        2026-11-08 11:08:46Z
1        2026-11-05 05:20:03Z
...
```

## Step 5: Plan It

`job plan` shows where a dispatch would run now, without running it:

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

| Line | Meaning |
|---|---|
| `admitted` | The provider passed every check, its pools' room for this task included |
| `score 91` | The share of the provider's tightest pool left: Cloud Run's memory pool at 91% |
| `homelab/nomad-client-04 ... score 100` | One line per node of an agent pool; the homelab has no pools, so nothing is used up |
| `rejected  not-allowlisted` | `homelab-vm` is not in the job's `providers` |
| `Tiers: strict` | Every admitted tier 0 provider ranks ahead of every tier 1 one, whatever the scores |
| `Selected: gcp-cloud-run` | Where a dispatch now would run |
| `Estimated cost: free` | The run fits in the selected provider's free quota, as `max_cost_usd = 0` requires |

Ranking and every reason code are in [Scheduling](../docs/scheduling.md).

## Step 6: Dispatch It From CI

A Forgejo Actions workflow dispatching it for every pushed branch, through the
`vagabond` image, on a runner that reaches the server:

```yaml
name: Static analysis

on:
  push:
    branches-ignore: [main, 'dependabot/**']

jobs:
  static-analysis:
    name: Dispatch to Vagabond
    runs-on: build
    steps:
      - name: Run the static-analysis job
        env:
          VAGABOND_TOKEN: ${{ secrets.VAGABOND_TOKEN }}
        run: |
          set -euo pipefail

          docker run --rm --pull always \
            -e VAGABOND_ADDR=http://vagabond.service.consul:4747 \
            -e VAGABOND_TOKEN \
            registry.munchbox.cc/vagabond:latest \
            job dispatch \
              -meta repo=afreidah/vagabond \
              -meta commit=${{ github.sha }} \
              -meta targets="vet lint fmt-check govulncheck" \
              static-analysis
```

`job dispatch` follows the run and exits with its result, so the workflow step
passes when every target passed and fails when one did. The task's output is
the step's log.

`-e VAGABOND_TOKEN` hands the container the runner's variable without putting
the secret on the command line. For a server with
[token blocks](../docs/configuration.md#token-block), the `VAGABOND_TOKEN`
repository secret holds a token that writes the job's namespace; without the
secret, no token is sent.

## Step 7: Verify

`execution list` shows where the runs went:

```shell
$ vagabond execution list -job static-analysis -limit 6
ID                                    JOB              TASK  PROVIDER         NODE  ATTEMPT  STATE      CREATED
01a1f60f-00d2-7960-9445-10aee77c63f0  static-analysis  make  gcp-cloud-run    -     1        succeeded  2026-11-19 23:05:40Z
01a1f365-60d4-7bdc-b032-5ab81c448943  static-analysis  make  ibm-code-engine  -     1        succeeded  2026-11-19 10:41:09Z
01a1f359-038e-7e3a-9bce-65008fa9c212  static-analysis  make  ibm-code-engine  -     1        succeeded  2026-11-19 10:27:39Z
01a1f32f-5c0a-7846-8752-4f1b95c32fd8  static-analysis  make  gcp-cloud-run    -     1        succeeded  2026-11-19 09:42:09Z
01a1f31f-cd28-72cb-bbc8-864176ba506f  static-analysis  make  gcp-cloud-run    -     1        succeeded  2026-11-19 09:25:10Z
01a1f300-abb3-7ad0-9166-d785212d2091  static-analysis  make  gcp-cloud-run    -     1        succeeded  2026-11-19 08:51:09Z
```

Runs alternate between the two clouds as their pools' shares left change, and
`provider status` shows each run's usage added to its provider's pools.
[Following and Finding Runs](following-runs.md) covers reading each run.
