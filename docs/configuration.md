# Configuration

The operator's file: which backends exist and how to reach them. Job files say
what to run; this says what is available to run it on.

```hcl
provider "gcp-cloud-run" {
  type = "cloud-run"

  config {
    project                 = "my-project"
    region                  = "us-central1"
    runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
  }

  credentials {
    file = "/etc/vagabond/gcp-dispatcher.json"
  }

  meta {
    region = "us-central1"
    tier   = "free"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }

  quota {
    free_percent = 80
  }
}
```

## Discovery

`-config <path>` accepts a file or a directory. A directory loads every `.hcl`
file inside it and merges the results.

Resolution order:

1. `-config <path>`
2. `$VAGABOND_CONFIG`
3. `./vagabond.hcl`
4. `<user config dir>/vagabond/config.hcl`, e.g. `~/.config/vagabond/config.hcl`
5. `/etc/vagabond.d`

`-config` and `$VAGABOND_CONFIG` fail if the path does not exist; they do not
fall through to the search path. The search path itself takes the first entry
that exists.

## `provider` block

One backend Vagabond can dispatch to. The label is the routing identifier a
job's `providers` list refers to.

| Name | Type | Required | Description |
|---|---|---|---|
| label | string | yes | Routing identifier, e.g. `gcp-cloud-run` |
| `type` | string | yes | Which plugin implements it |
| `enabled` | bool | no | Defaults to true |

Name and type are separate so one deployment can register the same plugin twice
against two accounts or regions, and a job can name them apart.

A disabled provider stays in the registry. A plan reports it as
`provider-disabled` rather than omitting it silently.

### Types

| Type | Plugin |
|---|---|
| `cloud-run` | [Google Cloud Run Jobs](providers/cloud-run.md) |
| `fake-container` | In-memory container provider |
| `fake-function` | In-memory function provider |
| `fake-worker` | In-memory worker provider |

The fakes run admission, scheduling and planning with no cloud account
configured. `examples/config.hcl` uses them.

## `config` block

Plugin-specific settings, decoded by the plugin rather than by Vagabond. Each
plugin documents its own fields; see [Cloud Run](providers/cloud-run.md).

An unknown attribute is reported against the line it was written on.

## `credentials` block

Where a provider's secret comes from. Exactly one source.

| Name | Type | Description |
|---|---|---|
| `file` | string | Path to a file holding the credential |
| `env` | string | Name of an environment variable holding it |
| `exec` | list(string) | Command to run; stdout is the credential |

```hcl
credentials {
  file = "/etc/vagabond/gcp-dispatcher.json"
}

credentials {
  env = "VAGABOND_GCP_KEY"
}

credentials {
  exec = ["vault", "kv", "get", "-field=key", "secret/vagabond/gcp"]
}
```

Rules:

- Naming no source is an error.
- Naming more than one is an error, not a precedence rule.
- Omitting the block entirely is valid. Most providers need no credential.
- `exec` is bounded at 30 seconds.
- `exec` output has trailing newlines trimmed. Stderr is captured for error
  messages and never mixed into the secret.

There is no inline literal source. Use `file`.

## `meta` block

Operator-defined labels. They reach a job as `provider.meta.*` attributes,
matchable by constraints and affinities.

```hcl
meta {
  region = "us-central1"
  tier   = "free"
}
```

Vagabond never interprets these. Anything you want to route on that Vagabond has
no opinion about belongs here.

## `pool` block

One usage budget, in a unit the provider itself meters. Repeatable.

| Name | Type | Required | Description |
|---|---|---|---|
| label | string | yes | Pool name; what its usage is counted under |
| `meter` | string | yes | What the pool counts |
| `limit` | number | yes | Ceiling, in the meter's unit |
| `period` | string | yes | When the allowance resets |

Meters:

| Meter | Unit | Charged |
|---|---|---|
| `executions` | executions | One per execution |
| `gb_seconds` | GB-seconds | Declared memory × declared timeout |
| `cpu_seconds` | vCPU-seconds | Declared CPU × declared timeout |
| `seconds` | seconds | Declared timeout |

Periods:

| Period | Resets |
|---|---|
| `daily` | UTC midnight |
| `monthly` | First of the month, UTC |

AWS Lambda's free tier, as two independent budgets:

```hcl
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
```

Rules:

- Nothing ships a default. The limit is how much you are willing to spend on a
  backend, not a published fact, so there is no correct number to supply.
- Declare every quantity the platform meters. A provider metered on memory but
  not CPU will keep admitting work after the CPU allowance is gone. The
  provider's page under `docs/providers/` lists which apply.
- A provider with no pools is unlimited. Capabilities still gate it.
- Pools are additive. An execution charges every pool whose meter it touches and
  needs headroom in all of them, so a daily cap can sit inside a monthly one.
- Every attribute is required. A pool with no limit refuses nothing, and a daily
  budget defaulted to monthly is enforced twelve times too loosely.
- Charges come from what a task declares, not what it used, so `job plan` can
  price a job without dispatching it.
- A limit above the free tier is how you permit spending. Vagabond does not know
  a provider's prices; do that arithmetic yourself and write the result.

Pools are declared and validated today. Enforcement arrives with the usage
ledger, which is what will count against them.

## `quota` block

A stand-in, replaced by `pool` once the usage ledger tracks consumption.

What is believed to remain of the provider's allowance.

| Name | Type | Description |
|---|---|---|
| `free_percent` | number | Percent of the free allowance remaining, 0–100 |
| `exhausted` | bool | Provider has no capacity left |

`free_percent` populates `provider.free_quota_percent`, which drives the
`headroom` scorer and the `quota-exhausted` check.

A provider with no headroom is rejected only for jobs that will not pay.
`max_cost_usd` defaults to 0, so that is most jobs. A job with
`max_cost_usd > 0` is admitted to a spent provider.

An unobserved quota is treated the same as a spent one. State it, or jobs that
refuse to pay will not route there.

## Multiple accounts

```hcl
provider "gcp-us" {
  type = "cloud-run"

  config {
    project                 = "my-project"
    region                  = "us-central1"
    runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
  }

  credentials { file = "/etc/vagabond/gcp.json" }
  meta        { region = "us-central1" }
  quota       { free_percent = 80 }
}

provider "gcp-eu" {
  type = "cloud-run"

  config {
    project                 = "my-project"
    region                  = "europe-west1"
    runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
  }

  credentials { file = "/etc/vagabond/gcp.json" }
  meta        { region = "europe-west1" }
  quota       { free_percent = 80 }
}
```

A job then selects between them with a constraint:

```hcl
constraint {
  attribute = "provider.meta.region"
  operator  = "="
  value     = "europe-west1"
}
```

## Diagnostics

Configuration is decoded with the same machinery job files use. Every problem in
the file is reported in one run, with line and column. A provider that fails to
build does not stop the others from being reported.
