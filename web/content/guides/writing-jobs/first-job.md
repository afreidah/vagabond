---
title: "Your First Job"
description: "Write a job with one container task, check it with job validate and job plan, run it with job run, and read its output back."
weight: 10
---

This page writes a job with one container task, checks it, runs it, and reads
its output back. It runs on whatever providers the server has.

## Step 1: Write the Job

`hello.vagabond.hcl` prints a line from a `busybox` image:

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

| Line | Effect |
|---|---|
| `job "hello"` | The job's name, which every plan and execution record carries |
| `task "greet"` | One unit of work. A job runs its tasks in order |
| `driver = "container"` | Run an image to completion; the exit status is the result |
| `config` | The image, and the command and arguments that replace its entrypoint. `command` is one executable; each `args` element is one argument, so a shell pipeline goes through `sh -c` |

With no `routing` block every provider is a candidate, and with no `resources`
or `timeout` each provider uses its own defaults.

## Step 2: Validate It

`job validate` checks the file without a server:

```shell
$ vagabond job validate hello.vagabond.hcl
Job specification hello.vagabond.hcl is valid.
```

It reports every problem at once. This copy has `image` misspelled and a
timeout written in words:

```hcl
  task "greet" {
    driver  = "container"
    timeout = "15 minutes"

    config {
      imgae   = "busybox:1.36"
      command = "echo"
      args    = ["hello from vagabond"]
    }
  }
```

```shell
$ vagabond job validate typo.vagabond.hcl
Missing image in "hello" task "greet"
  A container task runs an image, so its config block must set one.
Invalid timeout in "hello" task "greet"
  Timeout "15 minutes" is not a duration. Write one as 15m, 90s, or 1h30m.
```

Keys in `config` that no provider reads are ignored, so the misspelled
`imgae` shows up as a missing `image`.

## Step 3: Plan It

`job plan` asks the server where the job would run now, without running it:

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

| Line | Meaning |
|---|---|
| `hello.greet (container)` | The job, the task and its driver: each task is planned on its own |
| `gcp-cloud-run  admitted  tier 0  score 100` | A candidate, its tier from the server's configuration, and its score |
| `homelab/nomad-client-04` | A candidate on an agent pool: the provider and the node |
| `Tiers: strict` | Every tier 0 provider ranks ahead of every tier 1 one |
| `Selected: gcp-cloud-run` | Where a run now would go |
| `Estimated cost: free` | Within the selected provider's free quota |

## Step 4: Run It

`job run` sends the file to the server and follows the run to its end:

```shell
$ vagabond job run hello.vagabond.hcl
==> dispatch 01a1f712-b220-7a44-8a0f-7b3495508dca of "hello"
==> greet pending on gcp-cloud-run (execution 01a1f712-b235-77bc-9ce6-9f634a106aed)
==> greet accepted on gcp-cloud-run
==> greet running on gcp-cloud-run
hello from vagabond
Container called exit(0).
==> greet succeeded on gcp-cloud-run in 1.891s
```

The `==>` lines are the run's progress on stderr: the dispatch, then each
state the execution reached on Cloud Run, then the result. The lines between
them are the task's output on stdout; `Container called exit(0).` is Cloud
Run's own line at the end of a container's log. The command exits 0 because
the task did.

## Step 5: Read the Output Back

The server keeps the output after the run, under the execution's ID:

```shell
$ vagabond execution logs 01a1f712-b235-77bc-9ce6-9f634a106aed
hello from vagabond
Container called exit(0).
```

[Following and Finding Runs](../following-runs.md) covers reading runs in full.
