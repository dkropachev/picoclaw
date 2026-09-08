# Database SQLite Provider Core

## Feature ID

`FR-DATABASE-SQLITE-PROVIDER`

## Behavior Summary

PicoClaw defines a dormant internal SQLite provider core for the future
single-owner database broker. It alone binds the shipped driver to
`database/sql`, constructs private file DSNs, securely creates and validates a
complete SQLite generation, and configures verified live WAL or offline
rollback-journal operation.

The core itself owns no application route. Exact compatibility, readiness, and
backup/migration implementation filenames are reviewed by architecture guard;
their separate feature contracts determine when they exist and what they may
do. No database CLI, supervisor wiring, configuration or HTTP surface, or
persistence cutover is introduced here. Existing subsystem-local stores remain
the active authority.

## Reconstruction Notes

- Similarity target: centralize physical SQLite driver, path, security, and
  durability behavior without exposing it through an application API.
- Core functions: `OpenStore`, `Configure`, `ConfigureOffline`, `PrepareStore`,
  `EnsurePrivateDirectory`, `SecureGeneration`, `DSN`, and `IsBusyOrLocked`.
- Runtime ordering: validate input, traverse real directory components through
  retained no-follow platform handles, create private members, eagerly open the
  first connection, harden the complete generation, and recheck its identity.
