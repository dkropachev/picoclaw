# Repository Reviews

## Feature ID

`FR-REPOREVIEW`

## Behavior Summary

Repository reviews provide a durable pre-review control plane and retain
validated bug findings independently from pull-request workspaces. Before any
finding exists, the launcher dashboard can save a repository/ref profile,
review focus, model comparison set, bounded batch shape, token/cost budgets,
and account-window quota thresholds. It can start the review, show its live
stage and cumulative consumption, pause safely after the current checkpoint,
resume or restart it, and automatically continue or resume when configured
quota criteria recover. Each finding is bound to an exact repository commit,
primary file blob and size, one or more opaque AI context snapshots, and its
contributing models. The dashboard lets a user inspect that provenance, select
one or many findings, discuss the selected set in a durable reviewing thread,
change finding status, and prepare an editable issue draft. For canonical
`owner/repo` identities, the dashboard can publish the exact prepared draft
through the protected gateway GitHub provider, recover an ambiguous response by
its stable marker, and link the posted issue. Opening the GitHub composer remains
an explicit manual fallback and is not proof of posting.

## Reconstruction Notes

- Similarity target: recreate a launcher-owned repository pre-review control
  plane backed by bounded workflow batches and an independent immutable finding
  ledger, not a browser-only wrapper around the generic workflow form.
- Core types/functions: `repoaudit.Store`, `RepositoryReviewAutomation`, the
  repository review controller and API handlers, workflow `review.repository`
  native functions, the built-in repository-bug-finder template, and the
  `/repository-reviews` control center/result ledger.
- Runtime ordering: validate and persist a profile; evaluate token, cost, and
  account-window guards; reserve one durable workflow run; execute and account
  provider calls; verify the record/no-op checkpoint; then complete, pause, or
  admit the next batch. Recovery first reconciles orphaned durable state before
  it considers automatic resume.
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
| `FR-REPOREVIEW-008` | MUST  | A versioned repository-review automation can be created before the first ledger or finding. It stores repository/ref/target/focus, one or more reviewer aliases, comparison mode, optional per-model price metadata, force mode, bounded file/content/parallel/output settings, automatic batch continuation, token/USD budgets, selected account IDs, default and per-window minimum remaining percentages, unknown-limit policy, automatic resume, and a bounded recheck interval. Create/update/delete use cross-process locking, atomic `0600` files, validation, and version CAS. | Pre-review setup must be a reusable repository policy rather than a transient raw workflow form. |
| `FR-REPOREVIEW-009` | MUST  | The authenticated automation API and `/repository-reviews` UI expose Start, safe Pause, Resume, Restart, and Delete. Starting assigns a durable workflow run ID before work begins. Active state exposes running/stopping/paused/completed/failed status, current workflow stage, bounded-batch progress, run history, timestamps, and a structured pause reason/detail. Automatic continuation launches the next bounded batch until no files remain. Safe pause stops admission after the current batch records its durable checkpoint. | Operators need one place to launch, observe, stop, and continue repository work without discarding a completed model batch. |
| `FR-REPOREVIEW-010` | MUST  | Every non-nil provider response in workflow agent execution reports actual model-attributed prompt, completion, cached, and total token usage, including fallbacks, structured repairs, and concurrent managed children. The controller durably aggregates that usage and configured-price cost per automation and reviewer. Reaching a token or cost ceiling changes the active review to stopping and prevents another batch after the current bounded checkpoint; the UI states this bounded overshoot explicitly. Manual Resume may reset the budget counters, while Restart resets campaign progress/statistics. | A budget guard based only on prompt estimates or process logs cannot protect accounts or explain actual spend. |
| `FR-REPOREVIEW-011` | MUST  | Account limit telemetry is normalized into account/window snapshots with remaining percentage and reset time. Policies may select accounts, apply a default minimum and stricter named windows such as daily or weekly, and choose fail-open or fail-closed behavior for unknown telemetry. The backend—not a browser timer—rechecks active and quota-paused automations, safely stops admission when a threshold is crossed, and automatically resumes exactly once when all configured criteria recover. | Daily, weekly, and provider-specific limits must protect unattended reviews and recover after reset even when the dashboard is closed. |
| `FR-REPOREVIEW-012` | MUST  | The setup UI presents available safe reviewer aliases and known or operator-supplied input/output prices. The runtime retains per-reviewer request/failure counts, actual tokens, estimated USD, compact approximate unique reviewed-file coverage, and finding yield. The comparison table labels approximate coverage, derives cost per finding, and visually identifies the cheapest known successful reviewer without claiming a price when metadata is absent. Agentic CLI providers remain unavailable for immutable repository review. | Users need comparable quality and economics signals to choose a cheaper review model deliberately. |
| `FR-REPOREVIEW-013` | MUST  | On launcher startup, the durable controller reconciles an automation left running without a local executor, marks the orphaned workflow run canceled, records a `service_restart` pause, and resumes from repository checkpoints only when automatic resume is enabled. Controller shutdown cancels in-process execution and never creates a new batch after its context is closed. | Process restarts must not leave phantom running state, duplicate work, or silently disable unattended recovery. |

