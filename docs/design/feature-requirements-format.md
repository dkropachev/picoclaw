# Feature Requirements Format

## Decision

Use one canonical feature requirements format: the **Reconstruction Contract
Matrix**.

This format is optimized for two outcomes:

1. Humans can treat `docs/features/*.md` as the source of truth for product
   behavior.
2. A coding agent can recreate same or similar implementation behavior from the
   spec by following explicit contracts, state, ordering, tests, and anchors.

## Required Shape

Each feature spec must use these sections:

| Section | Purpose |
| --- | --- |
| `Feature ID` | Stable `FR-<FEATURE>` namespace for requirement IDs. |
| `Behavior Summary` | Capability boundary in user-facing terms. |
| `Reconstruction Notes` | Direct guidance for recreating similar code shape. |
| `Requirements` | The behavior contract matrix: observable trigger, output, state, failure, and rationale rows. |
| `Data And State Model` | Durable files, config, in-memory state, keys, IDs, schemas, and ownership boundaries. |
| `Surface Ownership` | Machine-readable `Owns:` mappings for APIs, config, tests, events, and code surfaces. |
| `Auxiliary Interfaces` | Public surface contract for HTTP, CLI, config, event, storage, runtime, and code APIs. |
| `Algorithms And Ordering` | Required validation, normalization, lookup, mutation, side effects, fallbacks, and emitted events. |
| `Cross-Feature Behavior` | Interactions with other feature specs and ownership boundaries. |
| `Failure And Edge Cases` | Defaults, invalid inputs, conflicts, security failures, retries, and resource limits. |
| `Acceptance Evidence` | Unit, integration, API, docs, or source evidence for every requirement ID. |
| `Implementation Anchors` | Key files a reader or agent should inspect first. |

## Reconstruction Notes Contract

`Reconstruction Notes` must contain these bullets:

- `Similarity target`: the code shape and behavior a model should recreate.
- `Core types/functions`: public structs, interfaces, constructors, handlers,
  commands, methods, registries, or packages that define the feature.
- `Runtime ordering`: the minimum decision sequence needed to reproduce behavior.
- `Non-obvious constraints`: compatibility, security, migration, concurrency,
  resource, or cross-feature constraints that are easy to miss from tests alone.

## Requirement Contract

Requirements are the behavior contract matrix. Each row must describe behavior,
not implementation preference. The strongest row shape is:

| Column | Meaning |
| --- | --- |
| `ID` | Stable requirement ID. |
| `Level` | `MUST`, `SHOULD`, or `MAY`. |
| `Trigger/Input` | The API call, command, event, config, state, or user action that starts behavior. |
| `Required Output` | Observable response, emitted event, return value, file, state, or side effect. |
| `State Mutation` | Durable or in-memory state that must change, remain unchanged, or be initialized. |
| `Failure/Edge` | Error handling, defaults, invalid input, retries, ordering, concurrency, or security behavior. |
| `Rationale` | Why this behavior exists. |

Existing specs may keep a compact `Requirement` column when the behavior is
simple, but new or high-risk requirements should use the expanded matrix
columns. A coding agent should not need to infer state mutation or edge behavior
from prose elsewhere when it belongs in the contract row.

Use these levels:

- `MUST`: behavior required for correctness or compatibility.
- `SHOULD`: expected behavior that can vary only with documented rationale.
- `MAY`: optional behavior that still needs ownership and evidence when present.

## Auxiliary Interface Contract

`Auxiliary Interfaces` is the public surface contract. Rows should be precise
enough to preserve compatibility:

| Column | Meaning |
| --- | --- |
| `Type` | HTTP, CLI, config, event, storage, Go API, file, process, or workflow. |
| `Surface` | Route, command, config path, event kind, package symbol, file path, or workflow. |
| `Contract` | Inputs, outputs, schema, method signatures, side effects, and versioning constraints. |
| `Requirement IDs` | Requirements that define the surface behavior. |

For code-facing features, include public type/function/interface names or exact
method signatures where compatibility depends on them.

## Evidence Contract

Every requirement ID must appear in `Acceptance Evidence`. Evidence can be a
test, source file, API handler, command implementation, or design document, but
test evidence is preferred for externally observable behavior.

Known gaps must be explicit during drafting, but committed feature specs should
not contain `Test gap:` entries unless the lint gate is intentionally bypassed.

## Ownership Contract

`Surface Ownership` is the machine-readable bridge between source code and
feature behavior. Use one `Owns:` line per surface pattern:

```text
Owns: HTTP GET /api/example
Owns: CLI cmd/picoclaw/internal/example/*
Owns: CONFIG.example*
Owns: EVENT example.*
Owns: TEST pkg/example/*
```

Auxiliary interfaces describe the contract in human-readable form. Ownership
lines make the contract auditable by tooling.

## Why This Format

The format keeps the current requirement-table workflow but makes the table
behave like a reconstruction matrix. It adds the missing information a coding
agent needs to recreate code:

- what similar code should look like,
- which types/functions define the behavior,
- what state must exist,
- what order operations happen in,
- what errors and edge cases matter,
- which tests prove the behavior,
- which files and surfaces own the feature.

The result is one format that works for product review, implementation, testing,
and future agent-driven reconstruction.
