# SQLite Runtime Storage Provider

## Feature ID

`FR-SQLITE`

## Behavior Summary

SQLite is PicoClaw's sole shipped database provider. It implements the
provider-neutral Database Layer contract inside the single-owner broker and is
the only production boundary allowed to open or manipulate physical SQLite
generations.

Application packages use typed domain clients and opaque logical store IDs.
They do not observe SQLite handles, paths, SQL, PRAGMAs, journal files, driver
errors, or migration machinery. Ordinary startup may initialize a missing empty
store at the current schema but never upgrades an existing store or imports
legacy state; those operations belong exclusively to the fenced offline
migrator.

## Reconstruction Notes

- Similarity target: recreate a broker-private SQLite provider with one durable
  connection pool per catalog-resolved physical store, exact current-schema
  validation, private generation handling, and an exclusive offline migration
  mode.
- Core types/functions: provider registration, trusted physical-store
  resolution, stable pool registry, current-schema initializer/validator,
  broker-side domain adapters, structured error mapper, generation snapshot and
  recovery helpers, migration transaction, and clean checkpoint/shutdown.
- Runtime ordering: validate catalog identity and existing generation metadata,
  reject unsafe sidecars before SQLite opens, open one pool, configure and
  verify every connection, initialize only a genuinely missing empty store or
  validate the exact current schema, then retain the pool until broker shutdown.
