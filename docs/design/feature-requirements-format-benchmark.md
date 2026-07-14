# Feature Requirements Format Benchmark

## Goal

Find the best `docs/features/*.md` format for code regeneration. The acceptance
criterion is practical: an AI coding agent receives a feature spec in that
format and should recreate same or similar feature code.

## Benchmark Design

Target feature: `FR-EVENTS` runtime events.

Why this target:

- contained implementation under `pkg/events`,
- public Go API used by bus, MCP, gateway, and agent code,
- hidden unit tests cover behavior and edge cases,
- cross-feature packages expose compatibility failures.

Redaction per run:

- remove non-test `pkg/events/*.go`,
- hide `pkg/events/*_test.go` and `pkg/config/events_test.go`,
- replace `docs/features/runtime-events.md` with the candidate format,
- remove `docs/architecture/runtime-events.md` to avoid leaking the original
  prose,
- run Codex in the redacted worktree,
- restore hidden tests after generation,
- score generated code.

AI runner:

- command: `codex exec`,
- model: `gpt-5.3-codex-spark`,
- reasoning effort: `medium`,
- per-candidate timeout: 10 minutes for generation,
- scorer: [feature_format_benchmark.go](../../scripts/feature_format_benchmark.go).

## Scoring

Total score is 100.

| Category | Points | Measurement |
| --- | ---: | --- |
| Test pass rate | 35 | Hidden `pkg/events`, config, and cross-feature package tests. |
| Requirement coverage | 20 | Static checks for the five `FR-EVENTS` behavior contracts. |
| API compatibility | 15 | Public types, functions, constants, and method signatures. |
| State correctness | 10 | Bus close state, subscriber registry, counters, event IDs, and defaults. |
| Failure semantics | 10 | Nil handler, backpressure, panic, timeout, nil/closed behavior. |
| Cross-feature behavior | 5 | `pkg/bus`, `pkg/mcp`, and `pkg/gateway` compatibility tests. |
| Maintainability | 5 | `gofmt`, scoped changes, and reasonable file count. |

Tie-breakers:

1. Lower agent token usage among perfect-scoring formats.
2. Less test overfitting.
3. Less file-layout prescription.
4. Better long-term source-of-truth readability.

## Candidate Iterations

Each candidate had three improvement rounds before the final v4 spec was tested.

| Candidate | Iteration 1 | Iteration 2 | Iteration 3 |
| --- | --- | --- | --- |
| `user-story-acceptance-v4` | Actor stories and broad acceptance criteria. | Add explicit public API names. | Add state, error, and hidden-test reconstruction hints. |
| `api-first-contract-v4` | Public symbol inventory. | Add behavior expectations beside symbols. | Add state counters, error semantics, and event-kind completeness. |
| `bdd-scenarios-v4` | Given/when/then scenarios. | Add scenarios for backpressure, close, once, panic, timeout. | Add API appendix for symbols and constants. |
| `contract-matrix-v4` | Requirement rows. | Add input/state/output/failure columns. | Add implementation-signature appendix. |
| `domain-model-v4` | Entities and relationships. | Add invariants for counters, close, queues, copies. | Map entities to public Go symbols. |
| `test-first-contract-v4` | Test classes and visible packages. | Add expected assertions for hidden test families. | Add API and state notes to avoid test stubbing. |
| `state-machine-v4` | Lifecycle states. | Add transition guards for close, publish, queue full, context cancel. | Add public API and constants. |
| `agent-prompt-pack-v4` | Direct implementation prompt. | Add no-edit boundaries and compatibility commands. | Add API, state, and edge-case checklists. |
| `implementation-blueprint-v4` | Package/file layout. | Add exact type signatures and algorithm order. | Add edge-case and cross-package constraints. |
| `behavioral-reconstruction-contract-v4` | Current hybrid requirements format. | Add exact public surface and behavior matrix columns. | Add reconstruction target, state invariants, algorithm order, and evidence hooks. |

## Results

| Rank | Candidate | Total | Tests | Req | API | State | Failures | Cross | Maint | Notes |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| 1 | `contract-matrix-v4` | 100 | 35 | 20 | 15 | 10 | 10 | 5 | 5 | Perfect score; lowest token use among perfect formats. |
| 2 | `test-first-contract-v4` | 100 | 35 | 20 | 15 | 10 | 10 | 5 | 5 | Perfect score, but more likely to overfit tests than source-of-truth behavior. |
| 3 | `implementation-blueprint-v4` | 100 | 35 | 20 | 15 | 10 | 10 | 5 | 5 | Perfect score, but more prescriptive about file layout than feature behavior. |
| 4 | `behavioral-reconstruction-contract-v4` | 100 | 35 | 20 | 15 | 10 | 10 | 5 | 5 | Perfect score, but higher token use than matrix format. |
| 5 | `api-first-contract-v4` | 76 | 15 | 20 | 15 | 6 | 10 | 5 | 5 | Hidden event tests failed; missed subscriber storage/defaults. |
| 6 | `bdd-scenarios-v4` | 76 | 15 | 20 | 15 | 6 | 10 | 5 | 5 | Hidden event tests failed; missed subscriber storage/defaults. |
| 7 | `state-machine-v4` | 76 | 15 | 20 | 15 | 6 | 10 | 5 | 5 | Hidden event tests failed; missed subscriber storage/defaults. |
| 8 | `agent-prompt-pack-v4` | 76 | 15 | 20 | 15 | 6 | 10 | 5 | 5 | Hidden event tests failed; missed subscriber storage/defaults. |
| 9 | `user-story-acceptance-v4` | 76 | 15 | 20 | 15 | 8 | 10 | 5 | 3 | Hidden event tests failed; gofmt finding. |
| 10 | `domain-model-v4` | 59 | 5 | 20 | 15 | 4 | 10 | 0 | 5 | Hidden event tests failed; cross-feature gateway test hung. |

Token use for perfect-scoring candidates:

| Candidate | Codex tokens |
| --- | ---: |
| `contract-matrix-v4` | 101,227 |
| `test-first-contract-v4` | 133,796 |
| `implementation-blueprint-v4` | 133,924 |
| `behavioral-reconstruction-contract-v4` | 135,504 |

## Decision

Select `contract-matrix-v4` as the best format and adapt it as the
**Reconstruction Contract Matrix**.

The winning format is not just a dense table. The table is the core contract,
and the spec still needs reconstruction notes, public surface details, state
model, algorithms, failure cases, evidence, implementation anchors, and surface
ownership. The key finding is that a matrix-first format gives the agent the
required behavior, state, output, and edge-case information in the most efficient
shape.
