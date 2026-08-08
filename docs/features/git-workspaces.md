# Git Workspaces

## Feature ID

`FR-GITWS`

## Behavior Summary

PicoClaw maintains reusable local git checkouts for agent work. A git workspace
records repository inventory and history, locks checkouts to active agent
sessions, preserves dirty work on a branch before release or drop, reports total
and ignored-file size, and exposes cleanup/drop controls through the agent tool,
launcher API, and frontend dashboard. Trusted controllers can instead request a
fresh checkout pinned to one exact source branch and commit without exposing
that stronger acquisition primitive to an agent tool.

## Reconstruction Notes

- Similarity target: recreate a durable manager around a root directory with an
  `inventory.json` file and checkout subdirectories.
- Core types/functions: `gitworkspace.Manager`, `Options`, acquire/release/stat
  request/result structs, `PinnedAcquireRequest`, `PinnedReleaseRequest`,
  `Manager.AcquirePinned`, `Manager.ReleasePinned`, `NewGitWorkspaceTool`, API
  routes under `/api/git-workspaces`, and frontend API/page components.
- Runtime ordering: load config, construct the manager, acquire and lock before
  repository work, or have a trusted controller validate and publish a fresh
  exact pinned checkout; heartbeat an owned pin without resetting work; release
  generic state at turn end or explicitly release a controller reservation,
  preserve descendant or dirty changes, then reconcile ignored-file cleanup and
  aged/oversized checkout drops.
