# AWS Lambda

Runs `function` tasks by invoking a Lambda function the operator deployed.
Vagabond does not create or update functions.

Provider type: `lambda`. Package: `internal/providers/aws`.

## Configuration

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

### `config` parameters

| Name | Type | Required | Description |
|---|---|---|---|
| `region` | string | yes | AWS region, e.g. `us-east-1` |

### Credentials

- No `credentials` block: the SDK default chain, i.e. environment, shared
  config, SSO, and `AWS_PROFILE`.
- A `credentials` block: `credential_process` JSON (`Version` 1, `AccessKeyId`,
  `SecretAccessKey`, optional `SessionToken`). The expiry is ignored.

```hcl
credentials {
  exec = ["aws", "configure", "export-credentials", "--format", "process"]
}
```

The identity needs `lambda:InvokeFunction` on the functions jobs name.

## Capabilities

| Field | Value |
|---|---|
| Drivers | `function` |
| Architectures | `amd64`, `arm64` |
| Max CPU | 6000 millicores (derived from memory; not selectable) |
| Max memory | 10240 MiB |
| Max duration | 15 minutes |
| Arbitrary images | no |

## Task translation

```hcl
task "test" {
  driver = "function"

  config {
    function = "vagabond-runner"
    args     = ["go", "test", "./..."]
  }

  env {
    GOFLAGS = "-mod=mod"
  }
}
```

| Config key | Required | Description |
|---|---|---|
| `function` | yes | Function name, partial ARN or ARN, optionally with a version or alias |
| `args` | no | List of strings, passed through in the event |

The function receives:

```json
{"execution_id": "<uuid>", "args": ["go", "test", "./..."], "env": {"GOFLAGS": "-mod=mod"}}
```

`resources` and `timeout` are not applied. Memory and timeout are function
configuration, set where the function is deployed.

## Execution lifecycle

- One synchronous invoke (`RequestResponse`, `LogType=Tail`). Nothing to poll,
  fetch, cancel or clean up.
- The SDK's retries are disabled. Vagabond owns retry.
- A 429 or 5xx is an infrastructure failure and may be rerouted. A 404 (no such
  function) is internal.

### Exit codes

Lambda has none. A response with `X-Amz-Function-Error` is exit code 1 and a
failed task; otherwise 0.

### Logs

The last 4 KB of the invocation's log, as Lambda returns it. Marked truncated
at 4 KB.

## Quota

Settled at what AWS billed, from the log's `REPORT` line: `Billed Duration` at
`Memory Size`. This supersedes the reservation, which is derived from what the
task declared.

A tail without a `REPORT` line settles from the declared shape over the
reported or measured duration.

A reservation left by a killed run cannot be asked about, since Lambda has no
status for an invoke. The reaper charges it at the reserved amount.
