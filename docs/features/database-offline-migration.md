# Database Offline Migration

## Feature ID

`FR-DATABASE-OFFLINE-MIGRATION`

## Behavior Summary

PicoClaw defines a dormant internal engine that coordinates exclusively
fenced, claim-bound, verified-backup SQLite maintenance and domain migration.
It holds one claims migration guard across the operation, selects logical stores
from its detached catalog, creates durable recovery evidence before provider
work, and delegates each live transition through one finite claims-owned child
lease and one manifest-bound immutable source.

No command, broker, IPC handler, or application runtime constructs or invokes
the engine in this stage. A failed run preserves its backup and never performs
automatic restoration.

## Reconstruction Notes

- Core APIs are `New`, `Engine.Run`, `Options`, `Result`, and `StoreResult`.
- An engine retains a detached projected catalog and explicit immutable adapter
  registry; it never retains the caller's configuration object.
- Dry-run may select the complete catalog, while a mutating run accepts exactly
  one logical store. All selection and execution are bounded.
- Every dry-run archive and every mutating archive that passes preflight advances
  through revision-1 `migration_in_progress` before its revision-2 terminal
  status.
- Adapter callbacks receive only an authorized disposable stage and disposable
  legacy copies reconstructed from the verified backup. An adapter that uses
  the shared legacy importer for those sealed roots must explicitly select
  deferred closeout so the importer never mutates them.
- The provider retires its normalized source stage before final live-source validation,
  pins the sole remaining final stage, and lends one opaque exact-stage scope
  to the engine's last live-source pass. This infrastructure remains dormant.
- Stage retirement is bound to retained parent/file identities and a no-replace
  quarantine; an absent old pathname or a replacement decoy is never proof that
  the provider-created inode was deleted.