- Non-obvious constraints: locked workspaces are never cleaned or dropped;
  dirty changes must be committed before unlock/drop; ignored-file size must
  include ignored files and directories, not only tracked git state. Pinned
  acquisition accepts only an exact lowercase 40- or 64-hex commit, publishes
  only a freshly staged and verified checkout, uses an opaque controller
  reservation key rather than a model turn session key, and remains a
  controller-only Go API rather than an action of the generic `git_workspace`
  tool.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-GITWS-001` | MUST | Config load with `git_workspaces` omitted or partially configured. | Effective root, max total size, ignored cleanup delay, and drop delay resolve to defaults. | No inventory mutation. | Empty root falls back under the configured workspace directory. | Operators need safe defaults without mandatory setup. |
| `FR-GITWS-002` | MUST | Acquire request with repository, optional ref, and session key. | A checked-out workspace path and lock metadata are returned. | Repository and workspace records plus allocation history are persisted using canonical SSH remote URLs when the input URL can be represented that way. | Missing repository/session returns an error; an already locked checkout for another session causes a separate checkout to be allocated. | Concurrent sessions must not overwrite each other. |
| `FR-GITWS-003` | MUST | Repeated acquire for the same repository and session. | The same locked workspace is returned and heartbeat metadata is updated. | Lock heartbeat and history are persisted. | Dropped workspaces are ignored. | Tool retries should be idempotent for a turn. |
| `FR-GITWS-004` | MUST | Release request for a session with dirty workspace contents. | Workspace unlocks and reports the preserved branch name. | Dirty contents are committed on a `picoclaw/session/...` branch before lock removal. | Preserve failure keeps the error visible and records failure history. | Agent work must survive turn cleanup. |
| `FR-GITWS-005` | MUST | Stats or list request. | Totals include active workspace count, locked count, total bytes, ignored bytes, per-repo rollups, per-workspace status, and newest history. | No mutation. | Dropped workspaces remain in history/status but are excluded from active totals. | UI and cleanup policies require accurate inventory. |
| `FR-GITWS-006` | MUST | Clean ignored request for an unlocked workspace. | Before/after ignored byte counts and refreshed workspace info are returned. | Ignored files are removed and cleanup history is persisted. | Locked, missing, or dropped workspaces return errors. | Generated caches should be recoverable without deleting work. |
| `FR-GITWS-007` | MUST | Drop request for an unlocked workspace. | Dropped workspace info is returned and the checkout path is removed. | Dirty changes are preserved first; drop time and history are persisted. | Locked, missing, or dropped workspaces return errors. | Operators need manual reclamation without losing changes. |
| `FR-GITWS-008` | MUST | Reconcile request or turn-end maintenance. | Eligible workspaces are cleaned or dropped and final stats are returned. | Ignored files older than the configured cleanup delay are removed; unlocked workspaces older than drop delay or exceeding max total size are dropped. | Locked workspaces are skipped. | Disk usage must be bounded automatically. |
| `FR-GITWS-009` | MUST | Agent tool call `git_workspace`. | Actions acquire, list/status, release, clean ignored, drop, and reconcile map to manager operations and return JSON. | Mutating actions persist through the manager. | Missing manager or invalid action returns tool errors. | Agents need a first-class path to allocate reusable checkouts. |
| `FR-GITWS-010` | MUST | Launcher API calls and frontend dashboard interactions. | API returns JSON stats/results; UI shows inventory/history/limits without long root paths in the summary metrics, displays normalized SSH remotes for legacy HTTPS rows when safe, exposes SSH remotes through a compact copy marker, labels the checkout branch column as current branch, shows compact checkout paths with a full-path copy action, and exposes refresh, maintain, clean, and drop actions. | Cleanup/drop/reconcile mutate through API helpers only. | API config/load errors return HTTP errors; UI disables clean/drop on locked workspaces. | Users need visibility and manual controls for local checkouts. |
| `FR-GITWS-011` | MUST | A trusted controller calls `Manager.AcquirePinned` with an exact repository, source branch, expected commit, opaque reservation key, and agent identity, then eventually calls `Manager.ReleasePinned` with that reservation and agent identity. | The manager returns a locked, detached checkout whose fetched source branch resolves to the exact lowercase 40- or 64-hex expected commit. A first acquisition uses a fresh isolated clone prepared and verified in an unpublished staging directory, atomically publishes it at its inventory-derived path, and only then records ownership; a matching same-reservation call heartbeats the existing checkout without fetching, checking out, cleaning, or resetting agent work. Generic `ReleaseSession` skips pinned reservations, while explicit pinned release preserves work and unlocks it. | Inventory durably records repository identity, pinned source ref, pinned commit, reservation/agent lock identity, heartbeat, and history. Pinned release preserves any clean descendant commit or dirty work on a unique create-only reservation branch before unlocking. All inventory access is serialized by a context-aware kernel advisory lock whose persistent legacy path fences out older directory-lock binaries. | Missing, whitespace-aliased, malformed, or noncanonical input fails before allocation. Repository/ref/commit/reservation/agent mismatches, an origin that is not one exact raw remote or has redirected push configuration, an escaped or symlink-substituted manager, checkout, or Git-internal path, a non-descendant `HEAD`, replacement/graft/sparse/index-hiding state, injected Git configuration, unpreservable dirty state, or another control-plane deviation fails closed without resetting work or adopting a generic reusable checkout. A failed/crashed staging attempt is never inventory-owned or reused. Pinned acquire/release are available only as Go controller APIs: they are not HTTP/frontend actions and are not registered as `git_workspace` tool actions. | A development controller must bind local code to provider-verified PR identity while preserving subsequent agent work and withholding checkout and lifecycle authority from untrusted model calls. |

## Data And State Model

The manager root contains `inventory.json`, the persistent kernel-lock target
`inventory.lock`, and `checkouts/`. The inventory stores repository records,
workspace records, generic refs, optional pinned source refs and commits, lock
metadata, preserved branch names, drop timestamps, and a bounded event history.
The advisory file lock coordinates manager instances and its ownership is
released by the kernel after process exit. Reusing the legacy directory-lock
path as a persistent file creates a quiescent upgrade fence: an older binary
cannot acquire its directory lock after a new binary has created the file, and a
new binary fails closed while an older process still owns the directory.
Workspace IDs are deterministic hash-derived values with numeric suffixes for
concurrent locked checkouts. A pinned checkout is
created under an unpublished staging name; only a fully verified atomic rename
to the inventory-derived checkout path may become inventory-owned, and an
unrecorded orphan is never eligible for reuse.

Config fields live under `git_workspaces`: `root_dir`,
`max_total_size_bytes`, `ignored_cleanup_delay_seconds`, and
`drop_delay_seconds`. The `tools.git_workspace.enabled` flag controls whether
the agent tool is registered.

## Surface Ownership

Owns: CODE pkg/gitworkspace/**
Owns: CODE pkg/agent/git_workspace.go
Owns: CODE pkg/tools/integration/git_workspace.go
Owns: CODE web/backend/api/git_workspaces.go
Owns: CODE web/frontend/src/api/git-workspaces.ts
Owns: CODE web/frontend/src/components/agent/git-workspaces/**
Owns: CODE web/frontend/src/routes/agent/git-workspaces.tsx
Owns: CONFIG.git_workspaces*
Owns: CONFIG.tools.git_workspace*
Owns: HTTP GET /api/git-workspaces
Owns: HTTP POST /api/git-workspaces/reconcile
Owns: HTTP POST /api/git-workspaces/cleanup
Owns: HTTP DELETE /api/git-workspaces/*
Owns: TEST pkg/gitworkspace/**
Owns: TEST pkg/tools/integration/git_workspace_test.go
Owns: TEST web/backend/api/git_workspaces_test.go
Owns: TEST web/frontend/src/api/git-workspaces.test.ts
Owns: TEST web/frontend/src/components/agent/git-workspaces/**
Owns: TOOL git_workspace

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `git_workspaces.*`, `tools.git_workspace.enabled` | Defines root, limits, retention delays, and tool enablement. | `FR-GITWS-001`, `FR-GITWS-009` |
| Go API | `(*gitworkspace.Manager).AcquirePinned(context.Context, gitworkspace.PinnedAcquireRequest)`, `(*gitworkspace.Manager).ReleasePinned(context.Context, gitworkspace.PinnedReleaseRequest)` | Controller-only exact acquisition binds repository, source branch, expected commit, opaque reservation, and agent identity to a fresh isolated checkout or a non-resetting heartbeat; explicit release safely preserves and unlocks only that reservation. | `FR-GITWS-011` |
| Tool | `git_workspace` | Agent-callable generic acquire/list/status/release/clean/drop/reconcile operations with JSON results. It has no pinned acquire/release actions, cannot supply pinned identity fields, and generic release skips pinned reservations. | `FR-GITWS-002` through `FR-GITWS-009`, `FR-GITWS-011` |
| HTTP | `/api/git-workspaces*` | Launcher-authenticated inventory, reconcile, cleanup, and drop endpoints. | `FR-GITWS-005` through `FR-GITWS-010` |
| Frontend | Git Workspaces dashboard and config fields | Browser inventory/maintenance surface and limit configuration. | `FR-GITWS-001`, `FR-GITWS-010` |

## Algorithms And Ordering

1. Normalize repository paths or remote URLs, prefer SCP-style SSH remotes for
   representable HTTP(S), `git://`, `ssh://`, and existing SCP-style remotes,
   and require a non-empty session key
   for acquire/release.
