# Database Backup Filesystem Foundation

## Feature ID

`FR-DATABASE-BACKUP-FOUNDATION`

## Behavior Summary

PicoClaw provides dormant filesystem primitives for private, bounded backup
evidence. They create private directories, copy and rehash bytes, prove full
path/handle identity and metadata, publish without replacement, and quarantine
and remove only captured identities. No runtime path calls these primitives.

## Reconstruction Notes

- Descendant access is relative to pinned parent/root handles.
- Source and output identities are verified before/after copy; the closed
  output is reopened, metadata-validated, and rehashed before retention.
- Hash verification binds both prior metadata and the caller's full-width
  `fileidentity.Identity` to the current path/opened handle.
- Windows owner-only DACLs, no reparse/device/readonly state, and full 128-bit
  file IDs are authoritative; Unix files must be private and single-link.
- Cleanup preflights the complete bounded tree before mutation. It atomically
  quarantines every expected leaf relative to a retained parent handle and
  revalidates the new name against the already-open identity before traversal
  or deletion. Unix syncs the retained directory handle; Windows renames and
  disposes the exact reopened object handle with write-through semantics.
- `backup.go` is the preapproved bridge to the provider's cross-platform
  private-directory creation primitive; `backup_io.go` stays provider-free.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-BACKUP-FOUNDATION-001` | MUST | A caller creates or inspects a private backup directory/object. | Path, opened handle, object type, privacy metadata, and full identity agree. | May create and durably sync private directories/files. | Symlink/reparse, device, public DACL/mode, hard link, read-only, replacement, or unsupported platform fails closed. | Lexical paths alone cannot prove the object used. |
| `FR-DATABASE-BACKUP-FOUNDATION-002` | MUST | A bounded source is copied to a private destination. | Output bytes, returned source identity, size, and digest match a stable source; reopened output retains the captured identity. | Exclusively creates, chmods/secures, fsyncs, and parent-syncs output. | Cancellation, short/no-progress IO, source/output transition, close/sync failure, or post-copy mismatch cleans only owned output and returns error. | Archive records must validate independently and name exact bytes. |
| `FR-DATABASE-BACKUP-FOUNDATION-003` | MUST | Under caller-held exclusive mutation authority, cleanup receives one expected file/tree identity. | Only captured identities are quarantined and removed relative to retained handles. | Full preflight, no-replace rename, bounded child unlink, top-level unlink, and retained-parent sync; Windows renames/disposes exact handles. | Identity/type/parent drift, alias, unsafe child, entry bound, or observed inventory change preserves evidence and never recursively traverses a substitute. | Cleanup must not delete a replacement tree. |

## Data And State Model

Opaque comparable `fileidentity.Identity` values bind paths to opened regular
files/directories. Copy operations return complete `BackupFileManifest`
records, including source identity. Tree removal is limited to
`backupMaxEntries`; uncertain cleanup leaves a uniquely named quarantine.

## Surface Ownership

Owns: CODE internal/databasemigration/backup.go
Owns: CODE internal/databasemigration/backup_io.go
Owns: CODE internal/databasemigration/backup_hash.go
Owns: CODE internal/databasemigration/backup_identity.go
Owns: CODE internal/databasemigration/backup_pinned.go
Owns: CODE internal/databasemigration/backup_metadata_*.go
Owns: CODE internal/databasemigration/backup_open_*.go
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

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `copyBackupFile` | Produce a self-validating private copy record. | `FR-DATABASE-BACKUP-FOUNDATION-002` |
| Internal Go API | `openPinnedBackupPath` | Prove path/opened identity and object type. | `FR-DATABASE-BACKUP-FOUNDATION-001` |
| Internal Go API | `removePinnedBackupTreeIdentity` | Quarantine/remove only an expected identity. | `FR-DATABASE-BACKUP-FOUNDATION-003` |

## Algorithms And Ordering

Validate ancestors; create/secure private destination; capture source/output
identities; stream bounded bytes; recheck source; sync/close output; reopen and
validate its metadata/identity/digest; sync parent. Cleanup first validates the
whole bounded inventory without mutation. Each leaf is then no-replace renamed
to an unpredictable sibling through a retained parent, the source absence and
quarantine identity are proved, and only that quarantine is traversed or
removed. Unix syncs retained directory descriptors. Windows renames and marks
the exact identity handle for deletion; later physical reclamation is
housekeeping after durable logical removal.

## Cross-Feature Behavior

D4a1 supplies the validated model and limits. D4b composes these filesystem
operations into committed archives and status. D4c reconstructs sealed inputs.
D5 alone owns claims, quiescence, and cutover.

## Failure And Edge Cases

Platforms lacking required identity, private metadata, or atomic publication
fail closed. The caller must exclude concurrent writers to the private removal
root; D5 owns that claim/quiescence contract. Detected drift still fails closed,
but an arbitrary same-user process that ignores the authority boundary is
outside this filesystem primitive's threat model.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-BACKUP-FOUNDATION-001`, `FR-DATABASE-BACKUP-FOUNDATION-002` | [backup_foundation_faults_test.go](../../internal/databasemigration/backup_foundation_faults_test.go), [backup_io_coverage_test.go](../../internal/databasemigration/backup_io_coverage_test.go) |
| `FR-DATABASE-BACKUP-FOUNDATION-003` | [backup_remove_identity_test.go](../../internal/databasemigration/backup_remove_identity_test.go), [backup_pinned_faults_test.go](../../internal/databasemigration/backup_pinned_faults_test.go) |

## Implementation Anchors

- [backup.go](../../internal/databasemigration/backup.go)
- [backup_io.go](../../internal/databasemigration/backup_io.go)
- [backup_pinned.go](../../internal/databasemigration/backup_pinned.go)
