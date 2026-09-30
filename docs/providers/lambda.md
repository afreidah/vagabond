---
title: "AWS Lambda"
seoTitle: "AWS Lambda Provider for Function Tasks"
description: "The lambda provider: configuration, credentials and IAM, the invocation event, failure classification, billed-duration settlement and quota pools."
weight: 620
---

The `lambda` provider runs `function` tasks by invoking an AWS Lambda function
the operator has already deployed. Vagabond does not create, update or delete
functions. Each execution is one synchronous invoke, and the verdict, the log
tail and the billed duration all come back from `Submit`.

| | |
|---|---|
| Provider type | `lambda` |
| Package | `internal/providers/aws` |
| Drivers | `function` |
| Execution family | synchronous: `Submit` returns a terminal state and the result |
| Optional interfaces | none |
| API | Lambda `Invoke`, through `aws-sdk-go-v2` |

## Configuration

```hcl
provider "aws-lambda" {
  type = "lambda"

  config {
    # Region the functions live in. Every invoke goes here.
    region = "us-east-1"
  }

  # Optional. Without it the SDK default credential chain is used.
  credentials {
    exec = ["aws", "configure", "export-credentials", "--format", "process"]
  }

  pool "requests" {
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}
```

The `provider`, `credentials` and `pool` block syntax is common to every
provider and is documented in [Configuration](../configuration.md).

### `config` block

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `region` | string | yes | none | AWS region, e.g. `us-east-1` |

**Validation**, at startup:

| Condition | Diagnostic |
|---|---|
| No `config` block | `Missing provider configuration`: "Provider "aws-lambda" is a lambda provider and declares no config block. It needs region." |
| `region` absent | HCL's missing-attribute diagnostic |
| An unknown attribute | HCL's unsupported-argument diagnostic |
| `region` set to `""` or whitespace | `Empty provider setting`: "Provider "aws-lambda" sets region to an empty string." |
| Credential unparseable | `Unusable credential`: "Provider "aws-lambda": ..." |

The region is not checked against AWS at startup. A function that does not
exist in it fails the first invoke with a 404.

## Credentials

| `credentials` block | Credential used |
|---|---|
| absent | The SDK default chain: environment variables, shared config and credentials files (`AWS_PROFILE` included), SSO, and the rest of the SDK's chain |
| present | `credential_process` JSON, read from the file, environment variable or command the block names |

The `credential_process` format is what
`aws configure export-credentials --format process` prints:

```json
{
  "Version": 1,
  "AccessKeyId": "AKIA...",
  "SecretAccessKey": "...",
  "SessionToken": "..."
}
```

- `Version` must be `1`, and `AccessKeyId` and `SecretAccessKey` must be
  non-empty; otherwise startup fails with "the credential is not
  credential_process JSON with Version 1, AccessKeyId and SecretAccessKey".
