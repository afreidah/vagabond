# Writing a provider

A provider plugin translates a normalized task into a platform's API and
classifies that platform's failures. It does nothing else.

Reference implementation: `internal/providers/gcp`.

## The interface

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

Declared in `internal/plugin`. This is the one producer-declared interface in
the tree; everywhere else a consumer declares the narrow interface it needs.

## What a plugin must not do

- Retry or back off. The control plane owns retry, with a view of every provider
  and of the ledger.
- Select an alternative provider.
- Modify the `*job.Task` it is given. The same task is offered to other
  candidates when a submission is rerouted.

A plugin that retries internally spends capacity the ledger never records.

## Method contracts

### `Name`

The routing identifier a job's `providers` list refers to. Stable for the life
of the provider; it is persisted on every execution and quota row.

Returns the name passed to the constructor, not a constant derived from the
type. One deployment can register the same plugin twice against two accounts.

### `Capabilities`

What the provider can currently do. Called by a refresh loop, never on the
request path, so implementations may call their platform here.

Set `ObservedAt`. Admission reads an unobserved snapshot as a provider nothing
is known about.

Report constants where nothing varies by account. Cloud Run's limits are fixed
by documentation, so asking Google what Cloud Run is would be a round trip to
learn nothing.

### `Submit`

Dispatches a task under an ID Vagabond has already recorded.

- The ID is supplied, not returned, so it exists durably before the call. A
  crash mid-call leaves a row to reconcile rather than an orphaned run.
- The ID is the idempotency key. A retry with the same ID must not start a
  second run.
- Derive the platform's resource name from the ID. That is what lets a later
  call find the work without the plugin remembering anything.

Return a `Submission`:

| Field | Meaning |
|---|---|
| `ProviderID` | The platform's identifier for the work |
| `State` | `accepted`, `running`, `succeeded`, or `failed` |
| `Result` | Set only when the work finished inside `Submit` |

A provider that merely queued the work reports `accepted`. One that already
finished reports a terminal state and fills in `Result`. The dispatcher calls
`Submission.Validate`, which rejects a terminal state with no result and a
result alongside a non-terminal state.

If `Submit` creates a resource and a later call in the same method fails, clean
the resource up.

### `Status`

Where a previously submitted execution has reached.

Providers whose work finishes inside `Submit` embed `plugin.StatusNotSupported`.

### `Result`

What a finished execution produced. Called once, after `Status` reports a
terminal state.

Where the result lives is the plugin's problem. Platforms disagree: an exit code
may sit on a task resource while logs sit in a separate product.

A failure here is not a failed execution, so it is never rerouted.

Providers that returned everything from `Submit` embed
`plugin.ResultNotSupported`.

### `Cancel`

Stops a running execution.

- Cancelling an execution that already finished is not an error. The caller's
  intent, that it not be running, is satisfied.
- If cancelling destroys what `Result` reads, document it. Cloud Run keeps the
  exit code on a task deleted along with its job.

Providers with no way to stop work embed `plugin.CancelNotSupported`.

## Optional interfaces

Two capabilities are declared by implementing an interface. Nothing registers
them; dispatch type-asserts and skips what is absent.

### `plugin.LogStreamer`

```go
StreamLogs(ctx context.Context, id execution.ID, w io.Writer) error
```

Writes output to `w` as it arrives, returning when the execution ends, the
stream closes, or `ctx` is cancelled.

A live view, never the record. `Result` stays authoritative, and a failure here
never fails an execution — the caller is watching a build, and losing that view
is not a reason to abandon work the provider is still doing.

Once the stream is open, treat every way it stops as the end rather than
classifying read errors. A cut connection, a closed body and a cancelled context
all mean the same thing to the caller.

Providers whose work finishes inside `Submit` have nothing to stream and should
not implement this.

### `plugin.Releaser`

```go
Release(ctx context.Context, id execution.ID) error
```

Deletes whatever the finished execution left behind. Implement it when an
execution requires a resource that outlives it — Cloud Run has no ad-hoc run, so
every execution needs a Job that persists until deleted.

Called after `Result`, because releasing may destroy what the result reads. Best
effort: a failure does not fail the execution.

Providers with nothing to release do not implement it.

## Errors

Every returned error should be a `*plugin.Error`.

| Constructor | Class | Reroutable |
|---|---|---|
| `plugin.Infrastructure(err)` | `infrastructure` | yes |
| `plugin.Internal(err)` | `internal` | no |

`plugin.ClassifyHTTP(status, retryAfter, err)` covers the usual HTTP shape.

Misattributing a Vagabond bug to a provider is the worst available outcome: it
would be rerouted, fail identically everywhere, spend capacity at each stop, and
never self-correct.

`plugin.ErrUnsupported` reports an operation the platform has no equivalent for.
It is distinct from a failure — nothing went wrong, and retrying will not help.

## Configuration

A plugin decodes its own `config` block. Vagabond passes the block undecoded as
an `hcl.Body`.

```go
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics)
```

- Return `hcl.Diagnostics`, not `error`, so a typo is reported against the line
  it was written on.
- A missing block gets one diagnostic naming the block, not one per absent
  attribute. The fix is different.
- `gohcl` refuses an absent attribute. Validate for present-and-empty, which
  would otherwise build a URL with a hole in it.

Declaring plugin fields in `internal/config` would make every provider carry
every other provider's fields.

## Credentials

Vagabond resolves the `credentials` block to bytes before the constructor is
called. A plugin never learns whether the secret came from a file, an
environment variable, or a command.

Fail at construction if the credential cannot be parsed, not at first dispatch.

## Registration

Add the type to `internal/registry/build.go`:

```go
var providerTypes = []string{
    gcp.Type,
    // ...
}

func Build(ctx context.Context, providerType string, settings Settings) (plugin.Provider, hcl.Diagnostics) {
    switch providerType {
    case gcp.Type:
        p, diags := gcp.New(ctx, settings.Name, settings.Config, settings.Credentials)
        if diags.HasErrors() {
            return nil, diags
        }
        return p, diags
    // ...
    }
}
```

Return through the interface, not concretely. A nil `*gcp.Provider` from a
failed build would otherwise become a non-nil `plugin.Provider` that panics
later.

`internal/registry` is the one package that knows every provider by name, which
is why it lives outside `internal/scheduler` — depguard forbids the scheduler
from importing a provider at all.

## Testing

Make the platform's base URL a field on the provider, defaulting to the real
endpoint, and point it at `httptest.NewServer` in tests.

```go
p := &Provider{
    http:    server.Client(),
    runURL:  server.URL,
    logsURL: server.URL,
}
```

This exercises request building, headers, JSON encoding and status
classification against a fake platform. A mocked HTTP client asserts that `Do`
was called with the right arguments, which is a weaker claim than that the
platform would have understood the request.

Add a live round-trip test gated on an environment variable, skipped by default,
so the standard suite needs no cloud account. The offline tests prove the plugin
builds requests the platform would understand; the live test proves the platform
agrees, which is what catches a renamed field.

Register cleanup with its own context. `t.Context()` is cancelled before
`t.Cleanup` functions run, so a delete on it leaks the resource it was
registered to remove.
