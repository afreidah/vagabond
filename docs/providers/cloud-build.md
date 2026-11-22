---
title: "Google Cloud Build"
linkTitle: "Cloud Build"
seoTitle: "Google Cloud Build Provider"
description: "The cloud-build provider: configuration, credentials and IAM, task translation, API calls, failure classification, quota pools and limits."
weight: 615
---

The `cloud-build` provider runs `container` tasks as Google Cloud Build builds.
Each execution is one build of one step running the task's image. The plugin
reads the exit code from the build and the output from Cloud Logging. Cloud
Build's free build-minutes are separate from Cloud Run's free tier, so a
project can use both.

| | |
|---|---|
| Provider type | `cloud-build` |
| Package | `internal/providers/gcp/cloudbuild`, on the shared `internal/providers/gcp` |
| Drivers | `container` |
| Execution family | asynchronous: `Submit` returns `accepted`, dispatch polls `Status` |
| Optional interfaces | `LogStreamer`, `Recredentialer` |
| APIs | Cloud Build API v1 (`cloudbuild.googleapis.com`), Cloud Logging API v2 (`logging.googleapis.com`) |

The plugin's client, credential handling, error classification and Cloud
Logging reads come from `internal/providers/gcp`, which the
[`cloud-run`](cloud-run.md) provider is built on too.

## Configuration

```hcl
provider "gcp-cloud-build" {
  type = "cloud-build"

  config {
    project                 = "my-project"
    region                  = "us-central1"
    runtime_service_account = "vagabond-build@my-project.iam.gserviceaccount.com"
  }

  credentials {
    file = "/secrets/gcp-dispatcher.json"
  }

  # 80% of the 2,500 free build-minutes a month.
  pool "minutes" {
    meter  = "seconds"
    limit  = 120000
    period = "monthly"
  }
}
```

The `provider`, `credentials` and `pool` block syntax is common to every
provider and is documented in [Configuration](../configuration.md).

### `config` block

