---
title: "Writing a Provider"
linkTitle: "Writing a provider"
seoTitle: "Writing a Provider Plugin"
description: "The provider plugin contract: interfaces, capabilities, submissions, failure classes, registration, configuration, testing and a compiling skeleton."
weight: 630
---

A provider plugin translates a normalized task into one platform's API and
classifies that platform's failures. Scheduling, retry, rerouting and quota
accounting stay in the control plane. The contract is declared in
`internal/plugin`; plugins live in `internal/providers/<name>` and are
registered in `internal/registry`. Plugins are compiled into the `vagabond`
binary and selected by the `provider` block's `type`; nothing is loaded at run
time.

| Reference | Package | Shows |
|---|---|---|
| Cloud Run Jobs | `internal/providers/gcp/cloudrun` | Asynchronous family, a `LogCapturer` over Cloud Logging that tails entries and catches up by listing them, `Releaser`, `Recredentialer`, raw HTTP against `httptest` |
| Cloud Build | `internal/providers/gcp/cloudbuild` | A second provider on the same platform, both embedding `gcp.API` for the signed client and the Cloud Logging reader; a resource the platform names and the plugin finds again by tag; a `Result` that reports a run the platform ended as infrastructure |
| Code Engine Jobs | `internal/providers/ibm` | Asynchronous family, a `LogCapturer` that follows one log and reads it whole when the follow did not see the container exit, `Releaser`, a `Recredentialer` whose token refusals are unauthorized, a fixed size table, two APIs behind one token |
| AWS Lambda | `internal/providers/aws` | Synchronous family, the `NotSupported` embeds, `Result.Billed`, output in `Result.Logs`, `Recredentialer`, a cloud SDK against `httptest` |
| containerd, firecracker | `internal/providers/pool` | One package behind two types, `Live`, `Targeter` and `Reserver`, a `LogCapturer` that places output past what the node dropped, `Releaser`, `Capabilities.Members`, an optional config block |
| Fakes | `internal/plugin/fake.go` | The smallest implementations of both families |

## Package boundaries

`make lint` runs golangci-lint with depguard rules from `.golangci.yml`:

| Rule | Applies to | Denies |
|---|---|---|
| `scheduler-boundary` | `internal/scheduler`, `internal/job`, `internal/quota` | Any import of `internal/providers/...` |
| `cloud-sdk-boundary` | Everything outside `internal/providers/...` | `github.com/aws/aws-sdk-go-v2`, `cloud.google.com/go`, `github.com/IBM/platform-services-go-sdk`, `github.com/IBM/code-engine-go-sdk`, `github.com/oracle/oci-go-sdk`, `github.com/cloudflare/cloudflare-go`, `github.com/Azure/azure-sdk-for-go`, `github.com/digitalocean/godo` |
| `state-boundary` | Everything outside `internal/state` and tests | `database/sql`, `github.com/jackc/pgx` |

Consequences for a plugin:

- Cloud SDK imports go in the plugin's package and nowhere else. No SDK type may
  appear in a signature the plugin exports to the rest of the tree.
- A plugin normally imports `internal/plugin`, `internal/execution`,
  `internal/job` and, to report a bill, `internal/quota`; the pool also imports
  `internal/nodes` and `internal/agentrpc`. `state-boundary` keeps it from the
  store.
- `internal/registry` is the only package that imports every plugin.

## The `Provider` interface

```go
type Provider interface {
    Name() string
    Capabilities(ctx context.Context) (Capabilities, error)
    Submit(ctx context.Context, id execution.ID, task *job.Task) (Submission, error)
    Status(ctx context.Context, id execution.ID) (execution.Status, error)
    Result(ctx context.Context, id execution.ID) (*execution.Result, error)
    Cancel(ctx context.Context, id execution.ID) error
}
```

Rules that apply to every method:

- **No retry, no backoff, no fallback provider.** Return the failure. Dispatch
  decides whether to try elsewhere. A plugin or SDK that retries internally
  runs work the ledger never recorded; disable SDK retryers.
- **Do not modify `*job.Task`.** The same task is offered to the next provider
  on reroute.
- **Derive platform names from the ID.** `execution.ID` is a UUIDv7 minted and
  stored before `Submit`, so any process can find the work later without
  plugin state.
- **Return `*plugin.Error`.** An unclassified error is treated as not
  reroutable.
- **Honour `ctx`.** The execution's runner cancels it when the dispatch is
  cancelled or claimed by another server.

### `Name`

Returns the name passed to the constructor, which is the `provider` block's
label. It is persisted on every execution record and quota row and is what a
job's `providers` list names. One type may be configured twice under two names.

### `Capabilities`

What the provider can run. Called by the registry at server startup and every
minute after, in parallel across providers, each under a 30-second timeout.
Never called on the request path.

- A returned error marks the provider unhealthy; admission rejects it with
  `provider-unhealthy` and the last good snapshot is kept.
- Returning constants is correct when nothing varies by account.
- Set `ObservedAt` to when the snapshot was taken. It is reported with each
  candidate in a plan.
- Return a value that shares no slices with plugin state, or return
  `c.Clone()`.

### `Submit`

Starts the task under `id` and reports what the platform said.

