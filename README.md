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

Vagabond is a compute broker for short-lived, stateless workloads. A job is
described once in Nomad-style HCL, with parameters, constraints, and resources;
Vagabond decides which backend can run it and which one should, runs it there,
and reports the result.

Backends are provider plugins. Each translates a task into its platform's API
and classifies that platform's failures; scheduling, retries and accounting
stay in Vagabond. Google Cloud Run Jobs and AWS Lambda are implemented, and the
plugin boundary is meant for more: IBM Code Engine, Azure Container Apps jobs,
AWS Fargate, Fly Machines, or Cloudflare Workers for edge-sized tasks.

Your own machines are a backend too. Run `vagabond agent` on one: a lightweight
process, on bare metal or in a container, that connects to the server and joins
a named pool of nodes. The server sends it workloads, which it runs on the
machine's containerd, as plain containers or as Firecracker microVMs where
stronger isolation is wanted, held to whatever CPU and memory you allow the
agent.

Every decision is explainable: `job plan` shows each backend with its score, or
the rule it failed and why. Vagabond tracks what every run uses, so a backend
the job would push past a configured quota is never chosen. Quotas can be set
per backend and shared out per namespace, which makes it as useful for keeping
work inside cloud free tiers as for capping spend.

It runs as a server in the Nomad mould: a CLI and HTTP API for submitting,
registering and dispatching jobs, agents on your nodes connecting back, and a
Postgres or CockroachDB store, so executions survive restarts and a failed
backend is routed around. It is an homage to HashiCorp Nomad, meant to feel
familiar to a Nomad user, but it does not depend on Nomad.

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
- Store outages fail closed: new work is refused with 503, running work
  finishes, and what the outage lost is repaired when the store returns.
  `GET /v1/health` reports it.
- Postgres or CockroachDB as the store.
- Agent nodes: `vagabond agent` registers a node into a pool and runs workloads
  on its containerd, held to the agent's cgroup. Beyond loopback, agents
  connect over mutual TLS and register under their certificate's name.

Backends:

| Backend | Driver | Doc |
|---|---|---|
| Google Cloud Run Jobs | `container` | [cloud-run.md](docs/providers/cloud-run.md) |
| AWS Lambda | `function` | [lambda.md](docs/providers/lambda.md) |
| Your own nodes, via `vagabond agent` and a `pool` provider | `container` | [agent.md](docs/agent.md) |

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
See [quickstart.md](docs/quickstart.md).

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
| `agent` | Run the agent on a node that executes workloads ([agent.md](docs/agent.md)) |
| `execution status <id>` | One execution's record and result |
| `execution logs <id>` | One execution's stored output |
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
  |  dispatch  reserve, submit, watch, settle,     |
  |            release; reroute on outage          |
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
| AWS Lambda | [providers/lambda.md](docs/providers/lambda.md) |
| Writing a provider plugin | [writing-a-provider.md](docs/writing-a-provider.md) |
| Coding conventions | [style-guide.md](docs/style-guide.md) || Build, test, contribute | [CONTRIBUTING.md](CONTRIBUTING.md) |

## Roadmap

Not implemented yet. Tracked in [issues](https://github.com/afreidah/vagabond/issues).

- Own nodes: how nodes rank against cloud backends, log streaming from agents,
  a Firecracker runtime.
- Server: API authentication, degraded mode that keeps dispatching through a
  store outage.
- Operations: periodic jobs, an event stream with live log streaming, blocking
  queries, disabling a provider at runtime.
- Jobs: submission hooks, variables and workload identity, execution garbage
  collection, fetching a job's source on the node.
- Providers: a `worker` plugin.
- A Nomad task driver that dispatches to Vagabond.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the build and test workflow and
[style-guide.md](docs/style-guide.md) for conventions.
