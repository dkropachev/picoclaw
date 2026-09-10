# Database Readiness Foundation

## Feature ID

`FR-DATABASE-READINESS`

## Behavior Summary

PicoClaw defines a dormant provider-neutral readiness probe over one claimed,
immutable physical catalog and explicit adapter registry. It classifies every
store, performs bounded catalog-wide legacy discovery, and retains ready
inspection references for typed adoption while the original lease remains
live.

The snapshot is not connected to IPC or runtime startup in this stage. It
creates no store, runs no migration, and changes no application authority.

## Reconstruction Notes

- Similarity target: make readiness a complete, bounded, ownership-checked
  decision rather than a filename or successful-open heuristic.
- Core APIs: `Probe`, `Snapshot.Statuses`, `Snapshot.Adopt`, and `Snapshot.Close`.
- Runtime ordering: prove registry completeness before metadata access, hold an
  exclusive refreshing catalog guard, inspect and reconcile every store,
  rebuild generation exclusions, discover legacy input under one aggregate
  budget as the final filesystem classification read, then perform one bounded,
  non-materializing generation revalidation before pure validation and release.
- Non-obvious constraint: status access and adoption remain lease-bound; a
  detached snapshot is not provider authority.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-READINESS-001` | MUST | Trusted infrastructure calls `Probe` with a live complete claim lease and explicit adapter registry. | Registry completeness is proven before generation metadata access. Under one exclusive refreshing lease/fence guard, every store is inspected with a bounded context, newly materialized members are claimed immediately, all generation exclusions are rebuilt, and every observation is reconciled/revalidated before publication. Each store receives one deterministic provider-neutral status derived from existence, integrity, version, typed contract, empty policy, import horizon, and legacy presence. Unix inspection uses descriptor-free live-member hardening that narrows SQLite-compatible legacy modes and preserves process-scoped locks; Windows retains handle-scoped DACL hardening. | Existing owner-controlled directory permissions, compatible Unix generation-file modes, and Windows member DACLs may be hardened. Probe may add monotonic physical claims, but creates or migrates no store and retains only ready existing inspection references. | Nil/expired lease, missing adapter, unsafe permission, replaced generation, invalid status set, cancellation, provider failure, or any cleanup failure fails the whole probe without paths or SQL diagnostics. | Startup needs a complete decision under the same physical ownership boundary. |
| `FR-DATABASE-READINESS-002` | MUST | Legacy files or directories may affect readiness. | Discovery excludes every post-inspection catalog generation member, validates every root ancestor through chained platform no-follow handles at each named observation, and retains pinned directory/file handles while using their entries or identities. Unix opens children relative to the retained parent with `O_NOFOLLOW`, `O_NONBLOCK`, and `O_DIRECTORY` for directories; Windows opens reparse-aware metadata/list handles while retaining no-delete-share ancestry during each full-path open. Opened and named full identities must match before and after use. One aggregate ceiling covers 65,536 visited entries, 32,768 visited files, depth 64, and bounded reads; repeated or overlapping roots consume that budget again. | Discovery performs metadata and directory-entry reads only; it never opens legacy file contents. | Reparse/symlink, FIFO/device, irregular file, identity drift, physical generation alias, excessive traversal, or read failure is unavailable/integrity, never ordinary legacy input. FIFO or path replacement cannot turn discovery into an unbounded blocking open. | Legacy detection must be deterministic, race-resistant, and attacker-bounded. |
| `FR-DATABASE-READINESS-003` | MUST | A caller reads, adopts, or closes a successful snapshot. | Statuses are detached and exposed only while the lease remains live. `Adopt(StoreID)` holds the lease guard and transfers exactly that ready inspection once with its immutable inspected timeout; `Close` acquires the lease guard before snapshot state and releases all remaining references. | Adoption transfers one provider pool; close releases unadopted pools. | Unknown/non-ready ID, expired lease, repeated/racing adoption, or release failure fails closed without affecting another reference. If lease authority is already lost, close reports that failure and still attempts every release once. | Readiness and ownership must share the same checked lease and pool. |

## Data And State Model

A `Snapshot` retains detached sorted `StoreStatus` values, ready inspection
references keyed by typed `StoreID`, the originating lease, and close/adoption
synchronization. It exposes no physical catalog fields.

## Surface Ownership

Owns: CODE internal/databasereadiness/**
Owns: TEST internal/databasereadiness/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `Probe`, `Snapshot.Statuses` | Produce and expose one complete lease-bound status set. | `FR-DATABASE-READINESS-001`, `FR-DATABASE-READINESS-002` |
| Internal Go API | `Snapshot.Adopt`, `Snapshot.Close` | Transfer ready pools or release them while retaining claim authority. | `FR-DATABASE-READINESS-003` |

## Algorithms And Ordering

1. Preflight a detached lease catalog and reject an incomplete registry before
   generation metadata access.
2. Hold `Lease.GuardStoresRefreshing`; pre-refresh claims, inspect each store,
   reconcile each inspection-created member, and revalidate its exact state.
3. Reconcile the complete batch, revalidate all existing and missing
   observations, rebuild generation exclusions, then discover legacy inputs
   through pinned no-follow/nonblocking handles and classify under one bounded
   context and aggregate traversal budget.
4. Treat legacy discovery as the last filesystem classification snapshot,
   check parent cancellation, and revalidate every successful generation once
   more without opening SQLite or reconciling on the success path. Any final
   revalidation failure aborts so failure cleanup reconciles before releasing
   references.
5. Validate the complete sorted status set and retain only ready pools.
6. Guard typed adoption and close with lease/fence authority before the
   snapshot mutex; cleanup failures abort rather than becoming a store status.

## Cross-Feature Behavior

Physical claims supply the immutable lease; adapter contracts supply typed
schema expectations; SQLite inspection supplies exact pool references. No
readiness value is published through broker IPC until later owner composition.

## Failure And Edge Cases

- Missing or empty legacy-free `initialize_online` stores may be ready without
  being created; `migrate_offline` stores require migration.
- Too-new, corrupt, locked, and unavailable remain distinct statuses.
- A closed or physically poisoned lease exposes no stale statuses.
- Snapshot close never closes a pool already transferred by adoption.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-READINESS-001`, `FR-DATABASE-READINESS-003` | [internal/databasereadiness/readiness_test.go](../../internal/databasereadiness/readiness_test.go), [internal/databasereadiness/coverage_closeout_test.go](../../internal/databasereadiness/coverage_closeout_test.go), [internal/databasereadiness/readiness_delta_coverage_test.go](../../internal/databasereadiness/readiness_delta_coverage_test.go) |
| `FR-DATABASE-READINESS-001`, `FR-DATABASE-READINESS-002`, `FR-DATABASE-READINESS-003` | [internal/databasereadiness/readiness_review_test.go](../../internal/databasereadiness/readiness_review_test.go) |
| `FR-DATABASE-READINESS-002` | [internal/databasereadiness/legacy_discovery_test.go](../../internal/databasereadiness/legacy_discovery_test.go), [internal/databasereadiness/legacy_handle_security_unix_test.go](../../internal/databasereadiness/legacy_handle_security_unix_test.go), [internal/databasereadiness/legacy_handle_delta_coverage_unix_test.go](../../internal/databasereadiness/legacy_handle_delta_coverage_unix_test.go) |

## Implementation Anchors

- [internal/databasereadiness/readiness.go](../../internal/databasereadiness/readiness.go)
- [internal/databasereadiness/legacy_handle.go](../../internal/databasereadiness/legacy_handle.go)
