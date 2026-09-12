# Database Physical Claims

## Feature ID

`FR-DATABASE-PHYSICAL-CLAIMS`

## Behavior Summary

PicoClaw defines a dormant process-external claim lease for every physical
namespace in one immutable store catalog, or for the exact selected stores in
an opaque review scope. A caller already holding the exact online or migration
home fence acquires sorted path-private lexical and existing physical
identities, strictly revalidates the governing catalog, and retains all claims
for the lease lifetime.

Claims and fences coordinate participating PicoClaw processes. They do not
exclude arbitrary same-user filesystem or SQLite writers. Runtime activation
must route every writer through the owner and use SQLite locking during
transitions. This stage opens no provider, publishes no IPC state, and changes
no application persistence path.

## Reconstruction Notes

- Similarity target: prevent two participating homes or owners from assigning
  one physical generation while paths materialize and controlled replacement
  occurs.
- Core APIs: `Acquire`, `AcquireProjected`, `AcquireReviewScope`, test-gated
  `PrepareRootForTesting`, `AcquireForTesting`, and
  `AcquireReviewScopeForTesting`, `Lease.Check`, `Guard`,
  `GuardStores`, `GuardStoresRefreshing`, `GuardStoresMigrating`,
  `MigrationRefreshingGuard`, `NewProviderLease`, `PinReplacement`,
  `DiscardReplacement`, `Refresh`, `RefreshReplacement`, and `Close`.
- Runtime ordering: project lexical identities, observe role-aware physical
  assignments, lock all sorted IDs, strictly revalidate the immutable catalog,
  compare repeated typed observations, prove every identity locked, and
  publish only after a final strict catalog/fence/handle check.
- Non-obvious constraints: claim filenames are SHA-256 digests in a stable
  owner-private user-cache root; identities and old claims accumulate
  monotonically; controlled replacement requires an exclusive fence and an
  exact StoreID-bound pinned stage.
