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
session and never a mixture. A separate media-freezing capability can detach
every provider-neutral locator in a strict snapshot into one self-contained,
versioned frozen set and later materialize the same bytes after the live media
store has been released or reconstructed. When the Seahorse context strategy is
selected, one generation additionally owns distinct per-agent SQLite context
engines and exposes their retrieval capabilities only after every engine and
startup bootstrap has completed successfully. Workspace delivery hints used by
heartbeat and device notifications persist separately in a hardened SQLite
runtime-state database instead of mutable JSON files.

## Reconstruction Notes

- Similarity target: recreate scoped session allocation, canonical key generation, JSONL history backend, legacy alias promotion, and launcher history endpoints.
- Core types/functions: `SessionScope`, `SessionSnapshot`, `SnapshotReader`,
  `SessionSnapshotReplacement`, `SnapshotReplacer`, route session allocator,
  canonical key helpers, JSONL backend, memory store, session API handlers,
  `media.FreezeInputs`, `media.FrozenSet`,
  `FreezeSessionSnapshotMedia`, `MaterializeSessionSnapshotMedia`,
  `seahorseContextManager`, `seahorse.Engine`, and the Seahorse retrieval tools.
- Runtime ordering: normalize route policy, derive dimensions, canonicalize
  identity, create metadata, promote aliases only when safe, append/read
  messages, capture strict existing-session snapshots when requested,
  compare-and-swap whole-session replacements only through a supporting
  backend, preflight and freeze all media locators when an isolated consumer
  requests it, materialize only from the resulting self-contained frozen set,
  construct and bootstrap a complete isolated Seahorse engine generation before
  atomically publishing its retrieval wrappers, and
  expose list/detail/delete from the committed history selector.
