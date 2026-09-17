# Contributing to Vagabond

Thanks for your interest in contributing. This document covers everything you
need to get started.

## Prerequisites

- **Go 1.27+** (version pinned in `go.mod`, resolved automatically through
  `GOTOOLCHAIN=auto`, so no manual install is required)
- **Make** (all common tasks have Makefile targets)
- **Docker** (integration tests only; not needed for unit tests)
- **jj** (optional; the repository is colocated, so plain git works too)

## Getting Started

```bash
# Clone the repository
git clone https://github.com/afreidah/vagabond.git
cd vagabond

# Install lint and codegen dependencies
make tools

# See every available target
make help

# Run the gate CI runs
make check
```

`make check` is formatting, vet, lint, tests, and a vulnerability scan. If it
passes locally it passes in CI, because CI runs the same targets with the same
pinned tool versions.

## Testing

### Unit tests

```bash
make test        # with the race detector, as CI runs it
make test-fast   # without, for quick iteration
make cover       # with a coverage profile
```

The contract packages perform no I/O, so their tests need no fixtures beyond
literals and no containers.

### Integration tests

```bash
make integration-test
```

Integration tests are gated behind the `integration` build tag and use
`testcontainers-go`, so the test process manages container lifecycle itself.
Docker must be running, but nothing needs starting by hand.

### Vulnerability scanning

```bash
make govulncheck
```

`govulncheck` is declared as a tool directive in `go.mod`, so its version is
pinned by the module graph rather than by whatever a developer happens to have
installed. The analysis is call-graph based: a vulnerable dependency is only
reported when a path to the affected symbol actually exists, so a finding is
always actionable.

## Code Style

Follow the conventions in [`docs/style-guide.md`](docs/style-guide.md). Key
points:

- All source files start with a 79-character box comment header
- Section dividers use 73-character dashes
- ASCII-only characters, no Unicode dashes or box drawing
- Every package carries a `doc.go` explaining *why the package exists*, not
  what its name already says
- Context propagation through all function chains

## Architectural Boundaries

Three rules carry the whole design, and `depguard` enforces all three
mechanically. Breaking one is a lint failure, not a review comment:

- **The scheduler does not know what a cloud is.** `internal/scheduler`,
  `internal/job`, and `internal/quota` cannot import `internal/providers/`.
- **Cloud SDKs live only in `internal/providers/`.** An SDK import elsewhere
  means a provider-specific type escaped the plugin boundary.
- **Database access lives only in `internal/state/`.** Everything else takes a
  narrow interface.

A fourth rule is not mechanically enforceable but matters as much: **admission
performs no I/O.** Shared admission rules are a pure function over a capability
snapshot and a quota snapshot, so that `vagabond job plan` is fast and
side-effect-free by construction.

## Version Control

The repository is a colocated jj/git repository. Both tools work against the
same underlying git repository, so use whichever you prefer.

Using jj:

```bash
jj new main -m "feat: add the thing"   # start a change on top of main
jj bookmark create GH_ISSUE_12-the-thing
jj describe                            # edit the message
jj split                               # split unrelated work apart
jj git push --bookmark GH_ISSUE_12-the-thing
```

Note that `jj describe --reset-author` does not exist; the current spelling for
correcting authorship on an existing change is `jj metaedit --update-author`.

## Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/) format:

```
feat: add new feature description
fix: correct bug in component
docs: update the provider landscape section
chore: bump golangci-lint
refactor: extract the rejection vocabulary
test: cover illegal execution transitions
```

## Branch Naming

When a branch corresponds to a GitHub issue:

```
GH_ISSUE_<issue number>-<description of topic>
```

Examples:

- `GH_ISSUE_1-repository-conventions`
- `GH_ISSUE_5-provider-interface`

For branches without a linked issue, use a short kebab-case description of the
topic.

## Pull Requests

The PR template includes a boundaries checklist. It duplicates what `depguard`
already enforces, deliberately: the checklist is there so that a reviewer
thinks about whether a *new* boundary should have been added, which no linter
can catch.
