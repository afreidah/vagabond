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

<p align="center">
  <a href="https://github.com/afreidah/vagabond/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/afreidah/vagabond/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=afreidah_vagabond"><img alt="Coverage" src="https://sonarcloud.io/api/project_badges/measure?project=afreidah_vagabond&metric=coverage"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=afreidah_vagabond"><img alt="Quality Gate" src="https://sonarcloud.io/api/project_badges/measure?project=afreidah_vagabond&metric=alert_status"></a>
</p>

<p align="center">
  <strong><a href="https://vagabond.munchbox.cc">Project website</a></strong>
</p>

Vagabond is a compute broker for short-lived, stateless jobs. Jobs are written
in Nomad-style HCL. For each task, the server rejects backends that fail the
job's constraints, resources, cost limit or remaining quota, scores the rest,
runs the task on the selected backend, and stores the result.

Backends are provider plugins: Google Cloud Run Jobs, IBM Code Engine Jobs, AWS
Lambda, and agent nodes, which are hosts running `vagabond agent` that execute
tasks on the host's containerd.

Usage is recorded for every run and checked against quotas set per backend,
optionally split per namespace. `job plan` prints every backend with its score
or the reason it was rejected.

The CLI, job format and server/agent layout follow HashiCorp Nomad. Vagabond
does not use or require Nomad.

## Features

- Nomad-style HCL job files, validated with line-and-column diagnostics.
- Admission: 13 checks across policy, availability, capability and quota.
  `max_cost_usd` defaults to 0; a job must raise it to run on a backend that
  bills.
- Ranking: admitted providers are scored and one is selected. Tiers are
  `strict` (lower tier always wins) or `weighted` (tier averaged into the
  score).
- Dispatch: submit, watch, collect output, release what the execution left on
  the provider. Reroutes when a provider stops responding. A 401 or 403 makes
  the provider reload its credentials and retry once.
- Quota ledger: budget pools per provider, in executions, GB-seconds,
  CPU-seconds or seconds, reset daily or monthly, with optional per-namespace
  shares. Reservations are atomic across processes. Settlement uses the
  platform's billed usage when it reports one.
- Registered jobs: versioned on change, dispatched by name, parameterized with
  `meta_required` and `meta_optional`. Tasks receive `VAGABOND_META_<KEY>`.
- Namespaces, declared in configuration.
- Execution history: every dispatch and attempt, with output.
- Live output: `job run`, `job dispatch` and `execution logs -f` stream output
  from Cloud Run and agent nodes while the task runs, then finish from the
  stored copy (last 64 KiB).
- Server: HTTP API over the same operations. Dispatches are leased; a server
  resumes ones a dead process left running.
- Store outages: new work is refused with 503, running work finishes, and what
  the outage lost is repaired when the store returns. `GET /v1/health` reports
  it.
- Store: Postgres or CockroachDB.
- Agent nodes: `vagabond agent` registers a node into a pool and runs tasks on
  its containerd, limited to the agent's cgroup. Off loopback, agents connect
  over mutual TLS and register under their certificate's name.

Backends:

| Backend | Driver | Doc |
|---|---|---|
| Google Cloud Run Jobs | `container` | [cloud-run.md](docs/providers/cloud-run.md) |
| IBM Code Engine Jobs | `container` | [code-engine.md](docs/providers/code-engine.md) |
| AWS Lambda | `function` | [lambda.md](docs/providers/lambda.md) |
| Agent nodes (`vagabond agent`, `pool` provider) | `container` | [agent.md](docs/agent.md) |

The `worker` driver is modeled and admitted; no plugin implements it yet.

## Server and CLI

- `vagabond server` holds the providers, stores and scheduling, and serves the
  [HTTP API](docs/api.md).
- Every command but `server`, `agent` and `job validate` is an API client,
  finding the server with `-address` or
  `$VAGABOND_ADDR` (default `http://127.0.0.1:4747`). Runners need an address,
  not cloud or database credentials.
- `vagabond server -dev` keeps every store in memory, for development and
  single-machine use.
- `job validate` is local, and job files are validated locally before they are
  sent.
- `vagabond agent` dials the server's agent address (default
  `127.0.0.1:4748`); the server calls back down that connection.

## Quickstart

```bash
make build

./vagabond server -dev -config examples/config.hcl &

./vagabond job plan -meta version=1.2.3 examples/go-test.vagabond.hcl
```

