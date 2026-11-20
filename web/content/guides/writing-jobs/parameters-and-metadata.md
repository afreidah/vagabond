---
title: "Parameters and Metadata"
description: "Declare the metadata every run of a job must supply, pass it to the task as environment variables, and see what validate, plan and dispatch refuse."
weight: 30
---

A parameterized job takes values at each run, so one registered job serves
every repository, commit or target. This page builds the `static-analysis`
job, which a CI workflow dispatches with the repository, the commit and the
make targets.

## Step 1: Declare the Parameters

```hcl
job "static-analysis" {
  parameterized {
    meta_required = ["repo", "commit", "targets"]
  }

  task "make" {
    driver = "container"

    config {
      image   = "golang:1.27"
      command = "sh"
      args = [
        "-ec",
        "git init -q /src; cd /src; git remote add origin \"https://github.com/$VAGABOND_META_REPO.git\"; git fetch -q --depth 1 origin \"$VAGABOND_META_COMMIT\"; git checkout -q FETCH_HEAD; make $VAGABOND_META_TARGETS",
      ]
    }

    # resources, timeout and routing as in the other pages
  }
}
```

| Setting | Effect |
|---|---|
| `meta_required` | Every run must supply these keys, with `-meta <key>=<value>` |
| `$VAGABOND_META_REPO` | Each key reaches the task as an environment variable: `VAGABOND_META_` and the key upper-cased |

The shell reads the variables at run time, so the job file itself needs no
value for them.

## Step 2: Validate With and Without Values

A run, and `job validate`, must supply every required key:

```shell
$ vagabond job validate static-analysis.vagabond.hcl
Error: Unset metadata for job "static-analysis"

  on static-analysis.vagabond.hcl line 17, in job "static-analysis":
  17:     meta_required = ["repo", "commit", "targets"]

This job requires metadata that was not supplied: repo, commit, targets. Pass
each one with -meta <key>=<value>.

$ vagabond job validate -meta repo=afreidah/vagabond -meta commit=5fbb16230b8b \
    -meta targets="vet lint" static-analysis.vagabond.hcl
Job specification static-analysis.vagabond.hcl is valid.
```

## Step 3: Register It

```shell
$ vagabond job register static-analysis.vagabond.hcl
```

Registering takes no values: the job is stored as written, and each dispatch
brings its own.

## Step 4: Plan and Dispatch by Name

A registered job is planned and dispatched by its name. Both take exactly the
declared keys: a key the job does not declare is refused, and so is a missing
one:

```shell
$ vagabond job plan -meta repo=a/b -meta commit=c -meta targets=lint -meta branch=main static-analysis
Error: job "static-analysis" does not declare metadata branch; declare it in meta_required or meta_optional

$ vagabond job plan -meta repo=a/b static-analysis
Error: job "static-analysis" requires metadata that was not supplied: commit, targets
```

A key that may be left out goes in `meta_optional` beside `meta_required`. With
all three keys, a dispatch runs:

```shell
$ vagabond job dispatch -meta repo=afreidah/vagabond -meta commit=5fbb16230b8b \
    -meta targets="vet lint fmt-check govulncheck" static-analysis
```

[Running CI on Free Cloud Quota](../ci-on-free-quota.md) dispatches it from a
CI workflow, and [Following and Finding Runs](../following-runs.md) shows its
output.
