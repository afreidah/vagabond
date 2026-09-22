# Architecture

Vagabond is a compute broker. A job declares what it needs; configuration
declares what backends exist; Vagabond decides which backends can run the work
and which one should.

Cost is one routing dimension, not the premise. `max_cost_usd = 0` restricts a
job to backends that charge nothing. Jobs that route on architecture, region, or
capability and ignore price use the same machinery.

## Pipeline

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
        |
        v
  +-----------+   one package per backend
  | provider  |   cloud-run
  +-----------+
```

`vagabond job plan` stops at the selection and prints it. `vagabond job run`
continues through dispatch.

## Packages

| Package | Responsibility |
|---|---|
| `internal/jobspec` | Parse HCL job files, substitute `meta.*`, emit diagnostics |
| `internal/job` | Normalized model: tasks, drivers, resources, routing |
| `internal/config` | Operator config: providers, credentials, settings |
| `internal/registry` | Construct plugins from config; maps type to implementation |
| `internal/plugin` | `Provider` interface, capability model, failure classes |
| `internal/scheduler` | Admission and ranking |
| `internal/dispatch` | Submit, watch, collect, reroute, release |
| `internal/providers/gcp` | Cloud Run Jobs plugin |
| `internal/execution` | Execution IDs, state machine, results |
| `internal/cli` | `job validate`, `job plan`, `job run` |

## Enforced boundaries

Three rules are enforced by `depguard` in `.golangci.yml`, not by convention.

| Rule | Scope | Denied import |
|---|---|---|
| Scheduler is cloud-agnostic | `internal/scheduler`, `internal/job`, `internal/quota` | `internal/providers/**` |
| Cloud SDKs are isolated | everything except `internal/providers/**` | AWS, GCP, Azure, IBM SDKs |
| DB access is isolated | everything except `internal/state` | database drivers |

If scheduling logic needs to know a candidate is Google, the capability model is
missing an attribute. Add the attribute; do not add a conditional.

## Admission inputs

Admission reads capability and quota snapshots. It does not call providers.

- A plan can be produced with no credentials configured.
- The test suite runs offline.
- A plan is only as current as its snapshots. `job plan` prints each provider's
  observation time.
- Planning reserves nothing. Planning the same job twice changes nothing.

Capability snapshots come from `Provider.Capabilities`, called by a refresh loop
rather than on the request path.

## Plugin responsibilities

A plugin does two things:

1. Translates a normalized task into its platform's API.
2. Classifies that platform's failures.

| Class | Meaning | Reroutable |
|---|---|---|
| `infrastructure` | Provider failed to answer | yes |
| `internal` | Vagabond bug; will reproduce anywhere | no |

Plugins must not retry, back off, or select alternative providers. Those
decisions require a view of every provider and of the ledger, which a plugin does
not have. A plugin that retries internally spends capacity the ledger never
records.

Two capabilities are optional, declared by implementing an interface rather than
by registering anything:

| Interface | Method | Used for |
|---|---|---|
| `plugin.LogStreamer` | `StreamLogs` | Showing output before the execution ends |
| `plugin.Releaser` | `Release` | Deleting a resource the execution left behind |

A provider that implements neither works normally; dispatch type-asserts and
skips what is absent.

See [Writing a provider](writing-a-provider.md).

## Drivers

A task declares a `driver`, which names an execution contract rather than a
product.

| Driver | Contract |
|---|---|
| `container` | Run an image to completion; return exit code and output |
| `function` | Invoke a packaged function; return its response |
| `worker` | Invoke an edge runtime with a hard CPU ceiling |

All three are parsed, admitted and scored. `cloud-run` implements `container`.

## See also

- [Quickstart](quickstart.md)
- [Job specification](job-specification.md)
- [Configuration](configuration.md)
- [Admission](admission.md)
- [Scheduling](scheduling.md)
- [Dispatch](dispatch.md)
- [Cloud Run provider](providers/cloud-run.md)
- [Writing a provider](writing-a-provider.md)
