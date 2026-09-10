# Database Backup Consumption

## Feature ID

`FR-DATABASE-BACKUP-CONSUMPTION`

## Behavior Summary

PicoClaw provides dormant guarded-use capabilities that reconstruct one store
generation or its ordered legacy inputs exclusively from a verified archive.
A generation crosses into the SQLite provider only as an opaque, StoreID-bound
source capability. Raw prepared paths exist only inside synchronous callbacks
protected by before/after seals.

## Reconstruction Notes

- `backup.go`-level catalog specs are immediately converted to detached backup
  provenance; seal implementations cannot import the physical catalog.
- Generation sealing derives the exact recognized role set, sizes, and digests
  from the manifest and rejects any missing or extra member.
- Legacy sealing retains the exact directory/member identity graph, ordered root
  descriptors, privacy metadata, single-link/reparse state, and hashes.
- `backup_prepare.go` is the sole production mint for
  `sqliteprovider.ImmutableGenerationSource`; it binds the manifest-validated
  StoreID to `preparedGeneration.use` without returning a caller-chosen path.
- Guarded callbacks are only for copying source bytes into a provider-owned
  private stage. The post-use seal must finish before SQLite opens the copy or
  later orchestration pins or publishes it.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-BACKUP-CONSUMPTION-001` | MUST | Trusted orchestration requests one manifest-bound generation. | A descriptor-pinned `preparedGeneration` mints one opaque provider source bound to the manifest StoreID; only its guarded synchronous callback can observe the path. | Recreates a private disposable generation from archive bytes. | Provenance, role presence, size/hash, identity, privacy, inventory, source minting, or post-use drift fails closed. | Providers must never consume mutable live paths, caller-selected targets, or unsealed reconstructed bytes. |
| `FR-DATABASE-BACKUP-CONSUMPTION-002` | MUST | Trusted orchestration requests exact ordered legacy roots. | `preparedLegacyInputs.use` supplies cloned ordered paths while exact tree/metadata/hash seals hold before and after. | Recreates private bounded disposable roots from archive bytes. | Root kind/order, wrapper/path budget, alias, link/reparse, descriptor, inventory, byte, or callback drift fails closed. | Adapters require provenance-bound immutable input views. |
| `FR-DATABASE-BACKUP-CONSUMPTION-003` | MUST | Caller closes a prepared capability. | Descriptors close once and only its captured work-root identity is quarantined and removed. | Durably removes private disposable evidence. | Replaced parent/root, close, cleanup, or sync failure is observable and never deletes the replacement. | Cleanup safety is part of the migration result. |

## Data And State Model

`backupStoreProvenance` carries only the selected store ID, canonical path, and
cloned ordered legacy roots. Prepared capabilities retain root handles, stable
identities, exact expected inventory, member handles, sizes, metadata, and
manifest digests. They are mutex-serialized and become unusable after close.

## Surface Ownership

Owns: CODE internal/databasemigration/backup_prepare.go
Owns: CODE internal/databasemigration/backup_prepare_temp.go
Owns: CODE internal/databasemigration/backup_provenance.go
Owns: CODE internal/databasemigration/prepared_generation.go
Owns: CODE internal/databasemigration/prepared_legacy.go
Owns: TEST internal/databasemigration/prepared_generation_additional_test.go *
Owns: TEST internal/databasemigration/backup_prepare_security_test.go *
Owns: TEST internal/databasemigration/backup_prepare_provider_test.go *
Owns: TEST internal/databasemigration/backup_consumption_closeout_test.go *
Owns: TEST internal/databasemigration/prepared_generation_faults_test.go *
Owns: TEST internal/databasemigration/prepared_inputs_hardening_test.go *
Owns: TEST internal/databasemigration/prepared_legacy_faults_test.go *
Owns: TEST internal/databasemigration/identity_index_test.go *
Owns: TEST internal/databasemigration/backup_windows_test.go *
Owns: TEST internal/databasemigration/coverage_live_prepare_additional_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `prepareImmutableGenerationSource`, `prepareGeneration`, `preparedGeneration.use` | Recreate one exact generation and mint its StoreID-bound provider source without exposing an ambient path. | `FR-DATABASE-BACKUP-CONSUMPTION-001` |
| Internal Go API | `prepareLegacyInputs`, `preparedLegacyInputs.use` | Recreate and synchronously expose ordered exact legacy roots. | `FR-DATABASE-BACKUP-CONSUMPTION-002` |

## Algorithms And Ordering

Verify the selected archive; create and pin a private backup-adjacent work root;
copy each manifest member with reopen/rehash; verify the archive again; derive
and seal the exact output inventory. `use` locks, verifies namespace,
identities, metadata, and hashes, invokes the synchronous copy callback, then
repeats the seal without inheriting callback cancellation. Cleanup closes all
descriptors before identity-bound quarantine removal.

## Cross-Feature Behavior

D4a supplies identity-safe copy/cleanup and D4b supplies committed archive
verification. The SQLite offline provider consumes the opaque source only to
construct separately owned working state. Later migration orchestration must
hold the refreshing guard/fence, mint a finite provider lease, and reconcile
claims before releasing either capability.

## Failure And Edge Cases

Missing generations are represented by a valid empty generation seal. Missing
legacy roots retain their ordered nonexistent destination paths. Cancellation,
same-size byte replacement, public metadata, hard links, reparse points, extra
entries, closed descriptors, and root replacement are all explicit failures.
As with physical claims, same-user processes holding equivalent write/search
authority must participate in the fence protocol: retained descriptors and
before/after seals detect ordinary drift but cannot prevent a deliberately
timed replace-and-restore ABA by a nonparticipating writer with that authority.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-BACKUP-CONSUMPTION-001` | [prepared_generation_additional_test.go](../../internal/databasemigration/prepared_generation_additional_test.go), [prepared_generation_faults_test.go](../../internal/databasemigration/prepared_generation_faults_test.go), [backup_prepare_provider_test.go](../../internal/databasemigration/backup_prepare_provider_test.go) |
| `FR-DATABASE-BACKUP-CONSUMPTION-002`, `FR-DATABASE-BACKUP-CONSUMPTION-003` | [prepared_legacy_faults_test.go](../../internal/databasemigration/prepared_legacy_faults_test.go), [prepared_inputs_hardening_test.go](../../internal/databasemigration/prepared_inputs_hardening_test.go) |

## Implementation Anchors

- [backup_prepare.go](../../internal/databasemigration/backup_prepare.go)
- [prepared_generation.go](../../internal/databasemigration/prepared_generation.go)
- [prepared_legacy.go](../../internal/databasemigration/prepared_legacy.go)
