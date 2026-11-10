---
title: "Documentation"
linkTitle: "Docs"
seoTitle: "Documentation Overview"
description: "Vagabond documentation: job files, scheduling, quotas, dispatch, configuration, deployment, agents and provider plugins."
---

Vagabond is a compute broker for short-lived, stateless jobs on Cloud Run Jobs,
AWS Lambda and agent nodes. Jobs are written in Nomad-style HCL. For each task,
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
| Own nodes | [Agent](agent.md) | Pools of your own nodes running containerd |
| Providers | [Cloud Run Jobs](providers/cloud-run.md) | The `cloud-run` provider |
| | [Code Engine Jobs](providers/code-engine.md) | The `code-engine` provider |
| | [AWS Lambda](providers/lambda.md) | The `lambda` provider |
| | [Writing a provider](writing-a-provider.md) | The plugin contract |
| Contributing | [Style guide](style-guide.md) | Coding conventions |
| | [Contributing](../CONTRIBUTING.md) | Build, test and pull request workflow |
