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
- isolate provider-specific behavior behind a common plugin interface;
- use a familiar declarative HCL job format;
- support CLI, CI/CD, workflow-engine, and direct API clients equally;
- maintain execution history, provider health, quota consumption, and routing data;
- remain useful standalone, without Nomad, Temporal, or any other orchestrator.

A guiding principle is:

> The task describes what it needs. Admission decides who can run it. The
> scheduler decides where it runs. The provider plugin decides how.

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
                    +-------------------+
                    | Parse / Validate  |
                    +---------+---------+
                              |
                              v
                    +-------------------+
                    | Admission Control |
                    |-------------------|
                    | Driver support    |
                    | Capabilities      |
                    | Free-tier quota   |
                    | Cost constraints  |
                    | Provider health   |
                    +----+---------+----+
                         |         |
                      reject    admitted
                                   |
                                   v
                         +------------------+
                         |    Scheduler     |
                         |------------------|
                         | Score eligible   |
                         | providers        |
                         +--------+---------+
                                  |
                                  v
                         +------------------+
                         |    Dispatcher    |
                         +--------+---------+
                                  |
                 common provider plugin interface
             +--------------------+--------------------+
             |                    |                    |
             v                    v                    v
        IBM / Google         AWS / Oracle       Cloudflare / etc.
        container plugins    function plugins     worker plugins
```

The scheduler deliberately does not understand IBM, AWS, Cloudflare, Lambda,
containers, or provider API details. Admission produces a set of eligible
provider candidates. The scheduler scores those candidates, the dispatcher
selects the corresponding plugin, and the plugin translates the normalized task
into whatever that provider actually requires.

## Execution Drivers

A task declares a **driver**, which describes its execution contract rather
than a specific cloud provider. Providers advertise the drivers they can
satisfy.

Initial driver classes are expected to include:

- **`container`** - arbitrary container image plus command, arguments,
  environment, and resource requirements. The process runs to completion and
  its exit status is the task result.
- **`function`** - invocation of a provider-compatible function or reusable
  Vagabond function executor. Packaging may be a binary, ZIP, source bundle, or
  provider-compatible container image.
- **`worker`** - invocation of a predeployed constrained executor, typically an
  edge/Wasm runtime. Only operations explicitly implemented by that executor are
  admissible.

A provider may support more than one driver in the future. Driver names are
part of Vagabond's workload model; provider names are routing destinations.

For example:

```hcl
task "test" {
  driver = "container"

  config {
    image   = "golang:1.27"
    command = "go"
    args    = ["test", "./..."]
  }
}
```

The distinction prevents a Lambda-compatible container image, an OCI Functions
image, and an arbitrary Cloud Run Job container from being treated as equivalent
simply because all three involve containers internally.

## Admission, Scheduling, and Dispatch

Admission is responsible for determining whether each provider can currently
accept a task. It combines provider-independent policy with provider-specific
knowledge supplied by plugins.

Shared admission checks include:

- requested driver support;
- explicit provider allowlists;
- architecture and resource requirements;
- maximum allowed cost;
- free-tier quota remaining;
- provider enabled/disabled state;
- provider health.

Provider plugins may additionally reject tasks for limits that only they
understand: maximum duration, memory combinations, payload size, unsupported
operations, runtime restrictions, or other provider-specific constraints.

Once admitted, candidates are normalized for the scheduler. The scheduler only
needs information such as eligibility, quota headroom, health, reliability,
latency, and score inputs. It does not need to know how the provider executes
the task.

The initial routing strategy is `free-first`. A job with:

```hcl
max_cost_usd = 0
```

must never intentionally consume paid capacity.

If nothing can satisfy a job, Vagabond returns a structured rejection such as
`no-capacity`, `quota-exhausted`, or `unsupported`. The caller decides what to
do next.

## Provider Plugin Model

Each cloud backend is implemented as a provider plugin behind a shared Go
interface. The exact interface will evolve with the POC, but conceptually each
plugin is responsible for:

```go
type Driver interface {
    Name() string
    Capabilities(ctx context.Context) (Capabilities, error)
    Admit(ctx context.Context, task Task) (AdmissionResult, error)
    Submit(ctx context.Context, task Task) (Execution, error)
    Status(ctx context.Context, id string) (ExecutionStatus, error)
    Cancel(ctx context.Context, id string) error
}
```

The public plugin boundary should remain a conventional non-generic Go
interface. Generics, including Go 1.27 generic methods, may be useful inside
provider implementations for typed provider configuration, API request/response
translation, quota representations, and reusable adapter machinery without
leaking provider-specific types into the scheduler.

## Provider Landscape

Providers fall into three broad execution families. The list below is a roadmap,
not a promise that every provider will ship in the initial implementation.

### Container / Batch Job Providers

These are the strongest fit for general Vagabond CI workloads because Vagabond
can submit an arbitrary container image with a command and wait for its exit
status.

- **IBM Cloud Code Engine Jobs** - container job; image, command, args, env,
  resources, and timeout are translated into a Code Engine job run.
- **Google Cloud Run Jobs** - container job; no HTTP server is required and
  the container runs to completion.
- **Tencent SCF Job Image Functions** - job-oriented image execution using the
  image entrypoint/command. Worth investigating as an additional container-job
  backend if its recurring free allowance is suitable.

### Function Providers

These providers expose a function contract rather than arbitrary batch
containers. Vagabond may deploy/invoke a reusable executor or translate a
compatible function task into the provider's packaging model.

- **AWS Lambda** - ZIP/custom-runtime binary or Lambda-compatible container
  image; container images still obey the Lambda runtime contract.
- **Oracle OCI Functions** - function packaged as a container image and invoked
  through OCI Functions; not equivalent to arbitrary container execution.
- **DigitalOcean Functions** - source/function package built and executed by the
  DigitalOcean Functions platform.
- **Azure Functions** - function package, custom handler, or supported
  containerized function depending on the execution path.
- **Vercel Functions** - source-oriented serverless HTTP functions; lower
  priority.
- **Netlify Functions** - source/function-oriented HTTP/event execution; lower
  priority.

### Edge / Worker Providers

These are constrained runtimes rather than general compute. They are useful for
specific operations such as external probes, lightweight transformations, and
HTTP-oriented tasks.

- **Cloudflare Workers** - predeployed Vagabond executor written in Rust and
  compiled to Wasm with `workers-rs`. The control plane sends normalized
  supported operations to it.
- **Deno Deploy** - Deno application runtime; potentially usable through a
  predeployed executor, but low priority for Vagabond.
- **Alibaba ESA Edge Functions** - V8 edge-function environment; potentially a
  future constrained executor, but low priority.

Alibaba Function Compute is not currently a priority because a temporary trial
allowance is not useful to Vagabond's goal of aggregating recurring free
compute.

## Provider Priorities

Current implementation priority is roughly:

```text
Tier 1
  IBM Code Engine
  Google Cloud Run Jobs
  AWS Lambda
  Oracle OCI Functions
  DigitalOcean Functions

