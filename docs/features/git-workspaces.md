# Git Workspaces

## Feature ID

`FR-GITWS`

## Behavior Summary

PicoClaw maintains reusable local git checkouts for agent work. A git workspace
records repository inventory and history, locks checkouts to active agent
sessions, preserves dirty work on a branch before release or drop, reports total
and ignored-file size, and exposes cleanup/drop controls through the agent tool,
launcher API, and frontend dashboard.

## Reconstruction Notes

- Similarity target: recreate a durable manager around a root directory with an
  `inventory.json` file and checkout subdirectories.
- Core types/functions: `gitworkspace.Manager`, `Options`, acquire/release/stat
  request/result structs, `NewGitWorkspaceTool`, API routes under
  `/api/git-workspaces`, and frontend API/page components.
- Runtime ordering: load config, construct the manager, acquire and lock before
  repository work, release at turn end, preserve dirty changes, then reconcile
  ignored-file cleanup and aged/oversized checkout drops.
- Non-obvious constraints: locked workspaces are never cleaned or dropped;
  dirty changes must be committed before unlock/drop; ignored-file size must
  include ignored files and directories, not only tracked git state.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-GITWS-001` | MUST | Config load with `git_workspaces` omitted or partially configured. | Effective root, max total size, ignored cleanup delay, and drop delay resolve to defaults. | No inventory mutation. | Empty root falls back under the configured workspace directory. | Operators need safe defaults without mandatory setup. |
| `FR-GITWS-002` | MUST | Acquire request with repository, optional ref, and session key. | A checked-out workspace path and lock metadata are returned. | Repository and workspace records plus allocation history are persisted. | Missing repository/session returns an error; an already locked checkout for another session causes a separate checkout to be allocated. | Concurrent sessions must not overwrite each other. |
| `FR-GITWS-003` | MUST | Repeated acquire for the same repository and session. | The same locked workspace is returned and heartbeat metadata is updated. | Lock heartbeat and history are persisted. | Dropped workspaces are ignored. | Tool retries should be idempotent for a turn. |
| `FR-GITWS-004` | MUST | Release request for a session with dirty workspace contents. | Workspace unlocks and reports the preserved branch name. | Dirty contents are committed on a `picoclaw/session/...` branch before lock removal. | Preserve failure keeps the error visible and records failure history. | Agent work must survive turn cleanup. |
| `FR-GITWS-005` | MUST | Stats or list request. | Totals include active workspace count, locked count, total bytes, ignored bytes, per-repo rollups, per-workspace status, and newest history. | No mutation. | Dropped workspaces remain in history/status but are excluded from active totals. | UI and cleanup policies require accurate inventory. |
| `FR-GITWS-006` | MUST | Clean ignored request for an unlocked workspace. | Before/after ignored byte counts and refreshed workspace info are returned. | Ignored files are removed and cleanup history is persisted. | Locked, missing, or dropped workspaces return errors. | Generated caches should be recoverable without deleting work. |
| `FR-GITWS-007` | MUST | Drop request for an unlocked workspace. | Dropped workspace info is returned and the checkout path is removed. | Dirty changes are preserved first; drop time and history are persisted. | Locked, missing, or dropped workspaces return errors. | Operators need manual reclamation without losing changes. |
| `FR-GITWS-008` | MUST | Reconcile request or turn-end maintenance. | Eligible workspaces are cleaned or dropped and final stats are returned. | Ignored files older than the configured cleanup delay are removed; unlocked workspaces older than drop delay or exceeding max total size are dropped. | Locked workspaces are skipped. | Disk usage must be bounded automatically. |
| `FR-GITWS-009` | MUST | Agent tool call `git_workspace`. | Actions acquire, list/status, release, clean ignored, drop, and reconcile map to manager operations and return JSON. | Mutating actions persist through the manager. | Missing manager or invalid action returns tool errors. | Agents need a first-class path to allocate reusable checkouts. |
| `FR-GITWS-010` | MUST | Launcher API calls and frontend dashboard interactions. | API returns JSON stats/results; UI shows inventory/history/limits and exposes refresh, maintain, clean, and drop actions. | Cleanup/drop/reconcile mutate through API helpers only. | API config/load errors return HTTP errors; UI disables clean/drop on locked workspaces. | Users need visibility and manual controls for local checkouts. |

## Data And State Model

The manager root contains `inventory.json` and `checkouts/`. The inventory stores
repository records, workspace records, lock metadata, preserved branch names,
drop timestamps, and a bounded event history. A filesystem `inventory.lock`
directory coordinates separate manager instances in the same process or across
launcher API requests. Workspace IDs are deterministic hash-derived values with
numeric suffixes for concurrent locked checkouts.

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
| Tool | `git_workspace` | Agent-callable acquire/list/status/release/clean/drop/reconcile operations with JSON results. | `FR-GITWS-002` through `FR-GITWS-009` |
| HTTP | `/api/git-workspaces*` | Launcher-authenticated inventory, reconcile, cleanup, and drop endpoints. | `FR-GITWS-005` through `FR-GITWS-010` |
| Frontend | Git Workspaces dashboard and config fields | Browser inventory/maintenance surface and limit configuration. | `FR-GITWS-001`, `FR-GITWS-010` |

## Algorithms And Ordering

1. Normalize repository paths or remote URLs and require a non-empty session key
   for acquire/release.
2. Load inventory under a manager mutex.
3. Reuse an existing session lock, reuse an unlocked matching checkout, or clone
   a new checkout when another session holds the available checkout.
4. Before release/drop, inspect git status. If dirty, create/update a
   `picoclaw/session/{session}/{timestamp}` branch, add all changes, and commit.
5. For stats, walk checkout paths for total bytes and use git ignored status to
   find ignored roots without double-counting nested paths.
6. Reconcile skips locked workspaces, cleans old ignored files first, drops
   aged workspaces second, then drops oldest unlocked workspaces until total
   active size is within the configured limit.

## Cross-Feature Behavior

Agent conversations register `git_workspace` with the shared tool registry when
enabled and release session-held workspaces at turn end. Tool execution owns the
generic registry and provider schema behavior; this feature owns only the
specific git workspace tool semantics. Launcher management owns shared config
editing patterns, while this feature owns the git workspace fields and
dashboard behavior.

## Failure And Edge Cases

- Missing manager, root, repository, session key, or workspace ID returns a
  structured error at the relevant layer.
- Locked workspaces cannot be cleaned or dropped manually or automatically.
- Preserve failures are recorded in history and prevent silent data loss.
- Missing checkout paths are tolerated for dropped workspaces but surface errors
  for active stat collection when not caused by expected deletion.

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

## Implementation Anchors

- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/agent/git_workspace.go](../../pkg/agent/git_workspace.go)
- [pkg/tools/integration/git_workspace.go](../../pkg/tools/integration/git_workspace.go)
- [web/backend/api/git_workspaces.go](../../web/backend/api/git_workspaces.go)
- [web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx](../../web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx)
