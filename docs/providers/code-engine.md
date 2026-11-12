---
title: "IBM Code Engine Jobs"
linkTitle: "Code Engine Jobs"
seoTitle: "IBM Code Engine Jobs Provider"
description: "The code-engine provider: configuration, credentials and IAM, task translation and sizing, API calls, failure classification, quota pools and limits."
weight: 615
---

The `code-engine` provider runs `container` tasks as IBM Code Engine job runs.
Each execution creates one job run with the image and settings inline, reads
the status from the v2 API and the exit code and output from the run's pod
through the project's Kubernetes API, then deletes the run.

| | |
|---|---|
| Provider type | `code-engine` |
| Package | `internal/providers/ibm` |
| Drivers | `container` |
| Execution family | asynchronous: `Submit` returns `accepted`, dispatch polls `Status` |
| Optional interfaces | `LogStreamer`, `Releaser`, `Recredentialer` |
| APIs | Code Engine API v2 and v1 (`api.<region>.codeengine.cloud.ibm.com`), the project's Kubernetes API (`proxy.<region>.codeengine.cloud.ibm.com`), IAM (`iam.cloud.ibm.com`) |

The plugin calls every API over plain HTTPS with an OAuth2 client. It does not
use IBM's generated SDK.

## Configuration

```hcl
provider "ibm-code-engine" {
  type = "code-engine"

  config {
    # Region the project lives in. Every call goes to this region's hosts.
    region = "us-east"

    # The project's ID, a UUID.
    project_id = "9d90b2a8-8b5d-4ed9-be66-9cae546ee5ca"

    # Optional: a registry secret already in the project, for private images.
    # image_secret = "ghcr"
  }

  # An IAM API key. Any one of file, env or exec.
  credentials {
    exec = ["vault", "kv", "get", "-field=api_key", "secret/vagabond/code-engine"]
  }

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 100000
    period = "monthly"
  }

  pool "memory" {
    meter  = "gb_seconds"
    limit  = 186264
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
| `region` | string | yes | none | Code Engine region, e.g. `us-east`. Builds the API and Kubernetes hosts |
| `project_id` | string | yes | none | The project's ID, a UUID. Used in every v2 URL and the kubeconfig call |
| `image_secret` | string | no | none | Name of a registry secret already in the project, sent as `image_secret` on every run |

**Validation**, at startup, reported against the configuration file:

| Condition | Diagnostic |
|---|---|
| No `config` block | `Missing provider configuration`: "Provider "ibm-code-engine" is a code-engine provider and declares no config block. It needs region and project_id." |
| An attribute absent | HCL's missing-attribute diagnostic, against the block |
| An unknown attribute | HCL's unsupported-argument diagnostic, against the line |
| An attribute set to `""` or whitespace | `Empty provider setting`: "Provider "ibm-code-engine" sets region to an empty string." |
| `project_id` not a UUID | `Invalid project_id` |
| `region` not one of `au-syd`, `br-sao`, `ca-tor`, `eu-de`, `eu-gb`, `jp-osa`, `jp-tok`, `us-east`, `us-south` | `Unknown Code Engine region`, a **warning**: the server starts, so a region IBM adds later still works |
| Credential empty | `Unusable credential`: "Provider "ibm-code-engine": no credential supplied." |
| The credentials source cannot be read | `Cannot resolve credentials`: "Provider "ibm-code-engine": ..." |

The errors stop `vagabond server` from starting. Nothing is checked against IBM
at startup.

> [!WARNING]
> Code Engine answers `403 forbidden` for a project called in the wrong region,
> the same answer as a missing permission. Every 403 from this provider names
> the region and project it called.

## Credentials

The credential is an IAM API key, ideally a service ID's. The `credentials`
block may supply it from a file, an environment variable or a command; see
[Configuration](../configuration.md#credentials-block). Surrounding whitespace,
such as a trailing newline from `vault kv get`, is trimmed.

- The key is exchanged at `https://iam.cloud.ibm.com/identity/token` with the
  `urn:ibm:params:oauth:grant-type:apikey` grant for an access token, which
  lasts an hour. A token is replaced a minute before it expires.
