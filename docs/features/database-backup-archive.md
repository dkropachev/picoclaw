# Database Backup Archive

## Feature ID

`FR-DATABASE-BACKUP-ARCHIVE`

## Behavior Summary

PicoClaw provides dormant internal creation, loading, verification, and status
primitives for committed byte-exact database archives. Capture is valid only
after D5 has fenced and quiesced selected stores. This feature neither acquires
claims nor opens SQLite.

## Reconstruction Notes

- A private `.partial` tree becomes committed only after payloads, canonical
  manifest, and a final SHA-256 marker are synced and no-replace published.
- `backupSession` pins the archive and its owner-private parent identity.
- Backup-parent selection proves two-way physical separation from every catalog
  source: bounded ancestor checks cover parent-above-source aliases, and one
  aggregate-bounded directory scan covers every legacy-root view. Distinct
  bind-mount views are never deduplicated by backing inode.
- Final live-source verification indexes global exclusions and manifest records
  once, then verifies selected stores without selected×catalog amplification.
- Migration status is a separate sibling state machine: absent → revision-1
  `migration_in_progress` → exactly one revision-2 terminal outcome.
- Status creation is exclusive; terminal exchange holds the revision-1 inode
  lock and rereads through that lock-owning handle before atomically exchanging
  only the expected identity/bytes. Placeholder cleanup is identity-bound.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-BACKUP-ARCHIVE-001` | MUST | Externally quiesced orchestration supplies the canonical catalog and selected subset. | A marker-complete, exact, private archive whose final live rescan matches every copied source. | Creates, syncs, no-replace publishes, and parent-syncs one archive. | Catalog/provenance drift, unsafe sources, aliasing, sidecar incoherence, bounds, cancellation, sync, or publication failure returns no usable session. | Only committed recovery evidence may authorize later migration. |
| `FR-DATABASE-BACKUP-ARCHIVE-002` | MUST | Trusted code loads or verifies an archive/session/store. | Durable manifest/marker/payload tree and in-memory inventory agree by bytes, metadata, and stable identity maps. | Read-only. | Partial, extra, missing, linked, reparsed, public, replaced, noncanonical, or hash-mismatched evidence fails closed. | Recovery evidence must remain immutable across verification. |
| `FR-DATABASE-BACKUP-ARCHIVE-003` | MUST | Orchestration records migration progress and one terminal result. | Owner-private digest-bound status advances monotonically through revisions 1 and 2. | Exclusive create then locked atomic exchange of only the expected incumbent. | Direct terminal, replay, stale identity/revision/bytes, concurrent writer, parent/archive drift, or post-exchange uncertainty is explicit. | Participating stale writers must not rewrite operational truth. |

## Data And State Model

The immutable archive contains payloads, `manifest.json`, and
`snapshot.complete.sha256`; hidden `.partial` siblings remain inert crash
evidence. Status lives at the validated sibling
`<archive>.migration-status.json`. Terminal outcomes are `dry_run`, `complete`,
`failed`, `outcome_unknown`, or `complete_with_cleanup_error`, with strict error
pairing.

## Surface Ownership

Owns: CODE internal/databasemigration/backup_archive.go
Owns: CODE internal/databasemigration/backup_commit.go
Owns: CODE internal/databasemigration/backup_manifest_json.go
Owns: CODE internal/databasemigration/backup_session.go
Owns: CODE internal/databasemigration/backup_exact_tree.go
Owns: CODE internal/databasemigration/backup_status_bridge.go
Owns: CODE internal/databasemigration/backup_status.go
Owns: CODE internal/databasemigration/backup_status_ops.go
Owns: CODE internal/databasemigration/backup_status_exchange_*.go
Owns: CODE internal/databasemigration/backup_status_lock_*.go
Owns: TEST internal/databasemigration/backup_core_faults_test.go *
Owns: TEST internal/databasemigration/backup_identity_hardening_test.go *
Owns: TEST internal/databasemigration/backup_archive_commit_load_test.go *
Owns: TEST internal/databasemigration/backup_archive_verification_test.go *
Owns: TEST internal/databasemigration/backup_archive_closeout_test.go *
Owns: TEST internal/databasemigration/backup_exact_tree_coverage_test.go *
Owns: TEST internal/databasemigration/backup_manifest_json_test.go *
Owns: TEST internal/databasemigration/backup_status_*_test.go *
Owns: TEST internal/databasemigration/coverage_snapshot_additional_test.go *
Owns: TEST internal/databasemigration/coverage_snapshot_faults_test.go *
Owns: TEST internal/databasemigration/live_sources_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `snapshotBackup`, `BackupManifest` | Commit bounded offline evidence from externally quiesced inputs. | `FR-DATABASE-BACKUP-ARCHIVE-001` |
| Internal Go API | `loadBackupSession`, `backupSession.verify`, `verifyLiveSources` | Load and prove exact committed evidence/live provenance. | `FR-DATABASE-BACKUP-ARCHIVE-002` |
| Internal Go API | `backupSession.finish`, `readMigrationStatus` | Advance or read the monotonic digest-bound status record. | `FR-DATABASE-BACKUP-ARCHIVE-003` |

## Algorithms And Ordering

Validate catalog and prospective parent ancestry; create and pin the private
parent; physically revalidate that pinned identity against all catalog inputs;
empty-only rollback a newly created parent on pre-staging containment rejection;
pin staging; copy bounded generation
and legacy bytes; sort/validate inventory; perform one shared-index final live
rescan; write and sync manifest then marker; exact-verify; no-replace rename;
sync parent; exact-verify again.
Status operations verify archive and parent, lock/read/reread the incumbent
through one handle, create or exchange the next canonical revision, sync,
re-read exact identity/bytes, and verify the archive again.

## Cross-Feature Behavior

D4a owns filesystem safety. D4c consumes only verified archive bytes. D5 must
wrap archive/status operations in its refreshing guard, use canonical detached
specs, reconcile before release, and own quiescence/flush.

## Failure And Edge Cases

Post-publication parent-sync failure leaves deterministic marker-complete crash
evidence but reports failure.
Post-status-exchange proof failure reports an uncertain committed outcome and
never silently restores arbitrary bytes. Subprocess tests prove exactly one
participating concurrent status writer wins.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-BACKUP-ARCHIVE-001`, `FR-DATABASE-BACKUP-ARCHIVE-002` | [coverage_snapshot_faults_test.go](../../internal/databasemigration/coverage_snapshot_faults_test.go), [backup_core_faults_test.go](../../internal/databasemigration/backup_core_faults_test.go) |
| `FR-DATABASE-BACKUP-ARCHIVE-003` | [backup_status_state_test.go](../../internal/databasemigration/backup_status_state_test.go), [backup_status_fault_test.go](../../internal/databasemigration/backup_status_fault_test.go), [backup_status_subprocess_test.go](../../internal/databasemigration/backup_status_subprocess_test.go) |

## Implementation Anchors

- [backup_archive.go](../../internal/databasemigration/backup_archive.go)
- [backup_commit.go](../../internal/databasemigration/backup_commit.go)
- [backup_status.go](../../internal/databasemigration/backup_status.go)
- [backup_status_bridge.go](../../internal/databasemigration/backup_status_bridge.go)
