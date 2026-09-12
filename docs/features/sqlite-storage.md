# SQLite Runtime Storage

## Feature ID

`FR-SQLITE`

## Behavior Summary

PicoClaw-owned mutable runtime state is stored in subsystem-local SQLite
databases. Every database uses the same durability, schema, migration, and
legacy-closeout contract while each subsystem retains typed ownership of its
domain rows.

The internal compatibility store now delegates driver binding, DSN creation,
generation security, control queries, and live/offline configuration to the
shared SQLite provider. Existing subsystem adapters still open these stores
directly; this refactor does not route them through broker IPC or perform a
production cutover. Existing callers retain archive-and-remove closeout by
default. Deferred closeout also supports one explicit offline-only
`sealed-absent` source-root policy: it can close an empty import horizon without
inventing a directory in the sealed disposable-input namespace. No production
adapter selects that policy yet, so runtime ownership remains unchanged.

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
  `LegacySealer`, `LegacyCloseoutPolicy`, `LegacySourceRootPolicy`, and
  `sqlitestore.Immediate`, backed by the internal provider.
- Runtime ordering: validate the path and migration catalog, securely prepare
  the directory and database, enable and verify PRAGMAs, reject corruption or a
  future schema, run upgrades and legacy import in `BEGIN IMMEDIATE`, validate
  the exact retained schema, commit, then either archive verified legacy
  sources by default or leave explicitly deferred sealed sources untouched. An
  explicit absent-root proof is captured before SQLite opens, marked by exactly
  one empty enumeration, revalidated after every schema/import callback and as
  the connection-free final proof immediately before the owner commit, and
  released only after the transaction ends.
- Transaction ownership: `Immediate` installs a provider-owned commit/rollback
  boundary before `BEGIN`. Trusted synchronous callbacks can query and mutate
  but cannot end the owner transaction with SQL; the provider authorizes and
  confirms exactly one final commit and removes hooks before rollback/release.
