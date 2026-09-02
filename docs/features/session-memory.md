# Session Memory And History

## Feature ID

`FR-SESSION`

## Behavior Summary

PicoClaw allocates conversation scope, resolves canonical and legacy aliases,
and persists conversation state through the opaque workspace `sessions` broker
store. The same logical store owns ordered messages, summaries, scope
dimensions, aliases, thread relationships, and strict snapshot revisions.
Launcher history HTTP shapes are unchanged. Legacy JSON/JSONL is imported and
archived only by offline database migration; runtime never falls back,
dual-writes, or opens the physical store.

Media freezing and optional Seahorse indexes remain derived capabilities. They
do not replace canonical session storage.

## Reconstruction Notes

- Similarity target: recreate scoped allocation, typed broker persistence,
  alias promotion, snapshot CAS, launcher history, offline legacy import/archive,
  frozen media, and isolated Seahorse context engines.
- Core types/functions: `SessionScope`, `SessionSnapshot`, `SnapshotReader`,
  `SessionSnapshotReplacement`, `SnapshotReplacer`, `ScopeAdmitter`,
  session domain store/client, session backend constructor,
  route allocation helpers, session HTTP handlers, media freeze/materialize,
  and Seahorse engine factories.
- Runtime ordering: normalize route; allocate scope/key/aliases; require broker
  readiness; admit ownership; execute typed metadata/message commands; resolve
  strict snapshots/CAS; expose HTTP projections; build derived indexes only
  from strict evidence.
