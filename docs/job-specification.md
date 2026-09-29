---
title: "Job Specification"
linkTitle: "Job specification"
seoTitle: "Job Specification: Job File Reference"
description: "Every block and attribute a job file accepts: routing, constraints, tasks, drivers, resources, retry and parameters, with defaults and validation."
weight: 210
---

A job file is HCL and declares exactly one `job`. The conventional name is
`<name>.vagabond.hcl`. The job states what to run and what it needs; the
server decides where it runs (see [scheduling](scheduling.md)).

Check a file locally with [`vagabond job validate`](cli.md#job-validate). The
same parse and validation runs again on the server for `job plan`, `job run`,
`job register` and `job dispatch`.

## Structure

```hcl
job "<name>" {
  namespace = "<namespace>"
  type      = "batch"

  meta { ... }            # job metadata, strings
  parameterized { ... }   # metadata a dispatch must or may supply

  routing {
    strategy     = "free-first"
    providers    = [...]
    max_cost_usd = 0

    constraint { ... }    # repeatable, hard requirement
    affinity { ... }      # repeatable, preference
  }

  task "<name>" {         # one or more, run in declaration order
    driver            = "container"
    working_directory = "/workspace"
    timeout           = "15m"

    config { ... }        # driver-specific
    env { ... }
    source { ... }
    resources { ... }
    network { ... }
    execution { ... }

    retry {
      backoff { ... }
    }
  }
}
```

Every block except `task` is optional, and at most one of each non-repeatable
block is accepted. An attribute or block not listed on this page is a decode
error:

```text
Error: Unsupported argument

  on build.vagabond.hcl line 14, in job "build":
  14:     colour = "blue"

An argument named "colour" is not expected here.
```

## `job` block

```hcl
job "go-test" {
  # Optional. Omit to take -namespace / $VAGABOND_NAMESPACE, then "default".
  namespace = "ci"

  # Optional. "batch" is the only type.
  type = "batch"

  task "test" { ... }
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| label | string | yes | — | Job name. Identifies the job in the registry, plans and execution records |
| `namespace` | string | no | see below | Namespace the job runs and registers in |
| `type` | string | no | `batch` | Scheduling model. `batch` runs each task to completion |

**Namespace resolution:** the server takes the job's `namespace`, else the
CLI's `-namespace` (or `$VAGABOND_NAMESPACE`), else `default`. The `default`
namespace always exists; any other must be declared in the server
[configuration](configuration.md).

| Situation | Result |
|---|---|
| Job names `ci`, no flag | Runs in `ci` |
| Job names `ci`, `-namespace ci` | Runs in `ci` |
| Job names `ci`, `-namespace dev` | `Error: job "go-test" names namespace "ci", but "dev" was requested; remove one` |
| Namespace not declared on the server | `Error: namespace "ci" is not declared in the configuration` |
| `namespace = ""` | Validation error `Empty namespace in "go-test"` |

**Validation:**

| Diagnostic | Cause |
|---|---|
| `Empty specification` | The file declares no `job` |
| `Too many jobs` | More than one `job` block. Detail lists the names |
| `Missing job name` | Empty label |
| `Unknown job type in "<job>"` | `type` other than `batch` |
| `No tasks in "<job>"` | No `task` block |
| `Duplicate task name in "<job>"` | Two tasks share a label |

## `meta` block

Key/value strings carried with the job. Every key reaches each task as the
environment variable `VAGABOND_META_<KEY>` (see
[`parameterized`](#parameterized-block)).

```hcl
meta {
  project = "example"
  purpose = "ci"
}
```

- Keys are free-form. Values must be strings or convert to one (numbers and
  bools do). A list or object is an error:

  ```text
  Error: Invalid value

    on build.vagabond.hcl line 3, in job "build":
     3:     tags = ["x"]

  The value for "tags" must be a string: string required, but have tuple.
  ```

- A value supplied with `-meta` overrides the same key here.

## `parameterized` block

Declares the metadata a submission supplies. Values are passed with
`-meta <key>=<value>` on `job validate`, `job plan`, `job run` and
`job dispatch`, or the `Meta` field of the [API](api.md).

```hcl
parameterized {
  # Must be supplied on every run and dispatch.
  meta_required = ["version"]

  # May be supplied; a dispatch refuses any key in neither list.
  meta_optional = ["target"]
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `meta_required` | list(string) | no | `[]` | Keys every submission must supply |
| `meta_optional` | list(string) | no | `[]` | Keys a submission may supply |

Both lists are read before anything else in the file is evaluated, so they
must be literal lists of strings; they cannot reference `${meta.*}`. A
non-string entry is `Invalid metadata key: Every entry in meta_required must be a string.`

### Supplying values

| Command | Metadata rule |
|---|---|
| `job validate`, `job plan <file>`, `job run` | Every `meta_required` key must be supplied. Keys outside both lists are accepted |
| `job register` | No values. Each key in either list stands as its own literal text (`${meta.version}` becomes the string `${meta.version}`) so the file validates in full |
| `job dispatch`, `job plan <name>` | Job with no `parameterized` block: no metadata at all. Otherwise every key must be in one of the lists, and every `meta_required` key supplied |

A missing required key fails before any other evaluation, pointing at the
declaration:

```text
Error: Unset metadata for job "go-test"

  on go-test.vagabond.hcl line 20, in job "go-test":
  20:     meta_required = ["version"]

This job requires metadata that was not supplied: version. Pass each one with
-meta <key>=<value>.
```

Dispatch errors, returned by the server:

| Error | Cause |
|---|---|
| `job "<job>" is not parameterized, so dispatch takes no metadata` | `-meta` given for a job with no `parameterized` block |
| `job "<job>" does not declare metadata <keys>; declare it in meta_required or meta_optional` | Key in neither list |
| `job "<job>" requires metadata that was not supplied: <keys>` | Required key missing |

The `-meta` flag itself rejects a pair without `=` (`expected key=value, got "a"`),
an empty key, and a key given twice (`key "a" was given more than once`).

### Interpolation

`${meta.<key>}` is available in every attribute of the job, including inside
`config`, `env` and `meta`, except the `parameterized` lists and block labels.
It resolves against supplied values (`-meta`) only, not against the job's own
`meta` block. A reference to a key that was not supplied fails with its
position, whether the key is misspelled or only defined in `meta`:

```text
Error: Unsupported attribute

  on build.vagabond.hcl line 9, in job "build":
   9:       image = "golang:${meta.vrsion}"

This object does not have an attribute named "vrsion".
```

Declare every referenced key in `meta_required` or `meta_optional`, so that
`job register` and `job dispatch` accept it. A reference to an
`meta_optional` key that a run did not supply fails the same way.

### `VAGABOND_META_<KEY>` variables

Every task's environment receives one variable per metadata key: the job's
`meta` block, overridden by supplied values.

- The key is upper-cased and every character outside `[A-Z0-9_]` becomes `_`:
  `build-id` becomes `VAGABOND_META_BUILD_ID`.
- An `env` entry with the same name wins over the generated variable.

## `routing` block

The job's provider selection policy. Omitting the block admits every provider
and uses every default.

```hcl
routing {
  # Base scorer for ranking. "free-first" is the only strategy.
  strategy = "free-first"

  # Allowlist. Order carries no meaning; ranking decides.
  providers = ["gcp-cloud-run", "homelab"]

  # Ceiling per execution. 0 refuses any provider that charges.
  max_cost_usd = 0

  constraint {
    attribute = "provider.architecture"
    operator  = "set_contains"
    value     = "amd64"
  }

  affinity {
    attribute = "provider.free_quota_percent"
    operator  = ">"
    value     = "50"
    weight    = 75
  }
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `strategy` | string | no | `free-first` | Base scorer used to rank admitted providers. See [ranking](scheduling.md#ranking) |
| `providers` | list(string) | no | all providers | Provider names the job may use. Empty or omitted admits all |
| `max_cost_usd` | integer | no | `0` | Highest per-execution estimated cost accepted |
| `constraint` | block | no | — | Hard requirement, repeatable |
| `affinity` | block | no | — | Preference, repeatable |

**`providers`:** an allowlist, not a failover chain. A provider absent from a
non-empty list is rejected with `not-allowlisted`; the rest are admitted or
rejected on their own merits and ranked by score. Names are the labels of
`provider` blocks in the server configuration and are not checked at
validation; an unknown name matches nothing.

**`max_cost_usd`:** a whole number (`0.5` is a decode error:
`value must be a whole number`). A provider whose `provider.estimated_cost`
exceeds it is rejected with `cost-policy`. Every provider publishes an
estimated cost of `0`. The value also decides how quota pools apply:

| `max_cost_usd` | Quota |
|---|---|
| `0` or omitted | A provider whose quota pools cannot fit the task is rejected with `quota-exhausted`, and dispatch refuses a reservation that no longer fits |
| Greater than `0` | Neither refuses. The task runs past a spent allowance and its usage is still charged (see [quotas](quotas.md)) |

A negative value is accepted and rejects every provider with `cost-policy`.

**Validation:**

| Diagnostic | Cause |
|---|---|
| `Unknown routing strategy in "<job>"` | `strategy` other than `free-first` |

### `constraint` block

A hard requirement. A provider that fails any constraint is rejected with
`constraint-unmet`, naming the first constraint it failed and the value it
publishes. Constraints are evaluated in file order.

```hcl
constraint {
  attribute = "provider.max_duration"
  operator  = ">="
  value     = "30m"
}

constraint {
  attribute = "provider.meta.region"
  operator  = "is_set"
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `attribute` | string | yes | — | Attribute to test. See [attributes](#attributes) |
| `operator` | string | yes | — | Comparison. See [operators](#operators) |
| `value` | string | for comparisons | `""` | Right-hand side. Must be omitted for `is_set` and `is_not_set` |

Plan output for a failed constraint:

```text
gcp-cloud-run  rejected  constraint-unmet  The job requires provider.architecture set_contains "arm64" and this provider publishes "amd64".
```

### `affinity` block

A preference. A provider that fails an affinity stays admitted and scores
lower. Affinities never reject.

```hcl
affinity {
  attribute = "provider.meta.region"
  operator  = "="
  value     = "us-central1"
  weight    = 25
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `attribute` | string | yes | — | Attribute to test |
| `operator` | string | yes | — | Comparison |
| `value` | string | for comparisons | `""` | Right-hand side |
| `weight` | integer | no | `50` | Relative importance. Must be positive |

**Scoring:** the affinity score is the sum of the weights of satisfied
affinities divided by the sum of all weights, from 0 to 1. It is averaged with
the strategy's base score into the plan's `score`. A job with no affinities
has no affinity scorer at all, so it does not halve every score. See
[ranking](scheduling.md#ranking).

**Validation (constraints and affinities):**

| Diagnostic | Cause |
|---|---|
| `Unknown constraint attribute in "<job>"` / `Unknown affinity attribute in "<job>"` | Attribute not published and not under an open prefix. Detail lists the known attributes |
| `Unknown constraint operator in "<job>"` / `Unknown affinity operator in "<job>"` | Operator outside the table below |
| `Missing constraint value in "<job>"` / `Missing affinity value ...` | Comparison operator with no `value` |
| `Unexpected constraint value in "<job>"` / `Unexpected affinity value ...` | `value` given with `is_set` or `is_not_set` |
| `Invalid affinity weight in "<job>"` | `weight` zero or negative |

The server repeats the attribute check at admission; a job reaching it with an
unknown attribute is rejected by every provider with `attribute-unknown`.

### Operators

Every attribute is a string. The operator decides how it is read.

| Operator | Holds when | Attribute absent |
|---|---|---|
| `=` | Exact string equality | false |
| `!=` | Not exactly equal | **true** |
| `<` `<=` `>` `>=` | Ordered comparison, see below | false |
| `set_contains` | The comma-separated attribute has `value` as a member; whitespace around members is ignored | false |
| `is_set` | The provider publishes the attribute; takes no `value` | false |
| `is_not_set` | The provider does not publish it; takes no `value` | **true** |

Ordering operators read both sides as the first of these that parses on both
sides:

1. Go durations (`15m`, `1h30m`). `provider.max_duration` is published as
   `15m0s` and compares correctly against `value = "10m"`.
2. Integers.
3. Floats.
4. Strings, compared lexically.

A bare number is not a valid duration, so numeric attributes are never read as
durations.

`provider.architecture` and `provider.drivers` hold sets. Use `set_contains`;
`=` asks whether the set is exactly that one value.

### Attributes

A constraint or affinity can name three kinds of attribute:

| Prefix | Source | Checked at validation |
|---|---|---|
| `provider.<name>` | Published by Vagabond from the provider's capabilities and quota | Yes, against the table below |
| `provider.meta.<key>` | The `meta` block of the provider's configuration block | No |
| `node.label.<key>` | An agent's `-label` flags, present only while a pool's node is judged | No |

Published attributes:

| Attribute | Type | Published | Description |
|---|---|---|---|
| `provider.architecture` | set | When the provider lists any | Architectures offered, e.g. `amd64,arm64` |
| `provider.drivers` | set | When the provider lists any | Drivers offered, e.g. `container` |
| `provider.max_duration` | duration | When the provider states a limit | Longest task accepted, e.g. `15m0s` |
| `provider.max_cpu` | integer | When the provider states a limit | Millicores available to one task |
| `provider.max_memory` | integer | When the provider states a limit | MiB available to one task |
| `provider.internet` | bool | Always | `true` when the provider offers outbound internet |
| `provider.private_network` | bool | Always | `true` when the provider offers private network access |
| `provider.arbitrary_images` | bool | Always | `true` when any container image may be run |
| `provider.estimated_cost` | integer | Always, including `0` | Estimated cost per execution |
| `provider.free_quota_percent` | integer | Always | Tightest remaining allowance, 0–100, see below |

A limit the provider never stated is absent rather than `0`, so `is_set` and
`is_not_set` distinguish "no limit stated" from "a limit of zero".

**`provider.free_quota_percent`:** computed per task at admission. It is the
lowest remaining percentage among the quota pools this task would charge,
across the provider's own pools and the namespace's share of them, floored to
an integer. A pool the task does not charge (for example a duration-metered
pool for a task with no `timeout`) does not lower it. A provider with no pools,
or a task that charges none, reports `100`. See [quotas](quotas.md).

**`provider.meta.*`:** from the provider block in the server configuration:

```hcl
# server configuration
provider "gcp-cloud-run" {
  type = "cloud-run"
  meta {
    region = "us-central1"
  }
}
```

```hcl
# job file
constraint {
  attribute = "provider.meta.region"
  operator  = "="
  value     = "us-central1"
}
```

**`node.label.*`:** a [pool](agent.md) provider judges each connected node
separately, and while it does, that node's labels are published under
`node.label.`. A node failing a constraint is left out of the pool's
candidates. On any provider other than a pool the attribute is absent, so `=`
fails and `!=` and `is_not_set` hold.

```hcl
constraint {
  attribute = "node.label.disk"
  operator  = "="
  value     = "ssd"
}
```

## `task` block

One unit of work. A job runs its tasks one after another in declaration order
and stops at the first task that fails or gets no answer. Each task is
admitted, ranked and dispatched independently, so tasks of one job may run on
different providers.

```hcl
task "test" {
  driver            = "container"
  working_directory = "/workspace"
  timeout           = "15m"

  config {
    image   = "golang:1.27"
    command = "go"
    args    = ["test", "./..."]
  }
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| label | string | yes | — | Task name, unique within the job |
| `driver` | string | yes | — | Execution contract: `container`, `function` or `worker` |
| `working_directory` | string | no | image default | Directory the command runs in. Applied by the pool provider only |
| `timeout` | duration | no | none | Maximum runtime. See [`timeout`](#timeout) |
| `config` | block | depends on driver | — | Driver configuration |
| `env` | block | no | — | Environment variables |
| `source` | block | no | — | Source repository reference |
| `resources` | block | no | — | CPU and memory required |
| `network` | block | no | — | Connectivity required |
| `execution` | block | no | — | Architecture and privilege required |
| `retry` | block | no | — | Retry and reroute policy |

**Validation:**

| Diagnostic | Cause |
|---|---|
| `Missing required argument` (`The argument "driver" is required, but no definition was found.`) | No `driver` |
| `Missing task name in "<job>"` | Empty label |
| `Unknown driver in "<job>" task "<task>"` | Driver outside `container, function, worker`. Names are lowercase and exact |

### Drivers

| Driver | Contract | Providers offering it |
|---|---|---|
| `container` | Run an arbitrary container image to completion; the exit status is the result | Cloud Run (`cloud-run`), pool (`pool`) |
| `function` | Invoke a predeployed function that implements the platform's handler contract | Lambda (`lambda`) |
| `worker` | Invoke a predeployed constrained executor, such as an edge or Wasm runtime | None; every provider rejects it with `driver-unsupported` |

A provider that does not offer the task's driver is rejected with
`driver-unsupported`.

### `config` block

Driver-specific attributes. Vagabond itself reads only `image`; each provider
reads the keys it needs and ignores the rest.

- Attributes only. A nested block is an error:

  ```text
  Error: Blocks are not supported in config

    on build.vagabond.hcl line 10, in job "build":
    10:       limits {

  A task config block holds attributes only, and "limits" is a block. Write it
  as an attribute instead, for example limits = { ... }.
  ```

- Values may reference `${meta.*}`; every reference is checked at validation.
- An unknown key is ignored silently, so a misspelled optional key has no
  effect.
- A key of the wrong type (for example a list for `command`) is treated as
  absent by the provider.

**`image` and admission:** when the task sets `image`, a provider that runs
only its own images (`provider.arbitrary_images = false`, e.g. Lambda) is
rejected with `image-unsupported`. A non-string `image` fails admission with
`Invalid image`.

#### `container` driver

Read by the Cloud Run and pool providers.

```hcl
config {
  image   = "golang:1.27"
  command = "go"
  args    = ["test", "-race", "./..."]
}
```

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `image` | string | yes | — | Image reference to pull and run |
| `command` | string | no | image entrypoint | Replaces the entrypoint with this single executable |
| `args` | list(string) | no | image default | Arguments. Each element is one argument; nothing is split on spaces |

`command` is one string, not a list. Put flags and arguments in `args`. For a
shell pipeline use `command = "sh"` and `args = ["-c", "..."]`.

A container task without `image` fails validation:
`Missing image in "<job>" task "<task>"` (`A container task runs an image, so its config block must set one.`).

#### `function` driver

Read by the Lambda provider.

```hcl
config {
  function = "arn:aws:lambda:us-east-1:123456789012:function:lint"
  args     = ["--strict"]
}
```

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `function` | string | yes | — | Function name or ARN to invoke |
| `args` | list(string) | no | — | Passed in the event as `args` |

Do not set `image` on a function task: Lambda publishes
`provider.arbitrary_images = false` and would reject the task with
`image-unsupported`.

The function is invoked synchronously with this JSON event; `args` and `env`
are omitted when empty:

```json
{
  "execution_id": "01926f3a-7c4e-7b1a-9d2e-5b8f4c1e9a2f",
  "args": ["--strict"],
  "env": {
    "CI": "true",
    "VAGABOND_META_VERSION": "1.4.2"
  }
}
```

A normal return is exit code `0`; a function error is exit code `1`.
`function` is not checked at validation. A task without it passes
validation and fails at dispatch as an internal failure
(`task "<task>" names no function`).

#### `worker` driver

No provider offers `worker`. No config keys are read.

### `env` block

Environment variables for the task, as free-form key/value pairs.

```hcl
env {
  CI          = "true"
  CGO_ENABLED = "0"
  VERSION     = "${meta.version}"
}
```

- Values may reference `${meta.*}`.
- `VAGABOND_META_<KEY>` is added for every metadata key unless `env` sets the
  same name.
- Values must be strings or convert to one: numbers and bools do. A list or
  object fails validation with `Invalid value: The value for "<NAME>" must be
  a string`, at its line, so the job is refused by `job validate`,
  `register` and `run`.

Cloud Run passes the variables sorted by name. Lambda passes them in the
event's `env` object, not as the function's process environment.

### `source` block

A source repository reference, stored with the job.

```hcl
source {
  type        = "git"
  repository  = "https://git.example.com/example/service.git"
  ref         = "${meta.version}"
  destination = "/workspace"
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `type` | string | yes | — | Source type, e.g. `git`. Not checked |
| `repository` | string | yes | — | Repository URL |
| `ref` | string | no | — | Revision |
| `destination` | string | no | — | Path |

No provider reads this block: nothing is fetched before the task starts. A
task that needs its source checks it out itself, for example from
`VAGABOND_META_VERSION`.

### `resources` block

What the task requires, in platform-neutral units.

```hcl
resources {
  cpu    = 2000  # millicores; 1000 is one vCPU
  memory = 4096  # MiB
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `cpu` | integer | no | provider default | Millicores; `1000` is one vCPU |
| `memory` | integer | no | provider default | MiB |

**Admission:** a value above the provider's `provider.max_cpu` or
`provider.max_memory` is rejected with `resources-exceeded`. An omitted value
is checked at the provider's default size where it publishes one (a pool sizes
it at 1000 millicores / 1024 MiB), and passes any provider that publishes none.

**Quota:** the declared values (0 when omitted) are what the task charges
against CPU- and memory-metered pools. See [quotas](quotas.md).

**Per provider:**

| Provider | Omitted `cpu` | Omitted `memory` | How values are applied |
|---|---|---|---|
| Cloud Run | 1000 | 2048 | CPU rounded up to whole vCPUs (minimum 1); memory in MiB. Limits 8000 millicores, 32768 MiB |
| Pool | 1000 | 1024 | Passed to the agent as the workload's cgroup limits. Limit is the largest node's capacity |
| Lambda | — | — | Not applied; the function's own configuration decides. Limits 6000 millicores, 10240 MiB |

**Validation:**

| Diagnostic | Cause |
|---|---|
| `Invalid CPU request in "<job>" task "<task>"` | `cpu` zero or negative. Omit it to state no requirement |
| `Invalid memory request in "<job>" task "<task>"` | `memory` zero or negative |
| `Unsuitable value type` (`value must be a whole number`) | Fractional value, e.g. `cpu = 1.5` |

### `timeout`

The task's maximum runtime, as a [duration](#durations).

```hcl
timeout = "15m"
```

- **Admission:** a timeout above the provider's `provider.max_duration` is
  rejected with `duration-exceeded`. With no timeout, the check is skipped.
- **Quota:** the timeout is the duration charged to duration-metered pools at
  reservation. A task with no timeout does not charge them.

| Provider | Omitted | Limit |
|---|---|---|
| Cloud Run | 24h | 24h |
| Pool | No timeout; the agent never stops the workload | none |
| Lambda | Not applied; the function's configured timeout decides | 15m |

**Validation:**

| Diagnostic | Detail |
|---|---|
| `Invalid timeout in "<job>" task "<task>"` | `Timeout "15 minutes" is not a duration. Write one as 15m, 90s, or 1h30m.` |
| `Invalid timeout in "<job>" task "<task>"` | `A timeout of zero bounds nothing. Give the task a duration, or omit the attribute to leave it unbounded.` |

### `network` block

Connectivity the task requires.

```hcl
network {
  internet = true
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `internet` | bool | no | unset | `true` requires outbound internet |
| `private` | bool | no | unset | `true` requires private network access |

Only an explicit `true` is a requirement. `false` or omitted never rejects a
provider. A provider lacking a required capability is rejected with
`network-unsupported`.

| Provider | `provider.internet` | `provider.private_network` |
|---|---|---|
| Cloud Run | `true` | `false` |
| Lambda | `true` | `false` |
| Pool | `true` | `true` |

### `execution` block

The environment the task must run in.

```hcl
execution {
  architecture = "arm64"
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `architecture` | string | no | any | `amd64` or `arm64`. A provider not offering it is rejected with `arch-unsupported` |
| `privileged` | bool | no | — | Accepted and stored. No provider or admission check reads it |

Architectures offered: Cloud Run `amd64`; Lambda `amd64`, `arm64`; a pool the
union of its connected nodes' architectures.

**Validation:** `Unknown architecture in "<job>" task "<task>"`
(`Architecture "x86_64" is not one Vagabond schedules against. Valid architectures are amd64, arm64.`).

### `retry` block

What to do when an execution gets no answer.

```hcl
retry {
  attempts = 3     # total submissions, including the first
  reroute  = true  # allow trying the next ranked provider

  backoff {
    initial = "5s"
    max     = "30s"
  }
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `attempts` | integer | no | `1` | Total submissions, including the first. `0` means `1` |
| `reroute` | bool | no | `false` | Allow later attempts on the next provider in the ranking |
| `backoff` | block | no | — | Delay between attempts |

**Semantics:**

- Without `reroute = true` the task gets exactly one submission, to the
  top-ranked provider, whatever `attempts` says.
- With `reroute = true`, attempts walk down the ranking. Each admitted provider
  is tried at most once, so the number of admitted providers caps the attempts.
- Only an infrastructure failure (the provider gave no verdict) moves to the
  next provider. A task that ran and exited non-zero is a result and is never
  retried. An internal failure is not retried either.
- A provider whose quota reservation is refused at dispatch is passed over
  without spending an attempt.
- When every attempt fails the dispatch ends `unanswered` and `job run` exits
  `2`.

See [dispatch](dispatch.md) for execution records and failure classes.

**Validation:** `Invalid retry attempts in "<job>" task "<task>"` when
`attempts` is negative.

#### `backoff` block

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `initial` | duration | no | `5s` | Wait before the second attempt |
| `max` | duration | no | `30s` | Ceiling on the wait |

The wait before attempt *n* (the first attempt is 0 and never waits) is
`initial × 2^(n-1)`, capped at `max`. With the defaults: 5s, 10s, 20s, 30s,
30s. A value of `0s` falls back to the default.

**Validation:**

| Diagnostic | Cause |
|---|---|
| `Invalid backoff initial in "<job>" task "<task>"` / `Invalid backoff max ...` | Not a duration |
| `Invalid backoff in "<job>" task "<task>"` | `max` below `initial` |

## Durations

Go duration strings: a sequence of numbers with units `ns`, `us`, `ms`, `s`,
`m`, `h`, e.g. `90s`, `15m`, `1h30m`. Days are not a unit. Negative durations
are rejected. Durations are parsed during validation, not decoding, so an
invalid one is reported by name rather than line.

## Diagnostics

Two kinds of diagnostic are reported, all of them in one run:

- **Decode errors** (syntax, unknown or missing attributes, wrong types,
  metadata references) carry a file, line and source excerpt, as HCL renders
  them.
- **Validation rules** run only when the file decoded cleanly. They have no
  source position and name the job and task instead:

  ```text
  Unknown driver in "build" task "test"
    Driver "docker" is not an execution contract Vagabond implements. Valid drivers are container, function, worker.
  Invalid timeout in "build" task "test"
    Timeout "15 minutes" is not a duration. Write one as 15m, 90s, or 1h30m.
  ```

Validation is provider-independent. Whether a provider can run the job
(driver, architecture, limits, quota, constraints) is decided at admission and
shown by [`job plan`](cli.md#job-plan) with a [reason code](scheduling.md#reason-codes)
per rejected provider.

## Complete example

```hcl
job "go-test" {
  namespace = "ci"
  type      = "batch"

  meta {
    project = "example"
  }

  parameterized {
    meta_required = ["version"]
    meta_optional = ["race"]
  }

  routing {
    providers    = ["gcp-cloud-run", "homelab"]
    max_cost_usd = 0

    # Must offer amd64.
    constraint {
      attribute = "provider.architecture"
      operator  = "set_contains"
      value     = "amd64"
    }

    # Prefer providers with over half their free allowance left.
    affinity {
      attribute = "provider.free_quota_percent"
      operator  = ">"
      value     = "50"
      weight    = 75
    }

    # Prefer the operator's us-central1 providers.
    affinity {
      attribute = "provider.meta.region"
      operator  = "="
      value     = "us-central1"
      weight    = 25
    }
  }

  task "test" {
    driver            = "container"
    working_directory = "/src"
    timeout           = "20m"

    config {
      image   = "golang:1.27"
      command = "sh"
      args = [
        "-c",
        "git clone --depth 1 --branch \"$VAGABOND_META_VERSION\" https://git.example.com/example/service.git /src && go test ./...",
      ]
    }

    env {
      CI          = "true"
      CGO_ENABLED = "0"
    }

    resources {
      cpu    = 2000
      memory = 4096
    }

    network {
      internet = true
    }

    execution {
      architecture = "amd64"
    }

    retry {
      attempts = 2
      reroute  = true

      backoff {
        initial = "10s"
        max     = "1m"
      }
    }
  }
}
```

```bash
vagabond job validate -meta version=v1.4.2 go-test.vagabond.hcl
vagabond job run -meta version=v1.4.2 go-test.vagabond.hcl
```