- Failures after an irreversible replacement are reported as
  `CodeOutcomeUnknown`; known pre-cutover failures remain ordinary failures.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-OFFLINE-MIGRATION-001` | MUST | Trusted infrastructure constructs an engine from an existing canonical-home catalog context and explicit adapter registry, then calls `Run` with optional exact store IDs, backup parent, and dry-run policy. | Construction projects and detaches one complete catalog. A run admits only one concurrent invocation per engine, acquires the exact-home migration fence, complete physical claim lease, and continuously held `MigrationRefreshingGuard` before exposing store state, and returns deterministic ID-sorted selected results. Empty selection means the complete catalog; selection is limited to 256 stores, and non-dry mutation requires exactly one. | Construction performs catalog metadata reads only. A run may create owner-private fence and claim files and holds their locks until status handoff and provider drain complete. | Nil or invalid construction input, cancellation, concurrent execution, active storage, expired authority, invalid/unknown/duplicate selection, excessive selection, or multi-store mutation fails before provider mutation. Partial acquisition is released in guard, lease, fence order. | Offline maintenance must have one explicit logical selection under the same exclusive and physical authority used for cutover. |
| `FR-DATABASE-OFFLINE-MIGRATION-002` | MUST | A guarded run has selected stores. | Every selected generation and legacy input is snapshotted and durably verified before provider work. Dry-run then commits revision-1 `migration_in_progress` and advances to revision-2 `dry_run` without provider or adapter work. A mutating run first revalidates live sources and preflights each adapter contract exclusively against a sealed disposable generation reconstructed from backup, then commits revision 1 immediately before provider admission. | Backup and status creation mutate only their private namespaces; dry-run never changes a live generation. Preflight opens only disposable backup material and releases its inspection before the seal returns. | Unsafe input, backup or live-source drift, corrupt inspection, too-new schema, missing adapter, required-but-missing callback, cleanup failure, or inability to persist lifecycle evidence fails before cutover and preserves the backup. A mutating preflight failure leaves status absent rather than fabricating an in-progress or terminal record. | Recovery evidence and a complete feasibility decision must precede both irreversible provider work and its durable in-progress claim. |
| `FR-DATABASE-OFFLINE-MIGRATION-003` | MUST | Preflight succeeds for one mutating store. | Missing, empty, old-version, legacy-bearing, or contract-incomplete state is classified from the verified archive and adapter contract. The engine mints one manifest-bound immutable-generation source, derives one finite provider child from the retained migration guard, and passes both to `MigrateStagedOfflineFromWithLiveVerification`. Required domain migration runs only on the provider-created final stage and receives legacy paths solely inside a sealed synchronous use callback. A shared-importer adapter explicitly selects deferred closeout: destination schema/data, import audit, and horizon closure commit atomically while source rows remain truthfully archive-pending and every sealed root/source remains unchanged. Ready non-empty state uses the same provider path with a no-op domain callback; missing or empty legacy-free `initialize_online` state remains untouched. The provider validates the complete typed contract, durably retires its normalized source stage, pins the sole remaining final stage, and supplies one opaque StoreID/target/invocation-bound scope. Within that scope the engine reverifies the backup and performs its last live-source pass while excluding only the exact pinned main path. The stage identity remains in the physical-alias deny-set; stage WAL/SHM/journal, near names, foreign/stale stages, and every other legacy member remain visible. | Adapter and provider work changes only disposable backup-derived inputs until the provider performs its single atomic live-name replacement and exact pin promotion. The temporary source is removed before final live-source validation; the final-stage exclusion is ephemeral and never changes the archive. Deferred closeout performs no rename, removal, chmod, write-sync, or other source mutation. | Callback error or panic, temporary-source cleanup ambiguity, backup alteration, sealed-root or source drift, stale/foreign/reused stage scope, stage/sidecar or other live-source drift, failed typed contract, lost authority, provider failure, or pre-cutover cleanup error discards an admitted unused pin before stage deletion and rejects replacement without modifying live bytes. Failed or ambiguous pin discard retains the pinned stage and fails closed. | Domain code must never migrate mutable live input, and only claims-owned child authority may carry and temporarily exempt the exact final main immediately before cutover. |
| `FR-DATABASE-OFFLINE-MIGRATION-004` | MUST | A provider child returns or a run terminates after revision 1. | The child drains before the engine disposes its immutable source. The provider's successful `MaintenanceResult` is authoritative only after it has validated the typed stage, atomically installed and promoted that exact identity, activated it, and integrity/version-checked the installed generation. The engine advances exactly once to revision-2 `complete`, `failed`, `outcome_unknown`, or `complete_with_cleanup_error` while the migration guard remains live, then releases guard, lease, and fence in order. | Successful cutover replaces one live generation and monotonically extends its physical claims. Fence, claims, inspections, and disposable inputs are released; immutable backup and sibling status remain. | Provider activation/validation uncertainty or provider-drain failure records and returns `CodeOutcomeUnknown`. Post-commit source cleanup records `complete_with_cleanup_error`; an uncertain terminal-status commit after cutover is also outcome-unknown. A later guard-release failure cannot rewrite immutable revision 2: after replacement it returns `CodeOutcomeUnknown` while the durable terminal continues to describe the already-recorded provider/source outcome. No failure triggers automatic restore. | Once replacement may have occurred, honest outcome evidence and retained recovery bytes are safer than an inferred rollback or false failure claim. |

## Data And State Model

`Engine` retains canonical catalog resolution context, a detached projected
catalog, an immutable adapter registry, a clock, and one atomic in-process run
flag. `Options` contains detached store selection, backup parent, and dry-run
policy. `Result` exposes only the backup directory and provider-neutral store
outcomes; physical generation and legacy paths remain internal.

One run owns a migration `Fence`, physical-claims `Lease`, continuously held
`MigrationRefreshingGuard`, at most one provider child, a `backupSession`,
short-lived sealed input references, and at most one callback-bounded pinned
final-stage scope. The immutable manifest records recovery bytes; its
digest-bound sibling status is the durable lifecycle record. The dynamic stage
path is never persisted in either one.

## Surface Ownership

Owns: CODE internal/databasemigration/migration.go
Owns: TEST internal/databasemigration/migration_test.go *
Owns: TEST internal/databasemigration/coverage_engine_store_migration_test.go *
Owns: TEST internal/databasemigration/coverage_engine_run_additional_test.go *
Owns: TEST internal/databasemigration/coverage_engine_panic_closeout_test.go *
Owns: TEST internal/databasemigration/coverage_engine_run_closeout_test.go *
Owns: TEST internal/databasemigration/coverage_engine_store_closeout_test.go *
Owns: TEST internal/databasemigration/migration_backup_integration_test.go *
Owns: TEST internal/databasemigration/migration_stage_exclusion_integration_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `New` | Detach one projected catalog and bind it to an explicit immutable adapter registry without activating runtime storage. | `FR-DATABASE-OFFLINE-MIGRATION-001` |
| Internal Go API | `Engine.Run`, `Options` | Select stores and coordinate fence, claims, backup, preflight, migration, verification, and lifecycle evidence in fail-closed order. | `FR-DATABASE-OFFLINE-MIGRATION-001`, `FR-DATABASE-OFFLINE-MIGRATION-002`, `FR-DATABASE-OFFLINE-MIGRATION-003`, `FR-DATABASE-OFFLINE-MIGRATION-004` |
| Internal Go values | `Result`, `StoreResult` | Return dry-run, backup, existence, schema-version, migration, and adapter-required state without exposing catalog paths. | `FR-DATABASE-OFFLINE-MIGRATION-002`, `FR-DATABASE-OFFLINE-MIGRATION-004` |

## Algorithms And Ordering

1. Project and detach the complete catalog during construction; retain no
   caller configuration pointer.
2. At run time, reject cancellation or overlap, acquire the exact-home
   migration fence and complete claims, enter `GuardStoresMigrating`, and
   resolve bounded exact store selection from that guard.
3. Snapshot and verify selected bytes and derive existence from the verified
   manifest. Dry-run commits revision 1 here; mutation first reverifies live
   sources and preflights adapter state inside sealed disposable callbacks.
4. Commit revision-1 `migration_in_progress` only after mutating preflight has
   succeeded and immediately before provider admission.
5. Mint the StoreID-bound immutable source, derive a finite provider child,
   run optional adapter work with sealed legacy inputs, and let the provider
   validate the sole final stage, retire its temporary source, and pin the final
   stage. Use the provider-minted opaque scope to reverify backup and live sources
   with only that exact main excluded; then let the provider recheck, replace,
   promote, activate, and reconcile the generation.
6. Accept the provider's post-activation result, drain the child even during
   panic unwinding, dispose source state, commit one terminal status under the
   live guard, then release guard, lease, and fence.

## Cross-Feature Behavior

The provider catalog supplies immutable logical-to-physical selection;
physical claims and local IPC supply lease-bound and exclusive authority.
Storage contracts supply explicit adapter schemas. Backup primitives preserve
and revalidate source bytes, while SQLite inspection and offline operations
perform typed validation, normalization, staging, and cutover. The engine
coordinates those layers but is not an online owner and is not connected to
readiness, broker startup, IPC dispatch, or application persistence. The shared
deferred closeout option changes no runtime adapter behavior; it is used only
when a selected adapter explicitly opts in for engine-supplied sealed
disposable roots.

## Failure And Edge Cases

- Dry-run may cover multiple stores but still acquires the exclusive fence and
  complete catalog claims; it invokes no adapter and changes no live database.
- A missing callback is acceptable only when the exact store state needs no
  domain migration.
- A too-new generation, corruption, or incomplete current-version contract is
  detected from a disposable backup before live provider work.
- A shared-importer adapter cannot give a sealed disposable root a normal
  archive destination; deferred closeout leaves every source and its metadata
  unchanged while retaining truthful pending archive state.
- Adapter panic text and provider diagnostics are not promoted into trusted
  migration state; a known pre-cutover failure records `failed`.
- A temporary provider source that cannot be conclusively removed blocks final
  validation and is never excluded. The sole final-stage main is excluded only
  under its current pin; its sidecars and every second or near-name stage remain
  ordinary legacy drift.
- Any post-pin pre-replacement failure discards the unused pin before cleanup;
  failed or ambiguous discard retains the stage instead of deleting possibly
  pinned evidence.
- A renamed working/final stage or a decoy at its old name makes retirement
  fail closed without deleting either substitute or escaped evidence.
- A failure after live replacement, or any inability to prove that the provider
  child drained, records `outcome_unknown` and preserves the backup for explicit
  operator recovery.
- Terminal status is immutable once committed under the live guard. If the
  subsequent guard release fails, the returned error reports the new uncertainty
  but the revision-2 record continues to report the earlier provider/source
  outcome.
- Restarting an already-ready store skips the adapter while retaining backup,
  maintenance, validation, and claim ordering.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-OFFLINE-MIGRATION-001`, `FR-DATABASE-OFFLINE-MIGRATION-002` | [internal/databasemigration/migration_test.go](../../internal/databasemigration/migration_test.go), [internal/databasemigration/migration_backup_integration_test.go](../../internal/databasemigration/migration_backup_integration_test.go), [internal/databasemigration/coverage_engine_run_additional_test.go](../../internal/databasemigration/coverage_engine_run_additional_test.go), [internal/databasemigration/coverage_engine_store_migration_test.go](../../internal/databasemigration/coverage_engine_store_migration_test.go) |
| `FR-DATABASE-OFFLINE-MIGRATION-003`, `FR-DATABASE-OFFLINE-MIGRATION-004` | [internal/databasemigration/migration_test.go](../../internal/databasemigration/migration_test.go), [internal/databasemigration/migration_backup_integration_test.go](../../internal/databasemigration/migration_backup_integration_test.go), [internal/databasemigration/coverage_engine_run_additional_test.go](../../internal/databasemigration/coverage_engine_run_additional_test.go), [internal/databasemigration/coverage_engine_store_migration_test.go](../../internal/databasemigration/coverage_engine_store_migration_test.go) |

## Implementation Anchors

- [internal/databasemigration/migration.go](../../internal/databasemigration/migration.go)
- [internal/databasemigration/migration_test.go](../../internal/databasemigration/migration_test.go)
- [internal/databasemigration/migration_backup_integration_test.go](../../internal/databasemigration/migration_backup_integration_test.go)
- [internal/databasemigration/coverage_engine_run_additional_test.go](../../internal/databasemigration/coverage_engine_run_additional_test.go)
- [internal/databasemigration/coverage_engine_store_migration_test.go](../../internal/databasemigration/coverage_engine_store_migration_test.go)