Tier 2
  Azure Functions
  Cloudflare Workers

Tier 3 / investigate
  Tencent SCF Job Image
  Deno Deploy
  Vercel Functions
  Netlify Functions
  Alibaba ESA Edge Functions
```

The first POC still only needs one provider end-to-end. Additional providers
should be added after the admission/plugin contracts are stable enough to prove
that the abstraction works across genuinely different execution models.

Nomad may be used by a client as a fallback, but it is not part of Vagabond's
core execution model.

## Implementation

The Vagabond control plane and CLI will be written in **Go**.

The Cloudflare executor will be a small **Rust** Worker. It should remain a thin
provider shim: authenticate a normalized request, validate it, perform the
supported operation, and return a normalized result. Scheduling, accounting,
provider selection, and retry decisions stay in the Go control plane.

Infrastructure provisioning is deliberately outside the Vagabond repository.
Users may provision provider resources with Terraform/Terragrunt, another IaC
tool, or manually; Vagabond should not depend on the user's infrastructure
implementation.

## Job Specification

Jobs use HCL and intentionally resemble Nomad where the concepts overlap.
Files use the convention:

```text
<name>.vagabond.hcl
```

The syntax is Nomad-inspired, not Nomad-compatible. Familiar concepts such as
`job`, `task`, `config`, `resources`, `constraint`, `affinity`, `env`, and
`retry` are retained where they make sense for a multi-cloud compute broker.

`driver` expresses the execution contract. `routing.providers` is an optional
provider allowlist/preference, not a hardcoded failover chain.

See [`examples/go-test.vagabond.hcl`](examples/go-test.vagabond.hcl).

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
vagabond job validate examples/go-test.vagabond.hcl
vagabond job plan examples/go-test.vagabond.hcl
vagabond job run -meta git_ref=<sha> examples/go-test.vagabond.hcl
vagabond job status <execution-id>
```

`job plan` should explain admission and scheduling without executing anything,
for example:

```text
IBM Code Engine       admitted       score 91
Google Cloud Run      admitted       score 86
AWS Lambda            rejected       driver container unsupported
Cloudflare Workers    rejected       driver container unsupported

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
  scheduler/                  admission, candidate scoring, dispatch policy
  state/                      CockroachDB persistence
  providers/
    aws/                      AWS Lambda plugin
    cloudflare/               Cloudflare Workers plugin
    ibm/                      IBM Code Engine plugin
    # additional provider packages added as adapters become real

examples/
  go-test.vagabond.hcl

workers/
  cloudflare-executor/        small Rust Worker used by the Cloudflare plugin
```

The directory tree is intentionally broader than the first implementation. New
provider packages should only be added when implementation work begins rather
than creating placeholders for the entire roadmap.

## POC Scope

The first POC should stay deliberately small:

1. HCL parser and schema validation, including the task driver contract.
2. `vagabond job validate` and `vagabond job plan`.
3. Admission controller and normalized admission results.
4. Provider plugin interface and capability model.
5. Scheduler that operates only on admitted candidates.
6. CockroachDB execution/quota state.
7. One real compute plugin, most likely IBM Code Engine.
8. Submit a real public-image CI task and return its result.
9. Add a provider with a meaningfully different execution model only after the
   first end-to-end path works, proving the plugin boundary rather than merely
   adding another similar API.

Image distribution is explicitly out of scope for the POC. Vagabond assumes the
image referenced by a task is available to the selected provider. Initial jobs
can use public images such as the official Go image, while private image
mirroring/staging can be addressed later if the project proves useful.