- **Idempotent on `id`:** a second call with the same ID must not start a
  second run. Naming the platform resource after the ID provides this on
  platforms that reject a duplicate name.
- **Clean up on partial failure:** if `Submit` creates a resource and a later
  step fails, delete it before returning.
- **Validate before calling out:** a task missing a required `config` key is
  an `Internal` error, raised before any request.
- **Deliver the task as written:** the container receives the command,
  arguments and env values the task declared, byte for byte, so a job file
  behaves the same on every provider. A platform that refuses or rewrites a
  value gets a translation that undoes it, as Code Engine's does for line breaks
  and `$(NAME)` expansion; see [Code Engine](providers/code-engine.md#command-line).

`Submission` fields:

| Field | Meaning |
|---|---|
| `ProviderID` | The platform's identifier for the work; stored on the execution record |
| `State` | One of `accepted`, `running`, `succeeded`, `failed` |
| `Result` | Set only when the work finished inside `Submit` |

Dispatch calls `Submission.Validate()` on every submission. It fails, as an
`internal` error wrapping `plugin.ErrInvalidSubmission`, when:

| Submission | Why rejected |
|---|---|
| `State` is not a known state | Invalid |
| `State` is `pending`, `submitted`, `cancelled` or `lost` | Not a state a submission can report |
| `Result` set with `accepted` or `running` | Result on a non-terminal state |
| `Result` nil with `succeeded` or `failed` | Terminal state without a result |

A submission with a `Result` is **synchronous**: the runner records the
result, calls `Release` if implemented, and never polls `Status` or calls
`Result`. Otherwise it polls `Status`.

### `Status`

Where an execution has reached. Polled by the execution's runner at 2s,
doubling to a 15s ceiling, while it runs or is `lost`. Also called once after
a cancel, or a `Submit` that failed as `infrastructure` or unclassified, to
learn what ran; and once for a `pending` record a resumed dispatch restores,
to learn whether the platform ever got it.

| `execution.Status` field | Set to |
|---|---|
| `ID` | The `id` argument |
| `State` | `accepted`, `running`, `succeeded`, `failed` or `cancelled` |
| `ProviderID` | As in the submission |
| `StartedAt` | When the work began running; zero until then |
| `EndedAt` | When it finished; zero until terminal |
| `UpdatedAt` | When the state last changed |

- Report `accepted` for provisioning and image pull, `running` only once the
  workload runs.
- Wrap `plugin.ErrUnknownExecution` only when the platform says it has no
  record of the ID. While polling it records the execution `failed` at once,
  charged for the time it was seen running. Classify it `infrastructure` when
  it may pass, as a listing that lags the run; the execution then goes `lost`
  like any other failure. Asked after a cancel or a failed `Submit`, it
  charges nothing for a run never seen running. A lookup that merely failed
  must not wrap it.
- Set `StartedAt` and `EndedAt`, on terminal states especially. A cancelled
  run, or a failed `Submit` the platform ran anyway, is charged the declared
  shape over the `EndedAt - StartedAt` that `Status` reports.

### `Result`

What a finished execution produced. Called once by the execution's runner
after `Status` reports a terminal state, before the output capture finishes
and before `Release`. Not
called for a run Vagabond cancelled, or one that ended without an answer. A
failed call is not repeated: the execution keeps its terminal state without a
result.

| `execution.Result` field | Meaning |
|---|---|
| `ID` | The `id` argument |
| `ExitCode` | `*int`; nil means the driver has no process to exit |
| `Duration` | How long the work ran; settles the quota charge when `Billed` is nil |
| `Billed` | `*quota.Execution` the platform reported charging; nil when it reports nothing |
| `Logs` | Combined output, for a provider that hands it over with the answer and does not implement `LogCapturer`. Dispatch stores it; a stored result carries none |
| `LogsTruncated` | `Logs` is not all of the output, as when the platform returns only its tail |
| `Reason` | Why the platform ended the workload, in its words: an eviction, an OOM kill. Empty when the process exited on its own, or the platform's reason says no more than the exit code |

- A non-zero exit is not an error. Return the result; the state is `failed`.
- The terminal state is the verdict: dispatch counts a task as succeeded only
  when the provider reports `succeeded`, whatever the exit code. Map exit
  codes to the state; a nil `ExitCode` is fine for a task that never ran.
- A platform that holds the output should not read it here. Implement
  [`LogCapturer`](#logcapturer), which dispatch calls while the execution
  runs, and leave `Logs` empty.
- Set `Billed` when the platform reports its charge, as Lambda's `REPORT` line
  does. Settlement uses it in full in place of declared shape times `Duration`.
- Read the platform's timestamps with `plugin.ParseTime` and take `Duration`
  from `execution.Elapsed`. A missing time, or an end before the start such as
  the 1970 finish Code Engine reports for an evicted instance, is then zero
  rather than a negative or centuries-long charge.

### `Cancel`

Stops a running execution. Called by the execution's runner, on a fresh
context with a 30s timeout, when its dispatch is cancelled
(`DELETE /v1/execution/{id}`, on this server or through the lease), and after
a `Submit` that failed as `infrastructure` or unclassified, since the run may
exist. One `Status` call follows, for the times it ran. Its error is ignored.

- Cancelling a finished or already deleted execution returns `nil`.
- If cancelling destroys what `Result` or `CaptureLogs` reads, document it on
  the provider page.
- If cancelling makes `Status` report the execution unknown, as deleting it
  does, a cancelled run is charged for the time Vagabond saw it running
  rather than the times the platform kept. Document that on the provider page
  too.

## Unsupported operations

A synchronous provider embeds the helpers instead of writing stub methods:

```go
type Provider struct {
    plugin.StatusNotSupported
    plugin.ResultNotSupported
    plugin.CancelNotSupported
    // ...
}
```

| Embed | Method it supplies | Returns |
|---|---|---|
| `StatusNotSupported` | `Status` | `Internal("status: " + ErrUnsupported)` |
| `ResultNotSupported` | `Result` | `Internal("result: " + ErrUnsupported)` |
| `CancelNotSupported` | `Cancel` | `Internal("cancel: " + ErrUnsupported)` |

Nothing reads `ErrUnsupported` specially; it is an `internal` failure like any
other. A provider that embeds `StatusNotSupported` is asked `Status` only after
a `Submit` that failed as `infrastructure` or unclassified, or for a `pending`
record a resumed dispatch restores. The call fails, so nothing says what ran,
and the execution is charged the reserved amount.

## Optional interfaces

Implemented or not; nothing registers them. Callers type-assert and skip what
is absent.

| Interface | Method | Called by | When |
|---|---|---|---|
| `LogCapturer` | `CaptureLogs(ctx context.Context, id execution.ID, log execution.Log, ended <-chan struct{}) error` | Execution runner | From when the provider has the work until the output is whole, called again whenever it returns early; before the result is recorded and `Release`; not after a failed `Submit` |
| `Releaser` | `Release(ctx context.Context, id execution.ID) error` | Execution runner | Last, for every ending: after `Result`, the output capture and the settle |
| `Recredentialer` | `Recredential(ctx context.Context, credential []byte) error` | Registry, for the execution runner | After a runner call (`Submit`, `Status`, `Result`, `CaptureLogs`, `Cancel`, `Release`) failed with `ErrUnauthorized`, when the provider block has a `credentials` block; at most once a minute per provider |
| `Live` | `LiveCapabilities() (Capabilities, error)` | Registry | On every plan, in place of the refreshed snapshot |
| `Targeter` | `Target(node string) Provider` | Dispatch, server | For every call on an execution whose candidate or record names a node |
| `Reserver` | `Reserve(id execution.ID, task *job.Task) error` | Dispatch | On the provider `Target` returned, after quota is reserved and before the execution is recorded |

### `LogCapturer`

The execution's runner copies the output into the store while the execution
runs, through this, and every client reads that copy; no client reaches the
provider.

- Copy the output into `log` as the platform has it, from `log.Len()` on:
  `log` holds what an earlier call, or another server before it took the
  dispatch over, already captured. A byte stream skips those bytes with
  `plugin.SkipWriter(log, log.Len())`. Cloud Logging has no byte positions,
  so its capture matches the lines it lists against `log.WriteTo` instead
  ([Cloud Run](providers/cloud-run.md#output)).
- Return `nil` only once `ended` has closed and `log` holds all of the output.
  `ended` closes when the runner sees the execution terminal. A platform whose
  stream ends exactly when the workload does may then wait for `ended`, as
  agent nodes do. One that cannot tell a stream that ended from one that was
  cut reports the cut, and once `ended` closes reads what is left, as Code
  Engine does.
- Return an error for any other end: a stream cut, a refusal, a read that
  failed. The runner logs it as `capturing output`, waits 1s doubling to 30s,
  back to 1s after a call that added output, and calls again. Once `ended`
  has closed, three failed calls end the capture: what was written is kept and
  the output is marked truncated. The execution is never failed over it.
- Write as the output arrives rather than collecting it. The runner stores
  what arrives at least once a second, in 256 KiB chunks, and keeps the end
  past the server's `max_log_mib`.
- A platform that keeps only the end of long output, knowing where that end
  begins, calls `log.StartsAt(offset)` before writing it, with `offset` a
  multiple of 256 KiB (`execution.LogChunk`) at or past `log.Len()`, as agent
  nodes do. What `log` held before it is dropped.
- Wrap `log` in `plugin.LogOutput` when writing to it directly, so a failed
  write is `internal` and told apart from the platform's failures.
  `plugin.CopyAll` copies a body and reports a failed read as infrastructure.
- `ctx` is cancelled when the capture is stopped: the dispatch's lease moved,
  the server is shutting down, or 5 minutes passed after `ended` closed.
  `plugin.Ended(ended)` reports whether `ended` has closed.
- A call refused as unauthorized is made again after a credential refresh,
  from what `log` holds.
- For a platform that holds the output. A provider whose output comes with the
  answer puts it in `Result.Logs` instead; synchronous providers should not
  implement it.

### `Releaser`

- Deletes a resource the execution left behind, such as Cloud Run's Job.
  Implementing it is all a provider does for cleanup: the control plane records
  which executions are released and calls it for every one that is not.
- The execution's runner calls it last, however the execution ended: a
  result, a cancel, a failed `Submit`, a failed `Result`, a `lost` execution
  once resolved. It comes after `Result` and the output capture, because
  releasing may destroy what they read, and after the quota settle. A runner restored after
  a claim calls it for an execution whose last runner did not.
- Called on a context with no deadline; a call that never returns holds the
  dispatch open.
- Idempotent: it may be called more than once for an execution. Return nil or
  `ErrUnknownExecution` when there is nothing left; any other error is retried,
  250ms doubling to 10s, until one of those comes back, and the dispatch stays
  open meanwhile.

### `Recredentialer`

- For a provider that holds a credential. Embed `*plugin.Credentialed[C]`,
  where `C` is whatever signs requests, and read it with `Client()`; it
  provides this method. It rebuilds the client from the new bytes and swaps
  it in atomically, so calls in flight may still use the old one.
- The context is the call that was rejected. `Credentialed` builds on
  `context.WithoutCancel` of it, since a token source outlives the call.
- When the bytes are unusable it returns the error and keeps the old client.
- Classify 401 and 403 through `ClassifyHTTP`, which marks them
  `ErrUnauthorized`; that is what triggers the refresh.
- Never called for a provider block with no `credentials` block: there is
  nothing to resolve again.
- Called at most once a minute per provider. A rejection within a minute of the
  last refresh is retried on the credential already in place.
- Only a runner's calls are retried, `CaptureLogs` among them.

### `Live`

- For capabilities that change between refreshes and cost nothing to read.
- A live provider is skipped by `Refresh`. `LiveCapabilities` is read on every
  plan; an error marks the provider unhealthy for that plan.
- It must not do I/O or block: the registry calls it under its read lock while
  building admission inputs.

### `Targeter` and `Reserver`

- For a provider made of nodes, each advertised in `Capabilities.Members`,
  judged by admission and ranked as a candidate of its own, in the order the
  provider lists them.
- `Target` returns the provider for one node's executions; the execution
  record keeps the node, so a restored runner reaches the same one.
  `plugin.Target(p, node)` returns `p` itself for a provider that does not
  implement `Targeter`.
- `Reserve` holds the task's room on the node and fails with
  `plugin.ErrNoRoom` when there is too little left. Dispatch counts that as a
  refusal: the quota reservation is dropped, nothing is recorded, and no
  attempt is spent. A later `Submit` that fails, or `Release`, gives the room
  back.

## Capabilities

`plugin.Capabilities` is everything admission may know about a provider. It
decides without calling the provider.

| Field | Type | Attribute | Admission use | Zero value means |
|---|---|---|---|---|
| `Drivers` | `[]job.DriverName` | `provider.drivers` | `driver-unsupported` | Runs nothing |
| `Architectures` | `[]job.Arch` | `provider.architecture` | `arch-unsupported` | Offers no architecture |
| `MaxResources.CPU` | `*int`, millicores | `provider.max_cpu` | `resources-exceeded` | nil: no limit. `0` admits nothing |
| `MaxResources.Memory` | `*int`, MiB | `provider.max_memory` | `resources-exceeded` | nil: no limit. `0` admits nothing |
| `DefaultResources` | `Resources`, millicores and MiB | none | Sizes a task that declares no CPU or memory before `resources-exceeded` compares it | The task is checked at 0 |
| `MaxDuration` | `time.Duration` | `provider.max_duration` | `duration-exceeded` | No limit |
| `InternetEgress` | `bool` | `provider.internet` | `network-unsupported` | No egress |
| `PrivateNetwork` | `bool` | `provider.private_network` | `network-unsupported` | No private network |
| `ArbitraryImages` | `bool` | `provider.arbitrary_images` | `image-unsupported` for a task naming `image` | Runs only its own images |
| `Isolation` | `string`, `plugin.IsolationVM` or `plugin.IsolationContainer` | `provider.isolation` | `constraint-unmet`, when a job constrains it | Unstated, and not published |
| `EstimatedCost` | `job.Cost` | `provider.estimated_cost` | `cost-policy` | Free |
| `ObservedAt` | `time.Time` | none | Shown in plans | Never observed |
| `Members` | `[]plugin.Member` | none | Each member judged separately, and a candidate of its own | Not a provider made of nodes |

Reason codes are listed in [Scheduling](scheduling.md#reason-codes).

- **Units:** CPU in millicores (1000 = 1 vCPU), memory in MiB, matching
  `job.Resources`.
- **Limits:** a nil `MaxResources.CPU` or `MaxResources.Memory` advertises no
  limit; `0` is a real limit that admits nothing, as a full node reports.
  A zero `MaxDuration` advertises no limit. A platform that derives CPU from
  memory advertises the CPU its largest tier implies rather than nil, as Lambda
  advertises 6000.
- **Attributes:** the dotted names are what job `constraint` and `affinity`
  blocks match. A nil resource limit and a zero `MaxDuration` are omitted from
  the attribute map; a limit of `0` is published as `0`. Booleans and
  `estimated_cost` are always present. Admission adds
  `provider.free_quota_percent` from the ledger, `provider.tier` from the
  provider block, and `provider.meta.<key>` from its `meta` block; a plugin
  publishes none of them. Every name under `provider.` is reserved to Vagabond.
- **Members:** each `plugin.Member` has a `Name`, its own `Capabilities` and
  `Labels`, matched as `node.label.<key>`.

A field is added to `Capabilities` only when admission branches on it. A
platform limit no job field maps to stays inside the plugin.

## Failures

Every error a plugin returns should be a `*plugin.Error`:

```go
type Error struct {
    Class      Class          // infrastructure or internal
    Retryable  bool
    RetryAfter time.Duration
    Provider   string         // set by the caller, not the plugin
    Op         string         // set by the caller, not the plugin
    Err        error
}
```

| Constructor | Class | `Retryable` | Reroutable | Use for |
|---|---|---|---|---|
| `plugin.Infrastructure(err)` | `infrastructure` | true | yes | The platform failed to give an answer: unreachable, throttled, 5xx, an inconsistent response |
| `plugin.Internal(err)` | `internal` | false | no | Vagabond is at fault: a malformed request, a missing config key, a permission the operator did not grant |

A task that ran and failed is neither. It is a result with a non-zero exit
code.

### `ClassifyHTTP`

`plugin.ClassifyHTTP(status int, retryAfter time.Duration, err error) *Error`
is the default mapping for HTTP APIs. The error text reads
`http <status>: <err>`, preceded by `not found: ` for a 404 and
`unauthorized: ` for a 401 or 403.

| Status | Class | `RetryAfter` | Also wraps |
|---|---|---|---|
| 408, 429 | infrastructure | passed through | |
| 404 | internal | 0 | `plugin.ErrNotFound` |
| 401, 403 | internal | 0 | `plugin.ErrUnauthorized` |
| other 4xx | internal | 0 | |
| 5xx | infrastructure | passed through | |
| anything else | infrastructure | 0 | |

A platform that uses a status unusually builds the `*Error` directly.

### `ClassifyResponse`

A plugin calling a JSON API sends through `internal/rest`, as Cloud Run and
Code Engine do, and classifies what failed with
`plugin.ClassifyResponse(err error, refused func(*rest.Refusal) error) error`:

| `rest` failure | Classified as |
|---|---|
| Already a `*plugin.Error`, as a token source's credential refusal | As it is |
| `*rest.Refusal`: a non-2xx with its method, URL, status, headers and up to 64 KiB of body | Whatever `refused` returns, usually `ClassifyHTTP(r.Status, r.RetryAfter(), <the platform's message from r.Body>)` |
| `rest.ErrUnreachable`: no answer, or a body cut off | `infrastructure` |
| Anything else: a payload that does not encode, a URL that does not parse, an answer that does not decode | `internal` |

`r.RetryAfter()` reads a `Retry-After` header in whole seconds, 0 when absent
or malformed.

### Sentinels

| Sentinel | Meaning | Who reads it |
|---|---|---|
| `plugin.ErrProvider` | Matches any `*plugin.Error` via `errors.Is` | Callers that only need "a provider call failed" |
| `plugin.ErrNotFound` | `ClassifyHTTP` saw a 404 | The plugin, to translate into `ErrUnknownExecution` or success |
| `plugin.ErrUnauthorized` | `ClassifyHTTP` saw a 401 or 403: the credential was rejected | Dispatch: for a `Recredentialer` whose block has `credentials`, re-resolves the credential and retries the call once |
| `plugin.ErrUnknownExecution` | The platform has no record of the execution | Execution runner: while polling, fails the execution at once, or makes it `lost` when classified `infrastructure`; from the `Status` after a cancel or failed `Submit`, charges for the time seen running, or drops the reservation for a run never seen running; from `Release`, counts the execution released |
| `plugin.ErrUnsupported` | The platform has no such operation | Nobody specially; an `internal` failure like any other |
| `plugin.ErrInvalidSubmission` | `Submission.Validate` failed | Dispatch |

### How dispatch reacts

| Where | Outcome | Execution record | Next step |
|---|---|---|---|
| `Submit` returns `infrastructure` | The run may exist: `Cancel`, then one `Status` for what ran; then `Release` | `failed`, failure `infrastructure` | Next ranked provider, if the task's `retry` block allows reroute and attempts remain |
| `Submit` returns `internal` | No run; the reservation is dropped; then `Release` | `failed`, failure `internal` | Task stops; job fails |
| `Submit` returns an unclassified error | As `infrastructure`: `Cancel`, `Status`, `Release` | `failed`, failure `internal` | Task stops; job fails |
| `Submission.Validate` fails | Treated as `internal` | `failed`, failure `internal` | Task stops |
| `Status` returns an error while polling | The attempt ends; the run is not cancelled; the runner keeps polling it | `lost` | Reroute if `infrastructure`, else stop |
| `Status` returns `ErrUnknownExecution`, not `infrastructure`, while polling | The attempt ends; output captured; `Release` called | `failed` | Stop |
| `Status`, `Result`, `Cancel` or `Release` runs past 30s | The call is given up on, as an `infrastructure` error | As for that call's error | As for that call's error |
| Dispatch cancelled while polling | `Cancel`, then one `Status` for what ran; output captured; `Release` | `cancelled` | Stop |
| `Status` reports `cancelled` | `Result` read, output captured, `Release` called | `cancelled` | Treated as `infrastructure`: reroute if allowed |
| `Result` returns an error | Output captured, `Release` called | Terminal state from `Status`, no result | Reroute if `infrastructure`, else stop |
| `Status` reports `succeeded` or `failed`, `Result` returns | Output captured, then `Release` called | Terminal state and result | Task ends; a non-zero exit stops the job |
| `CaptureLogs` returns an error | Logged and called again; past three failures once the execution ended, what was written is kept | `LogsTruncated` set when it gives up | None; the execution's outcome is unchanged |
| `Release` returns an error | Retried, 250ms doubling to 10s, until it succeeds or returns `ErrUnknownExecution`; the dispatch stays open meanwhile | `released_at` set once it does | None |

Retry budgets, backoff and reroute rules are in [Dispatch](dispatch.md).

## Configuration

A plugin decodes its own `config` block. Vagabond hands it over undecoded, as
the `hcl.Body` in `registry.Settings.Config`, so plugin fields never appear in
`internal/config`.

```go
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics)
```

- **Decode with `plugin.DecodeConfig[Config](name, Type, body, needs)`.** A
  nil body, no `config` block, is one `Missing provider configuration`
  diagnostic listing `needs`, not one per missing attribute; with `needs`
  empty the block is optional and a missing one decodes to a zero `Config`.
  Decoding is `gohcl.DecodeBody`, which reports missing and unknown
  attributes against source ranges.
- **Refuse present-but-empty strings** with
  `plugin.EmptySettings(name, map[string]string{"region": cfg.Region})`;
  `gohcl` accepts `""`.
- **Return `hcl.Diagnostics`, never `error`,** so the CLI prints the file and
  line.

A container plugin reads its task once with `plugin.ContainerOf`, which the
built-in container providers share; anything else in the driver `config` is
read with helpers on `*job.Task`:

| Helper | Returns |
|---|---|
| `plugin.ContainerOf(task, defaults)` | `(plugin.Container, error)`: the image, `command`, `args`, environment, working directory, size with `defaults` for what the task leaves unset, and timeout; a task with no image, or an `env` or `timeout` that does not evaluate, is `plugin.Internal` |
| `plugin.CloudContainer(plugin.Cloud{...})` | The capabilities of a managed container platform from its maximum, default size, longest run and isolation, so admission sizes a task as `ContainerOf` does |
| `task.ConfigString(name)` | `(string, bool)`: a string attribute evaluated against the job's variables |
| `task.ConfigStrings(name)` | `([]string, bool)`: a list of strings; false when absent or empty |
| `task.Environment()` | `(map[string]string, hcl.Diagnostics)`: the `env` block plus `VAGABOND_META_<KEY>` per job `meta` key |
| `job.ConfigImage` | The `"image"` key, which admission also reads |

## Credentials

The registry resolves the `credentials` block before calling the constructor
and passes bytes in `registry.Settings.Credentials`. The plugin never learns
whether they came from a file, an environment variable or a command. They are
resolved when the registry is built, and again for a `Recredentialer` after a
dispatch call fails with `ErrUnauthorized`.

- `nil` when the provider block has no `credentials` block. Either fail with a
  diagnostic or fall back to the platform's default chain, and document which.
- Parse the credential in the constructor, so a bad secret stops startup.
  `plugin.NewCredentialed(ctx, name, credential, build)` builds the first
  client and returns the `Unusable credential` diagnostic when `build` fails.
- Accept the narrowest format the platform offers. The Cloud Run plugin accepts
  service account keys only, because broader formats can name a command to
  run.

## Quota

A plugin declares no budgets. Operators declare `pool` blocks in the provider
block, using the closed meter vocabulary (`executions`, `gb_seconds`,
`cpu_seconds`, `seconds`) in `internal/quota`; see [Quotas](quotas.md).

The charge is arithmetic over what the task declared, because `job plan` prices
a job without dispatching it. What a plugin contributes:

- `Result.Duration` and `Result.Billed`, which settle the reservation.
- `Status.StartedAt` and `EndedAt`, which price a cancelled run, or a failed
  `Submit` the platform ran anyway.
- A provider page under `docs/providers/` naming the platform's billed
  quantities, the meter each maps to, the published free-tier figures, and any
  quantity no meter expresses.

A platform ceiling that refuses work but never depletes, such as a maximum
duration, belongs in `Capabilities`, not in a pool.

## Registration

Every plugin exports a `Type` constant and a constructor:

```go
const Type = "example"

func New(ctx context.Context, name string, body hcl.Body, credentials []byte) (*Provider, hcl.Diagnostics)
```

Add a row to the `builders` table in `internal/registry/build.go`:

```go
var builders = []struct {
    providerType string
    build        builder
}{
    {cloudrun.Type, cloud(cloudrun.New)},
    // ...
    {example.Type, cloud(example.New)},
    // ...
}
```

- `cloud` adapts a constructor of the shape above, passing it the block's
  label, its undecoded `config` body and its resolved credential. On error it
  returns a nil interface, not one holding a nil `*example.Provider`, which
  would be non-nil and panic on first use.
- The table's order is the order the `Unknown provider type` diagnostic lists
  every known type in.
- `registry.Settings` also carries `Nodes`, the server's connected agents,
  which only the `containerd` and `firecracker` types use.

Nothing else is wired by hand. `vagabond server` builds the registry from
configuration at startup, refreshes it, and hands each provider to admission
and dispatch.

## Skeleton

A minimal asynchronous provider that compiles against the current interfaces.
It targets an imaginary JSON API with `POST /runs`, `GET /runs/{name}`,
`GET /runs/{name}/result`, `POST /runs/{name}:stop` and `DELETE /runs/{name}`.

```go
// Package example dispatches container tasks to the Example batch API.
package example

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/rest"
)

// Type is what a provider block's type names to select this plugin.
const Type = "example"

// Compile-time proof the contract is met.
var (
	_ plugin.Provider = (*Provider)(nil)
	_ plugin.Releaser = (*Provider)(nil)
)

// Config is the provider's own config block.
type Config struct {
	Endpoint string `hcl:"endpoint"`
}

// Provider runs tasks as Example batch runs.
type Provider struct {
	name    string
	token   string
	http    *http.Client
	baseURL string // a field, so tests can point it at httptest
}

// New decodes the config block and checks the credential.
func New(_ context.Context, name string, body hcl.Body, credentials []byte) (*Provider, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, Type, body, "endpoint")
	if cfg == nil {
		return nil, diags
	}

	if diags = append(diags, plugin.EmptySettings(name, map[string]string{"endpoint": cfg.Endpoint})...); diags.HasErrors() {
		return nil, diags
	}

	// Fail at startup, not at first dispatch.
	if len(credentials) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unusable credential",
			Detail:   fmt.Sprintf("Provider %q: no credential supplied.", name),
		}}
	}

	return &Provider{
		name:    name,
		token:   string(credentials),
		http:    &http.Client{Timeout: time.Minute},
		baseURL: strings.TrimSuffix(cfg.Endpoint, "/"),
	}, nil
}

// Name returns the routing identifier from the provider block's label.
func (p *Provider) Name() string { return p.name }

// platform is the platform's fixed limits; nothing varies by account.
var platform = plugin.Cloud{
	Max:         plugin.Resources{CPU: 4000, Memory: 8192},
	Default:     plugin.Resources{CPU: 1000, Memory: 2048},
	MaxDuration: time.Hour,
	Isolation:   plugin.IsolationContainer,
}

// Capabilities reports the platform as admission needs it.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.CloudContainer(platform), nil
}

// Submit starts a run named after the execution ID, so a retry of the same
// ID collides rather than starting a second run.
func (p *Provider) Submit(ctx context.Context, id execution.ID, task *job.Task) (plugin.Submission, error) {
	c, err := plugin.ContainerOf(task, platform.Default)
	if err != nil {
		return plugin.Submission{}, err
	}

	name := plugin.ResourceName(id)

	run := map[string]any{"name": name, "image": c.Image, "env": c.Env, "memory_mib": c.Memory}
	if err := p.call(ctx, http.MethodPost, "/runs", run, nil); err != nil {
		return plugin.Submission{}, err
	}

	return plugin.Submission{ProviderID: name, State: execution.StateAccepted}, nil
}

// Status maps the platform's run state onto Vagabond's.
func (p *Provider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	var run struct {
		State   string    `json:"state"`
		Started time.Time `json:"started"`
		Ended   time.Time `json:"ended"`
	}

	name := plugin.ResourceName(id)

	if err := p.call(ctx, http.MethodGet, "/runs/"+name, nil, &run); err != nil {
		// Only the platform saying "no such run" wraps ErrUnknownExecution.
		if errors.Is(err, plugin.ErrNotFound) {
			return execution.Status{}, plugin.Internal(plugin.UnknownExecution("run %s", name))
		}

		return execution.Status{}, err
	}

	states := map[string]execution.State{
		"queued": execution.StateAccepted, "running": execution.StateRunning,
		"ok": execution.StateSucceeded, "error": execution.StateFailed,
	}

	state, ok := states[run.State]
	if !ok {
		return execution.Status{}, plugin.Infrastructure(fmt.Errorf("run %s: unknown state %q", id, run.State))
	}

	// Stamped with its end, or with now while it runs
	return plugin.ObservedStatus(id, state, name, run.Started, run.Ended), nil
}

// Result returns the exit code and output of a finished run.
func (p *Provider) Result(ctx context.Context, id execution.ID) (*execution.Result, error) {
	var out struct {
		ExitCode *int    `json:"exit_code"`
		Seconds  float64 `json:"seconds"`
		Logs     string  `json:"logs"`
	}

	if err := p.call(ctx, http.MethodGet, "/runs/"+plugin.ResourceName(id)+"/result", nil, &out); err != nil {
		return nil, err
	}

	return &execution.Result{
		ID:       id,
		ExitCode: out.ExitCode,
		Duration: time.Duration(out.Seconds * float64(time.Second)),
		Logs:     []byte(out.Logs),
	}, nil
}

// Cancel stops a run. A run that is already gone satisfies the caller.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	return plugin.IgnoreNotFound(p.call(ctx, http.MethodPost, "/runs/"+plugin.ResourceName(id)+":stop", struct{}{}, nil))
}

// Release deletes the run record, the last call for every execution.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	return plugin.IgnoreNotFound(p.call(ctx, http.MethodDelete, "/runs/"+plugin.ResourceName(id), nil, nil))
}

// call sends one JSON request and classifies any failure.
func (p *Provider) call(ctx context.Context, method, path string, payload, out any) error {
	err := rest.Call(ctx, p.http, &rest.Request{
		Method: method, URL: p.baseURL + path, Payload: payload,
		Header: http.Header{"Authorization": {"Bearer " + p.token}},
	}, out)

	return plugin.ClassifyResponse(err, func(r *rest.Refusal) error {
		return plugin.ClassifyHTTP(r.Status, r.RetryAfter(), fmt.Errorf("%s %s: %s", method, path, r.Body))
	})
}
```

The matching provider block:

```hcl
provider "example" {
  type = "example"

  config {
    endpoint = "https://batch.example.com/v1"
  }

  credentials {
    env = "EXAMPLE_TOKEN"
  }
}
```

A synchronous provider replaces `Status`, `Result` and `Cancel` with the three
`NotSupported` embeds and returns a terminal `State` and a `Result` from
`Submit`; see `internal/providers/aws`.

## Testing

**Against a fake platform.** Keep the platform's base URL, or the SDK's
endpoint, as a field and point it at `httptest.NewServer` in tests. This
exercises request building, headers, encoding and status classification.

A provider embedding `plugin.Credentialed` gets its test client the same
way it gets a real one, from a build function, here one that returns a client
for the test server:

```go
// Raw HTTP, as internal/providers/gcp/gcptest does: unsigned, since httptest needs no auth.
creds, _ := plugin.NewCredentialed(t.Context(), "gcp-cloud-run", nil,
    func(context.Context, []byte) (*http.Client, error) { return server.Client(), nil })

// An SDK, as internal/providers/aws does.
client := lambda.New(lambda.Options{
    Region:       "us-east-1",
    BaseEndpoint: sdkaws.String(srv.URL),
    Credentials:  credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
    Retryer:      sdkaws.NopRetryer{},
    HTTPClient:   srv.Client(),
})
creds, _ := plugin.NewCredentialed(t.Context(), "aws-lambda", nil,
    func(context.Context, []byte) (*lambda.Client, error) { return client, nil })
p := &Provider{Credentialed: creds, name: "aws-lambda"}
```

`internal/plugin/plugintest` holds what provider tests share:
`Body` and `RawBlock` parse configuration the way the loaders leave it, and
`ContainerTask` is a container task shaped like the documented example.

Cover at least:

| Case | Assert |
|---|---|
| Submit translates the task | The request body the fake received |
| Submit makes one request per call | The fake's call count, with the SDK retryer disabled |
| Each status, 404, 429 and 5xx | `Class`, `Retryable`, `errors.Is(err, plugin.ErrUnknownExecution)` where it applies |
| Every platform state | The mapped `execution.State` |
| Non-zero exit | Result returned, no error |
| Cancel twice, or after the resource is gone | `nil` |
| Missing config block, empty attribute, bad credential | Diagnostics from `New` |
| `CaptureLogs` of a finished run's long output | All of it, in order, and an error after what arrived when it is cut short, captured into a `plugin.MemoryLog` |
| `CaptureLogs` into a log holding the start of the output | Only the rest added |
| `CaptureLogs` of a stream cut while the run goes | An error, after what arrived |
| `CaptureLogs` of output the platform kept only the end of | The `MemoryLog`'s `Start`, where that end begins |

**Compile-time assertions** for every interface the plugin means to satisfy:

```go
var (
    _ plugin.Provider    = (*Provider)(nil)
    _ plugin.Releaser    = (*Provider)(nil)
    _ plugin.LogCapturer = (*Provider)(nil)
)
```

An optional interface is found by type assertion, so a method with a slightly
wrong signature compiles, is silently never called, and only this assertion
catches it.

**Live tests.** Add a round-trip test against the real platform, gated on an
environment variable and skipped when it is unset, so `make test` needs no
cloud account (see `internal/providers/gcp/cloudrun/live_test.go`). For a container
provider, `plugintest.Live{Poll, Deadline}.RoundTrip(t, p, resources)` is the
whole test: a container printing to both streams and exiting 3, its output
captured as the runner captures it and its first line required before the
task ends, its exit code checked, and cancelled in a cleanup. It returns the
execution ID for checks of the provider's own. `Finish`, `AwaitRunning`,
`Capture` and `ReadAll` are there for other live tests. A cleanup registered by hand needs its own context: `t.Context()` is
cancelled before `t.Cleanup` functions run, so a delete on it leaks the
resource.

**Without the platform.** `internal/plugin` exports fakes for tests of code
above the boundary: `NewFakeContainerProvider`, `NewFakeFunctionProvider` and
`NewFakeWorkerProvider`, with fixture capabilities from `FixtureContainer`,
`FixtureFunction` and `FixtureWorker`. They are also registered as the
`fake-container`, `fake-function` and `fake-worker` types, which the
integration tests (`make integration-test`) and `examples/config.hcl` use.
`FakeContainerProvider` exposes `Caps`, `SubmitErr` and `ExitCode`; its
`CaptureLogs` writes one fixed line, then waits for `ended`.
`Advance(id, state)` moves a recorded run as the platform would between polls.
`FakeSyncProvider` exposes `Caps`, `SubmitErr` and `ExitCode`; a nil `ExitCode`
succeeds.

**Before a PR:** `make check` runs formatting, vet, lint (including depguard),
the race-enabled tests and govulncheck.
