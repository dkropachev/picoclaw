# Repository Reviews

## Feature ID

`FR-REPOREVIEW`

## Behavior Summary

Repository reviews provide a durable pre-review control plane and retain
validated bug findings independently from pull-request workspaces. Reusable
named review profiles own review behavior, one execution model, scope, work
bounds, and guardrails. A separate repository configuration assigns exactly one
profile to one normalized repository and may select only an optional branch;
blank follows the repository's advertised default branch. Configuration,
actual review runs, and completed results have separate launcher flows. Model
comparison is a separate repository model probe and never writes the review
campaign or finding ledger. An actual review exposes live stage and cumulative
consumption, pauses safely after the current checkpoint, resumes or restarts,
and automatically continues or resumes when configured quota criteria recover.
Each finding is bound to an exact repository commit, primary file blob and size,
one or more opaque AI context snapshots, and its contributing model. The result
flow lets a user inspect provenance, discuss findings in a durable reviewing
thread, change status, prepare an editable issue draft, and safely publish a
canonical GitHub issue through the protected provider.

## Reconstruction Notes

- Similarity target: recreate a launcher-owned repository pre-review control
  plane backed by bounded workflow batches and an independent immutable finding
  ledger, not a browser-only wrapper around the generic workflow form.
- Core types/functions: `repoaudit.Store`, `RepositoryReviewProfile`,
  `RepositoryReviewAutomation`, the repository review controller and API
  handlers, workflow `review.repository` native functions, the built-in
  repository-bug-finder template, and the separate profile, repository, run,
  and result launcher routes.
- Runtime ordering: validate and persist a reusable profile; atomically assign
  it to one branch-bound repository configuration; materialize the current
  profile version at admission; evaluate token, cost, and account-window guards;
  reserve one durable workflow run; execute and account provider calls; verify
  the record/no-op checkpoint; then complete, pause, or admit the next batch.
  Recovery first reconciles orphaned durable state before automatic resume.
- Non-obvious constraints: repository bytes remain immutable no-tool evidence;
  one workspace-wide controller lease prevents competing launchers; active
  guardrails force one provider request at a time; actual response usage may
  exceed a threshold by that one response; internal coverage sketches never
  enter API responses; issue publication uses a separate protected gateway
  boundary.

## Requirements

