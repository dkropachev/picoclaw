# Session Memory And History

## Feature ID

`FR-SESSION`

## Behavior Summary

PicoClaw persists conversation state by routed session scope. Session behavior
defines how chat/user/topic dimensions become keys, how JSONL history is stored,
how legacy session aliases are promoted, how alias-valued model metadata is
stored and exposed, and how launcher history views accept optional runtime
account-selection metadata. A separate strict snapshot capability lets
workflow decisions inspect one already-existing session without creating it,
silently dropping corrupt context, or observing history and summary from
different points in time. An optional compare-and-swap replacement capability
publishes a complete new history, summary, scope, and alias tuple through one
metadata commit point, so readers observe either the old session or the new
session and never a mixture.

## Reconstruction Notes

- Similarity target: recreate scoped session allocation, canonical key generation, JSONL history backend, legacy alias promotion, and launcher history endpoints.
- Core types/functions: `SessionScope`, `SessionSnapshot`, `SnapshotReader`,
  `SessionSnapshotReplacement`, `SnapshotReplacer`, route session allocator,
  canonical key helpers, JSONL backend, memory store, and session API handlers.
- Runtime ordering: normalize route policy, derive dimensions, canonicalize
  identity, create metadata, promote aliases only when safe, append/read
  messages, capture strict existing-session snapshots when requested,
  compare-and-swap whole-session replacements only through a supporting
  backend, and expose list/detail/delete from the committed history selector.
