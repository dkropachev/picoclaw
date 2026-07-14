# Feature Requirements Format Evaluation

## Goal

Feature requirements must be detailed enough that a coding agent can recreate
same or similar code from the docs alone. The canonical format should preserve
the repo's current source-of-truth workflow while adding the state, ordering,
interfaces, tests, and implementation anchors needed for code regeneration.

## Method

`make evaluate-feature-formats` runs a deterministic proxy benchmark implemented
in [scripts/evaluate-feature-formats.go](../../scripts/evaluate-feature-formats.go).
The scorer checks each candidate format for code-regeneration signals and ranks
the formats by weighted coverage.

This is a proxy, not a live agent benchmark. A later benchmark can give agents a
redacted feature spec, ask for code reconstruction, and compare the resulting
tests, public APIs, file ownership, and behavior against the current repository.

## Metrics

| Metric | Weight |
| --- | ---: |
| Behavior contracts | 18 |
| Interface precision | 14 |
| State and data model | 14 |
| Algorithm and ordering | 14 |
| Failure semantics | 10 |
| Test mapping | 10 |
| Implementation anchors | 8 |
| Cross-feature behavior | 6 |
| Machine ownership | 4 |
| Prompt efficiency | 2 |

## Candidate Formats

| Format | Description |
| --- | --- |
| `current-requirements-table` | Current broad requirement table with evidence and surface ownership. |
| `user-story-acceptance` | Product-management format optimized for intent over code shape. |
| `api-first-contract` | Endpoint, command, schema, error, example, and test focused. |
| `behavior-driven-scenarios` | Given/when/then examples and edge scenarios. |
| `code-reconstruction-blueprint` | Direct coding-agent blueprint with types, state, ordering, tests, and anchors. |
| `contract-matrix` | Dense behavior matrix tracking inputs, outputs, state changes, failures, and evidence. |
| `test-first-spec` | Unit and integration test contracts with fixtures and regression risks. |
| `domain-model-spec` | Entities, state transitions, invariants, interfaces, failures, and tests. |
| `agent-prompt-pack` | Direct coding prompt with must-implement, exclusions, examples, tests, and files. |
| `hybrid-reconstruction-contract` | Current feature docs plus regeneration-specific sections. |

## Initial Ranking

| Rank | Format | Score | Notes |
| --- | --- | ---: | --- |
| 1 | `hybrid-reconstruction-contract` | 100 | Keeps current docs compatible while adding code-regeneration slots. |
| 2 | `code-reconstruction-blueprint` | 76 | Optimized for a coding agent recreating current implementation. |
| 3 | `contract-matrix` | 76 | Dense, machine-checkable behavior table with strong traceability. |
| 4 | `current-requirements-table` | 72 | Current broad requirement table with evidence and surface ownership. |
| 5 | `behavior-driven-scenarios` | 60 | Strong at observable examples, weak at data structures and implementation anchors. |
| 6 | `api-first-contract` | 56 | Good for HTTP/CLI surfaces, weak for internal runtime behavior. |
| 7 | `domain-model-spec` | 48 | Good for state-heavy features, less direct for tools and channels. |
| 8 | `user-story-acceptance` | 42 | Product-management format optimized for intent over code shape. |
| 9 | `test-first-spec` | 36 | Good for proving behavior, weaker for initial implementation design. |
| 10 | `agent-prompt-pack` | 36 | Direct coding prompt, but less stable as long-term documentation. |

## Top 3 Iteration

The top three formats were refined to close their code-regeneration gaps:

| Format | Change | Score |
| --- | --- | ---: |
| `hybrid-reconstruction-contract-v2` | Adds explicit similarity target and treats requirements as a contract matrix while preserving current docs and linter shape. | 100 |
| `code-reconstruction-blueprint-v2` | Adds missing cross-feature and ownership slots to the blueprint. | 100 |
| `contract-matrix-v2` | Adds implementation anchors, algorithm ordering, and data model to the dense matrix. | 100 |

## Decision

Use `hybrid-reconstruction-contract-v2` as the canonical repo format.

It reaches full metric coverage and has the lowest migration cost because it
extends the existing feature requirements instead of replacing them. The current
feature docs now require:

- `Reconstruction Notes` with similarity target, core types/functions, runtime
  ordering, and non-obvious constraints.
- `Data And State Model` for durable files, config, in-memory state, schemas,
  IDs, and ownership boundaries.
- `Algorithms And Ordering` for validation, normalization, lookup, mutation,
  side effects, fallbacks, and emitted events.
- Existing `Requirements`, `Auxiliary Interfaces`, `Cross-Feature Behavior`,
  `Failure And Edge Cases`, `Acceptance Evidence`, `Implementation Anchors`, and
  `Surface Ownership`.

## Acceptance Mapping

The selected format gives a coding agent the minimum reconstruction context:

| Need | Canonical Section |
| --- | --- |
| Same or similar code target | `Reconstruction Notes` |
| Public APIs and commands | `Auxiliary Interfaces` |
| State, config, and persistence | `Data And State Model` |
| Core sequence and algorithms | `Algorithms And Ordering` |
| Error and edge behavior | `Failure And Edge Cases` |
| Cross-feature interactions | `Cross-Feature Behavior` |
| Tests to reproduce behavior | `Acceptance Evidence` |
| Files and ownership boundaries | `Implementation Anchors`, `Surface Ownership` |
