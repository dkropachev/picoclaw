# Database SQLite Offline Operations

## Feature ID

`FR-DATABASE-SQLITE-OFFLINE`

## Behavior Summary

PicoClaw defines dormant provider-owned staged replacement for one exact SQLite
target. The provider consumes one opaque, target-bound child lease, copies a
descriptor-pinned immutable backup generation without opening it through
SQLite, normalizes and migrates only provider-owned working files, retires its
now-unused source stage, then validates one exactly pinned replacement stage
before an atomic live-name replacement.

No runtime or CLI calls these operations in this stage. They acquire no fence,
perform no backup themselves, and change no application persistence route.

## Reconstruction Notes

- Similarity target: isolate provider recovery and independently committing
  domain upgrades from the live generation until validated cutover.
- Core APIs: `MigrateStagedOfflineFrom`,
  `MigrateStagedOfflineFromWithLiveVerification`,
  `ImmutableGenerationSource`, `StagedMigration`, `StagedValidation`,
  `StagedLiveVerification`, `ValidatedReplacement`,
  `ValidatedReplacementCheck`, and `MaintenanceResult`.
- Runtime ordering: consume the exact target lease; invoke the immutable source
  once to byte-copy its complete main/WAL/SHM/journal generation; wait for the
  source's post-use seal; recover and normalize the working copy; SQLite-backup
  it to a final stage; invoke migration and domain validation; recheck the final
  stage and target; remove and sync the now-unused working copy; pin the final
  stage; invoke final live-source validation with an opaque pinned-stage scope; replace,
  reconcile, activate, and finally reconcile any installed sidecars.