- Non-obvious constraints: schema and relational validation fail closed;
  aliases can preserve established ambiguity but exclusive review ownership
  cannot; imported bad records are audited without payloads; unsafe migration
  inputs abort; database and archives are protected from model file tools;
  exact nested payloads are canonical JSON BLOBs, not document-store rows.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SESSION-001` | MUST | Route policy and normalized inbound context allocate scope from supported `space`, `chat`, `topic`, and `sender` dimensions. | Isolation must be predictable. |
| `FR-SESSION-002` | MUST | Canonical keys include normalized routed agent and ordered dimension identity; current keys use `sk_v1_<sha256>`. | Agents/channels must not collide. |
| `FR-SESSION-003` | MUST | Legacy aliases remain readable. Eligible nonempty legacy history may promote into an empty canonical owner in one transaction and never overwrite nonempty canonical history. | Upgrades preserve continuity. |
| `FR-SESSION-004` | MUST | The logical `sessions` store retains typed session/message/scope/alias records with contiguous ordering, seconds+nanos timestamps, row/byte limits, and canonical nested JSON. Appends and replacements are single broker-side domain commands; the provider supplies foreign keys, FULL synchronous durability, private modes, and its journal policy without exposing them to session callers. No supported runtime writes session JSON/JSONL. | Durable history needs one validated authority without provider coupling. |
| `FR-SESSION-005` | MUST | Summary, truncation, and full-history replacement retain future-turn context while respecting context thresholds; typed compaction compatibility calls do not create alternate stores or files. | Long sessions need bounded context. |
| `FR-SESSION-006` | MUST | Launcher list/detail/delete preserve HTTP formats, resolve the current canonical Pico owner, reject structured non-Pico scope, revalidate after lookup, and delete matching owners transactionally. Storage or migration errors return failure rather than reading/deleting legacy files directly. | Management must not expose or erase a rebound owner. |
| `FR-SESSION-007` | SHOULD | Trusted explicit opaque or legacy session keys are preserved when compatible. | Direct and compatibility flows need determinism. |
| `FR-SESSION-008` | MUST | Pico selection accepts transient `account_ref` plus alias-valued `model_name`; stored messages retain alias-valued `model_name` but never persist account routing or resolved upstream model identity. Frontend projections tolerate optional account metadata. | Selection and durable identity have different lifetimes. |
| `FR-SESSION-009` | MUST | `SnapshotReader.ReadSessionSnapshot` returns a deep-cloned coherent existing canonical key/history/summary/scope/alias tuple. Blank/unknown keys do not create state. Strict ambiguity, cancellation, invalid schema/data, or decode failures are returned. | Automated decisions require exact immutable evidence. |
| `FR-SESSION-010` | MUST | `SnapshotReplacer.ReplaceSessionSnapshot` validates exact key/scope/alias/message binding and atomically CAS-replaces the tuple using the transient revision. Empty expected revision requires absence; stale revisions return `ErrSnapshotConflict`. No caller may emulate replacement with separate setters. | Replacement must not tear or overwrite concurrent work. |
| `FR-SESSION-011` | MUST | Frozen-session media validates and bounds every admitted locator, atomically captures detached bytes into a strict versioned set, and later materializes only integrity-bound embedded data without live-store access. Hidden, malformed, local-path, or network capabilities fail the whole capture. | Delayed isolated decisions need exact safe media. |
| `FR-SESSION-012` | MUST | `ScopeAdmitter` arbitrates live versus protected review ownership inside the same broker-side atomic command as scope/alias mutation. Review reservation requires exact canonical v1 absence and exclusive aliases; conflicts/cancellation return before transcript use or provider invocation. Multiple clients rely on the one broker serialization boundary. | Check-then-write races must not overwrite review state. |
| `FR-SESSION-013` | MUST | Supported Seahorse generations use one distinct opaque catalog `StoreID` per agent, bootstrap only from strict snapshots, rely on broker catalog rejection of physical aliases, publish tools only after complete startup, and release exact typed generation clients on failure/reload without closing broker pools. | Derived context must not leak across agents/generations. |
| `FR-SESSION-014` | MUST | The workspace's last external channel, last chat ID, and shared update timestamp persist as a typed singleton in the logical `runtime-state` store. Field-specific broker-side atomic commands preserve independent updates across managers/clients. Existing `state/state.json` or `state.json` makes startup report `MigrationRequired`; only offline database migration imports the bounded preferred source, records safe code/digest audits, archives exact inputs under `state/legacy-json/runtime-state-v1/`, retries pending archives without re-import, refuses changed committed sources, and never dual-writes JSON. | Delivery continuity must survive restart without stale whole-state updates or unsafe agent-editable authority. |

Under `FR-SESSION-004` and `FR-SESSION-014`, each session or runtime database
MUST durably close its shared import horizon after the first complete legacy
enumeration, including an empty result. Later session JSON/JSONL, metadata,
thread/handoff, delete-manifest, or runtime-state sources are safely audited
and archived under their exact component archive, but MUST NOT be imported,
finalized, or applied as deletions; `sessions.db` and `runtime.db` remain
authoritative.

## Data And State Model

Authoritative state lives in the logical `sessions` store:

- `sessions`: key, summary, created/updated seconds+nanos, version;
- `session_messages`: ordered typed fields plus canonical nested JSON BLOB;
- `session_scopes` and `session_scope_dimensions`: typed scope and ordered values;
- `session_aliases`: ordered logical aliases;
- thread/link/handoff tables shared with `FR-THREADS`;
- `storage_imports` and `storage_import_issues`: source identity/digest/counts,
  archive status, safe issue code, and record digest.

`SessionMeta` remains a compatibility projection. `Skip` and `HistorySlot` are
always zero for broker-backed rows. `SessionSnapshot.Revision` is derived and never
stored. Legacy sources are retained below
`<workspace>/legacy-json/sessions-v1/` with original relative layout and mode.
Frozen sets and per-agent Seahorse stores remain separate derived state.
The independent `runtime-state` singleton contains only last channel, last chat ID,
timestamp seconds/nanoseconds, source priority, and row version.
The shared `pkg/internal/sessiondb` transaction helper is broker-side/internal
only: it operates on the broker-owned retained pool and is not an
application-facing constructor, SQL callback, or alternate storage owner.

## Surface Ownership

Owns: CODE pkg/agent/memory/**
Owns: CODE pkg/agent/sessions/**
Owns: CODE pkg/agent/state/**
Owns: CODE pkg/identity/**
Owns: CODE pkg/internal/sessiondb/**
Owns: CODE pkg/memory/**
Owns: CODE pkg/media/frozen.go
Owns: CODE pkg/seahorse/**
Owns: CODE pkg/session/**
Owns: CODE pkg/state/**
Owns: CODE web/backend/api/session.go
Owns: CODE web/frontend/src/api/sessions.ts
Owns: CODE web/frontend/src/components/logs/**
Owns: CODE web/frontend/src/hooks/use-session-history.ts
Owns: CODE web/frontend/src/routes/logs.tsx
Owns: CONFIG.session*
Owns: HTTP * /api/sessions*
Owns: TEST pkg/session/*
Owns: TEST pkg/memory/*
Owns: TEST pkg/media/frozen_test.go
Owns: TEST pkg/identity/*
Owns: TEST pkg/state/*
Owns: TEST web/backend/api/session*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `session.dimensions`, `session.identity_links`, legacy `dm_scope` | Scope isolation and compatibility input. | `FR-SESSION-001`, `FR-SESSION-003` |
| HTTP | `GET/DELETE /api/sessions*` | Unchanged launcher list/detail/delete JSON over typed broker authority. | `FR-SESSION-006` |
| Broker store | Logical workspace store `sessions` | Typed relational session/thread state and strict domain schema; physical provider details remain broker-private. | `FR-SESSION-004`, `FR-SESSION-010`, `FR-SESSION-012` |
| Archive | `<workspace>/legacy-json/sessions-v1/**` | Indefinite, permission-preserving, no-overwrite legacy retention. | `FR-SESSION-003`, `FR-SESSION-004` |
| Go API | Session memory store and backend constructors | Preserve typed session behavior while constructing a broker client; expose no SQL handle, callback, path, DSN, or provider error. | `FR-SESSION-004` |
| Go API | `SnapshotReader`, `SnapshotReplacer`, `ScopeAdmitter` | Strict coherent read, CAS replacement, and ownership admission. | `FR-SESSION-009`, `FR-SESSION-010`, `FR-SESSION-012` |
| Go API | `media.FreezeInputs`, `FreezeSessionSnapshotMedia`, materializers | Bounded all-or-nothing frozen media. | `FR-SESSION-011` |
| Broker store | Per-agent Seahorse `StoreID` | Isolated derived context generation resolved by the trusted catalog. | `FR-SESSION-013` |
| Broker store/runtime | Logical workspace store `runtime-state`, `state.Manager` | Atomically retain the most recent external delivery context; import/archive bounded legacy state only during offline migration. | `FR-SESSION-014` |

## Algorithms And Ordering

1. Resolve the workspace's opaque session `StoreID` and require broker readiness
   before constructing model-facing file tools.
2. Reject migration-required, too-new, corrupt, unavailable, or invalid
   relationship/sequence state without reading a legacy fallback.
3. During fenced offline migration only, read bounded legacy sources in
   deterministic relative order and resolve sessions before threads and handoffs.
4. Normalize/validate import records. Skip selected bad records with code+digest;
   abort unsafe filesystem, size, enumeration, provider, or integrity failures.
5. Resolve first-valid identity conflicts and dependencies, insert ordered rows,
   then atomically replace provisional import counts with final outcomes.
6. Commit schema and import authority, then archive every examined source
   without replacement; retry pending archives without re-import and refuse
   changed digests.
7. For live turns, admit scope/aliases through one typed command, resolve canonical key,
   then append messages and increment version.
8. For strict reads/CAS, compute and fence the complete tuple revision inside
   one broker-side atomic boundary.
9. HTTP list/detail/delete revalidate the projected Pico identity after lookup.
10. Runtime delivery-state setters update only their typed field and shared
    timestamp under one typed atomic command and row-version fence.

## Cross-Feature Behavior

Routing owns scope inputs. Agent conversations own turn sequencing and context
budget. Threads share the logical store and atomic command boundary but own thread
semantics. The Database Layer owns clients/readiness/offline migration and
SQLite storage owns provider hardening. Security isolation protects
provider-catalog artifacts and archives from file tools. Workflows consume
strict snapshots and frozen media. Seahorse is derived, never canonical.

## Failure And Edge Cases

- Too-new version, unknown schema object, failed integrity/FK check, invalid
  canonical nested JSON, sequence gap, mismatched thread primary, or orphan
  link rejects reopen.
- Blank keys, oversized text/payloads, invalid timestamps, alias conflicts, and
  stale revisions fail without partial mutation.
- Malformed aggregate JSON, JSONL lines, invalid identities, duplicates, and
  broken references follow selected safe-skip rules and emit no payload/secret.
- Unsafe/symlinked/writable legacy roots, oversized sources, enumeration error,
  or archive digest drift abort or defer archival safely.
- Mixed old/new processes are unsupported. Rollback requires explicit broker
  shutdown and provider-owned restore of the matching backed-up generation.
- Frozen media and Seahorse retain their independent bounds and fail-closed
  generation rules.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SESSION-001`, `FR-SESSION-002`, `FR-SESSION-003`, `FR-SESSION-007` | [pkg/session/allocator_test.go](../../pkg/session/allocator_test.go), [pkg/session/key_test.go](../../pkg/session/key_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go) |
| `FR-SESSION-004`, `FR-SESSION-005` | [pkg/memory/sqlite_store_test.go](../../pkg/memory/sqlite_store_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/agent/context_budget_test.go](../../pkg/agent/context_budget_test.go) |
| `FR-SESSION-006` | [web/backend/api/session_test.go](../../web/backend/api/session_test.go) |
| `FR-SESSION-008` | [pkg/channels/pico/pico_test.go](../../pkg/channels/pico/pico_test.go), [web/frontend/src/features/chat/history.ts](../../web/frontend/src/features/chat/history.ts) |
| `FR-SESSION-009`, `FR-SESSION-010`, `FR-SESSION-012` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/memory/sqlite_store_test.go](../../pkg/memory/sqlite_store_test.go), [pkg/agent/agent_message_review_test.go](../../pkg/agent/agent_message_review_test.go) |
| `FR-SESSION-011` | [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go) |
| `FR-SESSION-013` | [pkg/agent/context_seahorse_test.go](../../pkg/agent/context_seahorse_test.go), [pkg/seahorse/tool_factory_test.go](../../pkg/seahorse/tool_factory_test.go) |
| `FR-SESSION-014` | [pkg/state/state_test.go](../../pkg/state/state_test.go), [pkg/state/fresh_workspace_test.go](../../pkg/state/fresh_workspace_test.go) |

## Implementation Anchors

- [pkg/memory/sqlite_store.go](../../pkg/memory/sqlite_store.go)
- [pkg/memory/sqlite_schema.go](../../pkg/memory/sqlite_schema.go)
- [pkg/memory/sqlite_migration.go](../../pkg/memory/sqlite_migration.go)
- [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go)
- [pkg/session/session_store.go](../../pkg/session/session_store.go)
- [pkg/session/frozen_media.go](../../pkg/session/frozen_media.go)
- [pkg/media/frozen.go](../../pkg/media/frozen.go)
- [pkg/agent/context_seahorse.go](../../pkg/agent/context_seahorse.go)
- [pkg/state/state.go](../../pkg/state/state.go)
- [pkg/state/state_sqlite.go](../../pkg/state/state_sqlite.go)
- [web/backend/api/session.go](../../web/backend/api/session.go)