The same block as `cloud-run`, with the same validation; see
[`cloud-run` configuration](cloud-run.md#config-block).

| Name | Type | Required | Description |
|---|---|---|---|
| `project` | string | yes | Project the builds run in |
| `region` | string | yes | Region the builds run in, as in `projects/{project}/locations/{region}/builds` |
| `runtime_service_account` | string | yes | Email of the service account each build runs as |

## Credentials

The same service account key as `cloud-run`, resolved and refreshed the same
way, and resolved again after a 401 or 403; see
[`cloud-run` credentials](cloud-run.md#credentials). One key can serve both
providers.

## IAM

| Identity | Configured as | Needs |
|---|---|---|
| Dispatcher | the `credentials` key | `roles/cloudbuild.builds.editor` and `roles/logging.viewer` on the project; `roles/iam.serviceAccountUser` on the runtime account only |
| Runtime | `runtime_service_account` | `roles/logging.logWriter`, since a build that runs as its own account writes its logs as that account; anything else only as the workload needs |

The calls the plugin makes:

| Call | Used by |
|---|---|
| `POST /v1/projects/{p}/locations/{r}/builds` | `Submit` |
| `GET .../builds?filter=tags="vagabond-<id>"` | `Status`, `Result`, `Cancel`, `ReadLogs`, `StreamLogs` |
| `POST .../builds/{id}:cancel` | `Cancel` |
| `POST /v2/entries:list` | `ReadLogs`, `StreamLogs` |
| `POST /v2/entries:tail` | `StreamLogs` |

## Capabilities

Returned from constants on every refresh. They describe the one machine every
build runs on, `e2-standard-2`, which is the machine the free build-minutes
count.

| Attribute | Value | Field |
|---|---|---|
| `provider.drivers` | `container` | `Drivers` |
| `provider.architecture` | `amd64` | `Architectures` |
| `provider.max_cpu` | `2000` (2 vCPU) | `MaxResources.CPU` |
| `provider.max_memory` | `8192` (8 GB) | `MaxResources.Memory` |
| `provider.max_duration` | `24h0m0s` | `MaxDuration` |
| `provider.internet` | `true` | `InternetEgress` |
| `provider.private_network` | `false` | `PrivateNetwork` |
| `provider.arbitrary_images` | `true` | `ArbitraryImages` |
| `provider.isolation` | `container` | `Isolation` |
| `provider.estimated_cost` | `0` | `EstimatedCost` |

- A task asking for more than 2 vCPU or 8 GB is refused at admission with
  `resources-exceeded`. A smaller task still runs on the whole machine.
- Google does not say each build gets a virtual machine of its own, so only
  `container` isolation is claimed. A task requiring `vm` isolation is refused
  here.

## Task translation

The driver `config` keys are those of `cloud-run`: `image`, `command` and
`args`; see [driver `config` keys](cloud-run.md#driver-config-keys).

| Build field | From |
|---|---|
| `steps[0].name` | `image` |
| `steps[0].entrypoint` | `command`, when set; otherwise the image's own |
| `steps[0].args` | `args` |
| `steps[0].env` | The `env` block and `VAGABOND_META_<KEY>` variables, as `KEY=VALUE`, sorted by name |
| `steps[0].dir` | `working_directory`, when set |
| `timeout` | The task's `timeout` in whole seconds, `24h` when unset |
| `tags` | `vagabond-<execution id>`, which every later call finds the build by |
| `serviceAccount` | `runtime_service_account` |
| `options.machineType` | `E2_STANDARD_2` |
| `options.logging` | `CLOUD_LOGGING_ONLY`, which a build running as its own account requires |

**Not applied:** `resources`, since the machine is fixed, and `network`. The
build has no source: an image that needs code fetches it itself, as the
example in [CI on free quota](https://vagabond.munchbox.cc/guides/ci-on-free-quota/)
does.

## Operations

| Method | Behaviour |
|---|---|
| `Submit` | Creates the build and returns `accepted`, with the build's id as `ProviderID` |
| `Status` | Finds the build by its tag and reports its state and its own start and finish times |
| `Result` | The step's exit code, and `finishTime` minus `startTime` as the duration |
| `Cancel` | Cancels the build; a build no listing shows, or one already gone, is not an error |
| `ReadLogs` | Reads the build's Cloud Logging entries, filtered by `resource.type="build" resource.labels.build_id="<id>"` |
| `StreamLogs` | Follows the same entries, as `cloud-run` does; see [live output](cloud-run.md#live-output) |

A build's record and output stay after it ends, so the provider has no
`Release`.

### State mapping

| Build `status` | State |
|---|---|
| `PENDING`, `QUEUED` | `accepted` |
| `WORKING` | `running` |
| `SUCCESS` | `succeeded` |
| `FAILURE`, `TIMEOUT` | `failed`, an answer |
| `INTERNAL_ERROR`, `EXPIRED` | `failed`, with `Result` returning an infrastructure error, so dispatch tries another provider |
| `CANCELLED` | `cancelled` |
| anything else | an internal error from `Status` |

A build past its `timeout` carries Google's `statusDetail` as `Result.Reason`.

## Failure classification

The classes are those of `cloud-run` for every call they share; see
[`cloud-run` failure classification](cloud-run.md#failure-classification).
What differs:

| Condition | Class | Retryable | Notes |
|---|---|---|---|
| No build carries the execution's tag | infrastructure | yes | Wraps `ErrUnknownExecution`: right after `Submit` the listing can lag the build |
| Build ended `INTERNAL_ERROR` or `EXPIRED` | infrastructure | yes | Raised by `Result`: Google ended it, the task did not |
| Build status not in the table above | internal | no | |
| Container exits non-zero | none | | An answer: state `failed`, `Result.ExitCode` set, not rerouted |

## Quota

Cloud Build bills build-minutes per second, and not for the time a build sits
queued. That maps to the `seconds` meter; quota behaviour is described in
[Quotas](../quotas.md).

**Charging:** the reservation is the task's declared `timeout`. Settlement is
`Result.Duration`, the build's execution time, which leaves queue time out as
the bill does.

### Free tier pools

Every billing account gets 2,500 build-minutes a month on `e2-standard-2` in
the default pool, 150,000 seconds. Google calls the allowance promotional and
subject to change. The example above sets 80% of it. Vagabond ships no
defaults.

> [!WARNING]
> Cloud prices and free tier quotas change. Check Cloud Build's pricing page
> before setting limits.

## Testing against a real project

The live round trip takes the same variables as
[`cloud-run`'s](cloud-run.md#testing-against-a-real-project):

```bash
VAGABOND_GCP_CREDENTIALS=/path/to/key.json \
VAGABOND_GCP_PROJECT=my-project \
VAGABOND_GCP_RUNTIME_SA=vagabond-build@my-project.iam.gserviceaccount.com \
go test -run TestLive -v -timeout 15m ./internal/providers/gcp/cloudbuild/
```

The key's account needs the dispatcher roles above, and the runtime account
`roles/logging.logWriter`. The test runs the shared round trip: `alpine:3.20`
writes one line to stdout and one to stderr, sleeps 10 seconds so a follow
attaches, and exits 3. It asserts state `failed`, exit code 3, and both lines,
read whole and streamed, and cancels the build in a cleanup.
