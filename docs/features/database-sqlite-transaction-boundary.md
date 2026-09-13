# Database SQLite Transaction Boundary

## Feature ID

`FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY`

## Behavior Summary

The internal SQLite provider owns a connection-local commit/rollback boundary
for transactions executed by `sqlitestore.Immediate` and for provider-internal
exact-generation validation. It installs hooks before `BEGIN`, rejects
callback-controlled transaction termination, and either authorizes exactly one
owner commit or proves an always-rollback validation transaction remained live.
Schema, legacy-import, horizon, and domain changes therefore cannot become
durable merely because callback code attempted transaction control.

This is transaction containment inside the existing compatibility store. It
does not register a broker handler, start the supervisor or launcher, install a
client, or change runtime database ownership.

## Reconstruction Notes

- Core API: opaque `sqliteprovider.TransactionBoundary` values created only by
  `NewTransactionBoundary`, then advanced through `BeginAttempted`, `Started`,
  `Check`, `Commit`, and `Close`.
- Driver seam: only the provider sees the concrete modernc connection through
  `sql.Conn.Raw`; it type-asserts the minimal commit-hook, rollback-hook, and
  raw-execution interfaces without returning or retaining the driver value.
- Commit ordering: install hooks, mark begin-attempted, execute
  `BEGIN IMMEDIATE` or exact-target `BEGIN EXCLUSIVE`, mark started, run the
  callback, cross a Raw driver-lock barrier, run any connection-free final
  proof, and cross a second barrier. Then recheck cancellation, arm, and
  execute one raw `COMMIT` under the final Raw lock. Confirm exactly one commit
  hook and no rollback hook, unregister hooks, then release the connection.
- Abort ordering: under one Raw driver lock, unregister both hooks before owner
  rollback. A violated or cleanup-ambiguous physical connection is discarded
  only after hook removal.
- Validation ordering: provider-internal `inspection_validation.go` uses the
  same boundary with `BEGIN`, checks that the transaction remained clean, and
  always closes/rolls back; it never arms `Commit`.
- Trust boundary: production callbacks, including dynamically supplied helper
  functions, are trusted synchronous repository code and must not retain or
  re-export their connection. Static guards reject current direct `Raw`,
  `Close`, assignment, return, channel-transfer, and goroutine escape forms as
  defense in depth. They are not a sound Go capability sandbox: arbitrary
  wrapping, reflection, or a deliberately hostile dynamic callback can evade a
  source scan. A runtime-restricted callback facade is tracked in #436.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-001` | MUST | Trusted storage code installs a boundary on one provider-owned `sql.Conn` before a manual write transaction. | The boundary registers one commit hook and one rollback hook and starts in an unarmed state. `BeginAttempted` is recorded before owner `BEGIN`; `Started` is accepted exactly once after a successful result; `Check` crosses the driver lock and succeeds only for that clean state. | Only connection-local hook registration and bounded in-memory state change. | Nil, unsupported, copied/reused, closed, wrong-order, or concurrent capability use fails closed. A BEGIN that may have succeeded before returning cancellation/error is hook-cleared, raw-rollback attempted, and physically discarded before pool reuse. | Callback code must not inherit transaction-termination authority merely because it receives the query/execute connection, and ambiguous BEGIN results must not strand locks. |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-002` | MUST | A callback or SQLite statement attempts `COMMIT`, `END`, explicit rollback, or automatic rollback before owner authorization. | An unauthorized commit hook returns nonzero, SQLite rolls back, and the rollback hook poisons the boundary. Every rollback event is a violation. A subsequent callback error, ignored SQL error, replacement `BEGIN`, or write cannot make the owner operation succeed. | All changes in the violated transaction and any replacement transaction are rolled back; a poisoned physical connection is discarded after hooks are removed. | `INSERT OR ROLLBACK`, swallowed commit errors, cancellation, callback panic/error, begin failure, cleanup failure, and repeated transaction-control attempts return a stable integrity failure or the original non-boundary cause when the boundary stayed clean. | An error from the outer `COMMIT` is too late if callback state was already durable. |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-003` | MUST | A clean callback completes and the owner commits. | Commit arming, raw driver `COMMIT`, hook-count confirmation, state finalization, and hook removal occur under one `sql.Conn.Raw` driver lock. Exactly one commit hook and zero rollback hooks are required. Cancellation is checked immediately before arming; the short armed commit uses a non-cancelable child context so modernc cannot report cancellation after SQLite made the commit durable. | The one owner transaction commits, then the connection may return to the pool without hooks. | Authorization theft, commit error, unexpected hook count, rollback event, cancellation before arming, concurrent/repeated commit, or uninstall ambiguity fails; ambiguous connections are discarded. Cancellation after arming returns the actual commit result, never a false retryable failure. | No callback goroutine may win a gap between arming and owner commit, no hook may leak into later pooled work, and a durable commit must not be reported as failed. |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-004` | MUST | Repository code consumes or extends this boundary. | Outside the provider, only `internal/sqlitestore/open.go` may construct or consume the opaque boundary. Inside the provider, exactly one `NewTransactionBoundary` call in `inspection_validation.go` may create the always-rollback exact-validation boundary. Only `transaction_boundary.go` may register commit/rollback hooks. Repository scans reject direct callback-accessible `Raw`, `Close`, assignment, return, channel-transfer, goroutine escape, extra same-package construction, and retained/asynchronous exact-validation forms. | Architecture tests only. | Direct aliases, dot imports, inferred locals, method values, additional constructor calls, and hook registration fail the guard. | Hook containment depends on singular provider ownership and explicit audited consumers. |

## Data And State Model

