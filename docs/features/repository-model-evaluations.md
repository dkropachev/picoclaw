# Repository Model Evaluations

## Feature ID

`FR-REPOEVAL`

## Behavior Summary

Repository model evaluations are launcher-managed model review probes separate
from actual bug finding. An operator chooses a repository and ref, analyzes the exact resolved
commit, reviews a representative corpus spanning every eligible programming
language and multiple codebase regions, selects two or more model aliases, and
runs a fair comparison. The selector uses AI to rank safe immutable candidate
IDs while native validation enforces language quotas, region diversity, exact
Git blob identity, and all hard scope constraints. Every candidate receives the
same frozen corpus and rubric. A configured judge evaluates blinded outputs,
and the launcher persists progress, objective usage, and an explicitly
AI-judged comparison table that survives process restarts.

## Reconstruction Notes

- Similarity target: rebuild a durable repository corpus benchmark, not a
  special mode of the repository finding ledger and not a browser-only prompt
  wrapper.
- Core components: `reposcope` classification/selection, `repoeval` durable
  state, the repository evaluation workflow/native functions, the launcher
  controller/API, and `/model-evaluations`.
- Ordering: resolve ref to commit; inventory and classify; release checkout;
  obtain a bounded AI selection over opaque candidate IDs; validate and persist
  the corpus; reacquire only that commit; reclassify and verify only the selected
  path/blob refs; freeze two one-shot copies of each bounded batch; release
  before every model call; run all candidate aliases over one copy; blind outputs;
  judge against the other identical copy; analyze bounded summaries; persist the
  final table.
