---
title: "Command Line Interface"
linkTitle: "CLI"
seoTitle: "CLI Reference: Commands and Flags"
description: "Every vagabond command and flag: job, execution and node commands, the server and the agent, environment variables and exit codes."
weight: 220
---

One binary, `vagabond`, runs the server, the agent, and the commands that talk
to a server. Commands are grouped by noun (`job`, `execution`, `node`) and take
single-dash flags.

## Usage

```text
vagabond [-version] [-help] <command> [<args>]
```

```shell
$ vagabond -help
Usage: vagabond [-version] [-help] <command> [<args>]

Available commands are:
    agent            Run the agent that executes workloads on this node
    execution        Interact with executions
    job              Interact with jobs
    node             Interact with agent nodes
    server           Run the Vagabond server
```

| Command | Synopsis | Talks to a server |
|---|---|---|
| [`job validate`](#job-validate) | Check a job specification for errors | no |
| [`job plan`](#job-plan) | Show where a job would run, and why not elsewhere | yes |
| [`job run`](#job-run) | Run a job on the best available provider | yes |
| [`job register`](#job-register) | Store a job so it can be dispatched by name | yes |
| [`job dispatch`](#job-dispatch) | Run a registered job by name | yes |
| [`job status`](#job-status) | List registered jobs, or show one | yes |
| [`job stop`](#job-stop) | Deregister a job so it can no longer be dispatched | yes |
| [`execution status`](#execution-status) | Show one execution | yes |
| [`execution logs`](#execution-logs) | Print an execution's output | yes |
| [`node status`](#node-status) | List the connected agent nodes | yes |
| [`server`](#server) | Run the Vagabond server | runs one |
| [`agent`](#agent) | Run the agent that executes workloads on this node | dials one |

### Global flags

Given before the command.

| Flag | Description |
|---|---|
| `-help`, `-h`, `--help` | Print the command list. After a command, print that command's help |
| `-version`, `-v`, `--version` | Print the version and exit 0 |
| `-autocomplete-install` | Install shell completion for `vagabond` (bash, zsh, fish) |
| `-autocomplete-uninstall` | Remove shell completion |

```shell
$ vagabond -version
dev (2cbfbc9514fd)
```

The version is the release version, `dev` for a build without one, followed
by the first 12 characters of the commit when it is known.

### Flag and argument rules

- Flags use one dash: `-meta`, not `--meta`. Go's `flag` package also accepts
  two, but help and docs use one.
- Flags go after the command and before positional arguments. Parsing stops
  at the first positional argument, so
  `vagabond job run job.hcl -meta version=1` treats `-meta` as a second
  argument and fails with `This command takes one argument: <path>`.
- A flag given before the command fails with
  `Invalid flags before the subcommand. If these flags are for the subcommand, please put them after the subcommand.`
- A namespace on its own (`vagabond job`) prints its help and exits 1.
- An unknown command prints the command list to stderr and exits 1.
- A path argument of `-` reads the job file from standard input.

## Connecting to the server

Every command except `job validate`, `server` and `agent` calls the server's
[HTTP API](api.md). They share two flags:

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server API address |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace for job commands |

- An address without a scheme is taken as `http://`: `-address 10.0.0.5:4747`
  is `http://10.0.0.5:4747`. Use `https://` for a server with a `tls` block.
- The flag wins over the environment variable; an empty value falls through
  to the default.
- `-namespace` is sent as `?namespace=` on job requests. The execution and
  node commands accept it and ignore it: execution IDs are global, and nodes
  belong to pools, not namespaces.
- A job file that names its own `namespace` and a different `-namespace` is an
  error; see [namespace resolution](job-specification.md#job-block).

Failures are printed as `Error: <message>` on stderr with exit code 1:

```shell
$ vagabond job status
Error: contacting the server at http://127.0.0.1:4747: Get "http://127.0.0.1:4747/v1/jobs": dial tcp 127.0.0.1:4747: connect: connection refused
```

A job the server finds invalid is reported one diagnostic at a time, with the
range as `<file>:<line>,<column>-<column>`. For a registered job the file is
`<name> (version <n>)`:

```text
Error: Unsupported attribute

  on go-test (version 3):9,24-35

This object does not have an attribute named "race".
```

Job files are validated locally before `job plan`, `job run` and
`job register` send them, so server-side diagnostics mostly come from
registered jobs.

## Environment variables

| Variable | Used by | Description |
|---|---|---|
| `VAGABOND_ADDR` | client commands | Default for `-address` |
| `VAGABOND_NAMESPACE` | client commands | Default for `-namespace` |
| `VAGABOND_CONFIG` | `server` | Configuration file or directory, when `-config` is not given |
| `NO_COLOR` | `job validate` and every command rendering job diagnostics | Set to any value to disable colour in diagnostics |

Credentials a provider block reads from the environment are named in the
[configuration](configuration.md), not fixed by the CLI.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | The command did what was asked |
| `1` | It ran and the answer was no: invalid job, failed task, nowhere to run in a plan, server error, usage error, unknown command |
| `2` | The work never ran: nothing was eligible, every provider failed, or the run was interrupted. Returned by `job run` and `job dispatch` only |

No other code is returned. `1` versus `2` separates a failing build (the
job's problem) from no capacity (an infrastructure problem).

## Output

- Results go to stdout. Errors, warnings and run progress go to stderr, so
  `vagabond job run job.hcl > build.log` captures the task output alone.
- Times are printed in UTC as `2006-01-02 15:04:05Z`.
- Job diagnostics are coloured when stderr is a terminal and `NO_COLOR` is
  unset. Detail text wraps at 78 columns.

## Job commands

### `job validate`

```text
vagabond job validate [options] <path>
```

Parses and validates a job file locally. No server or provider is contacted.
Every problem is reported in one run: decode errors with the file, line and
source excerpt, validation rules naming the job and task. See
[diagnostics](job-specification.md#diagnostics).

| Flag | Default | Description |
|---|---|---|
| `-meta <key>=<value>` | none | Job metadata, repeatable. Substituted before checking, so `${meta.version}` is validated as it would be submitted. A `meta_required` key left out is an error |

| Exit | Meaning |
|---|---|
| `0` | Valid |
| `1` | Invalid, unreadable, or wrong number of arguments |

```shell
$ vagabond job validate -meta version=v1.4.2 go-test.vagabond.hcl
Job specification go-test.vagabond.hcl is valid.

$ cat go-test.vagabond.hcl | vagabond job validate -meta version=v1.4.2 -
The specification is valid.

$ vagabond job validate build.vagabond.hcl
Unknown driver in "build" task "test"
  Driver "docker" is not an execution contract Vagabond implements. Valid drivers are container, function, worker.
Invalid timeout in "build" task "test"
  Timeout "15 minutes" is not a duration. Write one as 15m, 90s, or 1h30m.

$ vagabond job validate missing.hcl
Cannot read job file
  Reading missing.hcl: open missing.hcl: no such file or directory.
```

### `job plan`

```text
vagabond job plan [options] <path or name>
```

Admits a job against the server's providers and shows, per task, which
providers could run it, how each scored, and why every other one was
rejected. Nothing is dispatched or reserved; planning is free and repeatable.

No provider is contacted. The server plans from capability and quota snapshots
gathered earlier, and each admitted provider's observation time is printed.

**Argument:** read as a job file when it is `-`, contains a path separator,
ends in `.hcl`, or exists on disk. Anything else names a registered job, whose
current version is planned with its metadata checked as `job dispatch` checks
it. A file is validated locally before it is sent, so its errors are reported
as `job validate` reports them.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |
| `-meta <key>=<value>` | none | Job metadata, repeatable |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace, for a job that names none |
| `-verbose` | `false` | Print each scorer's contribution under a candidate, and every further reason a rejected provider failed |

| Exit | Meaning |
|---|---|
| `0` | At least one provider can run every task |
| `1` | Some task has nowhere to run, or the plan could not be produced |

**Output:** one section per task, separated by a blank line.

```text
<job>.<task> (<driver>)
<provider>  admitted  score <0-100>  observed <time>
<provider>  rejected  <reason>       <detail>
Selected: <provider>
Estimated cost: free
```

Admitted providers come first, best score first. Rejected providers follow in
provider-name order with their first failed [reason code](scheduling.md#reason-codes).
`observed never observed` means the provider has no snapshot yet. A non-zero
estimated cost is printed as a bare integer.

```shell
$ vagabond job plan -meta version=v1.4.2 go-test.vagabond.hcl
go-test.test (container)
gcp-cloud-run  admitted  score 91            observed 2026-09-28 14:02:11Z
homelab        admitted  score 88            observed 2026-09-28 14:02:09Z
lambda         rejected  driver-unsupported  The task uses the container driver and this provider offers function.
Selected: gcp-cloud-run
Estimated cost: free
```

With `-verbose`, scorer values in [0, 1] and additional rejection reasons are
indented under each row:

```shell
$ vagabond job plan -verbose -meta version=v1.4.2 go-test.vagabond.hcl
go-test.test (container)
gcp-cloud-run  admitted  score 91  observed 2026-09-28 14:02:11Z
                           headroom 0.82
                           affinity 1.00
homelab        admitted  score 88  observed 2026-09-28 14:02:09Z
                           headroom 0.75
                           affinity 1.00
lambda         rejected  driver-unsupported  The task uses the container driver and this provider offers function.
                           image-unsupported
Selected: gcp-cloud-run
Estimated cost: free
```

When nothing can run a task:

```shell
$ vagabond job plan -meta version=v1.4.2 arm.vagabond.hcl
go-test.test (container)
gcp-cloud-run  rejected  constraint-unmet    The job requires provider.architecture set_contains "arm64" and this provider publishes "amd64".
lambda         rejected  driver-unsupported  The task uses the container driver and this provider offers function.
No provider can run this task.
```

If at least one rejection is transient (quota, health), the plan adds
`At least one rejection is transient, so this may be admitted later.`

### `job run`

```text
vagabond job run [options] <path>
```

Validates the job file locally, sends it to the server, and waits for the run
to finish. The job is not registered. The server makes the same selection
`job plan` shows.

- Tasks run in declaration order; the run stops at the first task that fails.
- A provider that gives no answer is abandoned for the next eligible one, as
  far as the task's [`retry`](job-specification.md#retry-block) block allows.
  A task that ran and exited non-zero is never retried.
- The CLI polls the run every second. Progress lines (`==> ...`) go to
  stderr. Each task's output goes to stdout when the task finishes, not while
  it runs.
- Interrupting the command (SIGINT or SIGTERM) cancels the unfinished
  execution on the server, waiting up to 30 seconds for the cancel, then exits
  2.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |
| `-meta <key>=<value>` | none | Job metadata, repeatable. Each task receives `VAGABOND_META_<KEY>` |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace, for a job that names none |
| `-no-logs` | `false` | Do not fetch or print task output. Progress and the exit status are still reported |

| Exit | Meaning |
|---|---|
| `0` | Every task ran and exited zero |
| `1` | A task ran and failed, or the job was invalid or could not be sent |
| `2` | The work never ran: nothing was eligible, every provider failed, or the run was interrupted |

**Progress lines:**

| Line | When |
|---|---|
| `==> dispatch <dispatch-id> of "<job>"` | The server accepted the run |
| `==> <task> <state> on <provider>` | An execution changed state (`pending`, `submitted`, `accepted`, `running`) |
| `... (attempt <n>)` | Appended from the second attempt on |
| `... (<class> failure)` | Appended when the execution failed without a result (`infrastructure`, `internal`) |
| `==> <task> succeeded on <provider> in <duration>` | The task exited 0 |
| `==> <task> failed (exit <n>) on <provider> in <duration>` | The task exited non-zero |
| `Output was truncated.` | Stored output hit its limit; only the tail is kept |
| `Job "<job>" did not run: <error>` | The run ended with no answer; followed by `Run vagabond job plan to see where it can run.` |
| `Job "<job>" was interrupted. The execution was stopped.` | Interrupted |

```shell
$ vagabond job run -meta version=v1.4.2 go-test.vagabond.hcl
==> dispatch 01926f3a-7c40-7a55-8c11-2d4e7d03e612 of "go-test"
==> test running on gcp-cloud-run
ok  	example.com/service/api	0.412s
ok  	example.com/service/store	1.087s
==> test succeeded on gcp-cloud-run in 1m42.318s
```

A reroute after an infrastructure failure:

```shell
$ vagabond job run -meta version=v1.4.2 go-test.vagabond.hcl
==> dispatch 01926f3a-9e12-7c03-b5a0-6f1d4a8e2c77 of "go-test"
==> test submitted on gcp-cloud-run
==> test failed on gcp-cloud-run (infrastructure failure)
==> test running on homelab (attempt 2)
ok  	example.com/service/api	0.405s
==> test succeeded on homelab in 2m03.551s
```

No eligible provider:

```shell
$ vagabond job run -meta version=v1.4.2 arm.vagabond.hcl
==> dispatch 01926f3b-0417-7e8d-a2c9-1b5f0e6d3a90 of "go-test"
Job "go-test" did not run: task "test": no provider can run this task
Run vagabond job plan to see where it can run.
$ echo $?
2
```

### `job register`

```text
vagabond job register [options] <path>
```

Stores the job on the server so it can be run with `job dispatch`. Nothing
runs.

- A new version is created only when the job changed. The comparison is on
  the HCL-formatted source, so whitespace and alignment changes do not make a
  new version.
- Registering a stopped job creates a new version and makes it dispatchable
  again.
- The job is validated in full with each declared metadata key standing as
  its own literal text (`${meta.version}`). A reference to an undeclared key
  fails here. See [supplying values](job-specification.md#supplying-values).

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace, for a job that names none |

| Exit | Meaning |
|---|---|
| `0` | Registered, or unchanged |
| `1` | Invalid job or server error |

```shell
$ vagabond job register go-test.vagabond.hcl
Job "go-test" registered as version 3 in namespace "default".

$ vagabond job register go-test.vagabond.hcl
Job "go-test" is unchanged at version 3 in namespace "default".
```

### `job dispatch`

```text
vagabond job dispatch [options] <name>
```

Runs the current version of a registered job and waits for it, with the same
progress, output, interrupt handling and exit codes as
[`job run`](#job-run). Every execution records the job version and a dispatch
ID shared by the run's tasks.

Metadata is checked against the job's `parameterized` block: a job without one
takes no `-meta`; otherwise every key must be declared and every
`meta_required` key supplied.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |
| `-meta <key>=<value>` | none | Job metadata, repeatable |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace the job is registered in |
| `-no-logs` | `false` | Do not fetch or print task output |

```shell
$ vagabond job dispatch -meta version=v1.4.2 go-test
==> dispatch 01926f3c-2b88-7f10-9e47-3c6a5d1e8b02 of "go-test"
==> test running on homelab
ok  	example.com/service/api	0.398s
==> test succeeded on homelab in 58.204s

$ vagabond job dispatch go-test
Error: job "go-test" requires metadata that was not supplied: version

$ vagabond job dispatch -meta version=v1.4.2 -meta colour=blue go-test
Error: job "go-test" does not declare metadata colour; declare it in meta_required or meta_optional

$ vagabond job dispatch nightly-lint
Error: job is stopped: "nightly-lint"; register it again to dispatch it
```

### `job status`

```text
vagabond job status [options] [name]
```

With no name, lists every job registered in the namespace, stopped ones
included. With a name, shows that job's versions and its most recent
executions.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace |

| Exit | Meaning |
|---|---|
| `0` | Listed or shown, including an empty namespace |
| `1` | Job not registered, server error, or more than one argument |

```shell
$ vagabond job status
NAME          VERSION  STATUS      UPDATED
go-test       3        registered  2026-09-28 13:40:02Z
nightly-lint  1        stopped     2026-09-21 09:12:44Z

$ vagabond job status -namespace ci
No jobs are registered in namespace "ci".
```

`STATUS` is `registered` (dispatchable) or `stopped`.

```shell
$ vagabond job status go-test
Name      = go-test
Namespace = default
Version   = 3
Status    = registered

Versions
VERSION  REGISTERED
3        2026-09-28 13:40:02Z
2        2026-09-26 18:03:51Z
1        2026-09-25 11:20:09Z

Recent executions
DISPATCH  VERSION  TASK  PROVIDER       STATE      CREATED
4c1e9a2f  3        test  gcp-cloud-run  succeeded  2026-09-28 13:41:10Z
b7d03e61  3        test  homelab        failed     2026-09-28 13:35:47Z
```

`DISPATCH` is the last eight characters of the dispatch ID. IDs are UUIDv7,
whose leading characters are a timestamp shared by runs started close
together. With no executions the section reads `None.`

```shell
$ vagabond job status unknown
Error: job not registered: "unknown" in namespace "default"
```

### `job stop`

```text
vagabond job stop [options] <name>
```

Deregisters a job. It can no longer be dispatched; its versions and executions
are kept, and `job register` makes it dispatchable again. Executions already
running are not affected.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |
| `-namespace <name>` | `$VAGABOND_NAMESPACE`, then `default` | Namespace the job is registered in |

| Exit | Meaning |
|---|---|
| `0` | Stopped |
| `1` | Job not registered, or server error |

```shell
$ vagabond job stop nightly-lint
Job "nightly-lint" stopped in namespace "default".
```

## Execution commands

An execution is one attempt of one task on one provider. Execution IDs are
listed in a dispatch's record, `GET /v1/dispatch/<id>` in the [API](api.md);
`job run` and `job dispatch` print the dispatch ID.

### `execution status`

```text
vagabond execution status [options] <id>
```

Prints one execution's record. Fields with no value yet are left out.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |

| Field | Description |
|---|---|
| `ID` | Execution ID |
| `Namespace` | Namespace of the job |
| `Job` | Job name and version (version 0 for a `job run` of an unregistered file) |
| `Dispatch` | Dispatch ID shared by the run's executions |
| `Task` | Task name |
| `Provider` | Provider it was sent to |
| `Attempt` | Attempt number within the task, from 1 |
| `Previous` | Execution ID of the previous attempt |
| `State` | `pending`, `submitted`, `accepted`, `running`, `succeeded`, `failed`, `cancelled` or `lost` |
| `Provider ID` | The provider's own identifier: Cloud Run execution name, Lambda function, or pool node name |
| `Failure` | Failure class when it failed without a result: `infrastructure` or `internal` |
| `Created`, `Started`, `Ended` | Timestamps |
| `Duration` | Run time, once there is a result |
| `Exit Code` | Task exit code, once there is a result |
| `Billed` | What the provider reported billing: millicores, MiB and duration |

```shell
$ vagabond execution status 01926f3a-7c4e-7b1a-9d2e-5b8f4c1e9a2f
ID          = 01926f3a-7c4e-7b1a-9d2e-5b8f4c1e9a2f
Namespace   = default
Job         = go-test (version 3)
Dispatch    = 01926f3a-7c40-7a55-8c11-2d4e4c1e9a2f
Task        = test
Provider    = homelab
Attempt     = 1
State       = succeeded
Provider ID = nuc-01
Created     = 2026-09-28 13:41:10Z
Started     = 2026-09-28 13:41:11Z
Ended       = 2026-09-28 13:42:09Z
Duration    = 58.204s
Exit Code   = 0
```

| Exit | Meaning |
|---|---|
| `0` | Printed |
| `1` | Not found, server error, or wrong number of arguments |

### `execution logs`

```text
vagabond execution logs [options] <id>
```

Prints the output stored for an execution once it has a result: the last
64 KiB. Nothing is printed for an execution with no stored output.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |

```shell
$ vagabond execution logs 01926f3a-7c4e-7b1a-9d2e-5b8f4c1e9a2f
ok  	example.com/service/api	0.398s
```

| Exit | Meaning |
|---|---|
| `0` | Printed, or nothing stored |
| `1` | Not found, server error, or wrong number of arguments |

## Node commands

### `node status`

```text
vagabond node status [options]
```

Lists the [agent](agent.md) nodes connected to the server now. A node that
loses its connection leaves the list until it reconnects. Takes no arguments.

| Flag | Default | Description |
|---|---|---|
| `-address <addr>` | `$VAGABOND_ADDR`, then `http://127.0.0.1:4747` | Server |

| Column | Description |
|---|---|
| `NAME` | Node name (`-name` on the agent) |
| `POOL` | Pool the node joined (`-pool`) |
| `ADDRESS` | Address the agent connected from |
| `ARCH` | Node architecture |
| `CPU USED` | Millicores used by running workloads / capacity |
| `MEMORY USED` | MiB used by running workloads / capacity |
| `WORKLOADS` | Running workloads |
| `CONNECTED` | When the current connection was made |

```shell
$ vagabond node status
NAME    POOL     ADDRESS          ARCH   CPU USED    MEMORY USED    WORKLOADS  CONNECTED
nuc-01  homelab  10.0.4.21:51432  amd64  1000/4000m  1024/15872MiB  1          2026-09-28 09:14:03Z
pi-02   homelab  10.0.4.35:40118  arm64  0/4000m     0/7680MiB      0          2026-09-28 09:14:07Z

$ vagabond node status
No agent nodes are connected.
```

## `server`

```text
vagabond server [options]
```

Runs the server until SIGINT or SIGTERM: the HTTP API under `/v1` on the
`server` block's `bind` address (`127.0.0.1:4747` by default, with TLS when the
block names a certificate and key), and the agent listener on `agent_bind`
(`127.0.0.1:4748` by default). See [configuration](configuration.md) and
[deployment](deployment.md).

| Flag | Default | Description |
|---|---|---|
| `-config <path>` | `$VAGABOND_CONFIG`, then the search path | Configuration file, or a directory whose `.hcl` files are all loaded |
| `-dev` | `false` | Keep jobs, executions and quota usage in memory, lost on exit. A `store` block is ignored with a warning |
| `-log-level <level>` | `info` | `debug`, `info`, `warn` or `error`. Requests log at `debug`, dispatches at `info` |

**Configuration discovery:** `-config`, else `$VAGABOND_CONFIG`, else the first
that exists of:

1. `./vagabond.hcl`
2. `<user config dir>/vagabond/config.hcl` (`$XDG_CONFIG_HOME` or
   `~/.config` on Linux, `~/Library/Application Support` on macOS)
3. `/etc/vagabond.d`

A path named by the flag or the variable that does not exist is an error; it
does not fall through to the search path.

**Startup sequence:**

1. Load configuration and build the providers.
2. Refresh every provider's capabilities. A provider that does not answer is
   marked unhealthy and rejected with `provider-unhealthy` until a later
   refresh succeeds; the server still starts, warning
   `Some providers did not answer: <error>`.
3. Without `-dev`: connect to the store, apply migrations and verify the
   schema (see [database](database.md)).
4. Listen for agents, then serve the API.

**Shutdown:** in-flight requests drain. Running dispatches are left to the
next server, which resumes them once their leases lapse (see
[background services](background-services.md)).

**Logs:** text-format `slog` lines on stderr.

**Startup errors (exit 1):**

| Message | Cause |
|---|---|
| `no configuration found. Looked in vagabond.hcl, <user dir>/vagabond/config.hcl, /etc/vagabond.d. Pass -config or set VAGABOND_CONFIG` | Nothing on the search path |
| `configuration not found: -config names <path>, which cannot be read: ...` | `-config` or `$VAGABOND_CONFIG` names a missing path |
| Configuration diagnostics | Invalid configuration, rendered like job diagnostics |
| `No providers are configured, so there is nothing to run on.` | No `provider` blocks |
| `The server needs a store block, or -dev to keep everything in memory.` | No `store` block and no `-dev` |
| `could not open the store: ...` / `could not prepare the store: ...` | Store unreachable, migration or schema check failed |
| `loading the server certificate: ...` | TLS certificate or key unreadable |
| `Listening for agents on <addr>: ...` | Agent address in use or not bindable |
| `Invalid -log-level "<value>": use debug, info, warn or error.` | Bad log level |

With `-dev` and a `store` block present, the server warns
`Running with -dev: the store block is ignored and everything is kept in memory.`

```bash
vagabond server -config /etc/vagabond.d -log-level debug
vagabond server -dev -config ./vagabond.hcl
```

Exit `0` on a clean shutdown.

## `agent`

```text
vagabond agent [options]
```

Runs the agent on a node: dials the server's agent address, registers the node
into a pool, and runs the workloads the server sends on the node's containerd
until SIGINT or SIGTERM. Workloads keep running when the agent stops, and it
finds them again when it starts. See [agent](agent.md) for requirements,
cgroup setup and pools.

**Requirements** (checked before the node registers): cgroup v2, access to
containerd's socket, and permission to create cgroups under the agent's own
(`Delegate=yes` under systemd, or `--cgroupns=host` with `/sys/fs/cgroup`
writable in a container).

| Flag | Default | Description |
|---|---|---|
| `-server <addr>` | `127.0.0.1:4748` | Server's agent address (`agent_bind`), not its API address |
| `-pool <name>` | `default` | Pool the node joins. Jobs reach it through a `pool` provider of this name |
| `-name <name>` | hostname | Node name, shown in `node status` and as the execution's provider ID |
| `-label <key>=<value>` | none | Node label, repeatable. Published as `node.label.<key>` for [constraints](job-specification.md#attributes). Same rules as `-meta`: a key twice is an error |
| `-cpu <millicores>` | `0` (no cap) | Cap on CPU the agent offers |
| `-memory <MiB>` | `0` (no cap) | Cap on memory the agent offers |
| `-containerd <path>` | `/run/containerd/containerd.sock` | containerd socket |
| `-data-dir <path>` | `/var/lib/vagabond` | Where workload output is kept. In a container, must be the same path inside and out |
| `-cgroup-parent <path>` | agent's own cgroup | Operator-prepared cgroup to create workloads under, instead of delegating from the agent's own |
| `-log-level <level>` | `info` | `debug`, `info`, `warn` or `error` |

**Capacity** is the smallest of the host's CPU and memory, the limits on the
agent's cgroup, and `-cpu` / `-memory`. Workloads run in the containerd
namespace `vagabond`.

**Startup errors (exit 1):**

| Message | Cause |
|---|---|
| `Reading the node: ...` | Host CPU, memory or cgroup could not be read |
| Cgroup delegation error | No `-cgroup-parent` and the agent's cgroup cannot be delegated |
| containerd error | Socket missing or not reachable |
| `Finding workloads from before: ...` | Recovering existing workloads failed |
| `Invalid -log-level "<value>": use debug, info, warn or error.` | Bad log level |

```bash
vagabond agent \
  -server 10.0.4.2:4748 \
  -pool homelab \
  -label disk=ssd \
  -cpu 4000 -memory 15872
```

On start the agent logs one `msg=node` line on stderr with the node's name,
pool, capacity and workload cgroup.
