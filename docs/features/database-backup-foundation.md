# Database Backup Filesystem Foundation

## Feature ID

`FR-DATABASE-BACKUP-FOUNDATION`

## Behavior Summary

PicoClaw provides dormant, bounded backup filesystem primitives that copy, rehash, identity-bind, publish, and remove only captured private evidence.

## Reconstruction Notes

- Descendant access is relative to pinned parent/root handles.
- Copy verifies source/output identity, ownership, and link metadata before and after IO, then reopens and rehashes output.
- Hash verification binds prior metadata and full identity to the current path/handle.
- Windows uses private DACLs and 128-bit IDs; Unix files are private and single-link.
- Parent selection performs caller-cancelable, aggregate-bounded physical
  containment scans from retained directory handles before exclusive creation.
- Cleanup preflights the whole tree without crossing a mount boundary, quarantines each leaf relative to a retained parent, revalidates identity and mount binding, then uses retained Unix directory handles or exact write-through Windows handles.
- `backup.go` bridges private-directory creation; `backup_io.go` stays provider-free.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-BACKUP-FOUNDATION-001` | MUST | A caller creates or inspects a private backup directory/object. | Path, opened handle, object type, privacy metadata, and full identity agree. | May create and durably sync private directories/files. | Symlink/reparse, device, public DACL/mode, hard link, read-only, replacement, or unsupported platform fails closed. | Lexical paths alone cannot prove the object used. |
| `FR-DATABASE-BACKUP-FOUNDATION-002` | MUST | A bounded current-user source is copied to a private destination. | Output bytes, returned source identity, size, and digest match a stable source; reopened output retains the captured identity. | Exclusively creates, chmods/secures, fsyncs, and parent-syncs output. | Cancellation, foreign ownership, hard-link drift, short/no-progress IO, source/output transition, close/sync failure, or post-copy mismatch cleans only owned output and returns error. | Archive records must validate independently and name exact bytes. |
| `FR-DATABASE-BACKUP-FOUNDATION-003` | MUST | Under caller-held exclusive mutation authority, cleanup receives one expected file/tree identity. | Only captured identities on the original mounted filesystem are quarantined and removed relative to retained handles. | Full preflight, no-replace rename, bounded child unlink, top-level unlink, and retained-parent sync; Windows renames/disposes exact handles. | Identity/type/parent/mount drift, alias, unsafe child, entry bound, or observed inventory change preserves evidence and never recursively traverses a substitute or mounted tree. | Cleanup must not delete a replacement tree or externally mounted content. |
| `FR-DATABASE-BACKUP-FOUNDATION-004` | MUST | Trusted orchestration supplies a canonical home, catalog paths, and optional backup parent. | The selected private parent is lexically and physically outside every generation and legacy input, bound to one identity, and safe to create. | A missing default parent is created exclusively; an owned empty parent may be identity-bound quarantined and rolled back. | Cancellation, symlink/reparse, ancestor or mount-view alias, path/identity drift, lost exclusive-create race, nonempty rollback, or traversal bound fails closed without removing unowned content. | Backup evidence must never capture itself or mutate a source namespace through an alias. |

## Data And State Model

Opaque `fileidentity.Identity` values bind paths to opened objects. Copies return complete manifests; bounded, uncertain cleanup stays quarantined.

## Surface Ownership

Owns: CODE internal/databasemigration/backup.go
Owns: CODE internal/databasemigration/backup_io.go
Owns: CODE internal/databasemigration/backup_hash.go
Owns: CODE internal/databasemigration/backup_identity.go
Owns: CODE internal/databasemigration/backup_pinned.go
Owns: CODE internal/databasemigration/backup_metadata_*.go
Owns: CODE internal/databasemigration/backup_open_*.go
Owns: CODE internal/databasemigration/backup_exact_mount.go
Owns: CODE internal/databasemigration/backup_exact_mount_*.go
Owns: CODE internal/databasemigration/backup_exact_root_*.go
Owns: CODE internal/databasemigration/backup_parent.go
Owns: CODE internal/databasemigration/backup_publish_*.go
Owns: CODE internal/databasemigration/backup_remove.go
Owns: CODE internal/databasemigration/backup_remove_platform_*.go
Owns: CODE internal/databasemigration/backup_foundation_dormant.go
Owns: TEST internal/databasemigration/backup_foundation_faults_test.go *
Owns: TEST internal/databasemigration/backup_foundation_coverage_test.go *
Owns: TEST internal/databasemigration/backup_io_coverage_test.go *
Owns: TEST internal/databasemigration/backup_validation_hardening_test.go *
Owns: TEST internal/databasemigration/backup_pinned_faults_test.go *
Owns: TEST internal/databasemigration/backup_remove_identity_test.go *
Owns: TEST internal/databasemigration/backup_remove_coverage_test.go *
Owns: TEST internal/databasemigration/backup_exact_mount_test.go *
Owns: TEST internal/databasemigration/backup_exact_mount_*_test.go *
Owns: TEST internal/databasemigration/backup_remove_mount_*_test.go *
Owns: TEST internal/databasemigration/backup_empty_parent_rollback_test.go *
Owns: TEST internal/databasemigration/backup_parent_containment_test.go *
Owns: TEST internal/databasemigration/backup_parent_mount_*_test.go *
Owns: TEST internal/databasemigration/backup_parent_coverage_test.go *
Owns: TEST internal/databasemigration/backup_foundation_hardening_coverage_test.go *
Owns: TEST internal/databasemigration/backup_source_owner_*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `copyBackupFile` | Produce a self-validating private copy record. | `FR-DATABASE-BACKUP-FOUNDATION-002` |
| Internal Go API | `openPinnedBackupPath` | Prove path/opened identity and object type. | `FR-DATABASE-BACKUP-FOUNDATION-001` |
| Internal Go API | `removePinnedBackupTreeIdentity` | Quarantine/remove only an expected identity. | `FR-DATABASE-BACKUP-FOUNDATION-003` |
| Internal Go API | `validateBackupParent`, `removePinnedEmptyBackupDirectoryIdentity` | Prove physical namespace separation and roll back only an exclusively created empty parent. | `FR-DATABASE-BACKUP-FOUNDATION-004` |

## Algorithms And Ordering

Validate ancestors; secure destination; copy bounded bytes; recheck, sync, reopen, and rehash output.
Cleanup mount-checks each root and child before opening a recursive root, preflights inventory, no-replace renames each leaf through a retained parent, proves source absence and quarantine identity, then removes only that quarantine.
Rollback of a newly created archive parent uses the same identity-bound quarantine but only an atomic empty-directory removal; it never traverses descendants.
Unix syncs retained directory descriptors; Windows marks exact handles for durable logical deletion before later physical reclamation.

## Cross-Feature Behavior

D4a1 supplies the model. D4b composes archives and status; D4c reconstructs
sealed inputs. D5 owns claims, quiescence, and cutover.

## Failure And Edge Cases

Unsupported identity, metadata, or atomic publication fails closed. D5 must exclude concurrent writers. Detected drift fails closed; same-user processes
ignoring that authority boundary remain outside this primitive's threat model.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-BACKUP-FOUNDATION-001`, `FR-DATABASE-BACKUP-FOUNDATION-002` | [backup_foundation_faults_test.go](../../internal/databasemigration/backup_foundation_faults_test.go), [backup_io_coverage_test.go](../../internal/databasemigration/backup_io_coverage_test.go) |
| `FR-DATABASE-BACKUP-FOUNDATION-003` | [backup_remove_identity_test.go](../../internal/databasemigration/backup_remove_identity_test.go), [backup_remove_mount_linux_test.go](../../internal/databasemigration/backup_remove_mount_linux_test.go), [backup_pinned_faults_test.go](../../internal/databasemigration/backup_pinned_faults_test.go) |
| `FR-DATABASE-BACKUP-FOUNDATION-004` | [backup_parent_containment_test.go](../../internal/databasemigration/backup_parent_containment_test.go), [backup_parent_mount_linux_test.go](../../internal/databasemigration/backup_parent_mount_linux_test.go), [backup_empty_parent_rollback_test.go](../../internal/databasemigration/backup_empty_parent_rollback_test.go) |

## Implementation Anchors

[backup.go](../../internal/databasemigration/backup.go), [backup_io.go](../../internal/databasemigration/backup_io.go), and [backup_pinned.go](../../internal/databasemigration/backup_pinned.go).
