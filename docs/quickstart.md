---
title: "Quickstart"
seoTitle: "Quickstart: Plan and Run a Job Locally"
description: "Build Vagabond, start a dev server over fake providers, and validate, plan, run, register and dispatch a job with no cloud account."
weight: 110
---

This page starts a server over the fake providers in `examples/config.hcl` and
takes a job through every stage: validate, plan, run, register, dispatch, and
read back. No cloud account, database or credential is needed. Every command
after the build talks to the server over its [HTTP API](api.md); the full
command and flag reference is in the [CLI reference](cli.md).

## Build

Requires Go 1.27.

```bash
git clone https://github.com/afreidah/vagabond
cd vagabond
make build
```

`make build` writes `./vagabond` in the repository root with the version
stamped in.

## Start a dev server

```bash
./vagabond server -dev -config examples/config.hcl
```

```shell
time=2026-09-28T14:02:11.482Z level=INFO msg="serving agents" address=127.0.0.1:4748
time=2026-09-28T14:02:11.483Z level=INFO msg=serving address=127.0.0.1:4747 tls=false
```

| Item | Value |
|---|---|
| API address | `127.0.0.1:4747` |
| Agent address | `127.0.0.1:4748` |
| Log level | `info`; change with `-log-level debug\|info\|warn\|error` |
| Stop | Ctrl-C or SIGTERM; in-flight requests get 10 seconds to drain |

Every other command looks for the server at `http://127.0.0.1:4747`. Point it
elsewhere with `-address` or `$VAGABOND_ADDR`.

`examples/config.hcl` declares four providers, all fakes:

| Provider | Type | Driver | Pools | Enabled |
|---|---|---|---|---|
| `aws-lambda` | `fake-function` | `function` | `requests`, `compute` | yes |
| `cloudflare-workers` | `fake-worker` | `worker` | none | no |
| `gcp-cloud-run` | `fake-container` | `container` | `cpu`, `compute` | yes |
| `ibm-code-engine` | `fake-container` | `container` | `requests`, `cpu`, `compute` | yes |

Fake provider behaviour:

- `fake-container` accepts a task and never finishes it. A run placed on one
  waits until it is interrupted.
- `fake-function` finishes inside the submit call with exit code 0, a duration
  of one second, and no output.
- Capability snapshots report the time the server started as their observation
  time.

**Startup failures:**

| Message | Cause |
|---|---|
| `No providers are configured, so there is nothing to run on.` | The configuration declares no `provider` block |
| `The server needs a store block, or -dev to keep everything in memory.` | `-dev` omitted and no `store` block |
| `Listening for agents on 127.0.0.1:4748: ...` | The agent port is taken |
| `listening on 127.0.0.1:4747: ...` | The API port is taken |

Run the commands below in a second terminal.

## Validate a job

`job validate` parses and checks a job file locally. It does not contact the
server.

```bash
./vagabond job validate examples/go-test.vagabond.hcl
```

`examples/go-test.vagabond.hcl` declares `parameterized { meta_required =
["version"] }`, so validation fails until the value is supplied:

```shell
Error: Unset metadata for job "go-test"

  on examples/go-test.vagabond.hcl line 41, in job "go-test":
  41:     meta_required = ["version"]

This job requires metadata that was not supplied: version. Pass each one with
-meta <key>=<value>.
```

```bash
./vagabond job validate -meta version=1.2.3 examples/go-test.vagabond.hcl
```

```shell
Job specification examples/go-test.vagabond.hcl is valid.
```

Exit code 0 when valid, 1 otherwise. Pass `-` as the path to read standard
input.

## Plan a job

`job plan` sends the job to the server, which admits and ranks it against every
provider and returns the result. Nothing is dispatched or reserved.

```bash
./vagabond job plan -meta version=1.2.3 examples/go-test.vagabond.hcl
```

```shell
go-test.test (container)
gcp-cloud-run       admitted  score 100        observed 2026-09-28 14:02:11Z
ibm-code-engine     admitted  score 100        observed 2026-09-28 14:02:11Z
aws-lambda          rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
cloudflare-workers  rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
Selected: gcp-cloud-run
Estimated cost: free
```

Reading the output:

- One block per task, headed `<job>.<task> (<driver>)`.
- Admitted providers first, best score first; equal scores keep provider-name
  order. Rejected providers follow in provider-name order.
