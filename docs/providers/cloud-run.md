---
title: "Google Cloud Run Jobs"
linkTitle: "Cloud Run Jobs"
seoTitle: "Google Cloud Run Jobs Provider"
description: "The cloud-run provider: configuration, credentials and IAM, task translation, API calls, failure classification, quota pools and limits."
weight: 610
---

The `cloud-run` provider runs `container` tasks as Google Cloud Run Jobs. Each
execution creates one Job, runs it once, reads the exit code from the Task
resource and the output from Cloud Logging, then deletes the Job.

| | |
|---|---|
| Provider type | `cloud-run` |
| Package | `internal/providers/gcp` |
| Drivers | `container` |
| Execution family | asynchronous: `Submit` returns `accepted`, dispatch polls `Status` |
| Optional interfaces | `LogStreamer`, `Releaser` |
| APIs | Cloud Run Admin API v2 (`run.googleapis.com`), Cloud Logging API v2 (`logging.googleapis.com`) |

The plugin calls both APIs over plain HTTPS with an OAuth2 client. It does not
use Google's generated client libraries.

## Configuration

```hcl
provider "gcp-cloud-run" {
  type = "cloud-run"

  config {
    # GCP project ID that owns the jobs and the logs.
    project = "my-project"

    # Cloud Run region. Every job is created here.
    region = "us-central1"

    # Identity the container executes as. Not the identity Vagabond
    # authenticates with; see IAM below.
    runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
  }

  # A service account key in JSON. Any one of file, env or exec.
  credentials {
    file = "/etc/vagabond/gcp-dispatcher.json"
  }

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 180000
    period = "monthly"
  }

  pool "memory" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}
```

The `provider`, `credentials` and `pool` block syntax is common to every
provider and is documented in [Configuration](../configuration.md). This page
covers the `config` block and the provider's behaviour.

### `config` block

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `project` | string | yes | none | GCP project ID. Used in every Cloud Run URL and as the Cloud Logging resource name `projects/<project>` |
| `region` | string | yes | none | Cloud Run location, e.g. `us-central1` |
| `runtime_service_account` | string | yes | none | Email of the service account set as the job's `serviceAccount` |

**Validation**, at startup, reported against the configuration file:

| Condition | Diagnostic |
|---|---|
| No `config` block | `Missing provider configuration`: "Provider "gcp-cloud-run" is a cloud-run provider and declares no config block. It needs project, region and runtime_service_account." |
| An attribute absent | HCL's missing-attribute diagnostic, against the block |
| An unknown attribute | HCL's unsupported-argument diagnostic, against the line |
| An attribute set to `""` or whitespace | `Empty provider setting`: "Provider "gcp-cloud-run" sets region to an empty string." |
| Credential missing or unparseable | `Unusable credential`: "Provider "gcp-cloud-run": ..." |

Any of these stops the server or CLI command from starting. Nothing is checked
against Google at startup: a wrong project or region surfaces as an HTTP error
on the first dispatch.

## Credentials

