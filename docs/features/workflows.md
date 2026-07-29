# Workflows And Reusable Automation

## Feature ID

`FR-WORKFLOW`

## Behavior Summary

PicoClaw workflows define GitHub Actions-shaped automation that can run from
manual calls, channel messages, slash-style commands, cron schedules, runtime
events, durable external events, and other workflows. Local reusable workflows
use canonical refs such as `workflows/summarize-text.yml`, can declare typed
`workflow_call` inputs, secrets, and outputs, can map or inherit secrets, and
can inherit conversation session and delivery context from a channel-triggered
parent run.
The workflow dashboard also owns the end-to-end development cycle: one active
workflow draft or repair session may exist at a time, a new brief starts with
AI-authored workflow YAML by default, deterministic scaffold remains available
as a fallback, external-event filters have a server-parsed structured editor
and side-effect-free captured-event match preview, and publish validates,
writes, reloads, and stamps the workflow against the current PicoClaw runtime.
The same dashboard exposes built-in template installation and explicit restore,
revision-fenced workflow settings, and exact draft dependency readiness.
Publishing is fenced to the visible development session, draft, target,
dependency evaluation, and successful draft test, then commits the definition
and compatibility state through one recoverable workspace transaction.
Template and publish transactions use exact pre/post transition journals:
recovery first proves that every participant still belongs to the interrupted
transaction, then restores all eligible post-images or preserves every current
file and the journal on conflict. Workflow-owned internal state roots are
guarded against symlink escape, and their directory, replacement, and removal
boundaries use the shared durable filesystem primitives on POSIX and Windows.
Event-parity draft tests resolve a selected durable event on the server and use
the same redacted context construction as automatic dispatch. Native workflow
functions provide common state, artifact, git inventory, and git filter
primitives so AI-authored workflows do not need helper scripts for durable
planning and reporting.
An explicitly installed GitHub issue-triage template composes deterministic
`issues.opened` routing, a no-tool structured classifier, and one separately
declared GitHub MCP comment action.

## Reconstruction Notes

- Similarity target: recreate a local, file-backed workflow engine with
  GitHub-style `on`, `jobs`, `needs`, job-level reusable workflow calls,
  step-level `uses`, channel-aware delivery, and conversation session reuse.
- Core types/functions: workflow parser, local ref resolver, validator, run
  executor, run store, trigger matcher, expression evaluator, and channel
  delivery context binding.
- Runtime ordering: resolve workflow ref, parse YAML, validate calls, schedule
  cron expressions, and graph, match trigger, bind input/session/delivery
  context, enforce timeout/concurrency limits, execute jobs by dependency
  order, persist run events and outputs, then deliver final messages or handled
  media through existing channel tools.
