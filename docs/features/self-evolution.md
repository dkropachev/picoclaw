# Self-Evolution

## Feature ID

`FR-EVO`

## Behavior Summary

Self-evolution records successful completed turns, clusters repeated patterns,
generates skill drafts, and optionally applies accepted drafts into workspace
skills depending on configured mode. Its optional model-backed cold path uses
the same explicit account-and-alias selection as the default agent and never a
provider-supplied default model.

## Reconstruction Notes

- Similarity target: recreate evolution runtime modes, learning record capture, cold-path clustering, draft generation/review, and guarded skill apply.
- Core types/functions: evolution runtime, typed evolution broker client and handler, store, pattern clusterer, cold path runner, draft generator, draft reviewer, applier, and agent bridge.
- Runtime ordering: resolve the configured workspace to an opaque broker `StoreID`, observe completed turn, write the learning record through a typed broker operation, run cold path after trigger, cluster successful patterns, generate draft, validate, optionally apply with backup.
- Non-obvious constraints: disabled mode is side-effect free, heartbeat turns
  are skipped, generated skill content is prompt-sensitive, rollback is manual
  from backups, and a missing resolved model uses the deterministic evolution
  fallback without invoking a provider. Mutable state is normalized in
  `evolution.db`; legacy JSON/JSONL is imported once and archived without dual
  writes.
- The supervisor broker retains the only evolution provider pool; hot and cold
  runtime paths reconnect to it and never derive or open a local database.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-EVO-001` | MUST | When disabled, evolution performs no learning capture or draft work. | Disabled mode must be side-effect free. |
| `FR-EVO-002` | MUST | Observe mode records learning data for completed non-heartbeat turns without changing skills. | Users need safe visibility before automation. |
| `FR-EVO-003` | MUST | Draft mode clusters records by repeated successful task patterns and generates candidate skill changes only after thresholds are met. | Drafts need evidence before generation. |
| `FR-EVO-004` | MUST | Apply mode validates generated `SKILL.md` content before writing and backs up replaced skills. | Automatic skill mutation needs guardrails and recovery. |
| `FR-EVO-005` | MUST | Cold path execution supports after-turn and scheduled triggers, with manual mode disabling automatic runs. | Draft timing must follow config. |
| `FR-EVO-006` | SHOULD | Invalid drafts are rejected without creating partial skill directories. | Bad generated content must not pollute workspace. |
| `FR-EVO-007` | MUST | Model-backed success judging, pattern clustering, and draft generation use the default agent's runtime-resolved candidate: account routing chooses a concrete account, the configured exact alias or model-router result resolves for that account, and its per-account override selects the concrete upstream model. Evolution invokes only that candidate's provider and model; it never reads a provider default or treats an alias as an upstream model ID. If no runnable explicit target exists, the component takes its configured deterministic fallback without a provider call, while normal agent startup still reports `no model configured` for an absent required selection. | Background learning must use the same reviewed model policy as foreground turns and must not silently select a different upstream default. |
| `FR-EVO-008` | MUST | The configured evolution state directory owns one private `evolution.db` with normalized ordered evidence, drafts, profiles, and version history. First open transactionally imports bounded legacy record, draft, and profile files, records digest-only skip audits, archives committed sources, and performs no JSON/JSONL dual writes. | Concurrent hot/cold paths need one durable authority and safe automatic upgrade. |
| `FR-EVO-009` | MUST | Evolution runtime uses typed broker operations and a trusted-catalog-resolved opaque `StoreID`; it cannot submit a path or obtain a provider handle. The supervisor broker owns one retained pool per canonical evolution store across runtime restarts. Broker loss fails the operation with a structured error and never activates local fallback. Missing empty stores may initialize online, but schema upgrades and legacy imports require `picoclaw database migrate` after exclusive shutdown fencing and a successful mandatory generation backup. | Learning evidence and draft history must have one owner and must not silently fork when the broker or schema is unavailable. |

`FR-EVO-008` storage MUST durably close the shared legacy import horizon after
the first complete evolution-source enumeration, including an empty result.
Later record, draft, or profile sources are safely audited and archived, but
MUST NOT be domain-imported or finalized; `evolution.db` remains authoritative.

## Data And State Model

`<state-dir>/evolution.db` includes typed learning records, normalized ordered
evidence, clustered pattern records, candidate drafts, skill profiles, and
profile version history. Backup copies for replaced workspace skills remain
recovery files. Model-backed work holds only the
resolved candidate provider and concrete model for the active agent generation;
the durable records do not turn raw provider model IDs into selection policy.
The physical database name is provider-private: runtime state identifies the
store only by its opaque catalogued `StoreID`, and the broker retains its pool.

## Surface Ownership

Owns: CODE pkg/agent/evolution_bridge*
Owns: CODE pkg/evolution/**
Owns: CONFIG.evolution*
Owns: TEST pkg/evolution/*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `evolution.*` | Enablement, mode, state directory, thresholds, and cold path trigger. | `FR-EVO-001` through `FR-EVO-005` |
| Runtime | Default agent account, model alias, and candidate registry | Resolve the same concrete account/provider/model candidate used by foreground execution, including router selection and per-account alias overrides. | `FR-EVO-007` |
| Storage | `<state-dir>/evolution.db` | Typed learning records, clusters, drafts, profiles, ordered relationships, transactional migration, and retained legacy archives. | `FR-EVO-002`, `FR-EVO-004`, `FR-EVO-008` |
| Broker | typed evolution operations and opaque `StoreID` | Own the retained provider pool, reject uncatalogued targets, and reserve upgrades/imports for backed-up exclusive offline migration. | `FR-EVO-009` |

## Algorithms And Ordering

1. Gate all behavior on `evolution.enabled` and effective mode.
2. Capture completed non-heartbeat turn summaries and metadata.
3. Run cold path after turn or scheduled time according to config.
4. Cluster records and require threshold success before draft generation.
5. When a judge, clusterer, or draft generator can use a model, select the
   default agent's explicit account-and-alias candidate through the normal
   router path and pass its resolved concrete model to that candidate's
   provider. If no such target exists, run the non-model fallback without a
   provider call.
6. Validate draft content and apply only in apply mode, creating backups first.
7. Treat broker `Unavailable`, `MigrationRequired`, and integrity failures as
   terminal for that durable operation; do not open a local store or import
   legacy state from a runtime process.

## Cross-Feature Behavior

Agent conversations publish turn-end data to evolution. Skills receive applied
drafts. Agent routing supplies the resolved account-and-alias candidate for
model-backed cold-path work. Security guidance treats generated skills as
prompt-sensitive material.

## Failure And Edge Cases

- Heartbeat turns are skipped.
- Invalid threshold values fall back or fail validation as configured.
- A blank or unrunnable model selection never reaches `Chat`; evolution uses
  its deterministic fallback, and provider default-model behavior is ignored.
- Draft validation blocks missing headers or suspicious content.
- Backup restore is manual after apply mode changes existing skills.
- Broker discovery/readiness failure cannot trigger a caller-owned pool or
  file-backed fallback. An outdated or legacy evolution generation remains
  unchanged until the exclusive offline migrator completes its mandatory
  backup and migration.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EVO-001`, `FR-EVO-002`, `FR-EVO-005` | [pkg/evolution/runtime_test.go](../../pkg/evolution/runtime_test.go), [pkg/agent/evolution_bridge_test.go](../../pkg/agent/evolution_bridge_test.go) |
