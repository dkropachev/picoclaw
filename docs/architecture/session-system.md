# Session System

> Back to [README](../README.md)

PicoClaw maps inbound messages to stable conversation scopes and persists
session, thread, and handoff state in one workspace-local SQLite database.
HTTP and Go contracts remain unchanged; JSON and JSONL are legacy import
formats only.

## Responsibilities

The session subsystem:

1. derives canonical conversation identities from route scope;
2. stores ordered messages, summaries, scopes, and aliases durably;
3. offers strict coherent reads and compare-and-swap snapshot replacement;
4. stores thread registry, membership, links, and handoffs transactionally;
5. imports and archives legacy session/thread JSON without dual writes.

## Main Components

| Layer | Files | Responsibility |
| --- | --- | --- |
| Session contract | `pkg/session/session_store.go` | `SessionStore` plus optional snapshot, replacement, and scope-admission capabilities. |
| In-memory store | `pkg/session/manager.go` | Non-persistent tests and callers that explicitly pass an empty path. A nonempty legacy constructor path is a deprecated SQLite facade. |
| SQLite adapter | `pkg/session/jsonl_backend.go` | `NewSQLiteBackend`; the JSONL-named type/constructor remain source-compatible facades for one cycle. |
| Durable store | `pkg/memory/sqlite_store.go`, `sqlite_schema.go`, `sqlite_migration.go` | Typed session/thread rows, transactions, schema validation, and legacy import. |
| Thread store | `pkg/threads/threads.go`, `pkg/threads/registry.go` | Thread projections and transactional create/update/attach/handoff operations. |
| Runtime integration | `pkg/agent/instance.go` | Opens the database before file tools and fails closed if storage cannot be validated. |

## Identity Model

`session.SessionScope` contains the routed agent, channel, account, ordered
dimensions, and one value per dimension. Supported dimensions are `space`,
`chat`, `topic`, and `sender`. Canonical keys use:

```text
sk_v1_<sha256>
```

Legacy `agent:...` keys remain aliases. Alias rows preserve order. Strict
resolution rejects ambiguous ownership; ordinary compatibility resolution uses
the established direct-key and deterministic-owner rules. Alias promotion
moves an eligible nonempty legacy session into an empty canonical owner in one
transaction and never overwrites nonempty canonical history.

## Database Layout

The authoritative file is:

```text
<workspace>/sessions/sessions.db
```

SQLite WAL and shared-memory companions may exist beside it. Important tables:

| Tables | State |
| --- | --- |
| `sessions`, `session_messages` | identity, summary, timestamps, version, and ordered messages |
| `session_scopes`, `session_scope_dimensions` | typed routed scope and ordered dimension/value pairs |
| `session_aliases` | ordered compatibility aliases |
| `threads`, `thread_context`, `thread_aliases` | thread identity, state, context, and aliases |
| `thread_sessions`, `session_thread_links` | ordered membership, exact primary ownership, and current attached link |
| `thread_handoffs` | origin/target relationship and handoff summary |
| `storage_imports`, `storage_import_issues` | source digest, safe counts, archive status, issue code, and record digest |

Message role/content/model/timestamps and tool-call identity are typed columns.
Only nested provider-neutral media, attachment, part, system-block, and tool-call
payloads use canonical JSON BLOBs. Prompt-runtime-only fields are rejected or
omitted according to the existing message contract.

## SQLite Contract

Every open enforces:

- private directories, `0600` database/WAL/SHM files;
- WAL, foreign keys, a bounded busy timeout, and `synchronous=FULL`;
- `PRAGMA user_version` migrations inside `BEGIN IMMEDIATE`;
- exact table/index definitions and rejection of unknown schema objects;
- row, byte, canonical-JSON, timestamp, sequence, primary-membership, and link
  reciprocity validation;
- integrity and foreign-key checks before use.

Versions newer than the binary, corrupt databases, and invalid schemas fail
closed. Runtime does not fall back to JSON.

## Mutation And Concurrency

All related changes share one immediate transaction:

- append/history replacement and session version update;
- scope plus ordered alias replacement;
- strict snapshot compare-and-swap;
- grouped session deletion with cascading thread relationships;
- thread creation/update, membership, and session link;
- attach plus handoff publication and best-effort continuation summary.

Rows carrying mutable aggregate state have a monotonic version. Updates use a
version fence where an operation spans a read/modify/write decision. Message
and relationship sequences are contiguous and deterministic. Multiple
processes coordinate through SQLite WAL and the busy timeout; mixed old/new
binaries are unsupported.

Strict snapshots return a graph-detached canonical key, history, summary,
scope, aliases, and transient revision. Replacement requires that exact
revision, or exact absence when the expected revision is empty. A stale token
returns `ErrSnapshotConflict` without publishing a partial tuple.

## Legacy Migration

On first open, the store deterministically examines legacy files below
`sessions/` and `threads/`, including aggregate JSON, metadata, JSONL/selected
history slots, delete manifests, thread registry records, and handoffs.

Migration order is sessions before threads and handoffs. Inputs are bounded and
read through hardened path checks. Invalid individual records are skipped with
safe issue codes and SHA-256 digests; unsafe roots, symlinks, modes,
enumeration/size failures, SQLite failures, or integrity failures abort the
transaction. Final per-source imported/skipped counts are written only after
conflict and dependency resolution, so the audit matches committed rows.

After commit, every examined source is moved without replacement to:

```text
<workspace>/legacy-json/sessions-v1/<original-relative-path>
```

Archives preserve permissions. An interrupted archive is retried on the next
open without re-import. A source whose digest changed after import is never
archived. SQLite becomes authoritative at commit; no dual writes or JSON
fallbacks occur.

Rollback requires stopping all PicoClaw processes, restoring the retained
archive layout, and removing or restoring `sessions.db` together with matching
`-wal` and `-shm` files.

## Runtime File Protection

Agent write/edit/append/apply-patch tools protect `sessions.db`, its WAL/SHM
companions, and the workspace `legacy-json` namespace. Protection is frozen into root and
owner tool factories and local-repair policy. Session storage opens before
apply-patch captures volatile roots, preventing database creation or archival
from invalidating ordinary source-file patches.

## Compatibility

- HTTP request/response shapes and session/thread IDs are unchanged.
- `memory.NewJSONLStore` and `session.NewJSONLBackend` remain deprecated
  source-compatible SQLite facades for one compatibility cycle.
- `session.NewSessionManager("")` remains non-persistent in-memory storage.
- `session.NewSessionManager(nonempty)` is a deprecated SQLite facade and
  fails closed on open errors.
- Legacy JSON/JSONL is read only by the bounded importer and retained archive;
  supported runtime paths never create mutable session or thread JSON.

## Verification

Primary coverage lives in:

- `pkg/memory/sqlite_store_test.go`
- `pkg/session/jsonl_backend_test.go`
- `pkg/threads/threads_test.go`
- `web/backend/api/session_test.go`
- `web/backend/api/thread_test.go`
- `pkg/agent/file_mutation_policy_test.go`

Tests cover schema/pragmas/modes, exact timestamps and nested payloads,
snapshot CAS, concurrent writers, full legacy audit/archive/idempotence,
malformed and unsafe inputs, relational corruption, thread transactions, HTTP
compatibility, and file-tool protection.
