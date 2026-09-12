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
  Immediately before cutover it may additionally consume one opaque
  provider-minted pinned-stage scope: only that scope's exact main pathname is
  omitted from legacy membership, while its physical identity remains in the
  alias deny-set and its WAL/SHM/journal names remain ordinary live inputs.
- Migration status is a separate sibling state machine: absent → revision-1
  `migration_in_progress` → exactly one revision-2 terminal outcome.
- Status creation is exclusive; terminal exchange holds the revision-1 inode
  lock and rereads through that lock-owning handle before atomically exchanging
  only the expected identity/bytes. Placeholder cleanup is identity-bound.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-BACKUP-ARCHIVE-001` | MUST | Externally quiesced orchestration supplies the canonical catalog and selected subset. | A marker-complete, exact, private archive whose final live rescan matches every copied source. | Creates, syncs, no-replace publishes, and parent-syncs one archive. | Catalog/provenance drift, unsafe sources, aliasing, sidecar incoherence, bounds, cancellation, sync, or publication failure returns no usable session. | Only committed recovery evidence may authorize later migration. |
| `FR-DATABASE-BACKUP-ARCHIVE-002` | MUST | Trusted code loads or verifies an archive/session/store, including the final live-source pass invoked with a provider-minted pinned-stage scope. | Durable manifest/marker/payload tree and in-memory inventory agree by bytes, metadata, and stable identity maps. Ordinary and backup-creation live verification uses only the immutable manifest catalog exclusions. The final pre-cutover pass admits exactly one opaque StoreID/target/invocation-bound stage scope, rechecks it before and after the whole pass, excludes only its exact canonical main path from legacy membership, and records that main identity in the shared physical-alias deny-set. Near names, a second stage, and the stage's WAL/SHM/journal are never excluded. | Read-only. The dynamic stage exclusion is invocation-local and is never written into the archive manifest. | Partial, extra, missing, linked, reparsed, public, replaced, noncanonical, hash-mismatched, stale/foreign/reused scope, stage identity drift, hardlink alias, sidecar appearance, or any other legacy drift fails closed. | Recovery evidence must remain immutable across verification, and a provider-created main nested under a legacy root must not weaken exact-tree drift detection. |
| `FR-DATABASE-BACKUP-ARCHIVE-003` | MUST | Orchestration records migration progress and one terminal result. | Owner-private digest-bound status advances monotonically through revisions 1 and 2. | Exclusive create then locked atomic exchange of only the expected incumbent. | Direct terminal, replay, stale identity/revision/bytes, concurrent writer, parent/archive drift, or post-exchange uncertainty is explicit. | Participating stale writers must not rewrite operational truth. |

## Data And State Model

The immutable archive contains payloads, `manifest.json`, and
`snapshot.complete.sha256`; hidden `.partial` siblings remain inert crash
evidence. Status lives at the validated sibling
`<archive>.migration-status.json`. Terminal outcomes are `dry_run`, `complete`,
`failed`, `outcome_unknown`, or `complete_with_cleanup_error`, with strict error
pairing. A final-stage exclusion is ephemeral capability state, never archive
data: it contributes one exact cleaned-path slot outside the platform-folded
catalog map and one provider-handle-derived physical identity only while its
synchronous provider scope remains valid.

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
Owns: TEST internal/databasemigration/live_stage_exclusion_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `snapshotBackup`, `BackupManifest` | Commit bounded offline evidence from externally quiesced inputs. | `FR-DATABASE-BACKUP-ARCHIVE-001` |
| Internal Go API | `loadBackupSession`, `backupSession.verify`, `verifyLiveSources` | Load and prove exact committed evidence/live provenance; final pre-cutover use may consume one opaque exact pinned-stage scope without changing ordinary verification. | `FR-DATABASE-BACKUP-ARCHIVE-002` |
| Internal Go API | `backupSession.finish`, `readMigrationStatus` | Advance or read the monotonic digest-bound status record. | `FR-DATABASE-BACKUP-ARCHIVE-003` |

## Algorithms And Ordering

Validate catalog and prospective parent ancestry; create and pin the private
parent; physically revalidate that pinned identity against all catalog inputs;
empty-only rollback a newly created parent on pre-staging containment rejection;
pin staging; copy bounded generation
and legacy bytes; sort/validate inventory; perform one shared-index final live
rescan; write and sync manifest then marker; exact-verify; no-replace rename;
sync parent; exact-verify again.
Immediately before provider cutover, build the same manifest-backed live index
inside one pinned-stage scope, seed only its retained-handle identity, scan and
verify every root/record, and validate the exact directory-entry observation
against that same retained handle at the skip decision. Recheck the scope after
final root inspection. The dynamic path never enters the case-folded catalog
exclusion map.
The walker charges the skipped main against its bounds and never skips by
physical identity, prefix, hidden-name policy, directory, or sidecar name.
Status operations verify archive and parent, lock/read/reread the incumbent
through one handle, create or exchange the next canonical revision, sync,
re-read exact identity/bytes, and verify the archive again.

## Cross-Feature Behavior

D4a owns filesystem safety. D4c consumes only verified archive bytes. D5 must
wrap archive/status operations in its refreshing guard, use canonical detached
specs, reconcile before release, and own quiescence/flush. The offline SQLite
provider alone mints the ephemeral final-stage scope after pinning; callers
cannot supply a pathname exclusion, and no runtime/CLI wiring is activated.

## Failure And Edge Cases

Post-publication parent-sync failure leaves deterministic marker-complete crash
evidence but reports failure.
Post-status-exchange proof failure reports an uncertain committed outcome and
never silently restores arbitrary bytes. Subprocess tests prove exactly one
participating concurrent status writer wins.
An exact excluded stage main is still identity-indexed, so a hardlink under any
other name fails as a physical alias. A stage sidecar or second/near-name stage
is an added legacy member. Scope cancellation, replacement, reuse, or failed
post-pass recheck rejects the cutover even when all recorded legacy bytes match.

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