- A review lease uses the same unsalted claim IDs as a complete lease, but
  acquires only selected identities. It retains the complete catalog as a
  validation-only deny-set: current unselected observations are replaceable,
  while selected observations and claims remain monotonic.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-PHYSICAL-CLAIMS-001` | MUST | Trusted infrastructure calls `Acquire` or `AcquireProjected` with a catalog and live fence authorizing its exact canonical home. | Every lexical claim and existing role-aware physical assignment (StoreID, role, path, regular/directory type, full platform identity, digest) is locked before strict catalog revalidation. Two matching observations, a final strict revalidation/observation, proof that every identity is locked, and final fence/claim validation precede publication. Duplicate physical assignments are rejected rather than flattened. | Owner-private persistent claim files may be created; OS locks remain held by the lease. | Nil/wrong/expired fence, invalid/excessive identity, unsafe claim root/file, duplicate assignment, contention, catalog/ancestor drift, or partial acquisition releases all partial state and fails closed. | A catalog snapshot or deduplicated identity set cannot prove exclusive physical assignment. |
| `FR-DATABASE-PHYSICAL-CLAIMS-002` | MUST | A lease exposes catalog data or transfers short-lived ownership work. | `Home`, `Stores`, and `Lookup` return detached data only while the retained fence and every claim pathname still name their locked objects. `Check` validates authority; `Guard`/`GuardStores` retain it across a handoff. `GuardStoresRefreshing` holds an exclusive lease/fence guard, pre-refreshes all assignments, and returns a non-reentrant reconciliation callback plus release. `GuardStoresMigrating` requires an exclusive migration fence and returns an opaque guard whose methods operate without reacquiring the already-held lease mutex. `NewProviderLease` derives one finite one-consumer child for an exact claimed StoreID/path and returns a drain closure that revokes admission before waiting for all provider work. Only one undrained child exists per guard; guard release closes new child/hook admission, revokes and drains that child while retaining the fence and lease mutex, then performs final reconciliation and unlock. | Ordinary reads mutate nothing; a refreshing guard may add monotonic claims and a child allocates only in-process revocation state. A detected loss permanently poisons the lease. | Nil/closed/poisoned lease, invalid or unclaimed ID, simultaneous child, unresolved prior pin, unbounded/canceled child context, wrong fence mode, fence replacement, lock-path replacement, reconcile after release, or close/refresh race exposes no stale catalog and cannot deadlock. | Consumers must not outlive or race the exact physical authority they rely on. |
| `FR-DATABASE-PHYSICAL-CLAIMS-003` | MUST | Catalog sidecars materialize or an exclusively fenced staged generation is about to replace one claimed store. | `Refresh` and readiness refreshing reconciliation strictly revalidate the retained catalog, compare role-aware observations, monotonically claim new identities, repeat revalidation/observation, and reject ordinary main replacement. Migration-guard ordinary reconciliation permits sidecar/legacy changes but requires every main to retain its approved baseline, established at lease acquisition and advanced only by exact pinned replacement reconciliation. `MigrationRefreshingGuard.PinReplacement` first validates that invariant. It and `Lease.PinReplacement` reconcile the catalog, require an absolute canonical ancestor-safe regular single-link stage, lock/recheck it, permanently record its target assignment and canonical path, and retain an exact no-follow handle until replacement reconciliation consumes the active association. Exact active-pin recheck requires the same StoreID, catalog target assignment, canonical stage path, physical identity, single-link handle, physical claim, child scope, and live migration fence; a generic guard check or a superseding pin cannot validate an old path. `DiscardReplacement` immediately retires an unused published pin and is a no-op when none exists, while retaining monotonic claim/assignment history. The migration guard holds one checked migration fence continuously across pin, exact rechecks, discard, and promotion; release performs mandatory validation/reconciliation, retires any remaining unused pins, then checks authority once more. Retired stages can never materialize or pin for another target. | New physical claim locks and bounded active stage handles accumulate until replacement consumption or retirement; assignment history remains monotonic. | Unknown StoreID is rejected before mutation without poisoning. Shared online fence, missing main materialized without a pin inside a migration guard, stale/new catalog alias, relative/directory/hardlinked/ancestor-aliased stage, stale/foreign/wrong-path pin proof, unpinned/unreconciled/retired/different replacement, contention, identity drift, or claim/resource limit permanently poisons/fails the lease. | Replacement authority must be explicit, target-bound, identity-and-path-pinned, and continuously provable during final live-source validation; an installed generation cannot escape final reconciliation. |
| `FR-DATABASE-PHYSICAL-CLAIMS-004` | MUST | The lease closes or acquisition unwinds. | Claim-lock and retained stage handles close once in reverse acquisition order; repeated/nil close is safe. | OS locks and stage handles release, while private claim files may persist for reuse. | Close errors are joined without skipping remaining handles; retired/consumed pin resources are removed from the bounded ordered ledger. | Partial cleanup must not strand in-process ownership or hide a release failure. |
| `FR-DATABASE-PHYSICAL-CLAIMS-005` | MUST | Trusted infrastructure calls `AcquireReviewScope` with an opaque immutable review scope and a live fence for its exact home. | The lease acquires the ordinary unsalted lexical and existing physical IDs for selected specs only, exposes only detached selected stores plus `ScopeFingerprint`, `FullCatalogFingerprint`, and sorted `StoreIDs`, and conflicts with selected claims held by review or complete leases. The full catalog is revalidated and observed before acquisition publication and at every exposure, check, guard entry/release, refresh, and migration boundary. | Selected identity/assignment history, selected replacement-pin assignment history, and selected claims accumulate monotonically. The current unselected observation is replaced on each validation and acquires no claim solely because it is unselected. | Scope/fingerprint/binding drift, current selected-to-unselected aliasing, reuse of a historically selected or pinned identity by an unselected assignment, missing selected claim, or partial acquisition fails closed and releases all acquired handles. Unselected-only materialization, deletion, replacement, inode reassignment, and inode reuse remain valid when they do not intersect selected authority. | A subset owner must conflict through the same physical locks without freezing or exposing unrelated stores, while the complete catalog still defines the collision boundary. |

## Data And State Model

A `Lease` retains the strict immutable selected catalog, canonical home,
claim-cache root, detached index, all locked claim handles, lexical identities,
typed role-aware observations, monotonic opaque selected identity sets,
StoreID-bound retained replacement paths, identities, handles, and the
originating fence. A review lease additionally retains opaque scope
fingerprints and a complete catalog validator with a replaceable current
unselected deny-set. No claim name contains a path or logical ID.

## Surface Ownership

Owns: CODE internal/databaseclaims/**
Owns: TEST internal/databaseclaims/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `Acquire`, `AcquireProjected`, `Lease.Check`, `Stores`, `Lookup` | Acquire and expose one complete immutable catalog under retained physical authority. | `FR-DATABASE-PHYSICAL-CLAIMS-001`, `FR-DATABASE-PHYSICAL-CLAIMS-002` |
| Internal Go API | `AcquireReviewScope`, `Lease.ScopeFingerprint`, `FullCatalogFingerprint`, `StoreIDs` | Acquire only the immutable review selection while continuously validating the complete collision boundary; expose path-free scope metadata and selected stores only. | `FR-DATABASE-PHYSICAL-CLAIMS-005` |
| Test-only internal Go API | `PrepareRootForTesting`, `AcquireForTesting`, `AcquireReviewScopeForTesting` | In a Go test process only, canonicalize/secure one explicit private root and route real lifecycle locks there; architecture tests forbid production callers and a non-test binary is rejected before mutation. | `FR-DATABASE-PHYSICAL-CLAIMS-001`, `FR-DATABASE-PHYSICAL-CLAIMS-005` |
| Internal Go API | `Lease.Guard`, `Lease.GuardStores`, `Lease.GuardStoresRefreshing` | Keep lease and fence authority live across a short handoff; the refreshing form exclusively reconciles members materialized within it. | `FR-DATABASE-PHYSICAL-CLAIMS-002`, `FR-DATABASE-PHYSICAL-CLAIMS-003` |
| Internal Go API | `Lease.GuardStoresMigrating`, `MigrationRefreshingGuard`, `NewProviderLease`, `CheckReplacement` | Hold one exclusive migration fence and lease mutex across detached catalog access, finite child-provider authority, checks, ordinary reconciliation, target-bound stage pinning/exact recheck/discard, replacement promotion, unused-pin retirement, and mandatory final reconciliation. | `FR-DATABASE-PHYSICAL-CLAIMS-002`, `FR-DATABASE-PHYSICAL-CLAIMS-003` |
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
   replacement. Derive one bounded child lease from the guard's exact StoreID
   and path; revoke and drain it before guard release, with release itself
   performing the same drain as a fail-safe. Admit no second child and no child
   while an earlier pin is unresolved. Pin the final stage before final
   live-source validation or any main appearance/change, recheck its exact path
   and identity throughout that validation, discard an unused pin before deleting its stage,
   promote only the pinned identity after cutover, reconcile every
   non-materializing provider open/close, and require release to
   validate/reconcile again, retire any remaining unused pin, then perform a
   final fence/claim check before unlocking.
8. Poison on ambiguous authority and close handles in reverse order.
9. For a review scope, perform the same acquisition over the selected catalog
   only. At the initial, final, exposure, check, guard, refresh, and migration
   and final guard-release checkpoints, reconstruct the immutable scope,
   require both fingerprints and bindings unchanged, strictly revalidate and
   observe the complete catalog, and reject any current selected/unselected
   cross-assignment. Replace the unselected deny-set after each successful
   observation; do not require one unselected generation or assignment to equal
   the next. Retain selected and replacement-pin assignment history.

## Cross-Feature Behavior

The provider catalog supplies immutable specs and opaque lexical claim IDs;
storage contracts supply stable physical identities; local IPC supplies shared
online and exclusive migration fences. Separately specified readiness and
migration consumers acquire a lease, but claims themselves never open a
database.

The privileged review-scope catalog bridge supplies both a selected ownership
catalog and its complete validation catalog. Only selected specs can reach
lease stores, lookup, refresh, migration, provider targets, or lock acquisition;
the complete catalog is retained solely to reject aliases and scope drift.

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
- A current generic authority check cannot substitute for an exact active-pin
  recheck; stale, superseded, wrong-path, wrong-child, and foreign-stage proofs
  fail closed.
- Old and replacement claims remain held until final close.
- Releasing a migration guard after an installed but unreconciled replacement
  fails, poisons the lease, and still retires retained stage handles.
- Review leases tolerate arbitrary unselected generation churn but reject a
  current selected/unselected hardlink, an unselected stage alias, or later
  reassignment of any identity previously observed for a selected member.
- Review acquisition failure, normal close, and process death release every
  selected OS lock; persistent claim files remain harmless and reusable.
- Guard release validates live fence/claim authority and the complete review
  collision boundary before unlocking; a transient loss or alias permanently
  poisons the lease even if the filesystem is restored before the next call.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-PHYSICAL-CLAIMS-001`, `FR-DATABASE-PHYSICAL-CLAIMS-004` | [internal/databaseclaims/claims_test.go](../../internal/databaseclaims/claims_test.go), [internal/databaseclaims/coverage_closeout_test.go](../../internal/databaseclaims/coverage_closeout_test.go), [internal/databaseclaims/testing_api_guard_test.go](../../internal/databaseclaims/testing_api_guard_test.go), [internal/databaseclaims/testing_api_external_test.go](../../internal/databaseclaims/testing_api_external_test.go) |
