# Database SQLite Inspection

## Feature ID

`FR-DATABASE-SQLITE-INSPECTION`

## Behavior Summary

PicoClaw provides a dormant SQLite inspection and pool-handoff layer. It
classifies an existing generation without creating a missing store, runs
bounded integrity and typed schema checks, and retains one exact-generation
pool reference for explicit adoption by a later sole owner.

Inspection itself publishes no application route. The exact provider import
guard admits the separately specified readiness implementation as its first
lifecycle consumer; nothing is published through IPC and no application
persistence path changes.

## Reconstruction Notes

- Similarity target: share one validated connection lifecycle between future
  readiness and ownership without path-global implicit adoption.
- Core APIs: `Inspect`, `InspectClaimed`, `Inspection`,
  `Inspection.Revalidate`, `HasSchemaObjects`, `HasTableColumns`,
  `HasImportHorizon`, `Inspection.ValidateDomain`, `Inspection.Adopt`, and
  `Inspection.Release`.
- Runtime ordering: validate a sidecar-free missing state or secure an existing
  generation, open one connection, check integrity/version/schema, retain an
  owner token, then explicitly adopt or release it.
- Non-obvious constraint: the timeout is immutable after inspection. Adoption
  secures the generation, captures every member twice, and requires the
  retained entry and both captures to match before transferring a sole owner.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-INSPECTION-001` | MUST | Trusted lifecycle infrastructure inspects one provider path with a bounded timeout. A claimed inspection is given the exact reconcile callback from a currently held exclusive `Lease.GuardStoresRefreshing` scope. | Two complete passes prove a missing generation remains absent. An existing generation is validated, eagerly opened with one connection, stable full physical identities are captured before and after integrity/version/schema checks, and it is empty only when schema count and version are zero. Unix live-file hardening narrows SQLite-compatible legacy modes without opening generation-member descriptors, so it cannot release SQLite's process-scoped locks; Windows retains handle-scoped DACL hardening. `InspectClaimed` invokes the supplied reconciliation after provider work and before any failed opened-pool cleanup; the caller retains the same guard through its own post-operation reconciliation and `Inspection.Revalidate`. Proven corruption is limited to explicit failed integrity/foreign-key/version results and SQLite primary `CORRUPT`/`NOTADB`; other SQL/provider failures use a redacted unavailable classification. | Existing owner-controlled directory permissions, compatible Unix generation-file modes, and Windows member DACLs may be hardened; reconciliation may add claims for newly materialized SQLite members. Schema and missing paths are unchanged. | A missing guard, substituted callback, early guard release, failed reconciliation, orphan/incoherent sidecar, unsafe permission, changed generation, contention, corruption, cancellation, provider failure, or cleanup failure fails the surrounding readiness operation without exposing driver diagnostics. | A readiness consumer must observe rather than initialize schema or content, while every transient provider member remains inside one uninterrupted claim authority and operational I/O is not mislabeled as corruption. |
| `FR-DATABASE-SQLITE-INSPECTION-002` | MUST | A caller queries typed readiness facts on a live inspection. | Object checks require exact `(table|index|trigger|view, name)` identity; column checks require a real table and every named column; an import horizon uses `pragma_table_xinfo` and requires exactly two non-hidden columns with the canonical name/type/nullability/key properties plus one exact component row with an integer completion value. | Queries change no schema or retained input. | Nil/released inspection, nil context, empty/duplicate/invalid/excessive identifier, hidden/generated or otherwise malformed marker schema/row, or query failure fails closed. | Domain contracts must not be satisfied by type confusion, hidden schema additions, or unbounded SQL. |
| `FR-DATABASE-SQLITE-INSPECTION-003` | MUST | An inspection owner revalidates inside its uninterrupted exclusive refreshing guard, or later adopts/releases while holding a live guard on the originating claim lease and fence. | `Revalidate` repeats ancestry/security validation and requires two complete identity captures to equal the retained generation. Adoption succeeds only for its sole live token and immutable inspected timeout, then secures the generation and requires the retained entry plus two new complete captures to agree. Copied `Inspection` values share one token; concurrent or repeated transitions can produce at most one successful adoption or final release. | Successful adoption atomically invalidates every copy, removes the handoff entry, and transfers the pool for the caller to close. Absent a concurrent winning release, failed adoption transfers nothing and leaves the live reference releasable; final unadopted release closes the pool exactly once. | Wrong token/generation, drift during either final capture, multiple owners, repeated adoption, candidate-close failure, or an adoption/release race cannot transfer a stale pool or invalidate another owner. | Pool handoff must preserve one checked lifecycle without a second-open or copied-token race. |
| `FR-DATABASE-SQLITE-INSPECTION-004` | MUST | Trusted migration or readiness infrastructure invokes an adapter's optional exact validator for one existing, nonempty, provider-neutral contract-ready inspection. | The provider physically revalidates the retained generation, verifies connection-local `query_only` is initially off, enables and verifies it, installs an always-rollback transaction boundary, and supplies the exact StoreID/domain through a 30-second callback scope. Serialized `ReadScalar` calls accept one UTF-8/NUL-free SELECT up to 64 KiB and at most 1,024 string arguments totaling 64 KiB; each returns exactly zero or one row and one column containing only copied NULL, `int64`, or UTF-8 text up to 1 MiB. After callback revocation, the provider cancels and synchronously joins every admitted read under the original claim, verifies `query_only` stayed on and the transaction stayed active, rolls back, restores and verifies off, closes the connection, and physically revalidates again. | Validation may create only connection-local query-only/transaction state, which is always rolled back. StoreID, domain, scope, connection, and reader state are scrubbed before return. | Mutation or multi-statement SQL, WITH entry, path/module pragma, file/extension function, invalid argument/result shape, ignored read error, concurrent/late read, callback escape, panic, timeout, query-only or transaction drift, cleanup ambiguity, or retained capability use fails closed. Semantic mismatch is integrity; busy/query I/O is unavailable; callback/lifecycle misuse or ambiguous cleanup is infrastructure and revokes the inspection owner. Validation, adoption, and release share one atomic lifecycle, so no copy can transfer or close the pool during validation. | Domain exactness must be proved against the same claimed generation without exposing a path or generic SQL capability and without letting a validator mutate, escape, or poison a pooled connection. |

## Data And State Model

The process-local handoff map retains only an immutable absolute path key,
timeout, main/WAL/SHM/journal stable identities and presence bits, pool, and
opaque owner-token set. Windows and Darwin keys conservatively fold case;
therefore distinct case-only files on a case-sensitive Darwin volume may
serialize or collide by availability, but can never acquire two pool owners.
Every real inspection also shares one atomic
idle/validating/invalid/adopting/terminal lifecycle across its copies. A failed
exact validation becomes non-adoptable while remaining revalidatable and
releasable; infrastructure cleanup may move directly to terminal. No path,
handle, SQL, or provider error enters protocol or configuration data.

## Surface Ownership

Owns: CODE internal/sqliteprovider/inspect.go
Owns: CODE internal/sqliteprovider/inspection_pool.go
Owns: CODE internal/sqliteprovider/inspection_validation.go
Owns: TEST internal/sqliteprovider/coverage_inspection_pool_test.go *
Owns: TEST internal/sqliteprovider/inspection_lifecycle_test.go *
Owns: TEST internal/sqliteprovider/inspection_boundaries_unix_test.go *
Owns: TEST internal/sqliteprovider/inspection_review_test.go *
Owns: TEST internal/sqliteprovider/inspection_delta_coverage_test.go *
Owns: TEST internal/sqliteprovider/inspection_validation_test.go *
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderInspectionDomainValidationConsumersAreNarrow
Owns: TEST internal/sqliteprovider/import_guard_test.go TestSQLiteProviderInspectionDomainValidationGuardRejectsEscapeForms

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `Inspect`, `Inspection` query methods | Classify and query one exact existing generation without initialization. | `FR-DATABASE-SQLITE-INSPECTION-001`, `FR-DATABASE-SQLITE-INSPECTION-002` |
| Internal Go API | `InspectClaimed`, `Inspection.Revalidate` | Inspect and revalidate while the caller continuously holds the exact `Lease.GuardStoresRefreshing` scope and uses its reconcile callback. | `FR-DATABASE-SQLITE-INSPECTION-001`, `FR-DATABASE-SQLITE-INSPECTION-003` |
| Internal Go API | `Inspection.Adopt`, `Inspection.Release` | Explicitly transfer or release one exact reference. | `FR-DATABASE-SQLITE-INSPECTION-003` |
| Internal Go API | `Inspection.ValidateDomain` | Invoke a deadline-signaled, fail-stop StoreID/domain-bound exact validator through the provider-owned typed scalar facade and always-rollback transaction. | `FR-DATABASE-SQLITE-INSPECTION-004` |

## Algorithms And Ordering

1. Reject invalid context/path/timeout and incoherent missing-sidecar state.
2. Secure and identity-capture an existing complete generation, open one
   connection, check integrity and schema facts under the caller/deadline
   context, then capture every generation identity twice.
3. For claimed inspection, keep the exact exclusive refreshing guard live,
   reconcile materialized members before failure cleanup, and revalidate the
   inspected generation before publishing its observation.
4. Retain an owner token in the normalized-path pool entry.
5. Query only validated quoted identifiers and typed `main.sqlite_schema` rows.
6. Revalidate security and two stable identity captures; adopt only a sole
   matching token/generation using the retained timeout, or close on final
   unadopted release. Cleanup errors are typed as infrastructure failures.
7. For exact validation, revalidate, enable verified query-only state, install
   and begin the provider transaction, run the deadline-signaled typed-scalar callback,
   revoke and scrub it, verify state, roll back, restore query-only off, close,
   and revalidate. Cancel and synchronously join every admitted read before
   releasing the original claim; a driver that violates cancellation makes the
   operation fail-stop under that authority instead of escaping into background
   cleanup. The shared lifecycle rejects concurrent validation, adoption, and
   release.

## Cross-Feature Behavior

The SQLite provider core supplies physical open/security and the control
foundation supplies integrity/version helpers. The provider import guard admits
only exact compatibility files, the separately specified readiness
implementation, and reserved migration files. Later owner composition may
adopt a ready pool by typed store ID while holding the same catalog lease.

## Failure And Edge Cases

- A missing main with any sidecar is an integrity error.
- A view cannot satisfy a required table-column contract.
- Failed adoption leaves the live inspection reference usable unless a
  concurrent final release won the shared-token transition.
- Copied inspection values share one transition token; a successful adoption
  makes every copy inert and transfers `Close` responsibility to its caller.
- Double release is harmless and cannot close an adopted pool.
- A retained exact-generation value exposes blank binding data and only a fixed
  expired-scope error; it retains no connection, context, or query arguments.
- A callback error cannot hide an earlier scalar-read error. Contract misuse
  poisons the attempt even when callback code ignores the returned error.
- Context-aware cleanup receives a bounded context, but physical boundary,
  connection, and pool close are fail-stop: if a driver close does not return,
  validation retains lifecycle/claim authority instead of reporting completion
  while cleanup is still active.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-INSPECTION-001` | [internal/databaseclaims/refreshing_guard_test.go](../../internal/databaseclaims/refreshing_guard_test.go), [internal/sqliteprovider/inspection_review_test.go](../../internal/sqliteprovider/inspection_review_test.go), [internal/sqliteprovider/inspection_delta_coverage_test.go](../../internal/sqliteprovider/inspection_delta_coverage_test.go) |
