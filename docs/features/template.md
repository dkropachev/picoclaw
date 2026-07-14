# Feature Name

## Feature ID

`FR-EXAMPLE`

## Behavior Summary

Describe the user-facing capability and its boundary.

## Reconstruction Notes

- Similarity target: state what code shape a model should recreate.
- Core types/functions: list public structs, interfaces, constructors, methods, handlers, or commands that define the behavior.
- Runtime ordering: list the minimum decision sequence needed to reproduce behavior.
- Non-obvious constraints: list compatibility, security, resource, and migration constraints.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-EXAMPLE-001` | MUST | Describe the API call, command, event, config, state, or user action. | State observable response, event, file, return value, or side effect. | State what changes, persists, initializes, or must remain unchanged. | Describe defaults, invalid input, retries, ordering, concurrency, or security behavior. | Explain why the behavior exists. |

## Data And State Model

Describe durable files, config fields, in-memory state, keys, IDs, schemas,
normalization rules, and ownership boundaries.

## Surface Ownership

Owns: HTTP GET /example
Owns: CLI cmd/example/*
Owns: CONFIG.example*
Owns: TEST pkg/example/*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `GET /example` | Describe request/response behavior. | `FR-EXAMPLE-001` |

## Algorithms And Ordering

Describe ordered behavior in enough detail for code reconstruction: validation,
normalization, lookup, mutation, side effects, fallbacks, and emitted events.

## Cross-Feature Behavior

Describe interactions with other specs.

## Failure And Edge Cases

Describe non-happy-path behavior and defaults.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EXAMPLE-001` | Link source and test files. |

## Implementation Anchors

- Link code files that implement the behavior.