The credential is a service account key in JSON, as downloaded from the IAM
console. The `credentials` block may supply it from a file, an environment
variable or a command; see
[Configuration](../configuration.md#credentials-block).

```hcl
credentials {
  exec = ["vault", "kv", "get", "-field=key", "secret/vagabond/gcp"]
}
```

- Parsed with `google.JWTConfigFromJSON` under the
  `https://www.googleapis.com/auth/cloud-platform` scope. Only service account
  keys are accepted. External-account (workload identity federation)
  configurations, which can name an executable to run for a token, are
  rejected.
- Application Default Credentials and the metadata server are not used. A
  `cloud-run` provider without a `credentials` block fails with
  `no credential supplied`.
- The key is resolved once, when the registry is built. Access tokens are
  minted and refreshed from it automatically. Rotating the key needs a restart.

## IAM

Two service accounts, kept separate so the workload cannot use the
dispatcher's permissions.

| Identity | Configured as | Needs |
|---|---|---|
| Dispatcher | the `credentials` key | `roles/run.developer` and `roles/logging.viewer` on the project; `roles/iam.serviceAccountUser` on the runtime account only |
| Runtime | `runtime_service_account` | nothing from Vagabond; grant only what the workload itself needs |

Grant `roles/iam.serviceAccountUser` on the runtime service account resource,
not at project scope, so the dispatcher can attach that one identity and no
other.

The permissions the plugin exercises, by call:

| Call | Used by | Permission |
|---|---|---|
| `POST /v2/projects/{p}/locations/{r}/jobs?jobId=...` | `Submit` | `run.jobs.create`, and `iam.serviceAccounts.actAs` on the runtime account |
| `POST .../jobs/{job}:run` | `Submit` | `run.jobs.run` |
| `GET .../jobs/{job}/executions` | `Status`, `Result` | `run.executions.list` |
| `GET /v2/{execution}/tasks` | `Result` | `run.tasks.list` |
| `DELETE .../jobs/{job}` | `Cancel`, `Release`, failed `Submit` | `run.jobs.delete` |
| `POST /v2/entries:list` | `Result` | `logging.logEntries.list` |
| `POST /v2/entries:tail` | `StreamLogs` | `logging.logEntries.list` |

`roles/run.developer` covers every `run.*` permission above;
`roles/logging.viewer` covers `logging.logEntries.list`. `roles/run.admin` is
not needed.

## Capabilities

Returned from constants on every refresh. No API call is made, so a
`cloud-run` provider never becomes `provider-unhealthy` from a refresh.

| Attribute | Value | Field |
|---|---|---|
| `provider.drivers` | `container` | `Drivers` |
| `provider.architecture` | `amd64` | `Architectures` |
| `provider.max_cpu` | `8000` (8 vCPU) | `MaxResources.CPU` |
| `provider.max_memory` | `32768` (32 GiB) | `MaxResources.Memory` |
| `provider.max_duration` | `24h0m0s` | `MaxDuration` |
| `provider.internet` | `true` | `InternetEgress` |
| `provider.private_network` | `false` | `PrivateNetwork` |
| `provider.arbitrary_images` | `true` | `ArbitraryImages` |
| `provider.estimated_cost` | `0` | `EstimatedCost` |

Admission rejects a task above these limits before anything is submitted, with
the reason codes in [Scheduling](../scheduling.md#reason-codes): an `arm64`
task gets `arch-unsupported`, a `timeout` over 24h gets `duration-exceeded`, a
task requiring a private network gets `network-unsupported`.

## Task translation

```hcl
task "test" {
  driver  = "container"
  timeout = "20m"

  config {
    image   = "us-central1-docker.pkg.dev/my-project/ci/go:1.25"
    command = "sh"
    args    = ["-c", "go test ./..."]
  }

  env {
    GOFLAGS = "-mod=mod"
  }

  resources {
    cpu    = 2000
    memory = 4096
  }
}
```

### Driver `config` keys

| Key | Type | Required | Maps to |
|---|---|---|---|
| `image` | string | yes | `containers[0].image` |
| `command` | string | no | `containers[0].command`, as a one-element list |
| `args` | list of strings | no | `containers[0].args` |

Other keys in the block are ignored. Values are evaluated against the job's
variables, so `image = "ci/go:${meta.commit}"` works.

`command` is a single string. It replaces the image's entrypoint; put every
argument in `args`.

### Job spec fields

| Vagabond | Cloud Run Job | Notes |
|---|---|---|
| `resources.cpu` (millicores) | `template.template.containers[0].resources.limits.cpu` | Rounded **up** to whole vCPU, minimum 1: `250` becomes `1`, `1500` becomes `2` |
| `resources.memory` (MiB) | `...limits.memory` | `<n>Mi`, unchanged |
| `timeout` | `template.template.timeout` | Whole seconds, e.g. `1200s` |
| `env` block and `meta` | `containers[0].env` | Sorted by name |
| none | `template.taskCount` | Always `1` |
| none | `template.template.maxRetries` | Always `0` |
| `runtime_service_account` | `template.template.serviceAccount` | From provider config |

**Defaults** when the task does not state a value:

| Field | Default |
|---|---|
| `resources.cpu` | `1000` millicores (1 vCPU) |
| `resources.memory` | `2048` MiB |
| `timeout` | `24h` (`86400s`) |

A value of `0` or below is treated as unset.

**Environment:** the task's `env` block plus one `VAGABOND_META_<KEY>` variable
per job `meta` key, with an `env` entry winning over a metadata variable of the
same name. Validation refuses an `env` value that is not a string; one that
still fails to evaluate fails the submission as `internal`, never a container
with no environment.

**Retries:** `maxRetries` is always `0`. Cloud Run's default of 3 would run a
failing task up to four times and bill each run while the ledger records one
execution. Retry and reroute are decided by dispatch, per the task's `retry`
block.

**Not translated:** `source`, `working_directory`, `network` and `execution`
blocks are not sent to Cloud Run. `network` and `execution.architecture` are
enforced by admission against the capabilities above.

## Operations

Cloud Run has no ad-hoc run: a Job resource must exist before an Execution of it
can start. The plugin creates one Job per Vagabond execution, named
`vagabond-<execution id>` (45 characters, inside Cloud Run's 63-character
limit).

| Method | API calls | Returns |
|---|---|---|
| `Submit` | `POST .../jobs?jobId=vagabond-<id>`, then `POST .../jobs/vagabond-<id>:run` | `ProviderID` = job name, state `accepted` |
| `Status` | `GET .../jobs/vagabond-<id>/executions` | State from the execution's counters; `StartedAt`, `EndedAt` from `startTime`, `completionTime` |
| `Result` | `GET .../executions` again, `GET /v2/<execution>/tasks`, then `POST /v2/entries:list` (paged) | Exit code, duration, logs |
| `Cancel` | `DELETE .../jobs/vagabond-<id>` | `nil`, also when the job is already gone (404) |
| `Release` | `DELETE .../jobs/vagabond-<id>` | Same call as `Cancel` |
| `StreamLogs` | `POST /v2/entries:tail` | Writes output until the stream ends |

If the `:run` call fails, `Submit` deletes the job it just created, ignoring
the delete's own error, and returns the `:run` error.

**Stateless:** every call derives the job name from the execution ID. The
plugin keeps nothing in memory between calls, so a server that restarts, or a
different server that takes over the dispatch, can poll an execution it did not
submit.

**Idempotency:** the job name is the idempotency key. A second `Submit` with the
same ID collides on the name, Google answers `409`, and the call fails as
`internal` without starting a second run. Dispatch mints a new ID for every
attempt, so this only guards against a duplicate call.

### State mapping

Google names the execution (`vagabond-<id>-<suffix>`), so `Status` lists the
job's executions rather than addressing one. If more than one exists, the one
with the latest `startTime` is used. State is read from the counters, not from
the `conditions` array.

| Execution fields | Vagabond state |
|---|---|
| no `completionTime`, `runningCount` = 0, no `startTime` | `accepted` |
| no `completionTime`, `runningCount` > 0 or `startTime` set | `running` |
| `completionTime` set, `cancelledCount` > 0 | `cancelled` |
| `completionTime` set, `failedCount` > 0 | `failed` |
| `completionTime` set, otherwise | `succeeded` |

Image pull and instance start are reported as `accepted`. `startTime` counts as
running because `runningCount` drops to 0 when the container exits, before
`completionTime` is written.

| Lookup result | Error |
|---|---|
| Job does not exist (404) | `internal`, wraps `plugin.ErrUnknownExecution` |
| Job exists with no execution | `infrastructure`, wraps `plugin.ErrUnknownExecution` |

The second case is infrastructure because the executions listing can lag the
`:run` call for a moment.

### Exit code

Read from `lastAttemptResult.exitCode` on the execution's single task.
`Result.Duration` is the task's `completionTime` minus its `startTime`.

When the task was stopped before its process ran, `exitCode` is absent and
`Result.ExitCode` is nil rather than 0.

### Logs

`Result` reads Cloud Logging `entries:list` with:

```text
resourceNames = ["projects/<project>"]
filter        = resource.type="cloud_run_job" resource.labels.job_name="vagabond-<id>"
orderBy       = timestamp asc
pageSize      = 1000
```

stdout and stderr arrive interleaved in timestamp order, one `textPayload` per
line. Entries with an empty `textPayload` (structured platform entries) are
skipped.

`entries:list` returns one page per storage shard, so an empty page with a
`nextPageToken` is not the end. The reader follows tokens until the token is
empty or a bound is hit.

| Bound | Value | On reaching it |
|---|---|---|
| Entries | 1000 | Stop, `LogsTruncated = true` |
| Bytes | 256 KiB | Stop before the entry that would exceed it, `LogsTruncated = true` |
| Pages | 20 | Stop, `LogsTruncated = true` |

The execution record keeps the last 64 KiB of what `Result` returns.

A failed log read does not fail `Result`: the exit code is returned with no
logs.

### Live output

`StreamLogs` calls `entries:tail`, a streaming method, over HTTP through gRPC
transcoding:

- The request body is a JSON array holding one request, with the same resource
  name and filter as `entries:list` and `bufferWindow = "1s"`. A bare object is
  rejected with `400 Invalid value (Object)`.
- The response is one JSON array that stays open; each element is decoded as it
  arrives and its entries' `textPayload` values are written, one per line.
- The 1s buffer window lets Cloud Logging order entries by timestamp. Shorter
  windows deliver stderr ahead of the stdout that preceded it.
- Once the stream is open, a read or decode error ends it without an error.
- Platform lines such as `Container called exit(0).` arrive on the same stream
  and are not filtered.

Output runs several seconds behind the container. The server passes dispatch
no log writer, so `vagabond server` does not call `StreamLogs`; output is read
from `Result` once the execution ends. See [Dispatch](../dispatch.md#output).

## Cleanup

| Mechanism | When | Effect |
|---|---|---|
| `Release` | After every `Result`, by dispatch, 30s timeout | Deletes the job |
| `Release` | Every other ending, by the [provider release](../background-services.md#provider-release) loop | Deletes the job |
| `Cancel` | When the dispatching caller gives up, or `DELETE /v1/execution/{id}` for an execution no local dispatch is running | Deletes the job, stopping its execution |
| Failed `:run` | Inside `Submit` | Deletes the job |

**Cancel destroys the result.** Deleting the job deletes its executions and
tasks, and the exit code with them. `Result` after `Cancel` fails with
`ErrUnknownExecution`. Dispatch always reads `Result` before `Release`.

A job outlives its run when dispatch never reaches `Release`: a failed
`Result`, a provider that stopped answering, a server that died. The release
loop deletes it once `Status` reports the execution over and any quota
reservation is settled. Leftover jobs count against Cloud Run's per-region job
quota until then.

## Failure classification

Every error is a `*plugin.Error`. Dispatch reroutes `infrastructure` failures
within the task's retry budget and stops on `internal` ones; see
[Dispatch](../dispatch.md).

| Condition | Class | Retryable | Notes |
|---|---|---|---|
| Connection, DNS or TLS failure, or error reading the response body | infrastructure | yes | |
| HTTP 408 or 429 | infrastructure | yes | `RetryAfter` from a `Retry-After` header in seconds, else 0 |
| HTTP 5xx | infrastructure | yes | `RetryAfter` as above |
| HTTP 404 on the executions listing | internal | no | Wraps `ErrUnknownExecution` |
| HTTP 404 on the tasks call | internal | no | Wraps `ErrNotFound` |
| HTTP 404 on `Cancel` or `Release` | none | | Returns `nil` |
| HTTP 400, e.g. a CPU and memory pairing Cloud Run does not offer | internal | no | Google's `error.message` is included |
| HTTP 403, missing IAM permission or disabled API | internal | no | |
| HTTP 409, job name already exists | internal | no | |
| Response body is not the expected JSON | internal | no | |
| Task names no `image` | internal | no | Raised before any call |
| `timeout` does not parse | internal | no | Raised before any call |
| Job exists with no execution | infrastructure | yes | Wraps `ErrUnknownExecution` |
| Execution has no task | infrastructure | yes | |
| Execution reports `cancelled` | infrastructure | yes | Raised by dispatch after `Result`: no verdict was produced |
| Container exits non-zero | none | | An answer: state `failed`, `Result.ExitCode` set, not rerouted |
| Log read fails | none | | `Result` returns without logs |

Google's error message is lifted out of the JSON error body; if the body is not
in that shape, its first 500 bytes are used.

## Quota

Cloud Run bills Jobs by vCPU-seconds and GiB-seconds. Both map to Vagabond
meters; quota behaviour is described in [Quotas](../quotas.md).

| Cloud Run quantity | Meter |
|---|---|
| vCPU-seconds | `cpu_seconds` |
| GiB-seconds | `gb_seconds` |
| Requests | not applicable to Jobs |

**Charging:** the reservation is the task's declared `resources` over its
declared `timeout`. `Result` reports no bill, so settlement is the declared
`resources` over `Result.Duration` (task start to task completion).

Charges use what the task **declared**, not what the plugin sent:

- A task with no `resources` block is charged 0 on both meters, although Cloud
  Run runs it at 1 vCPU and 2 GiB.
- A task declaring `cpu = 250` is charged 0.25 vCPU-seconds per second, although
  Cloud Run bills 1 vCPU.
- A task with no `timeout` reserves 0 on both meters, so the pools cannot refuse
  it at admission.

Declare `resources` in whole vCPU and a `timeout` on every task sent here so
the ledger matches the bill.

### Free tier pools

Cloud Run's monthly free tier is roughly 180,000 vCPU-seconds and 360,000
GiB-seconds per billing account; the example below uses those figures.
Vagabond ships no defaults.

> [!WARNING]
> Cloud prices and free tier quotas change. Check your provider's current
> pricing page before setting limits.

```hcl
provider "gcp-cloud-run" {
  type = "cloud-run"

  # config and credentials as above

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 180000
    period = "monthly"
  }

  pool "memory" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}
```

- Declare both pools. At these limits, tasks declaring 1 vCPU per 2 GiB spend
  both at the same rate; with less memory per vCPU the `cpu_seconds` pool runs
  out first, with more the `gb_seconds` pool does.
- The allowance is per billing account and shared by every Cloud Run service
  and job in it. Two `provider` blocks against the same billing account are
  counted separately by Vagabond; split the limits between them.
- Periods reset at 00:00 UTC on the first of the month.

## Limits and edge cases

- **Architecture:** `amd64` only. Multi-arch images run their `amd64` variant.
- **Provisioning time:** a measured job spent about 110 seconds provisioning
  before its container started. Short tasks are dominated by image pull and
  cold start, and billable instance time exceeds the task's own duration. An
  image in Artifact Registry in the same region reduces it.
- **CPU and memory pairs:** Cloud Run constrains which memory sizes each vCPU
  count accepts. A pairing it rejects fails `Submit` with a 400, classified
  internal, and is not rerouted.
- **Sub-second timeouts** are truncated to whole seconds.
- **Exit code absent:** a task stopped before its process ran has a nil exit
  code. Dispatch judges success from the state, so it fails as `failed`.
- **Hand-run executions:** if someone runs a `vagabond-` job by hand, `Status`
  and `Result` follow the execution with the latest start time.
- **Deleted from the console:** a job deleted outside Vagabond while running
  reports `ErrUnknownExecution` on the next poll, and the quota reaper drops its
  reservation.
- **Restart during an execution:** the job keeps running. The server that
  resumes the dispatch polls it by name and collects its result; see
  [Dispatch](../dispatch.md).

## Testing against a real project

The unit tests run against an `httptest` server standing in for both APIs. A
live round trip is included and skips unless credentials are supplied:

```bash
VAGABOND_GCP_CREDENTIALS=/path/to/key.json \
VAGABOND_GCP_PROJECT=my-project \
VAGABOND_GCP_RUNTIME_SA=vagabond-run@my-project.iam.gserviceaccount.com \
go test -run TestLive -v -timeout 12m ./internal/providers/gcp/
```

| Variable | Required | Default |
|---|---|---|
| `VAGABOND_GCP_CREDENTIALS` | yes; the test skips when unset | none |
| `VAGABOND_GCP_PROJECT` | yes | none |
| `VAGABOND_GCP_RUNTIME_SA` | yes | none |
| `VAGABOND_GCP_REGION` | no | `us-central1` |

The test runs `alpine:3.20` writing one line to stdout and one to stderr and
exiting 3, polls to completion for up to 6 minutes, asserts state `failed`,
exit code 3 and both lines, and deletes the job in a cleanup that runs even
when an assertion fails.
