# Quickstart

Start a dev server over fake providers and plan a job against it. Nothing is
dispatched to a cloud and no credentials are required.

## Build

```bash
git clone https://github.com/afreidah/vagabond
cd vagabond
make build
```

## Validate a job file

Validation is local; it needs no server.

```bash
./vagabond job validate examples/go-test.vagabond.hcl
```

The example job is parameterized, so validation fails until its metadata is
supplied:

```
Error: Unset metadata for job "go-test"

  on examples/go-test.vagabond.hcl line 41, in job "go-test":
  41:     meta_required = ["version"]

This job requires metadata that was not supplied: version. Pass each one with
-meta <key>=<value>.
```

```bash
./vagabond job validate -meta version=1.2.3 examples/go-test.vagabond.hcl
```

## Start a server

```bash
./vagabond server -dev -config examples/config.hcl
```

- `-dev` keeps jobs, executions and quota usage in memory, empty at every start.
- `examples/config.hcl` configures fake providers, so nothing reaches a cloud.
- It listens on `127.0.0.1:4747`, where every other command looks by default.
  Point them elsewhere with `-address` or `$VAGABOND_ADDR`.

The commands below run in a second terminal.

## Plan it

```bash
./vagabond job plan -meta version=1.2.3 examples/go-test.vagabond.hcl
```

```
go-test.test (container)
ibm-code-engine     admitted  score 90         observed 2026-09-21 07:30:59Z
gcp-cloud-run       admitted  score 23         observed 2026-09-21 07:30:59Z
aws-lambda          rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
cloudflare-workers  rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
Selected: ibm-code-engine
Estimated cost: free
```

One line per provider. Admitted providers show their score; rejected providers
show the first rule they failed.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | At least one provider can run every task |
| 1 | Some task has nowhere to run, or the plan could not be produced |

## Show the scoring

`-verbose` adds each scorer's contribution, and every rule a rejected provider
failed rather than only the first.

```bash
./vagabond job plan -meta version=1.2.3 -verbose examples/go-test.vagabond.hcl
```

```
go-test.test (container)
ibm-code-engine     admitted  score 90  observed 2026-09-21 07:31:21Z
                                headroom 0.80
                                affinity 1.00
gcp-cloud-run       admitted  score 23  observed 2026-09-21 07:31:21Z
                                headroom 0.45
                                affinity 0.00
aws-lambda          rejected  not-allowlisted  The job routes only to ibm-code-engine, gcp-cloud-run.
                                driver-unsupported
                                image-unsupported
```

`ibm-code-engine` wins because `examples/config.hcl` gives it 80% remaining
quota against `gcp-cloud-run`'s 45%, and the job's affinity prefers providers
above 50%.

## Reading from stdin

Pass `-` as the path:

```bash
cat job.vagabond.hcl | ./vagabond job plan -
```

## Running it

`job run` sends the job to the server, which dispatches it to the selected
provider; the command waits for it. A real backend is configured in the
server's config; see [Cloud Run](providers/cloud-run.md).

```bash
./vagabond server -config vagabond.hcl
./vagabond job run job.vagabond.hcl
```

```
==> dispatch 01926f3a-8c1e-7b2d-9f00-3c4d5e6f7a8b of "hello"
==> greet accepted on gcp-cloud-run
==> greet running on gcp-cloud-run
line-1
line-2
done
==> greet succeeded on gcp-cloud-run in 11.87s
```

- Progress goes to stderr and the task's output to stdout, so redirecting
  stdout captures the build alone.
- Output is printed when each task finishes.
- Ctrl-C stops the execution on the provider.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Every task ran and exited zero |
| 1 | A task ran and failed, or the job could not be sent |
| 2 | The work never ran: nothing was eligible, or every provider failed |

Expect roughly two minutes for a trivial Cloud Run job. Provisioning dominates;
see [Cloud Run](providers/cloud-run.md#cost-characteristics).

## Registering a job

`job run` registers nothing. To keep a job and run it again by name, register
it.

| Command | Does |
|---|---|
| `job register <file>` | Stores a new version if the job changed. Runs nothing. |
| `job dispatch <name> [-meta k=v]` | Runs the current version, reporting as `job run` |
| `job status [name]` | Lists jobs, or one job's versions and recent executions |
| `job stop <name>` | Deregisters; versions are kept, and registering again revives it |
| `job plan <name>` | Plans the current version |

```bash
./vagabond job register examples/go-test.vagabond.hcl
./vagabond job dispatch -meta version=1.2.3 go-test
```

A new version is made only when the job changed; formatting alone is not a
change. Every execution records the version it ran and a dispatch ID shared by
the run's tasks.

## Reading an execution

| Command | Does |
|---|---|
| `execution status <id>` | The execution's record and result |
| `execution logs <id>` | Its stored output, the last 64 KiB |

## Next

- [Job specification](job-specification.md): what goes in a job file
- [Configuration](configuration.md): providers, namespaces, quotas, store, server
- [Cloud Run provider](providers/cloud-run.md): configuring a real backend
- [Dispatch](dispatch.md): retries, rerouting, leases, cleanup
