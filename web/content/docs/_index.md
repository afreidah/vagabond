---
title: "Documentation"
linkTitle: "Docs"
seoTitle: "Documentation Overview"
description: "Vagabond documentation: job files, scheduling, quotas, dispatch, configuration, deployment, agents and provider plugins."
---

Vagabond is a compute broker for short-lived, stateless jobs on Cloud Run Jobs,
Cloud Build, Code Engine Jobs, AWS Lambda, OCI Functions and agent nodes. Jobs
are written in Nomad-style HCL. For each task,
the server rejects backends that fail the job's constraints, resources, cost
limit or remaining quota, scores the rest, runs the task on the selected
backend, and records what it used.

| Section | Page | Covers |
|---|---|---|
| Getting started | [Quickstart](quickstart.md) | A dev server, a plan and a run, with no cloud account |
| | [Architecture](architecture.md) | Components, the pipeline, packages and boundaries |
| | [Diagrams](../diagrams/) | Interactive diagrams of each part, with notes on every component |
| Using Vagabond | [Job specification](job-specification.md) | Every block and attribute of a job file |
| | [CLI](cli.md) | Every command and flag |
| | [HTTP API](api.md) | Every route, request and response |
| Scheduling and dispatch | [Scheduling](scheduling.md) | Admission checks, reason codes, ranking |
| | [Quotas](quotas.md) | Meters, pools, namespace shares, the ledger |
| | [Dispatch](dispatch.md) | Execution records, leases, retries, reroute, release |
| Operating | [Configuration](configuration.md) | Every block of the server configuration |
| | [Deployment](deployment.md) | Running the server and agents |
| | [Database](database.md) | Postgres and CockroachDB, migrations, schema |
| | [Background services](background-services.md) | Every periodic loop the server and agent run |
| Own nodes | [Agent](agent.md) | The agent, its pools, and how work is placed on their nodes |
| | [containerd](containerd.md) | The `containerd` provider: workloads in containers on the node's containerd |
| | [Firecracker](firecracker.md) | The `firecracker` provider: workloads in Firecracker microVMs, through vagabond-firecracker |
| Providers | [Cloud Run Jobs](providers/cloud-run.md) | The `cloud-run` provider |
| | [Cloud Build](providers/cloud-build.md) | The `cloud-build` provider |
| | [Code Engine Jobs](providers/code-engine.md) | The `code-engine` provider |
| | [AWS Lambda](providers/lambda.md) | The `lambda` provider |
| | [OCI Functions](providers/oci-functions.md) | The `oci-functions` provider |
| | [Writing a provider](writing-a-provider.md) | The plugin contract |
| Contributing | [Style guide](style-guide.md) | Coding conventions |
| | [Contributing](../CONTRIBUTING.md) | Build, test and pull request workflow |
