# Cloud Run Jobs

Runs `container` tasks on Google Cloud Run Jobs. Returns the container's exit
code and its stdout and stderr.

Provider type: `cloud-run`. Package: `internal/providers/gcp`.

## Configuration

```hcl
provider "gcp-cloud-run" {
  type = "cloud-run"

  config {
    project                 = "my-project"
    region                  = "us-central1"
    runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
  }

  credentials {
    file = "/etc/vagabond/gcp-dispatcher.json"
  }
}
```

### `config` parameters

| Name | Type | Required | Description |
|---|---|---|---|
| `project` | string | yes | GCP project ID |
| `region` | string | yes | Cloud Run region, e.g. `us-central1` |
| `runtime_service_account` | string | yes | Identity the container executes as |

All three are rejected if present and empty.

### Credentials

A service account key in JSON. Parsed with `google.JWTConfigFromJSON`, which
accepts service account keys only — not external-account configurations, which
can name an executable to run for a token.

A key that cannot be parsed fails at startup rather than at first dispatch.

## Identities

Two service accounts, deliberately separate.

| Identity | Purpose | Roles |
|---|---|---|
| Dispatcher | What Vagabond authenticates as | `roles/run.developer`, `roles/logging.viewer` |
| Runtime | What the container executes as | none |

The dispatcher additionally needs `roles/iam.serviceAccountUser` **on the
runtime account**, granted on that account rather than at project scope, so it
cannot impersonate anything else in the project.

`run.developer` creates, executes and deletes jobs. `run.admin` would also allow
changing services, which nothing dispatching work needs.

The runtime account holds no project roles. A compromised dependency inside a
build reaches nothing.

## Capabilities

| Attribute | Value |
|---|---|
| `provider.drivers` | `container` |
| `provider.architecture` | `amd64` |
| `provider.max_cpu` | `8000` (8 vCPU) |
| `provider.max_memory` | `32768` (32 GiB) |
| `provider.max_duration` | `24h` |
| `provider.internet` | `true` |
| `provider.arbitrary_images` | `true` |

Reported from constants rather than from an API call. None of it varies by
account.

## Task translation

| Vagabond | Cloud Run |
|---|---|
| `resources.cpu` (millicores) | `limits.cpu`, rounded **up** to whole vCPU |
| `resources.memory` (MiB) | `limits.memory` as `<n>Mi` |
| `timeout` | `template.timeout` in seconds |
| `config.image` | `containers[0].image` |
| `config.command` | `containers[0].command` |
| `config.args` | `containers[0].args` |
| `env` | `containers[0].env`, sorted by name |

Defaults when a task states nothing: 1000 millicores and 2048 MiB.

CPU rounds up because a task given less than it asked for produces a mysterious
timeout rather than a bill. A job cannot be given a fraction of a core, so 250
millicores becomes 1 vCPU.

Environment variables are sorted so two submissions of the same task are
byte-identical.

`maxRetries` is set to an explicit `0`. Cloud Run's default is 3, which would
run a failing job four times and charge four times while the ledger recorded one
execution. Retries are Vagabond's decision, made with its own accounting.

## Execution lifecycle

Cloud Run has no ad-hoc run. A Job resource must exist before an Execution of it
can start.

| Method | Calls |
|---|---|
| `Submit` | `POST .../jobs?jobId=vagabond-<id>` then `POST .../jobs/vagabond-<id>:run` |
| `Status` | `GET .../jobs/vagabond-<id>/executions` |
| `Result` | `GET .../executions/<name>/tasks`, then Cloud Logging |
| `Cancel` | `DELETE .../jobs/vagabond-<id>` |
| `Release` | `DELETE .../jobs/vagabond-<id>` |
| `StreamLogs` | `POST /v2/entries:tail` |

`Release` is the same delete as `Cancel`, reached for a different reason:
nothing is running, and the Job is the resource that outlived the execution.
Dispatch calls it after every finished execution, so the sweep is a backstop
rather than the only cleanup.

Jobs are named `vagabond-<execution id>`. Every later call derives the name from
the execution ID, so the plugin remembers nothing between calls and a process
that submits and exits leaves an execution another process can still query.

If the `:run` call fails, `Submit` deletes the job it created.

### State mapping

Read from the execution's counters, not from its conditions. Condition state is
prose that has changed spelling between API versions; the counts have not.

| Cloud Run | Vagabond state |
|---|---|
| no `completionTime`, `runningCount` = 0 | `accepted` |
| no `completionTime`, `runningCount` > 0 | `running` |
| `completionTime`, `cancelledCount` > 0 | `cancelled` |
| `completionTime`, `failedCount` > 0 | `failed` |
| `completionTime`, otherwise | `succeeded` |

A job spends a minute or more provisioning before a container starts. That is
reported as `accepted`, not `running`, so a stuck image pull does not look like
a slow test suite.

