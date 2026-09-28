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
registered in `internal/registry`.

| Reference | Package | Shows |
|---|---|---|
| Cloud Run Jobs | `internal/providers/gcp` | Asynchronous family, `LogStreamer`, `Releaser`, raw HTTP against `httptest` |
| AWS Lambda | `internal/providers/aws` | Synchronous family, the `NotSupported` embeds, `Result.Billed`, a cloud SDK against `httptest` |
| Pool | `internal/providers/pool` | `Live`, `MemberSubmitter`, `Capabilities.Members` |
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
- A plugin may import `internal/plugin`, `internal/execution`, `internal/job`
  and `internal/quota`. It has no store access.
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
- **The ID is the key.** `execution.ID` is a UUIDv7 minted and stored before
  `Submit`. Derive every platform name from it, so any process can find the
  work later without plugin state.
- **Return `*plugin.Error`.** An unclassified error is treated as not
  reroutable.
- **Honour `ctx`.** Dispatch cancels it when the caller gives up.

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
  second run. Naming the platform resource after the ID gets this for free on
  platforms that reject a duplicate name.
- **Clean up on partial failure:** if `Submit` creates a resource and a later
  step fails, delete it before returning.
- **Validate before calling out:** a task missing a required `config` key is
  an `Internal` error, raised before any request.

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

A submission with a `Result` is **synchronous**: dispatch records the result,
calls `Release` if implemented, and never calls `Status` or `Result`. Otherwise
dispatch polls `Status`.

### `Status`

Where an execution has reached. Polled by dispatch at 2s, doubling to a 15s
ceiling; also called by the quota reaper and when a dispatch is resumed.

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
  record of the ID. The reaper then drops the quota reservation as never having
  run. A lookup that merely failed must not wrap it.
- Set `StartedAt` and `EndedAt` on terminal states. The reaper charges an
  abandoned execution the declared shape over `EndedAt - StartedAt`.

### `Result`

What a finished execution produced. Called once, after `Status` reports a
terminal state, and before `Release`.

| `execution.Result` field | Meaning |
|---|---|
| `ID` | The `id` argument |
| `ExitCode` | `*int`; nil means the driver has no process to exit |
| `Duration` | How long the work ran; settles the quota charge when `Billed` is nil |
| `Billed` | `*quota.Execution` the platform reported charging; nil when it reports nothing |
| `Logs` | Combined output; the execution record keeps the last 64 KiB |
| `LogsTruncated` | The plugin cut the output short |

- A non-zero exit is not an error. Return the result; the state is `failed`.
- `Result.Succeeded()` is true for a nil `ExitCode`. Return nil only for
  drivers with no process; a container killed before it ran has no exit code,
  and dispatch will count that result as a success.
- A failed log fetch should not fail `Result`. Return the exit code without
  logs.
- Set `Billed` when the platform reports its charge, as Lambda's `REPORT` line
  does. Settlement uses it in full in place of declared shape times `Duration`.

### `Cancel`

Stops a running execution. Called when the dispatching caller gives up (on a
fresh context with a 30s timeout) and for `DELETE /v1/execution/{id}` on an
execution no local dispatch is running.

- Cancelling a finished or already deleted execution returns `nil`.
- If cancelling destroys what `Result` reads, document it on the provider page.

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

The reaper reads `ErrUnsupported` from `Status` as "it ran, for an unknown
time" and charges the reserved amount.

## Optional interfaces

Implemented or not; nothing registers them. Callers type-assert and skip what
is absent.

| Interface | Method | Called by | When |
|---|---|---|---|
| `LogStreamer` | `StreamLogs(ctx context.Context, id execution.ID, w io.Writer) error` | Dispatch | Alongside the poll loop, when the caller asked for live output |
| `Releaser` | `Release(ctx context.Context, id execution.ID) error` | Dispatch | After `Result` (or after a synchronous `Submit`) |
| `Live` | `LiveCapabilities() (Capabilities, error)` | Registry | On every plan, in place of the refreshed snapshot |
| `MemberSubmitter` | `SubmitTo(ctx context.Context, id execution.ID, task *job.Task, members []string) (Submission, error)` | Dispatch | In place of `Submit`, when admission passed specific members |

### `LogStreamer`

- Write output to `w` as it arrives. Return when the execution ends, the stream
  closes or `ctx` is cancelled.
- Dispatch cancels `ctx` after the execution reaches a terminal state and a
  5-second linger, then waits for `StreamLogs` to return.
- The return value is discarded. A broken stream never fails an execution.
- Once the stream is open, treat every way it stops as the end rather than as
  an error.
- If anything was written, the CLI does not print `Result.Logs` again. If
  nothing was written, it does.
- Synchronous providers have nothing to stream and should not implement it.

### `Releaser`