| ID                  | Level | Requirement                                                                                                                                                                                                                                                                                                                               | Rationale                                                                                                              |
| ------------------- | ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `FR-REPOREVIEW-001` | MUST  | Repository review state stores each validated finding with its commit SHA, primary file path/blob SHA/size, validation result, model contributors, observation count, and opaque context IDs; each referenced context stores its review profile hash and complete file-ref set.                                                           | Findings must be recoverable and attributable without guessing which source revision an AI saw.                        |
| `FR-REPOREVIEW-002` | MUST  | `GET /api/repository-reviews` returns compact repository summaries. `GET /api/repository-reviews/{id}` independently paginates findings and issue drafts, returns only contexts referenced by that finding page, bounds run/unsupported projections, and omits internal checkpoint maps. Finding-status and issue-draft mutations use the documented repository or draft version fence and return only the changed object plus a compact repository summary. | The frontend must recover large durable ledgers, reach older drafts, and reject stale edits without returning the whole internal state. |
| `FR-REPOREVIEW-003` | MUST  | `/repository-reviews` renders full commit/blob hashes, exact byte size, validation evidence, model consensus, and every context file reference.                                                                                                                                                                                           | A compact title without provenance is insufficient for acting on a bug report.                                         |
| `FR-REPOREVIEW-004` | MUST  | The user can select one or many findings. Discuss creates one `reviewing` thread tagged with repository/review/finding/context identity and sends one bounded self-contained message per selected finding; collectively those messages contain every selected finding and, for every retained context, its opaque ID, profile hash, model, and complete path/blob/size manifest before the thread opens. | Discussion must stay attached to the exact durable findings and recover every referenced file without a hidden lookup or a combined-prompt size failure. |
| `FR-REPOREVIEW-005` | MUST  | Prepare issue creates a durable draft from the exact selection; editing title, body, or labels uses the draft version.                                                                                                                                                                                                                    | Users must be able to refine a proposed issue without losing its finding membership.                                   |
| `FR-REPOREVIEW-006` | MUST  | For a repository value derived from the acquired checkout's exact GitHub origin as a normalized safe `owner/repo` identity, Post now first persists the draft and then publishes it through the protected gateway's exact GitHub issue-create capability. Publication durably changes the draft from `editing` to `publishing` before any external call, freezing the exact title/body/labels against concurrent edits. A stable draft marker is searched before every create; a recovered marker completes publication, while absent results for `publishing`/`unknown` never cause another create. Ambiguous outcomes become `unknown`. Success stores the provider issue ID/URL and marks the selected findings posted. An editable saved draft exposes the same publish action after discussion; the prefilled GitHub composer remains a manual alternative. Local/non-GitHub identities expose no publish action. | External publication must use a reviewable durable payload, a verified bounded destination, and idempotent recovery rather than treating a browser handoff, stale draft, concurrent request, or transport timeout as success. |
| `FR-REPOREVIEW-007` | MUST  | Review checkpoint CAS uses a dedicated `review_version`, while finding status, issue drafting, and publication use the aggregate/draft versions. UI mutations during a long AI run therefore merge with its later findings; only a newer review checkpoint invalidates an older review plan. Repository writes use OS locks on Unix and Windows. | A user discussing or drafting an issue must not make completed AI work fail, and launcher/gateway processes must not overwrite one another. |
| `FR-REPOREVIEW-008` | MUST  | A reusable versioned review profile can be created before any repository assignment, ledger, or finding. It stores a name, focus, exactly one safe reviewer alias, optional price metadata, force mode, bounded file/content/parallel/output settings, automatic batch continuation, token/USD budgets, selected account IDs, default and per-window minimum remaining percentages, unknown-limit policy, automatic resume, and a bounded recheck interval. Profile create/update/delete uses cross-process locking, atomic `0600` files, validation, and version CAS; deletion is rejected while assigned and mutation is rejected while an assigned review is running or stopping. | Review behavior should be reusable across repositories without coupling lifecycle state or mutable repository identity to the profile. |
| `FR-REPOREVIEW-009` | MUST  | A separate versioned repository configuration assigns one profile to one normalized repository and an optional branch. A repository has at most one configuration; one profile may serve many repositories. The authenticated run API and `/repository-reviews` run flow expose Start, safe Pause, Resume, Restart, and Delete. Starting snapshots the latest assigned profile version and assigns a durable workflow run ID before work begins. Active state exposes running/stopping/paused/completed/failed status, current workflow stage, bounded-batch progress, run history, timestamps, and a structured pause reason/detail. Automatic continuation launches the next bounded batch until no files remain. Safe pause stops admission after the current batch records its durable checkpoint. | Repository assignment, profile policy, and execution lifecycle require distinct authority and concurrency fences. |
| `FR-REPOREVIEW-010` | MUST  | Every non-nil provider response in workflow agent execution reports actual model-attributed prompt, completion, cached, and total token usage, including fallbacks and structured repairs. The controller durably aggregates that usage and configured-price cost per repository review run. Reaching a token or cost ceiling changes the active review to stopping and prevents another batch after the current bounded checkpoint; the UI states this bounded overshoot explicitly. Manual Resume may reset the budget counters, while Restart resets campaign progress/statistics. | A budget guard based only on prompt estimates or process logs cannot protect accounts or explain actual spend. |
| `FR-REPOREVIEW-011` | MUST  | Account limit telemetry is normalized into account/window snapshots with remaining percentage and reset time. Policies may select accounts, apply a default minimum and stricter named windows such as daily or weekly, and choose fail-open or fail-closed behavior for unknown telemetry. The backend—not a browser timer—rechecks active and quota-paused automations, safely stops admission when a threshold is crossed, and automatically resumes exactly once when all configured criteria recover. | Daily, weekly, and provider-specific limits must protect unattended reviews and recover after reset even when the dashboard is closed. |
| `FR-REPOREVIEW-012` | MUST  | The profile UI presents available safe reviewer aliases and known or operator-supplied input/output prices for the one actual-review model. Runtime retains request/failure counts, actual tokens, estimated USD, compact approximate reviewed-file coverage, and finding yield without claiming unknown price as zero. Comparative model testing, scoring, and efficiency ranking remain exclusively in the separate model-review probe domain and never start or mutate an actual review. Agentic CLI providers remain unavailable for immutable repository review. | Operators need honest actual-run economics without conflating exploratory model comparison with production findings. |
| `FR-REPOREVIEW-013` | MUST  | On launcher startup, the durable controller reconciles an automation left running without a local executor, marks the orphaned workflow run canceled, records a `service_restart` pause, and resumes from repository checkpoints only when automatic resume is enabled. Controller shutdown cancels in-process execution and never creates a new batch after its context is closed. | Process restarts must not leave phantom running state, duplicate work, or silently disable unattended recovery. |
| `FR-REPOREVIEW-014` | MUST  | Each review profile persists a bounded scope policy with one or more selectable inventory code types (`hotpath-code`, `code`, `test`, `bench-test`), canonical repository-relative include and exclude folder prefixes, and optional free-text guidance. The default selects normal production code (`hotpath-code` and `code`); exclusions always win. A generated repository preflight is commit-bound and persists bounded policy/plan hashes, summary, rationale, warnings, and aggregate file counts. Changing the assigned profile or its execution scope clears any prior commit-bound plan. | Operators must be able to express reusable, reproducible review intent without allowing unsafe paths, unbounded manifests, stale commit summaries, or an exclusion to be silently re-included. |
| `FR-REPOREVIEW-015` | MUST  | Repository configuration accepts only an optional branch name. Blank resolves through the acquired repository's advertised default branch; `HEAD`, commit hashes, tags, full refs, revision expressions, URLs, query/fragment forms, and invalid Git branch names are rejected before admission. The launcher exposes separate Review runs, Repositories, Profiles, and Results destinations. Basic fields are immediately visible; Scope, execution sizing, price overrides, and detailed spending/account policies remain in an Advanced section that is collapsed by default and preserves its draft while closed. | Repository review should follow a predictable branch, never masquerade an immutable commit/tag as mutable configuration, and avoid one overloaded control surface. |

