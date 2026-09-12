# Database Migration Stage Verification

## Feature ID

`FR-DATABASE-STAGE-VERIFICATION`

## Behavior Summary

PicoClaw provides a dormant provider-minted, callback-bounded proof that lets
offline orchestration validate one exact claims-pinned replacement stage during
the final live-source scan without accepting a caller-selected exclusion.

## Reconstruction Notes

- `ValidatedReplacement` has no public constructor and is usable exactly once
  inside one `StagedLiveVerification` callback.
- The provider binds StoreID, canonical target, target-derived random stage
  name, retained main handle, validated byte digest, callback scope, and exact
  active claims pin before exposing the proof.
- `ValidatedReplacementCheck` accepts the walker's actual `Lstat` observation,
  compares it with the retained handle, and returns only that handle-derived
  physical identity for alias accounting.
- Exact checks are sequential, callback-bounded, cancellation-bound, and
  drained. Stage main bytes, metadata, identity, and all sidecar names are
  checked around hashing, callback use, and cutover.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-STAGE-VERIFICATION-001` | MUST | A provider-validated final stage has a live claims replacement pin and final live-source verification is required. | `MigrateStagedOfflineFromWithLiveVerification` mints one opaque scope bound to the exact StoreID, canonical target, target-derived stage name, retained identity, validated digest, invocation, and active pin. | Retains one bounded read handle and in-memory digest/scope state only. | Nil callback, wrong ID/target/path form, missing/stale/replaced stage, sidecar, hash drift, lost pin/authority, or invalid scope fails before cutover. | A raw pathname must never become authority to relax live-source verification. |
| `FR-DATABASE-STAGE-VERIFICATION-002` | MUST | The migration engine consumes the proof during its final live-source scan. | Use is exactly once and synchronous. Every exact-entry check validates the walker's observation against the retained handle and returns the retained physical identity. Sequential checks support overlapping roots; concurrent, escaped, retained, omitted, repeated, or post-callback use fails and drains before invalidation. | Creates a child cancellation scope and bounded counters; performs read-only stage hashing/identity checks. | Cancellation, panic, callback return during a check, mismatched observation, same-metadata byte mutation, hardlink/link drift, sidecar appearance, or handle/path transition rejects replacement. | The exact skipped directory entry and the later installed bytes must remain the provider-validated object. |
| `FR-DATABASE-STAGE-VERIFICATION-003` | MUST | Repository code imports or references the proof API. | Only the offline migration bridge may invoke the provider entry point; only the backup archive bridge may consume `Use`/`ValidatedReplacementCheck`; dot imports and indirect `.Use` receivers are rejected by architecture tests. | None. | Any additional production reference or consumer fails repository tests. | The narrow proof must not become a general filesystem exclusion mechanism. |

## Data And State Model

The proof state is process-local and monotonic: active → one use with sequential
checks → inactive/drained. It contains no serializable path token and is never
written into the backup manifest, configuration, runtime state, or CLI output.

## Surface Ownership

Owns: CODE internal/sqliteprovider/staged_live_verification.go
Owns: TEST internal/sqliteprovider/staged_live_verification_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `StagedLiveVerification`, `ValidatedReplacement` | Carry one exact provider-selected, callback-bounded replacement proof. | `FR-DATABASE-STAGE-VERIFICATION-001`, `FR-DATABASE-STAGE-VERIFICATION-002` |
| Internal Go API | `ValidatedReplacement.Use`, `ValidatedReplacementCheck` | Admit one synchronous verifier and validate each actual exact-path observation against the retained identity. | `FR-DATABASE-STAGE-VERIFICATION-002`, `FR-DATABASE-STAGE-VERIFICATION-003` |
| Internal Go API | `MigrateStagedOfflineFromWithLiveVerification` | Require and invoke final live-source verification between exact pinning and cutover. | `FR-DATABASE-STAGE-VERIFICATION-001`, `FR-DATABASE-STAGE-VERIFICATION-003` |

## Algorithms And Ordering

After provider/domain validation, open and hash the exact retained stage. Retire
the outer working source, capture the untouched target, pin and recheck the
replacement, and create one child scope. Admit one `Use`; before, during each
walker check, and after it, compare pathname metadata with the retained handle,
check absent sidecars, and hash the retained bytes. Invalidate and drain the
scope, then recheck claims pin, stage, target, and sidecars immediately before
the atomic replacement.

## Cross-Feature Behavior

Stage retirement owns continuous creation/cleanup identity. Physical claims own
the replacement pin. Backup archive owns the exact-entry walker and alias ledger.
Offline migration composes them without exporting physical paths to runtime or
domain adapters.

## Failure And Edge Cases

The exact stage path never enters case-folded catalog exclusions. A Darwin
case-distinct name, near/second stage, or WAL/SHM/journal remains an ordinary
legacy input. The caller-held fence, claims, private directory, and quiescence
protocol exclude uncoordinated same-UID writers; finite checks do not claim an
OS lock against an omnipotent process that restores state between every check.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-STAGE-VERIFICATION-001`, `FR-DATABASE-STAGE-VERIFICATION-002` | [staged_live_verification_test.go](../../internal/sqliteprovider/staged_live_verification_test.go), [provider_offline_test.go](../../internal/sqliteprovider/provider_offline_test.go) |
| `FR-DATABASE-STAGE-VERIFICATION-002` | [live_stage_exclusion_test.go](../../internal/databasemigration/live_stage_exclusion_test.go), [migration_stage_exclusion_integration_test.go](../../internal/databasemigration/migration_stage_exclusion_integration_test.go) |
| `FR-DATABASE-STAGE-VERIFICATION-003` | [import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |

## Implementation Anchors

[staged_live_verification.go](../../internal/sqliteprovider/staged_live_verification.go),
[backup_archive.go](../../internal/databasemigration/backup_archive.go), and
[staged_migration.go](../../internal/sqliteprovider/staged_migration.go).
