# Session System

> Back to [README](../README.md)

This document describes the runtime session system used by PicoClaw to:

- map inbound messages onto stable conversation scopes
- persist message history and summaries
- preserve compatibility with legacy `agent:...` session keys while the runtime uses opaque canonical keys
- strictly read and optionally compare-and-swap a complete session snapshot

This document covers the core runtime path in `pkg/session`, `pkg/memory`, and `pkg/agent`.
It does not describe launcher login cookies or dashboard authentication sessions in `web/backend/middleware`.

## Responsibilities

The session system has five jobs:

1. Decide which messages should share the same conversation context.
2. Persist that context durably across turns and restarts.
3. Expose a small `SessionStore` interface to the agent loop.
4. Keep older session-key formats working during storage and routing migrations.
5. Let automated workflows inspect and, when supported, atomically replace one
   exact session revision without publishing torn history and metadata.

## Main Components

| Layer | Files | Responsibility |
| --- | --- | --- |
| Session contract | `pkg/session/session_store.go` | Defines `SessionStore` plus the optional strict `SnapshotReader` and atomic `SnapshotReplacer` capabilities. |
| Legacy backend | `pkg/session/manager.go` | Stores one JSON file per session. Still used as a fallback. |
| Session adapter | `pkg/session/jsonl_backend.go` | Adapts `pkg/memory.Store` to `SessionStore`, including alias/scope metadata, strict snapshot reads, and optional lower-store replacement support. |
| Durable storage | `pkg/memory/jsonl.go` | Append-oriented JSONL storage plus `.meta.json` sidecar metadata and bounded `a`/`b` history slots for crash-consistent tuple replacement. |
| Scope and key building | `pkg/session/scope.go`, `pkg/session/key.go`, `pkg/session/allocator.go` | Builds structured scopes, opaque canonical keys, and legacy aliases from routing results. |
| Runtime integration | `pkg/agent/instance.go`, `pkg/agent/agent.go`, `pkg/agent/agent_message.go` | Initializes the store, allocates session scope, and persists metadata before turns run. |

## Session Data Model

The structured session identity is represented by `session.SessionScope`:

| Field | Meaning |
| --- | --- |
| `Version` | Schema version. Current value is `ScopeVersionV1`. |
| `AgentID` | Routed agent handling the turn. |
| `Channel` | Normalized inbound channel name. |
| `Account` | Normalized account or bot identifier. |
| `Dimensions` | Ordered list of active partition dimensions such as `chat` or `sender`. |
| `Values` | Concrete normalized values for each selected dimension. |

Only four dimensions are currently recognized by the allocator:

- `space`
- `chat`
- `topic`
- `sender`

The default config uses:

```json
{
  "session": {
    "dimensions": ["chat"]
  }
}
```

That means one shared conversation per chat unless a dispatch rule overrides it.

## Canonical Keys And Legacy Aliases

The runtime now prefers opaque canonical keys:

```text
sk_v1_<sha256>
```

These keys are built from a canonical scope signature in `pkg/session/key.go`.
The goal is to make storage keys stable while decoupling them from any specific legacy text format.

For compatibility, the allocator also emits legacy aliases such as:

```text
agent:main:direct:user123
agent:main:slack:channel:c001
agent:main:pico:direct:pico:session-123
```

These aliases matter because older sessions, tests, and some tools still refer to the legacy shape.
The JSONL store resolves aliases while retaining its directory lock through the
canonical read or write, so ownership cannot change between those phases.

The agent loop also preserves explicit incoming session keys when the caller already supplied one of the recognized explicit formats:

- opaque canonical key
- legacy `agent:...` key

That behavior lives in `pkg/agent/agent_utils.go:resolveScopeKey`.

## Allocation Flow

The end-to-end flow for a normal inbound message is:

```text
InboundMessage
  -> RouteResolver.ResolveRoute(...)
  -> session.AllocateRouteSession(...)
  -> resolveScopeKey(...)
  -> ensureSessionMetadata(...)
  -> AgentLoop turn execution
  -> SessionStore read/write operations
```

More concretely:

1. `pkg/agent/agent_message.go` resolves the agent route from normalized inbound context.
2. `session.AllocateRouteSession` converts the route's `SessionPolicy` plus inbound context into a structured `SessionScope`.
3. The allocator builds:
   - `SessionKey`: canonical routed session key
   - `SessionAliases`: compatibility aliases for that routed scope
   - `MainSessionKey`: agent-level main session key
   - `MainAliases`: legacy alias for the main session
4. `runAgentLoop` persists scope metadata and aliases through `ensureSessionMetadata`.
5. During later reads or writes, `JSONLBackend.ResolveSessionKey` maps aliases back onto the canonical key.

The main session key is separate from routed chat sessions.
It is mainly used for agent-level or system-style flows that need one stable per-agent conversation, for example `processSystemMessage`.

## Scope Construction Rules

`pkg/session/allocator.go` builds scope values from normalized inbound context.
Important rules:

- `space` becomes `<space_type>:<space_id>`
- `chat` becomes `<chat_type>:<chat_id>`
- `topic` becomes `topic:<topic_id>`
- `sender` is canonicalized through `session.identity_links` before being stored

There are two special cases worth calling out.

### Telegram forum isolation

Telegram forum topics must stay isolated even when the configured dimensions only mention `chat`.
To preserve that behavior, the allocator appends `/<topic_id>` to the `chat` value for Telegram forum messages unless `topic` is already an explicit dimension.

Example:

```text
group:-1001234567890/42
group:-1001234567890/99
```

Those produce different session keys.

### Identity links

`session.identity_links` lets multiple sender identifiers collapse into one canonical identity.
Both dispatch matching and session allocation use that mapping so that the same person can keep one conversation even if their raw sender IDs differ across channels or accounts.

## Storage Format

The default runtime backend is `pkg/memory.JSONLStore`, wrapped by `session.JSONLBackend`.

Each session has metadata and one selected history. Existing sessions continue
to use the legacy history file; replacement-capable writes rotate between two
bounded slots:

```text
{sanitized_key}.jsonl       # legacy history; selected when HistorySlot is empty
{sanitized_key}.history-a   # bounded replacement slot a
{sanitized_key}.history-b   # bounded replacement slot b
{sanitized_key}.meta.json   # metadata and the active-history selector
```

The files store:

- the selected history file: one `providers.Message` per line
- `.meta.json`: summary, timestamps, line counts, logical truncation offset,
  scope, aliases, thread metadata, and `HistorySlot`

`HistorySlot` is the commit selector:

- empty selects only the legacy `.jsonl` file
- `a` selects only `.history-a`
- `b` selects only `.history-b`

The unselected slot is inactive and must not affect reads. An invalid selector
or a missing nonempty selected slot is corruption and fails closed; readers do
not fall back to legacy or inactive history. Slot files without corresponding
metadata are incomplete orphans, not discoverable sessions.

Session-related `SessionMeta` fields include:

- `Key`
- `Summary`
- `Skip`
- `Count`
- `CreatedAt`
- `UpdatedAt`
- `Scope`
- `Aliases`
- `HistorySlot`

Strict snapshots additionally return `Aliases` and an opaque `Revision`. The
revision is computed from the canonical key, exact visible history, and
committed metadata; it is transient (`json:"-"`) and is never stored in the
sidecar. `HistorySlot` is an additive, optional metadata field. Empty retains
the old layout, so this feature does not bump `ScopeVersionV1` or require a
storage migration.

## Write And Crash Semantics

Ordinary turn storage keeps append-first durability and stale-over-loss
recovery:

- `AddMessage` and `AddFullMessage` validate that the encoded line fits the
  shared scanner limit, append it, `fsync`, then update metadata.
- `TruncateHistory` is logical first: it only advances `meta.Skip`.
- `SetHistory` and `Compact` write and sync a complete history into the inactive
  `a`/`b` slot, then atomically replace metadata so it selects that slot.
- Corrupt JSONL lines are skipped during reads instead of failing the entire session.

Strict snapshot reads deliberately differ from ordinary recovery. They require
valid metadata and every retained record, resolve an alias to one canonical
key, and read metadata plus its selected history under the canonical session
lock. The returned `Revision` is therefore a compare-and-swap token for one
exact point-in-time tuple. Strict metadata offsets must be nonnegative with
`Skip <= Count`, and the physical nonempty record count may exceed but never be
smaller than `Count`. A missing legacy history is valid only when both values
are zero.