## Data And State Model

| State | Shape And Location | Contract |
| --- | --- | --- |
| Review profile | `workspace/repository_reviews/profile_rrpf_*.json` | Reusable versioned `0600` behavior, one reviewer alias, scope, sizing, price, and guard policy without repository or lifecycle state. |
| Repository configuration/run | `workspace/repository_reviews/automation_rra_*.json` | Unique normalized repository/profile assignment, optional branch, materialized profile version, lifecycle, guard-epoch usage/cost, bounded run history, quota snapshots, and internal approximate coverage sketch. |
| Repository ledger | `workspace/repository_reviews/repo_*.json` plus summary sidecar | Exact commit/blob checkpoints, attempts, unsupported files, findings, observations, contexts, completed review runs, and issue drafts. |
| Workflow run | `workspace/workflow_runs/<run-id>/` | Generic durable job/step state and events; automation stores only bounded identities/progress and requires a verified `record` output or authoritative no-op checkpoint. |
| Controller lease | `workspace/repository_reviews.controller.lock` | Non-blocking workspace-wide OS lock held for the launcher controller lifetime; never returned by an API. |
| Guard epoch | `usage`, `estimated_cost_usd`, `budget` | Resume may reset only current guard counters. Restart establishes a new campaign epoch and clears progress/model comparison state while the repository blob ledger still controls unchanged-file reuse. |
| Actual-model statistics | `model_stats`, `model_coverage_sketches` | Per-campaign request/failure/usage/cost/latency/finding counts and fixed-size approximate unique coverage for the assigned profile model. Sketches are persistence-only and removed from API projections. |
| Scope policy and preflight | `scope_policy` in the review profile and `scope_plan` in the repository run | Reusable code-type/folder/free-text intent plus the latest bounded commit SHA, policy hash, plan hash, explanation, warnings, and counts. It does not contain the selected-file manifest. |

## Surface Ownership

