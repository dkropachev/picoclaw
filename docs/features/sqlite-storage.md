# SQLite Runtime Storage

## Feature ID

`FR-SQLITE`

## Behavior Summary

PicoClaw-owned mutable runtime state is stored in subsystem-local SQLite
databases. Every database uses the same durability, schema, migration, and
legacy-archive contract while each subsystem retains typed ownership of its
domain rows.

Human-authored configuration, portable immutable artifacts, external formats,
and the small recovery journals that must operate before a database opens remain
file-backed.

## Reconstruction Notes

- Similarity target: recreate a small shared SQLite boundary that prepares a
  private filesystem location, configures durable connection-local behavior,
  upgrades and validates an owned schema, and imports bounded legacy sources
  exactly once.
- Core types/functions: `sqlitestore.Open`, `Options`, `Migration`,
  `LegacyOptions`, `LegacySource`, `LegacyImporter`, `LegacyResultFinalizer`,
  `LegacySealer`, and `sqlitestore.Immediate`.
- Runtime ordering: validate the path and migration catalog, securely prepare
  the directory and database, enable and verify PRAGMAs, reject corruption or a
  future schema, run upgrades and legacy import in `BEGIN IMMEDIATE`, validate
  the exact retained schema, commit, then archive verified legacy sources.
- Non-obvious constraints: import diagnostics never retain payloads or secrets;
  an archive transition is resumable after either side of its filesystem move;
  a changed committed source is never archived; database, WAL, and SHM files are
  private; and arbitrary JSON may be a bounded column payload but not a generic
  whole-store document table.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-SQLITE-001` | MUST | A subsystem opens a mutable database at a filesystem path. | The returned handle uses WAL, foreign keys, a five-second busy timeout, and `synchronous=FULL`; the parent is private and the database and companions are `0600` on POSIX hosts or carry a protected owner-only DACL on Windows. | A missing directory/database is securely created. | Empty, URI, NUL-bearing, symlinked/reparse, irregular, publicly writable/accessible, or otherwise unsafe boundaries fail before domain use. | Every store needs one durable and secure baseline. |
| `FR-SQLITE-002` | MUST | The database schema is older, current, too new, malformed, or corrupt. | Contiguous migrations reach the supported `PRAGMA user_version` and the retained schema validates exactly. | Migrations run in one explicit `BEGIN IMMEDIATE` transaction. | Failure rolls back; a future version, invalid schema, or failed integrity check returns a typed error and never falls back to JSON. | Mixed schemas and partial upgrades must fail closed. |
| `FR-SQLITE-003` | MUST | A subsystem performs its first complete bounded legacy JSON/JSONL enumeration, including an enumeration with no present source. | Valid records are imported deterministically by dependency order and relative path; selected malformed records are skipped with counts and safe issue codes/digests only. Aggregate/dependency importers may resolve relationships in `LegacyResultFinalizer`; their returned per-source outcomes atomically replace provisional counts and issues before commit. An optional idempotent `LegacySealer` then durably closes the subsystem's import horizon before validation. | Domain rows, final durable import/issue rows, and the subsystem import-horizon marker commit in the same immediate transaction. A source first appearing after that marker is audited as SQLite-authoritative rather than imported. | Unsafe enumeration, symlinks or modes, size/count bounds, incomplete/extra/invalid final accounting, SQLite errors, importer errors, or seal failure abort without an import commit or closed marker. | Automatic upgrade must preserve valid state and relationships, become authoritative even after an empty first open, and make the audit describe committed rows without exposing secrets. |
| `FR-SQLITE-004` | MUST | A committed import has an unarchived legacy source. | The exact imported bytes move to `legacy-json/<component>-v1/` without overwriting an existing archive and with their permissions retained. | Archive completion is durably recorded after the filesystem transition. | A crash before/after the move is retried without re-import; changed bytes or a conflicting archive fail closed. | SQLite becomes authoritative immediately while rollback material remains recoverable. |
| `FR-SQLITE-005` | MUST | Concurrent PicoClaw processes mutate a subsystem store. | Bounded lock waits and immediate write transactions serialize domain operations; version-fenced owners can reject stale updates. | Only the committed SQLite transaction becomes visible. | Busy, canceled, and stale-version operations return errors without partial domain state or JSON dual writes. | CLI, launcher, and gateway processes must share one authority. |
| `FR-SQLITE-006` | MUST | A clean integration runtime exercises persistent subsystems and then starts their owners a second time. | The exact expected private database inventory is present; every surviving JSON, JSONL, migrated snapshot, history slot, or invalidation sidecar matches an intentional configuration, recovery, exact component archive, sidecar, or immutable-artifact path; and the second startup creates no additional candidate path. | The test writes representative typed rows and immutable evidence, mutates and reopens Git workspace inventory through `Manager.Acquire`/`Stats`, and imports, version-fenced updates, and reopens a PR candidate checkpoint. Deliberate non-allowlisted JSON, exact Git/checkpoint archive-label near misses, JSON-like directory, unsafe archive-link, and SQLite-extension canaries are rejected without reading or reporting their payloads. | An unexpected candidate file/directory, unregistered `.db`/`.sqlite`/`.sqlite3` path, unsafe archive ancestry, missing/extra database (including `inventory.db` or `checkpoints.db`), non-private database mode, traversal failure, or changed second-start inventory fails the merge-gating suite. | A subsystem must not silently reintroduce mutable JSON persistence after its focused tests pass. |

Shared helpers `RequireOneRow` and `ScanStrings` retain exact driver errors and
provide bounded typed row/result validation without turning subsystem schemas
into generic document stores.

## Data And State Model

Every database owns typed subsystem tables and `PRAGMA user_version`. The shared
import ledger stores component and relative source identities, SHA-256 digests,
bounded source sizes, imported/skipped counts, archive status, timestamps, and
issue codes plus record digests. It never stores an original rejected payload,
credential, token, or diagnostic string derived from one.

Database paths are ordinary filesystem paths rather than caller-provided SQLite
URIs. The database directory is owner-only. SQLite `-wal` and `-shm` companions
share the database's private mode. Archives remain indefinitely and are not a
second write target.

All PicoClaw processes sharing a home or workspace must stop and upgrade
together. Rollback requires stopping them again, restoring retained archives to
their original relative paths, and removing or restoring each database with its
matching WAL, SHM, and lock directory as one generation. Mixed old/new binaries
against one storage root are unsupported.

The workflow subsystem uses `workspace/state/workflows.db`. It normalizes runs,
ordered events, ancestry links, job/step executions, human tasks, native state,
compatibility stamps/issues, and active or archived development sessions.
Canonical number-aware JSON BLOBs carry only nested payloads and private
continuations. Legacy runs/events import before dependent records; native
state, validation manifests, and development snapshots follow. Filesystem
publish/template journals remain allowed because they recover workflow
definition replacement when database state is unavailable.

## Surface Ownership

Owns: CODE pkg/sqlitestore/**
Owns: CODE integration/suites/storage-json/**
Owns: TEST pkg/sqlitestore/*
Owns: TEST pkg/gateway/runtime_storage_json_allowlist_integration_test.go TestIntegrationRuntimeOwnedJSONAllowlist
Owns: TEST pkg/gateway/runtime_storage_legacy_migration_integration_test.go TestIntegrationRuntimeOwnedJSONLegacyMigration
Owns: TEST pkg/gitworkspace/runtime_storage_legacy_relations_integration_test.go TestIntegrationRuntimeOwnedJSONLegacyGitInventoryRelations
Owns: INTEGRATION storage-json

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Go API | `sqlitestore.Open(ctx, path, options)` | Opens, configures, integrity-checks, migrates, validates, and archives one subsystem database or returns an error without a JSON fallback. | `FR-SQLITE-001`, `FR-SQLITE-002`, `FR-SQLITE-003`, `FR-SQLITE-004` |
| Go API | `sqlitestore.Immediate(ctx, db, callback)` | Runs one callback between explicit `BEGIN IMMEDIATE` and `COMMIT`, rolling back on callback, context, or commit failure. | `FR-SQLITE-002`, `FR-SQLITE-005` |
| Go API | `LegacyOptions.FinalizeResults` / `LegacyResultFinalizer` | Resolve ordered multi-source relationships and return exact final `ImportResult` for every newly imported source; the helper replaces provisional ledger counts/issues inside the import transaction. | `FR-SQLITE-003` |
| Go API | `LegacyOptions.Seal` / `LegacySealer` | Idempotently closes a subsystem import horizon after every successful deterministic enumeration, including zero-source opens, inside the migration transaction and before validation. | `FR-SQLITE-003` |
| File | `<root>/*.db`, `<root>/*.db-wal`, `<root>/*.db-shm` | Private mutable SQLite authority owned by its subsystem. | `FR-SQLITE-001`, `FR-SQLITE-005` |
| File | `<root>/legacy-json/<component>-v1/**` | Immutable retained legacy bytes, created once after their import transaction commits. | `FR-SQLITE-003`, `FR-SQLITE-004` |
| File | `<PICOCLAW_HOME>/auth.db`, `auth.db.locks/`; `legacy-json/auth-v1/auth.json` | Typed, version-fenced credential authority, protected cross-process refresh locks, and retained legacy source. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<PICOCLAW_HOME>/launcher-auth.db`; `legacy-json/launcher-auth-v1/launcher-config.json` | Typed launcher password/token authority and the retained pre-redaction launcher settings source; active `launcher-config.json` remains settings-only. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<PICOCLAW_HOME>/model-catalogs.db`; `legacy-json/model-catalogs-v1/model_catalogs.json` | Typed catalogs and ordered model children with bounded canonical JSON metadata BLOBs. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<PICOCLAW_HOME>/tool-adaptation.db`; `legacy-json/tool-adaptation-v1/tool_adaptation_state.json` | Typed observations and outcome counters with timestamp/version fences and retained legacy source. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `$PICOCLAW_HOME/channels/wecom/reqid-store.db` | Typed WeCom request-route identities, chat types, expiry timestamps, and row versions. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `$PICOCLAW_HOME/channels/weixin/state.db` | Typed Weixin account, cursor, and ordered context-token relationships with timestamps and row versions. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/sessions/sessions.db`; `<workspace>/legacy-json/sessions-v1/**` | Ordered messages, aliases, snapshots, metadata/delete manifests, threads, thread-session links, and handoffs with transactional relationship updates and retained legacy sources. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/state/runtime.db` | Typed singleton last-channel/chat state with field-specific version-fenced updates. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/cron/jobs.db`; `cron/legacy-json/cron-jobs-v1/jobs.json` | Typed ordered cron definitions/execution state and retained legacy source shared by CLI and gateway. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/state/account-router.db`; `state/legacy-json/account-router-v1/**` | Typed router/account/session/affinity/cursor/invalidation state with transactional cross-process updates and retained legacy state/sidecars. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/state/workflows.db`; `<workspace>/legacy-json/workflows-v1/**` | Typed runs, ordered events and links, execution snapshots, private continuation state, native/compatibility state, and development sessions with exact-number JSON BLOBs only where nested payloads require them. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/repository_reviews/repository-reviews.db`; `repository_reviews/legacy-json/repository-reviews-v1/**` | Typed repository review identities, versions, summaries, profiles, automations, ordered relationships, and bounded nested review evidence. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/repository_evaluations/evaluations.db`; `repository_evaluations/legacy-json/repository-evaluations-v1/**` | Typed evaluation identities, versions, lifecycle, progress, ordered model/run relationships, and bounded nested corpus/comparison payloads. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<evolution-state-dir>/evolution.db`; `legacy-json/evolution-v1/**` | Typed learning/pattern records, ordered evidence, skill drafts, profiles, version history, and retained JSON/JSONL migration sources. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<event-state>/pr-workspace-local-ci/evidence/cache.db`; `legacy-json/local-ci-cache-v1/cache/**` | Typed, version-fenced passing-result cache rows and retained legacy cache indexes; immutable plans, executions, attestations, and discovery records remain content-addressed JSON evidence. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/.git-workspaces/inventory.db`; `legacy-json/git-workspaces-v1/inventory.json` | Typed repository/workspace inventory, ordered development-line and rotation evidence, histories, and retained legacy aggregate. | `FR-SQLITE-001` through `FR-SQLITE-005` |
| File | `<workspace>/.git-workspaces/.pr-workspace-implementation/active/checkpoints.db`; `legacy-json/pr-workspace-checkpoints-v1/*.json` | Typed mutable candidate checkpoints and retained individually audited legacy checkpoint files. | `FR-SQLITE-001` through `FR-SQLITE-005` |

## Algorithms And Ordering

1. Reject invalid component names, migration catalogs, paths, timeouts, and
   connection bounds before opening SQLite.
2. Create and inspect the final database directory and file without accepting a
   symlink or irregular endpoint; set private permissions.
3. Build the internal filesystem URI and configure every pooled connection with
   foreign keys, the bounded busy timeout, and FULL synchronization. Select and
   verify WAL for file-backed stores.
4. Check existing integrity, acquire one connection, and enter
   `BEGIN IMMEDIATE`. Read `user_version`, reject a future version, apply each
   contiguous schema/data migration, enumerate and import deterministic legacy
   inputs, finalize aggregate relationships/accounting when configured, seal
   the subsystem import horizon when configured, validate domain and import
   schemas, and commit once.
5. Recheck integrity and permissions. For each pending import, revalidate the
   recorded relative identity and digest, complete or recover its no-overwrite
   archive transition, and mark the ledger row complete in an immediate
   transaction.

## Cross-Feature Behavior

Each owning subsystem defines its own relational schema, normalization rules,
version fences, and compatibility constructors. Workspace protection treats
database directories, database/WAL/SHM files, and legacy archives as
runtime-owned; model-facing mutation tools must enforce frozen lexical,
resolved, and exact-file-alias exclusions even when those paths fall inside a
workspace or an outside-write allowlist. Dynamic legacy and archive trees from
the configured/default workspace and every named-agent workspace use one
generation-wide immutable physical-identity catalog. Its first pinned,
batched pass retains only a streaming digest; its second pass retains the final
deduplicated identity set. Root/owner tools and local repair share that pointer,
so a source-to-archive rename cannot make a pre-existing hardlink alias
writable. Reads, listings, edit/append read-before-write, and apply-patch source
reads authorize the actual opened handle with `fstat`/`SameFile` and the catalog
before consuming bytes or names. Unsafe tree entries, aggregate entry/path/depth
bounds, or a changing two-pass snapshot fail agent construction without
disclosing a protected path. Git-inventory and PR-checkpoint inputs participate
in the same catalog; checkpoints also use pinned private-mode snapshots before
and after catalog construction under their tighter 10,000-source/depth-4
bounds. Each reload adds the current configured Git-state roots while retaining
the prior generation's frozen roots. Portability excludes
targets on which the required SQLite implementation is unsupported instead of
selecting a mutable JSON fallback.

## Failure And Edge Cases

- Missing legacy sources invoke no importer; a subsystem sealer may still close
  the first-open import horizon. Enumeration or seal errors are not no-ops.
- Inputs are bounded per source, in aggregate, and by source count.
- Malformed records may be skipped only when the domain importer explicitly
  classifies them; structural filesystem or SQLite failures abort.
- Duplicate canonical identities use a subsystem's documented deterministic
  winner and record later conflicts as safe issues.
- An archive destination is never overwritten. Matching partial transition
  states resume; mismatched bytes fail.
- A second startup neither imports again nor creates a mutable JSON file.
- An in-memory database is non-persistent and isolated from every other open.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SQLITE-001`, `FR-SQLITE-002`, `FR-SQLITE-005` | [pkg/sqlitestore/open_test.go](../../pkg/sqlitestore/open_test.go) |
| `FR-SQLITE-003`, `FR-SQLITE-004` | [pkg/sqlitestore/open_test.go](../../pkg/sqlitestore/open_test.go), [pkg/sqlitestore/legacy_finalize_results_test.go](../../pkg/sqlitestore/legacy_finalize_results_test.go), [pkg/memory/sqlite_store_test.go](../../pkg/memory/sqlite_store_test.go) |
| `FR-SQLITE-003`, `FR-SQLITE-004`, `FR-SQLITE-006` | [pkg/gateway/runtime_storage_legacy_migration_integration_test.go](../../pkg/gateway/runtime_storage_legacy_migration_integration_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/auth/store_sqlite_test.go](../../pkg/auth/store_sqlite_test.go), [web/backend/api/model_catalog_sqlite_test.go](../../web/backend/api/model_catalog_sqlite_test.go), [pkg/tools/adaptation_state_sqlite_test.go](../../pkg/tools/adaptation_state_sqlite_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/state/state_test.go](../../pkg/state/state_test.go), [pkg/channels/wecom/reqid_store_test.go](../../pkg/channels/wecom/reqid_store_test.go), [pkg/channels/weixin/state_sqlite_test.go](../../pkg/channels/weixin/state_sqlite_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/memory/sqlite_store_test.go](../../pkg/memory/sqlite_store_test.go), [pkg/workflows/sqlite_store_test.go](../../pkg/workflows/sqlite_store_test.go), [pkg/cron/sqlite_test.go](../../pkg/cron/sqlite_test.go), [pkg/accountrouter/store_sqlite_test.go](../../pkg/accountrouter/store_sqlite_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/repoaudit/sqlite_test.go](../../pkg/repoaudit/sqlite_test.go), [pkg/repoeval/sqlite_test.go](../../pkg/repoeval/sqlite_test.go), [web/backend/dashboardauth/store_test.go](../../web/backend/dashboardauth/store_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/evolution/sqlite_store_test.go](../../pkg/evolution/sqlite_store_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/prworkspace/localci/store_cache_sqlite_test.go](../../pkg/prworkspace/localci/store_cache_sqlite_test.go) |
| `FR-SQLITE-001` through `FR-SQLITE-005` | [pkg/gitworkspace/inventory_sqlite_test.go](../../pkg/gitworkspace/inventory_sqlite_test.go), [pkg/gateway/pr_workspace_candidate_checkpoint_test.go](../../pkg/gateway/pr_workspace_candidate_checkpoint_test.go) |
| `FR-SQLITE-006` | [pkg/gateway/runtime_storage_json_allowlist_integration_test.go](../../pkg/gateway/runtime_storage_json_allowlist_integration_test.go), [integration/suites/storage-json](../../integration/suites/storage-json) |

## Implementation Anchors

- [pkg/sqlitestore/open.go](../../pkg/sqlitestore/open.go)
- [pkg/sqlitestore/legacy.go](../../pkg/sqlitestore/legacy.go)
- [pkg/sqlitestore/open_test.go](../../pkg/sqlitestore/open_test.go)
- [pkg/state/state_sqlite.go](../../pkg/state/state_sqlite.go)
- [pkg/channels/wecom/reqid_store.go](../../pkg/channels/wecom/reqid_store.go)
- [pkg/channels/weixin/state_sqlite.go](../../pkg/channels/weixin/state_sqlite.go)
- [pkg/memory/sqlite_store.go](../../pkg/memory/sqlite_store.go)
- [pkg/workflows/sqlite_store.go](../../pkg/workflows/sqlite_store.go)
- [pkg/repoaudit/sqlite.go](../../pkg/repoaudit/sqlite.go)
- [pkg/repoeval/sqlite.go](../../pkg/repoeval/sqlite.go)
- [pkg/gateway/runtime_storage_json_allowlist_integration_test.go](../../pkg/gateway/runtime_storage_json_allowlist_integration_test.go)
- [integration/suites/storage-json](../../integration/suites/storage-json)
