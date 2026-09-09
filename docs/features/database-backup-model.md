# Database Backup Model

## Feature ID

`FR-DATABASE-BACKUP-MODEL`

## Behavior Summary

PicoClaw defines dormant, fail-closed data, path, provenance, ordering, and
resource-limit rules for database backup evidence. This slice performs no
filesystem mutation and is not wired into runtime orchestration.

## Reconstruction Notes

- Manifest version 2 binds stores, canonical generation paths, ordered legacy
  roots/kinds, source identities/modes, archive paths, byte sizes, and SHA-256.
- Validation is canonical, rejects exact collisions across all legacy roots and
  catalog generations, rejects generation-to-generation containment, requires
  a source-path/source-identity bijection, and applies raw-string and encoded
  aggregate budgets before aggregate JSON allocation. Catalog-defined legacy
  directory containment remains valid.
- Windows grammar rejects namespaces, devices, ADS, reserved/trailing
  components, control characters, and short-name aliases.
- Darwin and Windows path identity conservatively folds case so backup
  collision checks match the catalog/provider identity boundaries.
- Prepared legacy wrapper entries and archive path expansion share explicit
  bounded counters.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-BACKUP-MODEL-001` | MUST | Trusted infrastructure supplies a `BackupManifest`. | Only canonical, ordered, collision-free, internally coherent evidence validates. | Read-only. | Invalid version/time/mode/role/layout, duplicate path/identity, sidecar incoherence, or provenance mismatch fails closed. | Invalid recovery evidence must be rejected before use. |
| `FR-DATABASE-BACKUP-MODEL-002` | MUST | A path or aggregate resource request is derived. | Only canonical platform-safe paths and requests within byte/count/depth/encoded-size limits are accepted. | Read-only. | Invalid UTF-8, namespace/device/ADS/short-name syntax, depth, count, or aggregate expansion fails before amplification. | Attacker metadata must not cause unbounded allocation or traversal. |

## Data And State Model

`BackupManifest`, `BackupStoreManifest`, and `BackupFileManifest` are mutable Go
construction carriers. Validation establishes point-in-time validity only;
trusted consumers must retain the value without mutation or bind its validated
serialized bytes into sealed evidence before use. Shared `backupBudget` limits
files, source bytes, tree/archive entries, prepared wrappers, and encoded
metadata. Generation roles have fixed canonical order and SQLite coherence
rules.

## Surface Ownership

Owns: CODE internal/databasemigration/backup_model.go
Owns: CODE internal/databasemigration/backup_destination.go
Owns: CODE internal/databasemigration/backup_path_*.go
Owns: CODE internal/databasemigration/manifest_validation.go
Owns: TEST internal/databasemigration/backup_model_hardening_test.go *
Owns: TEST internal/databasemigration/backup_path_windows_test.go *
Owns: TEST internal/databasemigration/manifest_validation_test.go *
Owns: TEST internal/databasemigration/path_darwin_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `BackupManifest`, `validateBackupManifest` | Define and validate canonical bounded evidence. | `FR-DATABASE-BACKUP-MODEL-001` |
| Internal Go API | Path/budget helpers | Derive safe layouts and reject amplification. | `FR-DATABASE-BACKUP-MODEL-002` |

## Algorithms And Ordering

Validate fixed scalar fields and bounded counts, reserve encoded metadata
incrementally, build store/provenance maps, verify canonical file ordering and
SQLite role coherence, account archive/prepared path expansion, then validate
the exact sorted catalog-generation namespace with typed legacy/generation
collision rules.

## Cross-Feature Behavior

D4a2 builds identity-safe filesystem operations on this model. D4b composes
those operations into archives and monotonic status. D4c consumes verified
archives through sealed capabilities. D5 alone owns claims and quiescence.

## Failure And Edge Cases

The supplied marshal limit is also the pre-allocation metadata limit. Raw string
length is checked before JSON escaping. Unsupported platform path grammar fails
closed. This slice creates no files and opens no databases.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-BACKUP-MODEL-001` | [manifest_validation_test.go](../../internal/databasemigration/manifest_validation_test.go) |
| `FR-DATABASE-BACKUP-MODEL-002` | [backup_model_hardening_test.go](../../internal/databasemigration/backup_model_hardening_test.go) |

## Implementation Anchors

- [backup_model.go](../../internal/databasemigration/backup_model.go)
- [manifest_validation.go](../../internal/databasemigration/manifest_validation.go)