- The same token authenticates the v2 API and the Kubernetes API.
- The kubeconfig call, made once per provider, also takes a delegated refresh
  token from the same exchange with `response_type=delegated_refresh_token` and
  `receiver_client_ids=ce`.
- No call is made at startup. A key IAM refuses fails at its first use.
- **A refused key is resolved again.** IAM answering 400 (an unknown key), 401
  or 403 is classified `internal` wrapping `ErrUnauthorized`, so a call made by
  dispatch resolves the `credentials` block again, at most once a minute, and is
  made once more. A rotated key takes effect without a restart.
- A Code Engine API call answered 401 or 403 is treated the same way.

## IAM

One identity: the one the API key belongs to. The workload itself runs with no
identity in the project; see `run_service_account` below.

| Role | Scope | Covers |
|---|---|---|
| `Writer` (service role) | the Code Engine project | Creating, reading and deleting job runs; the kubeconfig call; listing pods and reading their logs |

No platform role is needed. `Manager` additionally allows changing the
project's own configuration and is not needed.

The calls the plugin makes:

| Call | Used by |
|---|---|
| `POST https://iam.cloud.ibm.com/identity/token` | every token, and the one delegated refresh token |
| `POST /v2/projects/{p}/job_runs` | `Submit` |
| `GET /v2/projects/{p}/job_runs/{run}` | `Status`, `Result` |
| `DELETE /v2/projects/{p}/job_runs/{run}` | `Cancel`, `Release` |
| `GET /api/v1/project/{p}/config` | the first pod lookup, for the namespace |
| `GET /api/v1/namespaces/{ns}/pods?labelSelector=codeengine.cloud.ibm.com/job-run={run}` | `Result`, `StreamLogs` |
| `GET .../pods/{pod}/log?container=user-container&limitBytes=262145` | `Result` |
| `GET .../pods/{pod}/log?follow=true&container=user-container` | `StreamLogs` |

Every v2 call carries `version=2026-09-04`.

## Capabilities

Returned from constants on every refresh. No API call is made, so a
`code-engine` provider never becomes `provider-unhealthy` from a refresh.

| Attribute | Value | Field |
|---|---|---|
| `provider.drivers` | `container` | `Drivers` |
| `provider.architecture` | `amd64` | `Architectures` |
| `provider.max_cpu` | `12000` (12 vCPU) | `MaxResources.CPU` |
| `provider.max_memory` | `45776` (48 GB) | `MaxResources.Memory` |
| `provider.max_duration` | `24h0m0s` | `MaxDuration` |
| `provider.internet` | `true` | `InternetEgress` |
| `provider.private_network` | `false` | `PrivateNetwork` |
| `provider.arbitrary_images` | `true` | `ArbitraryImages` |
| `provider.estimated_cost` | `0` | `EstimatedCost` |