- Non-obvious constraints: live permission checks use non-opening metadata
  operations; no caller may independently open or close a live file; aliases
  fail before pool publication; migration uses exclusive locking and rollback
  journal before returning to WAL; matching WAL/SHM files are recovered through
  SQLite rather than manually removed; pre-open hardening identity-fences every
  primary/sidecar pathname and file handle while tolerating a transient
  companion disappearance only after a fresh absence inspection; a complete
  zero-source legacy enumeration still closes the shared import horizon;
  bounded Git-workspace inventory and
  individually audited candidate-checkpoint legacy inputs are imported exactly
  once and retained without exposing their contents in diagnostics; and backend
  errors never cross the domain API.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-SQLITE-001` | MUST | The broker opens a trusted catalog store that is missing and empty or already at the supported schema. | Every connection uses foreign keys, a five-second busy timeout, `synchronous=FULL`, and WAL; the owner-only directory and database generation remain private: directories and generation members are `0700`/`0600` on POSIX or carry a protected owner-only DACL on Windows. Pre-open hardening binds each initial pathname identity to its opened file handle, current pathname, and final private-file validation; once the pool is live, rechecks use non-opening metadata. | A genuinely missing empty store is initialized directly at the current schema, and one stable pool is registered for the broker epoch. | Empty/unknown IDs, caller paths or URIs, unsafe ancestors, symlinks, irregular files, unsafe or replaced pre-existing sidecars, a missing/replaced primary during hardening, an ambiguously vanished companion, configuration mismatch, or failed PRAGMA verification return a structured error before domain use. | Every physical store needs one secure and durable provider baseline. |
| `FR-SQLITE-002` | MUST | Ordinary startup inspects a current, outdated, legacy, too-new, malformed, corrupt, or unavailable generation. | A current generation whose exact tables, indexes, views, triggers, import ledger, and horizon objects validate becomes `ready`; outdated or legacy state returns `MigrationRequired`; integrity failure and unavailability retain their distinct provider-neutral readiness. | Ordinary startup performs no schema upgrade, legacy import, archive transition, recovery rewrite, or destructive cleanup. | Too-new, malformed, unrelated-schema-object, or corrupt state fails closed; no JSON/file fallback or automatic upgrade is attempted. | Storage changes require an exclusive backed-up maintenance boundary. |
| `FR-SQLITE-003` | MUST | Multiple catalog entries, clients, or runtime generations address SQLite state. | The provider publishes one pool per canonical physical store and reuses it for all broker-side domain operations until broker shutdown. | Runtime stop/restart changes no pool, journal mode, or physical generation. | Physical aliases and duplicate registrations fail closed; independent open/close, workflow idle teardown, and zero-idle pool policy are unsupported. | Connection-local settings and generation ownership remain sound only under one long-lived pool authority. |
| `FR-SQLITE-004` | MUST | The exclusive offline migrator upgrades a schema, imports legacy data, backs up, recovers, or archives a store. | It snapshots and fsyncs the whole matching generation and affected inputs, recovers through SQLite, checks integrity/foreign keys, deterministically imports bounded inputs by dependency order and relative identity, finalizes exact per-source accounting so provisional counts/issues cannot survive commit, runs any idempotent domain sealer, and closes the shared import horizon even after a complete zero-source enumeration, and commits schema plus import ledger atomically under exclusive rollback-journal mode. Exact imported inputs—including the aggregate Git-workspace inventory and individually audited candidate checkpoints—are archived without overwrite after commit; the provider then returns to WAL, checkpoints, cleanly reopens, and revalidates. | Only the migrator changes schema/import state; domain rows, payload-free safe issue codes/digests and final counts, the sealed horizon, and ledger commit atomically, while archive completion remains crash-recoverable. The required timestamped backup is retained on success or failure. | Missing exclusivity, unsafe or unbounded enumeration, incomplete accounting, snapshot/fsync failure, too-new/corrupt state, migration/seal failure, changed input, archive conflict, failed checkpoint/reopen, or validation mismatch leaves the store unready and the backup recoverable. | Provider-specific upgrade and recovery must preserve ordered relationships, become authoritative after a complete enumeration, and remain atomic, durable, and reversible. |
| `FR-SQLITE-005` | MUST | Production source needs a SQLite driver, physical DSN, PRAGMA, database/WAL/SHM/rollback-journal operation, schema codec, transaction, or provider diagnostic. | Only the broker's SQLite provider performs the physical operation; broker-side domain adapters own SQL and map every result to typed domain data and backend-neutral errors. | No application-facing object retains a raw handle, SQL callback, path, DSN, driver error, or provider control. | Static architecture tests reject `modernc.org/sqlite`, `sql.Open`, SQLite DSNs, PRAGMAs, and database-generation file operations outside the provider; the private Matrix/WhatsApp RPC driver is limited to logical IDs and does not open SQLite. | The SQLite implementation must remain replaceable and impossible to bypass. |
| `FR-SQLITE-006` | MUST | A clean integration runtime exercises every persistent subsystem, including Git-workspace inventory and PR-candidate checkpoints, and then starts the owners a second time. | The trusted provider catalog and exact private generation inventory are stable; every surviving JSON/JSONL source, retained archive, history slot, invalidation sidecar, or immutable artifact is explicitly allow-listed, and the second startup creates no additional candidate path. | The suite writes representative typed rows, reopens broker-owned stores, mutates inventory through `Manager.Acquire`/`Stats`, and imports and version-fences one checkpoint through offline migration. | Unexpected mutable candidates, unregistered SQLite generations, unsafe archive ancestry, missing/extra stores including inventory/checkpoint stores, non-private modes, payload-bearing diagnostics, or a changed second-start inventory fail the merge gate. | A subsystem must not reintroduce mutable JSON persistence or a second physical SQLite owner after focused tests pass. |

## Data And State Model

The broker catalog maps each opaque logical store ID to a provider-private
canonical physical identity. The SQLite provider alone knows the directory,
database filename, DSN, schema version, and database/WAL/SHM/rollback-journal generation members.
The pool registry is keyed by canonical physical identity and records the
logical owner, readiness, current broker epoch, open pool, and current-schema
adapter. A second identity resolving to the same physical object is invalid.
Git-workspace inventory and PR-candidate checkpoint state each have a trusted
logical catalog entry; their physical generations, locks, legacy inputs, and
retained archives remain provider-private even when a configured Git state root
is located inside an agent workspace.

Every persistent connection verifies foreign keys enabled, a 5,000 millisecond
busy timeout, `synchronous=FULL`, and WAL. The provider sets a bounded positive
open and idle connection policy; it does not use `SetMaxIdleConns(0)`, schedule
idle pool teardown, or close between domain operations. Pools drain only during
controlled broker shutdown or exclusive migration fencing.

Before the first SQLite open, the provider validates the store directory,
database endpoint, and any existing matching sidecars as owner-controlled,
regular, nonsymlinked artifacts. Database directories are owner-only and
database generation members are `0600` where platform semantics support modes.
After open, permission and identity rechecks use non-opening metadata operations
so validation never creates a second live SQLite handle. SQLite itself performs
recovery, checkpoint, journal transitions, and clean close; no code manually
deletes a live WAL or SHM file.

Schemas, SQL codecs, import ledgers, migration definitions, and transaction
implementations live in broker-side domain adapters. Bounded canonical JSON may
remain a typed column payload where a domain requires nested data, but no
generic whole-store document API crosses the broker. Provider errors are
classified into the Database Layer's structured errors and do not expose
driver text, extended SQLite codes, SQL, schema names, or paths.

Offline backup state lives below the migrator's selected backup parent in
`database-migrate-<UTC>/`. Its manifest records logical store ID, provider and
schema versions, all generation members and affected legacy inputs, hashes,
sizes, modes, timestamps, and outcome. The backup is never the provider's live
write target and is preserved whether migration succeeds or fails.

## Surface Ownership

Owns: CODE internal/sqliteprovider/**
Owns: CODE internal/sqlitestore/**
Owns: CODE internal/sqlbridge/**
Owns: CODE internal/storecatalog/**
Owns: TEST pkg/database/*
Owns: TEST internal/sqliteprovider/*
Owns: TEST internal/sqlitestore/*
Owns: TEST internal/sqlbridge/*
Owns: TEST internal/storecatalog/*
Owns: CODE integration/suites/storage-json/**
Owns: TEST pkg/gateway/runtime_storage_json_allowlist_integration_test.go TestIntegrationRuntimeOwnedJSONAllowlist
Owns: TEST pkg/gateway/runtime_storage_legacy_migration_integration_test.go TestIntegrationRuntimeOwnedJSONLegacyMigration
Owns: TEST pkg/gitworkspace/runtime_storage_legacy_relations_integration_test.go TestIntegrationRuntimeOwnedJSONLegacyGitInventoryRelations
Owns: INTEGRATION storage-json

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal provider API | SQLite provider registration and pool registry | Accept only broker-resolved catalog entries, open or initialize one current store, publish one stable pool, execute broker-side adapters, and close only during broker shutdown or migration. | `FR-SQLITE-001`, `FR-SQLITE-002`, `FR-SQLITE-003`, `FR-SQLITE-005` |
| Internal provider API | Current-schema initializer and validator | Create the complete current schema only for a missing empty store; otherwise classify current, migration-required, too-new, malformed, corrupt, or unavailable state without upgrading it. | `FR-SQLITE-001`, `FR-SQLITE-002` |
| Internal provider API | Offline snapshot, recovery, migration, and archive operations | Operate only under exclusive per-home fencing, require a complete durable backup, recover/check/migrate/archive/reopen in the prescribed order, and preserve diagnostic manifests without payload leakage. | `FR-SQLITE-004` |
| Internal adapter API | Typed domain operation against one logical store | Own schema-aware query/mutation code and transaction boundaries, accept typed inputs, and return typed outputs or provider-neutral errors. | `FR-SQLITE-005` |
| Internal adapter API | Git-workspace inventory logical store | Preserve typed repository/workspace inventory, ordered development-line and rotation evidence, histories, exact aggregate legacy accounting, and its sealed import horizon without exposing a physical location. | `FR-SQLITE-004`, `FR-SQLITE-005` |
| Internal adapter API | PR-candidate checkpoint logical store | Preserve typed mutable checkpoints and independently audited legacy checkpoint inputs with exact per-source ledger and archive outcomes without exposing a physical location. | `FR-SQLITE-004`, `FR-SQLITE-005` |
| Compatibility API | Private Matrix/WhatsApp RPC `database/sql/driver` | Carry only allow-listed logical store IDs to broker-side adapters; reject runtime DDL, mutating PRAGMAs, `ATTACH`, `DETACH`, and `VACUUM`; never import or open SQLite. | `FR-SQLITE-002`, `FR-SQLITE-005` |
| File | Provider-private database generation | Database and matching WAL/SHM/rollback-journal artifacts are visible only to the provider, migrator, backup/recovery workflow, and provider artifact catalog. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| Integration suite | `storage-json` | Exercise the catalog-owned store inventory, retained legacy/archive allow-list, provider permissions, inventory/checkpoint relationships, malicious near-miss candidates, and second-start stability. | `FR-SQLITE-006` |

## Algorithms And Ordering

### Ordinary open

1. Accept a broker catalog entry, not a path or DSN. Resolve its provider-private
   physical identity and reject duplicate or aliased registration.
2. Inspect the parent, endpoint, and existing sidecars without opening SQLite.
   Reject unsafe modes, links/reparse points, irregular objects, ownership, or
   identity drift. Bind each initial pathname to its opened hardening handle and
   the final pathname; require the primary throughout, and accept a companion
   unlinked during the pass only after fresh inspection proves it absent and
   the verified handle has been hardened before close.
3. Construct the private SQLite DSN, open exactly one pool, set bounded open and
   idle capacity, and configure/verify foreign keys, five-second busy timeout,
   FULL synchronous mode, and WAL on its connections.
4. If no generation and no legacy input exists, create the complete current
   domain schema. Otherwise inspect schema and integrity without applying a
   migration or import.
5. Classify readiness through provider-neutral status, publish only a `ready`
   pool, and retain it until broker shutdown. Close a rejected candidate cleanly
   without publishing it.

### Domain operation

1. Resolve the typed operation's declared logical store and current broker
   epoch; reject cross-store transaction requests.
2. Validate typed input before executing a broker-side schema adapter.
3. Execute the adapter's read or single-store atomic command through the stable
   pool. Provider SQL and transaction objects remain local to the adapter.
4. Verify bounded result shape, detach provider values, and return typed domain
   data or map the provider failure to the closed backend-neutral error set.

### Offline migration

1. Prove the supervisor/runtime/launcher are stopped, acquire the exclusive
   per-home migration fence, load the trusted catalog, and select logical IDs.
2. Snapshot and fsync the complete matching generation plus affected legacy
   inputs into the timestamped backup, then fsync its manifest before mutation.
3. Open through SQLite and let it recover or roll back hot state. Run integrity
   and foreign-key checks; never remove sidecars manually.
4. Enter exclusive locking and rollback-journal mode. In one all-or-nothing
   transaction apply contiguous schema work, deterministic bounded legacy
   import, exact accounting, schema version, and import ledger.
   Third-party channel upgraders with internal commits operate only on a
   provider-created SQLite-backup stage, which is validated and atomically
   renamed over the original generation after all upgrade work succeeds.
5. After commit, archive only exact digest-verified inputs without overwrite.
   Record recoverable archive progress so a crash never repeats an import.
6. Restore WAL, checkpoint through SQLite, close cleanly, reopen through the
   ordinary provider path, and repeat schema, integrity, foreign-key,
   permission, and physical-identity checks.
7. Retain the complete backup and outcome manifest, close migration handles,
   and release exclusivity only after durable cleanup.

## Cross-Feature Behavior

The Database Layer owns broker discovery, supervision, logical IDs, readiness,
IPC, retries, structured errors, and migration commands. This feature owns the
physical SQLite implementation. Domain features own their models, validation,
and operation semantics; their SQL codecs and atomic implementations execute as
broker-side adapters rather than application APIs.

Launcher authentication, global auth, model catalogs, tool adaptation,
workflows, sessions/threads, eventing, cron, runtime state, account routing,
reviews/evaluations, evolution, local-CI cache, Seahorse, WeCom, Weixin, Matrix,
WhatsApp, Git-workspace inventory, and PR-candidate checkpoints all consume
catalog IDs. Agent mutation protection asks the provider catalog for protected
artifacts—including inventory/checkpoint legacy inputs, locks, and retained
archives—and never reconstructs SQLite filenames. Matrix and WhatsApp retain
only the temporary private RPC SQL bridge; Seahorse uses a normal typed client.

The provider projection also seeds the Agent feature's generation-wide
physical-file identity catalog for the configured/default workspace and every
named-agent workspace. Pinned, bounded, two-pass enumeration retains a
streaming digest followed by one deduplicated identity set; root and owner
tools plus controller local repair share that immutable catalog. Reads and
read-before-write operations reauthorize the actual opened handle before
consuming bytes or names, so a hardlink alias, source-to-archive rename, unsafe
entry, or changing snapshot cannot turn provider state into model-editable
content. Git-inventory and checkpoint inputs use their stricter source/depth
bounds, and reload retains earlier store-generation roots while adding the
current catalog projection.

## Failure And Edge Cases

- An existing legacy source adjacent to a missing or empty database is not a
  fresh initialization; ordinary startup returns `MigrationRequired`.
- Legacy enumeration is bounded per source, in aggregate, and by source count.
  Missing inputs still produce a complete seal decision; enumeration or seal
  failure aborts migration. A source appearing after the sealed horizon is
  audited as SQLite-authoritative rather than imported at startup.
- A domain importer may skip malformed records only when it explicitly assigns
  bounded safe counts and issue codes/digests. Structural filesystem/provider
  failures and incomplete or extra final accounting abort the transaction.
- Duplicate canonical inventory identities use the domain's documented
  deterministic winner and record later conflicts as safe issues. Candidate
  checkpoint inputs retain an independent exact ledger outcome per source.
- An archive destination is never overwritten. A digest-matching partial
  transition resumes without re-import; changed bytes or a conflicting archive
  fail closed.
- A second runtime startup neither repeats a committed import nor creates a
  mutable JSON fallback or another physical generation candidate.
- A store at a future version, with unknown schema objects, failed integrity or
  foreign keys, invalid canonical data, or an unsafe physical generation never
  becomes ready and never falls back to JSON.
- Existing WAL/SHM/rollback-journal artifacts are validated before open. Hot state is recovered
  through SQLite; manual sidecar deletion, replacement, or separate inspection
  connection is forbidden.
- A pool candidate is not published until configuration and current-schema
  validation succeed. Runtime child restart neither closes nor reconfigures an
  already published pool.
- Physical alias detection includes canonical path, symlink, case behavior,
  hard-link identity, resolved existing ancestors, and duplicate dynamic
  catalog entries. Ambiguity fails closed.
- Busy, locked, cancellation, constraint, corruption, I/O, and driver failures
  map to backend-neutral errors. A possibly committed mutation becomes
  `OutcomeUnknown` rather than an optimistic retry.
- Backup, snapshot, fsync, recovery, integrity, migration, archive, checkpoint,
  clean-close, or reopen failure preserves backup material and leaves readiness
  non-ready.
- The private compatibility driver cannot broaden its allow-list, carry a path,
  run DDL in runtime mode, or become a general application SQL API.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SQLITE-001`, `FR-SQLITE-002` | `pkg/database/server_unix_test.go`, `pkg/database/catalog/catalog_test.go` |
