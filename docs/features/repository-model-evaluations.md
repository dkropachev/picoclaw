# Repository Model Evaluations

## Feature ID

`FR-REPOEVAL`

## Behavior Summary

Repository model evaluations are launcher-managed model review probes separate
from actual bug finding. An operator chooses a repository, one reusable review
profile, and two to eight candidate model aliases, then launches the entire
probe with one Run action. Candidate models are the experiment variable; the
server resolves and freezes the selected profile version, effective account,
review focus, hard scope, reviewer, concurrency, and work-sizing maxima as the
experiment control. The controller resolves the exact commit and builds one
capped representative corpus spanning every eligible programming language and
multiple codebase regions before continuing automatically through a
deterministic files-per-batch and content-bytes-per-batch sweep. The selector
uses AI to rank safe immutable candidate IDs while native validation enforces
language representation, region diversity, exact Git blob identity, and every
hard scope constraint. Every candidate receives the same frozen corpus, sizing
plan, prompt, and rubric. A server-selected judge evaluates blinded outputs,
and the launcher persists progress, requested and observed provider-call sizing,
score statistics, objective usage, cached-weighted token efficiency, and an
explicitly AI-judged comparison report that survives process restarts.

## Reconstruction Notes

- Similarity target: rebuild a durable repository corpus benchmark, not a
  special mode of the repository finding ledger and not a browser-only prompt
  wrapper.
- Core components: `reposcope` classification/selection, `repoeval` durable
  state, the repository evaluation workflow/native functions, the launcher
  controller/API, and `/model-evaluations`.
- Ordering: resolve and snapshot the profile; derive the deterministic sizing
  plan; resolve ref to commit; inventory and classify; release checkout; obtain
  a bounded AI selection over opaque candidate IDs; validate and persist one
  capped representative corpus; for every sizing point reacquire only that
  commit, reclassify and verify only the selected path/blob refs, form the real
  provider-call groups under both requested ceilings, freeze two one-shot copies,
  and release before every model call; run all candidate aliases over one copy;
  blind outputs; judge against the other identical copy; aggregate requested and
  observed sizing, scores, and usage; analyze bounded summaries; persist the
  final report.
