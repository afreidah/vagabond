# Quickstart

Plan a job against a set of providers. Nothing is dispatched and no credentials
are required.

## Build

```bash
git clone https://github.com/afreidah/vagabond
cd vagabond
make build
```

## Validate a job file

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

## Plan it

```bash
./vagabond job plan \
  -config examples/config.hcl \
  -meta version=1.2.3 \
  examples/go-test.vagabond.hcl
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
./vagabond job plan -config examples/config.hcl -meta version=1.2.3 \
  -verbose examples/go-test.vagabond.hcl
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
cat job.vagabond.hcl | ./vagabond job plan -config examples/config.hcl -
```

## Next

- [Job specification](job-specification.md) — what goes in a job file
- [Configuration](configuration.md) — what goes in the provider config
- [Cloud Run provider](providers/cloud-run.md) — configuring a real backend