Admission rejects a task above these limits before anything is submitted, with
the reason codes in [Scheduling](../scheduling.md#reason-codes).

## Task translation

```hcl
task "test" {
  driver  = "container"
  timeout = "20m"

  config {
    image   = "golang:1.27"
    command = "sh"
    args    = ["-c", "go test ./..."]
  }

  env {
    GOFLAGS = "-mod=mod"
  }

  resources {
    cpu    = 1000
    memory = 3814
  }
}
```

### Driver `config` keys

| Key | Type | Required | Maps to |
|---|---|---|---|
| `image` | string | yes | `image_reference` |
| `command` | string | no | `run_commands`, as a one-element list |
| `args` | list of strings | no | `run_arguments` |

The same keys as [Cloud Run](cloud-run.md#driver-config-keys), read the same
way: other keys are ignored, a value of the wrong type is ignored as if absent,
and values are evaluated against the job's variables. `command` replaces the
image's entrypoint. `command` and `args` are escaped, and carried when they
hold a line break; see [Command line](#command-line).

### Job run fields

| Vagabond | Code Engine job run | Notes |
|---|---|---|
| none | `name` | `vagabond-<execution id>`, 45 characters, inside Code Engine's 53 |
| `config.image` | `image_reference` | |
| `image_secret` | `image_secret` | From provider config; omitted when unset |
| `resources.cpu` and `resources.memory` | `scale_cpu_limit`, `scale_memory_limit` | Rounded **up** to a valid size; see below |
| The size | `scale_ephemeral_storage_limit` | The size's memory, at least `0.4G`; see below |
| `timeout` | `scale_max_execution_time` | Whole seconds, rounded **up**, at least 1 |
| `env` block and `meta` | `run_env_variables` | Each `type: literal`, escaped, sorted by name, then any carried variables |
| none | `run_mode` | Always `task` |
| none | `scale_array_spec` | Always `"0"`: one instance |
| none | `scale_retry_limit` | Always `0`, always sent |
| none | `run_service_account` | Always `none` |

**Defaults** when the task does not state a value:

| Field | Default |
|---|---|
| `resources.cpu` | `1000` millicores (1 vCPU) |
| `resources.memory` | `2048` MiB, which runs at 4G |
| `timeout` | `24h` (`86400`) |

**Retries:** `scale_retry_limit` is always `0`. Code Engine's default of 3
would run a failing task up to four times and bill each run while the ledger
records one execution. The field is sent even though it is zero, since an absent
one means the default.

**Service account:** Code Engine's default gives the workload's pod an identity
that can call Code Engine's own API in the project. `none` gives it nothing, as
Cloud Run's runtime service account holds nothing.

**Environment:** the task's `env` block plus one `VAGABOND_META_<KEY>` variable
per job `meta` key, exactly as on [Cloud Run](cloud-run.md#job-spec-fields).
One that fails to evaluate fails the submission as `internal`.

**Not translated:** `source`, `working_directory`, `network` and `execution`
blocks are not sent. `network` and `execution.architecture` are enforced by
admission against the capabilities above.

### Command line

Code Engine runs a job run as a Kubernetes pod, which changes two things before
the container starts:

- **Line breaks:** a `run_commands` or `run_arguments` item holding a newline is
  refused with `400 field_invalid ... REGEXP`. Env values may hold one.
- **Expansion:** in a command, an argument or an env value, `$(NAME)` becomes
  the value of `NAME` when the spec sets it, and `$$` becomes `$`.

The translation undoes both, so the container receives what the task wrote:

| Task value | Sent as |
|---|---|
| `command` or `args` item on one line | The item with every `$` doubled |
| `command` holding `\n` or `\r` | `$(VAGABOND_COMMAND)`, with the value in a literal variable of that name |
| `args` item `<i>` holding `\n` or `\r` | `$(VAGABOND_ARG_<i>)`, counting from 0, likewise |
| Env value: `env`, `meta`, carried | The value with every `$` doubled |

```hcl
config {
  image   = "alpine:3.20"
  command = "sh"
  args    = ["-ec", "go vet ./...\necho \"vetted $PWD\""]
}
```

is sent as:

```json
"run_commands": ["sh"],
"run_arguments": ["-ec", "$(VAGABOND_ARG_1)"],
"run_env_variables": [
  {"type": "literal", "name": "VAGABOND_ARG_1", "value": "go vet ./...\necho \"vetted $$PWD\""}
]
```

- The carried variables are visible to the workload in its environment.
- A task whose `env` sets a carried name, while that item is carried, fails as
  `internal`. When the item is on one line, the name is the task's own.

### Sizes

Code Engine runs only fixed vCPU and memory pairs, and its `G` is decimal
(10^9 bytes): `0.25G` reaches the pod as a `250M` limit, and `Gi` is rejected.
A task gets the first pair, in order of CPU and then memory, with at least its
declared CPU and at least its declared memory in bytes (MiB times 2^20 against
G times 10^9).

| vCPU | Memory options |
|---|---|
| 0.125 | 0.25G, 0.5G, 1G |
| 0.25 | 0.5G, 1G, 2G |
| 0.5 | 1G, 2G, 4G |
| 1 | 2G, 4G, 8G |
| 2 | 4G, 8G, 16G |
| 4 | 8G, 16G, 32G |
| 6 | 12G, 24G, 48G |
| 8 | 16G, 32G |
| 10 | 20G, 40G |
| 12 | 24G, 48G |

| Declared | Runs at |
|---|---|
| nothing | 1 vCPU, 4G |
| `cpu = 100`, `memory = 128` | 0.125, 0.25G |
| `cpu = 1000`, `memory = 3814` | 1, 4G |
| `cpu = 1000`, `memory = 4096` | 1, 8G (4096 MiB is 4.29 GB) |
| `cpu = 250`, `memory = 8192` | 2, 16G (no smaller CPU offers 8.59 GB) |
| `cpu = 1500`, `memory = 1024` | 2, 4G |

Each G size in MiB, rounded down so a task declaring it lands on that size:

| G | MiB | G | MiB |
|---|---|---|---|
| 0.25G | 238 | 12G | 11444 |
| 0.5G | 476 | 16G | 15258 |
| 1G | 953 | 20G | 19073 |
| 2G | 1907 | 24G | 22888 |
| 4G | 3814 | 32G | 30517 |
| 8G | 7629 | 40G | 38146 |
| | | 48G | 45776 |

A task above 12 vCPU or 48G fails as `internal` before any call; admission
rejects it first through the capabilities.

**Disk:** a run's ephemeral storage is separate from its memory, and Code
Engine caps it at the larger of `0.4G` and the size's memory. Without
`scale_ephemeral_storage_limit` a run gets `0.4G`, which a Go build's module and
build caches fill in seconds. The pod is then evicted and the container killed
with exit 137. Code Engine bills vCPU and memory, not disk, so every run is
sent the most its size allows: `4G` at 2 vCPU / 4G, `0.4G` at 0.125 / 0.25G.
A task needing more disk declares more memory.

## Operations

| Method | API calls | Returns |
|---|---|---|
| `Submit` | `POST .../job_runs` | `ProviderID` = run name, state `accepted` |
| `Status` | `GET .../job_runs/{run}` | State from the run's `status`; `StartedAt`, `EndedAt` from the run's `start_time`, `completion_time` |
| `Result` | `GET .../job_runs/{run}`, the pod lookup, the pod's log | Exit code, reason, duration, logs |
| `Cancel` | `DELETE .../job_runs/{run}` | `nil`, also when the run is already gone (404) |
| `Release` | `DELETE .../job_runs/{run}` | Same call as `Cancel` |
| `StreamLogs` | the pod lookup until the container starts, then the followed log | Writes output until the container exits |

**Stateless:** every call derives the run name from the execution ID. The
plugin keeps nothing between calls but the project's namespace, so a server
that restarts, or a different server that takes over the dispatch, can poll an
execution it did not submit.

**Idempotency:** the run name is the idempotency key. A second `Submit` with the
same ID answers `409 resource_already_exists`, which fails as `internal`
without starting a second run.

**Namespace:** the Kubernetes calls need the project's namespace, which the v2
API does not report. It is read from `current-context` in the kubeconfig
(`GET /api/v1/project/{p}/config`, JSON) on the first pod lookup and kept for
the provider's lifetime. A failed read is not kept.

### State mapping

| Run `status` | Vagabond state |
|---|---|
| `pending` | `accepted`, including a run Code Engine holds back while the project is over its limits |
| `running` | `running` |
| `completed` | `succeeded` |
| `failed` | `failed` |
| anything else | an `internal` error naming the status |

`Status` uses the run's own times rather than its instance's. The instance
finishes a few seconds before the run reports `completed` or `failed`, and an
end time is not reported on a run still `running`.

| Lookup result | Error |
|---|---|
| Run does not exist (404) | `internal`, wraps `plugin.ErrUnknownExecution` |

### Exit code

Read from the pod: `status.containerStatuses[name=user-container].state.terminated.exitCode`.
The pod is found by its `codeengine.cloud.ibm.com/job-run` label; with retries
off there is one, named `<run>-0-0`, and if there are ever more the newest by
`creationTimestamp` is used.

When the pod is gone, the exit code comes from the run's own status: 0 for
`completed`, N from a `last_failure_reason` of `ContainerExitedCode<N>`, and
nil for any other reason, such as `OOMKilled` or `ExceededEphemeralStorage`.

`Result.Duration` is the instance's `finished_at` minus its `started_at`. An
evicted instance reports `finished_at` as `1970-01-01T00:00:00Z`, so when the
instance's times do not give a positive duration, the run's `completion_time`
minus its `start_time` is used, and zero when those fail too.

A pod lookup that fails fails `Result`, classified, so a transient outage is
not recorded as a missing exit code.

### Reason

`Result.Reason` is why the platform ended the task, first match wins:

| Source | Example |
|---|---|
| The pod's `status.reason` and `status.message`, set when the kubelet ended the pod | `Evicted: Pod ephemeral local storage usage exceeds the total limit of containers 400M.` |
| The task container's `state.terminated.reason` and `message` | `OOMKilled` |
| With the pod gone, the instance's `last_failure_reason` | `ExceededEphemeralStorage` |

`Completed`, `Error` and `ContainerExitedCode<N>` say no more than the exit code
and are not a reason. A run that exited on its own has none.

### Logs

`Result` reads the pod's log:

```text
GET /api/v1/namespaces/{ns}/pods/{pod}/log?container=user-container&limitBytes=262145
```

stdout and stderr arrive interleaved in one log. Lines keep their order within
each stream, but not always across the two: a stderr line written just after a
stdout line can come first. The bound is 256 KiB
from the start of the output; a log longer than that is cut there with
`LogsTruncated = true`. The execution record keeps the last 64 KiB of what
`Result` returns.

The pod and its log stay readable after the container exits, for at least five
minutes in testing, until the run is deleted. A failed log read does not fail
`Result`: the exit code is returned with no logs.

### Live output

`StreamLogs`:

- Polls the pod lookup every 2 seconds until `user-container` is running or
  has exited. The log cannot be opened before; asking answers 400.
- Opens `GET .../pods/{pod}/log?follow=true&container=user-container` and
  copies bytes to the caller as they arrive, until the container exits.
- **Resumes from an offset.** The followed log starts at the container's first
  line, so an `offset` above zero is skipped from the stream rather than
  refused. A follower that reconnects gets only what it does not have.
- A container that never starts, such as one whose image cannot be pulled, is
  waited on until the server ends the stream, 5 seconds after the execution
  ends.
- Once the stream is open, a read error ends it without an error. A refusal to
  open it is classified as for any call, and the server logs it; the execution
  is unaffected.

`StreamLogs` is called for `GET /v1/execution/{id}/logs?follow=true`
([API](../api.md#read-execution-logs)); the stored output is still read from
`Result` once the execution ends. See [Dispatch](../dispatch.md#output).

## Cleanup

| Mechanism | When | Effect |
|---|---|---|
| `Release` | After every `Result`, by dispatch, 30s timeout | Deletes the run and its pod |
| `Release` | Every other ending, by the [provider release](../background-services.md#provider-release) loop | Deletes the run and its pod |
| `Cancel` | When the dispatching caller gives up, or `DELETE /v1/execution/{id}` for an execution no local dispatch is running | Deletes the run, stopping it |

**Cancel destroys the result.** Code Engine has no way to stop a run except
deleting it, and deleting it removes its pod, with the exit code and output.
`Result` after `Cancel` fails with `ErrUnknownExecution`. Dispatch always reads
`Result` before `Release`.

A project holds 100 job runs before old ones must be removed, so leftover runs
eventually stop new submissions. The release loop deletes a run dispatch never
released once its record has gone 10 minutes unchanged, `Status` reports the
execution over and any quota reservation is settled. Code Engine itself deletes
completed runs after about a week.

## Failure classification

Every error is a `*plugin.Error`. Dispatch reroutes `infrastructure` failures
within the task's retry budget and stops on `internal` ones; see
[Dispatch](../dispatch.md).

| Condition | Class | Retryable | Notes |
|---|---|---|---|
| Connection, DNS or TLS failure, or error reading the response body | infrastructure | yes | |
| IAM refuses the key: 400, 401 or 403 | internal | no | Wraps `ErrUnauthorized`: on a dispatch call the key is resolved again and the call made once more |
| IAM answers any other failure status | as for any call | | |
| IAM answers 200 without a token | internal | no | |
| HTTP 408 or 429 | infrastructure | yes | `RetryAfter` from a `Retry-After` header in seconds, else 0 |
| HTTP 5xx | infrastructure | yes | `RetryAfter` as above |
| Error code `quota_exceeded` or `requests_too_many`, whatever the status | infrastructure | yes | `RetryAfter` as above |
| HTTP 404 on the run | internal | no | Wraps `ErrUnknownExecution` |
| HTTP 404 on `Cancel` or `Release` | none | | Returns `nil` |
| HTTP 400, e.g. `field_invalid` or `spec_too_large` | internal | no | Code Engine's code and message are included |
| HTTP 401 or 403 | internal | no | Wraps `ErrUnauthorized`; the message names the region and project |
| HTTP 409, run name already exists | internal | no | |
| Response body is not the expected JSON | internal | no | |
| Run reports a status the plugin does not know | internal | no | |
| Kubeconfig names no namespace | internal | no | |
| Task names no `image`, `timeout` does not parse or is over 24h, resources above the largest size | internal | no | Raised before any call |
| Container exits non-zero | none | | An answer: state `failed`, `Result.ExitCode` set, not rerouted |
| Log read fails | none | | `Result` returns without logs |
| Followed log cannot be opened | as for any call | | The server logs it and ends the response |

Error messages are lifted from whichever shape the answer takes: the v2 API's
`errors` list, the v1 API's single `code` and `message`, or a Kubernetes
`Status`. Anything else is shown as its first 500 bytes.

## Quota

Code Engine bills job runs by vCPU-seconds and GB-seconds while an instance
runs, image pull included. Both map to Vagabond meters; quota behaviour is
described in [Quotas](../quotas.md).

| Code Engine quantity | Meter |
|---|---|
| vCPU-seconds | `cpu_seconds` |
| GB-seconds | `gb_seconds` |
| HTTP requests | not applicable to job runs |

**Charging:** the reservation is the task's declared `resources` over its
declared `timeout`. `Result` reports no bill, so settlement is the declared
`resources` over `Result.Duration`.

Charges use what the task **declared**, not the size it ran at:

- A task with no `resources` block is charged 0 on both meters, although it runs
  at 1 vCPU and 4G.
- A task declaring `memory = 2048` is charged 2 GiB-seconds per second, although
  it runs at 4G.
- A task with no `timeout` reserves 0 on both meters, so the pools cannot refuse
  it at admission.

Declare `cpu` as one of the vCPU steps and `memory` as one of the MiB values in
[Sizes](#sizes), and a `timeout` on every task, so the ledger matches the bill.

**Units:** Vagabond's `gb_seconds` meter counts GiB-seconds (1024 MiB), and
Code Engine bills decimal GB-seconds. One GB is 0.931 GiB, so a limit written
from IBM's figure is multiplied by 0.931.

### Free tier pools

Code Engine's monthly free allowance is 100,000 vCPU-seconds and 200,000
GB-seconds per account, which is 186,264 GiB-seconds; the example above uses
those figures. Vagabond ships no defaults.

> [!WARNING]
> Cloud prices and free tier quotas change. Check your provider's current
> pricing page before setting limits.

- Declare both pools. At these limits a task spends both at the same rate at
  1.86 GiB per vCPU, which is the 1 vCPU / 2G size declared as
  `memory = 1907`; with less memory per vCPU the `cpu_seconds` pool runs out
  first, with more the `gb_seconds` pool does.
- The allowance is per account and shared by every Code Engine app, job and
  build in it, across projects and regions. Two `provider` blocks against the
  same account are counted separately by Vagabond; split the limits between
  them.
- The free allowance needs a Pay-As-You-Go or Subscription account.
- Periods reset at 00:00 UTC on the first of the month.

## Limits and edge cases

- **Architecture:** `amd64` only; runs report `x86_64`. Multi-arch images run
  their `amd64` variant.
- **Region:** a project addressed through another region's hosts answers 403
  on every call. The message names the region and project called.
- **Fixed sizes:** a task runs at the smallest size holding what it declared,
  and is billed at that size.
- **Disk follows memory:** a run gets as much ephemeral storage as its size's
  memory, at least `0.4G`. Past that the pod is evicted, exit 137 with reason
  `Evicted: Pod ephemeral local storage usage exceeds ...`. More disk means
  declaring more memory; see [Sizes](#sizes).
- **Sub-second timeouts** are rounded up to whole seconds.
- **Spec size:** Code Engine refuses a job run spec over 10 KiB with
  `spec_too_large`, classified internal. A task's `env`, `meta` and `args`
  count toward it, each `$` twice once [escaped](#command-line).
- **Project limits:** a run Code Engine holds back while the project is over
  its instance or CPU limits reports `pending`, which is `accepted`, until it
  starts.
- **Private images:** need `image_secret`, naming a registry secret created in
  the project beforehand. Without it the run is created and then fails.
- **Exit code absent:** with the pod gone and a failure reason other than
  `ContainerExitedCode<N>`, the exit code is nil. Dispatch judges success from
  the state, so it fails as `failed`.
- **Deleted outside Vagabond:** a run deleted from the console or CLI while
  running reports `ErrUnknownExecution` on the next poll, and the quota reaper
  drops its reservation.
- **Restart during an execution:** the run keeps going. The server that resumes
  the dispatch polls it by name and collects its result; see
  [Dispatch](../dispatch.md).

## Testing against a real project

The unit tests run against an `httptest` server standing in for IAM, the v2 API
and the Kubernetes API. Two live tests are included and skip unless a key is
supplied:

```bash
VAGABOND_IBM_API_KEY=$(vault kv get -field=api_key secret/vagabond/code-engine) \
VAGABOND_IBM_PROJECT=$(vault kv get -field=project_id secret/vagabond/code-engine) \
VAGABOND_IBM_REGION=$(vault kv get -field=region secret/vagabond/code-engine) \
go test -run TestLive -v -timeout 10m ./internal/providers/ibm/
```

| Variable | Required | Default |
|---|---|---|
| `VAGABOND_IBM_API_KEY` | yes; the test skips when unset | none |
| `VAGABOND_IBM_PROJECT` | yes | none |
| `VAGABOND_IBM_REGION` | yes, since a wrong one answers 403 | none |

`TestLiveRoundTrip` runs `alpine:3.20` at 0.125 vCPU / 0.5G, writing one line
to stdout and one to stderr, sleeping 10 seconds and exiting 3. It follows the
output while the run is going, polls to completion for up to 6 minutes, and
asserts state `failed`, exit code 3, and both lines in both the stream and
`Result`. It then releases the run, asserts the run is gone, and deletes it
again in a cleanup that runs even when an assertion fails.

`TestLiveMultiLine` runs a multi-line `sh -ec` script with a one-line argument
and an env value holding `$(UNSET)` and `$$`, and asserts that the output is
exactly the script's, `$` for `$`.