One boundary holds an operation mutex, a hook-state mutex, the owning
`sql.Conn`, a closed state, bounded commit/rollback counters, the first
violation, and an idempotent close result. Hooks only update this memory; they
never call SQLite reentrantly.

The state machine is:

`installed -> begin-attempted -> started -> commit-armed -> commit-observed -> committed -> closed`

Any unauthorized commit, rollback, invalid transition, or ambiguous cleanup
moves the effective outcome to violated/closed. A boundary is not durable state
and never crosses a callback, process, IPC, or database generation boundary.

## Surface Ownership

Owns: CODE internal/sqliteprovider/transaction_boundary.go
Owns: TEST internal/sqliteprovider/transaction_boundary_test.go *
Owns: TEST internal/sqliteprovider/transaction_boundary_coverage_test.go *
Owns: TEST internal/sqliteprovider/transaction_boundary_architecture_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `sqliteprovider.NewTransactionBoundary` | Install provider-exclusive hooks on one caller-owned connection without exposing the raw driver. | `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-001`, `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-004` |
| Opaque capability | `TransactionBoundary.BeginAttempted`, `Started`, `Check`, `Commit`, `Close` | Bind one possibly-started manual transaction, detect callback termination, authorize one atomic raw commit, and remove hooks before rollback/release. | `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-001` through `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-003` |
| Architecture gates | Provider and callback boundary tests | Preserve exclusive hook ownership and reject direct connection escape forms in the reviewed repository; they supplement, rather than replace, the trusted-synchronous callback contract. | `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-004` |

## Algorithms And Ordering

1. Acquire one `sql.Conn` and resolve ordinary immediate versus fenced exclusive
   mode.
2. Enter `sql.Conn.Raw`, verify the shipped driver hook interface, install both
   hooks, and return without retaining the raw driver value.
3. Mark begin-attempted, execute owner `BEGIN`, mark the boundary started only
   after an unambiguous success, and run the callback. An error in the attempt
   discards the hook-cleared physical connection after raw rollback.
4. Always call `Check`, even when the callback returned an error, so rollback or
   denied-commit evidence overrides a raw driver-only result. Exact-generation
   validation also verifies query-only state, calls `Check`, and takes only the
   abort path.
5. On success, run any connection-free final proof and call `Check` again. The
   second barrier proves the owner transaction remained active while that proof
   ran.
6. Enter one Raw critical section, recheck cancellation and clean state, arm,
   execute driver-level `COMMIT`, validate hook counts, mark committed, and
   unregister hooks.
7. On any other exit, enter one Raw critical section, unregister hooks, then
   issue owner rollback. Discard the physical connection after a violation or
   ambiguous rollback; otherwise return it cleanly to the pool.

## Cross-Feature Behavior

`FR-SQLITE` owns `sqlitestore.Immediate` and its domain callbacks. This feature
provides the internal provider capability used by that function and the
provider-internal exact-validation facade. The sealed
absent-root work in #433 will add a final proof between two `Check` calls; #435
must merge first so that proof cannot validate an already-ended transaction.

The provider import guard remains authoritative for concrete SQLite-driver
ownership. #436 tracks the later restricted callback facade and is nonblocking
under the synchronous trusted-code boundary enforced here.

## Failure And Edge Cases

- A denied commit invokes SQLite rollback; the rollback hook reinforces rather
  than clears the violation.
- A clean callback error or panic unregisters hooks before owner rollback and
  preserves the original cause.
- A callback rollback, ignored denied commit, or replacement transaction returns
  an integrity violation and discards that physical connection.
- A commit failure cannot strand hooks: both registrations are cleared before a
  normal reuse or intentional `driver.ErrBadConn` discard.
- BEGIN cancellation cannot leak a possibly active manual transaction into the
  pool. Post-arm cancellation cannot turn a durable COMMIT into a reported
  failure; cancellation is decisive only before authorization.
- Modernc hook registration replaces prior hooks and has no getter. Architecture
  therefore forbids all other production hook registration instead of claiming
  that unknown hooks can be restored.
- Deliberately hostile or dynamically wrapped code with direct `*sql.Conn`
  access is outside this runtime guarantee. Static gates catch direct repository
  escape forms but are not presented as whole-language proof; #436 removes the
  API capability later.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-001`, `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-003` | [internal/sqliteprovider/transaction_boundary_test.go](../../internal/sqliteprovider/transaction_boundary_test.go) |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-002` | [internal/sqliteprovider/transaction_boundary_test.go](../../internal/sqliteprovider/transaction_boundary_test.go), [internal/sqlitestore/transaction_boundary_test.go](../../internal/sqlitestore/transaction_boundary_test.go), [internal/sqlitestore/open_edge_test.go](../../internal/sqlitestore/open_edge_test.go) |
| `FR-DATABASE-SQLITE-TRANSACTION-BOUNDARY-004` | [internal/sqliteprovider/transaction_boundary_architecture_test.go](../../internal/sqliteprovider/transaction_boundary_architecture_test.go), [internal/sqlitestore/transaction_boundary_architecture_test.go](../../internal/sqlitestore/transaction_boundary_architecture_test.go), [internal/sqliteprovider/import_guard_test.go](../../internal/sqliteprovider/import_guard_test.go) |

## Implementation Anchors

- [internal/sqliteprovider/transaction_boundary.go](../../internal/sqliteprovider/transaction_boundary.go)
- [internal/sqlitestore/open.go](../../internal/sqlitestore/open.go)
- [internal/sqliteprovider/transaction_boundary_test.go](../../internal/sqliteprovider/transaction_boundary_test.go)
- [internal/sqlitestore/transaction_boundary_test.go](../../internal/sqlitestore/transaction_boundary_test.go)