Owns: CODE pkg/repoaudit/**
Owns: TEST pkg/repoaudit/**
Owns: CODE pkg/gateway/repository_review_publication.go
Owns: TEST pkg/gateway/repository_review_publication_test.go *
Owns: TEST pkg/gateway/repository_review_publication_coverage_test.go *
Owns: CODE web/backend/api/repository_review*.go
Owns: TEST web/backend/api/repository_review*_test.go
Owns: CODE web/frontend/src/api/repository-reviews.ts
Owns: TEST web/frontend/src/api/repository-reviews.test.ts
Owns: CODE web/frontend/src/components/repository-reviews/**
Owns: TEST web/frontend/src/components/repository-reviews/**
Owns: CODE web/frontend/src/routes/repository-reviews*.tsx
Owns: TEST web/frontend/src/routes/-repository-reviews*.test.tsx
Owns: HTTP * /api/repository-reviews*
Owns: UI /repository-reviews*

Profile state is stored in `workspace/repository_reviews/profile_rrpf_*.json`;
repository configuration and runtime state remains in
`workspace/repository_reviews/automation_rra_*.json`. Authenticated profile CRUD
uses `/api/repository-reviews/profiles*`. Repository configuration and run
lifecycle use `/api/repository-reviews/automations*`, with `start`, `pause`,
`resume`, and `restart` action subresources plus
`GET /api/repository-reviews/automation-options` for safe model/account choices.
Profileless automation files written by older versions remain readable for
recovery and have legacy `HEAD`/target values sanitized at admission. New HTTP
creation always requires `profile_id`; the split UI does not expose legacy
profileless editing or multi-model actual-review configuration.

The shared application sidebar and thread/chat controller remain auxiliary
interfaces owned by their existing feature specifications.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `GET/POST/PATCH/DELETE /api/repository-reviews/profiles*` | List/create/update/delete reusable CAS-fenced single-model profiles, including bounded scope, sizing, price, and guard policy. Assigned deletion and active-assignment mutation fail closed. | `FR-REPOREVIEW-008`, `FR-REPOREVIEW-012`, `FR-REPOREVIEW-014` |
| HTTP | `GET/POST/PATCH/DELETE /api/repository-reviews/automations*` | List/create/update/delete unique repository/profile assignments and invoke start, pause, resume, or restart transitions. Internal sketches and credentials are never projected; bounded scope preflight summaries are returned. | `FR-REPOREVIEW-009`, `FR-REPOREVIEW-013`, `FR-REPOREVIEW-015` |
| HTTP | `GET /api/repository-reviews/automation-options` | Return safe configured aliases, conservative selectable-route price metadata, and normalized limit-aware accounts without credential values. | `FR-REPOREVIEW-011`, `FR-REPOREVIEW-012` |
| HTTP | `GET/PATCH/POST /api/repository-reviews/**` | Page result ledgers and mutate finding status or issue drafts with exact version fences. | `FR-REPOREVIEW-001`–`FR-REPOREVIEW-007` |
| Gateway HTTP | `/runtime/repository-reviews/<repo>/issue-drafts/<draft>/publish` | Protected, idempotent publication/reconciliation boundary for canonical GitHub identities. | `FR-REPOREVIEW-006` |
| UI | `/repository-reviews`, `/repository-reviews/repositories`, `/repository-reviews/profiles`, `/repository-reviews/results` | Separate actual-run, one-profile-per-repository assignment, reusable profile, and completed finding/draft flows. Advanced profile controls are hidden by default; polling is observation only, not execution authority. | `FR-REPOREVIEW-003`–`FR-REPOREVIEW-005`, `FR-REPOREVIEW-008`–`FR-REPOREVIEW-015` |

## Algorithms And Ordering

1. Profile create/update normalizes one reviewer, prices, work bounds, scope,
   and guard policy under the shared review-store lock. Code types are
   canonicalized; folder prefixes must be exact safe repository-relative paths.
   CAS mismatch fails without partial mutation.
2. Repository configuration create/update normalizes repository identity and an
   optional branch under the catalog lock, atomically rejects a second
   configuration for the same repository, and binds one existing profile.
3. Start/resume/restart checks the action-specific source state, controller
   lifecycle, workspace lease, current configuration and assigned profile
   versions, token/cost threshold, and selected account windows. A changed
   profile is materialized and starts a new campaign before admission. A failed
   guard persists a structured pause; admission never allocates a workflow run.
4. Admission creates a run ID and persists `running` plus run history before a
   goroutine executes the built-in repository-bug-finder workflow with the exact
   captured profile and repository branch. A named branch is passed as
   `refs/heads/<branch>`; blank uses the repository's remote default.
5. The workflow acquires a fresh checkout, inventories exact Git blobs, plans
   only changed/profile-invalidated work, freezes bounded source evidence, and
   releases the checkout before model calls. Managed review takes the Cartesian
   one bounded scope with the assigned profile model.
6. Every provider response records requested reviewer, actual model, actual or
   conservative token usage, and latency. A persistence failure fails closed.
   When any guard is active, call admission is serialized; a threshold-crossing
   response is still validated, but subsequent provider calls are rejected.
7. A completed batch counts only after the qualified record step persisted a
   run or the authoritative no-op result was verified. The controller then
   merges campaign-scoped ledger outcomes and either completes, safely pauses,
   or atomically admits the next bounded batch.
8. The monitor periodically refreshes active and account-paused runs.
   Known exhausted limits always block; unknown telemetry follows
   `pause_on_unknown`. Only account-limit/service-restart pauses with
   `auto_resume` are eligible for unattended restart.
9. Issue publication first freezes a durable draft in `publishing`, searches
   its stable marker, creates only when safe, and stores `posted` or `unknown`
   before returning.

## Cross-Feature Behavior

- Workflows owns generic run persistence, DAG execution, events, cancellation,
  immutable repository-bug-finder YAML, and agent call admission plumbing.
- Agent execution optimization owns managed splitting, independent reviewer
  assignment, usage aggregation, and structured repair behavior.
- Account routing/model configuration owns alias resolution and conservative
  provider/account price metadata; GitHub Copilot supplies measured or
  conservative usage when its transports omit it.
- Git workspaces owns fresh lease acquisition, immutable Git-object reads, and
  checkout release. Repository review never retains mutable workspace access in
  a model call.
- Repository model evaluations owns the separate model-review probe lifecycle,
  frozen comparison corpus, blinded judging, and quality/efficiency ranking. A
  probe never changes a review profile, repository run, or finding ledger.
- Launcher authentication and same-origin mutation guards protect every control
  and result mutation. Runtime events/workflow pages remain optional detailed
  observation surfaces.
- Threads owns durable discussion creation; the protected GitHub provider owns
  external issue calls. Repository review supplies exact provenance and draft
  state to those features.

## Failure And Edge Cases

- Duplicate normalized repository assignments, missing or assigned profiles,
  credentialed or query/fragment-bearing repository URLs, unsafe paths,
  symlinked roots/files/locks, oversized ledgers, invalid reviewer aliases,
  unsafe agentic CLI reviewers, invalid prices, and out-of-range work/guard
  values fail before execution.
- Scope policies reject unknown/duplicate code types, absolute, parent-relative,
  non-canonical, duplicate, or over-limit folder prefixes, and oversized free
  text. Include folders narrow category matches and excludes always win. The
  commit-bound summary is invalidated when repository/branch/profile/scope changes.
- Branch configuration rejects detached commit or tag targets and every unsafe
  or ambiguous ref form. Internal workflow checkpoints may still reacquire the
  exact commit resolved from the admitted branch.
- A cost guard requires a positive price for the executable reviewer. Unknown
  price is displayed as unknown, never zero/free.
- Active token/cost/account guards require one parallel child. The documented
  overshoot bound is one completed provider response, including Copilot
  responses whose transport required conservative token estimation.
- Unknown account telemetry fails open or closed exactly as configured; known
  `limit_reached`/exhausted states always block. Multiple limits in one window
  remain distinct by account, limit name, and window.
- Manual/token/cost pause intent survives a launcher restart and is not
  auto-resumed. Orphaned running work becomes `service_restart`; quota/service
  recovery admits at most one new run under the controller lease.
- A workflow that reports success without a verified durable record/no-op
  checkpoint becomes `failed`, so incomplete work is never counted as a batch.
- Accepted findings/coverage are scoped to the automation campaign. Approximate
  model coverage is monotonic and explicitly labeled; internal sketches are
  excluded from frequently polled API responses.
- Publication transport ambiguity becomes `unknown`; retries reconcile the
  stable marker and never blindly create a duplicate issue.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-REPOREVIEW-001` | [pkg/repoaudit/store_test.go](../../pkg/repoaudit/store_test.go), [pkg/repoaudit/ensemble_test.go](../../pkg/repoaudit/ensemble_test.go) |
| `FR-REPOREVIEW-002` | [web/backend/api/repository_reviews_test.go](../../web/backend/api/repository_reviews_test.go), [web/frontend/src/api/repository-reviews.test.ts](../../web/frontend/src/api/repository-reviews.test.ts) |
| `FR-REPOREVIEW-003` | [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx) |
| `FR-REPOREVIEW-004` | [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx), [web/frontend/src/components/repository-reviews/repository-review-actions.ts](../../web/frontend/src/components/repository-reviews/repository-review-actions.ts) |
| `FR-REPOREVIEW-005` | [pkg/repoaudit/ensemble_test.go](../../pkg/repoaudit/ensemble_test.go), [web/backend/api/repository_reviews_test.go](../../web/backend/api/repository_reviews_test.go) |
| `FR-REPOREVIEW-006` | [pkg/gateway/repository_review_publication_test.go](../../pkg/gateway/repository_review_publication_test.go), [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx) |
| `FR-REPOREVIEW-007` | [pkg/repoaudit/store_test.go](../../pkg/repoaudit/store_test.go), [pkg/repoaudit/ensemble_test.go](../../pkg/repoaudit/ensemble_test.go) |
| `FR-REPOREVIEW-008` | [pkg/repoaudit/profile_test.go](../../pkg/repoaudit/profile_test.go), [web/backend/api/repository_review_profiles_test.go](../../web/backend/api/repository_review_profiles_test.go), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx) |
| `FR-REPOREVIEW-009` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/components/repository-reviews/repository-review-runs-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-runs-page.test.tsx), [web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx) |
| `FR-REPOREVIEW-010` | [pkg/agent/workflow_managed_ensemble_test.go](../../pkg/agent/workflow_managed_ensemble_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go) |
| `FR-REPOREVIEW-011` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go) |
| `FR-REPOREVIEW-012` | [pkg/providers/cli/github_copilot_provider_test.go](../../pkg/providers/cli/github_copilot_provider_test.go), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx), [web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx](../../web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx) |
| `FR-REPOREVIEW-013` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [pkg/repoaudit/control_test.go](../../pkg/repoaudit/control_test.go) |
| `FR-REPOREVIEW-014` | [pkg/repoaudit/scope_policy_test.go](../../pkg/repoaudit/scope_policy_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/api/repository-reviews.test.ts](../../web/frontend/src/api/repository-reviews.test.ts), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx) |
| `FR-REPOREVIEW-015` | [pkg/repoaudit/profile_test.go](../../pkg/repoaudit/profile_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx), [web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx) |

## Implementation Anchors

- [pkg/repoaudit/control.go](../../pkg/repoaudit/control.go)
- [pkg/repoaudit/profile.go](../../pkg/repoaudit/profile.go)
- [pkg/repoaudit/scope_policy.go](../../pkg/repoaudit/scope_policy.go)
- [pkg/repoaudit/store.go](../../pkg/repoaudit/store.go)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/workflows/native_functions.go](../../pkg/workflows/native_functions.go)
- [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go)
- [web/backend/api/repository_review_controller.go](../../web/backend/api/repository_review_controller.go)
- [web/backend/api/repository_review_automations.go](../../web/backend/api/repository_review_automations.go)
- [web/backend/api/repository_reviews.go](../../web/backend/api/repository_reviews.go)
- [pkg/gateway/repository_review_publication.go](../../pkg/gateway/repository_review_publication.go)
- [web/frontend/src/components/repository-reviews/repository-review-profiles-page.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.tsx)
- [web/frontend/src/components/repository-reviews/repository-review-repositories-page.tsx](../../web/frontend/src/components/repository-reviews/repository-review-repositories-page.tsx)
- [web/frontend/src/components/repository-reviews/repository-reviews-page.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.tsx)