- Trust boundary: repository paths/content and every model output are untrusted
  data. AI may rank or narrow safe candidates but may never invent a path,
  widen structured scope, alter a blob identity, or select beyond a quota.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-REPOEVAL-001` | MUST | Model evaluation has its own durable domain, API, controller lifecycle, navigation route, and state directory; it does not create repository findings, discussions, issue drafts, or publication state. | Comparing models and finding repository bugs have different lifecycle and result semantics. |
| `FR-REPOEVAL-002` | MUST | Create requires a repository, two to eight candidate aliases, a selector alias, a judge alias, and an optional hard scope; a blank optional ref normalizes to `HEAD`. Exact `owner/repository` shorthand normalizes to its canonical GitHub clone URL, only compatible safe registered Git Workspace remotes are offered as repository choices, and an absolute local repository must resolve to the root of a Git repository before it can be saved. Create, patch, restart, legacy retry, and batch execution all use the same canonical repository resolution. Mutations use expected-version CAS, cross-process locking, atomic `0600` files, bounded fields, and symlink-safe storage. | Evaluation configuration and results must survive restarts without concurrent overwrite, stale process-relative paths, or credential disclosure. |
| `FR-REPOEVAL-003` | MUST | Preflight resolves the requested ref once through a fresh Git Workspace acquisition, binds an exact commit and full inventory hash, classifies every safe eligible tracked source file by language, code type, module, and deterministic codebase region, and excludes non-regular, binary, generated, vendored, build, lock, LFS-pointer, unsafe-path, and oversized inputs. The acquired checkout is released before selection or model execution. | A reproducible benchmark cannot depend on a moving branch, mutable checkout, raw launcher working-directory paths, or unreviewable bytes. |
| `FR-REPOEVAL-004` | MUST | Every detected eligible programming language is represented. Each language defaults to at most 20 selected files, the operator may choose a per-language value from 1 through 20, and the effective value is capped by available files. Sparse languages are retained and labeled limited rather than omitted. | The corpus must represent the repository while keeping evaluation cost bounded. |
| `FR-REPOEVAL-005` | MUST | Candidate selection spreads files across modules/regions before repeating a region and prefers enough substantive source bytes when feasible. AI ranks only opaque commit-bound candidate IDs from a bounded sanitized catalog; native validation rejects unknown, duplicate, stale, wrong-language, out-of-scope, or over-quota IDs and deterministically fills safe omissions. | AI should improve representativeness without becoming path or scope authority. |
| `FR-REPOEVAL-006` | MUST | Editing repository, ref, scope, language quotas, candidate aliases, selector/judge alias, or rubric revision invalidates a ready corpus and prior results. Starting freezes the attempt configuration and corpus. A fully judged batch is the durable recovery boundary: resume skips its successful alias/file pairs and retries only missing pairs, while an interrupted in-flight batch is retried at the same commit. | Results from different repositories, model graphs, or corpora must never be merged silently, and recovery must not pretend unpersisted model output survived. |
| `FR-REPOEVAL-007` | MUST | Every candidate alias receives identical bounded chunks, prompt, schema, tool-free immutable evidence, effort, and output limits. Candidate order rotates across corpus batches. Each execution batch reacquires the canonical repository and exact commit through a fresh Git Workspace, validates only its persisted candidates by rebuilding classification from bounded exact blobs and matching commit, inventory, path, mode, blob, size, ID, and hard scope, and does not rescan the full repository. Candidate and judge consume distinct one-shot capabilities over independently cloned copies of one frozen snapshot. The Git Workspace is released before provider calls and an unavailable pinned commit pauses/fails instead of substituting a newer ref. | Comparisons need equal inputs, large repositories must not be rescanned for every small batch, and models must not receive mutable repository or host-filesystem authority. |
| `FR-REPOEVAL-008` | MUST | Candidate outputs are blinded before judging. The judge scores correctness, evidence, coverage, actionability, and overall quality; the analyzer consumes bounded judge summaries and objective runtime statistics. Scores are labeled AI judged and judge/candidate overlap produces a visible bias warning. | A model opinion is useful comparison evidence but is not ground-truth benchmark data. |
| `FR-REPOEVAL-009` | MUST | The comparison table reports alias, concrete model distribution, completion/failure, files/languages/regions/bytes covered, confirmed and unsupported claims, score dimensions, rank, verdict, strengths/limitations, requests, failures, tokens, latency, and known estimated cost without representing unknown price as zero. | Operators need both quality and economic evidence to choose a model deliberately. |
| `FR-REPOEVAL-010` | MUST | The API/UI expose asynchronous preflight, Start, Cancel, Resume, Restart, list/detail, paged corpus preview, live stage/counters, usage, warnings, run history, and completed results. A failed probe offers Resume to continue its durable checkpoints and Restart from scratch to create and preflight a fresh evaluation with the same configuration. The page labels the workload a model review probe and states that it does not start an actual repository review or write findings. Repository and candidate models form the basic setup; ref, hard scope, corpus quotas, selector, and judge controls are Advanced and collapsed by default without losing draft values. Active polling observes durable backend state; browser timers do not own execution. | Large repository evaluations must be understandable and controllable after navigation or launcher restart without becoming another overloaded review form. |
| `FR-REPOEVAL-011` | MUST | Progress is projected from actual backend workflow-step dispatch and distinguishes resolving, inventorying, classifying, selecting, validating, candidate batch execution, judging, analyzing, completion, cancellation, and failure, with the judge alias when one exact judge call is active plus per-language available/selected files, bytes, regions, and limited status. Candidate batches remain generic unless the provider call boundary can identify the active child without stale attribution. | A long evaluation must show real work rather than mock timer stages, an indeterminate spinner, or a stale model/file label. |
| `FR-REPOEVAL-012` | MUST | Ordinary API responses omit repository source content, credentials, absolute checkout/artifact paths, internal capabilities, and raw provider payloads. Persisted corpus entries contain only exact references and compact bounded results. | Durable comparison state must not become a source or credential exfiltration surface. |

## Data And State Model

| State | Shape And Location | Contract |
| --- | --- | --- |
| Evaluation | `workspace/repository_evaluations/evaluation_rme_*.json` | Versioned configuration, lifecycle, corpus references, progress, usage, compact comparison, warnings, and run IDs; mode `0600`. |
| Corpus manifest | Commit/inventory/policy/rubric hashes plus selected path/blob/size/language/type/module/region/chunk IDs | No source content; becomes immutable when execution starts. |
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
Owns: TEST web/frontend/src/components/model-evaluations/**
Owns: CODE web/frontend/src/routes/model-evaluations.tsx
Owns: TEST web/frontend/src/routes/-model-evaluations-route.test.tsx
Owns: HTTP * /api/model-evaluations*
Owns: UI /model-evaluations

Shared executor/model-resolution and built-in workflow registry behavior remains
owned by Workflows and Agent Execution Optimization. Repository Reviews owns
bug-finder scope planning even when both features call `reposcope`.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `/api/model-evaluations*` | Version-fenced configuration, asynchronous actions, bounded detail/list/options, and paged corpus references. | `FR-REPOEVAL-001`, `FR-REPOEVAL-002`, `FR-REPOEVAL-010`, `FR-REPOEVAL-012` |
| Workflow | Built-in preflight, batch, and analysis workflows | Resolve/classify/select, run identical candidate batches, blind/judge, and analyze with durable run IDs. | `FR-REPOEVAL-003`–`FR-REPOEVAL-009` |
| UI | `/model-evaluations` | Configuration, language quotas, corpus methodology, live progress, cancellation/resume, and AI-judged comparison. | `FR-REPOEVAL-004`, `FR-REPOEVAL-008`–`FR-REPOEVAL-011` |

## Cross-Feature Behavior

- Git Workspaces owns safe checkout acquisition/release; evaluations pin a
  commit and consume exact Git blobs without retaining a mutable checkout during
  model calls.
- Workflows owns durable run execution, immutable frozen-scope capabilities,
  built-in templates, agent-call usage events, and explicit model-alias
  overrides. Evaluation state stores only bounded run identities and results.
- Agent Conversations and Account/Model Routing own alias resolution,
  provider fallback, and actual concrete-model reporting. Evaluation never
  persists credentials and displays routed concrete-model distributions.
- Launcher Management owns authentication, same-origin/replay mutation guards,
  shared navigation, and safe model options.
- Repository Reviews uses the shared repository scope classifier for bug-finder
  preflight but retains an independent finding ledger and lifecycle.

## Algorithms And Ordering

1. Normalize repository/ref, aliases, code types, case-exact folder prefixes,
   free text, and per-language limits. Excludes always win. Free text can only
   narrow the structured hard boundary.
2. Preflight pins the ref and inventories the exact Git tree. Native code
   produces a sanitized candidate catalog and aggregate language/region counts.
3. The selector receives no source content or absolute workspace path and
   returns opaque candidate IDs. Native selection validates every ID and fills
   missing language slots round-robin across regions with deterministic path/blob
   tie breaks.
4. Persist the corpus and mark it ready before any candidate call. Start creates
   deterministic `(evaluation, corpus, model, chunk, rubric)` tasks.
5. Reacquire the pinned commit for each bounded batch, reclassify only its
   persisted selected blobs, verify their exact path/mode/blob/size and hard
   scope, freeze two one-shot capabilities over the same source, and release.
   Every candidate processes identical chunks without rebuilding the full catalog.
6. Blind bounded candidate outputs, then give the judge the second capability
   over the identical evidence. Persist the fully judged batch before admitting
   the next batch; an interrupted in-flight batch is intentionally retried.
7. Judge per batch, aggregate objective usage, then analyze only bounded judge
   summaries. Validate structured output and persist the comparison atomically.

## Failure And Edge Cases

- Empty repositories, no eligible source language, more than the bounded
  language limit, invalid folders, unsafe paths, stale commit/inventory IDs,
  tampered AI IDs, and invalid aliases fail preflight without a ready corpus.
- A language with fewer requested files selects all eligible files and records a
  limited warning. A tiny language is not padded with documentation or generated
  content merely to meet a byte target.
- Changing a branch after preflight cannot change a selected blob. If the pinned
  commit cannot be reacquired, execution stops with an actionable error.
- Candidate partial failure remains visible in the final table and does not
  manufacture a score. Cancellation checkpoints completed tasks and never marks
  a partial judge/analyzer result complete.
- Raw model output is bounded internal evidence. Invalid structured selection,
  candidate, judge, or analyzer output gets one bounded repair and then fails the
  affected stage explicitly.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-REPOEVAL-001`, `FR-REPOEVAL-002`, `FR-REPOEVAL-006`, `FR-REPOEVAL-010`, `FR-REPOEVAL-012` | [pkg/repoeval/store_test.go](../../pkg/repoeval/store_test.go), [web/backend/api/repository_model_evaluations_test.go](../../web/backend/api/repository_model_evaluations_test.go) |
