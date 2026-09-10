# Database SQLite Offline Operations

## Feature ID

`FR-DATABASE-SQLITE-OFFLINE`

## Behavior Summary

PicoClaw defines dormant provider-owned staged replacement for one exact SQLite
target. The provider consumes one opaque, target-bound child lease, copies a
descriptor-pinned immutable backup generation without opening it through
SQLite, normalizes and migrates only provider-owned working files, then
validates them before an atomic live-name replacement.

No runtime or CLI calls these operations in this stage. They acquire no fence,
perform no backup themselves, and change no application persistence route.

## Reconstruction Notes

- Similarity target: isolate provider recovery and independently committing
  domain upgrades from the live generation until validated cutover.
- Core APIs: `MigrateStagedOfflineFrom`, `ImmutableGenerationSource`,
  `StagedMigration`, `StagedValidation`, and `MaintenanceResult`.
- Runtime ordering: consume the exact target lease; invoke the immutable source
  once to byte-copy its complete main/WAL/SHM/journal generation; wait for the
  source's post-use seal; recover and normalize the working copy; SQLite-backup
  it to a final stage; invoke migration and validation callbacks; close and
  recheck the stage and target; pin, replace, reconcile, activate, and finally
  reconcile any installed sidecars.
- Non-obvious constraints: no raw source path escapes its synchronous callback;
  no SQLite connection touches the immutable source or live target before
  cutover; every known pre-replacement failure preserves live bytes;
  post-replacement uncertainty is `OutcomeUnknown`.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-OFFLINE-001` | MUST | A caller supplies one finite child lease and an opaque immutable-generation source minted only by the manifest-validating backup bridge. | The source immutably binds its verified `StoreID`; the provider requires equality with the lease target before invoking it. `MigrateStagedOfflineFrom` consumes the lease exactly once, derives the live target only from it, checks authority before filesystem/SQLite phases, and invokes the source callback exactly once. The inner path-use callback is synchronous and single-use. It copies a bounded coherent main/WAL/SHM/journal inventory into a random private working namespace using exclusive `0600` files, per-file identity/size/digest checks, file and directory sync, and context checks. Source identity, bytes, modes, and inventory are rechecked before the callback returns; only after its outer post-use seal returns may SQLite open the working copy. | Only provider-owned working files are created before cutover; the immutable source and live target remain byte-identical. | Nil/revoked/consumed/expired lease, source/target `StoreID` mismatch, unauthorized source minting, invalid callback use, alias with the target, unsafe/incoherent/oversized/replaced source, copy/sync failure, or cancellation fails before SQLite opens the source or target and identity-cleans every known partial copy. | Backup evidence must remain store-bound, immutable, and descriptor-pinned while crossing into provider-owned mutable state. |
| `FR-DATABASE-SQLITE-OFFLINE-002` | MUST | The sealed source copy and live target have matching existence and the caller supplies bounded migration and validation callbacks. | On the mutable working copy, the provider recovers/checks integrity, fully checkpoints WAL, commits an exclusive DELETE-journal boundary without schema change, restores WAL/normal locking, truncation-checkpoints, reopens, and requires the same pre-migration version. It then SQLite-backups into the final stage, invokes migration, verifies exact expected version and integrity, invokes domain validation, closes all handles, and rechecks stage/target identities and sidecar absence. `MaintenanceResult` reports the recovered before-version and installed after-version. | Recovery, journal metadata, schema, and application rows may change only in provider-owned working/final stages. | Source/target existence mismatch, corruption, busy state, callback error/panic, wrong version, target change, active sidecar, or cancellation fails before replacement. Identity-proven disposable files are removed; a stage whose integrity or pin retirement cannot be proved is retained as bounded diagnostic state rather than deleted unsafely. | Independently committing domain migrations must never operate directly on live or immutable backup bytes. |
| `FR-DATABASE-SQLITE-OFFLINE-003` | MUST | A fully validated stage reaches the live-name boundary. | The provider checks authority, attempts to pin the exact stage, checks authority again, then atomically replaces the live main. Because pin publication can precede a final pin error, any pin or other known pre-replacement failure is followed by idempotent unused-pin discard before stage deletion. A completed or uncertain replacement is immediately promoted through replacement reconciliation before any ordinary reconciliation. Unix rename is followed by directory fsync; Windows uses write-through replacement. The installed generation is activated, checkpointed, reopened, integrity/version-checked, and its new sidecars are reconciled before success. | The live main identity changes exactly once; no live sidecar is carried across cutover. Physical claims and replacement assignments remain monotonic. | Failure before replacement leaves live bytes unchanged. A failed pin discard retains rather than invalidates the possibly pinned stage. Replacement completion followed by durability, promotion, activation, reconciliation, or final-check failure returns provider-neutral `OutcomeUnknown` and never claims rollback. | Cutover must preserve both filesystem durability and the claims layer's exact physical authority around the irreversible step. |

## Data And State Model

Offline operations retain no global mode. Authority is an opaque one-consumer
lease carrying one immutable StoreID/path; only its synchronous access can
check, reconcile, pin, discard an unused pin, or promote a replacement.
Temporary working/final stage names use random bytes beside the target and are
never protocol or configuration values.

## Surface Ownership

Owns: CODE internal/sqliteprovider/maintenance.go
Owns: CODE internal/sqliteprovider/provider_offline.go
Owns: CODE internal/sqliteprovider/staged_migration.go
Owns: CODE internal/sqliteprovider/staged_source.go
Owns: CODE internal/sqliteprovider/staged_cutover_*.go
Owns: TEST internal/sqliteprovider/coverage_maintenance_staged_test.go *
Owns: TEST internal/sqliteprovider/coverage_offline_additional_test.go *
Owns: TEST internal/sqliteprovider/coverage_offline_additional_unix_test.go *
Owns: TEST internal/sqliteprovider/offline_test_helpers_test.go *
Owns: TEST internal/sqliteprovider/offline_authority_test.go *
Owns: TEST internal/sqliteprovider/provider_offline_test.go *
Owns: TEST internal/sqliteprovider/real_behavior_closeout_test.go *
Owns: TEST internal/sqliteprovider/real_behavior_closeout_unix_test.go *
Owns: TEST internal/sqliteprovider/staged_migration_test.go *
Owns: TEST internal/sqliteprovider/staged_migration_closeout_test.go *
Owns: TEST internal/sqliteprovider/staged_source_test.go *
Owns: TEST internal/sqliteprovider/staged_source_closeout_test.go *
Owns: TEST internal/sqliteprovider/staged_source_ops_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `NewImmutableGenerationSource`, `ImmutableGenerationSource` | Let only the guarded backup bridge bind one manifest-verified StoreID to a sealed source path exposed around one synchronous provider byte-copy callback. | `FR-DATABASE-SQLITE-OFFLINE-001` |
| Internal Go API | `MigrateStagedOfflineFrom`, `StagedMigration`, `StagedValidation`, `MaintenanceResult` | Consume one target lease; copy, normalize, migrate, validate, atomically install, activate, and reconcile one disposable generation. | `FR-DATABASE-SQLITE-OFFLINE-001`, `FR-DATABASE-SQLITE-OFFLINE-002`, `FR-DATABASE-SQLITE-OFFLINE-003` |

## Algorithms And Ordering

1. Validate the immutable source capability, consume one finite child lease,
   require its StoreID to match, derive its exact live target, and check it.
2. Allocate a random working namespace; call the immutable source once to copy
   every coherent generation member through retained file handles, sync it, and
   wait for the source's post-use seal.
3. Recover/integrity-check and normalize only that working copy, retaining its
   pre-migration schema version, then SQLite-backup it into a final stage.
4. Invoke migration, provider validation, and domain validation on the final
   stage; close all handles and recheck identities and sidecar absence.
5. Check authority, pin the exact stage, check again, replace the untouched live
   target, promote the replacement immediately, then activate WAL, reopen,
   validate, and reconcile new sidecars.
6. Under a fresh timeout no longer than one second, remove identity-proven
   unpinned or successfully discarded stages for known pre-cutover outcomes;
   retain a stage whose integrity or pin retirement could not be proved; report
   uncertainty after possible replacement without automatic restoration.

## Cross-Feature Behavior

The provider core supplies security/open/configuration; SQLite control supplies
integrity/version checks; the neutral provider lease carries claims-owned
authority without a package cycle. A later guarded backup-consumption slice is
the sole approved caller of `NewImmutableGenerationSource` and will bind the
backup manifest's exact StoreID; later migration orchestration will
mint/revoke/drain child leases and hold the claims migration guard throughout.

## Failure And Edge Cases

- A caller cannot substitute the live target or retain the immutable source
  callback/path after its synchronous scope.
- Source, migration, and validation callback panics become bounded errors and
  never escape across the provider.
- Active target or stage sidecars reject replacement.
- Raw-copy and online-backup loops check cancellation and close every handle.
- Ordinary reconciliation never runs between main replacement and exact
  replacement promotion.
- Installed reopen failure is unknown outcome, not a pre-cutover rollback.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-OFFLINE-001` | [internal/sqliteprovider/provider_offline_test.go](../../internal/sqliteprovider/provider_offline_test.go), [internal/sqliteprovider/staged_source_test.go](../../internal/sqliteprovider/staged_source_test.go), [internal/sqliteprovider/staged_source_closeout_test.go](../../internal/sqliteprovider/staged_source_closeout_test.go) |
| `FR-DATABASE-SQLITE-OFFLINE-002` | [internal/sqliteprovider/coverage_maintenance_staged_test.go](../../internal/sqliteprovider/coverage_maintenance_staged_test.go), [internal/sqliteprovider/real_behavior_closeout_test.go](../../internal/sqliteprovider/real_behavior_closeout_test.go), [internal/sqliteprovider/staged_migration_test.go](../../internal/sqliteprovider/staged_migration_test.go), [internal/sqliteprovider/staged_migration_closeout_test.go](../../internal/sqliteprovider/staged_migration_closeout_test.go) |
| `FR-DATABASE-SQLITE-OFFLINE-003` | [internal/sqliteprovider/provider_offline_test.go](../../internal/sqliteprovider/provider_offline_test.go), [internal/sqliteprovider/staged_migration_test.go](../../internal/sqliteprovider/staged_migration_test.go), [internal/sqliteprovider/staged_migration_closeout_test.go](../../internal/sqliteprovider/staged_migration_closeout_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/maintenance.go](../../internal/sqliteprovider/maintenance.go)
- [internal/sqliteprovider/provider_offline.go](../../internal/sqliteprovider/provider_offline.go)
- [internal/sqliteprovider/staged_migration.go](../../internal/sqliteprovider/staged_migration.go)
- [internal/sqliteprovider/staged_source.go](../../internal/sqliteprovider/staged_source.go)
