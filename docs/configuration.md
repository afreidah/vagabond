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

## `quota` block

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