| `FR-EVO-003` | [pkg/evolution/pattern_clusterer_test.go](../../pkg/evolution/pattern_clusterer_test.go), [pkg/evolution/llm_draft_generator_test.go](../../pkg/evolution/llm_draft_generator_test.go) |
| `FR-EVO-004`, `FR-EVO-006` | [pkg/evolution/apply_test.go](../../pkg/evolution/apply_test.go), [pkg/evolution/draft_review_test.go](../../pkg/evolution/draft_review_test.go), [docs/architecture/agent-self-evolution.md](../architecture/agent-self-evolution.md) |
| `FR-EVO-007` | [pkg/agent/evolution_bridge_test.go](../../pkg/agent/evolution_bridge_test.go), [pkg/evolution/llm_draft_generator_test.go](../../pkg/evolution/llm_draft_generator_test.go), [pkg/evolution/pattern_clusterer_test.go](../../pkg/evolution/pattern_clusterer_test.go) |
| `FR-EVO-008` | [pkg/evolution/sqlite_store_test.go](../../pkg/evolution/sqlite_store_test.go), [pkg/agent/file_mutation_policy_test.go](../../pkg/agent/file_mutation_policy_test.go) |
| `FR-EVO-009` | [pkg/evolution/broker_test.go](../../pkg/evolution/broker_test.go), [pkg/evolution/provider_access_test.go](../../pkg/evolution/provider_access_test.go), [pkg/database/migration/offline_adapter_fence_test.go](../../pkg/database/migration/offline_adapter_fence_test.go), [pkg/database/migration/migration_test.go](../../pkg/database/migration/migration_test.go) |

## Implementation Anchors

- [pkg/evolution/runtime.go](../../pkg/evolution/runtime.go)
- [pkg/evolution/broker.go](../../pkg/evolution/broker.go)
- [pkg/evolution/apply.go](../../pkg/evolution/apply.go)
- [pkg/agent/evolution_bridge.go](../../pkg/agent/evolution_bridge.go)