2. Acquire the context-aware kernel advisory lock, then load inventory under the
   manager mutex; fail closed when the platform cannot provide that lock.
3. Reuse an existing session lock, reuse an unlocked matching checkout, or clone
   a new checkout for generic acquisition when another session holds the
   available checkout. Never select a pinned checkout as generic reusable state.
4. For pinned acquisition, validate exact untrimmed identity fields, branch
   syntax, and lowercase 40- or 64-hex commit syntax before allocation. Under
   the inventory lock, accept a heartbeat only when repository, source ref,
   commit, opaque reservation, and agent identity all match the durable pin.
5. For a new pin, use a sanitized Git environment to clone without checkout or
   tags into a unique unpublished staging directory, fetch the exact source
   branch, require its resolved commit to equal the expected commit, check it out
   detached, and reject origin, path, object replacement, graft, sparse checkout,
   hidden-index, ancestry, or other control-plane deviations. Atomically rename
   the verified checkout to its exact inventory-derived path before persisting
   ownership; remove or ignore every failed or unrecorded candidate.
6. For a matching pin heartbeat, revalidate durable identity, exact origin and
   checkout path, safe Git control plane, and that the pinned commit remains an
   ancestor of `HEAD`; update only heartbeat/history so agent edits and
   descendant commits remain untouched.
7. Generic session release skips pinned reservations. On explicit pinned release,
   verify the reservation and agent identity before mutation. Before any
   release/drop, inspect git status. For generic dirty state, and for pinned dirty
   state or a clean descendant of the pin, create a unique create-only
   `picoclaw/session/{reservation}/{timestamp}` preservation branch, commit dirty
   contents when present, verify the checkout became clean, then unlock. Never
   overwrite an existing preservation ref, discard a pinned descendant, or
   unlock dirty state that Git cannot stage.
