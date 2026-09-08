# Database SQLite Control Foundation

## Feature ID

`FR-DATABASE-SQLITE-CONTROL`

## Behavior Summary

PicoClaw defines dormant internal SQLite control and schema-query primitives for
a future single-owner database provider. Given an explicit context and a
caller-owned `database/sql` query or execution boundary, these helpers read or
set the `main` schema version, run bounded integrity diagnostics, and validate
the exact set of manually created unique indexes.

The existing internal SQLite compatibility store now consumes these helpers and
the separately specified provider core. This stage does not consume the
provider catalog, compute readiness, run an offline migration engine, connect
to IPC, or change any application persistence path.

## Reconstruction Notes

- Similarity target: recreate small SQLite-specific control helpers without
  introducing a physical provider or exposing SQLite through an application
  API.
- Core types/functions: `SchemaVersion`, `SetSchemaVersion`,
  `CheckIntegrity`, `CheckIntegrityOnly`, `CheckForeignKeys`, and
  `ValidateUniqueIndexes` over narrow query/execute interfaces.
- Runtime ordering: validate the explicit context and caller boundary, issue
  only the required `main` schema control, consume and close diagnostic rows,
  and return a bounded result without changing caller input.
- Non-obvious constraints: `user_version` is limited to SQLite's non-negative
  signed 32-bit range; a combined integrity check runs physical integrity
  before referential integrity; only unique indexes created explicitly by
  `CREATE INDEX` belong to the exact index set; and these helpers provide no
  transaction, migration-fence, connection, or ownership guarantee.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-CONTROL-001` | MUST | Trusted internal code supplies an explicit non-nil context and caller-owned query or execution boundary to read or set the `main` schema `user_version`. | `SchemaVersion` returns a value from `0` through `MaxInt32`; `SetSchemaVersion` emits only the decimal representation of a value in that range. | Reading changes nothing. Setting changes only the caller's current SQLite connection or transaction according to SQLite semantics. The helper does not begin, commit, or roll back a transaction. | A nil context, nil boundary, negative value, value above `MaxInt32`, unavailable value, or query/execute failure returns an error. No helper claims that the caller holds a migration fence or transaction. | Schema control must be centralized without letting a primitive imply lifecycle authority it cannot enforce. |
| `FR-DATABASE-SQLITE-CONTROL-002` | MUST | Trusted internal code requests physical integrity, referential integrity, or both through an explicit context and caller-owned query boundary. | Physical integrity accepts only the exact successful result from a bounded one-result `main` integrity check. Referential integrity succeeds only when the `main` foreign-key check yields no row. `CheckIntegrity` runs the physical check first and the referential check only after it succeeds. | Diagnostics do not repair, configure, or mutate the database. Every opened row iterator is closed. | A nil context or boundary, unexpected integrity result, returned foreign-key row, row iteration error, or SQL failure returns an error. Reported corruption and foreign-key violations use generic messages and never include SQLite diagnostic content, table names, row values, or caller data. | Diagnostics must fail closed without turning provider details or stored values into application-visible output. |
| `FR-DATABASE-SQLITE-CONTROL-003` | MUST | Trusted internal code supplies one `main` schema table and an expected list of manually created unique-index names. | The named table must exist exactly once. Validation succeeds only when every expected unique index exists exactly once with SQLite origin `c` and no other origin-`c` unique index exists for that table. Primary-key and automatic indexes, including indexes created for table-level unique constraints, are outside this set. | Validation mutates neither schema nor caller-owned table/index inputs. | A nil context or boundary; empty, padded, invalid-UTF-8, NUL-bearing, or over-1,024-byte table/name; more than 256 expected indexes; a duplicate expected name; a missing table/index; an unexpected manual unique index; or catalog-query failure returns an error. An empty expected list requires zero manual unique indexes. | Domain schema adapters need an exact provider catalog check without confusing SQLite-maintained indexes with explicitly declared schema objects. |
| `FR-DATABASE-SQLITE-CONTROL-004` | MUST | Repository code attempts to consume or extend this foundation. | Exact-file architecture guards permit active provider imports only from `internal/sqlitestore/open.go` and `internal/sqlitestore/schema.go`, while reserving exact future readiness and backup/migration implementation filenames; only the provider core binds or opens the driver. | The allowlist changes no runtime composition; compatibility callers retain their existing direct-store behavior and reserved files are absent. | Any additional provider importer or unreviewed driver/open use fails architecture tests until separately specified. | Compatibility reuse and future dormant slices must not make the provider an application-accessible second owner. |

## Data And State Model

The foundation retains no state. It receives an explicit `context.Context` and
structural query or execution interface backed by a caller-owned
`database/sql` connection, transaction, or pool. Only `SetSchemaVersion` can
mutate SQLite state, and its effect belongs entirely to that supplied boundary.
No handle, path, catalog record, driver error, diagnostic row, or schema value
is cached or projected into the database protocol.

## Surface Ownership

Owns: CODE internal/sqliteprovider/control.go
Owns: CODE internal/sqliteprovider/schema.go
Owns: TEST internal/sqliteprovider/control_test.go *
Owns: TEST internal/sqliteprovider/schema_test.go *
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderProductionImportersAreExplicit

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `SchemaVersion`, `SetSchemaVersion` | Read or set a bounded `main.user_version` through a caller-owned SQL boundary without providing transaction or fence ownership. | `FR-DATABASE-SQLITE-CONTROL-001` |
| Internal Go API | `CheckIntegrity`, `CheckIntegrityOnly`, `CheckForeignKeys` | Run ordered, non-repairing `main` integrity diagnostics and suppress stored/provider diagnostic detail. | `FR-DATABASE-SQLITE-CONTROL-002` |
| Internal Go API | `ValidateUniqueIndexes` | Validate the exact expected origin-`c` unique-index set while excluding SQLite-created primary-key and automatic indexes. | `FR-DATABASE-SQLITE-CONTROL-003` |
| Architecture gate | SQLite-provider import guard | Keep production consumers exact and the driver boundary singular. | `FR-DATABASE-SQLITE-CONTROL-004` |

## Algorithms And Ordering

1. Require an explicit non-nil context and the narrow query or execution
   boundary needed by the requested operation.
2. For schema-version reads, query only `main.user_version` and validate the
   returned non-negative signed 32-bit value. For writes, validate the value
   first and append only its base-10 digits to the fixed control statement.
3. For combined diagnostics, run the bounded `main` physical integrity check
   first. Only its exact success result admits the `main` foreign-key check;
   any returned row represents a violation.
4. For unique-index validation, reject excessive, duplicate, or invalid inputs,
   require the exact `main` table, require each named origin-`c` unique index
   exactly once, then compare the total origin-`c` unique-index count with the
   expected count.
5. Return errors only to the trusted caller. Do not map, cache, transmit, or
   expose them through provider-neutral protocol values in this stage.

## Cross-Feature Behavior

`FR-SQLITE` remains the active subsystem-owned SQLite storage behavior and now
consumes these helpers through `internal/sqlitestore`. The provider catalog
remains separate and dormant. `FR-DATABASE` and IPC stay provider-neutral.
Later readiness, migration, and owner-composition features must explicitly
connect and constrain this foundation before use.

## Failure And Edge Cases

- A helper never substitutes `context.Background` for missing caller context.
- Schema-version range validation occurs before executing a control statement.
- Physical failure prevents the foreign-key query from running.
- Integrity and foreign-key violation messages contain no returned diagnostic
  text or row content.
- Expected index order does not affect the result, and validation never mutates
  the supplied slice.
- An empty manual-index set still rejects an unexpected manually created unique
  index while ignoring SQLite-maintained primary-key and automatic indexes.
- No failure opens a provider, repairs a schema, starts a transaction, or falls
  back to another persistence path.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-CONTROL-001`, `FR-DATABASE-SQLITE-CONTROL-002` | [internal/sqliteprovider/control_test.go](../../internal/sqliteprovider/control_test.go) |
| `FR-DATABASE-SQLITE-CONTROL-003` | [internal/sqliteprovider/schema_test.go](../../internal/sqliteprovider/schema_test.go) |
| `FR-DATABASE-SQLITE-CONTROL-004` | [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/control.go](../../internal/sqliteprovider/control.go)
- [internal/sqliteprovider/schema.go](../../internal/sqliteprovider/schema.go)
- [database-sqlite-provider.md](database-sqlite-provider.md)
