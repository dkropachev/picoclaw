# Database SQLite Provider Foundation

## Feature ID

`FR-DATABASE-SQLITE-CONTROL`

## Behavior Summary

PicoClaw defines the dormant internal SQLite provider for the future
single-owner database broker. Within the new broker foundation it alone binds
the shipped driver to `database/sql`, constructs physical DSNs, prepares and
secures a generation, configures live WAL or exclusively fenced offline
rollback-journal operation, inspects existing stores without initializing
missing ones, and performs provider-owned maintenance or validated staged
replacement. Its
control helpers still read or set the `main` schema version, run bounded
integrity diagnostics, and validate exact manually created unique indexes.

Production imports are restricted to readiness, offline migration, and the
existing internal SQLite compatibility store. This provider does not consume a
logical catalog itself and remains disconnected from broker IPC. This stage
adds no database CLI, supervisor/runtime wiring, domain adapter, configuration
or HTTP surface, or production persistence cutover.

## Reconstruction Notes

- Similarity target: recreate one internal SQLite binding and its small control,
  security, inspection, pool-handoff, maintenance, and staged-cutover
  primitives without exposing SQLite through an application API.
- Core types/functions: `OpenStore`, `Configure`, `ConfigureOffline`,
  `PrepareStore`, `SecureGeneration`, `DSN`, `Inspect`, `Inspection`,
  `Inspection.Release`, `MaintainOffline`, `MigrateStagedOfflineFrom`,
  `SchemaVersion`, `SetSchemaVersion`, `CheckIntegrity`,
  `CheckIntegrityOnly`, `CheckForeignKeys`, and `ValidateUniqueIndexes`.
- Runtime ordering: validate and privately prepare a trusted generation, bind
  only the shipped driver, configure and verify connection-local durability,
  inspect and retain one exact-generation pool, then either hand it to a later
  owner or close it. Offline work recovers and snapshots through SQLite before
  changing journal/locking mode or installing a validated disposable stage.