- A rejected row shows the first rule the provider failed and a sentence
  explaining it. Reason codes are listed in
  [Scheduling](scheduling.md#reason-codes).
- `observed` is the age of the capability snapshot the verdict was made from.
- `Selected` is where `job run` would send the task.

Both container providers score 100 here because the dev ledger starts empty:
the headroom scorer sees 100% of every pool free, and the job's affinity for
`provider.free_quota_percent > 50` holds on both. The tie falls to name order.
Ranking is described in [Scheduling](scheduling.md#ranking).

**`-verbose`** adds each scorer's value under a candidate, and every further
rule a rejected provider failed:

```bash
./vagabond job plan -meta version=1.2.3 -verbose examples/go-test.vagabond.hcl
```

```shell
go-test.test (container)
gcp-cloud-run       admitted  score 100  observed 2026-09-28 14:02:11Z
                                headroom 1.00
                                affinity 1.00
ibm-code-engine     admitted  score 100  observed 2026-09-28 14:02:11Z
                                headroom 1.00
                                affinity 1.00
aws-lambda          rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
                                driver-unsupported
                                image-unsupported
cloudflare-workers  rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
                                provider-disabled
                                driver-unsupported
                                arch-unsupported
                                image-unsupported
                                network-unsupported
                                constraint-unmet
```

`cloudflare-workers` is disabled, so it has never been refreshed and its
snapshot is empty; every capability check fails against it.

| Exit code | Meaning |
|---|---|
| 0 | Every task has at least one admitted provider |
| 1 | Some task has nowhere to run, or the plan could not be produced |

When a task has no admitted provider, the block ends with `No provider can run
this task.`, followed by `At least one rejection is transient, so this may be
admitted later.` when any rejection is `quota-exhausted`, `provider-disabled`
or `provider-unhealthy`.

## Run a job

`job run` sends a job file to the server, which dispatches it and returns a
dispatch ID. The command then polls the dispatch every second until it ends.
The job is not registered.

`go-test` is placed on a `fake-container` provider, which never finishes, so
the run waits after acceptance:

```bash
./vagabond job run -meta version=1.2.3 examples/go-test.vagabond.hcl
```

```shell
==> dispatch 01a0c3f1-9e02-7a44-8c1d-6b2e3f4a5b6c of "go-test"
==> test accepted on gcp-cloud-run
^CJob "go-test" was interrupted. The execution was stopped.
```

Ctrl-C (or SIGTERM) cancels the unfinished execution on the server, which stops
the whole dispatch, and exits 2.

A job on the `function` driver finishes on `aws-lambda`. Save this as
`hello.vagabond.hcl`:

```hcl
job "hello" {
  type = "batch"

  task "greet" {
    driver = "function"

    config {
      function = "hello"
    }

    resources {
      memory = 1024
    }
  }
}
```

The job names no providers, so every provider is considered; the two container
providers are rejected with `driver-unsupported` and `cloudflare-workers` with
`provider-disabled`.

```bash
./vagabond job run hello.vagabond.hcl
```

```shell
==> dispatch 01a0c3f2-4b17-7d09-a1e3-8c5d2f6e7a90 of "hello"
==> greet succeeded on aws-lambda in 1s
```

Output format:

| Line | Printed when |
|---|---|
| `==> dispatch <id> of "<job>"` | The server accepted the run |
| `==> <task> <state> on <provider>` | A poll sees the execution in a new non-terminal state; ` (attempt N)` is appended from the second attempt, ` (<class> failure)` when one is recorded |
| task output | The execution has a result, unless `-no-logs` |
| `==> <task> succeeded on <provider> in <duration>` | The task exited 0 |
| `==> <task> failed (exit N) on <provider> in <duration>` | The task exited non-zero |
| `Job "<job>" did not run: <error>` | The dispatch ended `unanswered` |

Progress lines go to stderr and task output to stdout, so redirecting stdout
captures the task's output alone. Output is printed once the task finishes, not
streamed. A state line such as `==> greet submitted on aws-lambda` appears
before the verdict when a poll lands between submission and result.

| Exit code | Meaning |
|---|---|
| 0 | Every task ran and exited zero |
| 1 | A task ran and failed, or the job could not be sent |
| 2 | The work never ran: nothing was eligible, every provider failed, or the command was interrupted |

The server logs each run at `info`:

```shell
time=2026-09-28T14:04:37.915Z level=INFO msg="dispatch started" dispatch=01a0c3f2-4b17-7d09-a1e3-8c5d2f6e7a90 job=hello version=0 namespace=default
time=2026-09-28T14:04:37.916Z level=INFO msg="dispatch finished" dispatch=01a0c3f2-4b17-7d09-a1e3-8c5d2f6e7a90 job=hello version=0 namespace=default tasks=1 succeeded=true
```

A run of an unregistered job records job version 0. Retries, rerouting and
leases are covered in [Dispatch](dispatch.md).

## Register and dispatch a job

A registered job is stored on the server under its name, versioned, and run by
name with `job dispatch`.

```bash
./vagabond job register hello.vagabond.hcl
```

```shell
Job "hello" registered as version 1 in namespace "default".
```

Registering the same file again stores nothing:

```shell
Job "hello" is unchanged at version 1 in namespace "default".
```

A new version is made only when the job changed. Versions are compared on the
HCL-formatted source, so indentation and alignment changes do not make one;
comment changes do. `job register` validates the file with each declared `${meta.key}`
left as written, since values arrive at dispatch.

```bash
./vagabond job dispatch hello
```

```shell
==> dispatch 01a0c3f3-0c5a-7f21-9d84-3e7b1a2c4d5f of "hello"
==> greet succeeded on aws-lambda in 1s
```

`job dispatch` reports and exits as `job run` does. Metadata rules:

- A job without a `parameterized` block takes no `-meta`.
- A parameterized job refuses keys in neither `meta_required` nor
  `meta_optional`, and requires every key in `meta_required`.

A registered job can also be planned by name: `./vagabond job plan hello`. An
argument containing a path separator, ending in `.hcl`, or naming an existing
file is read as a file; anything else is a job name.

## Job status

With no argument, `job status` lists the namespace's jobs:

```bash
./vagabond job status
```

```shell
NAME   VERSION  STATUS      UPDATED
hello  1        registered  2026-09-28 14:05:02Z
```

With a name, it shows the job's versions and its 20 most recent executions:

```bash
./vagabond job status hello
```

```shell
Name      = hello
Namespace = default
Version   = 1
Status    = registered

Versions
VERSION  REGISTERED
1        2026-09-28 14:05:02Z

Recent executions
DISPATCH  VERSION  TASK   PROVIDER    STATE      CREATED
1a2c4d5f  1        greet  aws-lambda  succeeded  2026-09-28 14:05:19Z
```

`DISPATCH` is the last eight characters of the dispatch ID. IDs are UUIDv7, so
their leading characters are a timestamp shared by runs started close together.
Times are UTC.

`job stop hello` deregisters the job (`Job "hello" stopped in namespace
"default".`). Its versions are kept, its status reads `stopped`, and
registering it again makes a new version and makes it dispatchable again.

## Read an execution

An execution is one attempt of one task on one provider. The CLI does not print
execution IDs; read them from the dispatch record in the API:

```bash
curl -s http://127.0.0.1:4747/v1/dispatch/01a0c3f3-0c5a-7f21-9d84-3e7b1a2c4d5f | jq -r '.Executions[].ID'
```

```shell
01a0c3f3-0c61-7b3e-8a27-5f9c0d1e2b3a
```

**`execution status`** prints the execution's record. Fields the execution does
not have yet are left out: `Started` appears once it reached `running`, and
`Duration`, `Exit Code` and `Billed` once it has a result.

```bash
./vagabond execution status 01a0c3f3-0c61-7b3e-8a27-5f9c0d1e2b3a
```

```shell
ID          = 01a0c3f3-0c61-7b3e-8a27-5f9c0d1e2b3a
Namespace   = default
Job         = hello (version 1)
Dispatch    = 01a0c3f3-0c5a-7f21-9d84-3e7b1a2c4d5f
Task        = greet
Provider    = aws-lambda
Attempt     = 1
State       = succeeded
Provider ID = fake-01a0c3f3-0c61-7b3e-8a27-5f9c0d1e2b3a
Created     = 2026-09-28 14:05:19Z
Ended       = 2026-09-28 14:05:19Z
Duration    = 1s
Exit Code   = 0
```

Execution states are `pending`, `submitted`, `accepted`, `running`,
`succeeded`, `failed`, `cancelled` and `lost`; see
[Dispatch](dispatch.md) for the transitions.

**`execution logs`** prints the stored output: the last 64 KiB, available once
the execution has a result. A `fake-function` execution produces no output, so
this prints nothing:

```bash
./vagabond execution logs 01a0c3f3-0c61-7b3e-8a27-5f9c0d1e2b3a
```

## What `-dev` keeps in memory

`-dev` replaces the store with in-memory stores, empty at every start and lost
on exit.

| Held in memory | Normally held in |
|---|---|
| Registered jobs and their versions | The store |
| Dispatches, their leases, and executions with results and output | The store |
| Quota usage and reservations | The store |

- A `store` block in the configuration is ignored with the warning `Running
  with -dev: the store block is ignored and everything is kept in memory.`
- No migrations run and no database is contacted.
- A dispatch running when the server stops is not resumed by the next start,
  because its record is gone.
- Quota usage restarts at zero, so every pool reports 100% free after a
  restart.

Provider capability snapshots and connected agent nodes are held in memory in
every mode. See [Database](database.md) for what the store holds.

## Next steps

- [Configuration](configuration.md): providers, pools, namespaces, the `store`
  and `server` blocks
- [Job specification](job-specification.md): every block and attribute of a job
  file
- [Agent](agent.md): run work on your own nodes through a pool provider
- [Cloud Run](providers/cloud-run.md) and [Lambda](providers/lambda.md): real
  providers
- [Architecture](architecture.md): the components and the path a task takes
