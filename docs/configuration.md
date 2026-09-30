---
title: "Configuration"
seoTitle: "Server Configuration Reference"
description: "Syntax reference for the server configuration: discovery, providers, tiers, credentials, quota pools, namespaces, scheduling, the server block and the store."
weight: 410
---

The server configuration declares which providers exist, how to reach them, how
much of each may be spent, and where the server listens and persists state. It
is HCL, read once by `vagabond server` at startup. Job files say what to run;
this file says what is available to run it on.

Every other command talks to a running server and reads no configuration; it
needs only `-address` or `$VAGABOND_ADDR` (default `http://127.0.0.1:4747`). See
the [CLI reference](cli.md).

```hcl
# Where the API and the agent listener bind.
server {
  bind       = "127.0.0.1:4747"
  agent_bind = "127.0.0.1:4748"
}

# Required unless the server runs with -dev.
store {
  dsn = "postgres://vagabond@db.internal:5432/vagabond"
}

# One backend. The label is what a job's providers list names.
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
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}
```

## Top-level blocks

| Block | Label | Repeatable | Required | Section |
|---|---|---|---|---|
| `server` | none | no | no | [`server` block](#server-block) |
| `store` | none | no | yes, unless `-dev` | [`store` block](#store-block) |
| `provider` | provider name | yes | at least one | [`provider` block](#provider-block) |
| `namespace` | namespace name | yes | no | [`namespace` block](#namespace-block) |
| `scheduling` | none | no | no | [`scheduling` block](#scheduling-block) |

Any other block or attribute at the top level is an error (`Unsupported block
type`, `Unsupported argument`), reported with its line and column.

## Discovery

`vagabond server -config <path>` names a file or a directory. Without the flag,
the first of these is used:

| Order | Location | If it does not exist |
|---|---|---|
| 1 | `-config <path>` | Error; does not fall through |
| 2 | `$VAGABOND_CONFIG` | Error; does not fall through |
| 3 | `./vagabond.hcl` | Next entry |
| 4 | `<user config dir>/vagabond/config.hcl`, e.g. `~/.config/vagabond/config.hcl` | Next entry |
| 5 | `/etc/vagabond.d` | Error listing every path tried |

The user config dir is Go's `os.UserConfigDir`: `$XDG_CONFIG_HOME`, else
`~/.config` on Linux. It is skipped when it cannot be determined.

Errors:

```text
configuration not found: -config names /etc/vagabond.hcl, which cannot be read: stat /etc/vagabond.hcl: no such file or directory
```

```text
no configuration found. Looked in vagabond.hcl, /home/me/.config/vagabond/config.hcl, /etc/vagabond.d. Pass -config or set VAGABOND_CONFIG
```

### Directories

A directory loads every `*.hcl` file directly inside it, sorted by name, and
merges them into one configuration. Subdirectories and files with other
extensions are ignored.

```text
/etc/vagabond.d/
  00-server.hcl      # server and store
  aws-lambda.hcl     # provider "aws-lambda"
  gcp-cloud-run.hcl  # provider "gcp-cloud-run"
  homelab.hcl        # provider "homelab" { type = "pool" }
  namespaces.hcl     # namespace blocks
```

Merge rules:

| Block | Across files |
|---|---|
| `provider` | Concatenated. The same name in two files is `Duplicate provider`. |
| `namespace` | Concatenated. The same name in two files is `Duplicate namespace`. |
| `store` | At most one across all files. A second is `Duplicate store`. |
| `server` | At most one across all files. A second is `Duplicate server`. |
| `scheduling` | At most one across all files. A second is `Duplicate scheduling`. |

- Validation runs on the merged result, so a cross-file duplicate is reported
  once.
- A namespace `quota` may name a provider declared in another file.
- A directory with no `.hcl` files is `Empty configuration directory`.

### Reloading

Configuration is read at startup only. There is no reload signal; `SIGINT` and
`SIGTERM` stop the server. Restart it to apply a change. Credentials are also
resolved once at startup (see [`credentials` block](#credentials-block)).

## `provider` block

One backend the server can dispatch to. The label is the routing identifier a
job's `providers` list and a namespace `quota` refer to.

```hcl
provider "aws-lambda" {
  type    = "lambda"   # which plugin implements it
  enabled = true       # default; false keeps it configured but unused
  tier    = 1          # preference, lower first; default 0

  config {             # plugin-specific settings
    region = "us-east-1"
  }

  credentials {        # where the plugin's secret comes from
    exec = ["aws", "configure", "export-credentials", "--format", "process"]
  }

  meta {               # labels a job can constrain on
    region = "us-east-1"
  }

  pool "requests" {    # usage budgets, repeatable
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| label | string | yes | | Provider name; unique across the configuration |
| `type` | string | yes | | Plugin, from [Types](#types) |
| `enabled` | bool | no | `true` | `false` keeps the provider registered but never selected |
| `tier` | int | no | `0` | Preference, lower first; see [Tiers](#tiers) |
| `config` | block | per type | | [`config` block](#config-block) |
| `credentials` | block | no | | [`credentials` block](#credentials-block) |
| `meta` | block | no | | [`meta` block](#meta-block) |
| `pool` | block, repeatable | no | none | [`pool` block](#pool-block) |

The name and the type are separate so one deployment can register the same
plugin twice, against two accounts or regions, and a job can tell them apart
(see [Multiple accounts](#multiple-accounts)).

**Disabled providers:** `enabled = false` still builds the provider at startup,
resolves its credentials and decodes its `config`, but it is never asked for
capabilities. A plan lists it rejected as `provider-disabled` rather than
omitting it.

**Health:** the server asks every enabled provider for its capabilities at
startup and every minute. One that does not answer within 30 seconds is marked
unhealthy and rejected as `provider-unhealthy` until a later refresh succeeds.
At startup this prints `Some providers did not answer: ...` and the server
starts anyway. See [background services](background-services.md).

### Tiers

`tier` ranks providers by operator preference: tier 0 is tried before tier 1,
and so on. Providers in the same tier are ordered by score. A provider without
`tier` is in tier 0, so tiering one fallback does not demote the rest.

```hcl
provider "homelab"       { type = "pool" }        # tier 0
provider "gcp-cloud-run" { type = "cloud-run" }   # tier 0
provider "aws-lambda" {
  type = "lambda"
  tier = 1                                        # only when both above are rejected
}
```

- A tier only orders providers that passed admission. It never admits or
  rejects one.
- How strictly tiers order depends on the tier mode: the
  [`scheduling` block](#scheduling-block) sets it for the deployment and a
  job's `routing.tiers` overrides it. See
  [Scheduling: Tiers](scheduling.md#tiers).
- A job can match on the value as `provider.tier` in a constraint or affinity.
- A negative tier is `Invalid provider tier`.

### Types

| Type | Plugin | `config` block | `credentials` |
|---|---|---|---|
| `cloud-run` | [Google Cloud Run Jobs](providers/cloud-run.md) | required: `project`, `region`, `runtime_service_account` | required: service account JSON key |
| `lambda` | [AWS Lambda](providers/lambda.md) | required: `region` | optional: `credential_process` JSON; without it, the AWS SDK default chain |
| `pool` | The [agent](agent.md#pools) nodes connected to this server that joined the pool named by the label | ignored | resolved, unused |
| `fake-container` | In-memory container provider | ignored | resolved, unused |
| `fake-function` | In-memory function provider | ignored | resolved, unused |
| `fake-worker` | In-memory worker provider | ignored | resolved, unused |

Any other value is `Unknown provider type`, listing the known types.

The fakes run admission, ranking and dispatch with no cloud account.
`examples/config.hcl` uses them.

### `pool` provider

A provider made of the agent nodes that joined the pool with `vagabond agent
-pool <label>`:

```hcl
provider "homelab" {
  type = "pool"

  meta {
    site = "basement"
  }
}
```

- Only `vagabond server` can build a pool. Elsewhere it is `Pool without a
  server`.
- `enabled`, `meta` and `pool` quota blocks apply to the pool as a whole.
- Its capabilities are read from the connected nodes on every plan, not on the
  one-minute refresh, so a node that joins is schedulable at once.
- A pool with no connected nodes is unhealthy and rejected as
  `provider-unhealthy`.
- An agent joining a pool name that no `provider` declares connects, but
  nothing schedules onto it.

## `config` block

Plugin-specific settings. Vagabond passes the block to the plugin undecoded, so
each plugin defines its own attributes and reports its own errors against the
line they were written on. Attribute reference: [Cloud Run](providers/cloud-run.md),
[Lambda](providers/lambda.md).

```hcl
config {
  project                 = "my-project"
  region                  = "us-central1"
  runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
}
```

| Condition | Diagnostic |
|---|---|
| Block missing on `cloud-run` or `lambda` | `Missing provider configuration`, naming the attributes it needs |
| Required attribute missing | `Missing required argument` |
| Unknown attribute | `Unsupported argument` |
| Attribute set to `""` or whitespace | `Empty provider setting` |

## `credentials` block

Where a provider's secret comes from. Exactly one source.

| Name | Type | Description |
|---|---|---|
| `file` | string | Path to a file holding the credential; read as-is |
| `env` | string | Name of an environment variable holding it |
| `exec` | list(string) | Command and arguments; standard output is the credential |

```hcl
# From a file only the server's user can read.
credentials {
  file = "/etc/vagabond/gcp-dispatcher.json"
}

# From the server's environment.
credentials {
  env = "VAGABOND_GCP_KEY"
}

# From any secret store with a CLI.
credentials {
  exec = ["vault", "kv", "get", "-field=key", "secret/vagabond/gcp"]
}
```

**Resolution:** once at startup, for every provider including disabled ones.
The plugin receives bytes and never learns the source. When the platform
rejects the credential (HTTP 401 or 403), the source is resolved again: the
file re-read, the variable re-read, the command re-run. The provider rebuilds
its client from the result, and the call is made once more. A source is
re-resolved at most once a minute per provider, so a credential that is
simply wrong does not rerun its command on every call. An expired session
token or a rotated secret therefore takes effect without a restart.

| Source | Behaviour |
|---|---|
| `file` | Read whole, byte for byte. Unreadable is an error. |
| `env` | An unset variable is an error; set to empty yields an empty secret. |
| `exec` | Run without a shell, bounded at 30 seconds. Trailing `\r` and `\n` are trimmed. Standard error is included in the failure message and never in the secret. Non-zero exit is an error. |

Validation:

| Condition | Diagnostic |
|---|---|
| Block names no source | `Missing credential source` |
| Block names more than one | `Ambiguous credential source`. Not a precedence rule. |
| Source fails at startup | `Cannot resolve credentials`, e.g. `credential environment variable VAGABOND_GCP_KEY is not set` |
| Plugin cannot use what was resolved | Plugin-specific, e.g. Lambda's `Unusable credential` |

- Omitting the block is valid; fakes and pools need none.
- There is no inline literal source.
- `exec` runs an arbitrary command from the configuration file with the
  server's privileges. Protect the file accordingly.

## `meta` block

Operator-defined labels. They reach admission as `provider.meta.<key>`
attributes, which a job's constraints and affinities can match. Vagabond
attaches no meaning to them.

```hcl
meta {
  region = "us-central1"
  tier   = "free"
}
```

- Attributes only; any key is allowed.
- Every value must be a string. `cores = 4` is `Invalid provider tag: The value
  for "cores" must be a string.` Write `cores = "4"`.
- Checked when the provider is built at startup, not at parse time.

Matching them from a job: [job specification](job-specification.md).

## `pool` block

One usage budget for a provider, in a unit the provider meters. Repeatable
inside `provider` and inside a namespace `quota`. How pools are charged,
reserved and settled is covered in [Quotas](quotas.md).

```hcl
pool "compute" {
  meter  = "gb_seconds"   # what the pool counts
  limit  = 400000         # ceiling, in the meter's unit
  period = "monthly"      # when usage resets
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| label | string | yes | | Pool name; usage is recorded under it |
| `meter` | string | yes | | One of the meters below |
| `limit` | whole number | yes | | Ceiling in the meter's unit; must be positive |
| `period` | string | yes | | `daily` or `monthly` |

| Meter | Unit | Charge per execution |
|---|---|---|
| `executions` | executions | 1 |
| `gb_seconds` | GB-seconds | declared memory × declared timeout |
| `cpu_seconds` | vCPU-seconds | declared CPU × declared timeout |
| `seconds` | seconds | declared timeout |

| Period | Resets |
|---|---|
| `daily` | 00:00 UTC |
| `monthly` | 00:00 UTC on the 1st |

Validation, per owner (a provider, or one namespace's share of a provider):

| Condition | Diagnostic detail |
|---|---|
| Attribute missing | `Missing required argument` |
| Two pools with the same label | `Provider "x" pool "p" is declared twice. ...` |
| Unknown meter | `... meters "gb_hours", which is not something Vagabond counts. Known meters are executions, gb_seconds, cpu_seconds, seconds.` |
| Unknown period | `... resets "weekly". Known periods are daily, monthly.` |
| `limit` zero or negative | `... has a limit of 0. A pool exists to refuse an execution, so its limit must be positive.` |
| `limit` fractional | HCL decode error; limits are whole numbers |

All carry the summary `Invalid quota pool`. No attribute has a default and no
provider ships preset pools. A provider with no pools is not quota-limited.

## `namespace` block

An owner of jobs, with an optional share of each provider's pools. How shares
combine with provider pools: [Quotas](quotas.md).

```hcl
namespace "ci" {
  # ci's share of aws-lambda. The label is a provider name.
  quota "aws-lambda" {
    pool "compute" {
      meter  = "gb_seconds"
      limit  = 100000
      period = "monthly"
    }
  }
}

# A namespace with no shares: jobs may run in it, limited by provider pools only.
namespace "nightly" {}
```

| Name | Type | Required | Description |
|---|---|---|---|
| label | string | yes | Namespace name; unique across the configuration |
| `quota` | block, repeatable | no | One share per provider |

### `quota` block

| Name | Type | Required | Description |
|---|---|---|---|
| label | string | yes | Name of a configured provider |
| `pool` | block, repeatable | no | Same syntax and validation as [`pool` block](#pool-block) |

Validation:

| Condition | Diagnostic |
|---|---|
| Two namespaces with the same name | `Duplicate namespace` |
| `quota` names a provider that is not configured | `Unknown provider in namespace quota` |
| Two `quota` blocks for one provider in one namespace | `Duplicate namespace quota` |

- `default` exists without being declared. A job naming no namespace runs
  there. Declaring `namespace "default"` gives it shares.
- A job naming an undeclared namespace is refused: `namespace "x" is not
  declared in the configuration`.
- A namespace may sit in its own file in a configuration directory.

## `scheduling` block

How the server ranks providers for a job that does not say. Omitting the block
uses every default.

```hcl
scheduling {
  tiers = "weighted"
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `tiers` | string | no | `strict` | Tier mode: `strict` or `weighted` |

| Mode | Ordering |
|---|---|
| `strict` | By provider [`tier`](#tiers), then by score. A lower tier always ranks first while any of its providers is admitted. |
| `weighted` | By score. The tier is one more scorer, averaged with headroom and affinity, so a higher tier with more headroom can outrank a lower one. |

- A job's `routing { tiers = ... }` overrides this for that job. See
  [Job specification: `routing`](job-specification.md#routing-block).
- Any other value is `Unknown tier mode`, listing the valid modes.
- Scoring details: [Scheduling: Tiers](scheduling.md#tiers).

## `server` block

Where `vagabond server` listens. Omitting the block uses every default.

```hcl
server {
  bind       = "0.0.0.0:4747"   # HTTP API
  agent_bind = "0.0.0.0:4748"   # agent connections

  tls {                         # the API
    cert = "/etc/vagabond/tls/server.crt"
    key  = "/etc/vagabond/tls/server.key"
  }

  agent_tls {                   # the agent listener, mutual TLS
    cert = "/etc/vagabond/agents/server.pem"
    key  = "/etc/vagabond/agents/server-key.pem"
    ca   = "/etc/vagabond/agents/ca.pem"
  }
}
```

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `bind` | string | no | `127.0.0.1:4747` | `host:port` for the HTTP API |
| `agent_bind` | string | no | `127.0.0.1:4748` | `host:port` [agents](agent.md) dial |
| `tls` | block | no | plain HTTP | Certificate for the API |
| `agent_tls` | block | when `agent_bind` is not loopback | plain TCP | Mutual TLS for agents |

### `tls` block

| Name | Type | Required | Description |
|---|---|---|---|
| `cert` | string | yes | PEM certificate (chain) path |
| `key` | string | yes | PEM private key path |

- Loaded at startup; a bad pair fails with `loading the server certificate:
  ...`. Minimum TLS 1.2.
- Covers the API listener only; `agent_tls` covers agents.
- The API has no authentication. Anyone who can reach `bind` can register,
  run and cancel jobs. See [Deployment](deployment.md#network-exposure).
- Clients reach a TLS server with `-address https://host:4747`; the certificate
  is verified against the system trust store.

### `agent_tls` block

Mutual TLS on the agent listener. The server presents `cert`, and every agent
must present a certificate `ca` signed.

| Name | Type | Required | Description |
|---|---|---|---|
| `cert` | string | yes | PEM certificate (chain) the server presents to agents |
| `key` | string | yes | Its PEM private key |
| `ca` | string | yes | PEM file of the CA certificates agent certificates must chain to |

- Required when `agent_bind` is not a loopback address. Without it the server
  refuses to start: `agent_bind <addr> is reachable beyond this machine: add an
  agent_tls block to the server block so agents must present a certificate, or
  bind to 127.0.0.1`. An empty host (`:4748`) listens everywhere and counts as
  not loopback.
- An agent registers only under its certificate's common name. Registering
  another name is refused with `PermissionDenied`, so a node can only be
  replaced by an agent holding its certificate.
- The server certificate must carry the name agents dial as a SAN (an IP
  address or DNS name), or agents set `-tls-server-name`.
- Loaded at startup; minimum TLS 1.2. A handshake not finished within 10
  seconds is dropped. Generating the certificates:
  [agent](agent.md#certificates).

## `store` block

The database holding jobs, dispatches, executions and quota usage. Schema,
migrations and operations: [Database](database.md).

```hcl
store {
  dsn = "postgres://vagabond@db.internal:5432/vagabond?sslmode=verify-full"
}
```

| Name | Type | Required | Description |
|---|---|---|---|
| `dsn` | string | yes | PostgreSQL or CockroachDB connection string, URL or `key=value` form |

| Condition | Result |
|---|---|
| Block absent | `vagabond server` exits: `The server needs a store block, or -dev to keep everything in memory.` |
| `dsn = ""` | `Empty store DSN` |
| Block present with `-dev` | Warning; the block is ignored and everything is kept in memory |
| Database unreachable at startup | `could not open the store: ...`, exit 1 |

- The password can stay out of the DSN: `PGPASSWORD` and `~/.pgpass` are read.
- Migrations apply at startup.

## Multiple accounts

The same plugin registered twice, told apart by name and by `meta`:

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
}
```

A job selects between them with a constraint:

```hcl
constraint {
  attribute = "provider.meta.region"
  operator  = "="
  value     = "europe-west1"
}
```

Each provider has its own pools; two providers on one account do not share a
budget.

## Diagnostics

Configuration is decoded with the same HCL machinery job files use.

- Every problem is reported in one run, not the first only.
- A provider that fails to build does not stop the others from being reported.
- Parse and decode errors carry file, line and column. Validation errors
  (duplicates, pools, credentials) name the provider, namespace or pool
  instead.
- Any error exits `vagabond server` with status 1 before it listens.

```text
Duplicate provider
  Two providers are named "aws-lambda". The name is what a job's provider list refers to, so it must identify one.
Invalid quota pool
  Provider "aws-lambda" pool "compute" meters "gb_hours", which is not something Vagabond counts. Known meters are executions, gb_seconds, cpu_seconds, seconds.
```

Other startup errors:

| Message | Cause |
|---|---|
| `No providers are configured, so there is nothing to run on.` | Configuration decoded with zero `provider` blocks |
| `Listening for agents on <addr>: ...` | `agent_bind` in use or not bindable |
| `listening on <addr>: ...` | `bind` in use or not bindable |

## Complete example

```hcl
# /etc/vagabond.d/vagabond.hcl

server {
  bind       = "0.0.0.0:4747"
  agent_bind = "10.0.0.5:4748"

  tls {
    cert = "/etc/vagabond/tls/server.crt"
    key  = "/etc/vagabond/tls/server.key"
  }

  agent_tls {
    cert = "/etc/vagabond/agents/server.pem"
    key  = "/etc/vagabond/agents/server-key.pem"
    ca   = "/etc/vagabond/agents/ca.pem"
  }
}

store {
  # Password from PGPASSWORD or ~/.pgpass.
  dsn = "postgres://vagabond@db.internal:5432/vagabond?sslmode=verify-full"
}

# Tiers order strictly unless a job says otherwise; this is the default.
scheduling {
  tiers = "strict"
}

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
    kind   = "cloud"
  }

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 180000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}

provider "aws-lambda" {
  type = "lambda"
  tier = 1 # only when homelab and gcp-cloud-run are both rejected

  config {
    region = "us-east-1"
  }

  # Prints credential_process JSON.
  credentials {
    exec = ["aws", "configure", "export-credentials", "--format", "process"]
  }

  meta {
    region = "us-east-1"
    kind   = "cloud"
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

  # A daily cap inside the monthly one.
  pool "compute-daily" {
    meter  = "gb_seconds"
    limit  = 13000
    period = "daily"
  }
}

provider "homelab" {
  type = "pool"

  meta {
    kind = "homelab"
  }
}

# Kept configured, never selected.
provider "gcp-eu" {
  type    = "cloud-run"
  enabled = false

  config {
    project                 = "my-project"
    region                  = "europe-west1"
    runtime_service_account = "vagabond-run@my-project.iam.gserviceaccount.com"
  }

  credentials {
    file = "/etc/vagabond/gcp-dispatcher.json"
  }
}

namespace "ci" {
  quota "aws-lambda" {
    pool "compute" {
      meter  = "gb_seconds"
      limit  = 200000
      period = "monthly"
    }
  }

  quota "gcp-cloud-run" {
    pool "compute" {
      meter  = "gb_seconds"
      limit  = 180000
      period = "monthly"
    }
  }
}

namespace "nightly" {}
```
