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
that checkout as one private development line, preflight the exact bounded
review that a proposed park would expose, park its committed or proven
no-change tip while releasing the mutation reservation but retaining the
private branch, resume it under a fresh reservation, and snapshot the same
object-addressed review without exposing the retained line through generic
workspace surfaces. It can also suspend an exact retained candidate without a
commit or review fence, retire the current reservation while keeping the
private ref and checkout, record whether an ambiguous prepared Commit already
advanced detached `HEAD`, and later resume the same ordinary content under a
fresh reservation. A separately trusted controller can compare-and-swap one
exact parked tip to the retained line's stored source branch under an exact
expected-remote-tip fence, without reacquiring mutation authority or changing
the retained local line.
The development-workspace controller can also list bounded paths and read one
bounded UTF-8 blob from the exact base or candidate object of a parked line.
Those reads retain the same private line fence, expose no checkout path or Git
control plane, and let the launcher's read-only Monaco surface inspect exact
code without receiving filesystem or mutation authority.
When a durable controller intent makes an expired Adopt or Resume recoverable,
two controller-only composite methods retain the old and fresh reservation
operation locks across the line transition and authority replacement. They
reuse inventory version 3's rotation chain so the stale bearer cannot regain
ownership in a replay-to-rotation gap. While mutation authority remains live,
a controller can also lend exact parent and candidate Git trees as bounded,
`.git`-free disposable roots to one local-validation callback without exposing
the retained checkout. This validation projection can explicitly prove either
ordinary changes or exact parent-tree equality without authorizing an empty
commit. The callback receives the canonical normalized repository identity,
never the caller's raw repository spelling.

## Reconstruction Notes

- Similarity target: recreate a durable manager around a root directory with an
  `inventory.json` file and checkout subdirectories.
- Core types/functions: `gitworkspace.Manager`, `Options`, acquire/release/stat
  request/result structs, `PinnedAcquireRequest`, `PinnedReleaseRequest`,
  `PinnedCandidateRequest`, `PinnedCommitRequest`, `Manager.AcquirePinned`,
  `Manager.WithPinnedOperation`, `Manager.SnapshotPinnedCandidate`,
  `Manager.SnapshotPinnedValidationCandidate`, `Manager.CommitPinned`,
  `Manager.ReleasePinned`,
  `PinnedCandidateValidationRequest`,
  `Manager.WithPinnedCandidateValidationRoots`, `PinnedLineAdoptRequest`,
  `PinnedLineResumeRequest`, `PinnedLineParkRequest`,
  `PinnedLineReviewRequest`, `Manager.AdoptPinnedLine`,
  `Manager.ResumePinnedLine`, `Manager.ParkPinnedLine`,
  `Manager.PreviewPinnedLineReview`, `Manager.SnapshotPinnedLineReview`,
  `Manager.RecoverPinnedLineAdoptReservation`,
  `Manager.RecoverPinnedLineResumeReservation`, `PinnedLineSuspendRequest`,
  `PinnedLineSuspendResult`, `PinnedLineCommitSuspensionRequest`,
  `PinnedLineSuspendedResumeRequest`, `PinnedLineSuspendedResumeResult`,
  `Manager.SuspendPinnedLine`, `Manager.SuspendPinnedLineCommitRecovery`,
  `Manager.ResumeSuspendedPinnedLine`, `PinnedLinePushRequest`,
  `PinnedLinePushResult`, `Manager.PushPinnedLine`, `NewGitWorkspaceTool`, API
  routes under `/api/git-workspaces`, and frontend API/page components.
