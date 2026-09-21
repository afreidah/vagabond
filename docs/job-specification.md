# Job specification

A job file is HCL. Convention is `<name>.vagabond.hcl`. A complete example is
`examples/go-test.vagabond.hcl`.

```hcl
job "go-test" {
  type = "batch"

  meta {
    project = "example"
  }

  parameterized {
    meta_required = ["version"]
  }

  routing {
    strategy     = "free-first"
    providers    = ["ibm-code-engine", "gcp-cloud-run"]
    max_cost_usd = 0

    constraint {
      attribute = "provider.architecture"
      operator  = "set_contains"
      value     = "amd64"
    }
  }

  task "test" {
    driver = "container"

    config {
      image   = "golang:1.27"
      command = "go"
      args    = ["test", "./..."]
    }

    resources {
      cpu    = 1000
      memory = 2048
    }

    timeout = "15m"
  }
}
```

## `job` block

| Name | Type | Default | Description |
|---|---|---|---|
| label | string | — | Job name |
| `type` | string | `batch` | Only `batch` is defined |

Contains `meta`, `parameterized`, `routing`, and one or more `task` blocks.

## `meta` block

Arbitrary key/value pairs carried with the job. Referenced elsewhere in the file
as `${meta.key}`, substituted before the job is planned or dispatched.

```hcl
meta {
  project = "example"
  purpose = "ci"
}
```

## `parameterized` block

| Name | Type | Description |
|---|---|---|
| `meta_required` | list(string) | Metadata keys the caller must supply |

A missing key is rejected before any provider is contacted. Supply values with
`-meta <key>=<value>`, repeatable. Passing the same key twice is an error.

## `routing` block

Controls which providers a job may use and how they are ordered.

| Name | Type | Default | Description |
|---|---|---|---|
| `strategy` | string | `free-first` | Base scorer used to rank candidates |
| `providers` | list(string) | all | Allowlist of provider names |
| `max_cost_usd` | number | `0` | Reject providers estimated to cost more |

`providers` is an allowlist, not a failover chain. Order carries no meaning:
admission removes ineligible providers, then ranking scores the rest.

`max_cost_usd` defaults to 0, so a job that says nothing will not route to paid
capacity. Such a job is also subject to the `quota-exhausted` check. Set it
above zero to allow both.

### `constraint` block

A hard requirement. A provider that fails it is rejected. Repeatable.

| Name | Type | Required | Description |
|---|---|---|---|
| `attribute` | string | yes | Provider attribute to match |
| `operator` | string | yes | Comparison to apply |
| `value` | string | no | Right-hand side; omitted for `is_set` / `is_not_set` |

### `affinity` block

A preference. A provider that fails it is still eligible, but scores lower.
Repeatable.

| Name | Type | Default | Description |
|---|---|---|---|
| `attribute` | string | — | Provider attribute to match |
| `operator` | string | — | Comparison to apply |
| `value` | string | — | Right-hand side |
| `weight` | number | 50 | Relative importance |

The affinity score is matched weight divided by total weight.

### Operators

| Operator | Applies to |
|---|---|
| `=` | strings, numbers |
| `!=` | strings, numbers |
| `<` `<=` `>` `>=` | numbers, durations |
| `set_contains` | set-valued attributes |
| `is_set` | any |
| `is_not_set` | any |

### Attributes

Attributes under `provider.` are published by Vagabond. A constraint naming an
unpublished name under this prefix is rejected as `attribute-unknown` rather
than silently matching nothing.

| Attribute | Type | Description |
|---|---|---|
| `provider.architecture` | set | Architectures offered, e.g. `amd64,arm64` |
| `provider.drivers` | set | Drivers offered, e.g. `container` |
| `provider.max_duration` | duration | Longest task accepted |
| `provider.max_cpu` | number | Millicores available to one task |
| `provider.max_memory` | number | MiB available to one task |
| `provider.internet` | bool | Outbound internet available |
| `provider.private_network` | bool | Private network access available |
| `provider.arbitrary_images` | bool | Any container image may be run |
| `provider.estimated_cost` | number | Estimated cost in USD |
| `provider.free_quota_percent` | number | Percent of free allowance remaining |

`provider.architecture` and `provider.drivers` hold sets. Use `set_contains`;
`=` asks whether the set has exactly one member.

An attribute a provider never advertised is absent rather than zero, so
`is_set` and `is_not_set` distinguish "no limit stated" from "a limit of zero".
`provider.estimated_cost` is always published, including at zero.

`provider.meta.*` holds operator-defined labels from the provider's `meta`
block. Anything under that prefix is accepted without validation.

## `task` block

| Name | Type | Required | Description |
|---|---|---|---|
| label | string | yes | Task name |
| `driver` | string | yes | `container`, `function`, or `worker` |
| `working_directory` | string | no | Directory the command runs in |
| `timeout` | duration | no | Maximum runtime |

Contains `config`, `env`, `source`, `resources`, `network`, `execution` and
`retry` blocks.

### `config` block

Driver-specific. Decoded by the provider plugin, not by Vagabond.

For `container`:

| Name | Type | Description |
|---|---|---|
| `image` | string | Container image to run |
| `command` | string | Entrypoint override |
| `args` | list(string) | Arguments to the command |

### `env` block

Environment variables, as free-form key/value pairs.

```hcl
env {
  CI          = "true"
  CGO_ENABLED = "0"
}
```

### `source` block

| Name | Type | Required | Description |
|---|---|---|---|
| `type` | string | yes | `git` |
| `repository` | string | yes | Repository URL |
| `ref` | string | no | Revision to check out |
| `destination` | string | no | Path to check out into |

`ref` commonly interpolates job metadata, so the provider receives a literal
revision:

```hcl
source {
  type        = "git"
  repository  = "https://git.example.com/example/service.git"
  ref         = "${meta.version}"
  destination = "/workspace"
}
```

### `resources` block

| Name | Type | Unit | Description |
|---|---|---|---|
| `cpu` | number | millicores | 1000 is one vCPU |
| `memory` | number | MiB | |

These are workload requirements, not provider sizes. A plugin rounds up to the
nearest size its platform offers.

CPU is millicores rather than MHz because clouds sell vCPU, not clock speed.

### `network` block

| Name | Type | Description |
|---|---|---|
| `internet` | bool | Task needs outbound internet |
| `private` | bool | Task needs private network access |

### `execution` block

| Name | Type | Description |
|---|---|---|
| `architecture` | string | `amd64` or `arm64` |
| `privileged` | bool | Task needs a privileged container |

### `retry` block

| Name | Type | Default | Description |
|---|---|---|---|
| `attempts` | number | — | Retry attempts |
| `reroute` | bool | — | Allow retrying on a different provider |

Only infrastructure failures are rerouted. A workload failure is an answer, not
an outage: a failing test suite is returned as-is.

#### `backoff` block

| Name | Type | Description |
|---|---|---|
| `initial` | duration | First backoff interval |
| `max` | duration | Ceiling on the interval |

## Durations

Go duration strings: `30s`, `15m`, `2h`.

## Validation

```bash
vagabond job validate -meta version=1.2.3 job.vagabond.hcl
```

Every problem in the file is reported in one run, with line and column.