- Deletes a resource the execution left behind, such as Cloud Run's Job.
- Called on a fresh context with a 30-second timeout, so it runs even after the
  caller gave up. The error is discarded.
- Called after `Result` because releasing may destroy what `Result` reads.

### `Live`

- For capabilities that change between refreshes and cost nothing to read.
- A live provider is skipped by `Refresh`. `LiveCapabilities` is read on every
  plan; an error marks the provider unhealthy for that plan.
- It must not do I/O or block: the registry calls it under its read lock while
  building admission inputs.

### `MemberSubmitter`

- For a provider made of members, each advertised in `Capabilities.Members` and
  judged by admission on its own.
- `members` is the `Member.Name` values admission passed for this task. When
  it is empty dispatch calls `Submit` instead.

## Capabilities

`plugin.Capabilities` is everything admission may know about a provider. It
decides without calling the provider.

| Field | Type | Attribute | Admission use | Zero value means |
|---|---|---|---|---|
| `Drivers` | `[]job.DriverName` | `provider.drivers` | `driver-unsupported` | Runs nothing |
| `Architectures` | `[]job.Arch` | `provider.architecture` | `arch-unsupported` | Offers no architecture |
| `MaxResources.CPU` | `int`, millicores | `provider.max_cpu` | `resources-exceeded` | No limit |
| `MaxResources.Memory` | `int`, MiB | `provider.max_memory` | `resources-exceeded` | No limit |
| `MaxDuration` | `time.Duration` | `provider.max_duration` | `duration-exceeded` | No limit |
| `InternetEgress` | `bool` | `provider.internet` | `network-unsupported` | No egress |
| `PrivateNetwork` | `bool` | `provider.private_network` | `network-unsupported` | No private network |
| `ArbitraryImages` | `bool` | `provider.arbitrary_images` | `image-unsupported` for a task naming `image` | Runs only its own images |
| `EstimatedCost` | `job.Cost` | `provider.estimated_cost` | `cost-policy` | Free |
| `ObservedAt` | `time.Time` | none | Shown in plans | Never observed |
| `Members` | `[]plugin.Member` | none | Each member judged separately | Not a pooled provider |