- Trust boundary: repository paths/content and every model output are untrusted
  data. AI may rank or narrow safe candidates but may never invent a path,
  widen structured scope, alter a blob identity, or select beyond a quota.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-REPOEVAL-001` | MUST | Model evaluation has its own durable domain, API, controller lifecycle, navigation route, and state directory; it does not create repository findings, discussions, issue drafts, or publication state. | Comparing models and finding repository bugs have different lifecycle and result semantics. |
| `FR-REPOEVAL-002` | MUST | Create, update, and Run require a repository, a reusable repository-review `profile_id`, two to eight candidate aliases, and an optional ref; a blank ref normalizes to `HEAD`. The server loads the latest profile at admission, resolves its effective account, requires the candidate experiment to contain the profile reviewer plus at least one other compatible model, uses the profile reviewer as selector, and selects a compatible judge while preferring an alias outside the candidate set. Public mutations reject a missing profile and caller-supplied focus, per-language quota, selector, or judge fields. Existing durable legacy evaluations remain readable/recoverable but no public mutation can create another custom probe. Exact `owner/repository` shorthand normalizes to its canonical GitHub clone URL, only compatible safe registered Git Workspace remotes are offered as repository choices, and an absolute local repository must resolve to the root of a Git repository before launch. Run, failed retry/start-over, and every sizing point use the same canonical repository resolution. Mutations use expected-version CAS, cross-process locking, atomic `0600` files, bounded fields, and symlink-safe storage. | A probe should compare candidate models under one reusable production-like policy without silently mixing caller-specific scope or model-role controls into the experiment. |
| `FR-REPOEVAL-003` | MUST | Preflight resolves the requested ref once through a fresh Git Workspace acquisition, binds an exact commit and full inventory hash, classifies every safe eligible tracked source file by language, code type, module, and deterministic codebase region, and excludes non-regular, binary, generated, vendored, build, lock, LFS-pointer, unsafe-path, and oversized inputs. The acquired checkout is released before selection or model execution. | A reproducible benchmark cannot depend on a moving branch, mutable checkout, raw launcher working-directory paths, or unreviewable bytes. |
| `FR-REPOEVAL-004` | MUST | Every detected eligible programming language is represented under the profile's frozen scope. Selection targets at most 20 files per language, retains sparse languages and labels them limited, then deterministically caps a sizing-probe corpus above 128 files by round-robin language representation. The exact resulting path/blob corpus is persisted once and reused unchanged for every model and every work-sizing point; a sweep never resamples easier or smaller source to satisfy a requested batch shape. | Quality changes are attributable to provider-call work sizing only when corpus composition does not change between points. |
| `FR-REPOEVAL-005` | MUST | Candidate selection spreads files across modules/regions before repeating a region and prefers enough substantive source bytes when feasible. AI ranks only opaque commit-bound candidate IDs from a bounded sanitized catalog; native validation rejects unknown, duplicate, stale, wrong-language, out-of-scope, or over-quota IDs and deterministically fills safe omissions. | AI should improve representativeness without becoming path or scope authority. |
| `FR-REPOEVAL-006` | MUST | The Run action atomically establishes one durable probe identity and freezes repository, ref, candidate order, the selected profile's ID/version/name, effective account, reviewer, review focus, hard scope, files/content ceilings, parallelism, the derived work-sizing plan, rubric, and eventual corpus against later profile/configuration mutation or deletion. Actual-review-only profile fields such as force mode, automatic continuation, and task-admission guard do not acquire probe authority. Ready is an internal recovery checkpoint and automatically advances to execution without another operator action. A complete fully judged `(sizing point, batch)` is the durable recovery boundary and is skipped on failed Restart. If its checkpoint contains missing candidate work, recovery discards that partial judged evidence and reruns/rejudges the complete original batch rather than mixing scores produced with different peer context. Failed Start over creates a fresh identity with the same frozen profile snapshot and plan. A completed probe owns exactly one final comparison result and permits neither retry, start-over, configuration mutation, nor deletion. | Results from different profile versions, attempts, models, sizing plans, corpora, or judge contexts must never be merged or rewritten. |
| `FR-REPOEVAL-007` | MUST | A profile-driven probe derives a deterministic one-dimensional sizing plan from the configured files-per-batch and content-bytes-per-batch maxima: deduplicated `ceil(max/4)` and `ceil(max/2)` file points hold content bytes at the configured maximum; the corresponding content-byte points hold files at the configured maximum; and one shared configured-baseline point records both maxima and serves as the terminal point of both axis series without being executed twice. Every point traverses the same ordered capped corpus. Controller batches and frozen related-file groups obey both requested ceilings, except that one individually reviewable file may stand alone when its bytes exceed the requested group ceiling. Managed chunking uses those frozen groups directly, so requested files/content describe real candidate provider calls rather than an outer controller partition; observed group counts/bytes are recorded from actual attempts. Every candidate alias receives identical groups, prompt, schema, tool-free immutable evidence, effort, and output limits. Candidate calls execute under the profile's bounded parallelism with a per-reviewer cap of one while results, failure selection, blinding, and judging remain deterministic in declared plan order; the judge waits for the full candidate set and candidate order rotates across batches. Each execution batch reacquires the canonical repository and exact commit through a fresh Git Workspace, validates only its persisted candidates by rebuilding classification from bounded exact blobs and matching commit, inventory, path, mode, blob, size, ID, and hard scope, and does not rescan the full repository. Candidate and judge consume distinct one-shot capabilities over independently cloned copies of one frozen snapshot. The Git Workspace is released before provider calls and an unavailable pinned commit pauses/fails instead of substituting a newer ref. Built-in probe workflows use a capped workload-sized minimum deadline that reserves time for candidate tasks, the judge, durable checkpointing, and cancellation instead of inheriting the shorter generic workflow default; an explicitly higher operator timeout remains authoritative. | The experiment must vary only one requested context-size dimension at a time and must measure the grouping the provider actually received. |
| `FR-REPOEVAL-008` | MUST | Candidate outputs are blinded before judging. For every model and sizing point the judge scores correctness, evidence, coverage, actionability, and overall quality. Native aggregation reports each dimension's completed batch-sample count, completed-file-weighted mean, minimum, maximum, and population standard deviation, together with supported/unsupported claims, attempts/successes/failures, analyzed files/bytes, point-specific concrete-model usage, and observed provider-call file/content min/mean/max. The analyzer consumes only bounded judge summaries and objective runtime statistics. Scores are labeled AI judged and judge/candidate overlap produces a visible bias warning. A per-model axis ceiling orders that axis's reduced points plus the shared configured-baseline point by the maximum workload actually observed. It uses only complete points with a judged overall score and a nonzero observation that did not exceed the requested ceiling; incomplete, failed, unscored, empty, and oversized-single-file points are shown but excluded. The smallest eligible observed workload is the analytical score baseline. The first larger eligible observation whose overall weighted mean is at least 5.0 points below that baseline establishes the preceding observed value as the ceiling; if no such drop occurs, the ceiling is reported as at least the largest eligible observed value, and with no eligible observation it is not established. | Sparse or underfilled batches must report the context actually delivered instead of manufacturing a requested-capacity ceiling. |
| `FR-REPOEVAL-009` | MUST | Completed comparison results appear both in the run's final **Final report** tab and at the dedicated deep-linkable `/model-evaluations/{evaluation_id}/report` route. The report leads with a deterministic recommendation drawn only from fully completed scored models and an exact faster-alternative tradeoff; renders fixed-scale quality bars, a desktop quality-versus-cumulative-model-time scatter with a readable mobile bar alternative, corpus composition and AI-judge claim-assessment donuts; presents per-model file/content degradation ceilings and exact work-sizing tables with requested values, observed provider-call min/mean/max, all score statistics, samples/outcomes, analyzed files/bytes, supported/unsupported claims, and token efficiency; and presents responsive ranked model cards with alias, concrete model distribution, completion/failure, files/languages/regions/bytes, exact supported and unsupported claim counts, every score dimension, verdict, strengths/limitations, requests, failures, tokens, cumulative provider time, and known estimated cost. Effective tokens equal `(input_tokens - cached_input_tokens) + 0.1 * cached_input_tokens + output_tokens`, with cached input clamped to input; effective tokens per KiB equal `effective_tokens * 1024 / bytes_analyzed` and are unavailable when analyzed bytes are zero. Reasoning tokens are reported separately and are not added again because providers include them in output usage. Exact textual values accompany every chart, missing values remain unavailable rather than zero, legacy results without sizing observations or an exact `unsupported_claims` value receive neither a ceiling nor a claim ratio, judge/candidate alias overlap is visible, and no time series is invented from aggregate data. | Operators need exact quality, context-size, and cached-weighted economic evidence to find where each model's review quality begins to degrade. |
| `FR-REPOEVAL-010` | MUST | The API/UI expose one asynchronous Run action, Cancel while active, failed-only Restart and Start over, list/detail, paged corpus preview, live stage/counters, usage, warnings, run history, an inline final report, and a dedicated completed-report deep link. The ordinary flow keeps repository/profile/candidate/ref configuration client-side until Run, then automatically performs profile materialization, preflight, every sizing point's candidate execution and judging, and final analysis from start to finish. After launch every configuration control is frozen. Completed probes are results-only; failed probes alone expose Restart for same-ID checkpoint recovery and Start over for a fresh evaluation. The page labels the workload a model review probe and states that it does not start an actual repository review or write findings. Repository, profile, candidate experiment, and optional ref are the only setup controls; the frozen profile card explains inherited scope, reviewer, account, parallelism, and sizing. Each selected run separates interim/result information into accessible keyboard tabs in this order: **Status**, **Corpus by language**, **Corpus preview**, and **Final report**. Tabs remain unavailable until their durable data exists; Status owns live progress and warnings, the two corpus tabs own their respective language summary and safe paged references, and Final report renders the completed report plus its dedicated-link action. Active polling, including the internal ready checkpoint, observes durable backend state; browser timers do not own execution. | Long probes need distinct stable information surfaces instead of interleaving warnings, corpus tables, previews, and final analysis in one scrolling stream. |
| `FR-REPOEVAL-011` | MUST | Progress is projected from actual backend workflow-step and managed-child dispatch. It distinguishes resolving, inventorying, classifying, selecting, validating, candidate batch execution, judging, analyzing, completion, cancellation, and failure; records current/total batch and completed/total/failed candidate calls; and exposes a bounded active-child list containing only index, safe label, model alias, scope count, and start time. Child completion advances progress exactly once independently of provider retries or structured-output repair calls. Judge, failure, cancellation, and launcher recovery clear stale active children. The judge alias is shown while one exact judge call is active, alongside per-language available/selected files, bytes, regions, and limited status. | A long evaluation must show real work rather than mock timer stages, an indeterminate spinner, or a stale model/file label. |
| `FR-REPOEVAL-012` | MUST | Ordinary API responses omit repository source content, credentials, absolute checkout/artifact paths, internal capabilities, and raw provider payloads. Persisted corpus entries contain only exact references and compact bounded results. | Durable comparison state must not become a source or credential exfiltration surface. |

| `FR-REPOEVAL-013` | MUST | Model Evaluation inventory accepts the shared bounded `query`, opaque `cursor`, and `limit` contract and returns summary-only rows with `total`, `next_cursor`, `canonical_query`, and a typed allowlisted `query_schema`; direct item lookup does not depend on a loaded list. Bulk delete accepts at most 200 explicitly selected IDs with per-item versions, holds the catalog lock, deletes only version-matching drafts, and reports `deleted_ids` plus stable failures while active, completed, stale, unknown, or duplicate selections remain safe and selected. The UI uses the standard List/Table/Grid collection at `/model-evaluations`, dedicated `/new`, `/{id}`, and draft-only `/{id}/edit` routes, and item-owned `/{id}/languages`, `/{id}/corpus`, and `/{id}/report` routes. Active polling and lifecycle actions remain on detail, route-scoped query canonicalization cannot interrupt navigation away from inventory, the report path stays canonical, Back restores collection state, and legacy `?probe=` selection is not interpreted. | Evaluation configuration, lifecycle, corpus, and results must have stable deep links without combining creation, inventory, and one selected workspace or allowing completed evidence to be deleted. |

## Data And State Model

Evaluation collection identity is immutable `id`; each deletion fence is the
item `version`. Query fields are sortable `id`, `status`, `repository`, `ref`,
`models`, `progress`, `version`, `created`, and `updated`; status suggestions are
the bounded lifecycle states and default ordering remains deterministic with
stable ID. Bulk failure codes are `invalid_id`, `duplicate_id`, `not_found`,
`stale_version`, `not_draft`, and `delete_failed`; only version-matching drafts
are removed while the catalog lock is held.

| State | Shape And Location | Contract |
| --- | --- | --- |
| Evaluation | `workspace/repository_evaluations/evaluation_rme_*.json` | Versioned repository/ref/candidate configuration, frozen profile snapshot and work-sizing plan, lifecycle, corpus references, per-point candidate usage and statistics, progress, aggregate usage/comparison, warnings, and run IDs; mode `0600`. |
| Corpus manifest | Commit/inventory/policy/rubric hashes plus selected path/blob/size/language/type/module/region/chunk IDs | No source content; becomes immutable when execution starts. |
| Work-sizing observations | Per point/model requested files/bytes, completion, observed provider-call file/content min/mean/max, batch score statistics, claims, analyzed scope, point-specific concrete models, candidate-only usage, effective tokens, and effective tokens/KiB within the evaluation | Bounded native aggregate derived from durable judged checkpoints; sparse batches retain their actual observed maxima and ineligible points remain explicit rather than being converted into ceilings. |
| Workflow runs | `workspace/workflow_runs/<run-id>/` | Durable preflight/evaluation execution and bounded internal artifacts; ordinary evaluation API projects only safe summaries. |
| Active controller state | Process-local cancellation plus workspace controller lease | Reconciles orphaned durable statuses at startup and never creates duplicate task IDs. |

## Surface Ownership

Owns: CODE pkg/reposcope/**
Owns: TEST pkg/reposcope/**
Owns: CODE pkg/repoeval/**
Owns: TEST pkg/repoeval/**
Owns: CODE pkg/workflows/repository_model_evaluation_native.go
Owns: TEST pkg/workflows/repository_model_evaluation_native_test.go *
Owns: CODE web/backend/api/repository_model_evaluation*.go
Owns: TEST web/backend/api/repository_model_evaluation*_test.go
Owns: CODE web/frontend/src/api/model-evaluations.ts
Owns: TEST web/frontend/src/api/model-evaluations.test.ts
Owns: CODE web/frontend/src/components/model-evaluations/**
Owns: CODE web/frontend/src/components/collections/pilots/model-evaluation*
Owns: TEST web/frontend/src/components/model-evaluations/**
Owns: CODE web/frontend/src/routes/model-evaluations*.tsx
Owns: TEST web/frontend/src/routes/-model-evaluations-route.test.tsx
Owns: HTTP * /api/model-evaluations*
Owns: UI /model-evaluations*

Shared executor/model-resolution and built-in workflow registry behavior remains
owned by Workflows and Agent Execution Optimization. Repository Reviews owns
bug-finder scope planning even when both features call `reposcope`.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `/api/model-evaluations*` | Version-fenced profile-driven configuration, safe profile/model/repository options, asynchronous actions, bounded detail/list with sizing observations, and paged corpus references. | `FR-REPOEVAL-001`, `FR-REPOEVAL-002`, `FR-REPOEVAL-008`, `FR-REPOEVAL-010`, `FR-REPOEVAL-012` |
| Workflow | Built-in preflight, batch, and analysis workflows | Resolve/classify/select one capped corpus, execute real provider groups for every deterministic sizing point, blind/judge, observe usage/group sizes, and analyze with durable run IDs. | `FR-REPOEVAL-003`–`FR-REPOEVAL-009` |
| UI | `/model-evaluations`, `/model-evaluations/{evaluation_id}/report` | Profile/candidate setup, one-shot Run, frozen launched state, tabbed status/language/corpus/final results, failed recovery/start-over, requested/observed work-sizing statistics, ceilings, token efficiency, and a dedicated AI-judged report. | `FR-REPOEVAL-004`, `FR-REPOEVAL-008`–`FR-REPOEVAL-011` |

## Cross-Feature Behavior

- Git Workspaces owns safe checkout acquisition/release; evaluations pin a
  commit and consume exact Git blobs without retaining a mutable checkout during
  model calls.
- Workflows owns durable run execution, immutable frozen-scope capabilities,
  built-in templates, real related-file grouping under separate per-file and
  per-provider-group file/content bounds, agent-call usage/child observations,
  and explicit model-alias overrides. Evaluation state stores only bounded run
  identities and derived results.
- Agent Conversations and Account/Model Routing own alias resolution,
  provider fallback, effective profile-account compatibility, and actual
  concrete-model reporting. Evaluation never persists credentials and displays
  routed concrete-model distributions.
- Launcher Management owns authentication, same-origin/replay mutation guards,
  shared navigation, and safe model options.
- Repository Reviews owns reusable profile persistence and actual-review-only
  force/continuation/task-guard behavior. Evaluation reads one profile at Run,
  freezes only its evaluation-relevant policy, and never assigns, mutates, or
  otherwise changes that profile, a repository review, or a finding ledger.

| HTTP/UI | `/api/model-evaluations*`; `/model-evaluations`, `/new`, `/{id}`, `/{id}/edit`, `/{id}/languages`, `/{id}/corpus`, `/{id}/report` | Typed query/cursor summaries, direct detail, version-fenced draft-only explicit-ID bulk delete, and dedicated lifecycle/detail sections. | `FR-REPOEVAL-010`, `FR-REPOEVAL-013` |

## Algorithms And Ordering

1. Normalize repository/ref/profile identity and candidate aliases. Resolve the
   selected profile's latest version and effective account; require its reviewer
   plus another compatible candidate; freeze the evaluation-relevant profile
   fields and choose selector/judge roles server-side. Excludes always win and
   profile free text can only narrow the structured hard boundary.
2. Derive the work-sizing plan from each configured maximum using deduplicated
   ceiling-quarter, ceiling-half, and configured values. Vary files while bytes
   remain configured, vary bytes while files remain configured, and retain one
   shared configured reference point.
3. Preflight pins the ref and inventories the exact Git tree. Native code
   produces a sanitized candidate catalog and aggregate language/region counts.
4. The selector receives no source content or absolute workspace path and
   returns opaque candidate IDs. Native selection validates every ID and fills
   missing language slots round-robin across regions with deterministic path/blob
   tie breaks. If selection exceeds 128 files, cap it round-robin by language and
   persist that exact representative corpus for the complete experiment.
5. Persist the corpus at the internal ready checkpoint, then automatically
   create deterministic `(evaluation, corpus, sizing point, model, group,
   rubric)` tasks and continue execution under the same one-shot controller
   lease.
6. At every point, partition the same ordered corpus under both requested
   ceilings. Reacquire the pinned commit for each bounded group, reclassify only
   its persisted selected blobs, verify exact path/mode/blob/size and hard scope,
   group the frozen evidence with the same file/content maxima used by managed
   dispatch, freeze two one-shot capabilities, and release the checkout.
7. Dispatch those real frozen groups to every candidate, recording actual
   attempt group file/content counts and candidate-only usage. Blind bounded
   candidate outputs, then give the judge the second capability over identical
   evidence. Persist the fully judged point/batch before admitting the next;
   an interrupted in-flight batch is intentionally retried.
8. Aggregate each model/point's completed-file-weighted judge dimensions,
   min/max/population deviation, observed grouping, claims, completion,
   point-specific concrete routing, and cached-weighted token efficiency. Order
   eligible points by their actual observed maximum and apply the 5-point rule;
   never substitute a larger requested maximum for sparse delivered work.
9. Analyze only bounded durable judge summaries, validate structured output,
   and atomically persist the overall comparison and final sizing report.

## Failure And Edge Cases

- Missing/unsafe profiles, profile file maxima outside the probe bound,
  unavailable effective profile accounts/reviewers/judges, candidate sets that
  omit the reviewer or contain fewer than two compatible aliases, profile-driven
  requests with custom scope/quota/model-role fields, empty repositories, no
  eligible source language, more than the bounded language limit, invalid
  folders, unsafe paths, stale commit/inventory IDs, tampered AI IDs, and invalid
  aliases fail before candidate execution.
- A language with fewer requested files selects all eligible files and records a
  limited warning. A tiny language is not padded with documentation or generated
  content merely to meet a byte target.
- Changing a branch after preflight cannot change a selected blob. If the pinned
  commit cannot be reacquired, execution stops with an actionable error.
- Candidate partial failure remains visible in the final table and does not
  manufacture a score. Cancellation checkpoints completed tasks and never marks
  a partial judge/analyzer result complete.
- Sparse or underfilled groups retain their actual observed files/content values,
  so a requested maximum is never presented as though it were delivered.
  Oversized individual reviewable files may produce an observed byte count above
  a smaller requested group ceiling and are excluded from ceiling inference.
  Incomplete, failed, unscored, empty, and oversized points stay visible.
- Probe workflow minimum deadlines scale up to a hard cap from the sequential
  candidate workload and judge tail while preserving an explicitly higher
  configured timeout. A generic short default cannot expire the candidate
  phase and leave no time for judging or checkpointing.
- Raw model output is bounded internal evidence. Invalid structured selection,
  candidate, judge, or analyzer output gets one bounded repair and then fails the
  affected stage explicitly.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-REPOEVAL-001`, `FR-REPOEVAL-002`, `FR-REPOEVAL-006`, `FR-REPOEVAL-010`, `FR-REPOEVAL-012` | [pkg/repoeval/store_test.go](../../pkg/repoeval/store_test.go), [pkg/repoeval/work_sizing_test.go](../../pkg/repoeval/work_sizing_test.go), [web/backend/api/repository_model_evaluations_test.go](../../web/backend/api/repository_model_evaluations_test.go) |
