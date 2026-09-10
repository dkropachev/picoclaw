# Database Physical Claims

## Feature ID

`FR-DATABASE-PHYSICAL-CLAIMS`

## Behavior Summary

PicoClaw defines a dormant process-external claim lease for every physical
namespace in one immutable store catalog. A caller already holding the exact
online or migration home fence acquires sorted path-private lexical and
existing physical identities, strictly revalidates the catalog, and retains
all claims for the lease lifetime.

Claims and fences coordinate participating PicoClaw processes. They do not
exclude arbitrary same-user filesystem or SQLite writers. Runtime activation
must route every writer through the owner and use SQLite locking during
transitions. This stage opens no provider, publishes no IPC state, and changes
no application persistence path.

## Reconstruction Notes

- Similarity target: prevent two participating homes or owners from assigning
  one physical generation while paths materialize and controlled replacement
  occurs.
- Core APIs: `Acquire`, `AcquireProjected`, test-gated `PrepareRootForTesting`
  and `AcquireForTesting`, `Lease.Check`, `Guard`,
  `GuardStores`, `GuardStoresRefreshing`, `GuardStoresMigrating`,
  `MigrationRefreshingGuard`, `PinReplacement`, `Refresh`,
  `RefreshReplacement`, and `Close`.
- Runtime ordering: project lexical identities, observe role-aware physical
  assignments, lock all sorted IDs, strictly revalidate the immutable catalog,
  compare repeated typed observations, prove every identity locked, and
  publish only after a final strict catalog/fence/handle check.
