# Database SQLite Provider Boundary

## Feature ID

`FR-DATABASE-SQLITE-PROVIDER`

## Behavior Summary

PicoClaw reserves an internal architecture boundary for the future SQLite
provider. The boundary names the only production files that may bind the
shipped driver and requires every production importer to be reviewed through
an exact-file allowlist.

This stage adds no provider implementation or importer. It opens no database,
constructs no DSN, and changes no persistence behavior.

## Reconstruction Notes

- Similarity target: establish the provider ownership and import rules before
  adding physical database behavior.
- Core test: `TestSQLiteProviderProductionImportersAreExplicit`.
- Runtime ordering: none; this is a repository architecture gate.
- Non-obvious constraint: adding an allowed provider file does not authorize a
  consumer; importer and driver-open checks remain independent.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-PROVIDER-001` | MUST | Repository code adds a provider importer, shipped-driver import, or `database/sql.Open` call under `internal/sqliteprovider`. | The guard accepts only exact reviewed importer files, permits the shipped driver only in declared provider implementation files, and permits `database/sql.Open` only in `provider.go`. | The check changes no runtime or durable state. | Any unreviewed importer, driver binding, or open call fails tests; absent future files do not create an expected-import failure. | Provider implementation must land behind a singular auditable boundary without activating a second owner. |

## Data And State Model

The boundary retains no runtime state. Its allowlists are test-only repository
policy and grant no filesystem, database, catalog, transport, or application
authority.

## Surface Ownership

Owns: CODE internal/sqliteprovider/provider_boundary.go
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderBoundaryVersionIsStable
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderOwnsDriverOpen

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Architecture gate | SQLite provider importer/driver guard | Keep future production consumers exact and driver open singular. | `FR-DATABASE-SQLITE-PROVIDER-001` |

## Algorithms And Ordering

1. Walk repository production Go files while excluding generated, vendored,
   cached, and test-only trees.
2. Reject every provider import not present in the exact-file allowlist.
3. Within the provider package, reject the shipped driver outside reviewed
   implementation files and reject `database/sql.Open` outside `provider.go`.

## Cross-Feature Behavior

`FR-DATABASE-SQLITE-CONTROL` still owns the dormant control helpers and its
existing no-production-consumer contract remains true. Later provider and
compatibility PRs update these boundaries when their implementations land.

## Failure And Edge Cases

- A same-named import alias cannot evade the AST-based open-call check.
- Test files never count as production provider consumers.
- A newly created production file is reviewed even when it has no provider
  import, because driver binding is scanned independently.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-PROVIDER-001` | [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go)
- [internal/sqliteprovider/provider_boundary.go](../../internal/sqliteprovider/provider_boundary.go)