- Non-obvious constraints: import diagnostics never retain payloads or secrets;
  an archive transition is resumable after either side of its filesystem move;
  a changed committed source is never archived; explicit deferred closeout
  leaves every source unchanged with truthful pending archive state; an absent
  deferred root is bound to a retained owner-private directory handle and a
  complete missing suffix using no-follow/reparse-aware component traversal;
  the primary
  database remains present and private while transient WAL/SHM companions are
  identity-fenced and hardened without opening or closing their inodes so
  SQLite's process-scoped locks remain intact across concurrent processes; and
  arbitrary JSON may be a bounded column payload but not a generic whole-store
  document table.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-SQLITE-001` | MUST | A subsystem opens a mutable database at a filesystem path. | The returned handle uses WAL, foreign keys, a five-second busy timeout, and `synchronous=FULL`; the parent is private and the database and present companions are `0600` on POSIX hosts or carry a protected owner-only DACL on Windows. On Unix, every hardening pass binds each initial pathname identity to a retained owner-private parent descriptor, narrows only SQLite-compatible legacy modes with parent-relative `chmod`, and requires the same final private-file identity without opening or closing the generation member. The chmod is no-follow where supported; otherwise it follows repeated no-follow parent/child proofs under the protected parent. Windows uses reparse-aware member handles. | A missing directory/database is securely created; compatible legacy file modes may be narrowed. | Empty, URI, NUL-bearing, symlinked/reparse, irregular, non-tightenable, replaced, or otherwise unsafe boundaries fail before domain use. The primary database must remain present at every hardening stage; transient WAL/SHM companions may request only a bounded whole-generation retry when they disappear or change, while a stable dangling link, foreign owner, hardlink, non-not-found error, or hardening failure remains fatal. | Every store needs one durable and secure baseline without surrendering SQLite's lock authority. |
| `FR-SQLITE-002` | MUST | The database schema is older, current, too new, malformed, or corrupt. | Contiguous migrations reach the supported `PRAGMA user_version` and the retained schema validates exactly. The provider-owned transaction boundary denies callback `COMMIT`/`END`, records rollback, and admits exactly one owner commit. | Migrations run in one explicit `BEGIN IMMEDIATE` transaction during existing runtime use, or `BEGIN EXCLUSIVE` on a single-connection pool in provider-owned rollback-journal mode when the call context carries a live migration-fence capability for that exact path. Commit authorization, execution, confirmation, and hook removal share one raw driver lock. | Failure rolls back; a future version, invalid schema, failed integrity check, callback transaction termination, expired capability, or capability for another path returns an error and never falls back to JSON. Violated or cleanup-ambiguous physical connections are discarded after hooks are removed. | Mixed schemas and partial upgrades must fail closed without a process-global migration mode or callback-controlled commit. |
| `FR-SQLITE-003` | MUST | A subsystem performs its first complete bounded legacy JSON/JSONL enumeration, including an enumeration with no present source. | Valid records are imported deterministically by dependency order and relative path; selected malformed records are skipped with counts and safe issue codes/digests only. Aggregate/dependency importers may resolve relationships in `LegacyResultFinalizer`; their returned per-source outcomes atomically replace provisional counts and issues before commit. An optional idempotent `LegacySealer` may perform subsystem-specific closeout or validation; independently, the shared `storage_import_horizons` row always closes the generic import horizon before validation. Under the explicit sealed-absent policy, `Sources` runs exactly once and must return zero entries; the importer is never called. | Domain rows, final durable import/issue rows, and the subsystem import-horizon marker commit in the same immediate transaction. A source first appearing after that marker is audited as SQLite-authoritative rather than imported. Explicit deferred closeout changes no source and does not weaken this atomic destination commit. Sealed-absent mode performs no filesystem mutation on the source root or ancestors. | Unsafe enumeration, symlinks or modes, size/count bounds, invalid closeout/root-policy or policy/archive-root combination, incomplete/extra/invalid final accounting, SQLite errors, importer errors, or seal failure abort without an import commit or closed marker. Sealed-absent mode also rejects a nonempty enumeration, root appearance, ancestor identity/type/privacy drift, ambiguous inspection, cancellation, and an unused/reused proof. | Automatic upgrade must preserve valid state and relationships, become authoritative even after an empty first open, and make the audit describe committed rows without exposing secrets or fabricating legacy state. |
| `FR-SQLITE-004` | MUST | A committed import has a legacy source whose archive status is pending. | The default archive closeout moves the exact imported bytes to `legacy-json/<component>-v1/` without overwrite and retains their permissions. Explicit deferred closeout instead performs no write-side operation on the source and truthfully leaves `archive_status='pending'`; it is intended for engine-supplied sealed disposable roots whose owner verifies the post-callback seal. | Default archive completion is durably recorded after the filesystem transition. Deferred closeout neither mutates the source nor marks it archived or itself schedules cleanup; a later separately authorized owner may explicitly resolve the still-pending live artifact. | A crash before/after a default move is retried without re-import; changed bytes or a conflicting archive fail closed. Deferred closeout rejects a nonempty archive root, while its exact-target/namespace checks, source proofs, and outer prepared-input seal reject aliases or drift without source cleanup or a partial installed destination. | SQLite becomes authoritative immediately while normal rollback material remains recoverable and sealed migration inputs remain immutable. |
| `FR-SQLITE-005` | MUST | Concurrent PicoClaw processes mutate a subsystem store. | Bounded lock waits and immediate write transactions serialize domain operations; version-fenced owners can reject stale updates. A live exact-path migration capability instead limits only that opened pool to one connection and uses an exclusive transaction. A driver-lock barrier detects already-started callback transaction termination before owner commit. | Only the owner-authorized committed SQLite transaction becomes visible. | Busy, canceled, stale-version, expired-capability, wrong-target, callback rollback, unauthorized commit, and concurrent/reused boundary operations return errors without partial domain state or JSON dual writes. | CLI, launcher, and gateway processes must share one authority without unrelated in-process stores inheriting offline mode. |
| `FR-SQLITE-006` | MUST | A clean integration runtime exercises persistent subsystems and then starts their owners a second time. | The exact expected private database inventory is present; every surviving JSON, JSONL, migrated snapshot, history slot, or invalidation sidecar matches an intentional configuration, recovery, exact component archive, sidecar, or immutable-artifact path; and the second startup creates no additional candidate path. | The test writes representative typed rows and immutable evidence, mutates and reopens Git workspace inventory through `Manager.Acquire`/`Stats`, and imports, version-fenced updates, and reopens a PR candidate checkpoint. Deliberate non-allowlisted JSON, exact Git/checkpoint archive-label near misses, JSON-like directory, unsafe archive-link, and SQLite-extension canaries are rejected without reading or reporting their payloads. | An unexpected candidate file/directory, unregistered `.db`/`.sqlite`/`.sqlite3` path, unsafe archive ancestry, missing/extra database (including `inventory.db` or `checkpoints.db`), non-private database mode, traversal failure, or changed second-start inventory fails the merge-gating suite. | A subsystem must not silently reintroduce mutable JSON persistence after its focused tests pass. |

The merge-gating `storage-json` suite runs this self-contained workload directly
with the repository Go toolchain and a disposable runtime, without Docker service
dependencies.

Shared helpers `RequireOneRow` and `ScanStrings` retain exact driver errors and
provide bounded typed row/result validation without turning subsystem schemas
into generic document stores.

## Data And State Model

Every database owns typed subsystem tables and `PRAGMA user_version`. The shared
import ledger stores component and relative source identities, SHA-256 digests,
bounded source sizes, imported/skipped counts, archive status, timestamps, and
issue codes plus record digests. It never stores an original rejected payload,
credential, token, or diagnostic string derived from one.

`archive_status='complete'` means the default no-overwrite archive transition
finished. `archive_status='pending'` means closeout remains outstanding; for an
explicitly deferred sealed source it never claims that the disposable source
was moved. The sealed callback cannot clean it up. Any later live-artifact
closeout requires a separate explicit owner-controlled authorization.

An explicit sealed-absent root has no ledger row because no source exists. Its
unexported proof follows `captured -> enumerated-empty -> checked-before-commit
-> closed`. The proof retains the nearest owner-private directory and exact
missing suffix, reopens the named ancestor without following links, and accepts
only the same first missing component. Descendants behind that missing name are
not claimed to be individually observable.

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

Owns: CODE internal/sqlitestore/**
Owns: CODE integration/suites/storage-json/**
Owns: TEST internal/sqlitestore/*
Owns: TEST pkg/gateway/runtime_storage_json_allowlist_integration_test.go TestIntegrationRuntimeOwnedJSONAllowlist
Owns: TEST pkg/gateway/runtime_storage_legacy_migration_integration_test.go TestIntegrationRuntimeOwnedJSONLegacyMigration
Owns: TEST pkg/gitworkspace/runtime_storage_legacy_relations_integration_test.go TestIntegrationRuntimeOwnedJSONLegacyGitInventoryRelations
Owns: INTEGRATION storage-json

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Go API | `sqlitestore.Open(ctx, path, options)` | Uses the internal provider to open/configure one subsystem database, then integrity-checks, migrates, validates, and archives without a JSON fallback. | `FR-SQLITE-001`, `FR-SQLITE-002`, `FR-SQLITE-003`, `FR-SQLITE-004` |
| Go API | `sqlitestore.Immediate(ctx, db, callback)` | Runs one trusted synchronous callback in `BEGIN IMMEDIATE`, or `BEGIN EXCLUSIVE` when `ctx` carries the live exact-target migration capability. A provider-owned hook boundary rejects callback transaction termination, authorizes one final commit, and removes hooks before abort/release. | `FR-SQLITE-002`, `FR-SQLITE-005` |
| Go API | `LegacyOptions.FinalizeResults` / `LegacyResultFinalizer` | Resolve ordered multi-source relationships and return exact final `ImportResult` for every newly imported source; the helper replaces provisional ledger counts/issues inside the import transaction. | `FR-SQLITE-003` |
| Go API | `LegacyOptions.Seal` / `LegacySealer` | Performs optional idempotent subsystem-specific closeout or validation after every successful deterministic enumeration, including zero-source and already-closed opens, inside the migration transaction and before validation. The shared helper, not this callback, owns the generic durable horizon. | `FR-SQLITE-003` |
| Go API | `LegacyOptions.Closeout` / `LegacyCloseoutPolicy`; `LegacyCloseoutArchive`, `LegacyCloseoutDeferred` | Select default archive-and-remove closeout or explicit sealed/deferred closeout. Deferred mode requires an empty archive root, preserves sources exactly, and keeps their archive ledger state pending. | `FR-SQLITE-003`, `FR-SQLITE-004` |
| Go API | `LegacyOptions.SourceRootPolicy` / `LegacySourceRootPolicy`; `LegacySourceRootExistingDirectory`, `LegacySourceRootSealedAbsent` | Keep an existing safe directory as the zero/default contract, or explicitly bind an exact absent disposable root to one empty offline import transaction. The absent proof is unexported, one-use, retained through commit, and never grants filesystem mutation authority. | `FR-SQLITE-003`, `FR-SQLITE-004` |
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
2. Ask the internal provider to prepare and inspect the final database
   directory and main/WAL/SHM/rollback-journal generation without accepting an
   unsafe endpoint, alias, owner, or mode.
3. Let the provider build the internal filesystem URI and configure foreign
   keys, bounded busy timeout, and FULL synchronization. Select WAL normally;
   under the explicit migration fence, limit the pool to one connection and
   select exclusive locking with the rollback journal.
4. Check existing integrity, acquire one connection, and enter
   `BEGIN IMMEDIATE`, or `BEGIN EXCLUSIVE` under the migration fence. Read
   `user_version`, reject a future version, apply each
   contiguous schema/data migration, enumerate and import deterministic legacy
   inputs, finalize aggregate relationships/accounting when configured, run
   optional subsystem closeout, always close the shared generic import horizon
   after its first complete enumeration, validate domain and import schemas,
   and commit once.
5. Recheck integrity and permissions. For each pending import under the default
   archive policy, revalidate the recorded relative identity and digest,
   complete or recover its no-overwrite archive transition, and mark the ledger
   row complete in an immediate transaction. Under explicit deferred closeout,
   perform no source write, rename, removal, permission change, or sync and
   retain the pending ledger state for the sealed disposable input owner.

## Cross-Feature Behavior

`FR-DATABASE-PROVIDER-CATALOG` remains a dormant internal inventory of future
provider candidates. The SQLite provider and control foundations now supply
mechanics used by this compatibility store. Subsystem-local SQLite stores
remain active persistence authority. No readiness, backup engine, database
CLI, supervisor/runtime wiring, concrete broker domain adapter, configuration
or HTTP contract, or production cutover is added here. The deferred closeout
capability is likewise dormant until a selected offline-migration adapter opts
in for engine-supplied sealed disposable legacy roots; existing runtime
adapters keep the default archive policy.

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

- Missing legacy sources invoke no importer; the shared helper still closes the
  generic first-open import horizon. An optional subsystem sealer still runs,
  and enumeration or seal errors are not no-ops.
- Inputs are bounded per source, in aggregate, and by source count.
- Malformed records may be skipped only when the domain importer explicitly
  classifies them; structural filesystem or SQLite failures abort.
- Duplicate canonical identities use a subsystem's documented deterministic
  winner and record later conflicts as safe issues.
- An archive destination is never overwritten. Matching partial transition
  states resume; mismatched bytes fail.
- Deferred closeout requires an empty archive root, leaves admitted sources and
  their metadata unchanged on every outcome, and retains pending archive state.
- Immediate callbacks cannot commit or roll back the owner transaction with
  SQL. A denied `COMMIT`/`END`, explicit rollback, or rollback conflict poisons
  the operation even if callback code ignores the SQL error; schema, import
  horizon, and domain rows remain uncommitted. Callbacks are reviewed trusted
  synchronous code; architecture gates reject direct `Raw`, `Close`, retention,
  return, channel, and goroutine escape forms as defense in depth. #436 tracks
  replacing the raw compatibility value with a restricted runtime facade.
- Sealed-absent deferred closeout additionally requires exact-target migration
  authority, a canonical non-overlapping root, a retained owner-private
  no-follow ancestry proof, exactly one empty enumeration, and a final proof
  immediately before commit. It never calls the importer or creates the missing
  namespace. Persistent appearance or proof drift rolls the transaction back;
  a database header may already exist because SQLite opens before `BEGIN`.
- Finite pre/post checks do not claim to detect a same-UID actor that creates and
  removes an object entirely between checks. Such an actor is outside the
  migration-fence, private-workspace, and quiescence boundary.
- A second startup neither imports again nor creates a mutable JSON file.
- An in-memory database is non-persistent and isolated from every other open.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SQLITE-001`, `FR-SQLITE-002`, `FR-SQLITE-005` | [internal/sqlitestore/open_test.go](../../internal/sqlitestore/open_test.go) |
| `FR-SQLITE-002`, `FR-SQLITE-005` | [internal/sqlitestore/offline_test.go](../../internal/sqlitestore/offline_test.go), [internal/sqlitestore/permission_routing_coverage_test.go](../../internal/sqlitestore/permission_routing_coverage_test.go), [internal/sqlitestore/transaction_boundary_test.go](../../internal/sqlitestore/transaction_boundary_test.go), [internal/sqlitestore/transaction_boundary_architecture_test.go](../../internal/sqlitestore/transaction_boundary_architecture_test.go) |
| `FR-SQLITE-003`, `FR-SQLITE-004` | [internal/sqlitestore/open_test.go](../../internal/sqlitestore/open_test.go), [internal/sqlitestore/legacy_deferred_test.go](../../internal/sqlitestore/legacy_deferred_test.go), [internal/sqlitestore/legacy_absent_open_test.go](../../internal/sqlitestore/legacy_absent_open_test.go), [internal/sqlitestore/legacy_absent_platform_test.go](../../internal/sqlitestore/legacy_absent_platform_test.go), [internal/sqlitestore/legacy_absent_unix_test.go](../../internal/sqlitestore/legacy_absent_unix_test.go), [internal/sqlitestore/legacy_absent_windows_test.go](../../internal/sqlitestore/legacy_absent_windows_test.go), [internal/sqlitestore/legacy_fault_test.go](../../internal/sqlitestore/legacy_fault_test.go), [internal/sqlitestore/legacy_finalize_results_test.go](../../internal/sqlitestore/legacy_finalize_results_test.go), [internal/databasemigration/migration_backup_integration_test.go](../../internal/databasemigration/migration_backup_integration_test.go), [pkg/memory/sqlite_store_test.go](../../pkg/memory/sqlite_store_test.go) |
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