`SnapshotReplacer.ReplaceSessionSnapshot` replaces visible history, summary,
scope, and aliases as one optimistic transaction:

1. Validate the exact opaque key/scope binding, current scope version,
   canonical owner/channel/account, unique canonical dimensions, exactly
   matching values with no unlisted semantic fields, canonical aliases, and
   persistable message shape.
2. Under the shared directory and canonical-session locks, require the exact
   current revision. An empty expected revision requires exact absence.
3. Write and `fsync` the complete new history to the inactive `a`/`b` slot.
4. Check cancellation before publication.
5. Atomically rename `.meta.json` with the new tuple and `HistorySlot`. This
   rename is the sole visibility/commit point, followed by a directory sync.
6. Verify all committed alias ownership. Newly introduced aliases must
   resolve uniquely to the canonical key. An unchanged legacy shared fallback
   alias may remain strict-ambiguous, and a retained promoted direct shadow is
   checked with owner-aware resolution; neither exception may be introduced by
   the replacement.

Until step 5, old metadata continues selecting the old history, so concurrent
coherent readers see the complete old tuple. After the rename they see the
complete new tuple. A stale revision, validation failure, history-write error,
metadata-write error before rename, or cancellation observed by step 4 leaves
the old tuple visible. Once step 5 starts, later cancellation does not interrupt
or undo publication. Any error after metadata rename, including directory sync,
cancellation during alias verification, or alias verification itself, is
commit-uncertain: the method may return an error even though the new tuple is
visible, so callers must strictly reread before retrying.

`JSONLBackend.Save` maps onto `store.Compact(...)`.
In other words, `Save` is no longer "flush dirty memory to disk"; it is now "reclaim dead lines after logical truncation".

## Concurrency Model

`pkg/memory.JSONLStore` uses process-wide fixed 64-shard lock arrays. Session
locks hash the absolute cleaned storage directory plus canonical key; directory
RW locks hash the storage directory. Independently constructed stores rooted at
the same directory therefore coordinate without keeping an unbounded lock map.

The session lock makes a metadata selector and its selected history one
coherent read/write unit. The directory write lock serializes alias-catalog
scans, whole-session replacement, and coordinated adjacent metadata updates;
ordinary append, summary, truncate, full-history, compact, and ensure-history
operations hold the matching directory read lock continuously from tolerant
alias resolution through their canonical session lock and access. A concurrent
replacement therefore cannot remove or rebind an alias between resolution and
the read or write. Adjacent metadata callbacks resolve aliases while holding
the directory write lock and preserve byte-for-byte alias collections when the
callback does not own or change them.
Web and thread consumers use the same store helpers, so they cannot
stale-overwrite a slot flip made by another store instance in the same process.
`UpdateSessionMeta` also rejects changes to the canonical key or history-owned
selector/count fields.

These are shared-process locks, not filesystem or cross-process locks. The
design does not claim transaction isolation between separate PicoClaw
processes that write the same directory.

Grouped deletion is crash-recoverable rather than merely process-atomic. It
atomically writes and syncs one manifest naming the exact canonical/shadow keys,
durably removes each member's metadata, legacy body, and bounded slots, and
durably removes the manifest last. `NewJSONLStore` completes any valid pending
manifest under the shared directory write lock before returning a usable store.
Cancellation is rechecked after acquiring locks and validating targets but
before writing the manifest. Once that durable intent exists, cleanup ignores
later cancellation; a returned cleanup error leaves recovery to the manifest.
If atomic manifest replacement makes the exact intent visible but reports a
directory-sync error, deletion finishes synchronously instead of returning a
false pre-commit result that could erase a later session generation on reopen.
If later cleanup still fails, the directory remains marked recovery-pending
process-wide: already-open stores fail all ordinary, strict, metadata, and CAS
access instead of creating a generation that recovery would later erase. A
successful constructor or delete recovery clears that marker under the same
directory lock.
Identity-based grouped deletion scans the complete current metadata catalog,
revalidates every matching owner, and selects compatible shadows under that
same lock. A metadata-less filename candidate is eligible only while metadata
remains absent, and an alias never removes a current metadata-backed resource
that does not itself match the deletion identity.

The legacy `SessionManager` uses a single in-memory map guarded by an RW mutex.