```
go-test.test (container)
ibm-code-engine     admitted  tier 0           score 90  observed 2026-09-21 07:30:59Z
gcp-cloud-run       admitted  tier 0           score 23  observed 2026-09-21 07:30:59Z
aws-lambda          rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
cloudflare-workers  rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
Tiers: strict
Selected: ibm-code-engine
Estimated cost: free
```

The example config uses fake providers, so this needs no cloud account.
See [quickstart.md](docs/quickstart.md). `make install` puts the binary on your
`PATH`; `make install BINDIR=$HOME/.local/bin` does so without sudo.

## Commands

| Command | Does |
|---|---|
| `job validate <file>` | Parse and validate |
| `job plan <file or name>` | Show where each task would run, and why |
| `job run <file>` | Run a job file without registering it |
| `job register <file>` | Store a job as a new version when it changed |
| `job dispatch <name>` | Run a registered job's current version |
| `job status [name]` | Registered jobs, or one job's versions and recent executions |
| `job stop <name>` | Stop a registered job from being dispatched |
| `node status` | Connected agent nodes |
| `provider status` | Configured providers and their quota usage |
| `agent` | Run the agent on a node that executes workloads ([agent.md](docs/agent.md)) |
| `execution status <id>` | One execution's record and result |
| `execution logs <id>` | One execution's stored output, or with `-f` its live output |
| `server [-dev]` | Serve the [HTTP API](docs/api.md) and accept agents |

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
  vagabond job ...          vagabond agent (per node)
        |  HTTP /v1                 |  gRPC over yamux, agent dials
        v                           v
  +------------------------------------------------+
  |                vagabond server                 |
  |                                                |
  |  jobspec   parse, validate, substitute meta.*  |
  |     |                                          |
  |  admission 13 checkers, 4 tiers                |
  |     |      --> rejections: reason, detail      |
  |  ranking   scorers in [0,1], score is the mean |
  |     |                                          |
  |  dispatch  reserve, submit, watch, release,    |
  |            settle; reroute on outage           |
  +------------------------------------------------+
        |                  |                   |
        v                  v                   v
   cloud providers    agent nodes         store: jobs, dispatches,
   Cloud Run, Lambda  containerd          executions, quota
                                          (Postgres / CockroachDB)
```

- The CLI holds no credentials; the server holds provider credentials and the
  store.
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
| Interactive diagrams of each part | [vagabond.munchbox.cc/diagrams](https://vagabond.munchbox.cc/diagrams/) |
| Job file syntax | [job-specification.md](docs/job-specification.md) |
| Commands and flags | [cli.md](docs/cli.md) |
| HTTP API | [api.md](docs/api.md) |
| Admission checks, reason codes, ranking | [scheduling.md](docs/scheduling.md) |
| Meters, pools, namespace shares, the ledger | [quotas.md](docs/quotas.md) |
| Records, leases, retries, reroute, release | [dispatch.md](docs/dispatch.md) |
| Every configuration block | [configuration.md](docs/configuration.md) |
| Running the server and agents | [deployment.md](docs/deployment.md) |
| Postgres, CockroachDB, migrations, schema | [database.md](docs/database.md) |
| Periodic loops in the server and agent | [background-services.md](docs/background-services.md) |
| Running workloads on your own nodes | [agent.md](docs/agent.md) |
| Google Cloud Run Jobs | [providers/cloud-run.md](docs/providers/cloud-run.md) |
| IBM Code Engine Jobs | [providers/code-engine.md](docs/providers/code-engine.md) |
| AWS Lambda | [providers/lambda.md](docs/providers/lambda.md) |
| Writing a provider plugin | [writing-a-provider.md](docs/writing-a-provider.md) |
| Coding conventions | [style-guide.md](docs/style-guide.md) |
| Build, test, contribute | [CONTRIBUTING.md](CONTRIBUTING.md) |

## Roadmap

Not implemented yet. Tracked in [issues](https://github.com/afreidah/vagabond/issues).

- Own nodes: a Firecracker runtime.
- Server: API authentication, degraded mode that keeps dispatching through a
  store outage.
- Operations: periodic jobs, an event stream, blocking queries, disabling a
  provider at runtime.
- Jobs: submission hooks, variables and workload identity, execution garbage
  collection, fetching a job's source on the node.
- Providers: a `worker` plugin.
- A Nomad task driver that dispatches to Vagabond.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the build and test workflow and
[style-guide.md](docs/style-guide.md) for conventions.