| `FR-REPOEVAL-003`, `FR-REPOEVAL-004`, `FR-REPOEVAL-005` | [pkg/reposcope/candidates_test.go](../../pkg/reposcope/candidates_test.go), [pkg/reposcope/selection_test.go](../../pkg/reposcope/selection_test.go), [pkg/workflows/repository_model_evaluation_native_test.go](../../pkg/workflows/repository_model_evaluation_native_test.go) |
| `FR-REPOEVAL-006`, `FR-REPOEVAL-007`, `FR-REPOEVAL-008`, `FR-REPOEVAL-009` | [pkg/repoeval/work_sizing_test.go](../../pkg/repoeval/work_sizing_test.go), [web/backend/api/repository_model_evaluation_controller_test.go](../../web/backend/api/repository_model_evaluation_controller_test.go), [pkg/workflows/immutable_scope_test.go](../../pkg/workflows/immutable_scope_test.go), [pkg/workflows/repository_model_evaluation_native_test.go](../../pkg/workflows/repository_model_evaluation_native_test.go), [pkg/workflows/repository_model_evaluation_workflows_test.go](../../pkg/workflows/repository_model_evaluation_workflows_test.go), [web/frontend/src/components/model-evaluations/model-evaluation-report-page.test.tsx](../../web/frontend/src/components/model-evaluations/model-evaluation-report-page.test.tsx), [web/frontend/src/routes/-model-evaluations-route.test.tsx](../../web/frontend/src/routes/-model-evaluations-route.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-REPOEVAL-010`, `FR-REPOEVAL-011` | [web/frontend/src/api/model-evaluations.test.ts](../../web/frontend/src/api/model-evaluations.test.ts), [web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx](../../web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

| `FR-REPOEVAL-013` | [web/backend/api/collection_apis_test.go](../../web/backend/api/collection_apis_test.go), [web/backend/api/repository_model_evaluations_test.go](../../web/backend/api/repository_model_evaluations_test.go), [web/backend/api/repository_model_evaluation_controller_test.go](../../web/backend/api/repository_model_evaluation_controller_test.go), [web/frontend/src/api/model-evaluations.test.ts](../../web/frontend/src/api/model-evaluations.test.ts), [web/frontend/tests/collection-visual.spec.ts](../../web/frontend/tests/collection-visual.spec.ts) |

## Implementation Anchors

- [pkg/reposcope](../../pkg/reposcope)
- [pkg/repoeval](../../pkg/repoeval)
- [pkg/workflows/repository_model_evaluation_native.go](../../pkg/workflows/repository_model_evaluation_native.go)
- [web/backend/api/repository_model_evaluations.go](../../web/backend/api/repository_model_evaluations.go)
- [web/backend/api/repository_model_evaluation_controller.go](../../web/backend/api/repository_model_evaluation_controller.go)
- [web/frontend/src/components/model-evaluations](../../web/frontend/src/components/model-evaluations)
