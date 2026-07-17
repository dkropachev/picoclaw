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
  session memory.

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
| `FR-WORKFLOW-015` | MUST | HTTP exposes workflow run events as JSON and SSE, plus child/retry run graph data; the dashboard exposes workflow definitions, run list, run details, events, graph, reload, cancel, and retry. | Operators need live inspection and control without shell access. |
| `FR-WORKFLOW-016` | MUST | Workflow tool steps that return handled media deliver attachments, generated audio, or files back to the same delivery target and preserve Telegram topics or Slack threads when present. | File and TTS workflows must reply in the same discussion as text workflows. |

## Data And State Model

Workflow definitions live under `workspace/workflows/`. A local ref
`workflows/summarize-text.yml` resolves to
`workspace/workflows/summarize-text.yml`; `./workflows/summarize-text.yml` may
be accepted as input but canonicalizes to the no-dot form.

Workflow runs persist under:

```text
workspace/workflow_runs/<run_id>/run.json
workspace/workflow_runs/<run_id>/events.jsonl
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
| Go API | `pkg/workflows.Parse`, `Resolver.ResolveLocal`, `Validate`, `Executor.Run`, `Executor.Retry`, `FileRunStore`, `MatchChannelMessage`, `MatchCommandMessage`, `MatchRuntimeEvent`, `BuildRunGraph`, `ReloadLocal` | Parse GitHub-shaped YAML, normalize local reusable refs, reject unsafe refs, validate static workflow contracts, match triggers, run/retry/cancel workflows, build run graphs, reload definitions, and persist run state. | `FR-WORKFLOW-001` through `FR-WORKFLOW-016` |
| Config | `workflows.*`, `tools.workflow` | Global enablement, workflow tool enablement, max call depth, definitions directory, concurrency, timeout, and retention defaults. | `FR-WORKFLOW-009`, `FR-WORKFLOW-013` |
| CLI | `picoclaw workflow list/validate/reload/run/cancel/retry/status/events/graph` | Manage definitions and runs through the same workflow runtime and file run store used by agent tools. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015` |
| HTTP | `/api/workflows*`, `/api/workflows/runs*` | List, validate, reload, run, cancel, retry, inspect, stream workflow events, and read run graph data. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015` |
| UI | `/agent/workflows` | Dashboard page for definitions, runs, selected run detail, events, graph, cancel, retry, reload, and refresh. | `FR-WORKFLOW-015` |
| Tool | `workflow` | Agent-callable list, validate, reload, run, cancel, retry, status, graph, and events actions. | `FR-WORKFLOW-010`, `FR-WORKFLOW-012`, `FR-WORKFLOW-015` |
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
   PicoClaw policies, hooks, redaction, and channel delivery.
8. Check cancellation between jobs/steps, enforce per-run timeout and top-level
   concurrency, and persist terminal status with cancel/error metadata.
9. Persist run and event state with embedded job and step snapshots before and
   after side effects.

## Cross-Feature Behavior

Workflows use chat channels as trigger sources and delivery sinks. Routing and
session memory define conversation scope for channel-triggered runs. Agent
conversations provide the agent step execution path and provider prompt cache
keys. Tool execution, MCP, skills, hooks, and security policies govern side
effects exactly as they do in normal agent turns. Runtime events expose
workflow trigger, run, job, and step lifecycle state.

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

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-WORKFLOW-001` | [pkg/workflows/resolver_test.go](../../pkg/workflows/resolver_test.go), [pkg/workflows/resolver.go](../../pkg/workflows/resolver.go) |
| `FR-WORKFLOW-002`, `FR-WORKFLOW-003`, `FR-WORKFLOW-004`, `FR-WORKFLOW-005`, `FR-WORKFLOW-007`, `FR-WORKFLOW-014` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/types.go](../../pkg/workflows/types.go), [pkg/workflows/validator.go](../../pkg/workflows/validator.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-WORKFLOW-006`, `FR-WORKFLOW-008`, `FR-WORKFLOW-011` | [pkg/workflows/catalog_trigger_test.go](../../pkg/workflows/catalog_trigger_test.go), [pkg/workflows/trigger.go](../../pkg/workflows/trigger.go), [pkg/workflows/runtime_trigger.go](../../pkg/workflows/runtime_trigger.go), [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go), [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-WORKFLOW-009`, `FR-WORKFLOW-012`, `FR-WORKFLOW-013` | [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/store.go](../../pkg/workflows/store.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/config/config_test.go](../../pkg/config/config_test.go) |
| `FR-WORKFLOW-010`, `FR-WORKFLOW-015` | [cmd/picoclaw/internal/workflow](../../cmd/picoclaw/internal/workflow), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [cmd/picoclaw/main_test.go](../../cmd/picoclaw/main_test.go) |
| `FR-WORKFLOW-016` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/tools/fs/send_file_test.go](../../pkg/tools/fs/send_file_test.go) |

## Implementation Anchors

- [pkg/workflows/types.go](../../pkg/workflows/types.go)
- [pkg/workflows/resolver.go](../../pkg/workflows/resolver.go)
- [pkg/workflows/validator.go](../../pkg/workflows/validator.go)
- [pkg/workflows/executor.go](../../pkg/workflows/executor.go)
- [pkg/workflows/store.go](../../pkg/workflows/store.go)
- [pkg/workflows/trigger.go](../../pkg/workflows/trigger.go)
- [pkg/workflows/runtime_trigger.go](../../pkg/workflows/runtime_trigger.go)
- [pkg/workflows/graph.go](../../pkg/workflows/graph.go)
- [pkg/workflows/reload.go](../../pkg/workflows/reload.go)
- [pkg/tools/workflow.go](../../pkg/tools/workflow.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
- [pkg/agent/workflow_triggers.go](../../pkg/agent/workflow_triggers.go)
- [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go)
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
Owns: CODE web/frontend/src/api/workflows.ts
Owns: CODE web/frontend/src/components/workflows/**
Owns: CODE web/frontend/src/routes/agent/workflows.tsx
Owns: CONFIG.workflows*
Owns: CONFIG.tools.workflow*
Owns: CLI cmd/picoclaw/internal/workflow/*
Owns: HTTP * /api/workflows*
Owns: TEST pkg/workflows/*
Owns: TEST pkg/agent/workflow_runtime_test.go
Owns: TOOL workflow
Owns: EVENT workflow.*