- `SessionToken` is optional.
- `Expiration` is ignored. When a session token expires, the next invoke is
  rejected with a 403; the `credentials` block is resolved again, the
  `exec` command re-run, and the invoke is made once more with the new
  credential. See [`credentials` block](../configuration.md#credentials-block).

### IAM

The identity needs one permission:

| Action | Resource |
|---|---|
| `lambda:InvokeFunction` | Each function (and version or alias) that jobs name |

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "lambda:InvokeFunction",
    "Resource": "arn:aws:lambda:us-east-1:123456789012:function:vagabond-*"
  }]
}
```

The function's own execution role is configured on the function and is not
Vagabond's concern.

## Capabilities

Returned from constants; no API call is made on refresh.

| Attribute | Value | Field |
|---|---|---|
| `provider.drivers` | `function` | `Drivers` |
| `provider.architecture` | `amd64,arm64` | `Architectures` |
| `provider.max_cpu` | `6000` | `MaxResources.CPU` |
| `provider.max_memory` | `10240` (10 GiB) | `MaxResources.Memory` |
| `provider.max_duration` | `15m0s` | `MaxDuration` |
| `provider.internet` | `true` | `InternetEgress` |
| `provider.private_network` | `false` | `PrivateNetwork` |
| `provider.arbitrary_images` | `false` | `ArbitraryImages` |
| `provider.estimated_cost` | `0` | `EstimatedCost` |

- Lambda allocates CPU in proportion to memory and offers no CPU setting.
  `6000` is the ceiling the 10 GiB tier implies, published so admission can
  refuse a larger request.
- `arbitrary_images = false`: a task whose `config` names an `image` is
  rejected with `image-unsupported`. See
  [Scheduling](../scheduling.md#reason-codes).
- Both architectures are advertised whatever the function was built for.
  Admission cannot see the function's architecture; the task's
  `execution.architecture` has to match the deployed function.

## Task translation

```hcl
task "test" {
  driver  = "function"
  timeout = "10m"

  config {
    function = "vagabond-runner:live"
    args     = ["go", "test", "./..."]
  }

  env {
    GOFLAGS = "-mod=mod"
  }

  resources {
    memory = 2048
  }
}
```

### Driver `config` keys

| Key | Type | Required | Description |
|---|---|---|---|
| `function` | string | yes | Function name, partial ARN or full ARN, optionally suffixed with `:<version>` or `:<alias>` |
| `args` | list of strings | no | Passed through in the event |

Other keys are ignored. Values are evaluated against the job's variables.

### The event

The function is invoked with this JSON payload:

```json
{
  "execution_id": "0199a3c4-6f2e-7b1a-9c3d-5e8f7a6b4c21",
  "args": ["go", "test", "./..."],
  "env": {"GOFLAGS": "-mod=mod", "VAGABOND_META_COMMIT": "3f9c2e1"}
}
```

| Field | Content | Omitted when |
|---|---|---|
| `execution_id` | Vagabond's execution ID | never |
| `args` | `config.args` | empty |
| `env` | The `env` block plus `VAGABOND_META_<KEY>` per job `meta` key; an `env` entry wins over a metadata variable of the same name | empty |

What the function does with the event is up to its handler. `env` is data in
the event, not the function's process environment.

**Not applied:** `resources`, `timeout`, `source`, `working_directory` and
`network` are not sent. Memory and timeout are function configuration, set
where the function is deployed. They are still checked by admission against
the capabilities above and still drive the quota reservation.

## Operations

| Method | Behaviour |
|---|---|
| `Submit` | `Invoke` with `InvocationType=RequestResponse`, `LogType=Tail`; blocks until the function returns |
| `Status` | Not supported: `internal` error wrapping `plugin.ErrUnsupported` |
| `Result` | Not supported: `internal` error wrapping `plugin.ErrUnsupported` |
| `Cancel` | Not supported: `internal` error wrapping `plugin.ErrUnsupported` |

`Submit` returns:

| Field | Value |
|---|---|
| `ProviderID` | The `function` value as written in the task |
| `State` | `failed` if the response carries `X-Amz-Function-Error`, else `succeeded` |
| `Result.ExitCode` | `1` when failed, `0` when succeeded |
| `Result.Duration` | The `REPORT` line's `Duration`, rounded up to the nanosecond; wall time of the invoke when there is no `REPORT` line |
| `Result.Logs` | The decoded `LogResult`: the last 4 KB of the invocation's log |
| `Result.LogsTruncated` | `true` when the tail is 4096 bytes or longer |
| `Result.Billed` | From the `REPORT` line; see [Quota](#quota) |

The function's response payload is not read or stored.

**Retries:** the SDK retryer is replaced with a no-op, so each `Submit` makes
exactly one HTTP request. A throttled or failed invoke is returned to dispatch,
which decides whether to try another provider.

**Idempotency:** Lambda has no idempotency key for `Invoke`. A repeated
`Submit` with the same ID invokes the function again. Dispatch mints a new ID
per attempt and never repeats a `Submit`; a function that must not run twice
can deduplicate on `execution_id`.

**No cleanup:** nothing outlives the invoke, so there is nothing to release,
stream or sweep.

## Failure classification

| Condition | Class | Retryable | Notes |
|---|---|---|---|
| Task `config` names no `function` | internal | no | Raised before any call |
| `env` block fails to evaluate | internal | no | Raised before any call |
| HTTP 429 (`TooManyRequestsException`, concurrency or rate throttling) | infrastructure | yes | `RetryAfter` 0 |
| HTTP 5xx (`ServiceException` and other service-side errors) | infrastructure | yes | |
| HTTP 404 (`ResourceNotFoundException`, no such function, version or alias) | internal | no | Wraps `ErrNotFound` |
| HTTP 401 or 403 (access denied, expired token) | internal | no | Wraps `ErrUnauthorized`: the credential is resolved again and the call made once more |
| Other HTTP 4xx (invalid parameters, payload too large) | internal | no | |
| No HTTP response: network failure, DNS, or the default chain found no credential | infrastructure | yes | |
| Function returns with `X-Amz-Function-Error` (handled or unhandled error, including the function timing out) | none | | An answer: state `failed`, exit code 1, not rerouted |

Dispatch reroutes `infrastructure` failures within the task's retry budget and
stops on `internal` ones. See [Dispatch](../dispatch.md).

## Quota

Lambda bills per request and per GB-second of configured memory over billed
duration. Both map to Vagabond meters; quota behaviour is described in
[Quotas](../quotas.md).

| Lambda quantity | Meter |
|---|---|
| Requests | `executions` |
| GB-seconds | `gb_seconds` |

**Reservation:** the task's declared `resources.memory` over its declared
`timeout`, plus one execution.

**Settlement:** Lambda ends every invocation log with a line such as:

```text
REPORT RequestId: 5e1c...	Duration: 12.34 ms	Billed Duration: 13 ms	Memory Size: 128 MB	Max Memory Used: 70 MB
```

| `REPORT` line | Settled at |
|---|---|
| Has `Billed Duration` and `Memory Size` | `Memory Size` (MB, counted as MiB) over `Billed Duration`, CPU 0 |
| Present without both | Declared `resources` over the `REPORT` `Duration` |
| Absent from the 4 KB tail | Declared `resources` over the measured invoke time |

The billed settlement replaces the reservation in full, above or below it.
Because it carries no CPU, a `cpu_seconds` pool on a `lambda` provider is
charged only at reservation and settles to 0.

**Abandoned reservations:** a process that dies during an invoke leaves its
reservation unsettled. `Status` is unsupported, so the quota reaper charges
the reserved amount. The execution record is still `pending` at that point, so
a server that resumes the dispatch records it `failed` with failure class
`infrastructure`.

**Declare the function's shape on the task.** A task without
`resources.memory` or `timeout` reserves 0 GB-seconds, so a `gb_seconds` pool
cannot refuse it at admission and is charged only at settlement. Setting
`resources.memory` to the function's memory size and `timeout` to its timeout
makes the reservation the worst case the bill can reach.

### Free tier pools

Lambda's monthly free tier is roughly 1,000,000 requests and 400,000
GB-seconds; the example below uses those figures. Vagabond ships no defaults.

> [!WARNING]
> Cloud prices and free tier quotas change. Check your provider's current
> pricing page before setting limits.

```hcl
provider "aws-lambda" {
  type = "lambda"

  config {
    region = "us-east-1"
  }

  pool "requests" {
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}
```

- The allowance is per account across all regions. Two `provider` blocks
  against one account are counted separately by Vagabond; split the limits
  between them.
- Periods reset at 00:00 UTC on the first of the month.

## Limits and edge cases

- **Duration:** 15 minutes is Lambda's ceiling and the advertised
  `max_duration`. The effective limit is the function's configured timeout,
  which Vagabond cannot see; a function that times out returns a function error
  and the task fails with exit code 1.
- **Logs:** only the last 4 KB of the invocation log is returned. Longer output
  is cut from the front; read the full log in CloudWatch Logs.
- **Payload size:** the event is subject to Lambda's synchronous request payload
  limit. An `env` or `args` too large for it fails with a 4xx, classified
  internal.
- **Blocking:** `Submit` holds a connection open for the whole invocation. A
  caller that gives up cancels the request; the function keeps running on AWS
  and is billed, since `Cancel` is unsupported.
- **Live output:** none. Output is shown when the invoke returns.
