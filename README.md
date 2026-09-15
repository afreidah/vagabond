# Vagabond

Vagabond is a standalone multi-cloud compute broker for running short-lived,
stateless workloads against available free-tier compute across multiple cloud
providers.

The core idea is simple:

> Describe a workload once. Vagabond finds an eligible provider with free
> capacity and runs it there.

Vagabond is inspired by the scheduling model and HCL ergonomics of HashiCorp
Nomad, but it is neither dependent on Nomad nor intended to reproduce it.
Clients submit provider-independent jobs; Vagabond evaluates workload
requirements against provider capabilities, free-tier quota, reliability, and
routing policy, then dispatches work to an eligible backend.

If Vagabond cannot execute a workload within the requested constraints, it
rejects the job. Fallback behavior belongs to the client. A CI system might use
a self-hosted runner, Temporal might execute a local activity, and a CLI user
might simply accept the failure.

## Goals

Vagabond should:

- aggregate fragmented free compute allowances into a useful execution pool;
- prevent accidental paid usage through explicit cost constraints and quota accounting;
- keep workloads independent of individual cloud-provider APIs;
- match jobs to providers based on capabilities rather than pretending every
  serverless platform is equivalent;
- use a familiar declarative HCL job format;
- support CLI, CI/CD, workflow-engine, and direct API clients equally;
- maintain execution history, provider health, quota consumption, and routing data;
- remain useful without Nomad, Temporal, or any other Munchbox-specific service.

A guiding principle is:

> Vagabond decides where eligible work can run. The client decides what work
> should run and what to do if Vagabond cannot run it.

## Initial Use Cases

The first target is stateless CI/CD work such as:

- Go vet, lint, vulnerability scanning, and tests;
- Terraform formatting, validation, linting, and module tests;
- Cookstyle and ChefSpec;
- Nomad HCL formatting and pack rendering;
- SBOM generation and vulnerability scanning;
- static-site and release artifact builds;
- checksum, backup, and object-storage verification;
- external HTTP, DNS, TLS, and certificate probes.

Privileged container builds, live infrastructure changes, private-network-only
workloads, and jobs requiring sensitive cluster credentials are deliberately
outside the initial scope.

## Architecture

```text
                     +------------------+
                     |      Clients     |
                     |------------------|
                     | CLI              |
                     | GitHub / Forgejo |
                     | Temporal         |
                     | Other API users  |
                     +--------+---------+
                              |
                         REST / API
                              |
                              v
                  +-----------------------+
                  |       Vagabond        |
                  |-----------------------|
                  | Job API               |
                  | HCL Parser/Validator  |
                  | Scheduler             |
                  | Capability Matching   |
                  | Quota Accounting      |
                  | Execution Tracking    |
                  +-----------+-----------+
                              |
             +----------------+----------------+
             |                |                |
             v                v                v
       IBM Code Engine    AWS Lambda    Cloudflare Workers
                                             |
                                      Rust executor shim

                              |
                              v
                     +-----------------+
                     | CockroachDB     |
                     | Canonical State |
                     +-----------------+

                              |
                        Execution Events
                              |
                              v
                         Aiven Kafka
                              |
                              v
                      Aiven OpenSearch
```

The POC does not need every component above immediately. The first useful
version should prove that Vagabond can parse a job, determine provider
eligibility, account for quota, dispatch it, and report the result.

## Initial Compute Backends

- **IBM Code Engine** — general-purpose containerized batch execution.
- **AWS Lambda** — short-lived function/container execution where compatible.
- **Cloudflare Workers** — lightweight edge/serverless execution through a small
  provider-side Worker.

Nomad may be used by a client as a fallback, but it is not part of Vagabond's
core execution model.

## Implementation

The Vagabond control plane and CLI will be written in **Go**.

The Cloudflare executor will be a small **Rust** Worker. It should remain a thin
provider shim: authenticate a normalized request, validate it, perform the
supported operation, and return a normalized result. Scheduling, accounting,
provider selection, and retry decisions stay in the Go control plane.

## Job Specification

Jobs use HCL and intentionally resemble Nomad where the concepts overlap.
Files use the convention:

```text
<name>.vagabond.hcl
```

The syntax is Nomad-inspired, not Nomad-compatible. Familiar concepts such as
`job`, `task`, `config`, `resources`, `constraint`, `affinity`, `env`, and
`retry` are retained where they make sense for a multi-cloud compute broker.

See [`examples/terraform-verify.vagabond.hcl`](examples/terraform-verify.vagabond.hcl).

## Scheduling Model

Each provider adapter advertises capabilities such as:

- container support;
- supported runtimes;
- architecture;
- CPU and memory limits;
- maximum execution duration;
- public network access;
- provider-specific execution limits.

Jobs are first filtered against hard requirements. Eligible providers are then
scored using inputs such as free quota remaining, expected monetary cost,
historical reliability, latency, and provider health.

The initial routing strategy is `free-first`.

A job with:

```hcl
max_cost_usd = 0
```

must never intentionally consume paid capacity.

If nothing can satisfy a job, Vagabond returns a structured rejection such as
`no-capacity`, `quota-exhausted`, or `unsupported`. The caller decides what to
do next.

## State and Events

**CockroachDB** is the planned canonical control-plane datastore for provider
capabilities, quota periods, quota consumption, executions, routing decisions,
results, failures, and performance history.

**Kafka** is planned as execution-event transport, not canonical state.
Normalized lifecycle events can be indexed into **OpenSearch** for operational
search and analysis without turning it into another log store.

## CLI Direction

The command surface should feel familiar to Nomad users:

```bash
vagabond job validate examples/terraform-verify.vagabond.hcl
vagabond job plan examples/terraform-verify.vagabond.hcl
vagabond job run -meta git_ref=<sha> examples/terraform-verify.vagabond.hcl
vagabond job status <execution-id>
```

`job plan` should explain eligibility and scheduling without executing anything,
for example:

```text
IBM Code Engine     eligible       score 91
AWS Lambda          unsupported    container requirement
Cloudflare Workers  unsupported    container requirement

Selected: ibm-code-engine
Estimated cost: $0.00
```

## Repository Layout

```text
cmd/
  server/                     Vagabond API/control-plane entrypoint
  vagabond/                   CLI entrypoint

internal/
  api/                        HTTP/API implementation
  config/                     daemon configuration
  events/                     Kafka/event publishing
  job/                        HCL parsing and validation
  quota/                      provider quota accounting
  scheduler/                  capability matching and provider scoring
  state/                      CockroachDB persistence
  providers/
    aws/                      AWS Lambda adapter
    cloudflare/               Cloudflare Workers adapter
    ibm/                      IBM Code Engine adapter

examples/
  terraform-verify.vagabond.hcl

workers/
  cloudflare-executor/        small Rust Worker used by the Cloudflare adapter
```

The directory tree is intentionally broader than the first implementation. New
packages should only gain code as their responsibilities become real.

## POC Scope

The first POC should stay deliberately small:

1. HCL parser and schema validation.
2. `vagabond job validate` and `vagabond job plan`.
3. Provider capability model and scheduler.
4. CockroachDB execution/quota state.
5. One real compute adapter, most likely IBM Code Engine.
6. Submit a real public-image CI task and return its result.
7. Add the second provider only after the first end-to-end path works.

Image distribution is explicitly out of scope for the POC. Vagabond assumes the
image referenced by a task is available to the selected provider. Initial jobs
can use public images such as the official Go image, while private image
mirroring/staging can be addressed later if the project proves useful.