## Data And State Model

| State | Shape And Location | Contract |
| --- | --- | --- |
| Automation profile | `workspace/repository_reviews/automation_rra_*.json` | Versioned `0600` configuration plus lifecycle, guard-epoch usage/cost, bounded run history, quota snapshots, lifetime model statistics, and internal approximate coverage sketches. |
| Repository ledger | `workspace/repository_reviews/repo_*.json` plus summary sidecar | Exact commit/blob checkpoints, attempts, unsupported files, findings, observations, contexts, completed review runs, and issue drafts. |
| Workflow run | `workspace/workflow_runs/<run-id>/` | Generic durable job/step state and events; automation stores only bounded identities/progress and requires a verified `record` output or authoritative no-op checkpoint. |
| Controller lease | `workspace/repository_reviews.controller.lock` | Non-blocking workspace-wide OS lock held for the launcher controller lifetime; never returned by an API. |
| Guard epoch | `usage`, `estimated_cost_usd`, `budget` | Resume may reset only current guard counters. Restart establishes a new campaign epoch and clears progress/model comparison state while the repository blob ledger still controls unchanged-file reuse. |
| Model comparison | `model_stats`, `model_coverage_sketches` | Lifetime per-campaign request/failure/usage/cost/latency/finding counts and fixed-size approximate unique coverage. Sketches are persistence-only and are removed from API projections. |

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
Owns: CODE web/frontend/src/routes/repository-reviews.tsx
Owns: TEST web/frontend/src/routes/-repository-reviews-route.test.tsx
Owns: HTTP * /api/repository-reviews*
Owns: UI /repository-reviews

Automation state is stored in
`workspace/repository_reviews/automation_rra_*.json`. The authenticated control
surface is `GET/POST/PATCH/DELETE /api/repository-reviews/automations*`, with
`start`, `pause`, `resume`, and `restart` action subresources plus
`GET /api/repository-reviews/automation-options` for safe model/account choices.

The shared application sidebar and thread/chat controller remain auxiliary
interfaces owned by their existing feature specifications.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `GET/POST/PATCH/DELETE /api/repository-reviews/automations*` | List/create/update/delete CAS-fenced profiles and invoke start, pause, resume, or restart transitions. Internal sketches and credentials are never projected. | `FR-REPOREVIEW-008`, `FR-REPOREVIEW-009`, `FR-REPOREVIEW-013` |
| HTTP | `GET /api/repository-reviews/automation-options` | Return safe configured aliases, conservative selectable-route price metadata, and normalized limit-aware accounts without credential values. | `FR-REPOREVIEW-011`, `FR-REPOREVIEW-012` |
| HTTP | `GET/PATCH/POST /api/repository-reviews/**` | Page result ledgers and mutate finding status or issue drafts with exact version fences. | `FR-REPOREVIEW-001`–`FR-REPOREVIEW-007` |
| Gateway HTTP | `/runtime/repository-reviews/<repo>/issue-drafts/<draft>/publish` | Protected, idempotent publication/reconciliation boundary for canonical GitHub identities. | `FR-REPOREVIEW-006` |
| UI | `/repository-reviews` | Persistent control center above the completed finding/draft ledger; polling is observation only, not automation authority. | `FR-REPOREVIEW-003`–`FR-REPOREVIEW-005`, `FR-REPOREVIEW-008`–`FR-REPOREVIEW-013` |

## Algorithms And Ordering

