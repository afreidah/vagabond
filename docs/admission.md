# Admission

Admission decides which providers *can* run a task. [Ranking](scheduling.md)
decides which one *should*.

Input is a task plus one capability and quota snapshot per provider. Admission
does not call providers, does not touch the network, and reserves nothing.

Output is a list of candidates and a list of rejections, each rejection naming a
provider, a reason code, and a detail string.

## Checkers

13 checkers run against every provider, in this order.

| # | Checker | Reason code | Tier |
|---|---|---|---|
| 1 | allowlist | `not-allowlisted` | policy |
| 2 | cost | `cost-policy` | policy |
| 3 | enabled | `provider-disabled` | availability |
| 4 | healthy | `provider-unhealthy` | availability |
| 5 | driver | `driver-unsupported` | mismatch |
| 6 | arch | `arch-unsupported` | mismatch |
| 7 | image | `image-unsupported` | mismatch |
| 8 | network | `network-unsupported` | mismatch |
| 9 | resources | `resources-exceeded` | mismatch |
| 10 | duration | `duration-exceeded` | mismatch |
| 11 | attribute | `attribute-unknown` | mismatch |
| 12 | constraint | `constraint-unmet` | mismatch |
| 13 | quota | `quota-exhausted` | capacity |

Every checker runs against every provider. The first rejection carries the
detail string and is what `job plan` displays; the rest are collected as codes
and shown under `-verbose`.

## Why the order matters

The plan table gives each provider one line, so whichever checker rejects first
is the answer a person reads.

**Policy first.** A provider the job excluded is excluded whether or not it
could have run the work. Reporting an unsupported driver instead invites a fix
to a job that was never trying to go there.

**Availability second.** A disabled or unhealthy provider has a fingerprint
nobody refreshed, so every capability comparison below it is made against stale
or absent data. Run the mismatch tier first and a disabled provider is rejected
for offering no drivers — true of its empty snapshot, and useless as an
explanation.

**Mismatch third, cheapest first.** Set membership, then booleans, then integer
comparisons, then constraint evaluation, which builds an attribute map. These
say the pairing is impossible, which is the most actionable thing a job author
can be told.

**Capacity last.** A provider that will never run this driver should say so
rather than report its quota.

## Reason codes

| Code | Meaning |
|---|---|
| `not-allowlisted` | The job's `providers` list excludes this provider |
| `cost-policy` | `provider.estimated_cost` exceeds the job's `max_cost_usd`, which defaults to 0 |
| `provider-disabled` | `enabled = false` in configuration |
| `provider-unhealthy` | Provider did not answer its last capability refresh |
| `driver-unsupported` | Provider does not offer the task's driver |
| `arch-unsupported` | Provider does not offer the required architecture |
| `image-unsupported` | Task names an image; provider cannot run arbitrary images |
| `network-unsupported` | Provider cannot satisfy the `network` block |
| `resources-exceeded` | Task asks for more CPU or memory than the provider offers |
| `duration-exceeded` | Task `timeout` exceeds the provider's maximum |
| `attribute-unknown` | A constraint names an unpublished `provider.*` attribute |
| `constraint-unmet` | A constraint evaluated false |
| `quota-exhausted` | No free allowance left, and the job will not pay |

## Quota and willingness to pay

The quota checker rejects only jobs that will not pay, meaning `max_cost_usd` is
unset or zero.

- A job with `max_cost_usd > 0` is admitted to a provider with no free
  allowance left.
- A quota snapshot that was never observed is treated as spent.

An unknown allowance and a spent one lead to the same decision, and the decision
that spends money is not the one to guess toward.

## Unknown attributes

A constraint naming something under `provider.` that Vagabond does not publish
is rejected as `attribute-unknown`.

The alternative is a job that silently matches no provider and reports as having
no capacity, which is the most misleading failure this can produce.

`provider.meta.*` is exempt. Those names are the operator's and are deliberately
unchecked.

## Output ordering

Candidates and rejections are sorted by provider name, so a plan of the same
inputs is byte-identical and can be diffed in CI.