- Non-obvious constraints: `uses: workflows/foo.yml` is canonical for local
  reusable workflows, reusable workflows are called at job level, workflow
  steps reuse existing tool/agent/MCP policy gates, classifier-style agent
  steps can read history without writing it or set `tools: none` so untrusted
  content cannot invoke tools, and delivery is separate from session memory.
  Only one workflow development session can be active at a
  time; release/version changes force deterministic revalidation before
  published workflows can trigger or run. External-event routing is
  deterministic; an AI classifier is an ordinary workflow step after a broad
  explicit `on.event` match.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-WORKFLOW-001` | MUST | Local reusable workflow refs resolve from `workspace/workflows/` using canonical `workflows/<file>.yml` or `.yaml` refs; absolute paths, parent traversal, non-workflow prefixes, invalid extensions, and symlink escapes are rejected. | Reusable workflows need predictable safe local addressing. |
| `FR-WORKFLOW-002` | MUST | Workflow YAML accepts unquoted GitHub-style `on`, job maps, string-or-list `needs`, job-level `uses: workflows/<file>.yml`, and step-level `uses` targets for `agent/`, `tool/`, `mcp/`, and `function/`. | Developers should be able to write workflows that look familiar to GitHub Actions users. |
| `FR-WORKFLOW-003` | MUST | `on.workflow_call` validates typed inputs, declared secrets, and output value expressions before the workflow is callable. | Reusable workflows need an explicit contract between caller and callee. |
| `FR-WORKFLOW-004` | MUST | Job dependencies reference existing jobs and reject dependency cycles before execution starts. | Workflow runs must fail fast instead of deadlocking or producing partial output. |
| `FR-WORKFLOW-005` | MUST | Reusable workflow calls are supported at job level; step-level `uses: workflows/...` is rejected in v1. | Parent/child run state, outputs, and secret binding are job-scoped. |
| `FR-WORKFLOW-006` | MUST | Channel-message and standalone command triggers can filter by channel, chat, sender, mention, command, regex, declared command args, and passthrough behavior, and can bind `conversation.session` and `conversation.delivery` modes. Each asynchronous launch receives the exact parsed, validated, compatibility-approved workflow snapshot whose trigger matched rather than reloading the ref. | Chat workflows need precise activation and duplicate-reply control without an edit between matching and execution changing the selected action. |
| `FR-WORKFLOW-007` | MUST | Conversation pipelines support agent step modes `history: read_write`, `history: read_only`, `history: none`, `session: inherit`, explicit `key:` sessions, and cache modes `session`, `agent`, `none`, or explicit `key:`. | Chat pipelines need classifier/enrichment steps that can follow context without polluting durable chat history. |
| `FR-WORKFLOW-008` | MUST | A channel-triggered run stores normalized event context and delivery metadata so `tool/message` can default to the same Telegram topic or Slack thread. | Delivery should be automatic while remaining separate from session memory. |
| `FR-WORKFLOW-009` | MUST | Runtime execution exclusively creates each run file without replacing an existing run identity, syncs the new file and its run/store/workspace directory entries before reporting creation, and atomically replaces later run updates and cancellation state with a synced temp file and rename. It persists parent and child run records, job/step status, input/output snapshots, session key, delivery context, and event JSONL under `workspace/workflow_runs/`. Durable external-event snapshots preserve exact JSON number tokens on read and update. Numbers propagated into dynamic run or lifecycle outputs remain readable even outside the float64 range, while representable ordinary workflow values retain their legacy float64 decoding. | Runs need a cross-process no-clobber and power-loss durability boundary, restart-safe inspection, full-fidelity event context, and auditable parent/child links. |
| `FR-WORKFLOW-010` | MUST | CLI, HTTP, and agent-tool surfaces expose list, validate, run, cancel, retry, status, graph, reload, and event inspection operations through the shared workflow parser, validator, executor, and file run store. | Operators, UI, and agents should not fork workflow behavior. |
| `FR-WORKFLOW-011` | MUST | `on.schedule` cron triggers and `on.runtime_event` filters run while the agent loop is active and use the same executor, depth, timeout, concurrency, session, and delivery rules as channel-triggered workflows. A schedule cache entry retains the exact validated workflow snapshot loaded with that entry, and an asynchronous runtime-event launch receives the exact snapshot whose filter matched; neither path reloads the ref before execution. Schedule cache entries and asynchronous launches are fenced to their exact config generation. The runtime-event subscription exists only while workflows are enabled, captures that generation, is synchronously removed for the outer reload transaction, and is recreated for the final committed or restored generation before turn admission resumes. | Workflows need autonomous automation beyond inbound chat messages without carrying cached schedules, changed post-match definitions, or provisional lifecycle events across config generations. |
| `FR-WORKFLOW-012` | MUST | Runs can be canceled and retried; cancellation marks the persisted run canceled and stops before later jobs or steps, while retry creates a new run linked to the original run and reuses the original ref, inputs, event, session, and delivery. | Operators need safe intervention without losing audit history. |
| `FR-WORKFLOW-013` | MUST | Runtime limits enforce configured max concurrent top-level runs, default per-run timeout, max reusable-call depth, and retention pruning for terminal runs. | Automation must be bounded in resource use and storage. |
| `FR-WORKFLOW-014` | MUST | Reusable workflow calls support `secrets: inherit`, explicit secret mapping expressions, and `continue-on-error` on jobs and steps. | Shared workflows need GitHub-like reuse and optional child failure handling. |
| `FR-WORKFLOW-015` | MUST | HTTP exposes workflow run events as JSON and SSE, plus child/retry run graph data; the dashboard exposes workflow definitions, manual run launch, run list, run details, events, graph, reload, cancel, and retry. | Operators need live inspection and control without shell access. |
| `FR-WORKFLOW-016` | MUST | Workflow tool steps that return handled media deliver attachments, generated audio, or files back to the same delivery target and preserve Telegram topics or Slack threads when present. | File and TTS workflows must reply in the same discussion as text workflows. |
| `FR-WORKFLOW-017` | MUST | Workflow development uses a single persisted active session with start, revise, AI-revise, validate, test-run, publish, and discard operations; starting another development session while one is active or sending concurrent development mutations returns a conflict. HTTP and the agent-callable workflow tool expose this lifecycle so a user can ask AI to draft, test, and publish a workflow without scripts. AI revision receives existing workflow refs plus bounded runtime agent/tool capability context so drafts can target dashboard-runnable steps. Repository-wide review prompts produce an explicit draft workflow that inventories the requested commit and feeds selected files into a managed scope-split review step. The active session persists the latest draft-test snapshot, clears it when the executable draft YAML or target ref changes, preserves it across prompt-only or no-op saves, and publish requires a current successful draft test. | AI-assisted authoring is simpler and avoids divergent pending edits. |
| `FR-WORKFLOW-018` | MUST | Workflow compatibility stamps record the PicoClaw version, git commit, workflow engine version, schema version, validator fingerprint, workflow hash, validation status, and issues; version or hash changes mark workflows pending revalidation and block automatic/manual execution until revalidated. | Releases can invalidate workflow semantics, so existing automation must fail closed until checked. |
| `FR-WORKFLOW-019` | MUST | HTTP-triggered workflow runs and draft test runs execute `agent/*`, `tool/*`, and `mcp/*` steps through the configured PicoClaw agent/tool runtime and persist step outputs in normal run records. Tool and MCP step results that return a JSON object or array expose the parsed value as `outputs.json`; object results also promote non-conflicting top-level fields for downstream expressions. | AI-authored workflows must be testable from the dashboard before publish, and later workflow steps need structured tool data without parsing prose. |
| `FR-WORKFLOW-020` | MUST | Native workflow functions expose workflow-scoped durable state, workflow run artifacts, git commit inventory, and path-policy filtering through `function/workflow.state`, `function/workflow.artifact`, `function/git.inventory`, and `function/git.filter`. Native state reads, writes, and deletes use the guarded internal workflow-state root and fail before mutation when a root or nested symlink escapes it. | AI-authored workflows need common state, artifact, repository-inspection, and deterministic filter-application primitives without opaque helper scripts or domain-specific helpers in core. |
| `FR-WORKFLOW-021` | MUST | Agent workflow steps integrate with the dedicated [Managed Agent Execution](agent-execution-optimization.md) contract: `with.output` declares structured JSON output, `with.managed` enables generic hidden scope/task/hybrid splitting, and the visible workflow step persists the combined structured result plus managed diagnostics. | AI-driven workflow development needs generic, inspectable agent adaptation that preserves output quality while reducing token and model spend. |
| `FR-WORKFLOW-022` | MUST | PicoClaw can install a local `workflows/code-review.yml` template that acquires a git workspace, inventories repository structure with workspace/file links, releases the workspace before asking an agent to propose include/exclude globs, reacquires only long enough for `git.filter` to refresh selected workspace file links, releases again before model review, and runs an agent step with structured JSON review output; workflow tool steps expose JSON object results as addressable step outputs for downstream workflow expressions. | Code review automation needs a local hosted workflow that composes the git workspace feature with AI-assisted path selection, deterministic filter enforcement, and inspectable review output. |
| `FR-WORKFLOW-023` | MUST | `on.event` accepts explicit non-empty source, connector, event-type, actor, subject, and attribute string-or-list filters with anchored `*`/`?` globs. Durable routing uses OR within lists and AND across fields, case-insensitive normalized types/connector identity and case-sensitive IDs/attribute values. It matches one exact validated byte snapshot and atomically stores that snapshot's opaque content revision with the new dispatch. Before a matched dispatch runs, the dispatcher loads one current runnable snapshot, rejects revision drift, re-evaluates the persisted event against that same snapshot (also required for a legacy unbound dispatch), and passes the snapshot directly to the executor. The run uses its deterministic run ID with the full redacted envelope, event/dispatch inputs, an isolated `workflow:<ref>:event:<event-id>` session, and empty delivery. The executor exclusively creates that run and invokes `OnRunPersisted` to link its dispatch before any step; crash reconciliation never repeats an existing running, terminal, or previously linked-but-pruned run. | GitHub, chat, email, and webhook automation need one deterministic trigger contract and one safe bridge into existing AI/tool workflow execution that cannot silently switch definitions after selection. |
| `FR-WORKFLOW-024` | MUST | An `agent/*` workflow step may declare `with.tools: none`; the initial model request, structured-output repair requests, managed fallbacks, and child work then expose no tool definitions and cannot execute a model-authored tool call. Omitted `tools` or explicit `inherit` preserves existing behavior. Unsupported or non-string modes fail workflow validation, and the workflow compatibility identity changes so a binary that ignored `tools: none` cannot run a newly stamped classifier workflow. | Classifying attacker-controlled event content must not silently grant the model the default agent's tools. |
| `FR-WORKFLOW-025` | MUST | `picoclaw workflow install github-issue-triage` idempotently installs a valid, opt-in `workflows/github-issue-triage.yml`. It deterministically matches native GitHub `issues.opened` events whose body-authenticated attribute is true, gives only signed repository/issue fields to an isolated no-tool `agent/main` step, requires enum category/priority plus a boolean comment decision, and conditionally calls the separately declared `mcp/github/add_issue_comment` step with owner, repository, and issue number from the signed body. The posted text is a fixed template containing only validated enum values and an event marker, never model prose or issue text. | A useful AI-driven action should be reviewable as ordinary workflow YAML and keep classification separate from authority-bearing effects. |
| `FR-WORKFLOW-026` | MUST | The authenticated workflow dashboard can inspect and edit a draft's typed `on.event` trigger while retaining raw YAML as the authoritative advanced surface. Projection and replacement use the server's workflow parser and YAML node tree, carry an exact source revision, preserve unrelated triggers/jobs/comments, and make unsupported alias/merge shapes or projected scalars containing line breaks raw-only rather than flattening or splitting them. A captured-event preview evaluates payload-free metadata through the same deterministic matcher used by routing and returns field-level checks without creating a replay, dispatch, or run. An event-parity draft test submits only an event ID; the server loads one already-redacted durable envelope through the live protected gateway generation, requires the draft trigger to match, derives the production event context, fixed inputs, isolated target-workflow session, and empty delivery, then creates only the ordinary auditable `draft:<target>` run and records the selected event ID in the current test snapshot. Event mode rejects manual input, secret, session, or delivery overrides. The workflow-author model runs with no history and no tools, is taught that deterministic filtering precedes AI steps, and receives no captured payload values through authoring or repair context. | Users need to build and safely test deterministic or AI-driven event workflows from the UI without duplicating matcher semantics, rewriting advanced YAML, granting classifier authority, or copying untrusted payloads through the browser and authoring model. |
| `FR-WORKFLOW-027` | MUST | The authenticated workflow dashboard lists every built-in workflow template with a path-free `available`, `installed`, `modified`, or `blocked` state, keeps the catalog visible with install/restore actions disabled while development is active, can install an available template, and requires an explicit restore confirmation before overwriting a modified regular file. Template installation is serialized with workflow/config mutations, refuses blocked or active-development targets, atomically writes the configured definitions target, and revalidates the complete compatibility catalog. Before either durable file changes, a synced version-2 journal records exact pre/post existence, bytes, and permission mode for both target and manifest. Synchronous rollback and prepared-journal recovery first inspect every participant, accept only its exact pre- or post-image, then recheck every participant unchanged before restoring any post-image to its pre-image. A third state, read failure, or recheck change is a recovery conflict that preserves every current file and the journal and blocks the workflow operation; a committed journal is finalized without rollback. Journal creation/removal, target/manifest replacement, and missing parent creation use the shared durable POSIX/Windows file boundaries. It never enables workflows, event ingress, MCP, or another subsystem implicitly. | Operators need a safe browser path from a reviewed built-in example to an installed workflow without silently replacing local edits, overwriting post-crash operator changes, or partially activating invalid state. |
| `FR-WORKFLOW-028` | MUST | The authenticated workflow dashboard exposes workflow-only settings for enablement, definitions directory, concurrency, timeout, reusable-call depth, and retention. GET returns configured values, effective defaulted values, an opaque exact public-plus-security config revision, and launcher/catalog/gateway effect status; PATCH accepts only those workflow fields, validates bounded non-negative limits and a definitions directory that remains strictly inside the workspace both lexically and after symlink resolution, rejects a definitions-root move during active development, rejects unknown/trailing/oversized input, and saves only when the expected config revision still matches under the advisory lock shared by every config save. Workflow settings always participate in gateway restart-signature evaluation even when event ingress is disabled. The responsive settings UI preserves unsaved values across conflict refetches, shows configured-versus-effective values and restart/reload effects, and surfaces stale-write or active-development conflicts. | Workflow runtime policy should be manageable without editing unrelated configuration, overflow-sized runtime limits, retargeting an active draft, following an apparently local definitions symlink outside the workspace, losing a concurrent secret rotation, or overwriting a concurrent operator/process change. |
| `FR-WORKFLOW-029` | MUST | Dependency inspection accepts exactly one published ref or exact draft target/YAML overlay, validates the root, and walks the complete declared reusable-workflow closure within fixed definition, declaration, issue, depth-traversal, per-file byte, total-byte, and call-depth budgets. It reports stable source locations, reusable cycles/depth, analysis-limit exhaustion, and static input/secret contract failures, plus current readiness for every declared `agent`, `tool`, `mcp`, `function`, and reusable dependency even when conditionally skipped. Runtime readiness uses the production agent registry, effective tool policy, native-function set, MCP enablement/allowlist/deferral/connection/discovery state, and the same canonical and exact original MCP server/tool identity checks as execution. The runtime is constructed from the same immutable config snapshot used by structural analysis, and evaluation fails closed if the full opaque config generation changes before report finalization. The path-free response uses fixed reason codes and an opaque revision bound to the exact root bytes, reachable child bytes/revisions, stable public-plus-security config revision, effective workflow limits, and report. The development UI automatically checks the exact current editor bytes, including trailing whitespace, displays actionable structural/runtime blockers, marks older or unavailable results stale, and cannot publish unless workflows and the exact report are ready. Operate automatically checks the selected published ref and exposes the same bounded structural/runtime report with an explicit retry for unavailable checks. | A draft that parses and tests on one path can still fail when another declared capability or reusable branch is activated; users need a bounded, internally consistent, production-equivalent, non-secret readiness gate before publish and while operating published workflows. |
| `FR-WORKFLOW-030` | MUST | Dashboard publish submits opaque fences for the active session, exact target/YAML draft, target pre-image, and dependency evaluation. While holding the shared config/development/workspace mutation boundary, the server rechecks all fences, the exact current successful draft-test snapshot, deterministic validation, target state, and a fresh dependency evaluation of the persisted draft bytes both before manifest preparation and immediately before transaction snapshots. It prepares a full compatibility manifest with the target overlaid in memory, then a synced version-2 transition journal records exact pre/post existence, bytes, and permission mode for the target, manifest, archive, and active session before activating them as one recoverable transaction. Synchronous rollback and prepared-journal recovery use the same two-pass all-file compare-and-swap rule as template recovery: any participant outside its exact pre/post images, unreadable during inspection, or changed during the all-file recheck preserves every current file and the journal and blocks the workflow operation; only a fully verified set is restored, while a committed journal is finalized without rollback. All transaction parent creation, atomic replacement, and logical removal use the shared durable POSIX/Windows file boundaries. Dashboard and agent-tool publish use the same production dependency gate and exact session/draft/target fences. The UI compares and submits exact YAML bytes, treats whitespace-only edits as stale, and fails closed while any fence is missing, loading, stale, blocked, or unavailable; it explains safe fixed-code conflicts without retrying a publish against newer state. | Publishing combines user-authored bytes, live runtime readiness, and several durable files, so stale browser state, a dependency edit during manifest preparation, a bypassing tool call, a whitespace-normalized fence, a crash, or post-crash operator edit must not produce an untested target, mismatched compatibility stamp, or lost development session. |
| `FR-WORKFLOW-031` | MUST | Every read, lock, write, replacement, or removal of workflow-owned internal state resolves through the evaluated workspace and one of the fixed `workflow_state`, `workflow_validations`, or `workflow_dev` roots. Those roots themselves cannot be symlinks, and any nested path that resolves outside its root fails closed before access; a symlink used as the workspace path remains supported after evaluation. This guard covers native workflow state, the mutation lock, template/publish journals, compatibility manifest, active development session, and development archive. | A workspace-local configuration must not let an internal state symlink redirect locks, recovery records, compatibility authority, or development data to unrelated files. |

## Data And State Model

Workflow definitions live under `workspace/workflows/`. A local ref
`workflows/summarize-text.yml` resolves to
`workspace/workflows/summarize-text.yml`; `./workflows/summarize-text.yml` may
be accepted as input but canonicalizes to the no-dot form.

Workflow-owned state, artifacts, and runs persist under:

```text
workspace/workflow_state/<workflow_namespace>/<key>.json
workspace/workflow_artifacts/<workflow_namespace>/<run_id>/**
workspace/workflow_runs/<run_id>/run.json
workspace/workflow_runs/<run_id>/events.jsonl
```

Workflow development and compatibility state persist under:

```text
workspace/workflow_dev/active.json
workspace/workflow_dev/archive/<development_id>.json
workspace/workflow_validations/manifest.json
workspace/workflow_state/mutation.lock
workspace/workflow_state/publish-transaction.json
workspace/workflow_state/template-transaction.json
```

Template and publish journals are version-2 file-transition records. Each
participant stores exact pre- and post-existence, bytes, and permission mode,
plus a prepared/committed phase and the last started transaction stage. The
journals remain present when recovery cannot prove that every current file is
one of those recorded images.

The three internal roots in this model are resolved against the evaluated
workspace before any read, lock, write, replacement, or removal. Root symlinks
and nested symlink escapes fail closed; a symlink naming the workspace itself
is allowed. Missing parent directories, atomic replacements, and logical
deletions follow the shared durable filesystem contract in
[Security, Credentials, And Isolation](security-isolation.md).

Run records include run ID, workflow ref, status, parent run ID, child run IDs,
caller job ID, retry source run ID, input/output/event snapshots, embedded job
and step snapshots, session key, delivery context, timestamps, cancel metadata,
and error summaries. Delivery context stores outbound target metadata such as
channel, chat ID, topic/thread identifier, and reply target. Session context
stores the memory key used by agent steps.

The GitHub issue-triage template is never installed or activated implicitly:

```sh
picoclaw workflow install github-issue-triage
```

The operator must separately enable workflows, native GitHub event ingress, and
an MCP server named `github` that exposes `add_issue_comment`. That server must
be enabled and non-deferred (`"deferred": false`) so the explicit workflow step
can resolve `mcp/github/add_issue_comment`; its write credential is independent
from the webhook signing secret. Installation revalidates the local catalog,
but does not change gateway or MCP configuration. The template matches all
native GitHub connectors unless the operator adds an explicit
`on.event.connectors` filter to the installed definition and revalidates it.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| File | `workspace/workflows/*.yml`, `workspace/workflows/*.yaml` | GitHub-style workflow definitions with `on`, `jobs`, `needs`, `uses`, `with`, `if`, `outputs`, `schedule`, `runtime_event`, `event`, and `workflow_call`. | `FR-WORKFLOW-001` through `FR-WORKFLOW-016`, `FR-WORKFLOW-023` through `FR-WORKFLOW-031` |
| Go API | `pkg/workflows.Parse`, `Resolver.ResolveLocal`, `Validate`, `LoadRunnableLocalSnapshot`, `Executor.Run`, `Executor.Retry`, `FileRunStore`, `MatchChannelMessage`, `MatchCommandMessage`, `MatchRuntimeEvent`, `EvaluateEventTrigger`, `MatchEventTrigger`, `EventWorkflowRouter`, `EventWorkflowDispatcher`, `BuildRunGraph`, `ReloadLocal`, `ListWorkflowTemplates`, `InstallWorkflowTemplateWithCompatibility`, `CheckWorkflowDependencyClosure`, `ResolveWorkflowDependencyReadiness`, `PublishWorkflowDevelopmentFenced` | Parse GitHub-shaped YAML, normalize local reusable refs, reject unsafe refs, validate an exact compatibility-stamped byte snapshot, inspect and transactionally install local workflow templates, project and revise typed event triggers without replacing unrelated YAML, inspect structural/runtime dependency readiness, evaluate process-local and durable triggers, durably route/reconcile external-event runs, run/retry/cancel workflows, build run graphs, recover and atomically publish fenced development state, reload definitions, and persist run state through guarded internal roots. | `FR-WORKFLOW-001` through `FR-WORKFLOW-016`, `FR-WORKFLOW-018`, `FR-WORKFLOW-022` through `FR-WORKFLOW-031` |
| Config | `workflows.*`, `tools.workflow` | Global enablement, workflow tool enablement, max call depth, definitions directory, concurrency, timeout, and retention defaults; workflow-only browser writes carry an exact config revision. | `FR-WORKFLOW-009`, `FR-WORKFLOW-013`, `FR-WORKFLOW-028` |
| CLI | `picoclaw workflow install/list/compatibility/revalidate/validate/reload/run/cancel/retry/status/events/graph` | Install local workflow templates, including `code-review` and `github-issue-triage`, then manage definitions, compatibility stamps, and runs through the same workflow runtime and file run store used by agent tools. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015`, `FR-WORKFLOW-018`, `FR-WORKFLOW-022`, `FR-WORKFLOW-025` |
| HTTP | `/api/workflows*`, `/api/workflows/runs*`, `/api/workflows/development*`, `/api/workflows/templates*`, `/api/workflows/settings`, `/api/workflows/dependencies/check`, `/api/workflows/compatibility`, `/api/workflows/revalidate` | List, validate, reload, run, cancel, retry, inspect, stream workflow events, read run graph data, list/install/restore templates, read and revision-fence workflow settings, inspect exact published or draft dependency readiness, manage the singleton development session, project and revision-fence typed event-trigger edits, preview a captured event through the runtime matcher, run configured-agent YAML revisions, test active drafts manually or with a server-resolved durable event after a run record is persisted, reject draft-changing development mutations while the current draft test is still running, execute configured agent/tool/MCP workflow steps synchronously or asynchronously, revision-fence and transactionally publish exact drafts, and revalidate release compatibility. Internal-state path or recovery conflicts fail closed before the requested workflow operation. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015`, `FR-WORKFLOW-017` through `FR-WORKFLOW-019`, `FR-WORKFLOW-026` through `FR-WORKFLOW-031` |
| UI | `/agent/workflows` | Two-mode workflow console: Develop shows singleton start readiness, starts new briefs with AI by default, resumes the singleton AI brief/YAML development cycle, marks the one active draft, sends active drafts to the configured no-tool workflow-author agent for YAML revision, offers deterministic scaffold fallback, and retains raw YAML beside a server-parsed event-trigger builder. The builder explains exact OR/AND, anchored-glob, and case semantics, preserves unsupported advanced YAML as raw-only, selects recent payload-free event metadata, previews field-level deterministic matches, and can explicitly reveal payload through the existing ephemeral no-cache inspector. Draft tests can use inline manual JSON context or event-parity mode; event mode sends only the selected ID and is gated by a successful server match. Failed-test AI repair receives bounded status and structural metadata rather than captured payload values. Develop restores the latest draft-test result, treats a running test as the active operation, shows exact structural/runtime dependency readiness, and gates revision-fenced publish on a current successful test and current ready report. The page also exposes path-free template catalog install/restore, keeps that catalog visible with actions disabled during active development, and provides responsive revision-fenced workflow settings with configured/effective/effect state. Operate shows definitions, compatibility status, published-ref dependency readiness, a GitHub-style manual run popover generated from declared `workflow_call` inputs and secrets with advanced session/delivery/raw secret JSON controls, compatibility-gated asynchronous launch, inline payload validation, the selected workflow run-readiness reason, runs, selected run detail, persisted delivery and trigger event context, job and step outputs, live streamed event payloads with polling fallback, graph, cancel, compatibility-gated retry with retry-secret JSON validation, reload, and refresh. | `FR-WORKFLOW-015`, `FR-WORKFLOW-017` through `FR-WORKFLOW-019`, `FR-WORKFLOW-026` through `FR-WORKFLOW-030` |
| Managed agent step | `uses: agent/*` with `with.output`, `with.managed`, and optional `with.scope` | Workflow-owned output schemas are injected into the agent prompt, parsed from the response, repaired once by default, validated locally, and exposed as `structured`. Managed options choose split strategy, fixed or token-adaptive chunk sizes, calibration sample/match/cache policy, parallel child limit, model candidates with price metadata, and effort optimization. Child runs are hidden from chat history by default and publish one combined structured result plus `managed` diagnostics. | `FR-WORKFLOW-007`, `FR-WORKFLOW-009`, `FR-WORKFLOW-019`, `FR-WORKFLOW-021` |
| Agent step policy | `uses: agent/*` with `with.tools` | Omitted/`inherit` retains the selected agent's registered tools; `none` disables tools for every model request made by that step. | `FR-WORKFLOW-024` |
| Local template | `workflows/github-issue-triage.yml` | Explicitly installed authenticated GitHub issue classifier plus a conditional declared MCP comment effect. | `FR-WORKFLOW-025` |
| Tool | `workflow` | Agent-callable list, compatibility, revalidate, validate, reload, run, cancel, retry, status, graph, events, `dev_status`, `dev_start`, `dev_revise`, `dev_validate`, `dev_test`, `dev_publish`, and `dev_discard` actions. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015`, `FR-WORKFLOW-017`, `FR-WORKFLOW-018` |
| Native functions | `function/workflow.state`, `function/workflow.artifact`, `function/git.inventory`, `function/git.filter` | Store/retrieve workflow-owned JSON state through the guarded internal root, write/read/list run artifacts, inventory git files by commit and blob hash inside a workspace, and apply structured include/exclude path policies to inventory output. `git.inventory` accepts a git workspace object or compatible working directory and emits file metadata plus workspace/file source references without embedding file content. `git.filter` accepts inventory files plus AI- or user-produced `includeGlobs`, `excludeGlobs`, and `selectedPaths`, supports recursive `**` globs, deterministically refreshes selected file source references for the active workspace, and does not embed file content in JSON output. Domain workflows compose these primitives for planning, reports, review scopes, and reuse decisions. | `FR-WORKFLOW-020`, `FR-WORKFLOW-031` |
| Internal storage | `workflow_state`, `workflow_validations`, `workflow_dev` | Guard mutation locks, transaction journals, native state, compatibility authority, and development state against root or nested symlink escape; use exact transition journals and shared durable file operations. | `FR-WORKFLOW-027`, `FR-WORKFLOW-030`, `FR-WORKFLOW-031` |
| Events | `workflow.*` | Trigger, run, job, and step lifecycle events. | `FR-WORKFLOW-008`, `FR-WORKFLOW-009`, `FR-WORKFLOW-011`, `FR-WORKFLOW-015` |

## Algorithms And Ordering

1. Normalize local workflow refs, reject unsafe paths, and resolve the canonical
   path under the workflow root.
2. Parse YAML into typed trigger, job, step, `workflow_call`, session, and
   delivery contracts.
3. Validate input types, output expressions, unknown dependencies, graph cycles,
   allowed `uses` targets, schedule cron expressions, runtime-event filters,
   channel trigger regex, and agent step history/cache/tool modes.
4. For channel and command triggers, match normalized `bus.InboundMessage`
   facts against one exact runnable workflow snapshot, bind event, session, and
   delivery context, and pass that same snapshot to the asynchronous executor
   before normal agent handling.
5. For schedule and runtime-event triggers, agent-loop automation goroutines
   load valid exact snapshots, retain the schedule snapshot or pass the
   runtime-event match snapshot directly, publish `workflow.triggered`, and
   start runs with the same executor configuration used by chat triggers.
6. Execute jobs in dependency order; job-level reusable calls create child runs
   and expose child outputs through `needs.<job>.outputs`.
7. Execute step-level tools, agents, MCP tools, and Go functions with existing
   PicoClaw policies, hooks, redaction, and channel delivery. Tool step results
   that contain JSON objects are exposed both under `json` and as non-conflicting
   top-level step outputs for later expressions.
8. For agent execution optimization steps, build child plans from declared
   scope items and textual agent `Tasks:`, calibrate grouped-vs-split output
   equivalence with a split-exercising sample, exact/similar trust cache,
   provisional borrowed verification, and split-fit-aware cadence, execute
   child plans in bounded parallel hidden runs, validate/repair structured
   child output, combine structured data, and persist model, effort, token,
   cost, split, and calibration metadata beside the visible step output.
9. Check cancellation between jobs/steps, enforce per-run timeout and top-level
   concurrency, and persist terminal status with cancel/error metadata.
10. Persist run and event state with embedded job and step snapshots before and
   after side effects.
11. Native workflow functions resolve all state, artifact, and git paths inside
    the workspace; git inventory uses commit blob hashes, `git.inventory` and
    `git.filter` emit workspace/file source references instead of embedding
    file content, `git.filter` applies include/exclude glob policies and exact
    selected paths to inventory output, and domain workflows compose these
    outputs with workflow-owned state and artifacts for their own planning,
    reports, review scopes, and reuse decisions.
12. For development, create `workflow_dev/active.json` only when no active
    session exists, scaffold repository-wide review prompts as an inventory
    step followed by a managed scope-split review step, use the configured
    no-tool/no-history agent as the default first draft path, revise the active
    draft locally or through that agent with existing workflow refs plus
    registered agent/tool target context, extract returned workflow YAML, and
    validate it. Structured event-trigger edits first project an exact source
    revision and then replace only the `on.event` node; captured-event preview
    loads payload-free metadata and calls the runtime evaluator. Event-parity
    draft testing sends one event ID, atomically loads the already-redacted
    envelope from the protected live generation, verifies the draft match,
    derives normal event context/input/session values, and persists a normal
    draft run without creating a dispatch.
13. For release revalidation, compare the current PicoClaw runtime identity and
    workflow hash with `workflow_validations/manifest.json`, classify stale
    workflows as pending, run deterministic validation on demand, and block
    stale or invalid workflow execution.
14. For local templates, inspect each configured target without exposing its
    filesystem path, distinguish available, exact installed, locally modified,
    and unsafe blocked state, require explicit overwrite for a modified regular
    file, build the overlaid compatibility manifest before mutation, and sync a
    version-2 prepared journal with the exact target and manifest pre/post
    images. Durably install validated YAML, activate the prebuilt manifest, mark
    the journal committed, and durably remove it. Before recovering a prepared
    transaction, inspect every participant for an exact pre/post image and
    recheck the complete unchanged set before restoring anything. Preserve all
    current files and the journal and fail closed on any conflict; finalize a
    committed transaction without rollback.
15. For durable external events, validate explicit non-empty filters, match the
    already-normalized envelope against one exact runnable byte snapshot,
    persist every selected dispatch plus that snapshot's content revision
    atomically through the current routing claim before acknowledging routing,
    and reconcile the deterministic run ID. When no run exists, load one exact
    runnable snapshot, reject stored revision drift, re-evaluate the persisted
    event against that same snapshot, renew the dispatch, and pass the snapshot
    to the normal executor; it exclusively creates the run and calls
    `OnRunPersisted` to link and renew the dispatch before any workflow step.
    Renew throughout long runs, cancel on lost ownership, and fail closed if a
    linked run is missing.
16. For the GitHub issue-triage template, route only an explicit
    body-authenticated `github`/`issues.opened` match, render a narrow signed
    payload projection as untrusted classifier scope, run classifier and repair
    requests with tools disabled, validate the required enum/enum/boolean
    decision, and only then evaluate the declared MCP comment step. The action
    receives signed body identity fields and fixed bounded text, not classifier
    prose.
17. For dependency inspection, parse and validate the exact published bytes or
    supplied draft overlay, traverse every declared reusable edge, check cycles,
    depth, static call contracts, and fixed graph/byte budgets, then resolve each
    occurrence through the configured production agent/tool/function/MCP
    runtime. Match an MCP dependency to the exact original server/tool identity
    carried by its registered wrapper. Hash length-delimited root bytes, child
    revisions, effective workflow configuration, and the sorted path-free
    report into the opaque result revision.
18. For fenced publish, acquire the config/development lock and the
    cross-process workspace mutation lock, recover any earlier publish journal,
    compare the session, exact draft, target pre-image, successful-test, and
    dependency revisions, run the dependency gate over the persisted draft
    YAML, and prepare the overlaid compatibility manifest. Re-read every
    authoring fence and run the exact dependency gate a second time immediately
    before snapshots. Sync a version-2 prepared journal containing the exact
    pre/post images for the target, manifest, archive, and active session;
    durably activate those transitions, mark the journal committed, then
    durably remove it. Synchronous rollback and later prepared recovery inspect
    all participants and recheck the complete unchanged set before restoring
    anything; a third state or recheck change preserves every current file and
    the journal and fails closed.
19. Before accessing workflow-owned internal state, resolve the evaluated
    workspace and fixed state root, reject a root symlink or nested symlink
    escape, then perform the read, lock, write, replacement, or removal. Create
    missing parents and replace or remove entries through the shared durable
    POSIX/Windows file primitives.

## Cross-Feature Behavior

Workflows use chat channels as trigger sources and delivery sinks. Routing and
session memory define conversation scope for channel-triggered runs. Agent
conversations provide the agent step execution path and provider prompt cache
keys. Tool execution, MCP, skills, hooks, and security policies govern side
effects exactly as they do in normal agent turns. Runtime events expose
workflow trigger, run, job, and step lifecycle state.
[Durable external event automation](event-automation.md) supplies normalized
redacted GitHub/chat/email/webhook envelopes plus fenced routing/dispatch
state. Workflows own filter validation, matching, run context, and execution;
AI classification remains inside ordinary agent steps and receives no extra
tool authority. The issue-triage template makes that boundary explicit with
`tools: none`; its GitHub mutation exists only as the following MCP step.
The code-review workflow template composes the git workspace tool, native git
inventory/filter functions, and agent structured-output path; checkout
allocation, locking, preservation, and retention remain owned by the git
workspaces feature. The filter-planning agent receives repository structure
metadata only, while `git.filter` enforces the returned globs and refreshes
workspace/file source references before model review. Review agents inspect
linked files through read-only file tools instead of receiving embedded content
inside workflow JSON. Workflow development preserves that visible
inventory-and-review shape when it recognizes repository-wide review prompts.

## Failure And Edge Cases

- Unsafe local refs fail before parsing or execution.
- Invalid YAML, unknown `uses` targets, unsupported input types, duplicate step
  IDs, unknown dependencies, and dependency cycles fail validation.
- A failed child workflow fails the caller job unless `continue-on-error: true`
  marks the job as optional.
- `passthrough: false` consumes matched channel messages to prevent duplicate
  agent replies; `passthrough: true` lets normal agent handling continue.
- `history: read_only` agent steps must not append to durable session history.
- Missing delivery context makes message-delivery steps fail closed unless the
  step explicitly provides a channel and chat target.
- Secrets are visible only when declared by the called workflow and are redacted
  from run records, logs, and events.
- Canceled runs remain auditable; retry creates a new linked run instead of
  mutating the original.
- Concurrent processes attempting the same run ID produce one owner; every
  loser receives the typed already-exists result and cannot truncate or replace
  the winner's run file.
- A successful run creation has synced `run.json`, the run-directory entry, the
  store-root entry, and a newly created store-root entry in the workspace before
  its dispatch may be linked. Updates and cancellation expose a complete old or
  new JSON record to concurrent readers, never a truncation window.
- Durable external-event runs link their dispatch after exclusive run creation
  and before steps. Callback failure leaves a failed run, and a dispatch that
  was linked to a subsequently missing run fails closed instead of replaying.
- Child reusable runs do not consume a separate top-level concurrency slot from
  their parent.
- Starting workflow development while `workspace/workflow_dev/active.json`
  exists fails with a conflict instead of creating another draft.
- Concurrent development mutations fail with conflict while another start,
  revise, AI-revise, validate, test-run, publish, or discard operation is in
  progress.
- A current running draft test is treated as the active development operation:
  draft-changing development mutations fail with conflict until that run
  completes or is canceled.
- Development session status reflects the current phase: valid-but-untested
  drafts remain editing, running draft tests set testing, and only a current
  successful draft test sets ready_to_publish.
- AI revision requires an active workflow development session and writes only
  back into that session; it never creates or publishes a second pending draft.
- Revise/save persists submitted YAML byte-for-byte, including trailing spaces
  and final newlines. Any YAML byte change changes the draft revision, clears a
  current draft-test snapshot, and makes earlier dependency readiness stale.
- Workflow authoring runs without durable chat history or model tools. Its
  prompt describes event fields but never includes a selected event payload;
  failed-test repair projects bounded run/event structure and omits event
  payload values and lifecycle message/payload text.
- Draft test runs use `draft:<target_ref>` refs and persist normal run records
  for inspection without writing the draft into `workspace/workflows/`.
- Event-parity draft tests accept only a durable event ID. A malformed or
  missing event, unavailable/replaced gateway generation, invalid or
  non-matching trigger, or any manual input/secret/session/delivery override
  fails before run creation. No preview or draft test creates a dispatch or
  changes event routing state.
- Event-trigger projection and replacement are stateless. A stale revision,
  unsupported alias/merge shape, or projected scalar containing a line break
  cannot overwrite newer YAML; the raw editor remains available for advanced
  definitions.
- Match preview evaluates only payload-free event metadata. Exact payload is
  loaded server-side only for an event-parity test or after the user's explicit
  inspector action, is never submitted back by the browser, and is discarded
  when event selection changes.
- The active development session stores the latest draft-test run ID, status,
  error, timestamp, and draft key so dashboard refreshes can resume publish
  readiness; changing the executable draft YAML or target ref clears that
  snapshot, while prompt-only or no-op saves preserve it.
- Dashboard refreshes and background run-event updates do not overwrite
  unsaved draft target, brief, or YAML edits in the active editor.
- Async draft-test completion takes the same singleton development lock and
  updates publish readiness only when the active session and draft key still
  match the draft that launched the run.
- Dashboard polling reconciles running draft-test snapshots when SSE is
  unavailable or fails, so terminal run state refreshes development publish
  readiness without requiring a manual page refresh.
- Canceling the run that backs the active draft test records a canceled
  draft-test snapshot so the dashboard can resume editing without waiting for
  the background executor callback.
- Dashboard draft tests and manual runs can supply the same session key and
  delivery JSON context used by channel-triggered workflow runs and return a
  running run ID as soon as the persisted run record exists.
- Dashboard manual runs and retries preserve failed HTTP run results and select
  the returned run ID so operators can inspect failed attempts.
- Dashboard workflow list, run detail, event, graph, and reload views tolerate
  persisted empty collections encoded as `null` by older run records or API
  responses.
- Publish requires the active draft to have a current successful draft-test
  result; the dependency report must describe the exact current draft and be
  ready, and backend publish rechecks every opaque fence before writing the
  workflow file and compatibility stamp. Editor currentness is byte-exact, so
  adding or removing only trailing whitespace still makes validation, test, and
  dependency readiness stale.
- A dependency check never returns loader, filesystem, provider, connection, or
  secret details. Invalid roots receive a fixed error; individual structural or
  runtime blockers use fixed reason codes and stable workflow-relative source
  locations. Definition count, declaration count, issue count, reusable depth,
  per-definition bytes, and total loaded bytes are bounded; exhausted analysis
  fails closed with `analysis_limit_exceeded`.
- Conditional jobs and steps still contribute dependencies. A dependency that
  is not used by one draft test therefore remains a publish blocker until its
  production runtime is ready.
- MCP readiness and execution use one canonical server/tool name function and
  require the registered wrapper to retain the requested exact original server
  and tool identity. Multiple connected identities that canonicalize to the
  same local tool name fail as a collision instead of selecting one by map
  iteration. A canonical name already occupied by a built-in, local, plugin, or
  different MCP tool is also a collision and is never overwritten.
- A stale settings revision cannot overwrite a concurrent config edit.
  Every cooperating config writer shares an advisory file lock and the scoped
  save performs its final compare-and-swap while holding it. Workflow settings
  reject negative or excessive limits, escaping definitions directories,
  definitions-root symlinks that resolve outside the workspace,
  definitions-root changes during active development, unknown fields, trailing
  JSON, and bodies over 1 MiB.
- Template restore is unavailable for non-regular, unreadable, or unsafe
  targets. A modified regular template requires an explicit confirmation and a
  failed or interrupted compatibility activation can restore both its previous
  bytes/mode and the prior manifest only when every current participant still
  exactly matches its journaled pre- or post-image. A durable committed marker
  prevents recovery from undoing a completed install.
- A stale session, draft, target, successful-test, or dependency revision
  rejects publish without changing any durable file. Unavailable dependency
  evaluation also fails closed.
- A failed publish mutation restores the target, compatibility manifest,
  development archive, and active session only after the two-pass all-file
  comparison proves that every participant still belongs to the transaction.
  A recovery conflict performs no restore, preserves all current files and the
  prepared journal for operator reconciliation, and blocks further workflow
  reads or mutations. It surfaces as HTTP 409
  `workflow_transaction_recovery_conflict`; browser guidance requires operator
  reconciliation and does not retry the interrupted operation. A committed
  journal is only removed.
- A symlinked `workflow_state`, `workflow_validations`, or `workflow_dev` root,
  or a nested internal-state path that resolves outside its root, fails before
  reading, locking, writing, replacing, or removing state. A symlink naming the
  workspace itself remains usable after evaluation.
- A failed durable parent creation, atomic replacement, or logical removal
  returns an error; once a transaction journal has been durably prepared, it
  remains available for recovery. On Windows, a crash may leave a same-parent
  deletion tombstone, but does not restore the removed original name.
- HTTP workflow and draft test runs create a request-scoped agent/tool runtime
  for `agent/*`, `tool/*`, and `mcp/*` steps and close it after the run
  completes.
- PicoClaw native `function/workflow.*` and `function/git.*` targets run
  without an embedding `FunctionRunner`; other `function/*` targets still
  require an embedding runtime with a Go `FunctionRunner`.
- Native git functions reject repositories outside the workspace and require a
  local git repository.
- Agent execution optimization requires a structured output contract before
  splitting; without one it runs as a single normal agent call so child results
  cannot be combined ambiguously.
- Split calibration falls back to a single full agent run when grouped
  and split structured outputs do not match the required number of times.
  Passing calibrations can be reused by an agent-local cache keyed by the model,
  language, repository/scope identity, schema, prompt, tasks, strategy, and
  chunking shape. Exact hits follow the stored cadence; similar hits create a
  provisional new-key cache entry, reuse once, verify on the next matching use,
  and either promote with inherited confidence or reset as fresh. Low split-fit
  scores keep probes more aggressive even after success.
- Task splitting uses textual `Tasks:` entries from the agent file as
  semantic responsibilities; it does not treat them as workflow DAG steps.
- Model optimization only replaces the model when configured candidate
  price metadata or model config price metadata identifies a lower estimated
  child-run cost. Subscription-backed models may point at an equivalent
  API-priced model for estimates.
- Workflows without a current compatible validation stamp do not run from the
  dashboard, CLI, workflow tool, retries, or automatic triggers.
- An absent or empty `on.event`, blank/empty filter, missing required
  actor/subject/attribute, or invalid workflow never matches a durable event.
  Explicit `types: "*"` is the reviewable catch-all.
- Durable event runs have no implicit chat delivery. A workflow must name an
  explicit target for message actions, and existing tool/security policy still
  applies.
- A recovered durable dispatch with an existing running run is canceled as
  interrupted and marked failed instead of repeating jobs or steps. External
  effects that require exactly-once semantics must use the deterministic
  dispatch/run ID as a provider idempotency key.
- Immediate channel, command, schedule, and runtime-event launches execute the
  exact validated workflow snapshot selected at match/cache time. Durable
  event dispatches reject content-revision drift or a no-longer-matching
  persisted event before run creation.
- Adding `on.event` changes the workflow engine/schema validator fingerprint,
  so workflows stamped by a binary that ignored this trigger remain blocked
  until revalidation.
- Adding `tools: none` changes the workflow engine/validator identity. A
  classifier stamped by the new runtime cannot run on an older binary that
  would have ignored the no-tool restriction.
- Installing the code-review workflow is idempotent when the target file already
  exists; overwrite requires an explicit force request.
- Installing the GitHub issue-triage workflow is also idempotent and never
  enables GitHub ingress, an MCP server, or the workflow engine. The operator
  configures those separately and revalidates the installed definition.
- GitHub MCP comments have no provider idempotency key. The template writes a
  deterministic event marker for audit/search, but does not read existing
  comments before writing; an explicit workflow retry, event replay, or GitHub
  redelivery after retention pruning can post another comment. Automatic
  dispatch recovery still refuses to repeat a run that reached its durable
  pre-effect boundary.
- A malformed code-review filter, unsupported file inventory shape, or filter
  result that excludes every useful file fails or produces an empty review scope
  through normal step outputs; the raw filter artifact remains inspectable.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-WORKFLOW-001` | [pkg/workflows/resolver_test.go](../../pkg/workflows/resolver_test.go), [pkg/workflows/resolver.go](../../pkg/workflows/resolver.go) |
| `FR-WORKFLOW-002`, `FR-WORKFLOW-003`, `FR-WORKFLOW-004`, `FR-WORKFLOW-005`, `FR-WORKFLOW-007`, `FR-WORKFLOW-014` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/types.go](../../pkg/workflows/types.go), [pkg/workflows/validator.go](../../pkg/workflows/validator.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-WORKFLOW-006`, `FR-WORKFLOW-008`, `FR-WORKFLOW-011` | [pkg/workflows/catalog_trigger_test.go](../../pkg/workflows/catalog_trigger_test.go), [pkg/workflows/trigger.go](../../pkg/workflows/trigger.go), [pkg/workflows/runtime_trigger.go](../../pkg/workflows/runtime_trigger.go), [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go), [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go), [pkg/agent/workflow_automations_test.go](../../pkg/agent/workflow_automations_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-WORKFLOW-009`, `FR-WORKFLOW-012`, `FR-WORKFLOW-013` | [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/store.go](../../pkg/workflows/store.go), [pkg/workflows/store_test.go](../../pkg/workflows/store_test.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/config/config_test.go](../../pkg/config/config_test.go) |
| `FR-WORKFLOW-010`, `FR-WORKFLOW-015` | [cmd/picoclaw/internal/workflow](../../cmd/picoclaw/internal/workflow), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [pkg/tools/workflow_test.go](../../pkg/tools/workflow_test.go), [cmd/picoclaw/main_test.go](../../cmd/picoclaw/main_test.go) |
| `FR-WORKFLOW-016` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/tools/fs/send_file_test.go](../../pkg/tools/fs/send_file_test.go) |
| `FR-WORKFLOW-017`, `FR-WORKFLOW-018`, `FR-WORKFLOW-019` | [pkg/workflows/development.go](../../pkg/workflows/development.go), [pkg/workflows/compatibility.go](../../pkg/workflows/compatibility.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_ai.go](../../web/backend/api/workflow_ai.go), [web/backend/api/workflow_runtime.go](../../web/backend/api/workflow_runtime.go), [web/backend/api/workflow_ai_test.go](../../web/backend/api/workflow_ai_test.go), [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [pkg/tools/workflow_test.go](../../pkg/tools/workflow_test.go), [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go) |
| `FR-WORKFLOW-020` | [pkg/workflows/native_functions.go](../../pkg/workflows/native_functions.go), [pkg/workflows/native_functions_test.go](../../pkg/workflows/native_functions_test.go), [pkg/workflows/internal_state_paths_test.go](../../pkg/workflows/internal_state_paths_test.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [web/backend/api/workflow_ai.go](../../web/backend/api/workflow_ai.go) |
| `FR-WORKFLOW-021` | [docs/features/agent-execution-optimization.md](agent-execution-optimization.md), [pkg/workflows/agent_output.go](../../pkg/workflows/agent_output.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx) |
| `FR-WORKFLOW-022` | [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/workflows/templates.go](../../pkg/workflows/templates.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go) |
| `FR-WORKFLOW-023` | [pkg/workflows/event_trigger.go](../../pkg/workflows/event_trigger.go), [pkg/workflows/event_trigger_test.go](../../pkg/workflows/event_trigger_test.go), [pkg/workflows/event_dispatcher.go](../../pkg/workflows/event_dispatcher.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go) |
| `FR-WORKFLOW-024` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/compatibility.go](../../pkg/workflows/compatibility.go), [pkg/workflows/event_trigger_test.go](../../pkg/workflows/event_trigger_test.go) |
| `FR-WORKFLOW-025` | [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/workflows/templates.go](../../pkg/workflows/templates.go), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go) |
| `FR-WORKFLOW-026` | [pkg/workflows/editor_test.go](../../pkg/workflows/editor_test.go), [pkg/workflows/event_trigger_test.go](../../pkg/workflows/event_trigger_test.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/eventing/operator/handler_test.go](../../pkg/eventing/operator/handler_test.go), [web/backend/api/workflow_editor_test.go](../../web/backend/api/workflow_editor_test.go), [web/backend/api/workflow_ai_test.go](../../web/backend/api/workflow_ai_test.go), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows](../../web/frontend/src/components/workflows), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-WORKFLOW-027` | [pkg/workflows/template_catalog_test.go](../../pkg/workflows/template_catalog_test.go), [pkg/workflows/template_recovery_classification_test.go](../../pkg/workflows/template_recovery_classification_test.go), [pkg/workflows/template_transaction.go](../../pkg/workflows/template_transaction.go), [pkg/workflows/transaction_recovery.go](../../pkg/workflows/transaction_recovery.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [web/backend/api/workflow_templates_test.go](../../web/backend/api/workflow_templates_test.go), [web/backend/api/workflow_recovery_errors_test.go](../../web/backend/api/workflow_recovery_errors_test.go), [web/frontend/src/components/workflows/workflow-template-catalog.test.tsx](../../web/frontend/src/components/workflows/workflow-template-catalog.test.tsx), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-WORKFLOW-028` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/workflow_settings_test.go](../../web/backend/api/workflow_settings_test.go), [web/frontend/src/components/workflows/workflow-settings-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-settings-dialog.test.tsx), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts) |
| `FR-WORKFLOW-029` | [pkg/workflows/dependencies_test.go](../../pkg/workflows/dependencies_test.go), [pkg/agent/workflow_dependencies_test.go](../../pkg/agent/workflow_dependencies_test.go), [pkg/mcp/toolname_test.go](../../pkg/mcp/toolname_test.go), [web/backend/api/workflow_dependencies_test.go](../../web/backend/api/workflow_dependencies_test.go), [web/backend/api/workflow_runtime_test.go](../../web/backend/api/workflow_runtime_test.go), [web/frontend/src/components/workflows/workflow-publish-readiness.test.tsx](../../web/frontend/src/components/workflows/workflow-publish-readiness.test.tsx), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-WORKFLOW-030` | [pkg/workflows/development_get_recovery_test.go](../../pkg/workflows/development_get_recovery_test.go), [pkg/workflows/development_revision_test.go](../../pkg/workflows/development_revision_test.go), [pkg/workflows/development_publish_test.go](../../pkg/workflows/development_publish_test.go), [pkg/workflows/transaction_recovery.go](../../pkg/workflows/transaction_recovery.go), [pkg/workflows/mutation_lock_test.go](../../pkg/workflows/mutation_lock_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [web/backend/api/workflow_exact_yaml_test.go](../../web/backend/api/workflow_exact_yaml_test.go), [web/backend/api/workflow_publish_test.go](../../web/backend/api/workflow_publish_test.go), [web/backend/api/workflow_recovery_errors_test.go](../../web/backend/api/workflow_recovery_errors_test.go), [web/frontend/src/components/workflows/workflow-publish-readiness.test.tsx](../../web/frontend/src/components/workflows/workflow-publish-readiness.test.tsx), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-WORKFLOW-031` | [pkg/workflows/internal_state_paths_test.go](../../pkg/workflows/internal_state_paths_test.go), [pkg/workflows/internal_state_paths.go](../../pkg/workflows/internal_state_paths.go), [pkg/workflows/mutation_lock_test.go](../../pkg/workflows/mutation_lock_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go) |

## Implementation Anchors

- [pkg/workflows/types.go](../../pkg/workflows/types.go)
- [pkg/workflows/resolver.go](../../pkg/workflows/resolver.go)
- [pkg/workflows/validator.go](../../pkg/workflows/validator.go)
- [pkg/workflows/executor.go](../../pkg/workflows/executor.go)
- [pkg/workflows/native_functions.go](../../pkg/workflows/native_functions.go)
- [pkg/workflows/store.go](../../pkg/workflows/store.go)
- [pkg/workflows/development.go](../../pkg/workflows/development.go)
- [pkg/workflows/development_revision.go](../../pkg/workflows/development_revision.go)
- [pkg/workflows/development_publish.go](../../pkg/workflows/development_publish.go)
- [pkg/workflows/transaction_recovery.go](../../pkg/workflows/transaction_recovery.go)
- [pkg/workflows/internal_state_paths.go](../../pkg/workflows/internal_state_paths.go)
- [pkg/workflows/mutation_lock.go](../../pkg/workflows/mutation_lock.go)
- [pkg/workflows/dependencies.go](../../pkg/workflows/dependencies.go)
- [pkg/workflows/compatibility.go](../../pkg/workflows/compatibility.go)
- [pkg/workflows/trigger.go](../../pkg/workflows/trigger.go)
- [pkg/workflows/runtime_trigger.go](../../pkg/workflows/runtime_trigger.go)
- [pkg/workflows/event_trigger.go](../../pkg/workflows/event_trigger.go)
- [pkg/workflows/event_dispatcher.go](../../pkg/workflows/event_dispatcher.go)
- [pkg/workflows/editor.go](../../pkg/workflows/editor.go)
- [pkg/workflows/graph.go](../../pkg/workflows/graph.go)
- [pkg/workflows/reload.go](../../pkg/workflows/reload.go)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/workflows/template_catalog.go](../../pkg/workflows/template_catalog.go)
- [pkg/workflows/template_transaction.go](../../pkg/workflows/template_transaction.go)
- [pkg/tools/workflow.go](../../pkg/tools/workflow.go)
- [pkg/agent/workflow_dependencies.go](../../pkg/agent/workflow_dependencies.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
- [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go)
- [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go)
- [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go)
- [pkg/agent/workflow_eventing.go](../../pkg/agent/workflow_eventing.go)
- [web/backend/api/workflow_ai.go](../../web/backend/api/workflow_ai.go)
- [web/backend/api/workflow_editor.go](../../web/backend/api/workflow_editor.go)
- [web/backend/api/workflow_runtime.go](../../web/backend/api/workflow_runtime.go)
- [web/backend/api/workflow_dependencies.go](../../web/backend/api/workflow_dependencies.go)
- [web/backend/api/workflow_publish.go](../../web/backend/api/workflow_publish.go)
- [web/backend/api/workflow_settings.go](../../web/backend/api/workflow_settings.go)
- [web/backend/api/workflow_templates.go](../../web/backend/api/workflow_templates.go)
- [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx)
- [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts)
- [pkg/bus/types.go](../../pkg/bus/types.go)
- [pkg/tools/shared/base.go](../../pkg/tools/shared/base.go)

## Surface Ownership

Owns: CODE pkg/workflows/**
Owns: CODE pkg/agent/workflow_*.go
Owns: CODE pkg/tools/workflow.go
Owns: CODE cmd/picoclaw/internal/workflow/**
Owns: CODE web/backend/api/workflows.go
Owns: CODE web/backend/api/workflow_ai.go
Owns: CODE web/backend/api/workflow_editor.go
Owns: CODE web/backend/api/workflow_event_context.go
Owns: CODE web/backend/api/workflow_runtime.go
Owns: CODE web/backend/api/workflow_dependencies.go
Owns: CODE web/backend/api/workflow_publish.go
Owns: CODE web/backend/api/workflow_settings.go
Owns: CODE web/backend/api/workflow_templates.go
Owns: CODE web/frontend/src/api/workflows.ts
Owns: CODE web/frontend/src/components/workflows/**
Owns: CODE web/frontend/src/routes/agent/workflows.tsx
Owns: CONFIG.workflows*
Owns: CONFIG.tools.workflow*
Owns: CLI cmd/picoclaw/internal/workflow/*
Owns: HTTP * /api/workflows*
Owns: TEST pkg/workflows/*
Owns: TEST pkg/agent/workflow_runtime_test.go
Owns: TEST cmd/picoclaw/internal/workflow/*
Owns: TOOL workflow
Owns: EVENT workflow.*