- Non-obvious constraints: invalid dimensions are dropped, corrupt JSONL lines
  are skipped, existing canonical history is never overwritten by alias
  promotion, and `model_name` denotes a configured alias or model router rather
  than a raw upstream model ID. Ordinary history recovery may skip corrupt
  JSONL lines, but a strict snapshot fails on corruption because an automated
  decision must not run against silently incomplete context. The metadata
  `HistorySlot` field is the commit selector: an empty value keeps the legacy
  `.jsonl` history active, while `a` or `b` selects exactly one bounded history
  slot. A missing or invalid selected slot is corruption and never falls back
  to another file.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SESSION-001` | MUST | Session scope is allocated from route policy and inbound context using supported dimensions: space, chat, topic, and sender. | Conversation isolation must be predictable. |
| `FR-SESSION-002` | MUST | Canonical session keys include routed agent identity and normalized dimension values. | Multi-agent and multi-channel history must not collide. |
| `FR-SESSION-003` | MUST | Legacy aliases remain readable and can promote history into an empty canonical session without overwriting existing canonical history. | Upgrades must preserve user history. |
| `FR-SESSION-004` | MUST | JSONL storage appends messages atomically per session and skips corrupt lines while reading remaining history. | Durable history should survive partial writes. |
| `FR-SESSION-005` | MUST | Session summaries and compaction preserve enough context for future turns while respecting configured thresholds. | Long sessions need bounded context. |
| `FR-SESSION-006` | MUST | Launcher session APIs list and fetch the canonical owner of promoted legacy Pico aliases without classifying an authoritative structured non-Pico scope as Pico; after ID discovery, the exact metadata returned with the coherent history tuple must still project to that ID before history is exposed. Deletion revalidates the full current catalog under one lock and removes every canonical owner projected to the requested Pico ID plus every compatible retained shadow in one group; metadata-less opaque/legacy candidates are removable only while metadata remains absent, and a shadow must not erase a nonmatching metadata-backed resource. Related JSONL deletions first commit one durable manifest, durably remove metadata, base history, and both bounded slots, then clear the manifest; reopening the store finishes any interrupted manifest before serving data. If an atomic manifest write reports an error after making the exact manifest visible, cleanup still completes before the store is reusable. If cleanup remains incomplete, every already-open store fails closed for that directory until successful recovery, preventing a new generation from being erased later. Legacy JSON deletion is also made durable, and non-not-found legacy lookup failures are surfaced. | History management must not expose another channel after a lookup/read rebind, project stale promoted history, partially delete a multiply owned launcher identity, or let a deleted shadow/recovery intent erase a later session generation. |
| `FR-SESSION-007` | SHOULD | Explicit session keys supplied by trusted callers are preserved when compatible with canonical or legacy formats. | Tests, direct calls, and compatibility flows need determinism. |
| `FR-SESSION-008` | MUST | An inbound Pico chat selection is the pair `account_ref` plus alias-valued `model_name`: the account reference may identify a concrete account or account router, and the model name may identify an exact configured alias or model router. Pico input normalizes and forwards both fields into turn resolution. Stored provider messages and current backend live/history projections preserve only alias-valued `model_name`; they do not persist or emit `account_ref`. Frontend live/history parsers accept an optional `account_ref` and include it in message reconciliation whenever a server supplies one, while remaining compatible with its absence. No selection field carries the resolved upstream provider model ID. | Runtime account selection and durable alias identity have different lifetimes; the wire contract must not imply persistence that the backend does not provide or leak concrete model resolution. |
| `FR-SESSION-009` | MUST | A session backend may implement `SnapshotReader.ReadSessionSnapshot(ctx, key)` to return one deep-cloned, coherent view of an existing session's canonical key, history, summary, and scope. Blank or unknown keys return `found=false` without creating a fallback/default session. JSONL snapshots require an existing decodable metadata file with a nonblank logical key matching the canonical lookup; a history-only orphan is rejected even when filename sanitization makes another logical key collide with it. Alias lookup returns the canonical key and is strict: metadata read/decode errors, inconsistent canonical metadata, JSONL corruption, scanner errors, and cancellation are returned rather than skipped. Distinct sessions claiming the same alias are rejected, and an alias resolved before the canonical lock must still be present in the locked metadata or the read fails as changed. The canonical session lock covers the final existence check plus metadata, summary, and history read, and every returned mutable message, nested tool argument, timestamp, metadata collection, and scope is detached from live state. | A read-only AI decision must be based on an exact immutable input and must not mutate, partially recover, or alias the conversation it is evaluating. |
| `FR-SESSION-010` | MUST | A backend may implement `SnapshotReplacer.ReplaceSessionSnapshot(ctx, replacement)` to compare-and-swap one canonical session's visible history, summary, scope, and aliases. The replacement key must be the exact opaque key derived from a canonical current-version scope: channel/account/dimensions/values are normalized, dimensions are unique, and `Values` contains exactly one nonblank canonical value per listed dimension with no unlisted semantic fields. Aliases and messages must be canonical and persistable, and `ExpectedRevision` must exactly match the opaque revision returned by a strict snapshot; an empty expected revision means the canonical session must not exist. JSONL replacement holds shared process-wide directory and session locks, rejects corrupt current state and new alias conflicts, durably writes the inactive bounded `a`/`b` history slot, checks cancellation, and atomically renames metadata that selects that slot as the sole commit point before verifying committed alias ownership. It may preserve an unchanged shared legacy fallback alias, including a main fallback, or retained promoted direct shadow already owned by the session, but may not introduce that ambiguity. Empty `HistorySlot` continues to select the legacy `.jsonl` file, while `a` and `b` select only their exact slot; invalid or missing selected slots fail closed. Strict metadata requires nonnegative `Skip`/`Count`, `Skip <= Count`, and at least `Count` physical nonempty records; a missing legacy file is valid only for an exact empty metadata tuple. Every append or rewrite rejects an encoded record that cannot be read within the shared scanner limit. Alias-aware reads and ordinary mutations retain the directory read lock from alias resolution through canonical session access; adjacent metadata mutations resolve under the directory write lock. All use compatible selector rules, so aliases cannot move between resolution and access, concurrent callers see only the complete old tuple or complete new tuple, and stale revisions conflict without a visible mutation. An observed cancellation at the check after staging history, or another error before metadata rename, leaves the old tuple visible; cancellation after that check does not undo metadata publication. Any error after metadata rename, including directory synchronization, cancellation, or alias verification, is an uncertain outcome and requires a fresh strict read before retry. The capability is optional: unsupported adapters return `ErrSnapshotUnsupported` and callers must never emulate it with individual legacy setters. This is an additive metadata/file-layout extension and does not change the scope schema or its version. | An AI-authored session rewrite must not tear history from its metadata, overwrite concurrent work, silently recover corrupt inputs, or assume a failed call was definitely uncommitted. |

## Data And State Model

Persistent state is session JSONL message files plus metadata containing scoped
identity and aliases. Runtime state includes allocated scope fields, canonical
keys, legacy aliases, summaries, structured message text/media parts, and
shared process-wide directory/session lock shards. Optional message selection
metadata stores alias-valued `model_name`; inbound `account_ref` remains
transient turn metadata and is not part of the stored provider message. The
concrete upstream model
used by the provider is not session selection state. `SessionSnapshot` is a
transient value, not another persistence format; its `Key` is canonical even
when the caller supplied an alias, and its history, summary, and optional scope
describe one point-in-time read. Strict JSONL snapshots also expose committed
aliases and an opaque transient `Revision` computed from the canonical key,
visible history, and committed metadata. `Revision` is not serialized. Durable
JSONL state may include the legacy `{base}.jsonl`, bounded
`{base}.history-a` and `{base}.history-b` files, and `{base}.meta.json`;
`SessionMeta.HistorySlot` chooses the only visible history. Empty
`HistorySlot` preserves all existing sessions without a format or scope-version
migration. Shared fixed-size locks coordinate independently constructed JSONL
stores rooted at the same directory within one process.

## Surface Ownership

Owns: CODE pkg/agent/memory/**
Owns: CODE pkg/agent/sessions/**
Owns: CODE pkg/agent/state/**
Owns: CODE pkg/identity/**
Owns: CODE pkg/memory/**
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
Owns: TEST pkg/identity/*
Owns: TEST pkg/state/*
Owns: TEST web/backend/api/session*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `session.dimensions`, `session.identity_links`, legacy `dm_scope` | Session isolation policy and compatibility input. | `FR-SESSION-001`, `FR-SESSION-003` |
| HTTP | `GET /api/sessions`, `GET /api/sessions/{id}`, `DELETE /api/sessions/{id}` | Launcher history list/detail resolves promoted ownership and reads the metadata-selected legacy or `a`/`b` history as one coherent tuple; delete revalidates and atomically removes all current owners of the projected Pico ID plus compatible shadows. | `FR-SESSION-006`, `FR-SESSION-010` |
| Storage | Workspace session JSONL files, bounded history slots, and metadata | Durable conversation messages, summaries, aliases, and the additive `HistorySlot` commit selector. | `FR-SESSION-004`, `FR-SESSION-005`, `FR-SESSION-010` |
| Chat/history wire | Inbound Pico payloads and frontend `SessionDetail.messages[]` | Inbound requests carry optional `account_ref` plus alias-valued `model_name`; stored/backend-projected messages preserve `model_name`, and frontend projections tolerate optional `account_ref` without requiring it. | `FR-SESSION-008` |
| Go API | `session.SnapshotReader.ReadSessionSnapshot(ctx, key)` | Strictly read and deep-clone one existing session, resolving aliases to a canonical key without creation or recovery-style corruption skipping; replacement-capable backends also return committed aliases and an opaque revision. | `FR-SESSION-009`, `FR-SESSION-010` |
| Go API | `session.SnapshotReplacer.ReplaceSessionSnapshot(ctx, replacement)` | Optionally publish an exact-revision whole-session replacement atomically, or fail closed with conflict/unsupported errors. | `FR-SESSION-010` |
| Go API | `memory.JSONLStore.ReadSessionState`, `UpdateSessionMeta`, `EnsureSessionHistory`, `DeleteSession`, and `DeleteSessionsWithAliasesMatching` | Give web/thread consumers an atomically alias-resolved tolerant projection, coordinated metadata mutation, selector-aware creation, and crash-recoverable exact/catalog-matched grouped deletion without bypassing the active slot. | `FR-SESSION-006`, `FR-SESSION-010` |
| Go API | `session.CloneMessages(messages)` | Produce a graph-detached message copy for each isolated consumer, including pointer fields and nested/cyclic tool arguments. | `FR-SESSION-009` |
| Frontend | Logs and session history UI under `web/frontend/src/components/logs/**`, `web/frontend/src/hooks/use-session-history.ts`, and `web/frontend/src/routes/logs.tsx` | Browser history and log surfaces expose session records and follow shared frontend API, token, and dynamic-style lint rules. | `FR-SESSION-006` |

## Algorithms And Ordering

1. Convert inbound context and route policy into normalized scope dimensions.
2. Build canonical key from agent and selected dimensions.
3. Create metadata and promote legacy alias history only when canonical history is empty.
4. Append messages in JSONL order under per-session synchronization.
5. Normalize the inbound account reference and selected alias for turn
   resolution, then persist/project the alias as `model_name`; keep
   `account_ref` transient and the resolved concrete provider model internal to
   execution.
6. Read history by skipping corrupt lines and applying summary/compaction policy,
   then reconcile browser messages using `model_name` and any optional
   `account_ref` a response actually supplies.
7. For a strict snapshot, reject a blank or missing key without creation,
   resolve aliases with strict metadata decoding, lock the canonical session,
   recheck existence, read matching metadata plus every retained JSONL record
   after the logical skip boundary without corruption recovery, then return
   deep-cloned history, summary, scope, aliases, and the opaque revision of that
   exact tuple.
8. For a whole-session replacement, validate the opaque key and exact canonical
   scope shape/binding (including no values outside its unique dimensions),
   aliases, and persistable message shape; under the shared directory and
   canonical-session locks, compare the exact current revision (or exact
   absence), write and sync the inactive `a`/`b` slot, check cancellation, then
   atomically rename and sync metadata selecting that slot, then verify its
   alias ownership. New aliases must resolve uniquely; unchanged
   legacy shared aliases and promoted direct shadows may be preserved without
   introducing another owner. Treat the metadata rename as the
   visibility point and reread after any post-rename error.
9. For browser, thread, and ordinary agent-store access, hold the directory
   read lock while resolving a retained promoted alias and until the canonical
   session access completes, including its session lock. Preserve tolerant malformed-record
   recovery, but do not inspect inactive slots or fall back when a nonempty
   selector is invalid or its selected file is missing. Coordinate thread-owned
   metadata changes through the memory store so they preserve a concurrent slot
   flip.

## Cross-Feature Behavior

Routing supplies the session policy. Agent conversations read and write session
history, including structured prompt parts when provider history supplies them.
Chat channels provide normalized scope values. Launcher management exposes the
history surface. Threads store discoverable thread records and handoff links on
top of session metadata without deleting the underlying conversation history.
Account and model routing consume the inbound selection pair. History preserves
the alias name, not the account reference or concrete provider-resolution
result. Workflow read-only agent decisions consume the optional strict snapshot
interface and fail closed when a backend cannot provide it.
Future workflow write decisions can consume the separate optional replacement
interface, use its `ssr_v1_` snapshot revisions for optimistic concurrency, and
reread after conflicts or uncertain post-commit errors. This PR adds the
capability; it does not reinterpret the existing workflow `history_revision`
fingerprint as a session CAS token. Launcher session and thread consumers use the
metadata-selected history and coordinated mutation helpers instead of opening
the legacy `.jsonl` file independently.

## Failure And Edge Cases

- Invalid or duplicate configured dimensions are ignored.
- Missing metadata does not prevent ordinary recovery of a valid legacy
  session body; strict snapshots and nonempty history selectors require exact
  metadata.
- Related JSONL deletion commits a checksummed-name manifest before removing
  any canonical/shadow member, makes every removal durable, and removes the
  manifest last. Store construction finishes a valid interrupted manifest and
  fails closed on an invalid one. Cancellation is rechecked after lock/target
  validation and before the manifest; after the manifest commits, cleanup
  intentionally continues and any returned cleanup error is commit-uncertain.
  A visible exact manifest after a write error is completed synchronously.
  If cleanup remains pending, all already-open stores fail closed for that
  exact directory until a constructor or delete operation recovers it.
  Identity deletion matches the current full metadata catalog under the same
  lock, includes exact metadata-less candidates only while still orphaned, and
  protects nonmatching metadata-backed alias targets. Legacy JSON removal is
  durable too.
- Missing optional selection metadata remains readable for legacy messages.
  On inbound chat, a raw `model` field is ignored as selection authority; only
  `account_ref` and alias-valued `model_name` participate in turn selection.
  Browser message identity uses either optional selection field only when that
  field is present in the received payload.
- Large sessions are summarized or compacted rather than loaded without bound.
- Strict snapshot lookup deliberately differs from normal history recovery:
  any unreadable or malformed candidate metadata, mismatched canonical key, or
  malformed retained JSONL record aborts the snapshot. Records before the
  metadata's logical skip boundary remain outside the visible snapshot. A history file without exact
  metadata is an incomplete orphan, not an existing strict session, including
  when two logical keys sanitize to the same filename. Cancellation is checked
  before lookup, during alias scanning and retained-history scanning, and
  immediately after lock acquisition and the coherent read. Existing Go mutex
  acquisition itself is not interruptible, so cancellation is returned after a
  current short critical-section holder releases the lock. A failed lookup
  never creates metadata, history, or a fallback session.
- `HistorySlot` accepts only empty, `a`, or `b`. Empty selects legacy JSONL for
  backward compatibility; a nonempty selector whose exact file is missing is
  corrupt and must not fall back to legacy or the inactive slot. Orphan slot
  files are not sessions and inactive slot contents are never projected.
- Strict metadata rejects negative offsets, `Skip > Count`, a declared count
  larger than the physical nonempty record count, and a missing legacy history
  whose metadata implies prior records. A physical count greater than metadata
  remains valid for append-before-metadata crash recovery.
- Append and whole-history writes reject any encoded JSONL record too large for
  the shared scanner before mutating visible history or metadata.
- Replacement rejects a stale or absent-session revision mismatch, invalid
  key/scope binding, duplicate/noncanonical/colliding aliases, non-persistable
  runtime message fields, and corrupt current metadata/history. Cancellation is
  checked after staging inactive history; if observed there, the prior visible
  tuple remains unchanged. Once metadata publication starts, later
  cancellation does not undo it. Any error after rename, including directory
  synchronization, cancellation, or committed-alias verification, is
  commit-uncertain, so the caller must perform a strict reread rather than
  blindly replaying the write.
- Ordinary legacy metadata may contain intentionally shared fallback aliases.
  Replacement can round-trip such an unchanged alias but rejects a new claim;
  strict lookup through an ambiguous alias remains fail-closed. Launcher and
  thread projections canonicalize retained promoted aliases before reading.
- Shared locks coordinate JSONL store instances only inside the current
  process. The feature does not claim cross-process transaction isolation.
- Coordinated adjacent metadata mutation rejects changes to the canonical key
  or history-owned `HistorySlot`, `Skip`, and `Count` fields. If an adjacent
  callback does not change aliases, their exact legacy shape is preserved and
  unrelated corrupt catalog entries do not block the update; an actual alias
  change is normalized and checked. Metadata initialization may retain
  allocator-generated shared `agent:` fallbacks across accounts, while strict
  replacement cannot newly introduce any shared owner.
- A backend that lacks the optional lower-store replacement capability returns
  `ErrSnapshotUnsupported`; sequential `SetHistory`, `SetSummary`, or metadata
  calls are not a valid fallback.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SESSION-001`, `FR-SESSION-002`, `FR-SESSION-003`, `FR-SESSION-007` | [pkg/session/allocator_test.go](../../pkg/session/allocator_test.go), [pkg/session/key_test.go](../../pkg/session/key_test.go), [docs/architecture/session-system.md](../architecture/session-system.md) |
| `FR-SESSION-004`, `FR-SESSION-005` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/agent/context_budget_test.go](../../pkg/agent/context_budget_test.go), [pkg/agent/context_cache_test.go](../../pkg/agent/context_cache_test.go) |
| `FR-SESSION-006` | [web/backend/api/session_test.go](../../web/backend/api/session_test.go) |
| `FR-SESSION-008` | [pkg/channels/pico/pico_test.go](../../pkg/channels/pico/pico_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [web/frontend/src/features/chat/protocol.test.ts](../../web/frontend/src/features/chat/protocol.test.ts), [web/frontend/src/features/chat/history.ts](../../web/frontend/src/features/chat/history.ts), [web/frontend/src/api/sessions.ts](../../web/frontend/src/api/sessions.ts) |
| `FR-SESSION-009` | [pkg/session/manager_test.go](../../pkg/session/manager_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/memory/jsonl_test.go](../../pkg/memory/jsonl_test.go), [pkg/session/session_store.go](../../pkg/session/session_store.go), [pkg/session/manager.go](../../pkg/session/manager.go), [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go), [pkg/memory/jsonl.go](../../pkg/memory/jsonl.go) |
| `FR-SESSION-010` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/memory/jsonl_test.go](../../pkg/memory/jsonl_test.go), [pkg/threads/threads_test.go](../../pkg/threads/threads_test.go), [web/backend/api/session_test.go](../../web/backend/api/session_test.go), [pkg/session/session_store.go](../../pkg/session/session_store.go), [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go), [pkg/memory/jsonl.go](../../pkg/memory/jsonl.go) |

## Implementation Anchors

- [pkg/session/allocator.go](../../pkg/session/allocator.go)
- [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go)
- [pkg/session/session_store.go](../../pkg/session/session_store.go)
- [pkg/memory/jsonl.go](../../pkg/memory/jsonl.go)
- [web/backend/api/session.go](../../web/backend/api/session.go)