- Runtime ordering: load config, construct the manager, acquire and lock before
  repository work, or have a trusted controller validate and publish a fresh
  exact pinned checkout; heartbeat an owned pin without resetting work; either
  release ordinary pinned state through preservation or adopt it as a private
  line, snapshot and validate either changed or exact no-change evidence,
  deterministically commit only changed evidence, preflight the bounded review,
  advance and park the retained ref, release the reservation while retaining
  the branch, and compare the parked review with its preflight before resuming
  only from its complete version/epoch/tip/tree fence. While that complete line
  remains parked, a separately authorized caller may exact-observe its stored
  remote source ref and compare-and-swap only the expected remote tip to the
  parked tip. After an expired write-ahead Adopt or Resume, hold both
  reservation locks while reconciling the intended transition and replacing
  the stale bearer; then reconcile generic ignored-file cleanup and
  aged/oversized checkout drops. When a recovered
  mutation has no active repair owner, snapshot its exact ordinary candidate,
  durably suspend the retained line while retiring the current reservation,
  and later normalize any exact prepared-Commit child back to the retained
  parent before resuming the unchanged candidate under one globally fresh
  reservation.
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
  separately. A proposed-park preview is object-addressed but requires and
  retains the exact active mutation reservation; a parked review is
  object-addressed and reservation-free while its private branch remains, and
  another mutation requires one exact fresh line epoch and reservation.
  Ordinary rotation requires the old bearer to own the Git workspace. Composite
  Resume recovery also handles the causally distinct pre-Resume state in which
  eventing issued the old bearer but Git never installed it; the same
  inventory-v3 rotation evidence permanently revokes it. Disposable validation
  roots contain only bounded object-addressed ordinary tree content; their
  paths exist only during the controller callback, are omitted from serialized
  evidence, and never disclose or copy the retained `.git` directory. An exact
  no-change declaration materializes separate parent and candidate roots for
  the same proven tree; it is validation evidence only and never relaxes the
  strict no-empty-commit boundary. A suspended line is neither parked nor
  mutating: it retains its private ref, checkout, and ordinary candidate but no
  reservation. Suspension does not create a commit, Park fence, review, CI, or
  readiness fact. Prepared-Commit recovery records whether the deterministic
  child is already `HEAD` without advancing the retained ref; exact resume
  converts that child back into ordinary candidate files over the retained
  parent before granting fresh mutation authority. Exact parked push takes no
  reservation and derives its sole destination from the stored source branch.
  Repository and source-ref request values are equality fences rather than
  target selectors, and the method neither decides whether a candidate is ready
  nor records durable publication state.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-GITWS-001` | MUST | Config load with `git_workspaces` omitted or partially configured. | Effective root, max total size, ignored cleanup delay, and drop delay resolve to defaults. | No inventory mutation. | Empty root falls back under the configured workspace directory. | Operators need safe defaults without mandatory setup. |
| `FR-GITWS-002` | MUST | Acquire request with repository, optional ref, and session key. | A checked-out workspace path and lock metadata are returned. | Repository and workspace records plus allocation history persist one canonical credential-free transport identity, preserving safe HTTP/HTTPS/Git URLs and normalizing SSH/SCP inputs. | Missing repository/session returns an error; credential-bearing URLs are rejected, and an already locked checkout for another session causes a separate checkout to be allocated. | Concurrent sessions must not overwrite each other or persist credentials embedded in a remote URL. |
| `FR-GITWS-003` | MUST | Repeated acquire for the same repository and session. | The same locked workspace is returned and heartbeat metadata is updated. | Lock heartbeat and history are persisted. | Dropped workspaces are ignored. | Tool retries should be idempotent for a turn. |
| `FR-GITWS-004` | MUST | Release request for a session with dirty workspace contents. | Workspace unlocks and reports the preserved branch name. | Dirty contents are committed on a `picoclaw/session/...` branch before lock removal. | Preserve failure keeps the error visible and records failure history. | Agent work must survive turn cleanup. |
| `FR-GITWS-005` | MUST | Stats or list request. | Generic totals include active workspace count, locked count, total bytes, ignored bytes, per-repo rollups, per-workspace status, and newest history. Every controller-pinned workspace from first acquisition, its repository when no generic workspace remains, its independently retained history, and all of its IDs, paths, locks, counts, timestamps, and bytes are structurally omitted. | No mutation. | Dropped generic workspaces remain in history/status but are excluded from active totals. Controller-private storage requires a separate future lifecycle and cannot be inferred as generic quota usage. | UI and generic cleanup policies require accurate inventory without declassifying or accidentally governing controller-owned work. |
| `FR-GITWS-006` | MUST | Clean ignored request for an unlocked generic workspace. | Before/after ignored byte counts and refreshed workspace info are returned. | Ignored files are removed and cleanup history is persisted. | Locked, missing, or dropped generic workspaces return errors; every controller-pinned workspace is treated exactly as missing. | Generated caches should be recoverable without deleting work or mutating private controller state. |
| `FR-GITWS-007` | MUST | Drop request for an unlocked generic workspace. | Dropped workspace info is returned and the checkout path is removed. | Dirty changes are preserved first; drop time and history are persisted. | Locked, missing, or dropped generic workspaces return errors; every controller-pinned workspace is treated exactly as missing. | Operators need manual reclamation without losing changes or bypassing controller retention. |
| `FR-GITWS-008` | MUST | Reconcile request or turn-end maintenance. | Eligible generic workspaces are cleaned or dropped and final generic stats are returned. | Ignored files older than the configured cleanup delay are removed; unlocked generic workspaces older than drop delay or exceeding the generic max total size are dropped. | Locked and controller-pinned workspaces are skipped. Controller-pinned workspaces are excluded both from candidates and from generic size/quota calculation; their storage lifecycle is separate. | Generic disk usage must be bounded automatically without destroying or accounting for controller-owned work. |
| `FR-GITWS-009` | MUST | Agent tool call `git_workspace`. | Actions acquire, list/status, release, clean ignored, drop, and reconcile map to generic manager operations and return JSON; acquire may request the fresh behavior in `FR-GITWS-019`. | Mutating actions persist through the manager. | Missing manager or invalid action returns a tool error; a guessed controller-pinned workspace is indistinguishable from missing, and no pinned or line action exists. | Agents need a first-class path to allocate reusable checkouts without receiving controller authority. |
| `FR-GITWS-010` | MUST | Launcher API calls and frontend dashboard interactions. | API returns JSON stats/results; UI shows inventory/history/limits without long root paths in the summary metrics, displays normalized SSH remotes for legacy HTTPS rows when safe, exposes SSH remotes through a compact copy marker, labels the checkout branch column as current branch, shows compact checkout paths with a full-path copy action, and exposes refresh, maintain, clean, and drop actions. | Cleanup/drop/reconcile mutate through API helpers only. | API config/load errors return HTTP errors; UI disables clean/drop on locked workspaces. | Users need visibility and manual controls for local checkouts. |
| `FR-GITWS-011` | MUST | A trusted controller calls `Manager.AcquirePinned` with an exact repository, source branch, expected commit, opaque reservation key, and agent identity. Unless the checkout is adopted into a retained line under `FR-GITWS-013`, it eventually calls `Manager.ReleasePinned` with that reservation and agent identity. | The manager returns a locked, detached checkout whose fetched source branch resolves to the exact lowercase 40- or 64-hex expected commit. A first acquisition uses a fresh isolated clone prepared and verified in an unpublished staging directory, atomically publishes it at its inventory-derived path in the controller-only `repoID-pinned[-N]` namespace, and only then records ownership; a matching same-reservation call heartbeats the existing checkout without fetching, checking out, cleaning, or resetting agent work. Generic `ReleaseSession` skips pinned reservations, while explicit ordinary pinned release preserves work and unlocks it. | Inventory durably records repository identity, pinned source ref, pinned commit, reservation/agent lock identity, heartbeat, and independently retained private history. From first acquisition, every pinned checkout, repository-only rollup, path, lock, ID, count, byte total, and history entry is absent from generic stats/list/quota surfaces; generic session lookup/reuse/release and reconciliation skip it, and cleanup/drop of its guessed ID returns the ordinary not-found result even after ordinary pinned release. Ordinary pinned release preserves any clean descendant commit or dirty work on a unique create-only reservation branch before unlocking. All inventory access is serialized by a context-aware kernel advisory lock whose persistent legacy path fences out older directory-lock binaries. | Missing, whitespace-aliased, malformed, or noncanonical input fails before allocation. Repository/ref/commit/reservation/agent mismatches, an origin that is not one exact raw remote or has redirected push configuration, an escaped or symlink-substituted manager, checkout, or Git-internal path, a non-descendant `HEAD`, replacement/graft/sparse/index-hiding state, injected Git configuration, unpreservable dirty state, or another control-plane deviation fails closed without resetting work or adopting a generic reusable checkout. A failed/crashed staging attempt is never inventory-owned or reused. Pinned acquire/release are available only as Go controller APIs: they are not HTTP/frontend actions and are not registered as `git_workspace` tool actions. A retained-line reservation must use its separate park boundary and is rejected by ordinary pinned release. | A development controller must bind local code to provider-verified PR identity while preserving subsequent agent work and withholding checkout and lifecycle authority from untrusted model calls. |
| `FR-GITWS-012` | MUST | A trusted controller snapshots one exact locked pinned checkout for validation and, only when ordinary content changed, commits the same parent/tree/digest evidence with an immutable `pdcmt_` intent, canonical bounded message, and stored UTC whole-second authored time. | `SnapshotPinnedValidationCandidate` builds the all-worktree projection through a private temporary index and returns opaque workspace identity, exact parent/tree, domain-separated raw-diff digest, and bounded changed-file count; an exact clean worktree is represented by zero changed files and the parent tree. `SnapshotPinnedCandidate` returns the identical projection for changed content but remains strict against no-change. `CommitPinned` recomputes changed evidence, creates and verifies one deterministic one-parent commit whose fixed identity/time/message bind the intent, compare-and-swaps detached `HEAD`, repairs only the real index, and proves cleanliness. Exact completed-subprocess replay recognizes the same object at `HEAD` and returns `already_applied`; proven commit plus later worktree drift returns the commit evidence with an explicit recovery error. | Candidate snapshots may add unreachable content-addressed Git objects but change no `HEAD`, real index, ordinary file, inventory ownership, branch, remote, or provider state. A successful changed commit alters only local objects, detached `HEAD`, its bounded exclusive reflog, and the real index. A hash-keyed kernel advisory operation lock serializes repair, snapshot, commit, and pinned release across processes and safely composes nested manager calls through its callback context. No-change validation changes no commit or ref. | Missing or stale workspace/pin/parent/tree/digest/intent/time evidence, an empty strict snapshot or commit, attached or unexpected `HEAD`, dirty real index, merge/rebase/sequencer state, changed gitlink, unsafe ref storage, nonexclusive reflog, excessive/invalid output, origin/path/control-plane drift, compare-and-swap loss, cancellation, workspace drift, or a stale Git lock fails closed. Stale locks require explicit operator recovery. Plumbing uses no shell, hook, signing, editor, pager, prompt, ambient config, replacement object, or lazy fetch. These controller-only APIs run no validation command and grant no push, branch publication, merge, release, HTTP, or agent-tool authority. | Every attempt needs immutable local validation evidence, while only changed evidence needs a deterministic commit anchor; a truthful no-change attempt must not invent an empty commit. |
| `FR-GITWS-013` | MUST | A trusted controller adopts one fresh exact pin as a private line; while its mutation reservation is live it may call `Manager.PreviewPinnedLineReview` with the complete proposed Park request, then after local work calls `Manager.ParkPinnedLine` with that unchanged request. It may call `Manager.SnapshotPinnedLineReview` against the complete parked fence, and a later mutation calls `Manager.ResumePinnedLine` with a fresh reservation and that fence. | Adopt/resume return opaque line fences while retaining/installing the exact mutation reservation. Preview validates the proposed direct-child commit or explicit no-change tip and returns the prospective next-version bounded review—exact version, park epoch/intent, base/tip/tree, at most 1,000 canonical paths, at most 512 KiB of valid-UTF-8 LF-canonical unified diff, and a digest—without mutation or reservation release. Park runs only after an outer mutation operation returns, compare-and-swaps and fsyncs the stable private `picoclaw/development/...` ref, proves tip/tree/cleanliness, advances the version, and clears the workspace reservation while retaining the private branch/fence. Parked review is reservation-free and, for the same proposal, equals the preview completely. | String-tagged inventory version 2 privately retains the line/workspace link, source, deterministic branch, exact tip/tree, version/epoch, parked/mutating state, reservation hash/history, pending-Park tuple, replay evidence, and capped history. Preview changes none of them. Adopt creates/reconciles the create-only line ref; Resume installs a fresh lock and increments the epoch without moving files, `HEAD`, or refs. Park write-aheads its intent, advances the exclusive loose no-reflog ref for a changed tip, records replay evidence, retires the reservation, and leaves a no-change ref/tip untouched while still advancing the version. All retained-line identities, storage, and activity remain excluded from generic reuse, stats, quota, maintenance, tools, HTTP, and frontend. | Every request requires exact bounded identities and matching repository/source/workspace/agent, SHA width, line version/epoch/tip/tree/state/lock, exclusive safe ref layout, detached clean checkout, origin, ancestry, and control plane. Preview additionally fails closed before Park on a consumed intent, pending Park, invalid advancement, or bounded-review failure and returns nothing partial without releasing authority. Exact adopt/resume/Park retries reconcile only the same intent; stale, partial, cross-line, reused-reservation, dirty, attached, multi-parent, nondirect-child, unsafe-ref/config/output, oversized, encoding, path, replacement, or cancellation evidence conflicts. Generic release cannot release a line. These controller-only APIs perform no fetch, model/tool/workflow/HTTP/frontend, CI, provider, push, merge, or publication action. | Multiple attempts need one retained crash-reconcilable branch, bounded immutable review before and after Park, and no exclusive edit ownership while review or user discussion is pending. |
| `FR-GITWS-014` | MUST | After an exclusive development-workspace implementation mutation lease expires, a trusted recovery controller calls `Manager.RotatePinnedReservation` with one caller-durable intent, the exact old and fresh reservation bearers, stable existing agent, workspace, source pin, and either the exact unbound-pin fence or the complete bound mutating-line version/epoch/tip/tree fence. | The manager waits for both reservation operation locks in canonical hash order, then atomically changes only the matching workspace reservation and, when bound, its matching line reservation hash; the logical Git agent remains unchanged and cannot be selected by the recovery worker. It returns the exact opaque workspace/line fence, a domain-separated rotation proof, and replay status. An exact retry recognizes only the latest still-active replacement and performs no write. | String-tagged inventory version 3 retains a bounded append-only, domain-separated hash chain of old-to-fresh rotation records. The old reservation is permanently revoked, the fresh reservation becomes the sole active bearer, and causal rotations may continue `A -> B -> C` without adding entries to the park-only retired-reservation sequence. The complete rotation history participates in inventory validation and global reservation nonreuse. No ref, `HEAD`, index, worktree, object, branch, remote, or filesystem content is changed. | Unbound and bound modes are disjoint. Missing, reused, aliased, partial, cross-workspace, cross-line, noncurrent, pending-park, parked, changed source/version/epoch/tip/tree, noncanonical lock order, malformed chain, duplicate hash, rollback to version 2, or replay after later progress fails closed. The API is controller-only and has no tool, workflow, model, HTTP, UI, CI, provider, commit, park, push, merge, or publication surface. | A crashed worker still knows its old bearer; durable recovery must revoke that bearer before a replacement controller can safely continue the same local checkout without resetting or discarding its work. |
| `FR-GITWS-015` | MUST | A trusted recovery controller has one caller-durable Adopt or Resume operation intent and calls `Manager.RecoverPinnedLineAdoptReservation` or `Manager.RecoverPinnedLineResumeReservation` with its exact old and globally fresh reservation bearers, stable agent, workspace/source/line identity, intended line fence, and rotation identity. | The manager acquires both reservation operation locks in canonical hash order and retains them across the complete transition. For a pre-effect Adopt it first durably records the unbound old-to-fresh rotation, revokes old, installs fresh as workspace owner, and then creates or exactly reconciles the retained line in a second durable save; replay completes that intermediate state. For a post-effect Adopt it first verifies the exact retained line and Git state, then records the bound rotation and replaces old with fresh. Resume accepts either the exact parked pre-Resume fence where old was never installed or the exact already-resumed fence owned by old, and converges both to that mutating fence owned only by fresh. Each method returns the same opaque line fence and inventory-v3 rotation proof on first execution and exact latest replay. | The existing inventory-v3 rotation chain, count, tail anchor, global nonreuse checks, and workspace/line records durably revoke old and install fresh in one controller operation while the canonical locks remain continuously held. Adopt may create or reconcile only its deterministic private source ref; Resume moves no ref. Neither method changes `HEAD`, index, worktree, ordinary content, source pin, tip, tree, line version, remote, or logical agent. No new inventory version or parallel recovery history is introduced. | Missing, reused, aliased, partial, cross-intent/workspace/line, dirty, attached, changed source/version/epoch/tip/tree/ref/control-plane, pending-park, later-progress, noncanonical-lock, or corrupt rotation evidence fails closed. A different old-owner operation cannot enter between transition validation and revocation; after success every use of old conflicts. Exact replay is accepted only for the latest matching fresh owner and record. The methods are controller-only and expose no model, tool, workflow, HTTP/UI, CI, commit, park, provider, push, merge, or publication capability. | A durable operation intent makes Adopt and Resume repeatable, but only one composite Git boundary can close the stale-bearer race between replaying the line transition and revoking the crashed worker's authority. |
| `FR-GITWS-016` | MUST | While one exact mutation reservation remains live, a trusted controller calls `Manager.WithPinnedCandidateValidationRoots` with the complete pin, workspace ID, candidate parent/tree/digest evidence, an exact `NoChanges` declaration, and one callback. | Under the reservation operation lock, the manager revalidates inventory identity, detached `HEAD`, real index, parent, recomputed candidate, origin, ancestry, and control plane; requires `NoChanges` to equal candidate-tree/parent-tree equality; materializes the exact object trees into separate private disposable `.git`-free parent and candidate roots, including two separate roots for one no-change tree; and supplies their canonical repository identity and full-SHA-256 `PinnedTreeManifest` evidence to the callback. It descriptor-confined postflights both roots, removes them, then revalidates the same retained candidate/control plane even after callback failure or cancellation before releasing the operation lock. | The callback treats both roots as immutable. Only private temporary roots and unreachable content-addressed objects may change; inventory, reservation, `HEAD`, real index, retained files, ref, branch, remote, provider, workflow, cache, and publication state do not. Root paths exist only during the callback and are omitted from JSON; normalized repository and manifests are evidence, while no reservation bearer is returned. | Missing, malformed, stale, cross-workspace, changed/no-change declaration mismatch, dirty index, attached or operation-in-progress state, control-plane drift, excessive or unsafe tree/path/blob/symlink content, invalid UTF-8, alias/collision/traversal, added/removed/renamed/replaced/hard-linked/swapped/raced/mode/content drift, special file, gitlink, callback write, cancellation, cleanup, or either postflight failure fails closed without successful evidence. Reads are bounded and rooted/no-follow/nonblocking where supported; direct `ls-tree`/`cat-file` plumbing uses no checkout, archive filter, shell, hook, prompt, pager, ambient config, replacement object, or lazy fetch. This controller-only API grants no tool, model, workflow, HTTP/UI, commit, Park, release, provider, push, merge, or publication authority. | Local validation must see immutable exact changed or no-change content without receiving the retained checkout, Git authority, sibling state, or stale evidence. |
| `FR-GITWS-017` | MUST | A trusted controller with one exact live mutating-line reservation and caller-durable suspension intent calls `Manager.SuspendPinnedLine`; when the unfinished effect is one exact prepared Commit it instead calls `Manager.SuspendPinnedLineCommitRecovery` with that complete immutable Commit request. A later controller calls `Manager.ResumeSuspendedPinnedLine` with the complete suspension fence and one globally fresh reservation. | Ordinary suspension revalidates the exact line/ref/workspace and snapshots all tracked and nonignored untracked ordinary content into bounded `CandidateTree`, domain-separated digest, and changed-file count evidence relative to the retained line tip, admitting exact no-change. Commit-recovery suspension deterministically reconstructs and verifies the prepared commit, accepts `HEAD` only at its retained expected parent or that exact direct child, records the child identity, its exact `PreparedTree`, and whether it was already applied, and independently snapshots the current ordinary `CandidateTree` without applying a missing commit, moving the retained ref, or discarding post-effect files. `PreparedTree` is the deterministic prepared child's tree; while the child is unapplied, `CandidateTree` must equal it, and only an applied child may retain later ordinary edits in a differing current `CandidateTree`. Both return one opaque exact suspension fence after clearing mutation ownership. Resume requires the latest matching fence; if the prepared child was applied, it compare-and-swaps detached `HEAD` and repairs only the real index from `PreparedTree` or the retained-parent tree back to that parent while preserving every ordinary candidate file, then re-snapshots exact `CandidateTree` equality before installing the fresh reservation and returning the unchanged line version/mutation-epoch/tip/tree. | String-tagged inventory version 4 adds private `suspended` line state and a bounded append-only suspension-record collection. Each line anchors its exact record count and domain-separated empty-or-tail hash; every record hash-binds mode (`candidate` or `commit_recovery`), intent and request hash, workspace/line/repository/source identity, unchanged line version/epoch/tip/tree, retired reservation hash and agent, `CandidateTree`/digest/count, optional prepared commit plus its distinct `PreparedTree` and applied bit, prior hash, and time. Suspend atomically appends that record, clears the workspace lock and line mutation owner, and leaves the checkout, private no-reflog ref, retained tip/tree/version/epoch, `HEAD`, real index, and ordinary files otherwise unchanged. Resume retains the append-only record, changes only an exactly necessary detached-`HEAD` compare-and-swap and index reset, marks the line mutating and installs the fresh workspace/line owner without changing the already-issued mutation epoch; it changes no ordinary file, retained ref, line version/tip/tree, remote, or provider state. Version-3 migration creates only zero-count empty anchors and no suspension fact, while a version-3 decoder rejects version 4 before rewrite. | A suspended line has no live reservation, pending Park, review snapshot, or generic release/maintenance eligibility and cannot be treated as parked, locally ready, reviewed, or publishable. Missing, malformed, reused, aliased, partial, nonlatest, cross-workspace/line/source/agent, stale version/epoch/tip/tree, changed ref/origin/control plane, unsafe or unexpected `HEAD`/index, merge/rebase/sequencer/Git-lock state, changed gitlink, excessive candidate, corrupt count/hash chain, rollback, later progress, candidate drift, prepared-commit/`PreparedTree` mismatch, or compare-and-swap ambiguity fails closed without clearing or installing authority or rewriting ordinary files. An exact Suspend retry matches only the current tail and re-proves its filesystem form before no-write replay; an exact Resume retry matches only the same latest record and still-current fresh owner. All methods are controller-only, run sanitized bounded Git plumbing without shell, hook, signing, prompt, ambient config, replacement object, lazy fetch, or network, and expose no tool/model/workflow/HTTP/UI/CI/review/provider/push/merge/publication capability. | Idle or recovery-required work must release exclusive edit ownership without losing a branch, applied prepared commit, or partial ordinary content, and must resume exactly without fabricating a WIP commit, Park/review fence, green gate, or publication authority. |
| `FR-GITWS-018` | MUST | A trusted controller calls `Manager.PushPinnedLine` with the exact stored repository, source ref and source commit; workspace and line identities; complete parked version, mutation epoch, Park intent, base, tip, and tree fence; and one exact expected remote tip. | The manager acquires its process mutex and then the kernel inventory lock and, once acquired, holds both across inventory admission, remote interaction, and postflight. It requires the retained line to be parked and reservation-free, revalidates its clean detached checkout, private ref, origin, ancestry, and Git control plane, proves source commit <= expected remote tip <= parked tip, derives only `refs/heads/<stored source ref>`, and addresses the literal stored repository rather than `origin`. An exact preflight observation returning the parked tip yields `already_current`; the expected tip permits one exact-tip, one-ref compare-and-swap push using `--force-with-lease=<destination>:<expected>`; every other observed tip conflicts. Once push may have started, bounded cancellation-detached remote readback plus local postflight returns a sanitized result containing the exact non-path line fence, derived remote ref, expected and observed remote tips, `applied`, `already_current`, or `reconciled` disposition, and local-cleanliness fact. | The client requests at most one exact remote-ref transition from the expected tip to the parked tip; behavior of the trusted remote endpoint and its server-side hooks is outside this client primitive. No inventory, schema, history, local ref, `HEAD`, index, worktree, line version or epoch, reservation, Park, review, or readiness state changes; the retained line remains parked and reservation-free. | Malformed or stale identity and local-state evidence fails with `ErrPinnedLineInvalid` or `ErrPinnedLineConflict`. A non-cancellation pre-effect remote-observation failure returns fixed `ErrPinnedLinePushRemoteUnavailable`; caller cancellation or deadline expiry returns its context error. After push may have started, inability to prove the desired remote tip returns `ErrPinnedLinePushOutcomeUnknown` and MUST NOT be retried automatically because an expected-to-tip-to-expected remote ABA is indistinguishable from no effect. A proven remote result plus failed local postflight returns that result joined with `ErrPinnedLinePushWorkspaceDrift`. Missing or changed target, nonancestor expected tip, unsafe Git configuration, output overflow, cancellation, alternate repository/ref, tag, deletion, multiple-ref, caller- or repository-supplied client-hook, signing, push-option, submodule, or force-includes behavior fails closed. This controller-only API has no model, generic tool, workflow, HTTP/UI, readiness-policy, provider-refresh, review-acknowledgement, or merge surface. | A separately authorized publisher needs one narrowly fenced, crash-inspectable remote branch update without turning a parked checkout, mutable Git configuration, stale provider observation, or convenient local-ready signal into arbitrary Git or provider authority. |
| `FR-GITWS-019` | MUST | A generic acquire supplies `fresh: true` with one repository, optional validated ref, and session key. | The manager clones a new checkout or reuses only an unlocked clean fresh snapshot after fetching/pruning the exact origin and resolving the currently available requested ref. A local source repository may project its single safe network origin as `upstream_url` and a fixed `picoclaw-upstream` remote for later immutable reads. | Inventory records the fresh-snapshot marker and safe upstream identity. Clean release keeps the snapshot reusable; changed release preserves work and removes it from the fresh pool. | A dirty cached snapshot is preserved and skipped. A missing/deleted ref, option-like or changed same-session ref, credential-bearing repository URL, changed origin, unsafe control state, or refresh failure fails without silently using stale cached `HEAD`. | Repository review needs a current immutable source snapshot without converting the generic agent tool into pinned-controller authority. |
| `FR-GITWS-020` | MUST | The trusted development controller lists a tree or reads one blob using the complete parked-line version/base/tip/tree fence, one `base`, `candidate`, or exact matching object revision, and a canonical relative path. | `ListPinnedLineTree` returns at most 500 sorted safe paths plus a continuation cursor. `ReadPinnedLineBlob` returns at most 1 MiB of valid UTF-8 regular-file content for the exact object. Both revalidate the retained workspace, private ref, detached checkout, origin, cleanliness, and control plane before and after reading. | Reads change no inventory, ref, checkout, index, ordinary file, reservation, publication, or provider state and expose no checkout path, internal ref, bearer, or Git metadata. | Stale or partial fences, another revision, traversal, absolute or control-bearing paths, symlinks, submodules, non-files, binary/NUL content, oversized output, cancellation, or pre/postflight drift fails without a partial result. The generic `git_workspace` tool and generic Git-workspace launcher API cannot invoke these methods. | A launcher may inspect one exact development candidate without granting browser code execution, filesystem traversal, mutation, or private-line discovery. |

Event Automation's durable repair-controller composition extends these requirements only
through the following Git Workspaces primitives; it grants this feature no CI,
ledger, model, provider, or publication authority:

- Within `FR-GITWS-012`, `SnapshotPinnedValidationCandidate` MUST return the
  same immutable parent/tree/digest projection as `SnapshotPinnedCandidate` for
  changed content, but MUST also admit an exact clean worktree as zero changed
  files whose candidate tree equals the parent tree. The digest still binds the
  exact parent and tree. `SnapshotPinnedCandidate` and `CommitPinned` remain
  strict: neither admits nor creates an empty commit.
- Within `FR-GITWS-013`, `PreviewPinnedLineReview` MUST validate one complete
  proposed Park fence while its exact mutation reservation remains live, read
  the same bounded canonical paths and diff used by parked review, revalidate
  the proposal after those reads, and return the prospective next-version
  review projection. It MUST NOT update the ref, inventory, `HEAD`, index,
  worktree, pending-Park state, or reservation. For the same successful Park
  request and exact post-Park snapshot, the complete preview and parked review
  projections MUST be equal, including their digest. Park alone retires the
  mutation reservation while the private branch and parked fence remain; an
  explicit no-change preview has no paths and an empty diff.
- Within `FR-GITWS-016`, `PinnedCandidateValidationRequest.NoChanges` MUST
  equal the recomputed parent-tree/candidate-tree equality. Both a false claim
  over clean content and a true claim over changed content fail before the
  callback. A true exact claim MUST still materialize separate disposable
  parent and candidate roots for that same tree and keep the reservation
  operation lock through callback, root postflight, cleanup, and retained-state
  postflight.
- Within `FR-GITWS-014` and `FR-GITWS-015`, a recovery controller that will
  next suspend the recovered line MUST set the controller-private
  `RequireSuspensionCapacity` request flag. A new guarded rotation requires an
  available suspension-record slot on the target line and enough rotation
  history for both that recovery and one later `ResumeSuspendedPinnedLine`;
  exact replay counts the already-recorded recovery only once and therefore
  requires just the later resume slot. A guarded direct rotation MUST be bound;
  pre-effect Adopt may name its not-yet-created target because that deterministic
  line starts with an empty suspension history. Capacity rejection writes
  nothing. The flag is not rotation evidence, changes no record hash, and its
  false default preserves existing rotation and recovery behavior.
- Within `FR-GITWS-017`, a controller may call `SuspendPinnedLine` only after
  any required Adopt/Resume old-to-fresh recovery has durably converged, or call
  `SuspendPinnedLineCommitRecovery` only after the prepared Commit's old bearer
  has been durably rotated to the exact live replacement. Each preceding
  recovery primitive keeps old and fresh locks continuously through its own
  convergence and revocation; suspension separately holds the still-current
  replacement lock through candidate capture and durable retirement. A crash
  between those primitives is resolved by exact recovery replay followed by
  exact suspension replay, never by an unfenced reset or guessed WIP commit.

`FR-GITWS-018` is invoked only by the development-workspace branch
publisher after implementation validation and its publication gate. The development
aggregate persists the exact parked-line version, epoch, Park intent, base,
tip, and tree before `PushPinnedLine`; the Git primitive performs one
remote compare-and-swap and returns evidence without granting merge authority.
An ambiguous transport outcome remains a development-workspace `unknown` publication
until exact remote-head reconciliation.

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
Inventory version 4 uses a canonical string-valued version discriminator. It
retains version 3's private development-line map, reservation-rotation chain,
and one
optional line-owner ID on its retained workspace. A line record binds its
opaque identity to exactly one workspace/repository, original source ref and
commit, deterministic internal `picoclaw/development/...` branch, current
tip/tree, version, mutation epoch, parked/mutating/suspended state, timestamps, hashed
current mutation reservation, complete retired-reservation history, one exact
pending-park write-ahead tuple, and complete last-park replay evidence. Version
3 introduced a bounded append-only reservation-rotation chain per
pinned workspace. Each record binds its caller-durable intent, exact unbound or
bound line fence, old and fresh reservation hashes, prior record hash, and
result hash. The chain retains at most 8,192 records, matching the bounded
caller-durable recovery capacity. Every pinned workspace separately stores its
exact rotation count and either the domain-separated empty-chain digest or the
exact tail record hash, so deleting the whole map entry or any suffix fails
inventory validation instead of forgetting revoked bearers. Causal chains
revoke each predecessor without changing the park-only retired-reservation
sequence. Composite Adopt/Resume recovery reuses this same bound
record and its count/tail anchors: the record can prove old-to-fresh revocation
whether Resume first installed old or atomically installed fresh from the exact
parked predecessor. It does not add another inventory collection or version.
Version 4 additionally stores at most 8,192 append-only suspension records per
line. The line's independent exact count plus domain-separated empty-or-tail
hash makes deletion, truncation, reordering, or replacement invalid even when
the optional record collection is absent or altered. Each record binds its
candidate or commit-recovery mode; caller intent and canonical request hash;
workspace, line, repository, source, agent, version, mutation epoch, retained
ref tip/tree; newly retired reservation hash; final ordinary candidate
tree/digest/changed-file count; optional deterministic prepared-commit identity,
its distinct prepared tree, and applied bit; prior record hash; and suspension
time. The prepared tree is exactly the tree named by the deterministic child.
The candidate tree is independently captured from current ordinary content over
the retained parent. It must equal the prepared tree while the child remains
unapplied, but may differ when ordinary edits followed an applied child. The latest record is
the complete fence for `suspended` state. That state requires an unlocked
workspace, no mutation owner or pending Park, the unchanged private ref at the
recorded retained tip, and exact candidate evidence; a prepared commit may be
detached `HEAD` while the private ref and line tip remain at its parent. Every
suspension-retired hash participates in the same store-wide reservation
nonreuse validation as active, Park-retired, and rotation bearers.
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
the string discriminator makes an older decoder reject newer inventory before
it can rewrite private evidence. A quiescent version-3 inventory upgrades to
version 4 by installing only zero-count domain-separated empty suspension
anchors; it invents no suspended state or record. A newer version,
orphaned/multiply owned workspace, inconsistent parked/mutating/suspended lock,
incomplete replay tuple, corrupt suspension anchor/chain, or malformed line/ref identity
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
the agent tool is registered. Candidate-validation manifests and disposable
root paths add no inventory field or configuration: manifests are returned
only to the synchronous controller callback, paths are omitted from JSON, and
the complete private temporary tree is removed before the API returns. The
canonical normalized repository is safe JSON evidence; root paths and
reservation bearers are not.

Pinned-line push adds no inventory field, record, history entry, version, or
configuration. Its request and result are transient controller values, and an
observed or updated remote tip does not rewrite the retained source commit or
mark the local line as durably published. The development-workspace publication
record belongs to Event Automation rather than this inventory and can invoke
this primitive only with its exact private line fence and authorization.

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
| Go API | `(*gitworkspace.Manager).AcquirePinned`, `WithPinnedOperation`, `SnapshotPinnedCandidate`, `SnapshotPinnedValidationCandidate`, `CommitPinned`, and `ReleasePinned` | Controller-only exact acquisition binds repository, source branch, expected commit, opaque reservation, and agent identity; the callback-scoped operation lock serializes trusted filesystem work while its derived context safely composes the atomic manager methods; strict snapshot/commit bind changed ordinary content to one deterministic local descendant, while the validation snapshot can prove exact no-change evidence without authorizing an empty commit; explicit release safely preserves and unlocks only that reservation. | `FR-GITWS-011`, `FR-GITWS-012` |
| Controller Go API | `(*gitworkspace.Manager).AdoptPinnedLine`, `ResumePinnedLine`, `PreviewPinnedLineReview`, `ParkPinnedLine`, and `SnapshotPinnedLineReview` plus the corresponding exact request/result structs | Retain one original exact pin under a private line, version-fence each fresh mutation reservation, preflight the exact bounded review projection without mutating or releasing it, atomically advance and park one direct-child commit or explicit no-change tip while retaining the branch and releasing that reservation, and return the equal object-addressed exact-SHA review snapshot while parked. Results expose no checkout path, internal branch, or reservation bearer. | `FR-GITWS-013` |
| Controller Go API | `(*gitworkspace.Manager).RotatePinnedReservation` and its exact request/result structs | Atomically revoke one expired pinned mutation bearer and install one globally fresh bearer against an exact unbound pin or bound mutating-line fence, retaining an idempotent hash-chained proof without changing repository content or refs. | `FR-GITWS-014` |
| Controller Go API | `(*gitworkspace.Manager).RecoverPinnedLineAdoptReservation`, `RecoverPinnedLineResumeReservation`, and their exact request/result structs | Under canonical old-plus-fresh operation locking, reconcile one write-ahead Adopt or Resume and converge its Git inventory authority to the globally fresh bearer while reusing the inventory-v3 rotation proof and changing no ordinary content. | `FR-GITWS-015` |
| Controller Go API | `(*gitworkspace.Manager).WithPinnedCandidateValidationRoots`, `PinnedCandidateValidationRequest`, `PinnedCandidateValidationRoots`, and `PinnedTreeManifest` | Under one live mutation reservation, revalidate the exact changed/no-change declaration and candidate; lend its canonical normalized repository, separate bounded private `.git`-free parent/candidate roots, and full-SHA-256 manifests to one read-only callback; exact-postflight the disposable roots before removing them; then detached-postflight the retained candidate/control plane. | `FR-GITWS-016` |
| Controller Go API | `(*gitworkspace.Manager).SuspendPinnedLine`, `SuspendPinnedLineCommitRecovery`, and `ResumeSuspendedPinnedLine` plus `PinnedLineSuspendRequest`/`Result`, `PinnedLineCommitSuspensionRequest`, and `PinnedLineSuspendedResumeRequest`/`Result` | Snapshot one exact ordinary candidate and retire its live reservation into private hash-chained `suspended` state; distinguish an unapplied prepared Commit from its exact applied deterministic child without moving the retained ref; and later normalize that child back to the retained parent while preserving candidate files before installing one globally fresh mutation reservation. | `FR-GITWS-017` |
| Controller Go API | `(*gitworkspace.Manager).PushPinnedLine`, `PinnedLinePushRequest`, and `PinnedLinePushResult` | Under manager-plus-kernel inventory serialization and no mutation reservation, revalidate one complete parked line, derive only its stored repository source branch, exact-observe its expected remote tip, compare-and-swap that one ref to the parked tip, and postflight both remote and retained local state. The result exposes no checkout path, internal ref, bearer, credential, arbitrary target, or raw remote output. | `FR-GITWS-018` |
| Controller Go API | `(*gitworkspace.Manager).ListPinnedLineTree`, `ReadPinnedLineBlob`, and their exact browse request/results | Read a bounded safe path inventory or one UTF-8 regular blob from the exact base/candidate of a completely fenced parked line. The development-workspace HTTP layer may project only these results; it receives no checkout path or mutation bearer. | `FR-GITWS-020` |
| Tool | `git_workspace` | Agent-callable generic acquire/list/status/release/clean/drop/reconcile operations with JSON results, including optional fresh acquire. It has no pinned, line, rotation, composite-recovery, suspension, suspended-resume, or pinned-push action, cannot supply pinned or line identity fields, generic release skips ordinary pinned reservations, every pinned workspace/repository/history/ID/path/lock/count/byte projection is absent from acquisition onward, and maintenance treats guessed pinned IDs as missing. | `FR-GITWS-002` through `FR-GITWS-009`, `FR-GITWS-011`, `FR-GITWS-013` through `FR-GITWS-019` |
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
   index from that parent, add all worktree changes, reject a changed gitlink,
   write the candidate tree, and hash the exact raw diff. The strict snapshot
   rejects an empty diff; the validation snapshot instead returns zero changed
   files, the exact parent tree, and its still-parent-bound digest so local
   validation can attest a no-change attempt without creating a commit.
10. To commit stored validation evidence, recreate and verify the deterministic
   commit object before compare-and-swapping detached `HEAD`. If `HEAD` already
   names that exact object, reconcile the real index and prove cleanliness. If
   content drifted after the commit became visible, preserve the commit fact and
   fail recovery-required without changing ordinary files.
11. Before parking, a trusted controller may preview the same complete Park
    request under its live reservation operation lock. Revalidate the mutating
    line, source/workspace/agent, version, epoch, reservation, previous tip,
    proposed direct-child-or-no-change tip/tree, retained ref, clean detached
    checkout, and consumable intent before and after reading the bounded paths
    and canonical diff. Construct the prospective next-version review metadata
    without changing any Git or inventory fact and without releasing the
    reservation. To park, repeat the exact fence checks, refuse a still-live
    outer mutation operation, durably store the pending tuple, compare-and-swap
    and reference-fsync the stable exclusive loose ref without a branch reflog,
    reconcile an ambiguous completed ref update under a bounded detached
    postflight, and re-prove ref layout/tip/tree/cleanliness. Then atomically
    advance the version, store complete caller-intent replay evidence, mark the
    line parked, and clear only the workspace mutation lock; retain the private
    line and branch. Event Automation's durable repair controller snapshots that parked fence and
    requires complete equality with the preview before accepting its own
    terminal transaction.
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
    nothing partial on limit, encoding, path, or state failure. The shared
    object-reader and projection constructor make an unchanged proposed Park
    preview and this exact post-Park snapshot structurally equal.
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
16. To lend a candidate for local validation, hold its reservation operation
    lock; re-prove exact pin/workspace/control-plane, detached parent, clean real
    index, and recomputed tree/digest; require the caller's no-change declaration
    to exactly match candidate-tree/parent-tree equality; enumerate only exact
    tree objects through bounded `ls-tree`; reject unsafe paths, case collisions,
    unsupported modes, gitlinks, and unsafe symlinks; stream each blob through
    bounded `cat-file` into separate fd-confined private parent/candidate roots
    while hashing full content, including two roots for the same exact
    no-change tree; create validated symlinks last; snapshot every materialized
    path; invoke one read-only callback with the canonical repository;
    detached-postflight both roots against their manifests, identities, modes,
    change metadata, and link state; remove all roots; then re-prove the same
    retained candidate under a second detached bounded postflight before
    releasing the operation lock.
17. To suspend an ordinary mutating line, hold its exact reservation operation
    lock through inventory serialization and durable retirement. Re-prove the
    current workspace/line/source/agent/version/epoch/tip/tree owner, unchanged
    private ref, detached parent `HEAD`, real index at that parent, no pending
    Git or Park operation, and the complete safe control plane. Build an
    all-worktree private-index candidate relative to the retained tip, admitting
    exact no-change; append the next per-line hash-chained candidate-mode record;
    atomically mark the line suspended and clear both workspace and line
    mutation ownership; then postflight the same ref, `HEAD`, index, and
    candidate before returning its opaque fence. For prepared-Commit suspension,
    additionally recreate and verify the deterministic direct child from the
    complete immutable Commit request. Accept only detached `HEAD` at the
    retained expected parent or that exact child, plus only the corresponding
    safe index transition; record the child, its exact prepared tree, and
    applied bit, and independently snapshot the final ordinary candidate
    relative to the retained parent. An unapplied child requires exact
    candidate/prepared-tree equality; an applied child retains later ordinary
    edits even when the current candidate differs. Do not apply a
    missing child, move the private ref, normalize an applied child, rewrite an
    ordinary file, or construct a review/CI/readiness fence. Exact current-tail
    replay is no-write only after re-proving the recorded filesystem form.
18. To resume a suspended line, acquire the globally fresh reservation
    operation lock and revalidate the exact latest suspension record, its
    count/tail anchors, global bearer nonuse, private ref, repository/control
    plane, and recorded candidate. For an unapplied or ordinary suspension,
    require detached `HEAD` and the real index at the retained parent. For an
    applied prepared Commit, accept either its exact child state or the
    crash-reconcilable exact parent state; compare-and-swap detached `HEAD` from
    child to parent when necessary and reset only the real index to the parent,
    never the worktree. Rebuild the ordinary candidate over that parent and
    require exact tree/digest/count equality before atomically installing the
    fresh workspace/line owner and marking the line mutating. Preserve the
    already-issued mutation epoch, private ref, line version/tip/tree, suspension
    history, and ordinary files. Only an exact latest fresh-owner replay is
    no-write; any intermediate ambiguity outside the two proven states fails
    closed.
19. To push a parked line, validate every bounded request field before network
    access, then acquire the manager mutex followed by the kernel inventory lock
    and, once acquired, hold both across admission, remote interaction, and
    postflight. Require the exact stored
    repository/source/workspace/line and complete parked version/epoch/Park
    intent/base/tip/tree fence; an unlocked workspace; the exclusive private ref
    at that tip; clean detached `HEAD` and index; and the existing safe origin,
    ancestry, and control plane. Prove the expected remote tip is a local commit
    on the inclusive source-to-parked-tip ancestry chain. Derive only
    `refs/heads/<stored source ref>` and invoke bounded sanitized Git against the
    literal stored repository, never a configured remote name. First read that
    one exact ref: the parked tip is an `already_current` no-effect result, the
    expected tip permits one exact-OID refspec guarded by its explicit
    `--force-with-lease`, and any other value conflicts before push. Disable
    local client hooks, signing, tag following, submodule recursion, arbitrary push options,
    force-includes, prompts, and every caller- or repository-supplied transport
    command. The fixed trusted SSH command and ambient controller/operator
    transport remain inside the existing controller threat model.
    Once push may have started, use a bounded cancellation-detached context to
    reread the remote ref and postflight the complete local parked fence. Return
    `applied` after ordinary confirmed success or `reconciled` when readback
    proves the exact tip after a command error. If readback cannot prove that
    tip, return outcome-unknown and never automatically retry; a remote
    expected-to-tip-to-expected ABA cannot be distinguished safely. If the
    remote result is proven but local postflight drifted, return the result
    together with the drift error rather than pretending the remote effect did
    not occur. Write no inventory or local Git state and leave the line parked.
20. To browse retained code, bind the request to the complete parked line and
    accept only `base`, `candidate`, or the corresponding exact object ID.
    Under manager and inventory serialization, re-prove the retained workspace,
    private ref, detached clean checkout, origin, and Git control plane. For a
    tree read, use bounded NUL-delimited Git plumbing, validate and sort every
    path, return at most 500 entries, and bind continuation to the last returned
    path. For a blob read, require a regular non-symlink/non-gitlink entry, read
    at most 1 MiB, and reject invalid UTF-8 or NUL. Re-run the complete parked
    postflight before returning and mutate nothing.
21. For stats, skip every controller-pinned workspace before inspecting its path or
    building repository rollups. Walk only generic checkout paths for total
    bytes and use Git ignored status to find generic ignored roots without
    double-counting nested paths.
22. Reconcile skips locked and controller-pinned workspaces, cleans old generic
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

The Event Automation durable-repair feature delta is composition only. Its trusted
repair controller uses the validation snapshot for changed or exact no-change
evidence, lends the exact disposable roots to local CI, deterministically
commits only changed evidence, previews the complete bounded Park review, Parks
and thereby releases mutation ownership while retaining the private branch,
then requires the exact reservation-free parked snapshot to equal that preview
before its atomic ledger/finalization boundary. A non-green but valid CI receipt
does not alter these Git primitives: changed evidence still receives its
deterministic local commit, and both changed and no-change attempts can Park.
The next edit must Resume under a fresh reservation; reservation-free review
is now consumed by Event Automation's generation-owned review worker. It
requests `SnapshotPinnedLineReview` by exact line, version, base, tip, and tree,
then cross-validates the returned mutation epoch, Park intent, digest, paths,
diff, and no-change relation against the durable fence; it receives no checkout
path, branch ref, reservation, or Git lifecycle authority.

Git Workspaces owns only the retained checkout/ref, candidate and diff
projection, disposable validation roots, commit/Park fences, and exact
one-ref remote compare-and-swap primitive. The development workspace owns
aggregate state, model and validation orchestration, authorization gates,
operation/publication evidence, recovery ordering, provider refresh, and
unknown-outcome reconciliation. Models, generic workflows/tools, launcher
routes, and browsers cannot discover or invoke line, ref, checkout, or
reservation authority.

`PushPinnedLine` is a narrow branch-publication effect, not a merge or
publication controller. Retained-line suspension and reservation rotation
remain controller-only recovery primitives whose caller must persist intent
and exact results; they create no legacy PR lifecycle, route, or storage
contract.

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
- Suspension rejects a parked or unbound line, a pending Park, an expired or
  mismatched current bearer, a generic workspace, and every `HEAD` other than
  the retained parent or the exact deterministic prepared-Commit child allowed
  by the selected method. Failure preserves the existing reservation and
  ordinary files and appends no partial record.
- A suspended line remains retained and private but cannot be reviewed as
  parked, heartbeated as mutating, released generically, cleaned, dropped,
  reconciled, or counted through generic inventory. Missing or altered
  suspension records/count/tail anchors, candidate drift, or an externally
  moved private ref/`HEAD` fails before a resume can install fresh authority.
- Suspended resume may move only an exact applied prepared child back to its
  recorded retained parent and reset only the real index. A crash may leave
  either of those two exact detached states for replay; an unrelated commit,
  staged-only content, compare-and-swap loss, candidate mismatch, or ordinary
  file rewrite fails closed instead of resetting, cleaning, or guessing.
- Pinned-line push observes only the exact stored source branch. A missing,
  malformed, or different preflight tip, unavailable pre-effect transport, or
  failed ancestry/control-plane fence causes no client-requested remote ref
  update; alternate refs,
  tags, deletions, and multi-ref updates are never attempted.
- Once a pinned-line push may have started, only detached readback of the exact
  parked tip proves the remote effect. Any other or unreadable result is
  outcome-unknown and is never blindly retried because remote
  expected-to-tip-to-expected ABA cannot be distinguished from no effect.
- A proven remote result survives a later local postflight failure as a
  sanitized receipt joined with workspace drift; it is not rolled back or
  reported as effect-free. Results and fixed sentinel errors expose no raw
  remote output, credential, checkout path, internal ref, or reservation.
- The push request cannot supply credentials, helpers, refspecs, push options,
  or transport commands. The controller's fixed trusted SSH command and ambient
  operator transport and the remote endpoint, including server-side hook
  behavior, remain trusted under the existing threat model; this primitive adds
  no credential broker or broader transport isolation claim.
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
- A proposed-Park preview applies the same advancement, review-size, encoding,
  path, ref-layout, workspace, and reservation fences before Park. Failure
  returns no partial projection, creates no pending Park, advances no ref or
  version, and leaves the exact mutation reservation live. A no-change preview
  is still digest-bound even though its changed paths and diff are empty.
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
- `SnapshotPinnedValidationCandidate` admits a clean candidate only as explicit
  zero-change evidence; the ordinary candidate snapshot and `CommitPinned`
  continue to reject it. Candidate validation roots reject either direction of
  a changed/no-change declaration mismatch before lending a path. In the exact
  no-change case the two root paths remain distinct even though their manifests
  describe the same tree.
- Candidate validation rejects a leaf count, total path bytes, individual path,
  tree listing, blob, symlink target, or aggregate tree content beyond its
  fixed bound. It accepts only regular nonexecutable/executable blobs and safe
  relative symlinks, creates symlinks after regular content, and rejects
  gitlinks, special modes, traversal, `.git` aliases, portable device aliases,
  case-fold collisions, escaping/recursive/chained symlinks, and control-bearing
  or non-UTF-8 names before invoking the callback.
- After callback entry and before cleanup, candidate validation always performs
  a detached, bounded, descriptor-confined postflight over both disposable
  roots. Batched no-follow enumeration and bounded nonblocking regular/symlink
  reads must reproduce each entry manifest and its callback-entry path,
  identity, type, mode, change metadata when available, and single-link state.
  Added, removed, renamed, replaced, hard-linked, special, swapped, raced,
  mode-drifted, or content-drifted input joins `ErrPinnedCommitConflict` and
  cannot become successful evidence.
- Callback failure, panic, or cancellation still removes the disposable roots
  and releases the operation lock. Disposable-root postflight runs before
  cleanup; retained candidate/control-plane postflight still runs afterward.
  Cleanup or either postflight failure is joined into the returned error and
  can never become successful local-validation evidence.
- Generic repository rollup timestamps are derived only from generic workspace
  records, so line activity cannot leak through a repository shared with a
  visible generic checkout.
- Inventory locking observes context cancellation, survives stale lock-file
  presence, and relies on kernel lock ownership so a crashed process cannot
  permanently strand the inventory.
- The persistent `inventory.lock` file fences directory-lock binaries, while
  the string-valued version-4 discriminator makes every older decoder fail
  before it can erase rotation or suspension evidence. Upgrades must still be
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
| `FR-GITWS-014` | [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/pinned_recovery_suspension_capacity_test.go](../../pkg/gitworkspace/pinned_recovery_suspension_capacity_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-015` | [pkg/gitworkspace/pinned_line_recovery.go](../../pkg/gitworkspace/pinned_line_recovery.go), [pkg/gitworkspace/pinned_line_recovery_test.go](../../pkg/gitworkspace/pinned_line_recovery_test.go), [pkg/gitworkspace/pinned_recovery_suspension_capacity_test.go](../../pkg/gitworkspace/pinned_recovery_suspension_capacity_test.go), [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/development_line_test.go](../../pkg/gitworkspace/development_line_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go) |
| `FR-GITWS-016` | [pkg/gitworkspace/pinned_validation_roots.go](../../pkg/gitworkspace/pinned_validation_roots.go), [pkg/gitworkspace/pinned_validation_roots_change.go](../../pkg/gitworkspace/pinned_validation_roots_change.go), [pkg/gitworkspace/pinned_validation_roots_change_ctim.go](../../pkg/gitworkspace/pinned_validation_roots_change_ctim.go), [pkg/gitworkspace/pinned_validation_roots_test.go](../../pkg/gitworkspace/pinned_validation_roots_test.go), [pkg/prworkspace/localci/runner_test.go](../../pkg/prworkspace/localci/runner_test.go) |
| `FR-GITWS-017` | [pkg/gitworkspace/development_line_suspension.go](../../pkg/gitworkspace/development_line_suspension.go), [pkg/gitworkspace/development_line_suspension_api.go](../../pkg/gitworkspace/development_line_suspension_api.go), [pkg/gitworkspace/development_line_suspension_test.go](../../pkg/gitworkspace/development_line_suspension_test.go), [pkg/gitworkspace/development_line_suspension_api_test.go](../../pkg/gitworkspace/development_line_suspension_api_test.go), [pkg/gitworkspace/development_line_suspension_matrix_test.go](../../pkg/gitworkspace/development_line_suspension_matrix_test.go), [pkg/gitworkspace/development_line_suspension_adversarial_test.go](../../pkg/gitworkspace/development_line_suspension_adversarial_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-GITWS-018` | [pkg/gitworkspace/development_line_push.go](../../pkg/gitworkspace/development_line_push.go), [pkg/gitworkspace/development_line_push_test.go](../../pkg/gitworkspace/development_line_push_test.go) |
| `FR-GITWS-019` | [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go), [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go), [pkg/workflows/repository_bug_finder_workflow_test.go](../../pkg/workflows/repository_bug_finder_workflow_test.go) |
| `FR-GITWS-020` | [pkg/gitworkspace/development_line_browse.go](../../pkg/gitworkspace/development_line_browse.go), [pkg/gateway/pr_workspace_implementation.go](../../pkg/gateway/pr_workspace_implementation.go), [web/frontend/src/api/development-workspaces.test.ts](../../web/frontend/src/api/development-workspaces.test.ts), [web/frontend/src/components/development-workspaces/development-code-browser.test.tsx](../../web/frontend/src/components/development-workspaces/development-code-browser.test.tsx) |

## Implementation Anchors

- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/gitworkspace/pinned_commit.go](../../pkg/gitworkspace/pinned_commit.go)
- [pkg/gitworkspace/pinned_validation_roots.go](../../pkg/gitworkspace/pinned_validation_roots.go)
- [pkg/gitworkspace/development_line.go](../../pkg/gitworkspace/development_line.go)
- [pkg/gitworkspace/development_line_push.go](../../pkg/gitworkspace/development_line_push.go)
- [pkg/gitworkspace/development_line_browse.go](../../pkg/gitworkspace/development_line_browse.go)
- [pkg/gitworkspace/development_line_suspension.go](../../pkg/gitworkspace/development_line_suspension.go)
- [pkg/gitworkspace/development_line_suspension_api.go](../../pkg/gitworkspace/development_line_suspension_api.go)
- [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go)
- [pkg/gitworkspace/inventory_lock_unix.go](../../pkg/gitworkspace/inventory_lock_unix.go)
- [pkg/gitworkspace/inventory_lock_windows.go](../../pkg/gitworkspace/inventory_lock_windows.go)
- [pkg/agent/git_workspace.go](../../pkg/agent/git_workspace.go)
- [pkg/tools/integration/git_workspace.go](../../pkg/tools/integration/git_workspace.go)
- [web/backend/api/git_workspaces.go](../../web/backend/api/git_workspaces.go)
- [web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx](../../web/frontend/src/components/agent/git-workspaces/git-workspaces-page.tsx)
