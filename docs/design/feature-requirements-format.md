# Feature Requirements Format

## Decision

Use one canonical feature requirements format: a reconstruction-oriented
behavior contract.

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
| `Requirements` | Observable behavior contracts with IDs, level, and rationale. |
| `Data And State Model` | Durable files, config, in-memory state, keys, IDs, schemas, and ownership boundaries. |
| `Surface Ownership` | Machine-readable `Owns:` mappings for APIs, config, tests, events, and code surfaces. |
| `Auxiliary Interfaces` | HTTP, CLI, config, event, storage, and runtime interfaces that expose the feature. |
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

Requirements are the behavior contract matrix. Each row must describe observable
behavior, not implementation preference. Good requirements include at least one
of input, output, state mutation, persistence, ordering, default behavior, or
error behavior.

Use these levels:

- `MUST`: behavior required for correctness or compatibility.
- `SHOULD`: expected behavior that can vary only with documented rationale.
- `MAY`: optional behavior that still needs ownership and evidence when present.

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

The format keeps the current requirement-table workflow but adds the missing
information a coding agent needs to recreate code:

- what similar code should look like,
- which types/functions define the behavior,
- what state must exist,
- what order operations happen in,
- what errors and edge cases matter,
- which tests prove the behavior,
- which files and surfaces own the feature.

The result is one format that works for product review, implementation, testing,
and future agent-driven reconstruction.