| `FR-DATABASE-PHYSICAL-CLAIMS-002`, `FR-DATABASE-PHYSICAL-CLAIMS-003` | [internal/databaseclaims/lease_safety_test.go](../../internal/databaseclaims/lease_safety_test.go), [internal/databaseclaims/claims_unix_test.go](../../internal/databaseclaims/claims_unix_test.go), [internal/databaseclaims/claims_windows_test.go](../../internal/databaseclaims/claims_windows_test.go), [internal/databaseclaims/hardening_regression_test.go](../../internal/databaseclaims/hardening_regression_test.go), [internal/databaseclaims/refreshing_guard_test.go](../../internal/databaseclaims/refreshing_guard_test.go), [internal/databaseclaims/refreshing_guard_coverage_test.go](../../internal/databaseclaims/refreshing_guard_coverage_test.go), [internal/databaseclaims/migration_refreshing_guard_test.go](../../internal/databaseclaims/migration_refreshing_guard_test.go), [internal/databaseclaims/provider_lease_test.go](../../internal/databaseclaims/provider_lease_test.go), [internal/databaseclaims/windows_semantics_test.go](../../internal/databaseclaims/windows_semantics_test.go) |
| `FR-DATABASE-PHYSICAL-CLAIMS-005` | [internal/databaseclaims/scoped_claims_test.go](../../internal/databaseclaims/scoped_claims_test.go), [internal/databaseclaims/scoped_claims_process_test.go](../../internal/databaseclaims/scoped_claims_process_test.go), [internal/databaseclaims/testing_api_guard_test.go](../../internal/databaseclaims/testing_api_guard_test.go) |

## Implementation Anchors

- [internal/databaseclaims/claims.go](../../internal/databaseclaims/claims.go)
- [internal/databaseclaims/scoped_claims.go](../../internal/databaseclaims/scoped_claims.go)
- [internal/databaseclaims/provider_lease.go](../../internal/databaseclaims/provider_lease.go)
- [internal/databaseclaims/root.go](../../internal/databaseclaims/root.go)