- Non-obvious constraints: claim filenames are SHA-256 digests in a stable
  owner-private user-cache root; identities and old claims accumulate
  monotonically; controlled replacement requires an exclusive fence and an
  exact StoreID-bound pinned stage.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-PHYSICAL-CLAIMS-001` | MUST | Trusted infrastructure calls `Acquire` or `AcquireProjected` with a catalog and live fence authorizing its exact canonical home. | Every lexical claim and existing role-aware physical assignment (StoreID, role, path, regular/directory type, full platform identity, digest) is locked before strict catalog revalidation. Two matching observations, a final strict revalidation/observation, proof that every identity is locked, and final fence/claim validation precede publication. Duplicate physical assignments are rejected rather than flattened. | Owner-private persistent claim files may be created; OS locks remain held by the lease. | Nil/wrong/expired fence, invalid/excessive identity, unsafe claim root/file, duplicate assignment, contention, catalog/ancestor drift, or partial acquisition releases all partial state and fails closed. | A catalog snapshot or deduplicated identity set cannot prove exclusive physical assignment. |
| `FR-DATABASE-PHYSICAL-CLAIMS-002` | MUST | A lease exposes catalog data or transfers short-lived ownership work. | `Home`, `Stores`, and `Lookup` return detached data only while the retained fence and every claim pathname still name their locked objects. `Check` validates authority; `Guard`/`GuardStores` retain it across a handoff. `GuardStoresRefreshing` holds an exclusive lease/fence guard, pre-refreshes all assignments, and returns a non-reentrant reconciliation callback plus release. `GuardStoresMigrating` requires an exclusive migration fence and returns an opaque guard whose methods operate without reacquiring the already-held lease mutex. | Ordinary reads mutate nothing; a refreshing guard may add monotonic claims. A detected loss permanently poisons the lease. | Nil/closed/poisoned lease, wrong fence mode, fence replacement, lock-path replacement, reconcile after release, or close/refresh race exposes no stale catalog and cannot deadlock. | Consumers must not outlive or race the exact physical authority they rely on. |
| `FR-DATABASE-PHYSICAL-CLAIMS-003` | MUST | Catalog sidecars materialize or an exclusively fenced staged generation is about to replace one claimed store. | `Refresh` and readiness refreshing reconciliation strictly revalidate the retained catalog, compare role-aware observations, monotonically claim new identities, repeat revalidation/observation, and reject ordinary main replacement. Migration-guard ordinary reconciliation permits sidecar/legacy changes but requires every main to retain its approved baseline, established at lease acquisition and advanced only by exact pinned replacement reconciliation. `MigrationRefreshingGuard.PinReplacement` first validates that invariant. It and `Lease.PinReplacement` reconcile the catalog, require an absolute canonical ancestor-safe regular single-link stage, lock/recheck it, permanently record its target assignment, and retain an exact no-follow handle until replacement reconciliation consumes the active association. The migration guard holds one checked migration fence continuously across pin and promotion; release performs mandatory validation/reconciliation, retires unused pins, then checks authority once more. Retired stages can never materialize or pin for another target. | New physical claim locks and bounded active stage handles accumulate until replacement consumption or close; assignment history remains monotonic. | Unknown StoreID is rejected before mutation without poisoning. Shared online fence, missing main materialized without a pin inside a migration guard, stale/new catalog alias, relative/directory/hardlinked/ancestor-aliased stage, unpinned/unreconciled/retired/different replacement, contention, identity drift, or claim/resource limit permanently poisons/fails the lease. | Replacement authority must be explicit, target-bound, identity-pinned, and unavailable while shared owners exist; an installed generation cannot escape final reconciliation. |
| `FR-DATABASE-PHYSICAL-CLAIMS-004` | MUST | The lease closes or acquisition unwinds. | Claim-lock and retained stage handles close once in reverse acquisition order; repeated/nil close is safe. | OS locks and stage handles release, while private claim files may persist for reuse. | Close errors are joined without skipping remaining handles; retired/consumed pin resources are removed from the bounded ordered ledger. | Partial cleanup must not strand in-process ownership or hide a release failure. |

## Data And State Model

A `Lease` retains the strict immutable catalog, canonical home, claim-cache
root, detached index, all locked claim handles, lexical identities, typed
role-aware observations, monotonic opaque identity sets, StoreID-bound retained
replacement handles, and the originating fence. No claim name contains a path
or logical ID.

## Surface Ownership

Owns: CODE internal/databaseclaims/**
Owns: TEST internal/databaseclaims/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `Acquire`, `AcquireProjected`, `Lease.Check`, `Stores`, `Lookup` | Acquire and expose one complete immutable catalog under retained physical authority. | `FR-DATABASE-PHYSICAL-CLAIMS-001`, `FR-DATABASE-PHYSICAL-CLAIMS-002` |
| Test-only internal Go API | `PrepareRootForTesting`, `AcquireForTesting` | In a Go test process only, canonicalize/secure one explicit private root and route real lifecycle locks there; architecture tests forbid production callers and a non-test binary is rejected before mutation. | `FR-DATABASE-PHYSICAL-CLAIMS-001` |
| Internal Go API | `Lease.Guard`, `Lease.GuardStores`, `Lease.GuardStoresRefreshing` | Keep lease and fence authority live across a short handoff; the refreshing form exclusively reconciles members materialized within it. | `FR-DATABASE-PHYSICAL-CLAIMS-002`, `FR-DATABASE-PHYSICAL-CLAIMS-003` |
| Internal Go API | `Lease.GuardStoresMigrating`, `MigrationRefreshingGuard` | Hold one exclusive migration fence and lease mutex across detached catalog access, checks, ordinary reconciliation, target-bound stage pinning, replacement promotion, unused-pin retirement, and mandatory final reconciliation. | `FR-DATABASE-PHYSICAL-CLAIMS-002`, `FR-DATABASE-PHYSICAL-CLAIMS-003` |
| Internal Go API | `Lease.Refresh`, `PinReplacement`, `RefreshReplacement` | Monotonically claim materialized or controlled replacement identities. | `FR-DATABASE-PHYSICAL-CLAIMS-003` |
| Internal Go API | `Lease.Close` | Release every handle once in reverse order. | `FR-DATABASE-PHYSICAL-CLAIMS-004` |

## Algorithms And Ordering

1. Require the exact live canonical-home fence and project the catalog without
   inspecting mutable missing leaves.
2. Derive lexical claim IDs, resolve existing stable physical IDs, sort the
   union, and acquire nonblocking private claim-file locks.
3. Strictly revalidate and retain the projected catalog; compare two typed
   role-aware observations, strictly revalidate/observe once more, prove every
   identity held, then validate the final fence and all claim handles.
4. Recheck fence and claim path identities before every exposure or handoff.
5. On refresh, guard the fence, strictly revalidate, observe assignments,
   acquire new identities, and repeat strict validation before commit; permit
   main replacement only when it equals the StoreID-bound retained stage.
6. For a separately specified guarded readiness consumer, pre-refresh before
   provider access, keep the lease and fence exclusively live, reconcile every
   materialized provider member, then release only after final observation
   revalidation.
7. For offline migration, acquire one migration-only refreshing guard and use
   its shared-state under-lock methods rather than copying authority or
   reentering lease APIs. Ordinary reconciliation may accept sidecar changes
   but must preserve every main's approved identity or absence from lease
   acquisition. Advance that baseline only after reconciling the exact pinned
   replacement; pin the stage before any main appears or changes, promote only
   that identity after cutover, reconcile every non-materializing provider
   open/close, and require release to validate/reconcile again, retire any
   unused pin, then perform a final fence/claim check before unlocking.
8. Poison on ambiguous authority and close handles in reverse order.

## Cross-Feature Behavior

The provider catalog supplies immutable specs and opaque lexical claim IDs;
storage contracts supply stable physical identities; local IPC supplies shared
online and exclusive migration fences. Separately specified readiness and
migration consumers acquire a lease, but claims themselves never open a
database.

## Failure And Edge Cases

- Hardlinked mains, main/sidecar pairs, and generation/legacy pairs within one
  catalog are invalid duplicate assignments; aliases across homes conflict on
  one physical claim even when lexical paths differ.
- Main and sidecar directories are invalid; legacy roots alone may be regular
  files or directories.
- Every claim-root and catalog ancestor is revalidated; symlink/reparse or
  mutation-authority changes poison the lease.
- A newly materialized hardlink alias is detected at refresh.
- Replacing a persistent claim pathname invalidates the lease even though the
  original locked inode remains open.
- A stage pin cannot authorize another StoreID or an already catalogued inode.
- Old and replacement claims remain held until final close.
- Releasing a migration guard after an installed but unreconciled replacement
  fails, poisons the lease, and still retires retained stage handles.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-PHYSICAL-CLAIMS-001`, `FR-DATABASE-PHYSICAL-CLAIMS-004` | [internal/databaseclaims/claims_test.go](../../internal/databaseclaims/claims_test.go), [internal/databaseclaims/coverage_closeout_test.go](../../internal/databaseclaims/coverage_closeout_test.go), [internal/databaseclaims/testing_api_guard_test.go](../../internal/databaseclaims/testing_api_guard_test.go), [internal/databaseclaims/testing_api_external_test.go](../../internal/databaseclaims/testing_api_external_test.go) |
| `FR-DATABASE-PHYSICAL-CLAIMS-002`, `FR-DATABASE-PHYSICAL-CLAIMS-003` | [internal/databaseclaims/lease_safety_test.go](../../internal/databaseclaims/lease_safety_test.go), [internal/databaseclaims/claims_unix_test.go](../../internal/databaseclaims/claims_unix_test.go), [internal/databaseclaims/claims_windows_test.go](../../internal/databaseclaims/claims_windows_test.go), [internal/databaseclaims/hardening_regression_test.go](../../internal/databaseclaims/hardening_regression_test.go), [internal/databaseclaims/refreshing_guard_test.go](../../internal/databaseclaims/refreshing_guard_test.go), [internal/databaseclaims/refreshing_guard_coverage_test.go](../../internal/databaseclaims/refreshing_guard_coverage_test.go), [internal/databaseclaims/migration_refreshing_guard_test.go](../../internal/databaseclaims/migration_refreshing_guard_test.go), [internal/databaseclaims/windows_semantics_test.go](../../internal/databaseclaims/windows_semantics_test.go) |

## Implementation Anchors

- [internal/databaseclaims/claims.go](../../internal/databaseclaims/claims.go)
- [internal/databaseclaims/root.go](../../internal/databaseclaims/root.go)
