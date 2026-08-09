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
that stronger acquisition primitive to an agent tool. A controller can retain
that checkout as one private development line, park its exact committed tip
while releasing the mutation reservation, resume it under a fresh reservation,
and snapshot a bounded exact-commit review without exposing the retained line
through generic workspace surfaces.
When a durable controller intent makes an expired Adopt or Resume recoverable,
two controller-only composite methods retain the old and fresh reservation
operation locks across the line transition and authority replacement. They
reuse inventory version 3's rotation chain so the stale bearer cannot regain
ownership in a replay-to-rotation gap.

## Reconstruction Notes

- Similarity target: recreate a durable manager around a root directory with an
  `inventory.json` file and checkout subdirectories.
- Core types/functions: `gitworkspace.Manager`, `Options`, acquire/release/stat
  request/result structs, `PinnedAcquireRequest`, `PinnedReleaseRequest`,
  `PinnedCandidateRequest`, `PinnedCommitRequest`, `Manager.AcquirePinned`,
  `Manager.WithPinnedOperation`, `Manager.SnapshotPinnedCandidate`,
  `Manager.CommitPinned`, `Manager.ReleasePinned`, `PinnedLineAdoptRequest`,
  `PinnedLineResumeRequest`, `PinnedLineParkRequest`,
  `PinnedLineReviewRequest`, `Manager.AdoptPinnedLine`,
  `Manager.ResumePinnedLine`, `Manager.ParkPinnedLine`,
  `Manager.SnapshotPinnedLineReview`,
  `Manager.RecoverPinnedLineAdoptReservation`,
  `Manager.RecoverPinnedLineResumeReservation`, `NewGitWorkspaceTool`, API routes under
  `/api/git-workspaces`, and frontend API/page components.
- Runtime ordering: load config, construct the manager, acquire and lock before
  repository work, or have a trusted controller validate and publish a fresh
  exact pinned checkout; heartbeat an owned pin without resetting work; either
  release ordinary pinned state through preservation or adopt it as a private
  line, validate and commit one mutation, advance and park the retained ref
  before releasing the reservation, review the parked exact commit, and resume
  only from its complete version/epoch/tip/tree fence; after an expired
  write-ahead Adopt or Resume, hold both reservation locks while reconciling
  the intended transition and replacing the stale bearer; then reconcile generic
  ignored-file cleanup and aged/oversized checkout drops.