| `FR-DATABASE-SQLITE-INSPECTION-002` | [internal/sqliteprovider/inspection_review_test.go](../../internal/sqliteprovider/inspection_review_test.go), [internal/sqliteprovider/inspection_delta_coverage_test.go](../../internal/sqliteprovider/inspection_delta_coverage_test.go) |
| `FR-DATABASE-SQLITE-INSPECTION-003` | [internal/sqliteprovider/coverage_inspection_pool_test.go](../../internal/sqliteprovider/coverage_inspection_pool_test.go), [internal/sqliteprovider/inspection_lifecycle_test.go](../../internal/sqliteprovider/inspection_lifecycle_test.go), [internal/sqliteprovider/inspection_delta_coverage_test.go](../../internal/sqliteprovider/inspection_delta_coverage_test.go) |
| `FR-DATABASE-SQLITE-INSPECTION-004` | [internal/sqliteprovider/inspection_validation_test.go](../../internal/sqliteprovider/inspection_validation_test.go), [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go), [internal/sqliteprovider/transaction_boundary_architecture_test.go](../../internal/sqliteprovider/transaction_boundary_architecture_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/inspect.go](../../internal/sqliteprovider/inspect.go)
- [internal/sqliteprovider/inspection_pool.go](../../internal/sqliteprovider/inspection_pool.go)
- [internal/sqliteprovider/inspection_validation.go](../../internal/sqliteprovider/inspection_validation.go)