Reason codes are listed in [Scheduling](scheduling.md#reason-codes).

- **Units:** CPU in millicores (1000 = 1 vCPU), memory in MiB, matching
  `job.Resources`.
- **Zero means unlimited** for the resource and duration limits. A platform
  that derives CPU from memory advertises the CPU its largest tier implies
  rather than 0, as Lambda advertises 6000.
- **Attributes:** the dotted names are what job `constraint` and `affinity`
  blocks match. Limits left at zero are omitted from the attribute map;
  booleans and `estimated_cost` are always present. `provider.free_quota_percent`
  is added by admission from the ledger, not by the plugin.
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
is the default mapping for HTTP APIs. The error text is prefixed
`http <status>: `.

| Status | Class | `RetryAfter` | Also wraps |
|---|---|---|---|
| 408, 429 | infrastructure | passed through | |
| 404 | internal | 0 | `plugin.ErrNotFound` |
| other 4xx | internal | 0 | |
| 5xx | infrastructure | passed through | |
| anything else | infrastructure | 0 | |

A platform that uses a status unusually builds the `*Error` directly.

### Sentinels

| Sentinel | Meaning | Who reads it |
|---|---|---|
| `plugin.ErrProvider` | Matches any `*plugin.Error` via `errors.Is` | Callers that only need "a provider call failed" |
| `plugin.ErrNotFound` | `ClassifyHTTP` saw a 404 | The plugin, to translate into `ErrUnknownExecution` or success |
| `plugin.ErrUnknownExecution` | The platform has no record of the execution | Quota reaper: drops the reservation |
| `plugin.ErrUnsupported` | The platform has no such operation | Quota reaper: charges the reservation |
| `plugin.ErrInvalidSubmission` | `Submission.Validate` failed | Dispatch |

### How dispatch reacts

| Where | Outcome | Execution record | Next step |
|---|---|---|---|
| `Submit` returns `infrastructure` | No run | `failed`, failure `infrastructure` | Next ranked provider, if the task's `retry` block allows reroute and attempts remain |
| `Submit` returns `internal` or unclassified | No run | `failed`, failure `internal` | Task stops; job fails |
| `Submission.Validate` fails | Treated as `internal` | `failed`, failure `internal` | Task stops |
| `Status` returns an error while polling | Watch ends; the run is not cancelled | `lost` | Reroute if `infrastructure`, else stop |
| Caller gives up while polling | `Cancel` called | `cancelled` | Stop |
| `Status` reports `cancelled` | `Result` read, `Release` called | `cancelled` | Treated as `infrastructure`: reroute if allowed |
| `Result` returns an error | Nothing released | Terminal state from `Status`, no result | Reroute if `infrastructure`, else stop |
| `Status` reports `succeeded` or `failed`, `Result` returns | `Release` called | Terminal state and result | Task ends; a non-zero exit stops the job |
| `StreamLogs` or `Release` returns an error | Ignored | Unchanged | None |

Retry budgets, backoff and reroute rules are in [Dispatch](dispatch.md).

## Configuration

A plugin decodes its own `config` block. Vagabond hands it over undecoded, as
the `hcl.Body` in `registry.Settings.Config`, so plugin fields never appear in
`internal/config`.

```go
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics)
```

- **Nil body:** the provider block has no `config` block. Return one
  diagnostic naming the block and the attributes it needs, not one per missing
  attribute.
- **Decode with `gohcl.DecodeBody(body, nil, &cfg)`.** It reports missing and
  unknown attributes against source ranges.
- **Validate present-but-empty strings** separately; `gohcl` accepts `""`.
- **Return `hcl.Diagnostics`, never `error`,** so the CLI prints the file and
  line.

Driver `config` in the task is read with helpers on `*job.Task`:

| Helper | Returns |
|---|---|
| `task.ConfigString(name)` | `(string, bool)`: a string attribute evaluated against the job's variables |
| `task.ConfigStrings(name)` | `([]string, bool)`: a list of strings; false when absent or empty |
| `task.Environment()` | `(map[string]string, hcl.Diagnostics)`: the `env` block plus `VAGABOND_META_<KEY>` per job `meta` key |
| `job.ConfigImage` | The `"image"` key, which admission also reads |

## Credentials

The registry resolves the `credentials` block before calling the constructor
and passes bytes in `registry.Settings.Credentials`. The plugin never learns
whether they came from a file, an environment variable or a command, and they
are resolved once per registry build.

- `nil` when the provider block has no `credentials` block. Either fail with a
  diagnostic or fall back to the platform's default chain, and document which.
- Parse the credential in the constructor and return an `Unusable credential`
  diagnostic when it fails, so a bad secret stops startup.
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
- `Status.StartedAt` and `EndedAt`, which price an abandoned reservation.
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

Add both to `internal/registry/build.go`:

```go
var providerTypes = []string{
    gcp.Type,
    aws.Type,
    pool.Type,
    example.Type,
    // fakes
}

func Build(ctx context.Context, providerType string, settings Settings) (plugin.Provider, hcl.Diagnostics) {
    switch providerType {
    // ...
    case example.Type:
        p, diags := example.New(ctx, settings.Name, settings.Config, settings.Credentials)
        if diags.HasErrors() {
            return nil, diags
        }

        return p, diags
    // ...
    }
}
```

- Return `nil` explicitly on error. Returning a nil `*example.Provider` through
  the `plugin.Provider` interface produces a non-nil interface that panics on
  first use.
- `providerTypes` feeds the `Unknown provider type` diagnostic, which lists
  every known type.
- `registry.Settings` also carries `Nodes`, the server's connected agents,
  which only the `pool` type uses.

Nothing else is wired by hand. The server and CLI build the registry from
configuration, refresh it, and hand each provider to admission and dispatch.

## Skeleton

A minimal asynchronous provider that compiles against the current interfaces.
It targets an imaginary JSON API with `POST /runs`, `GET /runs/{name}`,
`GET /runs/{name}/result`, `POST /runs/{name}:stop` and `DELETE /runs/{name}`.

```go
// Package example dispatches container tasks to the Example batch API.
package example

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
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
	if body == nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing provider configuration",
			Detail:   fmt.Sprintf("Provider %q is an %s provider and declares no config block.", name, Type),
		}}
	}

	var cfg Config
	if diags := gohcl.DecodeBody(body, nil, &cfg); diags.HasErrors() {
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

// Capabilities reports fixed platform limits; nothing varies by account.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{
		Drivers:         []job.DriverName{job.DriverContainer},
		Architectures:   []job.Arch{job.ArchAMD64},
		MaxResources:    plugin.Resources{CPU: 4000, Memory: 8192},
		MaxDuration:     time.Hour,
		InternetEgress:  true,
		ArbitraryImages: true,
		ObservedAt:      time.Now(),
	}, nil
}

// Submit starts a run named after the execution ID, so a retry of the same
// ID collides rather than starting a second run.
func (p *Provider) Submit(ctx context.Context, id execution.ID, task *job.Task) (plugin.Submission, error) {
	image, ok := task.ConfigString(job.ConfigImage)
	if !ok {
		return plugin.Submission{}, plugin.Internal(fmt.Errorf("task %q names no image", task.Name))
	}

	env, diags := task.Environment()
	if diags.HasErrors() {
		return plugin.Submission{}, plugin.Internal(fmt.Errorf("task %q env: %s", task.Name, diags.Error()))
	}

	run := map[string]any{"name": runName(id), "image": image, "env": env}
	if err := p.call(ctx, http.MethodPost, "/runs", run, nil); err != nil {
		return plugin.Submission{}, err
	}

	return plugin.Submission{ProviderID: runName(id), State: execution.StateAccepted}, nil
}

// Status maps the platform's run state onto Vagabond's.
func (p *Provider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	var run struct {
		State   string    `json:"state"`
		Started time.Time `json:"started"`
		Ended   time.Time `json:"ended"`
	}

	if err := p.call(ctx, http.MethodGet, "/runs/"+runName(id), nil, &run); err != nil {
		// Only the platform saying "no such run" wraps ErrUnknownExecution.
		if errors.Is(err, plugin.ErrNotFound) {
			return execution.Status{}, plugin.Internal(fmt.Errorf("%w: %s", plugin.ErrUnknownExecution, id))
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

	return execution.Status{
		ID: id, State: state, ProviderID: runName(id),
		StartedAt: run.Started, EndedAt: run.Ended, UpdatedAt: time.Now(),
	}, nil
}

// Result returns the exit code and output of a finished run.
func (p *Provider) Result(ctx context.Context, id execution.ID) (*execution.Result, error) {
	var out struct {
		ExitCode *int    `json:"exit_code"`
		Seconds  float64 `json:"seconds"`
		Logs     string  `json:"logs"`
	}

	if err := p.call(ctx, http.MethodGet, "/runs/"+runName(id)+"/result", nil, &out); err != nil {
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
	err := p.call(ctx, http.MethodPost, "/runs/"+runName(id)+":stop", struct{}{}, nil)
	if errors.Is(err, plugin.ErrNotFound) {
		return nil
	}

	return err
}

// Release deletes the run record once dispatch has read the result.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	err := p.call(ctx, http.MethodDelete, "/runs/"+runName(id), nil, nil)
	if errors.Is(err, plugin.ErrNotFound) {
		return nil
	}

	return err
}

// runName derives the platform's name for an execution.
func runName(id execution.ID) string { return "vagabond-" + id.String() }

// call sends one JSON request and classifies any failure.
func (p *Provider) call(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return plugin.Internal(fmt.Errorf("encoding request: %w", err))
		}

		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return plugin.Internal(fmt.Errorf("building request: %w", err))
	}

	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		// Never reached the platform: its failure, not ours.
		return plugin.Infrastructure(fmt.Errorf("%s %s: %w", method, path, err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return plugin.ClassifyHTTP(resp.StatusCode, 0, fmt.Errorf("%s %s: %s", method, path, msg))
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return plugin.Internal(fmt.Errorf("decoding response: %w", err))
	}

	return nil
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

```go
// Raw HTTP, as internal/providers/gcp does.
p := &Provider{
    name:    "gcp-cloud-run",
    cfg:     &Config{Project: "test-project", Region: "us-central1", RuntimeServiceAccount: "runtime@test-project.iam.gserviceaccount.com"},
    http:    server.Client(),
    runURL:  server.URL,
    logsURL: server.URL,
}

// An SDK, as internal/providers/aws does.
client := lambda.New(lambda.Options{
    Region:       "us-east-1",
    BaseEndpoint: sdkaws.String(srv.URL),
    Credentials:  credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
    Retryer:      sdkaws.NopRetryer{},
    HTTPClient:   srv.Client(),
})
```

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
| Result survives a failed log fetch | Exit code present, logs empty |

**Compile-time assertions** for every interface the plugin means to satisfy:

```go
var (
    _ plugin.Provider    = (*Provider)(nil)
    _ plugin.Releaser    = (*Provider)(nil)
    _ plugin.LogStreamer = (*Provider)(nil)
)
```

An optional interface is found by type assertion, so a method with a slightly
wrong signature compiles, is silently never called, and only this assertion
catches it.

**Live tests.** Add a round-trip test against the real platform, gated on an
environment variable and skipped when it is unset, so `make test` needs no
cloud account (see `internal/providers/gcp/live_test.go`). Register the
cleanup on its own context: `t.Context()` is cancelled before `t.Cleanup`
functions run, so a delete on it leaks the resource.

**Without the platform.** `internal/plugin` exports fakes for tests of code
above the boundary: `NewFakeContainerProvider`, `NewFakeFunctionProvider` and
`NewFakeWorkerProvider`, with fixture capabilities from `FixtureContainer`,
`FixtureFunction` and `FixtureWorker`. They are also registered as the
`fake-container`, `fake-function` and `fake-worker` types, which the
integration tests (`make integration-test`) and `examples/config.hcl` use.

**Before a PR:** `make check` runs formatting, vet, lint (including depguard),
the race-enabled tests and govulncheck.