| `FR-SQLITE-003` | `pkg/database/server_unix_test.go`, `pkg/database/supervisor_test.go` |
| `FR-SQLITE-004` | `pkg/database/migration/migration_test.go`, `cmd/picoclaw/internal/database/command_test.go` |
| `FR-SQLITE-004`, `FR-SQLITE-005` | [pkg/gitworkspace/inventory_sqlite_test.go](../../pkg/gitworkspace/inventory_sqlite_test.go), [pkg/gateway/pr_workspace_candidate_checkpoint_test.go](../../pkg/gateway/pr_workspace_candidate_checkpoint_test.go) |
| `FR-SQLITE-005` | `pkg/database/architecture_test.go`, `pkg/database/public_api_test.go`, `pkg/database/protocol_test.go` |
| `FR-SQLITE-001` through `FR-SQLITE-005` | `pkg/database/server_unix_test.go`, `pkg/database/migration/migration_test.go` |
| `FR-SQLITE-001`, `FR-SQLITE-002`, `FR-SQLITE-004` | [internal/sqlitestore/open_test.go](../../internal/sqlitestore/open_test.go), [internal/sqlitestore/hardening_test.go](../../internal/sqlitestore/hardening_test.go), [internal/sqlitestore/legacy_finalize_results_test.go](../../internal/sqlitestore/legacy_finalize_results_test.go) |
| `FR-SQLITE-006` | [pkg/gateway/runtime_storage_json_allowlist_integration_test.go](../../pkg/gateway/runtime_storage_json_allowlist_integration_test.go), [pkg/gateway/runtime_storage_legacy_migration_integration_test.go](../../pkg/gateway/runtime_storage_legacy_migration_integration_test.go), [pkg/gitworkspace/runtime_storage_legacy_relations_integration_test.go](../../pkg/gitworkspace/runtime_storage_legacy_relations_integration_test.go), [integration/suites/storage-json](../../integration/suites/storage-json) |

## Implementation Anchors

- `internal/sqliteprovider/provider.go`
- `internal/sqliteprovider/maintenance.go`
- `internal/sqliteprovider/staged_migration.go`
- `internal/sqlitestore/open.go`
- `internal/sqlitestore/legacy.go`
- `pkg/database/catalog/catalog.go`
- `pkg/database/migration/migration.go`
- `pkg/database/architecture_test.go`
- `pkg/gitworkspace/inventory_sqlite.go`
- `pkg/gateway/pr_workspace_candidate_checkpoint.go`
- `pkg/gateway/runtime_storage_json_allowlist_integration_test.go`
- `integration/suites/storage-json`
- [Database-Agnostic Single-Owner Layer](database-layer.md)
