---
title: "Documentation"
linkTitle: "Docs"
seoTitle: "Documentation Overview"
description: "Vagabond documentation: job files, scheduling, quotas, dispatch, configuration, deployment, agents and provider plugins."
---

Vagabond runs short-lived, stateless jobs on public cloud backends and on your
own nodes. A job is described once in HCL; Vagabond decides which backend can
run it and which one should, runs it there, and records what it used.

| Section | Page | Covers |
|---|---|---|
| Getting started | [Quickstart](quickstart.md) | A dev server, a plan and a run, with no cloud account |
| | [Architecture](architecture.md) | Components, the pipeline, packages and boundaries |
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
| | [AWS Lambda](providers/lambda.md) | The `lambda` provider |
| | [Writing a provider](writing-a-provider.md) | The plugin contract |