- Non-obvious constraints: `user_version` is limited to SQLite's non-negative
  signed 32-bit range; a combined integrity check runs physical integrity
  before referential integrity; only unique indexes created explicitly by
  `CREATE INDEX` belong to the exact index set; control helpers provide no
  transaction or ownership guarantee; readiness inspection never creates a
  missing store; and staged replacement requires its caller to have already
  acquired the migration fence, physical claims, and mandatory backup.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-CONTROL-001` | MUST | Trusted internal code supplies an explicit non-nil context and caller-owned query or execution boundary to read or set the `main` schema `user_version`. | `SchemaVersion` returns a value from `0` through `MaxInt32`; `SetSchemaVersion` emits only the decimal representation of a value in that range. | Reading changes nothing. Setting changes only the caller's current SQLite connection or transaction according to SQLite semantics. The helper does not begin, commit, or roll back a transaction. | A nil context, nil boundary, negative value, value above `MaxInt32`, unavailable value, or query/execute failure returns an error. No helper claims that the caller holds a migration fence or transaction. | Schema control must be centralized without letting a primitive imply lifecycle authority it cannot enforce. |
| `FR-DATABASE-SQLITE-CONTROL-002` | MUST | Trusted internal code requests physical integrity, referential integrity, or both through an explicit context and caller-owned query boundary. | Physical integrity accepts only the exact successful result from a bounded one-result `main` integrity check. Referential integrity succeeds only when the `main` foreign-key check yields no row. `CheckIntegrity` runs the physical check first and the referential check only after it succeeds. | Diagnostics do not repair, configure, or mutate the database. Every opened row iterator is closed. | A nil context or boundary, unexpected integrity result, returned foreign-key row, row iteration error, or SQL failure returns an error. Reported corruption and foreign-key violations use generic messages and never include SQLite diagnostic content, table names, row values, or caller data. | Diagnostics must fail closed without turning provider details or stored values into application-visible output. |
| `FR-DATABASE-SQLITE-CONTROL-003` | MUST | Trusted internal code supplies one `main` schema table and an expected list of manually created unique-index names. | The named table must exist exactly once. Validation succeeds only when every expected unique index exists exactly once with SQLite origin `c` and no other origin-`c` unique index exists for that table. Primary-key and automatic indexes, including indexes created for table-level unique constraints, are outside this set. | Validation mutates neither schema nor caller-owned table/index inputs. | A nil context or boundary; empty, padded, invalid-UTF-8, NUL-bearing, or over-1,024-byte table/name; more than 256 expected indexes; a duplicate expected name; a missing table/index; an unexpected manual unique index; or catalog-query failure returns an error. An empty expected list requires zero manual unique indexes. | Domain schema adapters need an exact provider catalog check without confusing SQLite-maintained indexes with explicitly declared schema objects. |
| `FR-DATABASE-SQLITE-CONTROL-004` | MUST | Repository code attempts to import the provider or extend its driver boundary. | Exact-file architecture guards permit provider imports only from `internal/databasemigration/backup.go`, `internal/databasemigration/migration.go`, `internal/databasereadiness/readiness.go`, `internal/sqlitestore/open.go`, and `internal/sqlitestore/schema.go`. Within `internal/sqliteprovider`, only `provider.go` calls `database/sql.Open`, and direct driver imports remain limited to reviewed provider files. | The allowlist adds no runtime composition and application packages receive no provider handle, path, DSN, SQL, PRAGMA, or driver error through this foundation. Existing application and channel storage remains unchanged until cutover. | Any additional provider importer or unreviewed driver/open use inside the provider package fails the architecture tests until a separately specified stage reviews the boundary. | A dormant provider must be reusable by migration and compatibility code without becoming an application-accessible second owner. |
| `FR-DATABASE-SQLITE-CONTROL-005` | MUST | Trusted storage infrastructure opens a memory database or a validated provider-private filesystem path and requests live or offline configuration. | `OpenStore` serializes same-path opens, traverses creation boundaries through retained no-follow platform handles, creates missing path components and the endpoint with private-at-creation security, constructs the internal DSN, forces the first connection open with a bounded ping, and revalidates the complete coherent generation transition. Live configuration selects and verifies WAL, foreign keys, bounded busy timeout, and `synchronous=FULL`; offline configuration on a caller-limited single-connection pool selects exclusive locking and DELETE journal while retaining the other settings. Memory DSNs use unique shared-cache names and create no file. | Missing file-backed directories/main files may be durably created and hardened. Existing main, WAL, SHM, and rollback-journal members are owner-checked, single-link regular files with private POSIX mode or a protected owner-only Windows DACL. | Blank, overlong, invalid-UTF-8, ambiguous Windows device namespace, URI-shaped, NUL-bearing, symlink/reparse, irregular, hardlinked, foreign-owned, replaced, orphaned, incoherent, or unsafe generation members fail before use. SQLite-managed WAL/SHM files may coherently appear, disappear, or be recreated by a cooperating process while the main identity remains stable; a new or replaced rollback journal is rejected. Invalid contexts/timeouts, driver/open/configuration errors, and a requested journal/locking mismatch fail without fallback. | One internal provider must own physical addressing, driver binding, security, and durability semantics. |
| `FR-DATABASE-SQLITE-CONTROL-006` | MUST | Trusted readiness code inspects one catalog path. | A missing main with no sidecar returns a non-existing inspection without creating state. An existing generation is secured, opened with one connection, pinged, complete-generation identity-rechecked, integrity/foreign-key checked, versioned, and classified empty only when schema object count and `user_version` are both zero. Typed schema-object, real-table-column, and import-horizon queries reuse the retained pool. Each `Inspection` owns one reference; `Release` closes only the last unadopted reference, and `Inspection.Adopt` transfers only its sole matching-timeout, matching-generation reference. `OpenStore` cannot consume readiness state by path. | Existing provider metadata and permissions may be hardened, but inspection changes no schema, applies no application migration, and does not create a missing generation. The process-local handoff map retains only the path key, timeout, reference owners, complete main/WAL/SHM/journal identities, and pool until typed adoption or final release. | An orphan/incoherent sidecar, unsafe or changed generation, integrity failure, lock contention, schema query failure, incompatible timeout, wrong reference, identity change, or adoption while multiple readiness owners remain fails closed without invalidating another reference. Double release and snapshot close cannot close an already adopted owner pool or another snapshot's reference. | Readiness and later ownership must share one checked connection lifecycle without a path-global second-owner race. |
| `FR-DATABASE-SQLITE-CONTROL-007` | MUST | Exclusively fenced and already-backed-up migration infrastructure supplies a live exact-target context and a distinct private source reconstructed from the verified backup. | `MaintainOffline` recovers and integrity-checks only the disposable source, performs a full WAL checkpoint, enters and commits an exclusive rollback-journal boundary without schema change, restores WAL/normal locking, truncation-checkpoints, reopens, and requires an unchanged schema version. `MigrateStagedOfflineFrom` rejects equal source/target identities, opens or recovers only the private source, uses SQLite online backup to build a random private stage, invokes independently committing domain code only against that stage, requires the exact expected version, integrity, and caller-supplied typed contract validation, rechecks the untouched target identity and absence of target/stage sidecars, atomically replaces the live main name with directory fsync on Unix or write-through replacement on Windows, then checkpoints and reopens the installed generation. | Maintenance may recover/checkpoint disposable state but cannot change schema version. Staged migration changes the live name only after callback success, closed-stage validation, and outer live-source revalidation; failed inspectable stages are removed, while retained diagnostic stages are reported. | Missing/wrong/expired authority, equal source/target, corrupt backup source, busy state, callback failure/panic, unexpected version, active live sidecars, changed live source, replacement failure, or failed installed reopen returns an error. Every failure before replacement leaves live bytes unchanged; uncertainty after replacement returns provider-neutral `OutcomeUnknown`. | Independently committing domain migrations must never operate directly on the live generation or make filesystem sidecars the copy protocol. |

## Data And State Model

Control helpers retain no state and operate on caller-owned SQL boundaries.
Provider operations own private physical paths, generation members, DSNs, and
temporary pools. The inspection handoff map retains only an exact path key,
timeout, reference tokens, complete generation identities, and pool until
release or explicit typed adoption. None is projected
into the database protocol, logical catalog, configuration, or HTTP surface.

## Surface Ownership

Owns: CODE internal/sqliteprovider/**
Owns: TEST internal/sqliteprovider/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `SchemaVersion`, `SetSchemaVersion` | Read or set a bounded `main.user_version` through a caller-owned SQL boundary without providing transaction or fence ownership. | `FR-DATABASE-SQLITE-CONTROL-001` |
| Internal Go API | `CheckIntegrity`, `CheckIntegrityOnly`, `CheckForeignKeys` | Run ordered, non-repairing `main` integrity diagnostics and suppress stored/provider diagnostic detail. | `FR-DATABASE-SQLITE-CONTROL-002` |
| Internal Go API | `ValidateUniqueIndexes` | Validate the exact expected origin-`c` unique-index set while excluding SQLite-created primary-key and automatic indexes. | `FR-DATABASE-SQLITE-CONTROL-003` |
| Architecture gate | SQLite-provider import/driver guards | Keep production consumers explicit and driver open inside `provider.go`. | `FR-DATABASE-SQLITE-CONTROL-004` |
| Internal Go API | `OpenStore`, `Configure`, `ConfigureOffline`, `PrepareStore`, `SecureGeneration`, `DSN` | Own eager driver binding, no-follow physical preparation/security, and live/offline durability configuration without implicit pool adoption. | `FR-DATABASE-SQLITE-CONTROL-005` |
| Internal Go API | `Inspect`, `Inspection`, `Inspection.Adopt`, `Inspection.Release` | Inspect without initialization and explicitly hand one complete-generation/timeout-bound reference to a later sole owner or release it. | `FR-DATABASE-SQLITE-CONTROL-006` |
| Internal Go API | `MaintainOffline`, `MigrateStagedOfflineFrom` | Maintain a backup-derived source or validate and atomically install its disposable migration stage under an exact-target capability. | `FR-DATABASE-SQLITE-CONTROL-007` |

## Algorithms And Ordering

1. Require an explicit non-nil context and the narrow query or execution
   boundary needed by the requested operation.
2. For schema-version reads, query only `main.user_version` and validate the
   returned non-negative signed 32-bit value. For writes, validate the value
   first and append only its base-10 digits to the fixed control statement.
3. For combined diagnostics, run the bounded `main` physical integrity check
   first. Only its exact success result admits the `main` foreign-key check;
   any returned row represents a violation.
4. For unique-index validation, reject excessive, duplicate, or invalid inputs,
   require the exact `main` table, require each named origin-`c` unique index
   exactly once, then compare the total origin-`c` unique-index count with the
   expected count.
5. Return errors only to the trusted caller. Do not map, cache, transmit, or
   expose them through provider-neutral protocol values in this stage.
6. For an open or inspection, validate private directory and generation
   identities before and after eager provider access. Retain an inspected pool
   by exact complete-generation identity and timeout; reference each inspection
   and adopt only its sole matching token, never through `OpenStore(path)`.
7. For offline work, recover and checkpoint through SQLite, use exclusive
   rollback-journal mode, and either preserve the live schema or migrate a
   staged copy. Validate and close the stage before atomic replacement, then
   restore WAL and reopen the installed generation.

## Cross-Feature Behavior

`FR-SQLITE` remains active subsystem-owned storage and now consumes this package
through `internal/sqlitestore`; an exact-target capability in the migration
context keeps its domain schema/import transaction offline when invoked by the
new migration engine without changing unrelated stores in the process.
`FR-DATABASE-PROVIDER-CATALOG` supplies claims, readiness, and backed migration
that call the provider without exposing it through the logical facade.
`FR-DATABASE` and IPC remain provider-neutral. No supervisor, CLI, runtime
composition, concrete domain adapter, configuration/HTTP change, or cutover is
present.

## Failure And Edge Cases

- A helper never substitutes `context.Background` for missing caller context.
- Schema-version range validation occurs before executing a control statement.
- Physical failure prevents the foreign-key query from running.
- Integrity and foreign-key violation messages contain no returned diagnostic
  text or row content.
- Expected index order does not affect the result, and validation never mutates
  the supplied slice.
- An empty manual-index set still rejects an unexpected manually created unique
  index while ignoring SQLite-maintained primary-key and automatic indexes.
- No control-helper failure repairs a schema or starts a transaction; no
  provider failure falls back to another persistence path.
- Inspection never creates a missing main file, and an orphan sidecar is an
  integrity failure.
- Staged callback failure leaves the live generation unchanged; post-replace
  revalidation failure reports an unknown outcome rather than claiming rollback.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-CONTROL-001`, `FR-DATABASE-SQLITE-CONTROL-002` | [internal/sqliteprovider/control_test.go](../../internal/sqliteprovider/control_test.go) |
