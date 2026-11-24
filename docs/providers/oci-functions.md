---
title: "OCI Functions"
seoTitle: "OCI Functions Provider for Function Tasks"
description: "The oci-functions provider: configuration, the API signing key and IAM policy, the event and reply, failure classification, settlement at the function's configured memory, and quota pools."
weight: 625
---

The `oci-functions` provider runs `function` tasks by invoking an OCI Functions
function the operator has already deployed. Vagabond does not create, update
or delete functions. Each execution is one lookup of the function and one
synchronous invoke: the function's reply, carrying the task's exit code and
output, comes back from `Submit`.

| | |
|---|---|
| Provider type | `oci-functions` |
| Package | `internal/providers/oci` |
| Drivers | `function` |
| Execution family | synchronous: `Submit` returns a terminal state and the result |
| Optional interfaces | Recredentialer |
| API | Functions `GetFunction` and `InvokeFunction`, API version `20260325`, over REST, signed with `oci-go-sdk`'s request signer |

## Configuration

```hcl
provider "oci-functions" {
  type = "oci-functions"

  config {
    # Region the functions live in. Lookups go to its Functions API.
    region = "us-ashburn-1"

    # The API signing key: its tenancy, its user, and its fingerprint.
    tenancy     = "ocid1.tenancy.oc1..aaaaaaaa..."
    user        = "ocid1.user.oc1..aaaaaaaa..."
    fingerprint = "12:34:56:78:9a:bc:de:f0:12:34:56:78:9a:bc:de:f0"
  }

  # Required. The PEM private key the requests are signed with.
  credentials {
    file = "/etc/vagabond/oci-api-key.pem"
  }

  pool "invocations" {
    meter  = "executions"
    limit  = 2000000
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
| `region` | string | yes | none | OCI region identifier, e.g. `us-ashburn-1` |
| `tenancy` | string | yes | none | OCID of the tenancy the API key's user belongs to |
| `user` | string | yes | none | OCID of the user the API key was added to |
| `fingerprint` | string | yes | none | The API key's fingerprint, as the console shows it after the key is added |

All four go into every request's signature. The region also picks the
Functions API host, `https://functions.<region>.oci.<realm domain>`, the
domain taken from the realm the region belongs to (`oraclecloud.com` for the
commercial realm).

**Validation**, at startup:

| Condition | Diagnostic |
|---|---|
| No `config` block | `Missing provider configuration`: "Provider "oci-functions" is a oci-functions provider and declares no config block. It needs region, tenancy, user and fingerprint." |
| An attribute absent | HCL's missing-attribute diagnostic |
| An unknown attribute | HCL's unsupported-argument diagnostic |
| An attribute set to `""` or whitespace | `Empty provider setting`: "Provider "oci-functions" sets user to an empty string." |
| No credential, or one that is not a PEM RSA private key | `Unusable credential`: "Provider "oci-functions": no credential supplied." or "...: parsing the private key: ..." |
| The credentials source cannot be read | `Cannot resolve credentials`: "Provider "oci-functions": ..." |

Nothing is checked against OCI at startup. A wrong OCID or fingerprint fails
the first lookup with a 401; a region the function is not in fails it with a
404.

## Credentials

The credential is the private half of an API signing key added to the user
`config.user` names, as PEM. Both PKCS#1 (`BEGIN RSA PRIVATE KEY`) and PKCS#8
(`BEGIN PRIVATE KEY`) are read. A key protected by a passphrase is not
supported.

```shell
openssl genrsa -out oci-api-key.pem 2048
openssl rsa -pubout -in oci-api-key.pem -out oci-api-key-public.pem
```

Add the public key to the user under **API keys** in the console; the console
then shows the fingerprint for `config.fingerprint`.