- Non-obvious constraints: invalid dimensions are dropped, corrupt JSONL lines
  are skipped, existing canonical history is never overwritten by alias
  promotion, and `model_name` denotes a configured alias or model router rather
  than a raw upstream model ID. Ordinary history recovery may skip corrupt
  JSONL lines, but a strict snapshot fails on corruption because an automated
  decision must not run against silently incomplete context. The metadata
  `HistorySlot` field is the commit selector: an empty value keeps the legacy
  `.jsonl` history active, while `a` or `b` selects exactly one bounded history
  slot. A missing or invalid selected slot is corruption and never falls back
  to another file. Media freezing is fail-closed and all-or-nothing: a
  `media://` capability must still be live at capture, every potential locator
  field is inspected even when another provider-neutral field currently takes
  precedence, and restart safety begins only after a complete frozen set has
  been obtained. Seahorse engines never share one canonical database path across
  agents, and a context-manager fallback never leaves retrieval tools bound to a
  rejected or closed engine.

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
| `FR-SESSION-011` | MUST | `FreezeSessionSnapshotMedia` counts one already detached `SessionSnapshot` before cloning, rejects a 33rd nonempty locator, then deep-clones and discovers every admitted occurrence in `Message.Media[]`, `Attachment.Ref`, `Attachment.URL`, and `PromptPart.URI` without applying provider precedence. Only a canonical `media://` UUID capability or a strict `data:` locator with canonical MIME, the base64 marker, and canonical padded base64 is admitted; raw paths, `file:`, network URLs, malformed/noncanonical data, unknown schemes, and unresolved or no-longer-live media capabilities fail the whole capture without changing the source snapshot or returning a partial set. At most 16 distinct nonempty frozen assets are admitted; each decoded asset is at most 2 MiB, the sum of decoded bytes counted per occurrence is at most 3 MiB, and both materialized encoding and frozen-set JSON are at most 5 MiB. At most four `FreezeInputs` operations hold capture admission concurrently; an excess operation waits only until a slot is available or its context is cancelled before its reader is invoked. Raw filename input is at most 4 KiB before basename sanitization; the result is valid UTF-8, control-free, and at most 255 bytes. Supplied MIME is at most 127 bytes; captured MIME input is at most 1 KiB before normalization to canonical parameter-free form of at most 127 bytes. Success returns the cloned snapshot with every locator rewritten to a canonical frozen reference plus one deterministic, versioned, self-contained `FrozenSet` containing detached bytes and canonical metadata; the caller can embed that pair in durable state. `MaterializeSessionSnapshotMedia(ctx, snapshot, set)` validates count and set before cloning, strictly resolves every frozen reference, requires each provider-authoritative attachment or prompt-part metadata field to equal its bound asset metadata, and deterministically replaces all four locator surfaces with canonical padded-base64 `data:` values without consulting a live `MediaStore`; when an attachment has both fields, URL metadata is authoritative and Ref metadata remains independently bound inside its frozen identity. The snapshot/set pair survives strict frozen-set JSON marshal/unmarshal and materializes identically with an empty reconstructed media store. Unknown/duplicate/trailing frozen-set JSON members, invalid UTF-8 or unpaired surrogate strings, unsupported versions, noncanonical encodings, missing/duplicate/unused assets, invalid sizes or digests, reference/metadata mismatches, cancellation, and any bound violation fail closed through fixed redacted errors. This capability does not automatically change ordinary session persistence, provider execution, or agent history resolution, and does not guarantee that a provider consumes the materialized modality. | A delayed isolated decision needs the exact captured media bytes after restart, while every hidden or precedence-inactive locator must be validated so durable context cannot conceal a local-path/network capability or silently degrade to text-only history. |
| `FR-SESSION-012` | MUST | A replacement-capable session backend also implements `ScopeAdmitter.AdmitSessionScope(ctx, admission)` so an ordinary live turn and a protected review projection arbitrate ownership under the same process-wide directory-write and strictly resolved canonical-session lock used by snapshot replacement. Live mode rejects any existing or requested `review` scope, including spoofed review-channel input, but may atomically migrate/update ordinary legacy scope and aliases before alias-history promotion. Review mode requires an exact canonical v1 review scope whose derived opaque key equals the admitted key, reserves only a genuinely absent key with exclusive immutable aliases, preserves an existing review tuple for subsequent strict binding validation, and rejects ordinary, unscoped, malformed, mismatched, or conflicting alias state. Admission through an existing ordinary alias preserves that alias and its canonical history without reopening an ownership gap. A typed conflict, unsupported capability, cancellation, decode error, or alias collision returns before the losing caller can use protected transcript content, mutate, clear, attach, or invoke a provider; scope and ownership checks may read metadata. Cancellation is rechecked after lock acquisition and before commit. The capability and locks are process-local; sharing one JSONL directory across processes remains outside the contract. | A check followed by an unconditional metadata upsert lets a live turn overwrite a review projection created between those operations; one atomic ownership boundary must decide which namespace wins. |
| `FR-SESSION-013` | MUST | A supported Seahorse context generation snapshots sorted configured agents and requires each to have one distinct canonical `sessions/seahorse.db` path; exact, case-insensitive existing-ancestor, hard-link, or resolved-parent aliases fail the complete candidate rather than sharing history, including `all_conversations`, across agents. It privately creates and records every per-agent engine, then revalidates persistent path identity and physical-file uniqueness before exposure, and bootstraps detached session keys in deterministic order with the construction context. Review-scoped sessions, missing snapshots, and strict unreadable/corrupt snapshots retain fail-closed skip behavior, while cancellation, panic, a real engine/bootstrap failure, an aliased database, persistent construction-time path drift, or a later-agent failure closes every created engine exactly once and yields the legacy context strategy with no Seahorse retrieval surface. Only the installed context manager owns engine shutdown. A successful generation gives Assemble, Compact, Ingest, Clear, grep, and expand the exact owning agent engine/database; another agent, owner-created wrapper, sibling, or reload generation cannot observe or close it. Reload constructs B only after the paused/drained A context manager has closed, and shutdown closes the context manager before releasing its factory-backed source registries. Unsupported platforms retain the legacy strategy and expose no Seahorse factory capability. The path guarantee assumes a stable trusted-workspace namespace during paused construction; hostile out-of-process symlink, mount, or rename swaps during SQLite's internal open are outside this contract because the engine exposes no opened-file identity. | Per-agent context memory must not leak through a shared SQLite path in the stable namespace, partial bootstrap, stale reload engine, or retrieval wrapper that outlives the context manager generation owning its database. |
| `FR-SESSION-014` | MUST | The workspace's last external channel, last chat ID, and shared update timestamp persist as typed singleton state in `<workspace>/state/runtime.db`. Every open uses the common hardened SQLite contract: private directory and files, WAL, foreign keys, bounded busy timeout, `synchronous=FULL`, exact schema/integrity validation, and a versioned row. Field-specific immediate transactions preserve another manager or process's independently updated field. The source-compatible `state.NewManager` retries and reports setter failures while its no-error getters log and return zero values on unavailable state; `state.NewSQLiteManager` exposes initialization errors directly. On first open, bounded `state/state.json` takes precedence over `state.json`; valid sources, selected malformed records, conflicts, and SQLite-authoritative skips are recorded with payload-free codes and digests before exact sources are archived without overwrite under `state/legacy-json/runtime-state-v1/`. Pending archives retry without reimport, changed committed sources are refused, and no operation dual-writes JSON. | Heartbeat and device delivery must survive restart without stale whole-file managers losing each other's fields, unsafe state becoming agent-editable authority, or migration diagnostics exposing channel/chat identities. |

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
stores rooted at the same directory within one process. Frozen session media is
a detached pair composed of a deep-cloned `SessionSnapshot` whose locators are
frozen and a self-contained `media.FrozenSet`; an owning durable record may
embed both. The set uses a strict explicit JSON version, canonical
frozen-reference identities, deterministic asset ordering, bounded copied
filename/content-type metadata, decoded bytes, sizes, and content digests. It
does not retain a local path, live `media://` capability, store scope, cleanup
policy, or `MediaStore` pointer. Re-encoding and materialization are bounded by
the same 32-occurrence, 16-asset, 2 MiB per-asset, 3 MiB per-occurrence raw, and
5 MiB encoded/JSON ceilings. A filename is retained only as a valid UTF-8,
control-free, at-most-255-byte basename; a MIME value is canonical,
parameter-free, and at most 127 bytes.
When Seahorse is active, runtime state also contains one context-manager-owned
engine and retrieval object per agent. Each engine stores derived context in a
distinct canonical workspace `sessions/seahorse.db`; these databases do not
replace the canonical JSONL session store and never become a shared multi-agent
namespace.
Workspace delivery state is separate from conversation history. Its singleton
`runtime.db` row contains only last channel, last chat ID, update
seconds/nanoseconds, a row version, and migration-origin priority; it stores no
provider message, session scope, credential, or arbitrary JSON payload.