Executions are listed rather than addressed directly: a job named
`vagabond-<id>` produces an execution named `vagabond-<id>-nhrzk`, and the
suffix is Google's to choose.

### Exit codes

Read from `lastAttemptResult.exitCode` on the task resource.

Absent rather than zero when a task was killed before its process ran, so a kill
is not reported as a success.

### Logs

Read from Cloud Logging `entries:list`, filtered to
`resource.type="cloud_run_job"` and the job name, ordered by timestamp. stdout
and stderr arrive interleaved.

`entries:list` scans storage in shards and returns one page per shard, so an
empty page carrying a `nextPageToken` is normal and is **not** the end of the
scan. A verified live run returned its output on the third page, after two empty
ones. The reader follows tokens until the token is empty.

Bounds, any of which marks the result truncated:

| Bound | Value |
|---|---|
| Entries | 1000 |
| Bytes | 256 KiB |
| Pages | 20 |

A failure reading logs does not fail the result. An exit code with no readable
output is still a usable answer.

### Streaming

`StreamLogs` uses `entries:tail`, a bidirectional streaming method reachable
over plain HTTP through gRPC transcoding. No gRPC dependency is needed.

Two details are not obvious from the documentation:

- The request body is a **JSON array**, because a streaming method's body is a
  stream of request messages. A bare object is rejected with
  `400 Invalid value (Object)`.
- The response is a JSON array that stays open, decoded element by element,
  rather than one object per line.

`bufferWindow` is set to 1s. Cloud Logging buffers server-side to return
entries in timestamp order; below about a second, stderr starts arriving before
the stdout that preceded it.

Streaming is several seconds behind the container. A job shorter than the
ingestion lag produces no live output at all — its entries arrive in one burst
during the linger window after the execution ends. See
[Dispatch](../dispatch.md#live-output).

Cloud Run's own platform messages, such as `Container called exit(0).`, arrive
through the same stream and are not filtered.

## Cancel destroys the result

`Cancel` deletes the Job, and Cloud Run keeps the exit code on a Task that is
deleted along with it. Fetch the result before cancelling.

## Sweeping leaked jobs

Every execution needs a Job resource that exists until something deletes it. A
process that dies between running and deleting leaves one behind, and enough of
those exhaust a per-region quota.

`Sweep(ctx, now)` deletes jobs that are:

- prefixed `vagabond-`, so nothing created by hand is at risk, and
- older than 25 hours, which is past Cloud Run's own 24-hour task maximum, so
  nothing still running can be destroyed.

A job whose creation time cannot be parsed is left alone. A delete that 404s
counts as swept. One failed delete does not abort the rest.

`Sweep` is not part of the `Provider` interface. It is a Cloud Run tax, paid in
the Cloud Run plugin.

## Free tier

Monthly, and metered on three quantities. Requests do not apply to Jobs.

| Quantity | Monthly allowance | Meter |
|---|---|---|
| vCPU-seconds | 180,000 | `cpu_seconds` |
| GiB-seconds | 360,000 | `gb_seconds` |
| Requests | 2,000,000 | not applicable to Jobs |

Confirm these against Google's current pricing page before relying on them.
Vagabond ships no defaults precisely because they move.

```hcl
provider "gcp-cloud-run" {
  type = "cloud-run"

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

Declare both. vCPU-seconds is the tighter budget at ordinary task shapes: one
vCPU against 512 MiB exhausts the vCPU allowance after 180,000 task-seconds,
having spent a quarter of the GiB allowance. A `gb_seconds` pool on its own would
keep admitting work after billing started.

Two caveats specific to this platform:

- The allowance is per billing account, shared across every Cloud Run service and
  job in it. Two `provider` blocks against one project draw on one allowance, and
  Vagabond counts them separately. Split the limits between them.
- Billable instance lifetime exceeds task duration, sometimes by a lot — see
  below. A charge settled against task duration alone under-counts what Google
  billed.

## Cost characteristics

A 4-second task occupied 116 seconds of billable instance lifetime in a measured
run. Image pull and cold start dominate short tasks. A prebaked image in
Artifact Registry in the same region is the largest available optimisation.

## Testing against a real project

The plugin's test suite runs offline against an `httptest` server. A live
round-trip test is included and skips unless credentials are supplied:

```bash
VAGABOND_GCP_CREDENTIALS=/path/to/key.json \
VAGABOND_GCP_PROJECT=my-project \
VAGABOND_GCP_RUNTIME_SA=vagabond-run@my-project.iam.gserviceaccount.com \
go test -run TestLive -v -timeout 12m ./internal/providers/gcp/
```

`VAGABOND_GCP_REGION` defaults to `us-central1`.

The test submits a container that writes to both streams and exits 3, polls to
completion, asserts the exit code and both streams, and deletes the job.