1. Create/update normalizes the repository, ref, reviewers, prices, work bounds,
   and guard policy under the shared review-store lock. CAS mismatch fails
   without partial mutation. Execution-affecting changes start a new campaign;
   price-only edits are non-retroactive and do not erase progress.
2. Start/resume/restart checks the action-specific source state, controller
   lifecycle, workspace lease, current automation version, token/cost threshold,
   and selected account windows. A failed guard persists a structured pause;
   admission never allocates a workflow run.
3. Admission creates a run ID and persists `running` plus run history before a
   goroutine executes the built-in repository-bug-finder workflow with the exact
   captured profile and workspace configuration.
4. The workflow acquires a fresh checkout, inventories exact Git blobs, plans
   only changed/profile-invalidated work, freezes bounded source evidence, and
   releases the checkout before model calls. Managed review takes the Cartesian
   product of bounded scope and reviewer aliases.
5. Every provider response records requested reviewer, actual model, actual or
   conservative token usage, and latency. A persistence failure fails closed.
   When any guard is active, call admission is serialized; a threshold-crossing
   response is still validated, but subsequent provider calls are rejected.
6. A completed batch counts only after the qualified record step persisted a
   run or the authoritative no-op result was verified. The controller then
   merges campaign-scoped ledger outcomes and either completes, safely pauses,
   or atomically admits the next bounded batch.
7. The monitor periodically refreshes active and account-paused profiles.
   Known exhausted limits always block; unknown telemetry follows
   `pause_on_unknown`. Only account-limit/service-restart pauses with
   `auto_resume` are eligible for unattended restart.
8. Issue publication first freezes a durable draft in `publishing`, searches
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
- Launcher authentication and same-origin mutation guards protect every control
  and result mutation. Runtime events/workflow pages remain optional detailed
  observation surfaces.
- Threads owns durable discussion creation; the protected GitHub provider owns
  external issue calls. Repository review supplies exact provenance and draft
  state to those features.

## Failure And Edge Cases

- Credentialed or query/fragment-bearing repository URLs, unsafe paths,
  symlinked roots/files/locks, oversized ledgers, invalid reviewer aliases,
  unsafe agentic CLI reviewers, invalid prices, and out-of-range work/guard
  values fail before execution.
- A cost guard requires a positive price for each executable reviewer. Unknown
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
| `FR-REPOREVIEW-008` | [pkg/repoaudit/control_test.go](../../pkg/repoaudit/control_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go) |
| `FR-REPOREVIEW-009` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/components/repository-reviews/repository-review-control-center.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-control-center.test.tsx) |
| `FR-REPOREVIEW-010` | [pkg/agent/workflow_managed_ensemble_test.go](../../pkg/agent/workflow_managed_ensemble_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go) |
| `FR-REPOREVIEW-011` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go) |
| `FR-REPOREVIEW-012` | [pkg/agent/workflow_managed_ensemble_test.go](../../pkg/agent/workflow_managed_ensemble_test.go), [pkg/providers/cli/github_copilot_provider_test.go](../../pkg/providers/cli/github_copilot_provider_test.go), [web/frontend/src/components/repository-reviews/repository-review-control-center.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-control-center.test.tsx) |
| `FR-REPOREVIEW-013` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [pkg/repoaudit/control_test.go](../../pkg/repoaudit/control_test.go) |

## Implementation Anchors

- [pkg/repoaudit/control.go](../../pkg/repoaudit/control.go)
- [pkg/repoaudit/store.go](../../pkg/repoaudit/store.go)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/workflows/native_functions.go](../../pkg/workflows/native_functions.go)
- [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go)
- [web/backend/api/repository_review_controller.go](../../web/backend/api/repository_review_controller.go)
- [web/backend/api/repository_review_automations.go](../../web/backend/api/repository_review_automations.go)
- [web/backend/api/repository_reviews.go](../../web/backend/api/repository_reviews.go)
- [pkg/gateway/repository_review_publication.go](../../pkg/gateway/repository_review_publication.go)
- [web/frontend/src/components/repository-reviews/repository-review-control-center.tsx](../../web/frontend/src/components/repository-reviews/repository-review-control-center.tsx)
- [web/frontend/src/components/repository-reviews/repository-reviews-page.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.tsx)
