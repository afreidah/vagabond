---
title: "Style Guide"
seoTitle: "Go Style Guide for Contributors"
linkTitle: "Style guide"
description: "Vagabond's Go conventions: comment and file-header format, package layout, architectural boundaries, error handling, testing, and branch naming."
weight: 710
---

**Author:** Alex Freidah

This guide is derived from the conventions used in
[`s3-orchestrator`](https://github.com/afreidah/s3-orchestrator). Where a rule
is identical it is reproduced here so that this repository is self-contained;
where Vagabond's architecture differs, the rule differs with it.

---

## Table of Contents

- [Core Principles](#core-principles)
- [Comment Types and Spacing](#comment-types-and-spacing)
- [File Headers](#file-headers)
- [Package Docs (doc.go)](#package-docs-docgo)
- [Go Conventions](#go-conventions)
- [Interface Design](#interface-design)
- [Architectural Boundaries](#architectural-boundaries)
- [Error Handling](#error-handling)
- [Testing](#testing)
- [Code Style](#code-style)
- [Branch Naming](#branch-naming)
- [Quick Reference](#quick-reference)

---

## Core Principles

- **ASCII-only characters** - never Unicode em-dashes, en-dashes, or
  box-drawing characters
- **Dashes, not equals** - always `-` for dividers, never `=`
- **Box comment spacing** - all box comments, both the file header and the
  section box, are always followed by a blank line
- **Professional tone** - no personal references, no numbered lists in
  comments, no casual language
- **Self-documenting** - code explains *why*, not just *what*
- **Context propagation** - `context.Context` passes through all function
  chains for cancellation and tracing
- **Boundaries are mechanical** - the architectural rules in this guide are
  enforced by `depguard` in `.golangci.yml`, not by reviewer memory

---

## Comment Types and Spacing

### File Header

`// ` followed by 79 dashes, 82 characters in all.

```go
// -------------------------------------------------------------------------------
// Title of File or Component
//
// Author: Alex Freidah
//
// Two to four sentences describing the file's purpose, scope, and key
// functionality. Include architecture notes, design decisions, or important
// context that helps a reader understand why the file exists.
// -------------------------------------------------------------------------------

package mypackage
```

Spacing rules:

- Blank line after the title
- Blank line after the metadata
- No blank line before the closing divider
- Blank line after the closing divider, always separating the box from code

### Major Section Box

`// ` followed by 73 dashes, 76 characters in all.

```go
// -------------------------------------------------------------------------
// SECTION NAME
// -------------------------------------------------------------------------

func doSomething() {
	// ...
}
```

Spacing rules:

- ALL CAPS for the section name
- Blank line after the closing divider
- The box goes above the first declaration's doc comment, never between a doc
  comment and its declaration

**When to use them.** Every file with more than one logical group of
declarations carries section boxes, so a reader lands in a familiar shape
whichever file they open, and every declaration in it sits under one. A file
with a single concern does not: one under roughly 100 lines, or one that
defines or tests a single type at any length. A box per file is noise, not
structure. In a test file, the fixtures and helpers count as a group of their
own.

**Section vocabulary.** Prefer these names, in this order, so the same concept
is not called three things across the tree:

| Section | Holds |
|---------|-------|
| `CONSTANTS` | package-level constant blocks |
| `TYPES` | the types a file declares |
| `INTERFACE` | a consumer-declared or role interface |
| `CONSTRUCTOR` | `New*` and the wiring it needs |
| `COMMAND` | a CLI command's `Help`, `Synopsis`, and `Run` |
| *domain sections* | the file's actual work, named for it: `ADMISSION`, `SCORING`, `DISPATCH`, `QUOTA LEDGER`, `INGEST` |
| `LIFECYCLE` | `Close`, `Shutdown`, and other teardown |
| `INTERNALS` / `HELPERS` | unexported helpers the sections above call |
| `FIXTURES` / `HARNESS` | in tests, the doubles and fixtures the tests share, and the setup that wires them |

Domain sections carry the weight. A file split into `ADMISSION` /
`REJECTION REASONS` / `INTERNALS` tells a reader more than one split into
`PUBLIC API` / `PRIVATE`.

### Comments Inside Declaration Blocks

A comment line above a single entry inside a `struct`, `interface`, `const`, or
`var` block breaks the block's visual flow and makes a block that should read at
a glance three times its height. Two forms are allowed instead.

End-of-line, when the note is short:

```go
const (
	ClassInfrastructure Class = "infrastructure" // provider broke  -  reroute is allowed
	ClassInternal       Class = "internal"       // Vagabond's own bug  -  never reroute
)
```

In the block's doc comment, when the note carries real reasoning:

```go
// Capabilities is what a provider advertises about itself so that admission
// can decide without calling it.
//
// ObservedAt is carried so that admission can distinguish current data from
// data collected before an outage. Without it there is no way to decline to
// decide on stale input, and a provider that has been down for an hour looks
// exactly like one that is healthy.
type Capabilities struct {
	Drivers       []DriverName
	Architectures []Arch
	MaxDuration   time.Duration
	ObservedAt    time.Time
}
```

An entry whose name already says what it is gets no comment at all.

### Single-Line Comments

```go
// Reject before scoring so that a stale snapshot never reaches the scheduler
if snap.Age() > maxSnapshotAge {
	return rejected(ReasonStaleCapabilities)
}
```

One line, at a step that is not obvious. No blank line before the code.
Lowercase or sentence case.

### Inline Comments

```go
charge := task.Timeout // lost executions are charged their full declared bound
```

Used sparingly, explaining *why* rather than *what*, kept to one short clause.

---

## File Headers

Every `.go` file except `doc.go` and generated code starts with the file
header above. The same box, with `#` in place of `//` (81 characters), heads
every YAML file, Makefile, and HCL example in the repository.

---

## Package Docs (doc.go)

Every package carries a `doc.go` whose comment explains **why the package
exists and what design decision it embodies**, not what its name already says.

A doc comment that restates the package name is worse than none, because it
occupies the place where the reasoning should have been.

```go
// Package ledger keeps Vagabond's own account of what each provider's quota
// pools have been charged, since no provider reports remaining allowance in a
// form that can be scheduled against.
//
// An execution reserves its declared worst case before it is submitted and
// settles to what it cost once it ends. The store decides each reservation in
// one statement, so two processes charging the same pool cannot both see
// room. A reservation nobody settled stays charged until the reaper learns
// what became of the run: over-counting wastes allowance, under-counting
// spends money.
package ledger
```

---

## Go Conventions

### Indentation

One tab, `gofmt` enforced. Formatting is a lint failure, not a convention:
`make lint` runs `gofmt` and `goimports`, and `make fmt` applies both.

### Imports

Three blocks separated by blank lines, ordered stdlib, external, internal.
`goimports` with `local-prefixes: github.com/afreidah/vagabond` enforces the
last block:

```go
import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/quota"
)
```

### Naming

- Exported types get standard Go doc comments directly above the declaration
- Every function and method, exported or not, gets a doc comment of a line or
  two; a consumer interface or an ordering decision may take up to five to
  state its contract
- Constants grouped by concern in `const` blocks, named in `CamelCase`
- Sentinel errors use the `Err` prefix: `ErrQuotaExhausted`, `ErrUnknownDriver`
- Reason codes and other strings that cross the API boundary are declared once
  as constants and marked as stable surface

### Struct Organization

Group related fields, with end-of-line comments only where a field is not
self-evident:

```go
type Candidate struct {
	Provider      string
	Score         int
	EstimatedCost MicroUSD      // always zero while max_cost_usd is 0
	Capabilities  Capabilities  // the snapshot the decision was made against
}
```

---

## Interface Design

Vagabond follows "accept interfaces, return structs". Producer packages export
concrete types; **each consumer declares its own narrow interface** listing only
the methods it calls. The concrete type satisfies every consumer's interface
because Go interfaces are structurally typed.

The one deliberate exception is `Provider`, which is a genuine plugin boundary
rather than a consumer's view of a collaborator. It is declared once, on the
producer side, because every implementation is written against it by an author
who cannot see the consumers.

Rationale for consumer-declared interfaces elsewhere:

- A consumer's dependency footprint is documented in its own source file
- Adding a method to a producer never bloats existing consumer mocks
- Tests mock what is used, not the full producer surface

Naming convention is `<Consumer><Provider>`: the prefix names the consumer, the
suffix names the producer concept.

---

## Architectural Boundaries

These are the rules the whole design rests on. They are enforced mechanically
by `depguard`; this section explains why each one exists.

### The scheduler does not know what a cloud is

`internal/scheduler`, `internal/job`, and `internal/quota` must not import any
package under `internal/providers/`. Admission produces normalized candidates;
the scheduler scores them. If scheduling logic ever needs to know that a
candidate is IBM, the normalization is wrong and the fix belongs in the
capability model, not in a conditional.

### Cloud SDKs live in exactly one place

No package outside `internal/providers/` may import a cloud provider SDK. A
provider plugin translates a normalized task into whatever its cloud requires,
and that translation is the entire reason the package exists. An SDK import
anywhere else means a provider-specific type has escaped.

### Database access lives in exactly one place

No package outside `internal/state/` may import a database driver or
`database/sql`. Everything else receives a narrow interface.

### Admission performs no I/O

Shared admission rules are a pure function over a capability snapshot and a
quota snapshot. `vagabond job plan` fans out across every configured provider
and must stay fast, free, and side-effect-free by construction rather than by
discipline. Provider-specific limits are contributed through an optional
interface that is likewise pure.

---

## Error Handling

### The failure taxonomy

Every error that crosses a provider boundary carries one of two
classifications, and plugin authors are responsible for assigning it:

| Class | Meaning | Reroute? |
|---|---|---|
| `ClassInfrastructure` | the provider failed, the job never got a verdict | yes |
| `ClassInternal` | Vagabond's own fault | no, and make it loud |

A task that ran and exited non-zero carries no class: it is the task's result,
returned unchanged and never rerouted.

`ClassInternal` exists because misattributing Vagabond's own bug to the user's
workload is the worst available outcome: it reports a broken build to someone
whose build is fine, and because it is not rerouted, it never self-corrects.

**Retryability is an independent flag, not derived from the class.** A provider
429 and a provider 503 are both infrastructure failures wanting different
backoff, and a provider 400 is `internal`: Vagabond sent something wrong, and
it is never rerouted.

### Sentinel errors and wrapping

Sentinel errors use the `Err` prefix and are compared with `errors.Is`. Typed
errors carrying structured detail are extracted with `errors.As`. Every error
returned across a package boundary wraps its cause with `%w`.

### Background workers

Background workers log and continue rather than crashing. Individual item
failures are logged at warn level and skipped; the batch proceeds.

---

## Testing

- Test files live alongside the code they test
- Table-driven tests for anything with multiple input and output combinations
- `go.uber.org/mock/mockgen` for mocks, driven by `//go:generate` directives,
  with generated mocks committed alongside the interface they mock
- Test names follow `TestFunctionName_Scenario`
- A test's doc comment states the behaviour it proves in a sentence; it need
  not start with the test's name

### What the contract packages must prove

The contract packages have no I/O, so their tests need no fixtures beyond
literals. Where a type encodes a rule, the rule gets a test:

- illegal execution state transitions are rejected, naming both states
- an unknown driver name produces an error listing the valid set
- every admission rejection reason is reachable from a realistic input
- the reroute decision is a pure function of the failure class

### Integration tests

Integration tests live in `internal/integration/`, gated behind the
`integration` build tag, and use `testcontainers-go` so that the test process
manages container lifecycle itself. Nothing is started by hand.

---

## Code Style

### Character Rules

Always use the ASCII hyphen-minus `-` and standard ASCII characters. Never use
Unicode em-dashes, en-dashes, box-drawing characters, or equals signs as
dividers.

### Professional Tone

Avoid personal references, numbered lists in comments, conversational tone, and
future tense.

Use present tense, declarative statements, technical precision, and an
impersonal voice: "Admission rejects", "The dispatcher selects", "The ledger
over-counts".

---

## Branch Naming

When a branch corresponds to a GitHub issue:

```
GH_ISSUE_<issue number>-<description of topic>
```

Examples:

- `GH_ISSUE_1-repository-conventions`
- `GH_ISSUE_5-provider-interface`

For branches without a linked issue, a short kebab-case description of the
topic.

---

## Quick Reference

| Comment Type | Length | Spacing After | Use Case |
|-------------|--------|---------------|----------|
| File header | 82 chars | 1 blank line | Top of every `.go`, `.yml`, `.hcl` example, and Makefile |
| Major section | 76 chars | 1 blank line | Major divisions within a file |
| Single-line | One line | None | A non-obvious step within a function |
| Inline | One clause | N/A | Specific line explanation |
