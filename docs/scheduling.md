# Scheduling

Ranking orders the candidates [admission](admission.md) produced and selects
one.

## Scoring

Each scorer returns a value in `[0,1]`. A candidate's score is the arithmetic
mean of its scorers. The highest score is selected; ties break by provider name.

```
score = mean(headroom, affinity?)
```

`job plan` prints the score as a percentage. `-verbose` prints each scorer's
contribution.

```
ibm-code-engine     admitted  score 90  observed 2026-09-21 07:31:21Z
                                headroom 0.80
                                affinity 1.00
```

A mean rather than a sum: adding scorers would make a candidate's score depend
on how many scorers happened to apply, so a job with an affinity would outscore
the same job without one.

## Scorers

### `headroom`

The base scorer, selected by `routing.strategy`.

```
headroom = clamp(free_quota_percent / 100)
```

Prefers the provider with the most allowance remaining. Spending the scarcest
allowance first strands the workloads with the fewest eligible providers;
draining the emptiest last keeps the most options open.

Clamped to `[0,1]` so a ledger reporting more than a full allowance cannot drag
the mean off scale.

### `affinity`

Applied only when the job declares at least one `affinity` block.

```
affinity = matched weight / total weight
```

A job with two affinities weighted 75 and 25, where only the first matches,
scores 0.75.

Affinities apply on top of whichever base scorer the strategy chose.

## Strategies

| Strategy | Base scorer |
|---|---|
| `free-first` | `headroom` |

`free-first` is the default when `routing.strategy` is unset.

## Worked example

From `examples/config.hcl` and `examples/go-test.vagabond.hcl`:

| Provider | `free_percent` | headroom | affinity | score |
|---|---|---|---|---|
| `ibm-code-engine` | 80 | 0.80 | 1.00 | 90 |
| `gcp-cloud-run` | 45 | 0.45 | 0.00 | 23 |

The job declares one affinity, `provider.free_quota_percent > 50`, weight 75.
`ibm-code-engine` matches it and `gcp-cloud-run` does not, so the affinity
scorer returns 1.00 and 0.00 respectively.

## Selection

The ranking is returned in full, not just the winner. Every candidate keeps its
score and its per-scorer breakdown so a plan can explain the outcome rather than
assert it.
