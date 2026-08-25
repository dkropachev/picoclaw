# Repository Reviews

## Feature ID

`FR-REPOREVIEW`

## Behavior Summary

Repository reviews provide a durable pre-review control plane and retain
validated bug findings independently from pull-request workspaces. Reusable
named review profiles own review behavior, one execution model, scope, real
provider-call work bounds, and guardrails. A separate repository configuration assigns exactly one
profile to one normalized repository and may select only an optional branch;
blank follows the repository's advertised default branch. Configuration,
actual review runs, and completed results have separate launcher flows. Model
comparison is a separate repository model probe that reads and freezes an
evaluation-relevant profile snapshot at admission but never writes the profile,
review campaign, or finding ledger. An actual review exposes live stage and cumulative
consumption, pauses safely after the current checkpoint, resumes or restarts,
and automatically continues through bounded batches. Each profile selects one
execution account (blank follows the runtime default) and may define one
bounded task-admission expression evaluated whenever a worker takes its next
task. At Start/Resume/Restart the chosen default is resolved and stored as the
campaign's effective account so completed-run provenance never follows a later
default-account change.
Each finding is bound to an exact repository commit, primary file blob and size,
one or more opaque AI context snapshots, and its contributing model. The result
flow lets a user inspect provenance, discuss findings in a durable reviewing
thread, change status, prepare an editable issue draft, and safely publish a
canonical GitHub issue through the protected provider.
Model findings are diagnosis-only: they describe a confirmed defect and its
evidence, never a fix, recommendation, remediation, workaround, patch, or test
change.
The same model-output boundary applies to the generic code-review template and
PR-workspace review/completion findings; any later implementation or
user-directed discussion is a separate process rather than a field smuggled
through the finding.

New model-probe reports also retain a bounded diagnosis-only claim ledger for
each model: path, title, evidence, impact, supported/unsupported judge
disposition, and rationale. The report exposes those claims in a collapsed
drill-down beside its charts and textual analysis. It retains no prompts,
repository source payloads, fixes, or provider payloads; historical
aggregate-only reports remain readable.

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
  profile version and execution account; reserve one durable workflow run;
  immediately before each managed worker takes a task, load exact cumulative
  usage, reserve projected in-flight usage, refresh the selected account's
  referenced limit telemetry, and evaluate the guard expression; then execute
  and account provider calls and verify the record/no-op checkpoint. Recovery
  reconciles orphaned durable state into an explicit paused state.
- Non-obvious constraints: repository bytes remain immutable no-tool evidence;
  one workspace-wide controller lease prevents competing launchers; eight
  workers may run concurrently, while atomic projected-usage reservations stop
  simultaneous task pickups from all passing the same token/cost predicate;
  internal coverage sketches never enter API responses; issue publication uses
  a separate protected gateway boundary.

## Requirements

