<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.png">
    <img alt="Vagabond" src="docs/assets/logo-light.png" width="420">
  </picture>
</p>

Vagabond is a multi-cloud compute broker for short-lived, stateless workloads.
Describe a workload once; Vagabond decides which backend can run it and which
one should.

Routing is policy-driven. A job can require a driver, an architecture, a region,
a resource envelope, or a cost ceiling. `max_cost_usd` defaults to 0, so jobs
stay on free capacity unless they opt in to paying — which makes Vagabond good
at spreading work across free tiers. Cost is one routing dimension among
several, not the premise.

Vagabond borrows Nomad's scheduling model and HCL ergonomics. It does not depend
on Nomad and does not reproduce it.

If no backend satisfies a job's constraints, Vagabond rejects it. Fallback is
the client's decision.

## Who this is for

- Running CI or batch work across more than one cloud without writing per-cloud
  submission logic.
- Keeping stateless work on free tiers deliberately, with an accounting of what
  is left.
- Anyone who wants Nomad-style job files against cloud backends that have no
  scheduler of their own.

## What it does

- Parses Nomad-style HCL job files with diagnostics that name a line and column.
- Decides which providers can run a task: 13 admission checks across policy,
  availability, capability and capacity.
- Scores the survivors and selects one.
- Explains itself. `vagabond job plan` prints every provider, its score or the
  rule it failed, and how stale the data was.
- Runs it. `vagabond job run` dispatches to the winner, streams the output where
  the provider supports it, reroutes around providers that fail to answer, and
  deletes what the execution left behind.
- Dispatches `container` tasks to Google Cloud Run Jobs and returns the exit
  code and output.

## Quickstart

```bash
make build

./vagabond job plan \
  -config examples/config.hcl \
  -meta version=1.2.3 \
  examples/go-test.vagabond.hcl
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

The example config uses in-memory providers, so this runs with no cloud account
configured. See [docs/quickstart.md](docs/quickstart.md).

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
  +-----------+   submit, watch, collect, release
  | dispatch  |   reroute on infrastructure failure
  +-----------+
```

Admission never calls a provider. It reads capability and quota snapshots
gathered by a refresh loop, which is why a plan works offline and reserves
nothing.

Provider plugins are translation plus failure classification. Scheduling, retry
and accounting stay in the control plane. Three architectural boundaries are
enforced by `depguard` rather than by convention.

Details: [docs/architecture.md](docs/architecture.md).

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

## Documentation

| Topic | Doc |
|---|---|
| First run | [Quickstart](docs/quickstart.md) |
| Architecture and boundaries | [architecture.md](docs/architecture.md) |
| Job file syntax | [job-specification.md](docs/job-specification.md) |
| Provider config, credentials, discovery | [configuration.md](docs/configuration.md) |
| Admission checks and reason codes | [admission.md](docs/admission.md) |
| Scoring and strategies | [scheduling.md](docs/scheduling.md) |
| Retries, rerouting, streaming, cleanup | [dispatch.md](docs/dispatch.md) |
| Google Cloud Run Jobs | [providers/cloud-run.md](docs/providers/cloud-run.md) |
| Writing a provider plugin | [writing-a-provider.md](docs/writing-a-provider.md) |
| Coding conventions | [style-guide.md](docs/style-guide.md) |
| Build / test / contribute | [CONTRIBUTING.md](CONTRIBUTING.md) |

## Roadmap

Not implemented. Everything above this line is.

- **Quota ledger.** Reserve on dispatch, settle on completion, so
  `provider.free_quota_percent` is measured rather than declared in config.
- **Persistence.** Execution and quota state.
- **More providers.** `function` and `worker` drivers are modeled, admitted and
  scored, but no plugin implements either. The next container backend should
  have a meaningfully different execution model, to prove the plugin boundary
  rather than add another similar API.
- **Free-tier guide.** A walkthrough of running real CI across several
  providers' free tiers, with the numbers.
- **Nomad task driver.** Dispatch to Vagabond from a Nomad job.
- **Image distribution.** Vagabond assumes the image a task names is available
  to the selected provider. Prebaking images in a registry near the provider is
  the largest known cost optimisation.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the build and test workflow, and
[docs/style-guide.md](docs/style-guide.md) for the codebase's conventions.
