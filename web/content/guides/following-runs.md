---
title: "Following and Finding Runs"
linkTitle: "Following and Finding Runs"
description: "Follow a run as it happens, find it again afterwards by job, state or provider, and read its dispatch, executions and output."
weight: 50
---

This guide covers reading what Vagabond ran: following a run from the command
that started it, finding it again later, and reading each attempt's record and
output.

## Overview

A run has two levels:

| Level | What it is | Read with |
|---|---|---|
| Dispatch | One run of a job, started by `job run`, `job dispatch` or a periodic job's schedule | `dispatch status <id>` |
| Execution | One attempt of one task on one provider | `execution status <id>`, `execution logs <id>` |

![job dispatch starts a dispatch, one run of the static-analysis job, which dispatch status reads. The dispatch created one execution, attempt 1 of the make task on gcp-cloud-run, which failed with exit 2; execution list and execution status read it. The execution's output is stored on the server, the last 16 MiB for 7 days, and execution logs reads it.](../docs/assets/run-anatomy.svg)

Every ID is a UUIDv7, so IDs sort by the time they were made. The commands
that start a run print both kinds of ID as they go; `execution list` finds
them afterwards.

The examples come from a server whose `static-analysis` job runs make targets
against a commit on Cloud Run, Code Engine, or the homelab pool.

## Prerequisites

- A Vagabond server, with `VAGABOND_ADDR` set to its API address, or
  `-address` on every command, and `VAGABOND_TOKEN` set to a token that reads
  the job's namespace when the server declares tokens
  ([CLI](../docs/cli.md#connecting-to-the-server))
- A job to run, registered or in a file

## Step 1: Follow a Run

`job dispatch` runs a registered job by name and `job run` a job file; both
follow the run until it ends:

```shell
$ vagabond job dispatch -meta repo=afreidah/vagabond -meta commit=5fbb16230b8b \
    -meta targets="vet lint fmt-check govulncheck" static-analysis
==> dispatch 01a1f60f-00c9-7c46-bd03-c69608668233 of "static-analysis"
==> make pending on gcp-cloud-run (execution 01a1f60f-00d2-7960-9445-10aee77c63f0)
==> make accepted on gcp-cloud-run
==> make running on gcp-cloud-run
go: downloading golang.org/x/vuln v1.8.0
...
=== Symbol Results ===
No vulnerabilities found.
==> make succeeded on gcp-cloud-run in 4m11.584s
```

| Line | Meaning |
|---|---|
| `==> dispatch <id> of "<job>"` | The server accepted the run. The ID is the dispatch's |
| `==> make pending on gcp-cloud-run (execution <id>)` | The task's execution, recorded and about to be sent to Cloud Run. The first line about an execution carries its ID |
| `==> make accepted on gcp-cloud-run`, `running` | Cloud Run took it, then started it. The CLI reads the run once a second and prints each new state it sees |
| task output | What the task prints, as the server stores it from Cloud Run while it runs |
| `==> make succeeded on gcp-cloud-run in 4m11.584s` | The task exited 0, after the run time Cloud Run reported |

The command exits 0. Progress lines go to stderr and task output to stdout, so
`2>/dev/null` keeps only the task's output.

A task that exits non-zero ends the run `failed`, and the command exits 1.
This run of the `lint` target found four issues:

```shell
$ vagabond job dispatch -meta repo=afreidah/vagabond -meta commit=<sha> \
    -meta targets="vet lint fmt-check govulncheck" static-analysis
==> dispatch 01a1f2cf-42bb-76c0-8af9-34ac05c73c87 of "static-analysis"
==> make pending on gcp-cloud-run (execution 01a1f2cf-42c1-7f41-82d3-2176c35a3d8f)
==> make running on gcp-cloud-run
...
4 issues:
* gocognit: 1
* gofmt: 2
* misspell: 1
make: *** [Makefile:166: lint] Error 1
Container called exit(2).
==> make failed (exit 2) on gcp-cloud-run in 3m9.444s
```

Every progress line, exit code and the interrupt are in the
[CLI reference](../docs/cli.md#job-run).

## Step 2: Follow an Execution From Elsewhere

`execution logs -f` follows one execution's output from another terminal, or
after the starting command has gone. Ctrl-C ends the command and leaves the
execution running, unlike Ctrl-C on `job run`, which cancels the run.

```shell
$ vagabond execution logs -f 01a1f60f-00d2-7960-9445-10aee77c63f0
go: downloading golang.org/x/vuln v1.8.0
...
No vulnerabilities found.
```

It prints the output from the first byte and exits when the execution ends.
This one had already ended, so it printed the copy the server stored.

## Step 3: Find a Run Afterwards

`execution list` lists a namespace's most recent executions across every job,
newest first, 20 by default:

```shell
$ vagabond execution list -limit 5
ID                                    JOB                TASK   PROVIDER         NODE             ATTEMPT  STATE      CREATED
01a1f60f-00d2-7960-9445-10aee77c63f0  static-analysis    make   gcp-cloud-run    -                1        succeeded  2026-11-19 23:05:40Z
01a1f588-b91a-70cd-bb85-3c9a1feb2ff0  firecracker-hello  hello  homelab-vm       nomad-client-04  1        succeeded  2026-11-19 20:39:00Z
01a1f365-60d4-7bdc-b032-5ab81c448943  static-analysis    make   ibm-code-engine  -                1        succeeded  2026-11-19 10:41:09Z
01a1f359-038e-7e3a-9bce-65008fa9c212  static-analysis    make   ibm-code-engine  -                1        succeeded  2026-11-19 10:27:39Z
01a1f32f-5c0a-7846-8752-4f1b95c32fd8  static-analysis    make   gcp-cloud-run    -                1        succeeded  2026-11-19 09:42:09Z
```

`-job`, `-state` and `-provider` narrow it, together when given together:

```shell
$ vagabond execution list -job static-analysis -state failed -provider gcp-cloud-run
ID                                    JOB              TASK  PROVIDER       NODE  ATTEMPT  STATE   CREATED
01a1f2cf-42c1-7f41-82d3-2176c35a3d8f  static-analysis  make  gcp-cloud-run  -     1        failed  2026-11-19 07:57:11Z
01a1bbfe-eed1-7f05-b332-962ed724e40a  static-analysis  make  gcp-cloud-run  -     1        failed  2026-11-08 16:30:09Z

$ vagabond execution list -state lost
No executions match.
```

| Flag | Narrows to |
|---|---|
| `-job <name>` | One job's executions, registered or run from a file |
| `-state <state>` | `pending`, `submitted`, `accepted`, `running`, `succeeded`, `failed`, `cancelled` or `lost` |
| `-provider <name>` | One provider's executions |
| `-limit <n>` | The newest `n`, up to 1000 |
| `-namespace <name>` | Another namespace than `$VAGABOND_NAMESPACE`, or `default` |

`NODE` is the agent node an execution ran on, and `-` on a provider not made of
nodes. For a registered job, `job status <name>` shows the same table for its
20 newest, with its versions:

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

Recent executions
ID                                    TASK  PROVIDER         NODE  ATTEMPT  STATE      CREATED
01a1f60f-00d2-7960-9445-10aee77c63f0  make  gcp-cloud-run    -     1        succeeded  2026-11-19 23:05:40Z
01a1f365-60d4-7bdc-b032-5ab81c448943  make  ibm-code-engine  -     1        succeeded  2026-11-19 10:41:09Z
...
```

## Step 4: Read a Dispatch

`dispatch status` shows one run: the job and version it ran, how it ended, and
every execution it created, oldest first:

```shell
$ vagabond dispatch status 01a1f2cf-42bb-76c0-8af9-34ac05c73c87
ID        = 01a1f2cf-42bb-76c0-8af9-34ac05c73c87
Namespace = default
Job       = static-analysis (version 4)
State     = failed
Created   = 2026-11-19 07:57:11Z
Ended     = 2026-11-19 08:02:23Z

Executions
ID                                    TASK  PROVIDER       NODE  ATTEMPT  STATE   CREATED
01a1f2cf-42c1-7f41-82d3-2176c35a3d8f  make  gcp-cloud-run  -     1        failed  2026-11-19 07:57:11Z
```

This run is the failed lint run from step 1: one task, one attempt, `failed`
because the task exited 2. Every dispatch state is in the
[Dispatch reference](../docs/dispatch.md).

## Step 5: Read an Execution

`execution status` is one attempt's record:

```shell
$ vagabond execution status 01a1f2cf-42c1-7f41-82d3-2176c35a3d8f
ID          = 01a1f2cf-42c1-7f41-82d3-2176c35a3d8f
Namespace   = default
Job         = static-analysis (version 4)
Dispatch    = 01a1f2cf-42bb-76c0-8af9-34ac05c73c87
Task        = make
Provider    = gcp-cloud-run
Attempt     = 1
State       = failed
Provider ID = vagabond-01a1f2cf-42c1-7f41-82d3-2176c35a3d8f
Created     = 2026-11-19 07:57:11Z
Started     = 2026-11-19 07:58:59Z
Ended       = 2026-11-19 08:02:17Z
Duration    = 3m9.444s
Exit Code   = 2
```

- `Provider ID` is the Cloud Run job execution's name, for finding it in the
  Google Cloud console.
- `Created` to `Started`, almost two minutes here, is Cloud Run starting the
  container; `Duration` is the run time Cloud Run reported.
- There is no `Node` line: fields with no value are left out, and only an
  agent pool's executions run on a node.

`execution logs` prints what the task printed, from the copy the server
stored when the execution ended:

```shell
$ vagabond execution logs 01a1f2cf-42c1-7f41-82d3-2176c35a3d8f | tail -6
4 issues:
* gocognit: 1
* gofmt: 2
* misspell: 1
make: *** [Makefile:166: lint] Error 1
Container called exit(2).
```

The server keeps the last 16 MiB of each execution's output for 7 days after
it ends, by default; `max_log_mib` and `log_retention` in the
[configuration](../docs/configuration.md) change both.

## The Same From the API

Every command reads an API route, for scripts and dashboards:

| Command | Route |
|---|---|
| `execution list` | `GET /v1/executions?job=&state=&provider=&limit=&namespace=` |
| `dispatch status <id>` | `GET /v1/dispatch/{id}` |
| `execution status <id>` | `GET /v1/execution/{id}` |
| `execution logs <id>` | `GET /v1/execution/{id}/logs`, with `?follow=true` for `-f` |
| `job status <name>` | `GET /v1/job/{name}` |

```shell
$ curl -s "$VAGABOND_ADDR/v1/executions?job=static-analysis&state=failed&limit=1" | jq -r '.[] | "\(.ID) \(.Provider) exit \(.ExitCode)"'
01a1f2cf-42c1-7f41-82d3-2176c35a3d8f gcp-cloud-run exit 2
```

On a server with tokens, curl also sends `-H "X-Vagabond-Token: $VAGABOND_TOKEN"`;
see [Authentication](../docs/api.md#authentication). The
[API reference](../docs/api.md) covers every field.

## Important Notes

`firecracker-hello`, in the first listing of step 3, ran from a file with
`job run`, so it is not registered. `job status` does not know it, and
`execution list -job firecracker-hello` finds it:

```shell
$ vagabond job status firecracker-hello
Error: job not registered: "firecracker-hello" in namespace "default"
```
