# Database Provider Lease

## Feature ID

`FR-DATABASE-PROVIDER-LEASE`

## Behavior Summary

PicoClaw defines a dormant neutral capability that carries one exact,
deadline-bound store target from the physical-claims layer into the SQLite
provider without introducing a package cycle. One provider callback may
consume it. Revocation immediately closes new admission and cancels admitted
contexts; authority is not released until the callback and every admitted
operation have returned.

This slice opens no database, acquires no claim, and changes no runtime wiring.

## Reconstruction Notes

- A `Lease` and every value copy share one private lifecycle state.
- The claims layer alone constructs/revokes/waits; the provider alone consumes
  and invokes target-bound hooks. An exact repository guard enforces this.
- Construction requires a valid `StoreID`, canonical absolute target, complete
  hook set, and finite future deadline clamped to ten minutes.
- `Access` is synchronous and scope-bound. Retained copies fail after callback
  return, revocation, parent cancellation, or deadline.
- Callback and hook contexts expose the earliest applicable deadline. Explicit
  revocation cancels every admitted context before returning, while operation
  contexts preserve values from their immediate caller.
- Revocation never waits while a claims mutex may be held. `Wait` is separately
  context-bounded for observation, while the consuming provider remains
  fail-stopped until admitted work really drains; a timeout cannot restore
  authority.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-PROVIDER-LEASE-001` | MUST | Claims constructs a child capability with a live bounded context, exact `StoreID`/path, and migration-guard hooks. | A copy-safe opaque lease is bound immutably to that target and expires no later than ten minutes; an already-ended parent reports its exact cancellation cause. | Allocates only in-process lifecycle state and cancellation callbacks. | Nil/canceled/unbounded context, invalid ID/path, or incomplete hooks fails before publication. | Provider authority must be exact, finite, and independent of mutable catalog objects. |
| `FR-DATABASE-PROVIDER-LEASE-002` | MUST | The provider consumes a live lease. | Exactly one synchronous callback receives `Access`; target and hook operations are admitted only within that scope, expose the earliest caller/lease deadline, preserve immediate caller values, and retain caller/lease cancellation causes. Cleanup runs during return, panic, and `Goexit`; consumption does not unwind past any admitted operation that ignores cancellation. | Marks the lease consumed, counts all in-flight operations, then revokes it when the callback returns. | Repeated consumption, retained access, caller cancellation, panic/`Goexit`, hook failure, concurrent revocation, or deadline fails closed. | A callback or copied value cannot retain migration authority or let unproved provider work outlive the consuming boundary. |
| `FR-DATABASE-PROVIDER-LEASE-003` | MUST | Claims revokes and drains a child before releasing its migration guard. | `Revoke` closes admission and synchronously cancels every admitted context without waiting for provider code; `Wait` returns only after callback and all operations drain, or reports its own exact cancellation cause. | Monotonically transitions live → revoked → drained; never reopens. | Nil/repeated revoke is safe as specified; a callback ignoring cancellation keeps drain incomplete and therefore cannot authorize fence release. | Claims must never wait while holding provider locks, and unproved quiescence must fail-stop. |
| `FR-DATABASE-PROVIDER-LEASE-004` | MUST | Repository code imports or invokes the neutral capability. | Only the exact claims bridge may construct/revoke/wait and only the exact provider bridge may consume it. | None. | Any other production importer or cross-role package call fails an architecture test. | The neutral package must not become a general authority bypass. |

## Data And State Model

`Lease` contains only a pointer to shared private state. The state retains the
typed ID, canonical path, bounded context/cancel function, hook closures,
one-consumer flag, revocation cause, callback flag, in-flight count, scope
generation, and one close-only drain channel. `Access` contains the same state
pointer plus the admitted scope generation and exact consuming caller context.

## Surface Ownership

Owns: CODE internal/databaseproviderlease/**
Owns: TEST internal/databaseproviderlease/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `New`, `Lease`, `Hooks` | Mint one exact finite child authority from claims-owned closures. | `FR-DATABASE-PROVIDER-LEASE-001`, `FR-DATABASE-PROVIDER-LEASE-004` |
| Internal Go API | `Consume`, `Access` | Admit one provider scope and invoke target-bound checked hooks without exposing claims. | `FR-DATABASE-PROVIDER-LEASE-002`, `FR-DATABASE-PROVIDER-LEASE-004` |
| Internal Go API | `Revoke`, `Wait` | Separate nonblocking cancellation from bounded drain proof. | `FR-DATABASE-PROVIDER-LEASE-003`, `FR-DATABASE-PROVIDER-LEASE-004` |

## Algorithms And Ordering

1. Validate target, hooks, parent state, and finite deadline; clamp lifetime.
2. Share one lifecycle state through every lease/access value copy.
3. On consume, atomically win the one-consumer transition and invoke the
   callback without holding the lifecycle mutex.
4. Admit hook calls only for the current live scope, count them, combine the
   scope and operation contexts with the earliest deadline and operation
   values, call hooks outside the mutex, then decrement exactly once.
5. Callback completion, explicit revoke, parent cancellation, or deadline
   closes admission and cancels the capability. Drain closes only after the
   callback and all operations return.
6. Claims revokes children before waiting; a wait timeout leaves authority
   failed closed rather than pretending the provider is quiescent.

## Cross-Feature Behavior

Physical claims will mint the lease from `MigrationRefreshingGuard`; the
managed SQLite provider will consume it while draining/opening offline state.
Neither dependency is active in this slice. D5 orchestration must complete the
provider scope before releasing the migration guard.

## Failure And Edge Cases

- A lease cannot be consumed twice, even through value copies.
- A retained `Access` cannot read the target or invoke hooks after scope end.
- Revocation is nonblocking even while a hook is stalled.
- A caller-bounded `Wait` reports incomplete drain; the underlying drain may
  complete later, but the capability never reopens.
- `Consume` itself waits without a timeout after revocation. If admitted
  provider code ignores cancellation, the migration remains deliberately
  fenced rather than returning a false quiescence result.
- Callback and hook errors remain observable and are joined with external
  cancellation causes where both apply.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-PROVIDER-LEASE-001`, `FR-DATABASE-PROVIDER-LEASE-002`, `FR-DATABASE-PROVIDER-LEASE-003` | [lease_test.go](../../internal/databaseproviderlease/lease_test.go) |
| `FR-DATABASE-PROVIDER-LEASE-004` | [import_guard_test.go](../../internal/databaseproviderlease/import_guard_test.go) |

## Implementation Anchors

- [lease.go](../../internal/databaseproviderlease/lease.go)
