<p align="center">
  <br>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.png">
    <img alt="Vagabond" src="docs/assets/logo-light.png" width="420">
  </picture>
  <br>
  <br>
</p>

<br>

Vagabond is a multi-cloud compute broker for short-lived, stateless workloads.
A job is described once in Nomad-style HCL; Vagabond decides which cloud
backend can run it, which one should, runs it there, and charges it against
that provider's free-tier quota.

It is an homage to HashiCorp Nomad: the job file, `plan` output, registered and
parameterized jobs, and namespaces should read as familiar to a Nomad user.
Vagabond does not depend on Nomad. Its backends are cloud services with no
scheduler of their own.

## Features

- Nomad-style HCL job files, validated with line-and-column diagnostics.
- Admission: 13 checks across policy, availability, capability and quota decide
  which providers can run a task. `max_cost_usd` defaults to 0, so work stays
  on free capacity unless a job opts in to paying.
- Ranking: survivors are scored and one is selected. `job plan` prints every
  provider with its score or the rule it failed.
- Dispatch: submit, watch, collect output, reroute around
  providers that fail to answer, release what the execution left behind.
- Quota ledger: free-tier pools per provider, with optional per-namespace
  shares. Reservations are atomic across processes; settlement uses what the
  platform billed when it reports it.
- Registered jobs: versioned on change, dispatched by name, parameterized with
  `meta_required` and `meta_optional`. Tasks receive `VAGABOND_META_<KEY>`.
- Namespaces, declared in configuration.
- Execution history: every dispatch and attempt recorded, with output.
- Server: an HTTP API over the same operations. Dispatches are leased, so a
  server resumes the ones a dead process left running.
- Postgres or CockroachDB as the store.

Providers:

| Provider | Driver | Doc |
|---|---|---|
| Google Cloud Run Jobs | `container` | [cloud-run.md](docs/providers/cloud-run.md) |
| AWS Lambda | `function` | [lambda.md](docs/providers/lambda.md) |

The `worker` driver is modeled and admitted; no plugin implements it yet.

## Server and CLI

- `vagabond server` holds the providers, stores and scheduling, and serves the
  [HTTP API](docs/api.md).
- Every other command is an API client, finding the server with `-address` or
  `$VAGABOND_ADDR` (default `http://127.0.0.1:4747`). Runners need an address,
  not cloud or database credentials.
- `vagabond server -dev` keeps every store in memory, for development and
  single-machine use.
- `job validate` is local, and job files are validated locally before they are
  sent.

## Quickstart

```bash
make build

./vagabond server -dev -config examples/config.hcl &

./vagabond job plan -meta version=1.2.3 examples/go-test.vagabond.hcl
```

```
go-test.test (container)
ibm-code-engine     admitted  score 90         observed 2026-09-21 07:30:59Z
gcp-cloud-run       admitted  score 23         observed 2026-09-21 07:30:59Z
aws-lambda          rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
cloudflare-workers  rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
Selected: ibm-code-engine
Estimated cost: free
```

The example config uses fake providers, so this needs no cloud account.
See [quickstart.md](docs/quickstart.md).

## Commands

| Command | Does |
|---|---|
| `job validate <file>` | Parse and validate |
| `job plan <file or name>` | Show where each task would run, and why |
| `job run <file>` | Run a job file without registering it |
| `job register <file>` | Store a job as a new version when it changed |
| `job dispatch <name>` | Run a registered job's current version |
| `job status <name>` | Versions and recent executions |
| `job stop <name>` | Stop a registered job from being dispatched |
| `execution status <id>` | One execution's record and result |
| `execution logs <id>` | One execution's stored output |
| `server` | Serve the [HTTP API](docs/api.md) |

## Job file

```hcl
job "go-test" {
  routing {
    providers    = ["gcp-cloud-run"]
    max_cost_usd = 0

    constraint {
      attribute = "provider.architecture"
      operator  = "set_contains"
      value     = "amd64"
    }
  }

  task "test" {
    driver = "container"

    config {
      image   = "golang:1.27"
      command = "go"
      args    = ["test", "./..."]
    }

    resources {
      cpu    = 1000   # millicores
      memory = 2048   # MiB
    }

    timeout = "15m"
  }
}
```

## Architecture

```
  job file (HCL)
        |
        v
  +-----------+   parse, validate, substitute meta.*
  |  jobspec  |
  +-----------+
        |
        v  job.Job
  +-----------+   which providers can run this task
  | admission |   13 checkers, 4 tiers
  +-----------+
        |
        +--> rejections: provider, reason, detail
        |
        v  candidates
  +-----------+   which candidate should take it
  |  ranking  |   scorers in [0,1], score is their mean
  +-----------+
        |
        v  selection
  +-----------+   reserve, submit, watch, collect, settle, release
  | dispatch  |   reroute on infrastructure failure
  +-----------+
        |
        v
  +-----------+   dispatches, executions, jobs, quota
  |   store   |   Postgres or CockroachDB
  +-----------+
```

- Admission never calls a provider. It reads capability and quota snapshots
  gathered ahead of time, so a plan works offline and reserves nothing.
- Provider plugins translate a task to their platform's API and classify its
  failures. Scheduling, retry and accounting stay in the control plane.
- Three architectural boundaries are enforced by `depguard`.

Details: [architecture.md](docs/architecture.md).

## Documentation

| Topic | Doc |
|---|---|
| First run | [quickstart.md](docs/quickstart.md) |
| Architecture and boundaries | [architecture.md](docs/architecture.md) |
| Job file syntax | [job-specification.md](docs/job-specification.md) |
| Providers, namespaces, quotas, store, server | [configuration.md](docs/configuration.md) |
| Admission checks and reason codes | [admission.md](docs/admission.md) |
| Scoring and strategies | [scheduling.md](docs/scheduling.md) |
| Records, leases, retries, streaming, cleanup | [dispatch.md](docs/dispatch.md) |
| HTTP API | [api.md](docs/api.md) |
| Google Cloud Run Jobs | [providers/cloud-run.md](docs/providers/cloud-run.md) |
| AWS Lambda | [providers/lambda.md](docs/providers/lambda.md) |
| Writing a provider plugin | [writing-a-provider.md](docs/writing-a-provider.md) |
| Coding conventions | [style-guide.md](docs/style-guide.md) |
| Build, test, contribute | [CONTRIBUTING.md](CONTRIBUTING.md) |

## Roadmap

Not implemented yet. Tracked in [issues](https://github.com/afreidah/vagabond/issues).

- Server: API authentication, a ledger that degrades when the store is
  unreachable.
- Operations: periodic jobs, an event stream with live log streaming, blocking
  queries, disabling a provider at runtime.
- Jobs: submission hooks, variables and workload identity, execution garbage
  collection.
- Providers: a `worker` plugin; a container backend with a different execution
  model; provider pools.
- A Nomad task driver that dispatches to Vagabond.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the build and test workflow and
[style-guide.md](docs/style-guide.md) for conventions.