8. For stats, walk checkout paths for total bytes and use git ignored status to
   find ignored roots without double-counting nested paths.
9. Reconcile skips locked workspaces, cleans old ignored files first, drops
   aged workspaces second, then drops oldest unlocked workspaces until total
   active size is within the configured limit.

## Cross-Feature Behavior

Agent conversations register `git_workspace` with the shared tool registry when
enabled and release session-held workspaces at turn end. Tool execution owns the
generic registry and provider schema behavior; this feature owns only the
specific git workspace tool semantics. Generic turn finalization cannot release
a pin even if a model session key collides with its reservation. Launcher
management owns shared config editing patterns, while this feature owns the git
workspace fields and dashboard behavior. Trusted development controllers may
call `AcquirePinned` and `ReleasePinned` directly, but neither tool registration
nor the launcher API/frontend may translate an agent or browser request into
those operations. The controller-only local repair runner receives an
`AcquirePinned`-only interface and an exact request, never a raw path or
`ReleasePinned`; it acquires a fresh exact pin or heartbeats the matching pin
before model access, then reacquires it as postflight so this feature revalidates
repository, ancestry, and Git control-plane state while the separate security
contract confines ordinary content edits.

## Failure And Edge Cases

- Missing manager, root, repository, session key, or workspace ID returns a
  structured error at the relevant layer.
- Locked workspaces cannot be cleaned or dropped manually or automatically.
- Preserve failures are recorded in history and prevent silent data loss.
- Missing checkout paths are tolerated for dropped workspaces but surface errors
  for active stat collection when not caused by expected deletion.
- Pinned acquisition rejects repository-hash collisions instead of adopting an
  inventory record for a different raw repository identity.
- A pinned same-reservation retry is a heartbeat, not reconciliation: identity or
  control-plane drift returns an error, and local files, index state, and `HEAD`
  are not reset to the original pin.
- Generic release ignores pinned reservations. Explicit pinned release fails
  rather than preserving from an unrelated `HEAD` or unlocking dirty state that
  cannot be staged; clean descendant commits and preservable dirty changes
  receive a collision-safe branch before the lock is cleared.
- Inventory locking observes context cancellation, survives stale lock-file
  presence, and relies on kernel lock ownership so a crashed process cannot
  permanently strand the inventory.
- The persistent `inventory.lock` file is also a mixed-version fence: upgrades
  must be quiescent, and rollback to a directory-lock binary is rejected while
  that file exists rather than permitting concurrent inventory writers.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-GITWS-001` | [pkg/config/config_test.go](../../pkg/config/config_test.go) |
| `FR-GITWS-002` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-003` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-004` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-005` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go), [web/backend/api/git_workspaces_test.go](../../web/backend/api/git_workspaces_test.go) |
| `FR-GITWS-006` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go), [web/backend/api/git_workspaces_test.go](../../web/backend/api/git_workspaces_test.go) |
| `FR-GITWS-007` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go), [web/backend/api/git_workspaces_test.go](../../web/backend/api/git_workspaces_test.go) |
| `FR-GITWS-008` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-009` | [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go) |
| `FR-GITWS-010` | [web/backend/api/git_workspaces_test.go](../../web/backend/api/git_workspaces_test.go), [web/frontend/src/api/git-workspaces.test.ts](../../web/frontend/src/api/git-workspaces.test.ts), [web/frontend/src/components/agent/git-workspaces/git-workspaces-page.test.tsx](../../web/frontend/src/components/agent/git-workspaces/git-workspaces-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-GITWS-011` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go), [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go) |

## Implementation Anchors

- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/gitworkspace/inventory_lock_unix.go](../../pkg/gitworkspace/inventory_lock_unix.go)
- [pkg/gitworkspace/inventory_lock_windows.go](../../pkg/gitworkspace/inventory_lock_windows.go)
- [pkg/agent/git_workspace.go](../../pkg/agent/git_workspace.go)
- [pkg/tools/integration/git_workspace.go](../../pkg/tools/integration/git_workspace.go)
- [web/backend/api/git_workspaces.go](../../web/backend/api/git_workspaces.go)
- [web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx](../../web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx)
