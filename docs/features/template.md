# Feature Name

## Feature ID

`FR-EXAMPLE`

## Behavior Summary

Describe the user-facing capability and its boundary.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-EXAMPLE-001` | MUST | State observable behavior with inputs, outputs, persistence, and errors where relevant. | Explain why the behavior exists. |

## Auxiliary Interfaces

Owns: HTTP GET /example
Owns: CLI cmd/example/*
Owns: CONFIG.example*
Owns: TEST pkg/example/*

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `GET /example` | Describe request/response behavior. | `FR-EXAMPLE-001` |

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
