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
  retained no-follow platform handles, create and secure the main before SQLite
  opens it, eagerly open the first connection, then validate the complete
  generation without disturbing live SQLite locks and recheck its identity.
- Non-obvious constraints: memory DSNs are unique; file DSNs cannot recreate a
  vanished main; WAL/SHM identities are transient under cooperating SQLite
  processes; rollback-journal replacement is not an allowed open transition;
  absolute process-local path keys conservatively fold case on Windows and
  Darwin, matching catalog reservation keys.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-PROVIDER-001` | MUST | Trusted infrastructure supplies a memory name or validated filesystem path and bounded busy timeout. | The provider constructs the only shipped-driver DSN, forces one connection open, and returns a pool only after the main generation remains the same exact file. Memory uses a unique shared cache; file DSNs use existing-file mode. Process-local keys capture an absolute clean path once and conservatively fold case on Windows and Darwin. | A missing file-backed endpoint may be created durably and privately. | Blank, padded, invalid-UTF-8, NUL-bearing, overlong, URI-shaped, ambiguous Windows device, symlink/reparse, irregular, hardlinked, foreign-owned, or replaced inputs fail without fallback. Distinct case-only files on a case-sensitive Darwin volume may contend on one key and fail by availability. | Physical addressing and driver binding must not split a commonly case-insensitive namespace into two process-local authorities; a conservative collision is safer than dual ownership. |
| `FR-DATABASE-SQLITE-PROVIDER-002` | MUST | The provider prepares a directory, main file, or complete generation. | Unix traverses components with descriptor-relative no-follow operations and accepts only protected creation ancestry; Windows rejects reparses, retains the parent handle, and validates a current-user protected DACL. Main and sidecars are single-link owner-private regular files. After SQLite opens a generation, Unix hardening never opens or closes a descriptor for the main, WAL, SHM, or journal, preserving SQLite's process-scoped POSIX locks. It narrows legacy SQLite-compatible modes through a retained current-user `0700` parent descriptor, then requires the same identity and exact `0600`. Link count `1` is valid, `>1` is a proven hardlink integrity violation, and a transient optional-member count of `0` is a fail-closed generation transition rather than a hardlink classification. | Missing components and main files are private at creation, synced, and revalidated. Existing Unix main and live sidecar modes derived from SQLite creation are narrowed to `0600` without opening the member; Windows retains handle-scoped DACL hardening. | Unsafe ancestry, type, owner, non-tightenable mode/DACL, reparse, identity drift, stable or main link-count zero, hardlink count above one, orphan SHM, or mixed WAL and rollback journal fails closed; callers can distinguish proven integrity violations from generation churn and transient I/O without parsing text. | SQLite must never follow an attacker-controlled generation member, and provider hardening must not release SQLite's own lock authority or mislabel a disappearing sidecar as a hardlink. |
| `FR-DATABASE-SQLITE-PROVIDER-003` | MUST | Trusted code configures a live, memory, or caller-isolated offline pool. | Live file stores select WAL; offline pools select exclusive locking and DELETE journal; all modes verify foreign keys, bounded busy timeout, and `synchronous=FULL`. SQLite primary-code classification preserves only 5/6 as busy/locked and 11/26 as proven corruption/not-a-database; other result codes remain operational failures. | Configuration changes only provider connection state and SQLite journal metadata. | Nil/canceled context, nil pool, invalid timeout, unexpected selected mode, or provider failure returns an error. | Durability and concurrency settings must be explicit and verified without conflating transient I/O and corruption. |
| `FR-DATABASE-SQLITE-PROVIDER-004` | MUST | Repository code adds a driver open, direct provider import, or platform implementation. | The exact-file guard permits compatibility open/schema, admits the exact readiness implementation when present, reserves future backup/migration filenames and shipped-driver use by future maintenance/staged-copy files; only `provider.go` calls `database/sql.Open`. Unsupported secure platforms fail closed. | Absent reserved files grant no capability; present importers remain governed by separate feature contracts. | Any unreviewed importer, driver binding, or open call fails tests. | Provider evolution must remain auditable without activating a second application owner. |

## Data And State Model

The provider retains only a short process-local absolute-path open mutex. Its
key is case-folded on Windows and Darwin and otherwise case-preserving; stable
physical identity checks remain authoritative for existing generation members
and aliases. On a case-sensitive Darwin volume a case-only pair may share a
conservative serialization key, reducing availability without granting dual
authority. Returned pools belong to callers. Physical paths, DSNs, driver
errors, and generation identities are never projected into the database
protocol or configuration.

## Surface Ownership

Owns: CODE internal/sqliteprovider/provider*.go
Owns: CODE internal/sqliteprovider/security_*.go
Owns: CODE internal/sqliteprovider/linkcount_*.go
Owns: TEST internal/sqliteprovider/provider_core_test.go *
Owns: TEST internal/sqliteprovider/provider_identity_test.go *
Owns: TEST internal/sqliteprovider/provider_dirs_unix_test.go *
Owns: TEST internal/sqliteprovider/provider_syscall_fault_linux_amd64_test.go *
Owns: TEST internal/sqliteprovider/coverage_helpers_test.go *
Owns: TEST internal/sqliteprovider/coverage_open_security_test.go *
Owns: TEST internal/sqliteprovider/coverage_provider_additional_test.go *
Owns: TEST internal/sqliteprovider/coverage_provider_additional_unix_test.go *
Owns: TEST internal/sqliteprovider/provider_generation_coverage_test.go *
Owns: TEST internal/sqliteprovider/hot_journal_test.go *
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderBoundaryVersionIsStable
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderOwnsDriverOpen
Owns: TEST internal/sqliteprovider/linkcount_*_test.go *
Owns: TEST internal/sqliteprovider/security_*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `OpenStore`, `DSN`, `Configure`, `ConfigureOffline` | Bind and eagerly validate one provider pool with verified live or offline durability. | `FR-DATABASE-SQLITE-PROVIDER-001`, `FR-DATABASE-SQLITE-PROVIDER-003` |
| Internal Go API | `PrepareStore`, `EnsurePrivateDirectory`, `SecureGeneration` | Create or narrow an exact private no-follow SQLite generation without disturbing live Unix locks; Windows retains handle-scoped DACL hardening. | `FR-DATABASE-SQLITE-PROVIDER-002` |
| Architecture gate | Provider import/driver guard | Keep the dormant driver boundary unconsumed and singular. | `FR-DATABASE-SQLITE-PROVIDER-004` |

## Algorithms And Ordering

1. Validate path syntax and timeout before filesystem or provider access.
2. Traverse to the creation boundary with platform handles that reject links
   and reparses; privately create and sync missing directories and the main.
   Tag proven unsafe semantic outcomes without tagging raw filesystem/API I/O.
3. Capture main and complete-generation identities, build an existing-file DSN,
   eagerly ping, and secure all live members without opening another Unix file
   descriptor. Unix narrows compatible legacy modes relative to a retained
   owner-private parent descriptor and rechecks every identity; Windows retains
   handle-scoped DACL hardening.
4. Accept only coherent SQLite-managed WAL/SHM transitions around a stable main;
   reject a new or replaced rollback journal.
5. Configure and query back the requested journal, locking, foreign-key,
   timeout, and synchronous settings.

## Cross-Feature Behavior

`FR-DATABASE-SQLITE-CONTROL` supplies provider-neutral schema and integrity
queries used by this core. Inspection extends this provider package without
activating an application route. Readiness and migration may consume it only
through exact separately specified files.

## Failure And Edge Cases

- Opening never silently recreates a deleted file-backed main.
- A hot rollback journal may be recovered by SQLite only when its identity was
  validated before open and it disappears rather than being replaced.
- Cooperating cross-process WAL checkpoints may replace WAL/SHM while the main
  identity remains stable and the final generation is coherent and private.
- A disappearing optional sidecar is a fail-closed generation transition, not
  proof of a hardlink. A stable link count above one remains an integrity error.
- Unix live hardening must not open and close generation files because POSIX
  record locks are process-scoped and such a close can release SQLite's locks;
  parent-relative `chmod` is allowed only after no-follow identity, owner, link,
  and tightenable-mode checks and requires the same private file afterward.
- Unsupported secure filesystem primitives return `Unsupported` rather than
  falling back to ordinary pathname creation.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-PROVIDER-001`, `FR-DATABASE-SQLITE-PROVIDER-003` | [internal/sqliteprovider/provider_core_test.go](../../internal/sqliteprovider/provider_core_test.go), [internal/sqliteprovider/provider_identity_test.go](../../internal/sqliteprovider/provider_identity_test.go), [internal/sqliteprovider/coverage_open_security_test.go](../../internal/sqliteprovider/coverage_open_security_test.go) |
| `FR-DATABASE-SQLITE-PROVIDER-002` | [internal/sqliteprovider/provider_dirs_unix_test.go](../../internal/sqliteprovider/provider_dirs_unix_test.go), [internal/sqliteprovider/linkcount_unix_test.go](../../internal/sqliteprovider/linkcount_unix_test.go), [internal/sqliteprovider/provider_generation_coverage_test.go](../../internal/sqliteprovider/provider_generation_coverage_test.go), [internal/sqliteprovider/security_live_unix_test.go](../../internal/sqliteprovider/security_live_unix_test.go), [internal/sqliteprovider/security_unix_live_coverage_test.go](../../internal/sqliteprovider/security_unix_live_coverage_test.go), [internal/sqliteprovider/security_windows_test.go](../../internal/sqliteprovider/security_windows_test.go) |
| `FR-DATABASE-SQLITE-PROVIDER-004` | [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/provider.go](../../internal/sqliteprovider/provider.go)
- [internal/sqliteprovider/provider_filesystem.go](../../internal/sqliteprovider/provider_filesystem.go)
- [internal/sqliteprovider/provider_identity.go](../../internal/sqliteprovider/provider_identity.go)
- [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go)