- Non-obvious constraints: no raw source path escapes its synchronous callback;
  no SQLite connection touches the immutable source or live target before
  cutover; the temporary source stage is never a live-verification exclusion;
  stage sidecars are never exclusions; every known pre-replacement failure
  preserves live bytes; post-replacement uncertainty is `OutcomeUnknown`.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-OFFLINE-001` | MUST | A caller supplies one finite child lease and an opaque immutable-generation source minted only by the manifest-validating backup bridge. | The source immutably binds its verified `StoreID`; the provider requires equality with the lease target before invoking it. `MigrateStagedOfflineFrom` consumes the lease exactly once, derives the live target only from it, checks authority before filesystem/SQLite phases, and invokes the source callback exactly once. The inner path-use callback is synchronous and single-use. It copies a bounded coherent main/WAL/SHM/journal inventory into a random private working namespace using exclusive `0600` files, per-file identity/size/digest checks, file and directory sync, and context checks. Each destination member is retained as soon as it is created; failure cleanup retires those exact identities, while successful copy transfers the main retention continuously into normalization. Source identity, bytes, modes, and inventory are rechecked before the callback returns; only after its outer post-use seal returns may SQLite open the working copy. | Only provider-owned working files are created before cutover; the immutable source and live target remain byte-identical. | Nil/revoked/consumed/expired lease, source/target `StoreID` mismatch, unauthorized source minting, invalid callback use, alias with the target, unsafe/incoherent/oversized/replaced source, copy/sync failure, or cancellation fails before SQLite opens the source or target. A renamed partial member or decoy is preserved and reported rather than mistaken for successful cleanup. | Backup evidence must remain store-bound, immutable, and descriptor-pinned while crossing into provider-owned mutable state. |
| `FR-DATABASE-SQLITE-OFFLINE-002` | MUST | The sealed source copy and live target have matching existence and the caller supplies bounded migration and validation callbacks. | On the mutable working copy, the provider recovers/checks integrity, fully checkpoints WAL, commits an exclusive DELETE-journal boundary without schema change, restores WAL/normal locking, truncation-checkpoints, reopens, and requires the same pre-migration version. It SQLite-backups that copy into the final stage, or creates and syncs an empty provider stage for a missing target, and retains that exact main before exposing its path to migration code. It invokes migration, verifies the exact expected version and integrity, invokes domain validation, then rechecks the final stage identity, target identity, and absent WAL/SHM/journal. Immediately before final live-source validation, it conclusively retires the retained working-source identity and proves that only the retained final stage remains. `MaintenanceResult` reports the recovered before-version and installed after-version. | Recovery and journal metadata change the provider-owned working source; schema and application rows change only in the provider-owned final stage. Exact main retirement uses retained parent/file identities, no-replace quarantine, exact unlink/disposition proof, and supported parent durability. | Source/target existence mismatch, corruption, busy state, callback error/panic, wrong version, target change, active sidecar, cancellation, pre-cleanup rename/replacement/hardlink, or unproved working-source cleanup fails before replacement. A renamed/replaced or unvalidated stage and every ambiguous cleanup remain bounded diagnostic state; pathname absence alone never proves retirement and no substitute is deleted. | Independently committing domain migrations must never operate directly on live or immutable backup bytes, and final source verification must never need to ignore two provider stages. |
| `FR-DATABASE-SQLITE-OFFLINE-003` | MUST | One provider-validated final stage reaches the live-name boundary after the working source has been retired. | The provider retains the exact final main before migration code receives its path, and immediately after final provider/domain validation opens and hashes those exact bytes. It checks authority, pins that main to its StoreID and lease target, proves the claims pin still names both retained objects, then invokes final live-source validation with one opaque synchronous pinned-stage scope. That scope binds the invocation, target, canonical derived path, retained-handle identity, validated-byte digest, and a sequential exact-entry checker. The checker compares the walker's actual `Lstat` observation with the retained handle and returns that handle-derived identity for alias accounting; the dynamic path never enters a platform-folded exclusion map. The provider drains checks, rehashes and rechecks stage/target/sidecars and the claims pin, then atomically replaces the live main. A completed or uncertain replacement is promoted before ordinary reconciliation; activation, reopen, integrity/version checks, and sidecar reconciliation precede success. | The live main identity changes exactly once; no live or stage sidecar is carried across cutover. Physical claims and replacement assignments remain monotonic. | Callback error/panic, omitted/repeated/concurrent/escaped check or scope use, stale/foreign scope, stage entry replacement, same-metadata content mutation, new stage sidecar, target drift, or another known pre-replacement failure first discards the unused pin and only then permits identity-bound retirement. Failed/ambiguous pin discard or identity retirement retains evidence and never deletes a decoy. Post-replacement uncertainty returns provider-neutral `OutcomeUnknown`. | Cutover and its immediately preceding live-source proof must share the claims layer's exact physical and validated-content authority around the irreversible step. |

## Data And State Model

Offline operations retain no global mode. Authority is an opaque one-consumer
lease carrying one immutable StoreID/path; only its synchronous access can
check, reconcile, pin, recheck the exact active pin, discard an unused pin, or
promote a replacement. Temporary working/final stage names use random bytes
beside the target and are never protocol or configuration values. The working
source is retired before final live-source validation; the surviving final-stage scope is
opaque, one-use, callback-bounded, and invalid after validation returns.

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
Owns: TEST internal/sqliteprovider/staged_coverage_delta_test.go *
Owns: TEST internal/sqliteprovider/staged_coverage_delta_linux_test.go *
Owns: TEST internal/sqliteprovider/staged_source_test.go *
Owns: TEST internal/sqliteprovider/staged_source_closeout_test.go *
Owns: TEST internal/sqliteprovider/staged_source_ops_test.go *
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderValidatedReplacementBoundaryIsNarrow
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderValidatedReplacementBoundaryRejectsUnauthorizedConsumers

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `NewImmutableGenerationSource`, `ImmutableGenerationSource` | Let only the guarded backup bridge bind one manifest-verified StoreID to a sealed source path exposed around one synchronous provider byte-copy callback. | `FR-DATABASE-SQLITE-OFFLINE-001` |
| Internal Go API | `MigrateStagedOfflineFrom`, `MigrateStagedOfflineFromWithLiveVerification`, `StagedMigration`, `StagedValidation`, `StagedLiveVerification`, `ValidatedReplacement`, `ValidatedReplacementCheck`, `MaintenanceResult` | Consume one target lease; copy and normalize a temporary source, migrate and validate one retained final stage, retire the source, pin and scope final live-source validation, then atomically install, activate, and reconcile the final generation. The compatibility entry point omits final live-source validation; orchestration uses the explicit required-callback variant. | `FR-DATABASE-SQLITE-OFFLINE-001`, `FR-DATABASE-SQLITE-OFFLINE-002`, `FR-DATABASE-SQLITE-OFFLINE-003` |

## Algorithms And Ordering

1. Validate the immutable source capability, consume one finite child lease,
   require its StoreID to match, derive its exact live target, and check it.
2. Allocate a random working namespace; call the immutable source once to copy
   every coherent generation member through retained file handles, sync it, and
   wait for the source's post-use seal. Retain each destination identity at
   creation and transfer the main retention without a pathname-only cleanup gap.
3. Recover/integrity-check and normalize only that working copy, retain its
   main and pre-migration schema version, then SQLite-backup it into a final
   stage. For a missing source, create/sync the empty final main first. Retain
   that final main before invoking migration code.
4. Invoke migration on the final stage, provider-validate it, invoke domain
   validation, recheck its identity, target identity, and absent sidecars, then
   open and hash-seal those validated bytes through the retained pathname.
5. No-replace quarantine, unlink/dispose, prove, and parent-sync the retained
   working source; abort if retirement is not conclusive. Check authority, pin
   the exact final main, recheck both claims and provider-retained identities,
   and invoke final live-source validation through one opaque pinned-stage
   scope.
   Windows long-lived retention requests no delete access, so SQLite's
   read/write-only sharing remains compatible; only after SQLite closes does
   `ReOpenFile` add delete access to the same retained identity for retirement.
6. Recheck the final stage, target, and absent sidecars; replace the untouched
   live target, promote the replacement immediately, then activate WAL, reopen,
   validate, and reconcile new sidecars.
7. Under a fresh timeout no longer than one second, remove identity-proven
   unpinned or successfully discarded stages for known pre-cutover outcomes;
   retain a stage whose integrity or pin retirement could not be proved; report
   uncertainty after possible replacement without automatic restoration.

## Cross-Feature Behavior

The provider core supplies security/open/configuration; SQLite control supplies
integrity/version checks; the neutral provider lease carries claims-owned
authority without a package cycle. Guarded backup consumption is the sole
approved caller of `NewImmutableGenerationSource` and binds the backup
manifest's exact StoreID. Later migration orchestration will mint, revoke, and
drain child leases while holding the claims migration guard throughout.

## Failure And Edge Cases

- A caller cannot substitute the live target or retain the immutable source
  callback/path after its synchronous scope.
- Source, migration, and validation callback panics become bounded errors and
  never escape across the provider.
- Failure to retire the temporary working source blocks final live-source
  validation; it is never hidden from a nested legacy-root scan. Pathname
  absence, a moved retained inode, or a replacement decoy is never cleanup
  success.
- Active target or final-stage sidecars reject validation and replacement.
- Final-stage WAL, SHM, and journal names never enter the exclusion capability.
- Raw-copy and online-backup loops check cancellation and close every handle.
- Ordinary reconciliation never runs between main replacement and exact
  replacement promotion.
- The fence, complete physical claims, private directory, and caller-established
  quiescence exclude uncoordinated same-UID raw writers. Stat/hash sequencing
  detects persistent drift but does not claim an OS lock against an omnipotent
  process that can rewrite and restore bytes between every finite check.
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
