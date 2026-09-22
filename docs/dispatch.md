# Dispatch

Dispatch takes the selection [ranking](scheduling.md) produced and runs it:
submit, watch until terminal, collect the result, release what the provider left
behind.

Synchronous and stateless. A run holds its execution in memory, so a process
that dies mid-run loses it.

## The rule

A workload failure is an answer. A task that ran and exited non-zero has told
you something true, and running it again elsewhere spends capacity to be told
the same thing.

| Failure | Behaviour |
|---|---|
| Non-zero exit code | Ends the task. Never retried elsewhere. |
| `infrastructure` | Next provider, if the budget allows |
| `internal` | Stops immediately; it will reproduce anywhere |
| Unclassified | Treated as not reroutable |

## Retry budget

Read from the task's [`retry` block](job-specification.md#retry-block).

| Configuration | Attempts |
|---|---|
| No `retry` block | 1 |
| `attempts = 2`, `reroute = true` | 2, on the two highest-ranked providers |
| `attempts = 2`, no `reroute` | 1 |
| `attempts` above the candidate count | capped at the candidate count |

`attempts` is total attempts, not attempts after the first.

Without `reroute` a task gets one provider. Retrying in place would spend
capacity on a provider that just failed, and the budget is capped by the
candidate count because trying the same provider again for an outage it is still
having is a slower way to reach the same place.

Each attempt gets a fresh execution ID. The ID is the submission idempotency
key, so reusing it would ask the provider to resume the run that just failed.

## Two schedules

Poll intervals double from 2s to a 15s ceiling. Retry backoff doubles from the
`backoff` block's `initial` to its `max`, defaulting to 5s and 30s.

Cloud Run spends roughly 100–120 seconds provisioning before a container starts,
so a short fixed poll interval spends a dozen API calls learning nothing.

## Job execution

Tasks run in declaration order, and the job stops at the first task that fails
or exits non-zero. The specification has no dependency graph, so a job's tasks
read as steps.

The outcome is returned even when the run failed, so a caller can report what
did happen before the failure.

## Live output

Where a provider implements `plugin.LogStreamer`, output is streamed while the
execution runs. Providers that do not implement it print nothing until the
result arrives.

The stream is a view, never the record. A broken stream never fails an
execution, and `Result` remains the authoritative fetch for the exit code and
the complete output.

After the execution reaches a terminal state, the stream is held open for a
further 5 seconds. Cloud Logging runs seconds behind the container, so a job
that finishes promptly finishes before its own last lines are readable. A job
shorter than the ingestion lag will not appear to stream at all; its output
arrives in one burst during that window.

`TaskOutcome.Streamed` reports whether any bytes actually reached the writer,
not whether a stream was opened. A stream that delivered nothing must not
suppress printing the result's own copy.

## Release

After `Result` succeeds, dispatch calls `Release` on providers that implement
`plugin.Releaser`.

Cloud Run has no ad-hoc run: every execution needs a Job resource that exists
until something deletes it. Without this, every run leaks one against a
per-region quota.

Release happens after the result because releasing may destroy what the result
reads. It is best effort on its own context, so a caller who has already stopped
waiting still gets the resource cleaned up, and a failure does not fail the
execution. The provider's own sweep is the backstop.

## Cancellation

When the caller's context is cancelled, dispatch stops the execution on the
provider rather than leaving it running and billing. That call gets its own
context, since the caller's is the one that was cancelled.

## Errors

| Error | Meaning |
|---|---|
| `ErrNoCandidates` | Admission admitted nothing; the outcome carries the rejections |
| `ErrExhausted` | Every attempt ended in an infrastructure failure |
| `ErrNoProvider` | A ranked candidate the registry has no plugin for |