| `FR-REPOEVAL-003`, `FR-REPOEVAL-004`, `FR-REPOEVAL-005` | [pkg/reposcope/candidates_test.go](../../pkg/reposcope/candidates_test.go), [pkg/reposcope/selection_test.go](../../pkg/reposcope/selection_test.go), [pkg/workflows/repository_model_evaluation_native_test.go](../../pkg/workflows/repository_model_evaluation_native_test.go) |
| `FR-REPOEVAL-006`, `FR-REPOEVAL-007`, `FR-REPOEVAL-008`, `FR-REPOEVAL-009` | [web/backend/api/repository_model_evaluation_controller_test.go](../../web/backend/api/repository_model_evaluation_controller_test.go), [pkg/workflows/repository_model_evaluation_native_test.go](../../pkg/workflows/repository_model_evaluation_native_test.go), [pkg/workflows/repository_model_evaluation_workflows_test.go](../../pkg/workflows/repository_model_evaluation_workflows_test.go) |
| `FR-REPOEVAL-010`, `FR-REPOEVAL-011` | [web/frontend/src/api/model-evaluations.test.ts](../../web/frontend/src/api/model-evaluations.test.ts), [web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx](../../web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [pkg/reposcope](../../pkg/reposcope)
- [pkg/repoeval](../../pkg/repoeval)
- [pkg/workflows/repository_model_evaluation_native.go](../../pkg/workflows/repository_model_evaluation_native.go)
- [web/backend/api/repository_model_evaluations.go](../../web/backend/api/repository_model_evaluations.go)
- [web/backend/api/repository_model_evaluation_controller.go](../../web/backend/api/repository_model_evaluation_controller.go)
- [web/frontend/src/components/model-evaluations](../../web/frontend/src/components/model-evaluations)