Both backends satisfy the same `SessionStore` interface, which is why the agent loop does not need storage-specific code. Strict snapshot read and replacement are separate optional capabilities.

## Compatibility And Migration

`pkg/agent/instance.go:initSessionStore` prefers the JSONL backend.

Startup sequence:

1. Create `memory.NewJSONLStore(dir)`.
2. Run `memory.MigrateFromJSON(...)` to import legacy `.json` sessions.
3. Wrap the store with `session.NewJSONLBackend(store)`.
4. If JSONL initialization or migration fails, fall back to `session.NewSessionManager(dir)`.

This fallback is intentional: a partial migration would be worse than staying on the legacy store for one run.
Migration skips `.meta.json` sidecars, so it cannot re-import metadata as an
empty legacy session and overwrite either legacy-selected or slotted history.

### History-slot compatibility

Legacy metadata has no `HistorySlot`, so its zero value continues to select the
existing `.jsonl` file. The first complete-history rewrite or atomic snapshot
replacement writes `.history-a` and commits metadata that selects `a`; later
rewrites alternate between `a` and `b`. Only two replacement slots are used,
and inactive/legacy files may remain on disk without becoming visible. This is
an additive compatibility mechanism, not a scope schema or version change.

### Alias promotion

When canonical metadata is first created, `EnsureSessionMetadata` may promote history from a non-empty legacy alias into the canonical session.
That promotion only happens when the canonical session is still empty, so active canonical history is not overwritten.
Promotion reads only the history selected by the alias metadata and commits the
canonical copy through the same inactive-slot/metadata-flip protocol; stale
legacy or inactive files are ignored. The direct legacy files are retained for
compatibility. Launcher and thread discovery therefore resolve their metadata
owner before projecting history. A structured non-Pico channel is authoritative
even when a sender or alias looks like Pico. Launcher deletion removes every
current canonical owner projected to the requested Pico ID plus owned retained
Pico shadows in one grouped commit, so neither a second owner nor a shadow can
reappear after deletion.

This is how the system preserves old histories such as:

- legacy direct-message keys
- older Pico direct-session keys

while moving the runtime onto opaque canonical keys.

## Other SessionStore Implementations

`pkg/agent/subturn.go` defines an `ephemeralSessionStore`.
It satisfies the same `SessionStore` interface, but keeps data in memory only and is destroyed when the sub-turn ends.

That lets SubTurn reuse the same session-facing APIs without writing child-session history into the parent's durable storage.

`SnapshotReplacer` is optional. The legacy `SessionManager` and ephemeral store
do not provide atomic whole-session replacement. `JSONLBackend` delegates only
when its lower memory store supports replacement and otherwise returns
`ErrSnapshotUnsupported`. Callers must fail closed; composing legacy
`SetHistory`, `SetSummary`, or metadata setters would expose a torn tuple and is
not an acceptable fallback.

## Operational Consumers

The session system is consumed by more than the agent loop:

- `web/backend/api/session.go` resolves promoted ownership and uses
  `ReadSessionState` so list/detail project canonical metadata and only its
  selected legacy/`a`/`b` history as one tolerant tuple. The ID is revalidated
  from that exact returned metadata before any history is exposed, closing a
  lookup/read rebind race. Deletion revalidates and removes every current owner
  of the projected ID plus compatible shadows.
- `pkg/threads/registry.go` and `pkg/threads/threads.go` use the same coherent
  owner-aware projection for preview/count and selected-history timestamp
  fallback, plus `UpdateSessionMeta` for canonicalized thread linkage. Thread
  creation initializes session identity only for a genuinely empty session and
  preserves replacement-owned scope, aliases, summary, and history selector.
- `pkg/agent/steering.go` can recover scope metadata for active steering flows.
- tooling and tests can still refer to legacy aliases because alias resolution is handled below the agent loop.

## Related Files

- `pkg/session/session_store.go`
- `pkg/session/manager.go`
- `pkg/session/jsonl_backend.go`
- `pkg/session/scope.go`
- `pkg/session/key.go`
- `pkg/session/allocator.go`
- `pkg/memory/jsonl.go`
- `pkg/threads/registry.go`
- `pkg/threads/threads.go`
- `web/backend/api/session.go`
- `pkg/agent/instance.go`
- `pkg/agent/agent.go`
- `pkg/agent/agent_message.go`
