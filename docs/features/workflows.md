# Workflows And Reusable Automation

## Feature ID

`FR-WORKFLOW`

## Behavior Summary

PicoClaw workflows define GitHub Actions-shaped automation that can run from
manual calls, channel messages, slash-style commands, cron schedules, runtime
events, and other workflows. Local reusable workflows use canonical refs such
as `workflows/summarize-text.yml`, can declare typed `workflow_call` inputs,
secrets, and outputs, can map or inherit secrets, and can inherit conversation
session and delivery context from a channel-triggered parent run.
The workflow dashboard also owns the end-to-end development cycle: one active
workflow draft or repair session may exist at a time, a new brief starts with
AI-authored workflow YAML by default, deterministic scaffold remains available
as a fallback, and publish validates, writes, reloads, and stamps the workflow
against the current PicoClaw runtime. Native workflow functions provide common
state, artifact, git inventory, and git filter primitives so AI-authored
workflows do not need helper scripts for durable planning and reporting.

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
  steps can read history without writing it, and delivery is separate from
  session memory. Only one workflow development session can be active at a
  time; release/version changes force deterministic revalidation before
  published workflows can trigger or run.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-WORKFLOW-001` | MUST | Local reusable workflow refs resolve from `workspace/workflows/` using canonical `workflows/<file>.yml` or `.yaml` refs; absolute paths, parent traversal, non-workflow prefixes, invalid extensions, and symlink escapes are rejected. | Reusable workflows need predictable safe local addressing. |
| `FR-WORKFLOW-002` | MUST | Workflow YAML accepts unquoted GitHub-style `on`, job maps, string-or-list `needs`, job-level `uses: workflows/<file>.yml`, and step-level `uses` targets for `agent/`, `tool/`, `mcp/`, and `function/`. | Developers should be able to write workflows that look familiar to GitHub Actions users. |
| `FR-WORKFLOW-003` | MUST | `on.workflow_call` validates typed inputs, declared secrets, and output value expressions before the workflow is callable. | Reusable workflows need an explicit contract between caller and callee. |
| `FR-WORKFLOW-004` | MUST | Job dependencies reference existing jobs and reject dependency cycles before execution starts. | Workflow runs must fail fast instead of deadlocking or producing partial output. |
| `FR-WORKFLOW-005` | MUST | Reusable workflow calls are supported at job level; step-level `uses: workflows/...` is rejected in v1. | Parent/child run state, outputs, and secret binding are job-scoped. |
| `FR-WORKFLOW-006` | MUST | Channel-message and standalone command triggers can filter by channel, chat, sender, mention, command, regex, declared command args, and passthrough behavior, and can bind `conversation.session` and `conversation.delivery` modes. | Chat workflows need precise activation and duplicate-reply control. |
| `FR-WORKFLOW-007` | MUST | Conversation pipelines support agent step modes `history: read_write`, `history: read_only`, `history: none`, `session: inherit`, explicit `key:` sessions, and cache modes `session`, `agent`, `none`, or explicit `key:`. | Chat pipelines need classifier/enrichment steps that can follow context without polluting durable chat history. |
| `FR-WORKFLOW-008` | MUST | A channel-triggered run stores normalized event context and delivery metadata so `tool/message` can default to the same Telegram topic or Slack thread. | Delivery should be automatic while remaining separate from session memory. |
| `FR-WORKFLOW-009` | MUST | Runtime execution persists parent and child run records, job/step status, input/output snapshots, session key, delivery context, and event JSONL under `workspace/workflow_runs/`. | Runs need restart-safe inspection and auditable parent/child links. |
| `FR-WORKFLOW-010` | MUST | CLI, HTTP, and agent-tool surfaces expose list, validate, run, cancel, retry, status, graph, reload, and event inspection operations through the shared workflow parser, validator, executor, and file run store. | Operators, UI, and agents should not fork workflow behavior. |
| `FR-WORKFLOW-011` | MUST | `on.schedule` cron triggers and `on.runtime_event` filters run while the agent loop is active and use the same executor, depth, timeout, concurrency, session, and delivery rules as channel-triggered workflows. | Workflows need autonomous automation beyond inbound chat messages. |
| `FR-WORKFLOW-012` | MUST | Runs can be canceled and retried; cancellation marks the persisted run canceled and stops before later jobs or steps, while retry creates a new run linked to the original run and reuses the original ref, inputs, event, session, and delivery. | Operators need safe intervention without losing audit history. |
| `FR-WORKFLOW-013` | MUST | Runtime limits enforce configured max concurrent top-level runs, default per-run timeout, max reusable-call depth, and retention pruning for terminal runs. | Automation must be bounded in resource use and storage. |
| `FR-WORKFLOW-014` | MUST | Reusable workflow calls support `secrets: inherit`, explicit secret mapping expressions, and `continue-on-error` on jobs and steps. | Shared workflows need GitHub-like reuse and optional child failure handling. |
| `FR-WORKFLOW-015` | MUST | HTTP exposes workflow run events as JSON and SSE, plus child/retry run graph data; the dashboard exposes workflow definitions, manual run launch, run list, run details, events, graph, reload, cancel, and retry. | Operators need live inspection and control without shell access. |
| `FR-WORKFLOW-016` | MUST | Workflow tool steps that return handled media deliver attachments, generated audio, or files back to the same delivery target and preserve Telegram topics or Slack threads when present. | File and TTS workflows must reply in the same discussion as text workflows. |
| `FR-WORKFLOW-017` | MUST | Workflow development uses a single persisted active session with start, revise, AI-revise, validate, test-run, publish, and discard operations; starting another development session while one is active or sending concurrent development mutations returns a conflict. HTTP and the agent-callable workflow tool expose this lifecycle so a user can ask AI to draft, test, and publish a workflow without scripts. AI revision receives existing workflow refs plus bounded runtime agent/tool capability context so drafts can target dashboard-runnable steps. Repository-wide review prompts produce an explicit draft workflow that inventories the requested commit and feeds selected files into a managed scope-split review step. The active session persists the latest draft-test snapshot, clears it when the executable draft YAML or target ref changes, preserves it across prompt-only or no-op saves, and publish requires a current successful draft test. | AI-assisted authoring is simpler and avoids divergent pending edits. |
| `FR-WORKFLOW-018` | MUST | Workflow compatibility stamps record the PicoClaw version, git commit, workflow engine version, schema version, validator fingerprint, workflow hash, validation status, and issues; version or hash changes mark workflows pending revalidation and block automatic/manual execution until revalidated. | Releases can invalidate workflow semantics, so existing automation must fail closed until checked. |
| `FR-WORKFLOW-019` | MUST | HTTP-triggered workflow runs and draft test runs execute `agent/*`, `tool/*`, and `mcp/*` steps through the configured PicoClaw agent/tool runtime and persist step outputs in normal run records. Tool and MCP step results that return a JSON object or array expose the parsed value as `outputs.json`; object results also promote non-conflicting top-level fields for downstream expressions. | AI-authored workflows must be testable from the dashboard before publish, and later workflow steps need structured tool data without parsing prose. |
| `FR-WORKFLOW-020` | MUST | Native workflow functions expose workflow-scoped durable state, workflow run artifacts, git commit inventory, and path-policy filtering through `function/workflow.state`, `function/workflow.artifact`, `function/git.inventory`, and `function/git.filter`. | AI-authored workflows need common state, artifact, repository-inspection, and deterministic filter-application primitives without opaque helper scripts or domain-specific helpers in core. |
| `FR-WORKFLOW-021` | MUST | Agent workflow steps integrate with the dedicated [Managed Agent Execution](agent-execution-optimization.md) contract: `with.output` declares structured JSON output, `with.managed` enables generic hidden scope/task/hybrid splitting, and the visible workflow step persists the combined structured result plus managed diagnostics. | AI-driven workflow development needs generic, inspectable agent adaptation that preserves output quality while reducing token and model spend. |
| `FR-WORKFLOW-022` | MUST | PicoClaw can install a local `workflows/code-review.yml` template that acquires a git workspace, inventories repository structure with workspace/file links, releases the workspace before asking an agent to propose include/exclude globs, reacquires only long enough for `git.filter` to refresh selected workspace file links, releases again before model review, and runs an agent step with structured JSON review output; workflow tool steps expose JSON object results as addressable step outputs for downstream workflow expressions. | Code review automation needs a local hosted workflow that composes the git workspace feature with AI-assisted path selection, deterministic filter enforcement, and inspectable review output. |

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
```

Run records include run ID, workflow ref, status, parent run ID, child run IDs,
caller job ID, retry source run ID, input/output/event snapshots, embedded job
and step snapshots, session key, delivery context, timestamps, cancel metadata,
and error summaries. Delivery context stores outbound target metadata such as
channel, chat ID, topic/thread identifier, and reply target. Session context
stores the memory key used by agent steps.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| File | `workspace/workflows/*.yml`, `workspace/workflows/*.yaml` | GitHub-style workflow definitions with `on`, `jobs`, `needs`, `uses`, `with`, `if`, `outputs`, `schedule`, `runtime_event`, and `workflow_call`. | `FR-WORKFLOW-001` through `FR-WORKFLOW-016` |
| Go API | `pkg/workflows.Parse`, `Resolver.ResolveLocal`, `Validate`, `Executor.Run`, `Executor.Retry`, `FileRunStore`, `MatchChannelMessage`, `MatchCommandMessage`, `MatchRuntimeEvent`, `BuildRunGraph`, `ReloadLocal`, `InstallWorkflowTemplate` | Parse GitHub-shaped YAML, normalize local reusable refs, reject unsafe refs, validate static workflow contracts, install local workflow templates, match triggers, run/retry/cancel workflows, build run graphs, reload definitions, and persist run state. | `FR-WORKFLOW-001` through `FR-WORKFLOW-016`, `FR-WORKFLOW-022` |
| Config | `workflows.*`, `tools.workflow` | Global enablement, workflow tool enablement, max call depth, definitions directory, concurrency, timeout, and retention defaults. | `FR-WORKFLOW-009`, `FR-WORKFLOW-013` |
| CLI | `picoclaw workflow install/list/compatibility/revalidate/validate/reload/run/cancel/retry/status/events/graph` | Install local workflow templates, manage definitions, compatibility stamps, and runs through the same workflow runtime and file run store used by agent tools. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015`, `FR-WORKFLOW-018`, `FR-WORKFLOW-022` |
| HTTP | `/api/workflows*`, `/api/workflows/runs*`, `/api/workflows/development*`, `/api/workflows/compatibility`, `/api/workflows/revalidate` | List, validate, reload, run, cancel, retry, inspect, stream workflow events, read run graph data, manage the singleton development session, run configured-agent YAML revisions, test active drafts inline or asynchronously after a run record is persisted, reject draft-changing development mutations while the current draft test is still running, execute configured agent/tool/MCP workflow steps synchronously or asynchronously, publish drafts, and revalidate release compatibility. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015`, `FR-WORKFLOW-017`, `FR-WORKFLOW-018`, `FR-WORKFLOW-019` |
| UI | `/agent/workflows` | Two-mode workflow console: Develop shows singleton start readiness, starts new briefs with AI by default, resumes the singleton AI brief/YAML development cycle, marks the one active draft, sends active drafts to the configured agent for YAML revision, offers deterministic scaffold fallback, validates and test-runs drafts asynchronously with inline JSON validation for inputs/secrets/session/delivery context through configured workflow runtime steps, preserves structured validation feedback from failed draft tests, can ask AI to repair the current draft with the latest failed draft-test status, run ID, compact run/job/step state, recent event payloads, and error context, restores the latest draft-test result when resuming the active session, treats a running draft test as the active development operation, gates publish on a current successful draft test, shows publish readiness with the next blocking reason, shows the active development operation while mutations run, opens the repair/review queue with compatibility issue summaries, can start AI review or AI repair directly from blocked compatibility entries, and after publish switches Operate to the published workflow; Operate shows definitions, compatibility status, a GitHub-style manual run popover generated from declared `workflow_call` inputs and secrets with advanced session/delivery/raw secret JSON controls, compatibility-gated asynchronous launch, inline payload validation, the selected workflow run-readiness reason, runs, selected run detail, persisted delivery and trigger event context, job and step outputs, live streamed event payloads with polling fallback, graph, cancel, compatibility-gated retry with retry-secret JSON validation, reload, and refresh. | `FR-WORKFLOW-015`, `FR-WORKFLOW-017`, `FR-WORKFLOW-018`, `FR-WORKFLOW-019` |
| Managed agent step | `uses: agent/*` with `with.output`, `with.managed`, and optional `with.scope` | Workflow-owned output schemas are injected into the agent prompt, parsed from the response, repaired once by default, validated locally, and exposed as `structured`. Managed options choose split strategy, fixed or token-adaptive chunk sizes, calibration sample/match/cache policy, parallel child limit, model candidates with price metadata, and effort optimization. Child runs are hidden from chat history by default and publish one combined structured result plus `managed` diagnostics. | `FR-WORKFLOW-007`, `FR-WORKFLOW-009`, `FR-WORKFLOW-019`, `FR-WORKFLOW-021` |
| Tool | `workflow` | Agent-callable list, compatibility, revalidate, validate, reload, run, cancel, retry, status, graph, events, `dev_status`, `dev_start`, `dev_revise`, `dev_validate`, `dev_test`, `dev_publish`, and `dev_discard` actions. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015`, `FR-WORKFLOW-017`, `FR-WORKFLOW-018` |
| Native functions | `function/workflow.state`, `function/workflow.artifact`, `function/git.inventory`, `function/git.filter` | Store/retrieve workflow-owned JSON state, write/read/list run artifacts, inventory git files by commit and blob hash inside a workspace, and apply structured include/exclude path policies to inventory output. `git.inventory` accepts a git workspace object or compatible working directory and emits file metadata plus workspace/file source references without embedding file content. `git.filter` accepts inventory files plus AI- or user-produced `includeGlobs`, `excludeGlobs`, and `selectedPaths`, supports recursive `**` globs, deterministically refreshes selected file source references for the active workspace, and does not embed file content in JSON output. Domain workflows compose these primitives for planning, reports, review scopes, and reuse decisions. | `FR-WORKFLOW-020` |
| Events | `workflow.*` | Trigger, run, job, and step lifecycle events. | `FR-WORKFLOW-008`, `FR-WORKFLOW-009`, `FR-WORKFLOW-011`, `FR-WORKFLOW-015` |

## Algorithms And Ordering

1. Normalize local workflow refs, reject unsafe paths, and resolve the canonical
   path under the workflow root.
2. Parse YAML into typed trigger, job, step, `workflow_call`, session, and
   delivery contracts.
3. Validate input types, output expressions, unknown dependencies, graph cycles,
   allowed `uses` targets, schedule cron expressions, runtime-event filters,
   channel trigger regex, and agent step history/cache modes.
4. For channel and command triggers, match normalized `bus.InboundMessage`
   facts and bind event, session, and delivery context before normal agent
   handling.
5. For schedule and runtime-event triggers, agent-loop automation goroutines
   load valid definitions, match trigger data, publish `workflow.triggered`,
   and start runs with the same executor configuration used by chat triggers.
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
    agent as the default first draft path, revise the active draft locally or
    through the configured agent with existing workflow refs plus registered
    agent/tool target context, extract returned workflow YAML, validate the
    YAML, optionally test-run the draft inline with persisted run records,
    publish by atomically writing `workspace/workflows/<file>.yml`, revalidate
    the catalog, archive the session, and remove the active marker.
13. For release revalidation, compare the current PicoClaw runtime identity and
    workflow hash with `workflow_validations/manifest.json`, classify stale
    workflows as pending, run deterministic validation on demand, and block
    stale or invalid workflow execution.
14. For local templates, install validated YAML into the configured workflow
    definitions directory, leave existing files unchanged unless overwrite is
    requested, and revalidate compatibility through the same catalog path used
    by ordinary workflow definitions.

## Cross-Feature Behavior

Workflows use chat channels as trigger sources and delivery sinks. Routing and
session memory define conversation scope for channel-triggered runs. Agent
conversations provide the agent step execution path and provider prompt cache
keys. Tool execution, MCP, skills, hooks, and security policies govern side
effects exactly as they do in normal agent turns. Runtime events expose
workflow trigger, run, job, and step lifecycle state.
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
- Draft test runs use `draft:<target_ref>` refs and persist normal run records
  for inspection without writing the draft into `workspace/workflows/`.
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
  result; backend publish still revalidates deterministically before writing
  the workflow file and compatibility stamp.
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
- Installing the code-review workflow is idempotent when the target file already
  exists; overwrite requires an explicit force request.
- A malformed code-review filter, unsupported file inventory shape, or filter
  result that excludes every useful file fails or produces an empty review scope
  through normal step outputs; the raw filter artifact remains inspectable.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-WORKFLOW-001` | [pkg/workflows/resolver_test.go](../../pkg/workflows/resolver_test.go), [pkg/workflows/resolver.go](../../pkg/workflows/resolver.go) |
| `FR-WORKFLOW-002`, `FR-WORKFLOW-003`, `FR-WORKFLOW-004`, `FR-WORKFLOW-005`, `FR-WORKFLOW-007`, `FR-WORKFLOW-014` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/types.go](../../pkg/workflows/types.go), [pkg/workflows/validator.go](../../pkg/workflows/validator.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-WORKFLOW-006`, `FR-WORKFLOW-008`, `FR-WORKFLOW-011` | [pkg/workflows/catalog_trigger_test.go](../../pkg/workflows/catalog_trigger_test.go), [pkg/workflows/trigger.go](../../pkg/workflows/trigger.go), [pkg/workflows/runtime_trigger.go](../../pkg/workflows/runtime_trigger.go), [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go), [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go), [pkg/agent/workflow_automations_test.go](../../pkg/agent/workflow_automations_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-WORKFLOW-009`, `FR-WORKFLOW-012`, `FR-WORKFLOW-013` | [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/store.go](../../pkg/workflows/store.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/config/config_test.go](../../pkg/config/config_test.go) |
| `FR-WORKFLOW-010`, `FR-WORKFLOW-015` | [cmd/picoclaw/internal/workflow](../../cmd/picoclaw/internal/workflow), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [pkg/tools/workflow_test.go](../../pkg/tools/workflow_test.go), [cmd/picoclaw/main_test.go](../../cmd/picoclaw/main_test.go) |
| `FR-WORKFLOW-016` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/tools/fs/send_file_test.go](../../pkg/tools/fs/send_file_test.go) |
| `FR-WORKFLOW-017`, `FR-WORKFLOW-018`, `FR-WORKFLOW-019` | [pkg/workflows/development.go](../../pkg/workflows/development.go), [pkg/workflows/compatibility.go](../../pkg/workflows/compatibility.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_ai.go](../../web/backend/api/workflow_ai.go), [web/backend/api/workflow_runtime.go](../../web/backend/api/workflow_runtime.go), [web/backend/api/workflow_ai_test.go](../../web/backend/api/workflow_ai_test.go), [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [pkg/tools/workflow_test.go](../../pkg/tools/workflow_test.go), [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go) |
| `FR-WORKFLOW-020` | [pkg/workflows/native_functions.go](../../pkg/workflows/native_functions.go), [pkg/workflows/native_functions_test.go](../../pkg/workflows/native_functions_test.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [web/backend/api/workflow_ai.go](../../web/backend/api/workflow_ai.go) |
| `FR-WORKFLOW-021` | [docs/features/agent-execution-optimization.md](agent-execution-optimization.md), [pkg/workflows/agent_output.go](../../pkg/workflows/agent_output.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx) |
| `FR-WORKFLOW-022` | [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/workflows/templates.go](../../pkg/workflows/templates.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go) |

## Implementation Anchors

- [pkg/workflows/types.go](../../pkg/workflows/types.go)
- [pkg/workflows/resolver.go](../../pkg/workflows/resolver.go)
- [pkg/workflows/validator.go](../../pkg/workflows/validator.go)
- [pkg/workflows/executor.go](../../pkg/workflows/executor.go)
- [pkg/workflows/native_functions.go](../../pkg/workflows/native_functions.go)
- [pkg/workflows/store.go](../../pkg/workflows/store.go)
- [pkg/workflows/development.go](../../pkg/workflows/development.go)
- [pkg/workflows/compatibility.go](../../pkg/workflows/compatibility.go)
- [pkg/workflows/trigger.go](../../pkg/workflows/trigger.go)
- [pkg/workflows/runtime_trigger.go](../../pkg/workflows/runtime_trigger.go)
- [pkg/workflows/graph.go](../../pkg/workflows/graph.go)
- [pkg/workflows/reload.go](../../pkg/workflows/reload.go)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/tools/workflow.go](../../pkg/tools/workflow.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
- [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go)
- [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go)
- [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go)
- [web/backend/api/workflow_ai.go](../../web/backend/api/workflow_ai.go)
- [web/backend/api/workflow_runtime.go](../../web/backend/api/workflow_runtime.go)
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
Owns: CODE web/backend/api/workflow_runtime.go
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