- Non-obvious constraints: locked workspaces are never cleaned or dropped;
  dirty changes must be committed before unlock/drop; ignored-file size must
  include ignored files and directories, not only tracked git state. Pinned
  acquisition accepts only an exact lowercase 40- or 64-hex commit, publishes
  only a freshly staged and verified checkout, uses an opaque controller
  reservation key rather than a model turn session key, and remains a
  controller-only Go API rather than an action of the generic `git_workspace`
  tool. Every pinned checkout is private from acquisition onward, and
  development-line inventory, refs, checkout paths, reservation hashes, and
  history are private controller state; controller-pinned checkouts are never
  generic reuse, cleanup, release, drop, or reconciliation state, and their
  identity, workspace/repository detail, history, counts, and bytes never enter
  generic stats, quota, HTTP, frontend, or tool projections. A later
  controller-owned storage lifecycle must account for all pinned storage,
  including released acquisitions that were never adopted into a line,
  separately. A parked review is object-addressed and reservation-free, while a
  mutation requires one exact fresh line epoch and reservation.
  Ordinary rotation requires the old bearer to own the Git workspace. Composite
  Resume recovery also handles the causally distinct pre-Resume state in which
  eventing issued the old bearer but Git never installed it; the same
  inventory-v3 rotation evidence permanently revokes it.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-GITWS-001` | MUST | Config load with `git_workspaces` omitted or partially configured. | Effective root, max total size, ignored cleanup delay, and drop delay resolve to defaults. | No inventory mutation. | Empty root falls back under the configured workspace directory. | Operators need safe defaults without mandatory setup. |
| `FR-GITWS-002` | MUST | Acquire request with repository, optional ref, and session key. | A checked-out workspace path and lock metadata are returned. | Repository and workspace records plus allocation history are persisted using canonical SSH remote URLs when the input URL can be represented that way. | Missing repository/session returns an error; an already locked checkout for another session causes a separate checkout to be allocated. | Concurrent sessions must not overwrite each other. |
| `FR-GITWS-003` | MUST | Repeated acquire for the same repository and session. | The same locked workspace is returned and heartbeat metadata is updated. | Lock heartbeat and history are persisted. | Dropped workspaces are ignored. | Tool retries should be idempotent for a turn. |
| `FR-GITWS-004` | MUST | Release request for a session with dirty workspace contents. | Workspace unlocks and reports the preserved branch name. | Dirty contents are committed on a `picoclaw/session/...` branch before lock removal. | Preserve failure keeps the error visible and records failure history. | Agent work must survive turn cleanup. |
| `FR-GITWS-005` | MUST | Stats or list request. | Generic totals include active workspace count, locked count, total bytes, ignored bytes, per-repo rollups, per-workspace status, and newest history. Every controller-pinned workspace from first acquisition, its repository when no generic workspace remains, its independently retained history, and all of its IDs, paths, locks, counts, timestamps, and bytes are structurally omitted. | No mutation. | Dropped generic workspaces remain in history/status but are excluded from active totals. Controller-private storage requires a separate future lifecycle and cannot be inferred as generic quota usage. | UI and generic cleanup policies require accurate inventory without declassifying or accidentally governing controller-owned work. |
| `FR-GITWS-006` | MUST | Clean ignored request for an unlocked generic workspace. | Before/after ignored byte counts and refreshed workspace info are returned. | Ignored files are removed and cleanup history is persisted. | Locked, missing, or dropped generic workspaces return errors; every controller-pinned workspace is treated exactly as missing. | Generated caches should be recoverable without deleting work or mutating private controller state. |
| `FR-GITWS-007` | MUST | Drop request for an unlocked generic workspace. | Dropped workspace info is returned and the checkout path is removed. | Dirty changes are preserved first; drop time and history are persisted. | Locked, missing, or dropped generic workspaces return errors; every controller-pinned workspace is treated exactly as missing. | Operators need manual reclamation without losing changes or bypassing controller retention. |
| `FR-GITWS-008` | MUST | Reconcile request or turn-end maintenance. | Eligible generic workspaces are cleaned or dropped and final generic stats are returned. | Ignored files older than the configured cleanup delay are removed; unlocked generic workspaces older than drop delay or exceeding the generic max total size are dropped. | Locked and controller-pinned workspaces are skipped. Controller-pinned workspaces are excluded both from candidates and from generic size/quota calculation; their storage lifecycle is separate. | Generic disk usage must be bounded automatically without destroying or accounting for controller-owned work. |
| `FR-GITWS-009` | MUST | Agent tool call `git_workspace`. | Actions acquire, list/status, release, clean ignored, drop, and reconcile map to generic manager operations and return JSON. | Mutating actions persist through the manager. | Missing manager or invalid action returns a tool error; a guessed controller-pinned workspace is indistinguishable from missing, and no pinned or line action exists. | Agents need a first-class path to allocate reusable checkouts without receiving controller authority. |
| `FR-GITWS-010` | MUST | Launcher API calls and frontend dashboard interactions. | API returns JSON stats/results; UI shows inventory/history/limits without long root paths in the summary metrics, displays normalized SSH remotes for legacy HTTPS rows when safe, exposes SSH remotes through a compact copy marker, labels the checkout branch column as current branch, shows compact checkout paths with a full-path copy action, and exposes refresh, maintain, clean, and drop actions. | Cleanup/drop/reconcile mutate through API helpers only. | API config/load errors return HTTP errors; UI disables clean/drop on locked workspaces. | Users need visibility and manual controls for local checkouts. |
| `FR-GITWS-011` | MUST | A trusted controller calls `Manager.AcquirePinned` with an exact repository, source branch, expected commit, opaque reservation key, and agent identity. Unless the checkout is adopted into a retained line under `FR-GITWS-013`, it eventually calls `Manager.ReleasePinned` with that reservation and agent identity. | The manager returns a locked, detached checkout whose fetched source branch resolves to the exact lowercase 40- or 64-hex expected commit. A first acquisition uses a fresh isolated clone prepared and verified in an unpublished staging directory, atomically publishes it at its inventory-derived path in the controller-only `repoID-pinned[-N]` namespace, and only then records ownership; a matching same-reservation call heartbeats the existing checkout without fetching, checking out, cleaning, or resetting agent work. Generic `ReleaseSession` skips pinned reservations, while explicit ordinary pinned release preserves work and unlocks it. | Inventory durably records repository identity, pinned source ref, pinned commit, reservation/agent lock identity, heartbeat, and independently retained private history. From first acquisition, every pinned checkout, repository-only rollup, path, lock, ID, count, byte total, and history entry is absent from generic stats/list/quota surfaces; generic session lookup/reuse/release and reconciliation skip it, and cleanup/drop of its guessed ID returns the ordinary not-found result even after ordinary pinned release. Ordinary pinned release preserves any clean descendant commit or dirty work on a unique create-only reservation branch before unlocking. All inventory access is serialized by a context-aware kernel advisory lock whose persistent legacy path fences out older directory-lock binaries. | Missing, whitespace-aliased, malformed, or noncanonical input fails before allocation. Repository/ref/commit/reservation/agent mismatches, an origin that is not one exact raw remote or has redirected push configuration, an escaped or symlink-substituted manager, checkout, or Git-internal path, a non-descendant `HEAD`, replacement/graft/sparse/index-hiding state, injected Git configuration, unpreservable dirty state, or another control-plane deviation fails closed without resetting work or adopting a generic reusable checkout. A failed/crashed staging attempt is never inventory-owned or reused. Pinned acquire/release are available only as Go controller APIs: they are not HTTP/frontend actions and are not registered as `git_workspace` tool actions. A retained-line reservation must use its separate park boundary and is rejected by ordinary pinned release. | A development controller must bind local code to provider-verified PR identity while preserving subsequent agent work and withholding checkout and lifecycle authority from untrusted model calls. |
| `FR-GITWS-012` | MUST | A trusted controller snapshots one exact locked pinned checkout, stores the returned parent/tree/candidate digest as validation evidence, then calls `Manager.CommitPinned` with that evidence, an immutable `pdcmt_` intent, a canonical bounded message, and a stored UTC whole-second authored time. | `SnapshotPinnedCandidate` builds an all-worktree candidate from tracked plus nonignored untracked content through a private temporary index and returns only opaque workspace identity, exact parent/tree, a domain-separated raw-diff digest, and bounded changed-file count. `CommitPinned` recomputes the candidate, creates one deterministic one-parent commit object whose message binds a domain-separated digest of the intent and whose identity/time are fixed, verifies its raw object, compare-and-swaps detached `HEAD`, repairs only the real index, and proves the checkout clean. An exact retry after a crash between completed Git subprocesses recognizes the same deterministic object at `HEAD`, repairs an interrupted index update, and returns `already_applied`; a proven commit with later worktree drift returns the commit evidence plus an explicit recovery error. | Candidate snapshots may add unreachable content-addressed Git objects but do not change `HEAD`, the real index, ordinary files, inventory ownership, branches, remotes, or provider state. A successful commit changes only local Git objects, detached `HEAD`, its bounded exclusive local reflog, and the real index. A hash-keyed kernel advisory operation lock serializes repair, snapshot, commit, and pinned release for the reservation across processes; its callback-derived context composes atomic manager calls without re-locking and expires when the callback returns, and the edit-only repair runner holds it for its complete preflight/model/postflight interval. | Missing or stale workspace/pin/parent/tree/digest/intent/time evidence, empty changes, attached or unexpected `HEAD`, a dirty real index before first application, merge/rebase/sequencer state, changed gitlinks, unsafe ref-storage/symlink-ref configuration, nonexclusive appendable reflogs, excessive/invalid Git output, origin/path/control-plane drift, compare-and-swap loss, cancellation, workspace drift, or a stale lock left by termination inside a Git subprocess fails closed. Stale Git locks require explicit operator recovery and are never deleted automatically. Git plumbing uses no shell, hook, signing, editor, pager, prompt, system/global config, replacement object, or lazy fetch. Commit, snapshot, and operation-lock capabilities remain controller-only Go APIs and perform no validation command, push, branch update, merge, release, HTTP action, or agent-tool action. | Every validated repair needs a local, content-addressed commit anchor that reconciles completed-subprocess crash boundaries without turning model completion or ambient Git configuration into publication authority. |
| `FR-GITWS-013` | MUST | A trusted controller calls `Manager.AdoptPinnedLine` for one freshly acquired clean exact pin using a caller-durable opaque line ID, workspace ID, and source tree; after local work it calls `Manager.ParkPinnedLine` with the complete expected version, mutation epoch, previous tip, new tip/tree, and caller-durable intent or an explicit no-change fact. A later mutation calls `Manager.ResumePinnedLine` with a fresh reservation plus the complete parked fence, while a local review calls `Manager.SnapshotPinnedLineReview` with the exact parked version, prior tip, current tip, and tree. | Adopt and resume return only opaque workspace, version, mutation epoch, tip, tree, and replay evidence while retaining or installing the exact mutation reservation. Park runs only after any outer mutation operation has returned, compare-and-swaps a stable private `picoclaw/development/...` branch ref to either one direct-child clean commit or the unchanged tip, proves the exact tree and cleanliness, then returns the next version and exact previous-tip/tip/tree/replay/no-change evidence only after the workspace reservation is cleared. Review requires the line to remain parked and returns its exact version, park epoch/intent, base commit, tip commit, tree, at most 1,000 bounded canonical changed paths, an at-most-512-KiB valid-UTF-8 LF-canonical unified diff, and a domain-separated digest over that complete projection without acquiring a mutation reservation. | String-tagged inventory version 2 adds a private line map, workspace-owner link, and independently retained private history while fencing numeric-version rollback. Fresh pinned controller workspaces use a disjoint `repoID-pinned[-N]` identity namespace, and adoption rejects a legacy pinned checkout from the generic numeric namespace so hidden lines cannot influence visible workspace suffixes. Each line binds one repository, original source ref/commit, deterministic internal branch, exact tip/tree, version, monotonically increasing mutation epoch, parked-or-mutating state, timestamps, a domain-separated current reservation hash while mutating, the complete never-reusable retired reservation-hash history, a write-ahead pending-park tuple, and complete last-park replay evidence without storing the current reservation bearer in the line record. Adopt creates or reconciles the create-only line ref and owner while retaining the initial lock and moves that workspace's prior history into the private retention domain. Resume atomically transitions one parked line to mutating, increments its epoch, and installs the fresh workspace lock without fetching, recloning, cleaning, resetting, or moving `HEAD`. Park durably records its exact intent before advancing and reference-fsyncing the exclusive loose ref without dereferencing it and with reflog creation disabled, then records replay evidence and clears the lock; an explicit no-change park leaves the ref and tip unchanged while still advancing the line version. Retained line workspaces, repository-only rollups, independently capped history, counts, bytes, and activity-derived repository timestamps are omitted from generic stats/list/quota output, and the checkout is excluded from generic reuse, cleanup, release, drop, and reconciliation; direct generic cleanup/drop of a guessed private ID returns the same not-found result as an absent workspace. A later controller-owned storage lifecycle must account for and retire retained storage separately. | Every request requires exact untrimmed bounded identities and matching repository/source/workspace/agent, SHA width, line version, epoch, tip, tree, state, lock, exclusive canonical direct loose ref, absent ref lock/reflog, detached clean checkout, origin, ancestry, and control plane. Stale, partial, cross-line, reused-reservation, changed, attached, dirty, multi-parent, nondirect-child, symbolic/dereferenced/packed-only/ref-layout, symlink, unsafe fsync/diff/output configuration, replacement, oversized-output, or cancellation evidence fails closed without adopting a different workspace, resetting files, or releasing the mutation lock. Exact adopt/resume retries return the same lease; an exact park retry reconciles a ref-ahead/inventory-behind completed Git effect only from its matching write-ahead tuple and returns the same parked result, while changed intent conflicts. Generic `ReleasePinned` rejects a line reservation, every retired line reservation remains unusable, and a pending park also seals the outer operation callback. Review holds inventory serialization through preflight, bounded object reads, and postflight; both Git's fail-closed attribute-source option and its environment are pinned to the exact tip, local `diff.*` and output-changing configuration is rejected, and malformed, control-bearing, noncanonical, excessive, non-UTF-8, bare-CR, or NUL-bearing review output fails without returning a partial snapshot. These APIs remain controller-only Go surfaces and perform no fetch, hook, shell, validation command, model/tool/workflow/HTTP/frontend action, push, merge, provider call, or publication. | Multiple local repair attempts need one retained, crash-reconcilable commit line that releases exclusive edit ownership between attempts and supports immutable local review without letting generic workspace maintenance, live worktree drift, or untrusted callers discard, replace, or control it. |
| `FR-GITWS-014` | MUST | After an exclusive PR-development mutation lease expires, a trusted recovery controller calls `Manager.RotatePinnedReservation` with one caller-durable intent, the exact old and fresh reservation bearers, stable existing agent, workspace, source pin, and either the exact unbound-pin fence or the complete bound mutating-line version/epoch/tip/tree fence. | The manager waits for both reservation operation locks in canonical hash order, then atomically changes only the matching workspace reservation and, when bound, its matching line reservation hash; the logical Git agent remains unchanged and cannot be selected by the recovery worker. It returns the exact opaque workspace/line fence, a domain-separated rotation proof, and replay status. An exact retry recognizes only the latest still-active replacement and performs no write. | String-tagged inventory version 3 retains a bounded append-only, domain-separated hash chain of old-to-fresh rotation records. The old reservation is permanently revoked, the fresh reservation becomes the sole active bearer, and causal rotations may continue `A -> B -> C` without adding entries to the park-only retired-reservation sequence. The complete rotation history participates in inventory validation and global reservation nonreuse. No ref, `HEAD`, index, worktree, object, branch, remote, or filesystem content is changed. | Unbound and bound modes are disjoint. Missing, reused, aliased, partial, cross-workspace, cross-line, noncurrent, pending-park, parked, changed source/version/epoch/tip/tree, noncanonical lock order, malformed chain, duplicate hash, rollback to version 2, or replay after later progress fails closed. The API is controller-only and has no tool, workflow, model, HTTP, UI, CI, provider, commit, park, push, merge, or publication surface. | A crashed worker still knows its old bearer; durable recovery must revoke that bearer before a replacement controller can safely continue the same local checkout without resetting or discarding its work. |
| `FR-GITWS-015` | MUST | A trusted recovery controller has one caller-durable schema-v13 Adopt or Resume operation intent and calls `Manager.RecoverPinnedLineAdoptReservation` or `Manager.RecoverPinnedLineResumeReservation` with its exact old and globally fresh reservation bearers, stable agent, workspace/source/line identity, intended line fence, and rotation identity. | The manager acquires both reservation operation locks in canonical hash order and retains them across the complete transition. For a pre-effect Adopt it first durably records the unbound old-to-fresh rotation, revokes old, installs fresh as workspace owner, and then creates or exactly reconciles the retained line in a second durable save; replay completes that intermediate state. For a post-effect Adopt it first verifies the exact retained line and Git state, then records the bound rotation and replaces old with fresh. Resume accepts either the exact parked pre-Resume fence where old was never installed or the exact already-resumed fence owned by old, and converges both to that mutating fence owned only by fresh. Each method returns the same opaque line fence and inventory-v3 rotation proof on first execution and exact latest replay. | The existing inventory-v3 rotation chain, count, tail anchor, global nonreuse checks, and workspace/line records durably revoke old and install fresh in one controller operation while the canonical locks remain continuously held. Adopt may create or reconcile only its deterministic private source ref; Resume moves no ref. Neither method changes `HEAD`, index, worktree, ordinary content, source pin, tip, tree, line version, remote, or logical agent. No new inventory version or parallel recovery history is introduced. | Missing, reused, aliased, partial, cross-intent/workspace/line, dirty, attached, changed source/version/epoch/tip/tree/ref/control-plane, pending-park, later-progress, noncanonical-lock, or corrupt rotation evidence fails closed. A different old-owner operation cannot enter between transition validation and revocation; after success every use of old conflicts. Exact replay is accepted only for the latest matching fresh owner and record. The methods are controller-only and expose no model, tool, workflow, HTTP/UI, CI, commit, park, provider, push, merge, or publication capability. | A durable operation intent makes Adopt and Resume repeatable, but only one composite Git boundary can close the stale-bearer race between replaying the line transition and revoking the crashed worker's authority. |

Controller storage lifecycle debt is deliberate and bounded at this layer:
ordinary pins remain private after `ReleasePinned`, even when never adopted, so
the planned controller lifecycle must account for, compact, archive, and retire
both non-adopted pinned checkouts and development lines. Generic quota and
maintenance never substitute for that lifecycle.

## Data And State Model

The manager root contains `inventory.json`, the persistent kernel-lock target
`inventory.lock`, and `checkouts/`. The inventory stores repository records,
workspace records, generic refs, optional pinned source refs and commits, lock
metadata, preserved branch names, drop timestamps, and independently bounded
generic and controller-private event histories.
Inventory version 3 uses a canonical string-valued version discriminator. It
retains version 2's private development-line map and one
optional line-owner ID on its retained workspace. A line record binds its
opaque identity to exactly one workspace/repository, original source ref and
commit, deterministic internal `picoclaw/development/...` branch, current
tip/tree, version, mutation epoch, parked-or-mutating state, timestamps, hashed
current mutation reservation, complete retired-reservation history, one exact
pending-park write-ahead tuple, and complete last-park replay evidence. Version
3 additionally stores a bounded append-only reservation-rotation chain per
pinned workspace. Each record binds its caller-durable intent, exact unbound or
bound line fence, old and fresh reservation hashes, prior record hash, and
result hash. The chain retains at most 8,192 records, matching schema-v12
controller recovery capacity. Every pinned workspace separately stores its
exact rotation count and either the domain-separated empty-chain digest or the
exact tail record hash, so deleting the whole map entry or any suffix fails
inventory validation instead of forgetting revoked bearers. Causal chains
revoke each predecessor without changing the park-only retired-reservation
sequence. Schema-v13 composite Adopt/Resume recovery reuses this same bound
record and its count/tail anchors: the record can prove old-to-fresh revocation
whether Resume first installed old or atomically installed fresh from the exact
parked predecessor. It does not add another inventory collection or version.
The active reservation bearer remains
only in the ordinary live workspace lock. Private
line records and every controller-pinned workspace from acquisition, their
repository-only rollups, internal branches, associated history, IDs, paths,
locks, timestamps, counts, and bytes are structurally absent from `Stats`,
generic quota/reconciliation accounting, tools, HTTP, and the frontend. A later
controller-owned storage lifecycle must measure and retire all pinned storage
separately. A quiescent inventory without line state or a live legacy-namespace
pinned checkout upgrades from numeric version 0 or 1; a dropped legacy pinned
tombstone is purged only after its canonical managed path is proven absent and
its history is moved private, while any live legacy pin requires release and
drop with a version-1 binary before upgrade. The public error is deliberately
neutral. A version-2 inventory upgrades without inventing a rotation record;
the string discriminator makes a version-2 decoder reject version 3 before it
can rewrite the new state. A newer version, orphaned/multiply owned workspace, inconsistent
parked/mutating lock, incomplete replay tuple, or malformed line/ref identity
fails closed on load.
The advisory file lock coordinates manager instances and its ownership is
released by the kernel after process exit. Reusing the legacy directory-lock
path as a persistent file creates a quiescent upgrade fence: an older binary
cannot acquire its directory lock after a new binary has created the file, and a
new binary fails closed while an older process still owns the directory.
Generic workspace IDs are deterministic hash-derived values with numeric
suffixes for concurrent locked checkouts. Pinned IDs use the disjoint
`repoID-pinned[-N]` controller namespace. A pinned checkout is
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
| Go API | `(*gitworkspace.Manager).AcquirePinned`, `WithPinnedOperation`, `SnapshotPinnedCandidate`, `CommitPinned`, and `ReleasePinned` | Controller-only exact acquisition binds repository, source branch, expected commit, opaque reservation, and agent identity; the callback-scoped operation lock serializes trusted filesystem work while its derived context safely composes the atomic manager methods; snapshot/commit bind validated ordinary content to one deterministic local descendant; explicit release safely preserves and unlocks only that reservation. | `FR-GITWS-011`, `FR-GITWS-012` |
| Controller Go API | `(*gitworkspace.Manager).AdoptPinnedLine`, `ResumePinnedLine`, `ParkPinnedLine`, and `SnapshotPinnedLineReview` plus the corresponding exact request/result structs | Retain one original exact pin under a private line, version-fence each fresh mutation reservation, atomically advance and park one direct-child commit or explicit no-change tip before releasing that reservation, and return one bounded object-addressed exact-SHA review snapshot while parked. Results expose no checkout path, internal branch, or reservation bearer. | `FR-GITWS-013` |
| Controller Go API | `(*gitworkspace.Manager).RotatePinnedReservation` and its exact request/result structs | Atomically revoke one expired pinned mutation bearer and install one globally fresh bearer against an exact unbound pin or bound mutating-line fence, retaining an idempotent hash-chained proof without changing repository content or refs. | `FR-GITWS-014` |
| Controller Go API | `(*gitworkspace.Manager).RecoverPinnedLineAdoptReservation`, `RecoverPinnedLineResumeReservation`, and their exact request/result structs | Under canonical old-plus-fresh operation locking, reconcile one write-ahead Adopt or Resume and converge its Git inventory authority to the globally fresh bearer while reusing the inventory-v3 rotation proof and changing no ordinary content. | `FR-GITWS-015` |
| Tool | `git_workspace` | Agent-callable generic acquire/list/status/release/clean/drop/reconcile operations with JSON results. It has no pinned, line, rotation, or composite-recovery action, cannot supply pinned or line identity fields, generic release skips ordinary pinned reservations, every pinned workspace/repository/history/ID/path/lock/count/byte projection is absent from acquisition onward, and maintenance treats guessed pinned IDs as missing. | `FR-GITWS-002` through `FR-GITWS-009`, `FR-GITWS-011`, `FR-GITWS-013` through `FR-GITWS-015` |
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
7. To adopt a fresh exact pin as a development line, validate the caller-durable
   line/workspace/tree fence under the reservation operation and inventory
   locks, prove the checkout is detached, clean, operation-free, and exactly at
   the source commit/tree, then create-or-verify the deterministic private ref
   with compare-and-swap. Persist the line owner and initial mutating
   version-zero/epoch-one state while retaining the original workspace lock.
8. Generic session release skips pinned reservations. On explicit pinned release,
   verify the reservation and agent identity before mutation. Before any
   release/drop, inspect git status. For generic dirty state, and for pinned dirty
   state or a clean descendant of the pin, create a unique create-only
   `picoclaw/session/{reservation}/{timestamp}` preservation branch, commit dirty
   contents when present, verify the checkout became clean, then unlock. Never
   overwrite an existing preservation ref, discard a pinned descendant, or
   unlock dirty state that Git cannot stage. Refuse this generic release path
   when the reservation belongs or previously belonged to a development line.
9. Before trusted repair filesystem work, acquire the reservation-derived
   cross-process operation lock, then acquire inventory locks only inside
   manager calls. To snapshot a candidate, require detached `HEAD`, an ordinary
   operation-free checkout, and a real index equal to `HEAD`; seed a private
   index from that parent, add all worktree changes, reject an empty diff or
   changed gitlink, write the candidate tree, and hash the exact raw diff.
10. To commit stored validation evidence, recreate and verify the deterministic
   commit object before compare-and-swapping detached `HEAD`. If `HEAD` already
   names that exact object, reconcile the real index and prove cleanliness. If
   content drifted after the commit became visible, preserve the commit fact and
   fail recovery-required without changing ordinary files.
11. To park a mutating line, validate the complete source/workspace/agent,
    version, mutation epoch, reservation hash, previous tip, new tip/tree, and
    clean detached checkout. Require either the unchanged tip for an explicit
    no-change park or exactly one commit whose sole parent is the previous tip.
    Refuse to park inside a still-live outer mutation operation. Durably store
    the exact pending tuple, compare-and-swap and reference-fsync the stable
    exclusive loose ref without creating a branch reflog, reconcile an
    ambiguous completed ref update under a bounded detached postflight,
    re-prove ref layout/tip/tree/cleanliness,
    then atomically advance the version, store complete caller-intent replay
    evidence, mark the line parked, and clear the workspace lock.
12. To review a parked line, hold manager and inventory serialization without a
    mutation reservation; bind the request to the exact current version, last
    previous tip, tip, and tree; revalidate the retained workspace, ref, origin,
    control plane, detached `HEAD`, and cleanliness; then read only the bounded
    changed-path list and unified diff between those exact Git objects with
    attributes pinned to the exact tip and with external diff, text conversion,
    renames, color, hooks, prompts, ambient configuration, and local diff-driver
    configuration disabled. Re-prove the parked state after both reads,
    canonicalize CRLF to LF, bind the exact line version, park intent/epoch,
    base/tip/tree, paths, and diff in a domain-separated digest, and return
    nothing partial on limit, encoding, path, or state failure.
13. To resume a parked line, take the fresh reservation operation lock and
    require the complete prior version/epoch/tip/tree and original source
    identity. Revalidate the retained unlocked workspace and exact ref without
    fetch, clone, checkout, clean, reset, or ref movement, then atomically
    install the workspace lock, mark the line mutating, and increment its epoch.
    An exact retry recognizes that same new ownership; any different reservation
    or fence conflicts.
14. To rotate an expired pinned reservation, acquire the old and fresh
    reservation operation locks in canonical hash order before inventory
    serialization. Require the exact current workspace/source/agent ownership
    and either an unbound workspace or a bound mutating-line fence with no
    pending park. Prove the fresh hash has never appeared, append its
    caller-intent and predecessor-bound rotation record, atomically replace the
    workspace lock and bound-line hash, and leave every Git and ordinary file
    fact unchanged. An exact latest-record replay is no-write; later progress
    makes the earlier request stale.
15. To recover a write-ahead Adopt or Resume, acquire both reservation operation
    locks in the same canonical order as rotation and keep them until the
    intended line transition, post-transition fence, old-to-fresh ownership
    replacement, rotation record, count/tail anchors, and inventory durability
    are complete. For Adopt, require old to own the exact source pin and create
    or reconcile only its deterministic source-version line before replacement.
    For Resume, accept either the exact parked fence with old not installed or
    its exact mutating successor owned by old, then install fresh directly or
    replace old respectively. In both cases permanently revoke old, return the
    same proof for an exact latest replay, and allow no intervening old-bearer
    operation.
16. For stats, skip every controller-pinned workspace before inspecting its path or
    building repository rollups. Walk only generic checkout paths for total
    bytes and use Git ignored status to find generic ignored roots without
    double-counting nested paths.
17. Reconcile skips locked and controller-pinned workspaces, cleans old generic
    ignored files first, drops aged generic workspaces second, then drops oldest
    unlocked generic workspaces until total active size is within the configured
    limit. Pinned state remains structurally absent from generic stats and quota
    accounting and non-reclaimable by generic maintenance before adoption,
    after ordinary release, and while a line is parked; a later controller
    lifecycle owns its separate storage accounting and reclamation.

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
acquire-plus-operation-lock interface and an exact request, never release,
snapshot, commit, or publication capability; it locks the reservation, acquires
a fresh exact pin or heartbeats the matching pin before model access, then
reacquires it as postflight so this feature revalidates
repository, ancestry, and Git control-plane state while the separate security
contract confines ordinary content edits.

A trusted orchestration layer may separately compose pinned acquisition,
development-line adoption, candidate validation and commit, parking, immutable
review, and later resume. This feature owns only the retained checkout/ref and
exact fencing primitives: it does not make the current case-scoped repair worker
call them, persist attempt-ledger state, run CI, interpret a review, or publish a
commit. When a schema-v13 operation intent authorizes recovery, Event Automation
owns its claim and cross-store ordering; this feature owns only the two
composite Adopt/Resume transitions and their inventory-v3 revocation proof. A
model, workflow, generic tool, launcher route, and browser cannot discover a
line identity/ref/checkout or invoke it. The security contract additionally
constrains the bounded review projection and makes repository paths and
lifecycle authority unrepresentable outside the trusted controller.

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
- A workspace adopted by one development line cannot be adopted by another,
  selected for generic reuse, cleaned, dropped, reconciled, or returned by
  generic stats. A generic pinned release cannot unlock it, and the reservation
  from an exact completed park cannot acquire a replacement fresh checkout.
- Development-line adoption, resume, and park reject an altered line/workspace,
  repository/source pin, reservation/agent, version, epoch, tip/tree, detached
  `HEAD`, worktree/index cleanliness, stable ref, or ref-storage/control-plane
  shape. Exact replays reconcile only the same operation; they never guess the
  winning intent, reset files, or create a second line.
- Composite Adopt/Resume recovery rejects an unstaged or changed caller tuple
  and holds both reservation locks until fresh is the sole owner. A stale worker
  may finish before those locks are acquired, but it cannot race back in after
  the composite boundary validates the intended line state; changed work makes
  recovery fail closed rather than granting either bearer authority.
- Park never represents an arbitrary descendant as one attempt: a changed tip
  must be exactly one one-parent child of the prior line tip. An explicit
  no-change park must keep the exact tip but still durably records the attempt
  boundary and releases the mutation reservation.
- Park is terminal with respect to mutation authority: it rejects a callback-
  inherited operation context, so the old edit scope must return before the
  reservation can be cleared and a later attempt can resume.
- The retained branch is one exclusive loose ref with no reflog. Ref updates
  override ambient durability settings to fsync references, while local fsync
  weakening, a ref lock/symlink/hardlink, a reflog, or packed-only storage fails
  closed before parked inventory is finalized.
- A line review is admitted only while parked and only for its exact stored
  previous-tip/tip/tree/version. Path-count, aggregate path bytes, individual
  path bytes, diff bytes, canonical relative-path shape, valid UTF-8, control
  characters, bare carriage returns, NULs, cancellation, local diff-driver
  configuration, or retained-ref/worktree drift are hard failures with no
  partial review result. CRLF diff lines are returned in canonical LF form and
  Git attributes come only from the exact reviewed tip.
- Generic repository rollup timestamps are derived only from generic workspace
  records, so line activity cannot leak through a repository shared with a
  visible generic checkout.
- Inventory locking observes context cancellation, survives stale lock-file
  presence, and relies on kernel lock ownership so a crashed process cannot
  permanently strand the inventory.
- The persistent `inventory.lock` file fences directory-lock binaries, while
  the string-valued version-3 discriminator makes a version-2 decoder fail
  before it can erase rotation evidence. Upgrades must still be
  quiescent; rollback and mixed-version operation are unsupported.

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
| `FR-GITWS-011` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go), [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go) |
| `FR-GITWS-012` | [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go) |
| `FR-GITWS-013` | [pkg/gitworkspace/development_line_test.go](../../pkg/gitworkspace/development_line_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/development_line_review_test.go](../../pkg/gitworkspace/development_line_review_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-014` | [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-015` | [pkg/gitworkspace/pinned_line_recovery.go](../../pkg/gitworkspace/pinned_line_recovery.go), [pkg/gitworkspace/pinned_line_recovery_test.go](../../pkg/gitworkspace/pinned_line_recovery_test.go), [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/development_line_test.go](../../pkg/gitworkspace/development_line_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go) |

## Implementation Anchors

- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/gitworkspace/pinned_commit.go](../../pkg/gitworkspace/pinned_commit.go)
- [pkg/gitworkspace/development_line.go](../../pkg/gitworkspace/development_line.go)
- [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go)
- [pkg/gitworkspace/inventory_lock_unix.go](../../pkg/gitworkspace/inventory_lock_unix.go)
- [pkg/gitworkspace/inventory_lock_windows.go](../../pkg/gitworkspace/inventory_lock_windows.go)
- [pkg/agent/git_workspace.go](../../pkg/agent/git_workspace.go)
- [pkg/tools/integration/git_workspace.go](../../pkg/tools/integration/git_workspace.go)
- [web/backend/api/git_workspaces.go](../../web/backend/api/git_workspaces.go)
- [web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx](../../web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx)