| ID                  | Level | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Rationale                                                                                                                                                                                                                     |
| ------------------- | ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-REPOREVIEW-001` | MUST  | Repository review state stores each validated finding with its commit SHA, primary file path/blob SHA/size, validation result, model contributors, observation count, and opaque context IDs; each referenced context stores its review profile hash and complete file-ref set.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Findings must be recoverable and attributable without guessing which source revision an AI saw.                                                                                                                               |
| `FR-REPOREVIEW-002` | MUST  | `GET /api/repository-reviews` returns compact repository summaries. `GET /api/repository-reviews/{id}` independently paginates findings and issue drafts, returns only contexts referenced by that finding page, bounds run/unsupported projections, and omits internal checkpoint maps. Finding-status and issue-draft mutations use the documented repository or draft version fence and return only the changed object plus a compact repository summary.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | The frontend must recover large durable ledgers, reach older drafts, and reject stale edits without returning the whole internal state.                                                                                       |
| `FR-REPOREVIEW-003` | MUST  | `/repository-reviews` renders full commit/blob hashes, exact byte size, validation evidence, model consensus, and every context file reference.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | A compact title without provenance is insufficient for acting on a bug report.                                                                                                                                                |
| `FR-REPOREVIEW-004` | MUST  | The user can select one or many findings. Discuss creates one `reviewing` thread tagged with repository/review/finding/context identity and sends one bounded self-contained message per selected finding; collectively those messages contain every selected finding and, for every retained context, its opaque ID, profile hash, model, and complete path/blob/size manifest before the thread opens.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Discussion must stay attached to the exact durable findings and recover every referenced file without a hidden lookup or a combined-prompt size failure.                                                                      |
| `FR-REPOREVIEW-005` | MUST  | Prepare issue creates a durable draft from the exact selection; editing title, body, or labels uses the draft version.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Users must be able to refine a proposed issue without losing its finding membership.                                                                                                                                          |
| `FR-REPOREVIEW-006` | MUST  | For a repository value derived from the acquired checkout's exact GitHub origin as a normalized safe `owner/repo` identity, Post now first persists the draft and then publishes it through the protected gateway's exact GitHub issue-create capability. Publication durably changes the draft from `editing` to `publishing` before any external call, freezing the exact title/body/labels against concurrent edits. A stable draft marker is searched before every create; a recovered marker completes publication, while absent results for `publishing`/`unknown` never cause another create. Ambiguous outcomes become `unknown`. Success stores the provider issue ID/URL and marks the selected findings posted. An editable saved draft exposes the same publish action after discussion; the prefilled GitHub composer remains a manual alternative. Local/non-GitHub identities expose no publish action.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | External publication must use a reviewable durable payload, a verified bounded destination, and idempotent recovery rather than treating a browser handoff, stale draft, concurrent request, or transport timeout as success. |
| `FR-REPOREVIEW-007` | MUST  | Review checkpoint CAS uses a dedicated `review_version`, while finding status, issue drafting, and publication use the aggregate/draft versions. UI mutations during a long AI run therefore merge with its later findings; only a newer review checkpoint invalidates an older review plan. Repository writes use OS locks on Unix and Windows.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | A user discussing or drafting an issue must not make completed AI work fail, and launcher/gateway processes must not overwrite one another.                                                                                   |
| `FR-REPOREVIEW-008` | MUST  | A reusable versioned review profile can be created before any repository assignment, ledger, finding, or model probe. It stores a name, focus, exactly one safe reviewer alias, one optional execution `account_ref` (blank means the runtime default), force mode, bounded files-per-batch/content-bytes-per-batch/parallel settings, automatic batch continuation, scope, and one bounded `guard_expression`. For an actual repository review, the files value bounds both pending batch admission and the related-file group sent to a provider child, while the content value bounds that real provider-call group; immutable hydration retains its independent safe per-file ceiling. A model probe may read the latest profile and freeze its ID/version/name, effective account, reviewer, focus/scope, file/content maxima, and parallelism, but it does not assign or mutate the profile and does not inherit actual-review-only force, continuation, or task-guard authority. The profile never stores model prices, account lists, polling intervals, output-token estimates, or separate token/cost/quota threshold controls. Profile create/update/delete uses cross-process locking, atomic `0600` files, validation, and version CAS; legacy budget fields migrate to an equivalent guard expression on durable load, deletion is rejected while assigned, and mutation is rejected while an assigned review is running or stopping. Profile mutation clients send only writable configuration fields, plus `expected_version` for updates, and never echo response metadata such as `schema_version`, ID, version, or timestamps. | Review behavior should be reusable across production review and controlled model experiments while account routing, mutation, and actual-review admission authority remain explicit. |
| `FR-REPOREVIEW-009` | MUST  | A separate versioned repository configuration assigns one profile to one normalized repository and an optional branch. A repository has at most one configuration; one profile may serve many repositories. The authenticated run API and `/repository-reviews` run flow expose Start, safe Pause, Resume, Restart, and Delete. Starting snapshots the latest assigned profile version and assigns a durable workflow run ID before work begins. Active state exposes running/stopping/paused/completed/failed status, current workflow stage, bounded-batch progress, run history, timestamps, and a structured pause reason/detail. Automatic continuation launches the next bounded batch until no files remain. Safe pause stops admission after the current batch records its durable checkpoint.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Repository assignment, profile policy, and execution lifecycle require distinct authority and concurrency fences.                                                                                                             |
| `FR-REPOREVIEW-010` | MUST  | Every non-nil provider response reports actual model-attributed prompt, completion, cached, and total token usage, including fallbacks and structured repairs. At every Start, Resume, or Restart the server refreshes conservative prices for the selected account/model route and stores only numeric per-alias accounting snapshots on the automation. The controller durably aggregates actual usage and estimated USD. At task pickup it atomically includes all in-flight projected prompt/output tokens and price-known cost reservations in `spent.tokens.*` and `spend.total.*`; completion releases the reservation while provider-reported usage remains. Unknown price makes monetary guard fields unknown rather than zero.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Concurrent workers must not all pass the same guard from stale counters, and accounting must remain honest without editable price metadata or reset controls.                                                                 |
| `FR-REPOREVIEW-011` | MUST  | The task-admission guard is an optional, bounded, JQL-like boolean expression with case-insensitive `AND`, `OR`, `NOT`, parentheses, numeric/boolean/string literals, and `=`, `==`, `!=`, `<`, `<=`, `>`, `>=`. Its only variable families are `account.limits.*`, `spent.tokens.*`, and `spend.total.*`. It is parsed at profile mutation/load and evaluated exactly once after a managed worker claims its next task but before provider dispatch. Account-limit fields come only from the profile's selected account (router members aggregate conservatively). A false, unknown, malformed, or telemetry-error result denies that task and latches a safe `guard_expression` pause; already admitted tasks may finish. There is no account-target list, background quota polling interval, or automatic guard recovery. The profile editor autocompletes valid fields (including common and current selected-account windows), operators, keywords, and literals at the caret; its full expression reference opens from the help control beside the field label.                                                                                                                                                                                                                                                                                                                                             | One expression can combine account health, real consumption, and cost while preserving a clear execution-account boundary and deterministic fail-closed admission.                                                            |
| `FR-REPOREVIEW-012` | MUST  | The profile UI presents safe reviewer aliases and execution-account routes but exposes no model-price fields or price override controls. Account-router availability expands every reachable concrete or credential-backed member instead of treating the router ID as a concrete account. Individually selectable credential accounts expose execution availability separately from limit-telemetry status: invalid or expired credentials are disabled, while a telemetry-only error does not disable an otherwise executable credential. The UI fails closed unless an alias is globally safe and explicitly compatible with the selected available account, preserves a compatible model when the account changes, selects the first safe replacement otherwise, invalidates cached options after a failed refresh, and explains globally blocked, account-incompatible, stale, missing, and empty option states. Pricing authority remains in central model/account configuration. Runtime retains request/failure counts, actual tokens, estimated USD, compact approximate reviewed-file coverage, and finding yield without claiming unknown price as zero. Comparative model testing, scoring, context-size ceilings, and cached-weighted token efficiency remain exclusively in the separate model-review probe domain. That domain may list compatible safe profiles and snapshot one at Run, but it never starts or mutates an actual review, profile assignment, or finding ledger. Agentic CLI providers remain unavailable for immutable repository review. | Operators need honest centrally governed actual-run economics without conflating exploratory model comparison with production findings. |
| `FR-REPOREVIEW-013` | MUST  | On launcher startup, the durable controller reconciles an automation left running without a local executor, marks the orphaned workflow run canceled, and records a `service_restart` pause. Resume is explicit and re-evaluates the task guard at the next worker pickup. Controller shutdown cancels in-process execution and never creates a new batch after its context is closed.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Process restarts must not leave phantom running state, duplicate work, or an implicit polling/restart loop.                                                                                                                   |
| `FR-REPOREVIEW-014` | MUST  | Each review profile persists a bounded scope policy with one or more selectable inventory code types (`hotpath-code`, `code`, `test`, `bench-test`), canonical repository-relative include and exclude folder prefixes, and optional free-text guidance. The default selects normal production code (`hotpath-code` and `code`); exclusions always win. A generated repository preflight is commit-bound and persists bounded policy/plan hashes, summary, rationale, warnings, and aggregate file counts. Changing the assigned profile or its execution scope clears any prior commit-bound plan.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Operators must be able to express reusable, reproducible review intent without allowing unsafe paths, unbounded manifests, stale commit summaries, or an exclusion to be silently re-included.                                |
| `FR-REPOREVIEW-015` | MUST  | Repository configuration accepts only an optional branch name. Blank resolves through the acquired repository's advertised default branch; `HEAD`, commit hashes, tags, full refs, revision expressions, URLs, query/fragment forms, and invalid Git branch names are rejected before admission. The launcher exposes separate Review runs, Repositories, Profiles, and Results destinations. Basic profile identity, execution account, reviewer, focus, and Scope are immediately visible; account precedes reviewer because it constrains model availability. Sizing and task-admission guard remain in Advanced, collapsed by default, and preserve their draft while closed. New profiles default to eight configurable parallel review workers; adding a guard does not silently serialize them because admission uses atomic in-flight reservations. Internal output-token estimation is server-owned and absent from profile/API controls.                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Repository review should follow a predictable branch, keep core review intent visible, never masquerade an immutable commit/tag as mutable configuration, and avoid one overloaded control surface.                           |
| `FR-REPOREVIEW-016` | MUST  | The executor supplies a fixed diagnosis-only system policy that profile focus, scope guidance, repository content, candidate output, and other user-controlled text cannot override. Model output contains only a factual summary, exact reviewed paths, confirmed findings, and evidence limitations. Each finding contains severity, title, smallest stable symbol, exact path, optional line, factual failure mechanism/trigger, source-grounded evidence, observable impact, and validation already performed. It never supplies or implies a fix, recommendation, remediation, mitigation, workaround, patch, replacement code, design/configuration/test change, or next-step advice. The structured schema and native decoder reject extra fields, including legacy `recommendation` and top-level `tests`; new discussion and issue-draft projections contain diagnosis only. Model probes apply the same policy, and diagnostic utility means locatable, reproducible, verifiable, and prioritizable without rewarding remediation.                                                                                                                                                                                                                                                                                                                                                                      | Finding quality must be comparable independently from a model's ability to invent patches, and editable focus text must never weaken the reporting boundary.                                                                  |

## Data And State Model

| State                        | Shape And Location                                                          | Contract                                                                                                                                                                                                                                          |
| ---------------------------- | --------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Review profile               | `workspace/repository_reviews/profile_rrpf_*.json`                          | Reusable versioned `0600` behavior, one reviewer alias, optional execution account, scope, sizing, and one guard expression without repository or lifecycle state.                                                                                |
| Repository configuration/run | `workspace/repository_reviews/automation_rra_*.json`                        | Unique normalized repository/profile assignment, optional branch, materialized profile/account version, lifecycle, cumulative usage/cost, bounded run history, latest selected-account limit snapshots, and internal approximate coverage sketch. |
| Repository ledger            | `workspace/repository_reviews/repo_*.json` plus summary sidecar             | Exact commit/blob checkpoints, attempts, unsupported files, findings, observations, contexts, completed review runs, and issue drafts.                                                                                                            |
| Workflow run                 | `workspace/workflow_runs/<run-id>/`                                         | Generic durable job/step state and events; automation stores only bounded identities/progress and requires a verified `record` output or authoritative no-op checkpoint.                                                                          |
| Controller lease             | `workspace/repository_reviews.controller.lock`                              | Non-blocking workspace-wide OS lock held for the launcher controller lifetime; never returned by an API.                                                                                                                                          |
| Task guard                   | `budget.guard_expression`, `usage`, `estimated_cost_usd`                    | Resume preserves counters; Restart starts a new campaign epoch. In-flight reservations are process-local because their provider tasks cannot survive a launcher restart. Legacy threshold fields migrate to the expression on durable load.       |
| Actual-model statistics      | `model_stats`, `model_coverage_sketches`                                    | Per-campaign request/failure/usage/cost/latency/finding counts and fixed-size approximate unique coverage for the assigned profile model. Sketches are persistence-only and removed from API projections.                                         |
| Scope policy and preflight   | `scope_policy` in the review profile and `scope_plan` in the repository run | Reusable code-type/folder/free-text intent plus the latest bounded commit SHA, policy hash, plan hash, explanation, warnings, and counts. It does not contain the selected-file manifest.                                                         |

## Diagnosis-Only Finding Contract

The model returns this exact shape for a confirmed finding. `line` is optional
when the supplied evidence cannot support one; every other field is required.

```json
{
  "severity": "high",
  "title": "Concurrent Save can silently lose a committed update",
  "symbol": "Save",
  "file": "service.go",
  "line": 83,
  "message": "When two callers enter Save from the same stored version, each can complete successfully while the later write replaces the earlier update.",
  "evidence": "Save reads the current version before serialization, and the final write path has no version check. Both executions can derive version N and commit different values as N+1.",
  "impact": "A caller receives success even though its committed state can disappear, causing silent data loss under concurrent writes.",
  "validation": {
    "status": "confirmed",
    "summary": "The two-writer interleaving was traced through the read and commit paths, and no later conflict check was present in the assigned evidence.",
    "checks": [
      "Both calls can observe the same starting version",
      "Both final writes accept state derived from that version",
      "No surrounding lock serializes the complete read-modify-write sequence"
    ]
  }
}
```

The server, not the model, adds finding IDs, commit/blob identity, model and
reviewer identity, consensus, context IDs, status, versions, and timestamps.
`validation.checks` records analysis already performed; it is not a test plan.
`residualRisks` contains only unavailable or unread evidence. A review with no
confirmed defect returns an empty `findings` array.

## Task-Admission Guard Contract

An empty expression permits every task. A non-empty expression must evaluate to
the boolean value `true`; `false` or a final unknown value pauses admission.
Expressions are limited to 4,096 bytes, 256 tokens, and 16 nesting levels.
`*` documents a field family and is not literal wildcard syntax.

Available fields:

- `spent.tokens.prompt`, `.completion`, `.cached`, `.total`
- `spend.total.usd`
- `account.limits.known`, `.exhausted_known`, `.exhausted`, `.any`
- `account.limits.any.known`, `.observed`, `.remaining_percent`,
  `.used_percent`, `.minimum_remaining_percent`, `.maximum_used_percent`
- `account.limits.<window>.known`, `.observed`, `.remaining_percent`,
  `.used_percent`, `.minimum_remaining_percent`, `.maximum_used_percent`, where
  common window names normalize to values such as `daily` and `weekly`

For example:

```text
account.limits.weekly.known and
account.limits.weekly.remaining_percent >= 10 and
spent.tokens.total < 500000 and
spend.total.usd < 25
```

The expression is evaluated only for the profile's execution account. For an
account router, reachable concrete members aggregate conservatively: minimum
remaining percentage and maximum used percentage. A missing price makes
`spend.total.usd` unknown; missing or partial limit telemetry makes affected
numeric limit fields unknown. `.known` fields let a profile state its intended
policy explicitly without a separate fail-open/fail-closed toggle.

Legacy threshold fields are durably rewritten into a bounded expression on
load. Legacy account IDs selected telemetry rather than execution credentials,
so any account-targeted legacy policy migrates fail-closed until an operator
selects the single execution account required by the new contract.

## Surface Ownership

<!-- prettier-ignore -->
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

| Type         | Surface                                                                                                                  | Contract                                                                                                                                                                                                                                                                                                          | Requirement IDs                                                                    |
| ------------ | ------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| HTTP         | `GET/POST/PATCH/DELETE /api/repository-reviews/profiles*`                                                                | List/create/update/delete reusable CAS-fenced single-model profiles, including one execution account, bounded scope/sizing, and one guard expression, but no caller-supplied pricing or polling controls. Assigned deletion and active-assignment mutation fail closed.                                           | `FR-REPOREVIEW-008`, `FR-REPOREVIEW-011`, `FR-REPOREVIEW-012`, `FR-REPOREVIEW-014` |
| HTTP         | `GET/POST/PATCH/DELETE /api/repository-reviews/automations*`                                                             | List/create/update/delete unique repository/profile assignments and invoke start, pause, resume, or restart transitions. Internal sketches and credentials are never projected; bounded scope preflight summaries are returned.                                                                                   | `FR-REPOREVIEW-009`, `FR-REPOREVIEW-013`, `FR-REPOREVIEW-015`                      |
| HTTP         | `GET /api/repository-reviews/automation-options`                                                                         | Return safe configured aliases and selectable runtime account refs with bounded current limit summaries and explicit execution availability, never credential material. Empty profile `account_ref` remains the explicit Default account option.                                                                  | `FR-REPOREVIEW-011`, `FR-REPOREVIEW-012`                                           |
| HTTP         | `GET/PATCH/POST /api/repository-reviews/**`                                                                              | Page result ledgers and mutate finding status or issue drafts with exact version fences.                                                                                                                                                                                                                          | `FR-REPOREVIEW-001`–`FR-REPOREVIEW-007`                                            |
| Gateway HTTP | `/runtime/repository-reviews/<repo>/issue-drafts/<draft>/publish`                                                        | Protected, idempotent publication/reconciliation boundary for canonical GitHub identities.                                                                                                                                                                                                                        | `FR-REPOREVIEW-006`                                                                |
| UI           | `/repository-reviews`, `/repository-reviews/repositories`, `/repository-reviews/profiles`, `/repository-reviews/results` | Separate actual-run, one-profile-per-repository assignment, reusable profile, and completed finding/draft flows. Execution account and compatible reviewer selection stay visible; sizing and task-admission controls are hidden under Advanced by default. Polling is observation only, not execution authority. | `FR-REPOREVIEW-003`–`FR-REPOREVIEW-005`, `FR-REPOREVIEW-008`–`FR-REPOREVIEW-015`   |

## Algorithms And Ordering

1. Profile create/update normalizes one reviewer, work bounds, scope,
   and guard policy under the shared review-store lock. Code types are
   canonicalized; folder prefixes must be exact safe repository-relative paths.
   CAS mismatch fails without partial mutation.
2. Repository configuration create/update normalizes repository identity and an
   optional branch under the catalog lock, atomically rejects a second
   configuration for the same repository, and binds one existing profile.
3. Start/resume/restart checks the action-specific source state, controller
   lifecycle, workspace lease, current configuration, assigned profile version,
   selected account/model compatibility, and central pricing required by any
   `spend.total.*` field. A changed profile is materialized and starts a new
   campaign before admission. Resume preserves cumulative counters; Restart
   clears the campaign counters and progress.
4. Admission creates a run ID and persists `running` plus run history before a
   goroutine executes the built-in repository-bug-finder workflow with the exact
   captured profile and repository branch. A named branch is passed as
   `refs/heads/<branch>`; blank uses the repository's remote default.
5. The workflow acquires a fresh checkout, inventories exact Git blobs, plans
   only changed/profile/account-invalidated work, freezes bounded source
   evidence, and releases the checkout before model calls. Scope planning,
   managed reviewers, fallbacks, and structured repairs all use the profile's
   frozen effective account.
6. When a worker dequeues one managed child, the controller serializes only the
   admission decision, adds that child's projected prompt/output tokens and
   known cost to current in-flight reservations, refreshes referenced limit
   telemetry for the selected account, and evaluates the expression. A denial
   latches a safe stop and prevents new children; already admitted children may
   finish. Every provider response records requested reviewer, actual model,
   actual token usage, cost, and latency. Completion releases the projection;
   accounting persistence failure fails closed.
7. A completed batch counts only after the qualified record step persisted a
   run or the authoritative no-op result was verified. The controller then
   merges campaign-scoped ledger outcomes and either completes, safely pauses,
   or atomically admits the next bounded batch.
8. The monitor only reconciles orphaned active runs after launcher failure; it
   does not poll account limits or auto-resume a guard pause. Explicit Resume
   causes the next worker pickup to fetch current telemetry and evaluate again.
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
  read-only profile materialization, frozen comparison corpus, deterministic
  provider-call sizing sweep, blinded judging, and quality/efficiency ranking.
  A probe freezes the admitted profile version and never changes a review
  profile, assignment, repository run, or finding ledger.
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
  unavailable account/model pairs, unsafe agentic CLI reviewers, invalid
  prices required by a monetary guard, and out-of-range work/guard values fail
  before execution.
- Scope policies reject unknown/duplicate code types, absolute, parent-relative,
  non-canonical, duplicate, or over-limit folder prefixes, and oversized free
  text. Include folders narrow category matches and excludes always win. The
  commit-bound summary is invalidated when repository/branch/profile/account/scope changes.
- Branch configuration rejects detached commit or tag targets and every unsafe
  or ambiguous ref form. Internal workflow checkpoints may still reacquire the
  exact commit resolved from the admitted branch.
- A guard that references `spend.total.usd` requires a positive central price
  for every reachable selected-account route. Unknown price is unknown, never
  zero/free.
- Parallel workers remain configurable up to 64. Prompt/output/cost projections
  are reserved at task pickup, so later workers see admitted in-flight work
  before evaluating the same expression. Provider-reported usage remains after
  a task releases its projection.
- Unknown or partial selected-account telemetry makes affected numeric fields
  unknown; final unknown denies work. Known `limit_reached`/exhausted entries
  normalize to zero remaining. Multiple entries in one window aggregate to the
  most conservative value.
- Manual and guard pause intent survives a launcher restart and is never
  auto-resumed. Orphaned running work becomes `service_restart`; explicit
  Resume re-enters through the same task-admission boundary.
- A workflow that reports success without a verified durable record/no-op
  checkpoint becomes `failed`, so incomplete work is never counted as a batch.
- Accepted findings/coverage are scoped to the automation campaign. Approximate
  model coverage is monotonic and explicitly labeled; internal sketches are
  excluded from frequently polled API responses.
- Publication transport ambiguity becomes `unknown`; retries reconcile the
  stable marker and never blindly create a duplicate issue.

## Acceptance Evidence

| Requirement IDs     | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-REPOREVIEW-001` | [pkg/repoaudit/store_test.go](../../pkg/repoaudit/store_test.go), [pkg/repoaudit/ensemble_test.go](../../pkg/repoaudit/ensemble_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `FR-REPOREVIEW-002` | [web/backend/api/repository_reviews_test.go](../../web/backend/api/repository_reviews_test.go), [web/frontend/src/api/repository-reviews.test.ts](../../web/frontend/src/api/repository-reviews.test.ts)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `FR-REPOREVIEW-003` | [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `FR-REPOREVIEW-004` | [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx), [web/frontend/src/components/repository-reviews/repository-review-actions.ts](../../web/frontend/src/components/repository-reviews/repository-review-actions.ts)                                                                                                                                                                                                                                                                                                                                                         |
| `FR-REPOREVIEW-005` | [pkg/repoaudit/ensemble_test.go](../../pkg/repoaudit/ensemble_test.go), [web/backend/api/repository_reviews_test.go](../../web/backend/api/repository_reviews_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `FR-REPOREVIEW-006` | [pkg/gateway/repository_review_publication_test.go](../../pkg/gateway/repository_review_publication_test.go), [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx)                                                                                                                                                                                                                                                                                                                                                                                                             |
| `FR-REPOREVIEW-007` | [pkg/repoaudit/store_test.go](../../pkg/repoaudit/store_test.go), [pkg/repoaudit/ensemble_test.go](../../pkg/repoaudit/ensemble_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `FR-REPOREVIEW-008` | [pkg/repoaudit/profile_test.go](../../pkg/repoaudit/profile_test.go), [pkg/repoaudit/control_test.go](../../pkg/repoaudit/control_test.go), [web/backend/api/repository_review_profiles_test.go](../../web/backend/api/repository_review_profiles_test.go), [web/frontend/src/api/repository-reviews.test.ts](../../web/frontend/src/api/repository-reviews.test.ts), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx)                                                                                                                                     |
| `FR-REPOREVIEW-009` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/components/repository-reviews/repository-review-runs-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-runs-page.test.tsx), [web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx)                                                                                                                                                                                           |
| `FR-REPOREVIEW-010` | [pkg/agent/workflow_managed_ensemble_test.go](../../pkg/agent/workflow_managed_ensemble_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go)                                                                                                                                                                                                                                                                                                                                                                                             |
| `FR-REPOREVIEW-011` | [pkg/repoaudit/guard_expression_test.go](../../pkg/repoaudit/guard_expression_test.go), [pkg/repoaudit/guard_migration_test.go](../../pkg/repoaudit/guard_migration_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx)                                                                                                   |
| `FR-REPOREVIEW-012` | [pkg/providers/cli/github_copilot_provider_test.go](../../pkg/providers/cli/github_copilot_provider_test.go), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx), [web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx](../../web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx)                                                                                                                                                                                                                       |
| `FR-REPOREVIEW-013` | [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [pkg/repoaudit/control_test.go](../../pkg/repoaudit/control_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `FR-REPOREVIEW-014` | [pkg/repoaudit/scope_policy_test.go](../../pkg/repoaudit/scope_policy_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/api/repository-reviews.test.ts](../../web/frontend/src/api/repository-reviews.test.ts), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx)                                                                                                                                                                                           |
| `FR-REPOREVIEW-015` | [pkg/repoaudit/profile_test.go](../../pkg/repoaudit/profile_test.go), [web/backend/api/repository_review_automations_test.go](../../web/backend/api/repository_review_automations_test.go), [web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-profiles-page.test.tsx), [web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-review-repositories-page.test.tsx), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx) |
| `FR-REPOREVIEW-016` | [pkg/workflows/repository_bug_finder_workflow_test.go](../../pkg/workflows/repository_bug_finder_workflow_test.go), [pkg/workflows/repository_review_native_test.go](../../pkg/workflows/repository_review_native_test.go), [pkg/workflows/repository_model_evaluation_workflows_test.go](../../pkg/workflows/repository_model_evaluation_workflows_test.go), [pkg/repoaudit/store_test.go](../../pkg/repoaudit/store_test.go), [web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx](../../web/frontend/src/components/repository-reviews/repository-reviews-page.test.tsx)                                                                                           |

## Implementation Anchors

- [pkg/repoaudit/control.go](../../pkg/repoaudit/control.go)
- [pkg/repoaudit/guard_expression.go](../../pkg/repoaudit/guard_expression.go)
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