| `FR-DATABASE-SQLITE-CONTROL-003` | [internal/sqliteprovider/schema_test.go](../../internal/sqliteprovider/schema_test.go) |
| `FR-DATABASE-SQLITE-CONTROL-004` | [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |
| `FR-DATABASE-SQLITE-CONTROL-005`, `FR-DATABASE-SQLITE-CONTROL-006` | [internal/sqliteprovider/provider_test.go](../../internal/sqliteprovider/provider_test.go), [internal/sqliteprovider/hot_journal_test.go](../../internal/sqliteprovider/hot_journal_test.go), [internal/sqliteprovider/linkcount_unix_test.go](../../internal/sqliteprovider/linkcount_unix_test.go), [internal/sqliteprovider/security_windows_test.go](../../internal/sqliteprovider/security_windows_test.go) |
| `FR-DATABASE-SQLITE-CONTROL-007` | [internal/sqliteprovider/staged_migration_test.go](../../internal/sqliteprovider/staged_migration_test.go), [internal/databasemigration/migration_test.go](../../internal/databasemigration/migration_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/control.go](../../internal/sqliteprovider/control.go)
- [internal/sqliteprovider/schema.go](../../internal/sqliteprovider/schema.go)
- [internal/sqliteprovider/provider.go](../../internal/sqliteprovider/provider.go)
- [internal/sqliteprovider/inspect.go](../../internal/sqliteprovider/inspect.go)
- [internal/sqliteprovider/maintenance.go](../../internal/sqliteprovider/maintenance.go)
- [internal/sqliteprovider/staged_migration.go](../../internal/sqliteprovider/staged_migration.go)
- [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go)