- Non-obvious constraints: memory DSNs are unique; file DSNs cannot recreate a
  vanished main; WAL/SHM identities are transient under cooperating SQLite
  processes; rollback-journal replacement is not an allowed open transition.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-PROVIDER-001` | MUST | Trusted infrastructure supplies a memory name or validated filesystem path and bounded busy timeout. | The provider constructs the only shipped-driver DSN, forces one connection open, and returns a pool only after the main generation remains the same exact file. Memory uses a unique shared cache; file DSNs use existing-file mode. | A missing file-backed endpoint may be created durably and privately. | Blank, padded, invalid-UTF-8, NUL-bearing, overlong, URI-shaped, ambiguous Windows device, symlink/reparse, irregular, hardlinked, foreign-owned, or replaced inputs fail without fallback. | Physical addressing and driver binding must have one internal owner. |
| `FR-DATABASE-SQLITE-PROVIDER-002` | MUST | The provider prepares a directory, main file, or complete generation. | Unix traverses components with descriptor-relative no-follow operations and accepts only protected creation ancestry; Windows rejects reparses, retains the parent handle, and validates a current-user protected DACL. Main and sidecars are single-link owner-private regular files. | Missing components and main files are private at creation, synced, and revalidated; existing owned members may be hardened. | Unsafe ancestry, type, owner, link count, mode/DACL, identity drift, orphan SHM, or mixed WAL and rollback journal fails closed. | SQLite must never follow an attacker-controlled generation member. |
| `FR-DATABASE-SQLITE-PROVIDER-003` | MUST | Trusted code configures a live, memory, or caller-isolated offline pool. | Live file stores select WAL; offline pools select exclusive locking and DELETE journal; all modes verify foreign keys, bounded busy timeout, and `synchronous=FULL`. Busy/locked classification preserves only SQLite primary codes 5 and 6. | Configuration changes only provider connection state and SQLite journal metadata. | Nil/canceled context, nil pool, invalid timeout, unexpected selected mode, or provider failure returns an error. | Durability and concurrency settings must be explicit and verified. |
| `FR-DATABASE-SQLITE-PROVIDER-004` | MUST | Repository code adds a driver open, direct provider import, or platform implementation. | The exact-file guard permits compatibility open/schema, reserves future readiness and backup/migration implementation filenames, and reserves shipped-driver use by future maintenance/staged-copy files; only `provider.go` calls `database/sql.Open`. Unsupported secure platforms fail closed. | Reserved absent files grant no runtime capability. | Any unreviewed importer, driver binding, or open call fails tests. | Provider evolution must remain auditable without activating a second application owner. |

## Data And State Model

The provider retains only a short process-local same-path open mutex. Returned
pools belong to callers. Physical paths, DSNs, driver errors, and generation
identities are never projected into the database protocol or configuration.

## Surface Ownership

Owns: CODE internal/sqliteprovider/provider*.go
Owns: CODE internal/sqliteprovider/security_*.go
Owns: CODE internal/sqliteprovider/linkcount_*.go
Owns: TEST internal/sqliteprovider/provider_core_test.go *
Owns: TEST internal/sqliteprovider/provider_dirs_unix_test.go *
Owns: TEST internal/sqliteprovider/provider_syscall_fault_linux_amd64_test.go *
Owns: TEST internal/sqliteprovider/coverage_helpers_test.go *
Owns: TEST internal/sqliteprovider/coverage_open_security_test.go *
Owns: TEST internal/sqliteprovider/coverage_provider_additional_test.go *
Owns: TEST internal/sqliteprovider/coverage_provider_additional_unix_test.go *
Owns: TEST internal/sqliteprovider/hot_journal_test.go *
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderBoundaryVersionIsStable
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderOwnsDriverOpen
Owns: TEST internal/sqliteprovider/linkcount_*_test.go *
Owns: TEST internal/sqliteprovider/security_*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `OpenStore`, `DSN`, `Configure`, `ConfigureOffline` | Bind and eagerly validate one provider pool with verified live or offline durability. | `FR-DATABASE-SQLITE-PROVIDER-001`, `FR-DATABASE-SQLITE-PROVIDER-003` |
| Internal Go API | `PrepareStore`, `EnsurePrivateDirectory`, `SecureGeneration` | Create or harden an exact private no-follow SQLite generation. | `FR-DATABASE-SQLITE-PROVIDER-002` |
| Architecture gate | Provider import/driver guard | Keep the dormant driver boundary unconsumed and singular. | `FR-DATABASE-SQLITE-PROVIDER-004` |

## Algorithms And Ordering

1. Validate path syntax and timeout before filesystem or provider access.
2. Traverse to the creation boundary with platform handles that reject links
   and reparses; privately create and sync missing directories and the main.
3. Capture main and complete-generation identities, build an existing-file DSN,
   eagerly ping, harden all present members, and recheck the main.
4. Accept only coherent SQLite-managed WAL/SHM transitions around a stable main;
   reject a new or replaced rollback journal.
5. Configure and query back the requested journal, locking, foreign-key,
   timeout, and synchronous settings.

## Cross-Feature Behavior

`FR-DATABASE-SQLITE-CONTROL` supplies provider-neutral schema and integrity
queries used by this core. `FR-SQLITE` remains the active compatibility store
and does not consume the new provider yet. Later reviewed changes add exact
internal consumers, readiness, and migration.

## Failure And Edge Cases

- Opening never silently recreates a deleted file-backed main.
- A hot rollback journal may be recovered by SQLite only when its identity was
  validated before open and it disappears rather than being replaced.
- Cooperating cross-process WAL checkpoints may replace WAL/SHM while the main
  identity remains stable and the final generation is coherent and private.
- Unsupported secure filesystem primitives return `Unsupported` rather than
  falling back to ordinary pathname creation.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-PROVIDER-001`, `FR-DATABASE-SQLITE-PROVIDER-003` | [internal/sqliteprovider/provider_core_test.go](../../internal/sqliteprovider/provider_core_test.go), [internal/sqliteprovider/coverage_open_security_test.go](../../internal/sqliteprovider/coverage_open_security_test.go) |
| `FR-DATABASE-SQLITE-PROVIDER-002` | [internal/sqliteprovider/provider_dirs_unix_test.go](../../internal/sqliteprovider/provider_dirs_unix_test.go), [internal/sqliteprovider/linkcount_unix_test.go](../../internal/sqliteprovider/linkcount_unix_test.go), [internal/sqliteprovider/security_windows_test.go](../../internal/sqliteprovider/security_windows_test.go) |
| `FR-DATABASE-SQLITE-PROVIDER-004` | [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/provider.go](../../internal/sqliteprovider/provider.go)
- [internal/sqliteprovider/provider_filesystem.go](../../internal/sqliteprovider/provider_filesystem.go)
- [internal/sqliteprovider/provider_identity.go](../../internal/sqliteprovider/provider_identity.go)
- [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go)