## Surface Ownership

Owns: CODE pkg/agent/memory/**
Owns: CODE pkg/agent/sessions/**
Owns: CODE pkg/agent/state/**
Owns: CODE pkg/identity/**
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
| Config | `session.dimensions`, `session.identity_links`, legacy `dm_scope` | Session isolation policy and compatibility input. | `FR-SESSION-001`, `FR-SESSION-003` |
| HTTP | `GET /api/sessions`, `GET /api/sessions/{id}`, `DELETE /api/sessions/{id}` | Launcher history list/detail resolves promoted ownership and reads the metadata-selected legacy or `a`/`b` history as one coherent tuple; delete revalidates and atomically removes all current owners of the projected Pico ID plus compatible shadows. | `FR-SESSION-006`, `FR-SESSION-010` |
| Storage | Workspace session JSONL files, bounded history slots, and metadata | Durable conversation messages, summaries, aliases, and the additive `HistorySlot` commit selector. | `FR-SESSION-004`, `FR-SESSION-005`, `FR-SESSION-010` |
| Storage/runtime | Per-agent `sessions/seahorse.db`, `seahorseContextManager`, engines, and retrieval objects | Build one isolated derived context index per agent, bootstrap it from strict session evidence, and close it only with its exact context generation. | `FR-SESSION-013` |
| Storage/runtime | `<workspace>/state/runtime.db`, `state.Manager` | Transactionally persist and read the last external delivery context used by AgentLoop, heartbeat, and device notifications, with one-time bounded legacy import/archive. | `FR-SESSION-014` |
| Chat/history wire | Inbound Pico payloads and frontend `SessionDetail.messages[]` | Inbound requests carry optional `account_ref` plus alias-valued `model_name`; stored/backend-projected messages preserve `model_name`, and frontend projections tolerate optional `account_ref` without requiring it. | `FR-SESSION-008` |
| Go API | `session.SnapshotReader.ReadSessionSnapshot(ctx, key)` | Strictly read and deep-clone one existing session, resolving aliases to a canonical key without creation or recovery-style corruption skipping; replacement-capable backends also return committed aliases and an opaque revision. | `FR-SESSION-009`, `FR-SESSION-010` |
| Go API | `session.SnapshotReplacer.ReplaceSessionSnapshot(ctx, replacement)` | Optionally publish an exact-revision whole-session replacement atomically, or fail closed with conflict/unsupported errors. | `FR-SESSION-010` |
| Go API | `session.ScopeAdmitter.AdmitSessionScope(ctx, admission)` | Atomically admit ordinary live ownership or reserve protected review ownership before either caller may use the session. | `FR-SESSION-012` |
| Go API | `memory.JSONLStore.ReadSessionState`, `UpdateSessionMeta`, `EnsureSessionHistory`, `DeleteSession`, and `DeleteSessionsWithAliasesMatching` | Give web/thread consumers an atomically alias-resolved tolerant projection, coordinated metadata mutation, selector-aware creation, and crash-recoverable exact/catalog-matched grouped deletion without bypassing the active slot. | `FR-SESSION-006`, `FR-SESSION-010` |
| Go API | `session.CloneMessages(messages)` | Produce a graph-detached message copy for each isolated consumer, including pointer fields and nested/cyclic tool arguments. | `FR-SESSION-009` |
| Go API | `media.FreezeInputs(ctx, inputs, reader)`, `media.FrozenSet.Materialize(ctx, refs)` | Atomically freeze one complete bounded locator batch into canonical references plus a self-contained strict/versioned set, then materialize only validated embedded bytes without live-store lookup. | `FR-SESSION-011` |
| Go API | `session.FreezeSessionSnapshotMedia(ctx, snapshot, reader)`, `session.MaterializeSessionSnapshotMedia(ctx, snapshot, set)` | Deep-clone, enumerate, freeze, rewrite, strictly round-trip, and materialize every `Message.Media`, attachment ref/URL, and prompt-part URI while preserving all other snapshot state. | `FR-SESSION-011` |
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
10. For session-scope admission, acquire the process-shared directory write
    lock, strictly resolve the requested key or alias, lock the canonical
    session, and evaluate the live/review policy against the locked metadata.
    Atomically write only an allowed scope and aliases, preserving the requested
    key when it resolved through an existing owner. While retaining those same
    locks, live admission may promote compatible legacy history; review
    admission never promotes alias history and leaves an existing protected
    tuple unchanged for strict bridge validation and exact-revision replacement.
11. To freeze snapshot media, count all four locator surfaces before cloning
    and stop at the 33rd; then deep-clone and enumerate the admitted locators in
    stable message/field/index order. Preflight occurrence count,
    locator form/length, and supplied metadata; parse only canonical UUID
    `media://` or canonical padded-base64 `data:` inputs; acquire one of four
    context-cancellable global capture slots; then charge decoded,
    aggregate-occurrence, distinct-nonempty-asset, and encoded budgets while
    capturing each distinct live capability. Basename-sanitize and bound
    filenames and normalize MIME to bounded parameter-free canonical form.
    Construct the canonical ordered set, rewrite every occurrence to its frozen
    identity, validate the complete detached result, then return it without
    mutating the input.
12. To materialize, count locators and strictly validate the set before cloning,
    then validate its version, shape,
    canonical ordering, unique identities, size, digest, metadata, references,
    and all aggregate budgets before allocating provider-neutral output. Clone
    the supplied frozen-reference snapshot, resolve each locator from the set
    in the same stable order, verify provider-authoritative attachment/part
    metadata against the resolved asset, emit canonical base64 `data:` locators, verify no
    capability locator remains, and return only the complete result. Never fall
    back to the live media store, filesystem, network, or original locator
    text.
13. To construct Seahorse context, canonicalize and reject aliased per-agent DB
    paths before I/O, create every engine under one private cleanup guard, and
    bootstrap sorted agent/session snapshots with the caller's context. Only
    after every bootstrap succeeds may the agent feature atomically publish
    retrieval wrappers and return the owning manager. Any precommit failure
    closes the complete candidate; a committed manager remains responsible for
    its engines until quiesced reload or shutdown closes it.
14. To manage workspace delivery hints, open and validate `state/runtime.db`
    behind its private cross-process lock, import root and nested legacy JSON in
    deterministic priority order, then read the singleton row or update exactly
    one delivery field plus timestamp and row version in `BEGIN IMMEDIATE`.

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
Ordinary live turns and protected review projection share the optional atomic
scope-admission interface before any session-dependent command, history read,
metadata mutation, provider call, or whole-snapshot replacement.
Future workflow write decisions can consume the separate optional replacement
interface, use its `ssr_v1_` snapshot revisions for optimistic concurrency, and
reread after conflicts or uncertain post-commit errors. This PR adds the
capability; it does not reinterpret the existing workflow `history_revision`
fingerprint as a session CAS token. Launcher session and thread consumers use the
metadata-selected history and coordinated mutation helpers instead of opening
the legacy `.jsonl` file independently.
The optional Seahorse strategy derives a per-agent SQLite retrieval index from
the owning session store. Session Memory owns engine/database/bootstrap
isolation, while Agent Conversations owns atomic tool-catalog publication and
runtime-generation ordering. A Seahorse construction failure falls back to the
legacy context strategy without changing canonical JSONL history.
Frozen session media reuses the tool feature's optional path-free
`media.SnapshotReader` for capture and the security feature's no-follow,
resource-bound, consistency-check, and redacted-failure rules. The session
feature owns the frozen encoding and all-locator rewrite. Ordinary agent turns
are not wired to this capability by this prerequisite change; a later runtime
feature must opt in explicitly and preserve provider-specific message ordering.
AgentLoop records accepted external channel turns into workspace runtime state;
heartbeat and device services read that same database when choosing a delivery
target. None owns a cached or JSON copy.

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
- Media freezing treats all locator fields as active security input even when
  `Parts` would supersede `Media`/`Attachments` or an attachment URL would
  supersede its ref during provider conversion. One unsupported, malformed,
  missing, expired, or unsafe locator rejects the whole snapshot. Filename
  paths are reduced to valid UTF-8 control-free basenames of at most 255 bytes;
  invalid names fail, and MIME is normalized to parameter-free canonical form
  of at most 127 bytes.
- The 32-occurrence limit counts every nonempty locator field. The 16-asset
  limit counts canonical distinct frozen assets. The 3 MiB aggregate counts
  decoded bytes once per occurrence, so repeating one asset cannot amplify the
  materialized snapshot past the aggregate bound; each distinct decoded asset
  must be nonempty and fit 2 MiB, and the complete encoded set/JSON must fit
  5 MiB. No more than four captures hold admission concurrently; a saturated
  fifth capture can be cancelled without invoking its reader.
- Freeze and materialize return no partial rewritten history or partial
  `FrozenSet`. Cancellation, a live-store release/expiry before capture, strict
  JSON failure, missing/duplicate/noncanonical frozen identity, digest/size or
  metadata inconsistency, and any limit error leave every caller-owned input
  unchanged.
- Restart safety starts after successful freeze. A `media://` capability lost
  before capture cannot be recovered; after capture, release, TTL cleanup, an
  empty new `FileMediaStore`, and process restart do not affect strict JSON
  round-trip or materialization from the returned self-contained set.
- Seahorse rejects an aliased per-agent database path before engine creation.
  Review/corrupt snapshot skips do not weaken that isolation; a real bootstrap
  error, cancellation, panic, or later-engine failure closes the whole private
  generation. Once retrieval wrappers commit, later detached-result faults
  cannot close their engine underneath them. Reload and repeated shutdown close
  each engine at most once, and unsupported platforms remain legacy-only.
- Runtime-state values must be valid UTF-8, NUL-free, and bounded. Invalid
  setters fail before mutation. Selected malformed legacy objects are archived
  with only a safe issue code and digest; filesystem, schema, integrity, and
  future-version failures abort. The nested legacy path wins a valid conflict,
  while a row already updated by SQLite remains authoritative over late JSON.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SESSION-001`, `FR-SESSION-002`, `FR-SESSION-003`, `FR-SESSION-007` | [pkg/session/allocator_test.go](../../pkg/session/allocator_test.go), [pkg/session/key_test.go](../../pkg/session/key_test.go), [docs/architecture/session-system.md](../architecture/session-system.md) |
| `FR-SESSION-004`, `FR-SESSION-005` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/agent/context_budget_test.go](../../pkg/agent/context_budget_test.go), [pkg/agent/context_cache_test.go](../../pkg/agent/context_cache_test.go) |
| `FR-SESSION-006` | [web/backend/api/session_test.go](../../web/backend/api/session_test.go) |
| `FR-SESSION-008` | [pkg/channels/pico/pico_test.go](../../pkg/channels/pico/pico_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [web/frontend/src/features/chat/protocol.test.ts](../../web/frontend/src/features/chat/protocol.test.ts), [web/frontend/src/features/chat/history.ts](../../web/frontend/src/features/chat/history.ts), [web/frontend/src/api/sessions.ts](../../web/frontend/src/api/sessions.ts) |
| `FR-SESSION-009` | [pkg/session/manager_test.go](../../pkg/session/manager_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/memory/jsonl_test.go](../../pkg/memory/jsonl_test.go), [pkg/session/session_store.go](../../pkg/session/session_store.go), [pkg/session/manager.go](../../pkg/session/manager.go), [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go), [pkg/memory/jsonl.go](../../pkg/memory/jsonl.go) |
| `FR-SESSION-010` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/memory/jsonl_test.go](../../pkg/memory/jsonl_test.go), [pkg/threads/threads_test.go](../../pkg/threads/threads_test.go), [web/backend/api/session_test.go](../../web/backend/api/session_test.go), [pkg/session/session_store.go](../../pkg/session/session_store.go), [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go), [pkg/memory/jsonl.go](../../pkg/memory/jsonl.go) |
| `FR-SESSION-011` | [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go) |
| `FR-SESSION-012` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/memory/jsonl_test.go](../../pkg/memory/jsonl_test.go), [pkg/agent/agent_message_review_test.go](../../pkg/agent/agent_message_review_test.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go) |
| `FR-SESSION-013` | [pkg/agent/context_seahorse.go](../../pkg/agent/context_seahorse.go), [pkg/agent/context_seahorse_catalog.go](../../pkg/agent/context_seahorse_catalog.go), [pkg/agent/context_seahorse_catalog_test.go](../../pkg/agent/context_seahorse_catalog_test.go), [pkg/agent/context_seahorse_catalog_runtime_test.go](../../pkg/agent/context_seahorse_catalog_runtime_test.go), [pkg/agent/context_seahorse_test.go](../../pkg/agent/context_seahorse_test.go), [pkg/agent/context_manager_test.go](../../pkg/agent/context_manager_test.go), [pkg/agent/context_seahorse_unsupported.go](../../pkg/agent/context_seahorse_unsupported.go), [pkg/seahorse/tool_factory.go](../../pkg/seahorse/tool_factory.go), [pkg/seahorse/tool_factory_test.go](../../pkg/seahorse/tool_factory_test.go), [pkg/seahorse/short_engine_test.go](../../pkg/seahorse/short_engine_test.go), [pkg/seahorse/short_retrieval_test.go](../../pkg/seahorse/short_retrieval_test.go) |
| `FR-SESSION-014` | [pkg/state/state_test.go](../../pkg/state/state_test.go) |

## Implementation Anchors

- [pkg/session/allocator.go](../../pkg/session/allocator.go)
- [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go)
- [pkg/session/session_store.go](../../pkg/session/session_store.go)
- [pkg/session/frozen_media.go](../../pkg/session/frozen_media.go)
- [pkg/memory/jsonl.go](../../pkg/memory/jsonl.go)
- [pkg/state/state.go](../../pkg/state/state.go)
- [pkg/state/state_sqlite.go](../../pkg/state/state_sqlite.go)
- [pkg/media/frozen.go](../../pkg/media/frozen.go)
- [pkg/agent/context_seahorse.go](../../pkg/agent/context_seahorse.go)
- [pkg/agent/context_seahorse_catalog.go](../../pkg/agent/context_seahorse_catalog.go)
- [pkg/seahorse/short_engine.go](../../pkg/seahorse/short_engine.go)
- [pkg/seahorse/short_retrieval.go](../../pkg/seahorse/short_retrieval.go)
- [pkg/seahorse/tool_factory.go](../../pkg/seahorse/tool_factory.go)
- [web/backend/api/session.go](../../web/backend/api/session.go)