- [internal/sqlitestore/open.go](../../internal/sqlitestore/open.go)
- [internal/sqlitestore/legacy.go](../../internal/sqlitestore/legacy.go)
- [internal/sqlitestore/legacy_absent.go](../../internal/sqlitestore/legacy_absent.go)
- [internal/sqlitestore/open_test.go](../../internal/sqlitestore/open_test.go)
- [pkg/state/state_sqlite.go](../../pkg/state/state_sqlite.go)
- [pkg/channels/wecom/reqid_store.go](../../pkg/channels/wecom/reqid_store.go)
- [pkg/channels/weixin/state_sqlite.go](../../pkg/channels/weixin/state_sqlite.go)
- [pkg/memory/sqlite_store.go](../../pkg/memory/sqlite_store.go)
- [pkg/workflows/sqlite_store.go](../../pkg/workflows/sqlite_store.go)
- [pkg/repoaudit/sqlite.go](../../pkg/repoaudit/sqlite.go)
- [pkg/repoeval/sqlite.go](../../pkg/repoeval/sqlite.go)
- [pkg/gateway/runtime_storage_json_allowlist_integration_test.go](../../pkg/gateway/runtime_storage_json_allowlist_integration_test.go)
- [integration/suites/storage-json](../../integration/suites/storage-json)