When OCI answers a lookup or invoke with 401 or 403, the `credentials` block is
resolved again and the call made once more with the new key. See
[`credentials` block](../configuration.md#credentials-block).

### IAM policy

The user's group needs two permissions in the compartment holding the
functions:

| Statement | Permission | Call |
|---|---|---|
| `read fn-function` | `FN_FUNCTION_READ` | `GetFunction` |
| `use fn-invocation` | `FN_INVOCATION` | `InvokeFunction` |

```text
Allow group vagabond to read fn-function in compartment ci
Allow group vagabond to use fn-invocation in compartment ci
```

The function's own access to other OCI services is its resource principal's,
configured where it is deployed, and is not Vagabond's concern.

## Capabilities

Returned from constants; no API call is made on refresh.

| Attribute | Value | Field |
|---|---|---|
| `provider.drivers` | `function` | `Drivers` |
| `provider.architecture` | `amd64,arm64` | `Architectures` |
| `provider.max_cpu` | not published | `MaxResources.CPU` |
| `provider.max_memory` | `3072` | `MaxResources.Memory` |
| `provider.max_duration` | `5m0s` | `MaxDuration` |
| `provider.internet` | `true` | `InternetEgress` |
| `provider.private_network` | `false` | `PrivateNetwork` |
| `provider.arbitrary_images` | `false` | `ArbitraryImages` |
| `provider.isolation` | not published | `Isolation` |
| `provider.estimated_cost` | `0` | `EstimatedCost` |

- **CPU:** Oracle publishes no CPU figure for a function, so no CPU limit is
  advertised and a task's `cpu` is not checked here.
- **Memory:** 3072 MB is the largest memory setting a function takes; the
  settings are 128, 256, 512, 1024, 2048 and 3072 MB.
- **Duration:** 300 seconds is the longest synchronous invoke.
- **Architectures:** an application's shape is `GENERIC_X86`, `GENERIC_ARM`
  or `GENERIC_X86_ARM`. Admission cannot see it; the task's
  `execution.architecture` has to match the deployed function.
- **Internet:** a function reaches the internet only when its application's
  subnet routes there through a NAT or internet gateway. Vagabond cannot see
  the subnet, so `internet = true` is advertised either way.
- `arbitrary_images = false`: a task whose `config` names an `image` is
  rejected with `image-unsupported`. See
  [Scheduling](../scheduling.md#reason-codes).

## Task translation

```hcl
task "lint" {
  driver  = "function"
  timeout = "2m"

  config {
    function = "ocid1.fnfunc.oc1.iad.aaaaaaaa..."
    args     = ["--strict"]
  }

  env {
    CI = "true"
  }

  resources {
    memory = 256
  }
}
```

### Driver `config` keys

| Key | Type | Required | Description |
|---|---|---|---|
| `function` | string | yes | The function's OCID |
| `args` | list of strings | no | Passed through in the event |

Other keys are ignored, and so is an `args` that is not a list of strings.
Values are evaluated against the job's variables.

### The event and the reply

The function is invoked with the event every function provider sends, and
replies with the task's exit code and output; both are described under the
[`function` driver](../job-specification.md#function-driver):

```json
{
  "execution_id": "0199a3c4-6f2e-7b1a-9c3d-5e8f7a6b4c21",
  "args": ["--strict"],
  "env": {"CI": "true", "VAGABOND_META_COMMIT": "3f9c2e1"}
}
```

```json
{"exit_code": 0, "output": "lint: 0 issues\n"}
```

The function can be code-only, an archive run on one of OCI's managed
runtimes, or image-based; either way it decodes the event from its input and
returns the reply. This is a complete code-only function on the
`python312.ol9` runtime, at `function/func.py` in its archive with the handler
`func.handler`, and is the one the [live tests](#testing-against-a-tenancy)
invoke:

```python
import io
import json
import time

from fdk import response


def handler(ctx, data: io.BytesIO = None):
    event = json.loads(data.getvalue() or b"{}") if data else {}
    args = event.get("args") or []
    env = event.get("env") or {}

    if args[:1] == ["sleep"]:
        time.sleep(60)

    output = "hello {} [{}] {}\n".format(event.get("execution_id", ""), "|".join(args), env.get("VAGABOND_LIVE", ""))

    return response.Response(
        ctx,
        response_data=json.dumps({"exit_code": 3, "output": output}),
        headers={"Content-Type": "application/json"},
    )
```

The Python runtime carries the Fn function development kit, `fdk`, so the
archive needs nothing else. `env` is data in the event, not the function's
process environment.

**Not applied:** `resources`, `timeout`, `source`, `working_directory` and
`network` are not sent. Memory and timeout are function configuration, set
where the function is deployed. They are still checked by admission against
the capabilities above and still drive the quota reservation.

## Operations

| Method | Behaviour |
|---|---|
| `Submit` | `GET /20260325/functions/<function>` on the region's Functions API for the function's `invokeEndpoint` and `memoryInMBs`, then `POST /20260325/functions/<function>/actions/invoke` on the `invokeEndpoint`; blocks until the function returns |
| `Status` | Not supported: `internal` error wrapping `plugin.ErrUnsupported` |
| `Result` | Not supported: `internal` error wrapping `plugin.ErrUnsupported` |
| `Cancel` | Not supported: `internal` error wrapping `plugin.ErrUnsupported` |

`Submit` returns:

| Field | Value |
|---|---|
| `ProviderID` | The invoke's `opc-request-id`, which the function's logs in OCI Logging are searched by; the function's OCID when the header is missing |
| `State` | `succeeded` when the reply's `exit_code` is 0, else `failed` |
| `Result.ExitCode` | The reply's `exit_code`; `1` for a reply without one, or for a function fault |
| `Result.Duration` | Wall time of the invoke |
| `Result.Logs` | The reply's `output`; the whole reply for one without an `exit_code`, or OCI's error for a function fault |
| `Result.Billed` | `memoryInMBs` over `Result.Duration`; see [Quota](#quota) |

Every request is signed: a `Date` header is set, then the SDK's signer signs
`date`, `(request-target)` and `host`, and for the invoke also
`content-length`, `content-type` and `x-content-sha256`.

**Retries:** none. Each `Submit` makes one lookup and one invoke. A throttled
or failed call is returned to dispatch, which decides whether to try another
provider.

**Idempotency:** OCI has no idempotency key for an invoke. A repeated `Submit`
with the same ID invokes the function again. Dispatch mints a new ID per
attempt and never repeats a `Submit`; a function that must not run twice can
deduplicate on `execution_id`.

**No cleanup:** nothing outlives the invoke, so there is nothing to release,
stream or sweep. Detached invocation, which runs up to an hour, is not used:
OCI hands its result only to a Notifications, Queue or Streaming destination,
and offers no call to look an invocation up or cancel it.

## Failure classification

OCI answers an invoke whose function failed with a 502 or 504 and a body
naming the fault, such as `{"code":"FunctionInvokeTimeout","message":"Timed out"}`.
The function ran, so those are the task's answer, not a failed call:

| `code` | Status | The function |
|---|---|---|
| `FunctionInvokeExecutionFailed` | 502 | Failed while running, most likely a bug in its code |
| `FunctionInvokeExecutionError` | 502 | Answered with an error |
| `FunctionInvokeInvalidResponse` | 502 | Answered with an invalid HTTP status |
| `FunctionInvokeResponseBodyTooLarge` | 502 | Replied with more than 6 MB |
| `FunctionInvokeResponseHeaderTooLarge` | 502 | Replied with headers too large |
| `FunctionInvokeTimeout` | 504 | Ran past its configured timeout |

Each fails the task with exit code 1 and OCI's error as its output, and is
billed. Every other failure is classified by its status:

| Condition | Class | Retryable | Notes |
|---|---|---|---|
| Task `config` names no `function` | internal | no | Raised before any call |
| `env` block fails to evaluate | internal | no | Raised before any call |
| HTTP 429 (rate limited) | infrastructure | yes | `RetryAfter` from `Retry-After` |
| HTTP 5xx, any other `code`: `FunctionInvokeServiceUnavailable` (503), image pull, container start, subnet or syslog failures (502, 504) | infrastructure | yes | |
| HTTP 404 (`NotAuthorizedOrNotFound`: no such function in the region, or no `read fn-function`) | internal | no | Wraps `ErrNotFound`; the invoke is not attempted |
| HTTP 401 or 403 (a key OCI does not accept) | internal | no | Wraps `ErrUnauthorized`; the `credentials` block is resolved again and the call made once more |
| Other HTTP 4xx, `FunctionInvokeRequestContentTooLarge` (413) among them | internal | no | |
| No HTTP response: network failure, DNS, or a reply cut off | infrastructure | yes | |

An error's text carries OCI's `code` and `message`, or the start of the body
when it is not OCI's error. Dispatch reroutes `infrastructure` failures within
the task's retry budget and stops on `internal` ones. See
[Dispatch](../dispatch.md).

## Quota

OCI Functions bills per invocation and per GB-second of the function's
configured memory over its execution time. Both map to Vagabond meters; quota
behaviour is described in [Quotas](../quotas.md).

| OCI quantity | Meter |
|---|---|
| Invocations | `executions` |
| GB-seconds | `gb_seconds` |

**Reservation:** the task's declared `resources.memory` over its declared
`timeout`, plus one execution.

**Settlement:** the function's `memoryInMBs` (MB, counted as MiB) over the
measured invoke, CPU 0, whether the task succeeded or failed. The invoke
returns no billed duration, so the measured time stands in for it; it includes
the round trip and any cold start, so it is at least what OCI bills. The
settlement replaces the reservation in full, above or below it. Because it
carries no CPU, a `cpu_seconds` pool on an `oci-functions` provider is charged
only at reservation and settles to 0.

**Abandoned reservations:** a process that dies during an invoke leaves its
reservation unsettled and its execution record `pending`. The server that
claims the dispatch asks `Status`, which this provider does not support, so it
records the execution `failed` with failure class `infrastructure` and
settles it at the reserved amount. A `Submit` that fails as `infrastructure`
is charged the same way.

**Declare the function's shape on the task.** A task without
`resources.memory` or `timeout` reserves 0 GB-seconds, so a `gb_seconds` pool
cannot refuse it at admission and is charged only at settlement. Setting
`resources.memory` to the function's memory and `timeout` to its timeout makes
the reservation the worst case the bill can reach.

### Free tier pools

OCI Functions' monthly free allowance is 2,000,000 invocations and 400,000
GB-seconds, per tenancy; the example under [Configuration](#configuration)
uses those figures. Vagabond ships no defaults.

> [!WARNING]
> Cloud prices and free tier quotas change. Check your provider's current
> pricing page before setting limits.

- Two `provider` blocks against one tenancy are counted separately by
  Vagabond; split the limits between them.
- Periods reset at 00:00 UTC on the first of the month.

## Limits and edge cases

- **Duration:** 300 seconds is OCI's ceiling for a synchronous invoke and the
  advertised `max_duration`. The effective limit is the function's configured
  timeout, 30 seconds unless changed; a function that runs past it fails the
  task with `FunctionInvokeTimeout`.
- **Logs:** the task's output is what the function puts in its reply. What it
  prints goes to OCI Logging when logging is enabled on the application,
  searchable by the execution's provider ID.
- **Payload size:** the event and the reply are capped at 6 MB each. An `env`
  or `args` too large fails with a 413, classified internal; a reply too large
  fails the task.
- **Blocking:** `Submit` holds a connection open for the whole invocation. A
  caller that gives up cancels the request; the function keeps running on OCI
  and is billed, since `Cancel` is unsupported.
- **Live output:** none. Output is shown when the invoke returns.

## Testing against a tenancy

`TestLive*` in `internal/providers/oci` invoke the Python function under
[The event and the reply](#the-event-and-the-reply), deployed with 128 MB and
a 30 second timeout. They check the reply's exit code and output, the event's
contents with a `$` passed through as written, the bill at the function's
memory, and a timeout failing the task. They are skipped unless
`VAGABOND_OCI_FUNCTION` is set:

```shell
VAGABOND_OCI_FUNCTION=<function OCID> VAGABOND_OCI_REGION=us-ashburn-1 VAGABOND_OCI_TENANCY=<tenancy OCID> VAGABOND_OCI_USER=<user OCID> VAGABOND_OCI_FINGERPRINT=<fingerprint> VAGABOND_OCI_KEY_FILE=<PEM path> go test -run TestLive -v -timeout 10m ./internal/providers/oci/
```
