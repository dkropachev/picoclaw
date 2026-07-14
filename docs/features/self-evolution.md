# Self-Evolution

## Feature ID

`FR-EVO`

## Behavior Summary

Self-evolution records successful completed turns, clusters repeated patterns,
generates skill drafts, and optionally applies accepted drafts into workspace
skills depending on configured mode.

## Reconstruction Notes

- Similarity target: recreate evolution runtime modes, learning record capture, cold-path clustering, draft generation/review, and guarded skill apply.
- Core types/functions: evolution runtime, store, pattern clusterer, cold path runner, draft generator, draft reviewer, applier, and agent bridge.
- Runtime ordering: observe completed turn, write learning record, run cold path after trigger, cluster successful patterns, generate draft, validate, optionally apply with backup.
- Non-obvious constraints: disabled mode is side-effect free, heartbeat turns are skipped, generated skill content is prompt-sensitive, and rollback is manual from backups.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-EVO-001` | MUST | When disabled, evolution performs no learning capture or draft work. | Disabled mode must be side-effect free. |
| `FR-EVO-002` | MUST | Observe mode records learning data for completed non-heartbeat turns without changing skills. | Users need safe visibility before automation. |
| `FR-EVO-003` | MUST | Draft mode clusters records by repeated successful task patterns and generates candidate skill changes only after thresholds are met. | Drafts need evidence before generation. |
| `FR-EVO-004` | MUST | Apply mode validates generated `SKILL.md` content before writing and backs up replaced skills. | Automatic skill mutation needs guardrails and recovery. |
| `FR-EVO-005` | MUST | Cold path execution supports after-turn and scheduled triggers, with manual mode disabling automatic runs. | Draft timing must follow config. |
| `FR-EVO-006` | SHOULD | Invalid drafts are rejected without creating partial skill directories. | Bad generated content must not pollute workspace. |

## Data And State Model

Evolution state includes learning records, clustered pattern records, candidate
drafts, skill profiles, configured thresholds, cold-path trigger state, and
backup copies for replaced workspace skills.

## Surface Ownership

Owns: CONFIG.evolution*
Owns: TEST pkg/evolution/*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `evolution.*` | Enablement, mode, state directory, thresholds, and cold path trigger. | `FR-EVO-001` through `FR-EVO-005` |
| Storage | Workspace evolution state | Learning records, clusters, drafts, profiles, and backups. | `FR-EVO-002`, `FR-EVO-004` |

## Algorithms And Ordering

1. Gate all behavior on `evolution.enabled` and effective mode.
2. Capture completed non-heartbeat turn summaries and metadata.
3. Run cold path after turn or scheduled time according to config.
4. Cluster records and require threshold success before draft generation.
5. Validate draft content and apply only in apply mode, creating backups first.

## Cross-Feature Behavior

Agent conversations publish turn-end data to evolution. Skills receive applied
drafts. Security guidance treats generated skills as prompt-sensitive material.

## Failure And Edge Cases

- Heartbeat turns are skipped.
- Invalid threshold values fall back or fail validation as configured.
- Draft validation blocks missing headers or suspicious content.
- Backup restore is manual after apply mode changes existing skills.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EVO-001`, `FR-EVO-002`, `FR-EVO-005` | [pkg/evolution/runtime_test.go](../../pkg/evolution/runtime_test.go), [pkg/agent/evolution_bridge_test.go](../../pkg/agent/evolution_bridge_test.go) |
| `FR-EVO-003` | [pkg/evolution/pattern_clusterer_test.go](../../pkg/evolution/pattern_clusterer_test.go), [pkg/evolution/llm_draft_generator_test.go](../../pkg/evolution/llm_draft_generator_test.go) |
| `FR-EVO-004`, `FR-EVO-006` | [pkg/evolution/apply_test.go](../../pkg/evolution/apply_test.go), [pkg/evolution/draft_review_test.go](../../pkg/evolution/draft_review_test.go), [docs/architecture/agent-self-evolution.md](../architecture/agent-self-evolution.md) |

## Implementation Anchors

- [pkg/evolution/runtime.go](../../pkg/evolution/runtime.go)
- [pkg/evolution/apply.go](../../pkg/evolution/apply.go)
- [pkg/agent/evolution_bridge.go](../../pkg/agent/evolution_bridge.go)
