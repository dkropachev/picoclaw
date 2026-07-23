# Agent Conversations And Turn Execution

## Feature ID

`FR-AGENT`

## Behavior Summary

PicoClaw accepts a user turn, builds prompt context, selects provider
candidates, calls an LLM, executes requested tools, streams or finalizes
responses, and records turn state. Provider, model, CLI, and config surfaces are
auxiliary to this capability.

## Reconstruction Notes

- Similarity target: recreate an agent loop that builds prompt context, selects provider candidates, executes tool calls, and stores a final turn.
- Core types/functions: `AgentLoop`, agent instance creation, context builder, pipeline setup/execute/finalize helpers, provider factory, and tool registry.
- Runtime ordering: normalize input, resolve route/session, build prompt, select model candidate, call provider, execute tool calls, stream/finalize response, persist history, emit runtime events.
- Non-obvious constraints: tool iteration limits, media limits, turn profile block disabling, fallback candidates, and child-turn concurrency must stay explicit.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-AGENT-001` | MUST | A turn starts from normalized input and creates a scoped runtime context containing agent, session, channel, chat, sender, turn ID, and media metadata when available. | Downstream tools, events, and persistence need stable context. |
| `FR-AGENT-002` | MUST | Prompt construction includes configured identity, workspace instructions, memory, session history, skills, and tool definitions unless the turn profile disables a block. | Current behavior depends on composable prompt contributors. |
| `FR-AGENT-003` | MUST | Model resolution uses configured agent model candidates first, then defaults, then model list fallbacks, preserving provider/model identity for retries; Codex OAuth requests preserve any non-empty requested model name and only substitute the Codex default for an empty model. | Multi-provider behavior must be reproducible and provider-side model rollout names must not be rewritten locally. |
| `FR-AGENT-004` | MUST | LLM responses with tool calls execute registered tools until the configured maximum tool iterations is reached or no tool calls remain. | Prevents unbounded loops while preserving agent tool use. |
| `FR-AGENT-005` | MUST | Tool execution errors are returned to the model or user in normalized error text without panicking the turn loop. | Tool failures are normal runtime outcomes. |
| `FR-AGENT-006` | MUST | Streaming output emits deltas when supported and still produces a final assistant message for session storage. | Streaming and durable history must stay consistent. |
| `FR-AGENT-007` | MUST | Subturn and spawn tools run child work with bounded depth, concurrency, timeout, and token budget. | Background work must not exhaust the parent turn. |
| `FR-AGENT-008` | SHOULD | Thinking or reasoning content is preserved for surfaces that display it and omitted from ordinary final replies unless configured. | Reasoning display is auxiliary, not the answer itself. |
| `FR-AGENT-009` | MUST | CLI direct-agent commands use the same agent runtime path as gateway turns, with command-specific input/output wrapping only. | CLI must not fork behavior from gateway runtime. |
| `FR-AGENT-010` | MUST | Per-model OpenAI-style `reasoning_effort` is normalized before provider calls; blank/default values are omitted, `off` maps to `none`, and unsupported values are rejected by config validation. | Provider requests must not forward invalid reasoning controls. |
| `FR-AGENT-011` | MUST | Provider prompt serialization preserves ordered text/media parts, scoped context, tool call/result identifiers, and token estimates through the provider-neutral prompt representation before mapping to provider-specific wire formats. | Multi-provider turns need one canonical prompt model so media, summaries, cache hints, and tool relationships are not silently lost or double-counted. |

## Data And State Model

Agent state includes configured defaults, resolved candidate providers, registered
tools, skills filter, MCP allowlist, context builder cache, runtime event bus,
turn scope, and session store references. A turn records user input, media,
assistant content, tool calls/results, optional reasoning, and runtime metadata.

## Surface Ownership

Owns: CODE cmd/picoclaw/main.go
Owns: CODE cmd/picoclaw/dns_noresolv.go
Owns: CODE cmd/picoclaw/internal/agent/**
Owns: CODE cmd/picoclaw/internal/model/**
Owns: CODE cmd/picoclaw/internal/status/**
Owns: CODE cmd/picoclaw/internal/version/**
Owns: CODE pkg/agent/**
Owns: CODE pkg/audio/**
Owns: CODE pkg/devices/**
Owns: CODE pkg/providers/**
Owns: CODE pkg/tokenizer/**
Owns: CODE web/frontend/src/components/agent/**
Owns: CODE web/frontend/src/routes/agent/**
Owns: CLI cmd/picoclaw/main.go *
Owns: CLI cmd/picoclaw/internal/agent/*
Owns: CLI cmd/picoclaw/internal/model/*
Owns: CLI cmd/picoclaw/internal/status/*
Owns: CLI cmd/picoclaw/internal/version/*
Owns: CONFIG.agents*
Owns: CONFIG.model_list*
Owns: CONFIG.build_info
Owns: CONFIG.version
Owns: CONFIG.voice*
Owns: CONFIG.tools.spawn*
Owns: CONFIG.tools.spawn_status*
Owns: CONFIG.tools.subagent*
Owns: CONFIG.devices*
Owns: TEST cmd/picoclaw/main_test.go *
Owns: TEST cmd/picoclaw/internal/agent/*
Owns: TEST cmd/picoclaw/internal/model/*
Owns: TEST cmd/picoclaw/internal/status/*
Owns: TEST cmd/picoclaw/internal/version/*
Owns: TEST pkg/agent/*
Owns: TEST pkg/providers/*
Owns: TEST pkg/tokenizer/*
Owns: TEST pkg/audio/*
Owns: TOOL spawn
Owns: TOOL spawn_status
Owns: TOOL subagent
Owns: TOOL delegate
Owns: EVENT agent.*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw agent`, `picoclaw model`, `picoclaw status`, `picoclaw version` | Direct agent use, model selection, status, and build metadata. | `FR-AGENT-003`, `FR-AGENT-009` |
| Config | `agents.*`, `model_list.*` | Agent defaults, per-agent models, fallbacks, turn profile, retry, token, media, tool iteration policy, and optional model price metadata used by workflow-managed child selection. | `FR-AGENT-002`, `FR-AGENT-003`, `FR-AGENT-004` |
| Config | `model_list[].reasoning_effort` | Optional OpenAI-style reasoning effort forwarded only after shared normalization and validation. | `FR-AGENT-003`, `FR-AGENT-010` |
| Tools | `spawn`, `spawn_status`, `subagent`, `delegate` | Child work delegation and status reporting. | `FR-AGENT-007` |
| Events | `agent.*` | Turn, LLM, tool, steering, interrupt, subturn, and error telemetry. | `FR-AGENT-001`, `FR-AGENT-004`, `FR-AGENT-006` |

## Algorithms And Ordering

1. Build an `InboundContext` and resolve the route/session before prompt work.
2. Resolve prompt contributors and turn profile decisions before provider calls.
3. Select model candidates, normalize optional provider controls such as `reasoning_effort`, then execute provider attempts with retry/fallback policy.
4. For each tool-call response, validate tool availability and arguments, run hooks and registry execution, append tool results, and re-enter provider execution until done or capped.
5. Write final messages and summaries after the assistant response is known.

## Cross-Feature Behavior

Routing selects the target agent before this feature builds candidates. Session
memory supplies history and stores results. Tool execution, MCP, skills, hooks,
and security policies can alter the visible tool set or execution outcome.
Runtime events report each major step. Threads can contribute a policy prompt
that lets the main chat become or join a thread only after configured routing
thresholds are satisfied.
Workflow agent steps reuse this same turn execution path, including session
history modes, provider prompt cache keys, tool iteration limits, and final
message persistence. Managed workflow agent steps can additionally run hidden
no-history child turns with scoped prompts, per-child model and reasoning-effort
overrides, and tool disabling while preserving the same provider resolution and
turn-finalization path.
Git workspaces are allocated through the registered tool during a turn and are
released or reconciled by the shared turn-finalization path, while checkout
inventory and retention behavior are owned by the git workspaces feature.

## Failure And Edge Cases

- Missing or disabled providers fail the turn with a clear model/provider error.
- Tool lookup misses produce a tool-skipped result instead of a panic.
- Iteration limits stop repeated tool-call loops.
- Media too large for configured limits is rejected before provider execution.
- Child turns that cannot deliver results report orphan or failed status.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-AGENT-001`, `FR-AGENT-002`, `FR-AGENT-006`, `FR-AGENT-008` | [pkg/agent/context_test.go](../../pkg/agent/context_test.go), [pkg/agent/pipeline_streaming_test.go](../../pkg/agent/pipeline_streaming_test.go), [pkg/agent/thinking_test.go](../../pkg/agent/thinking_test.go) |
| `FR-AGENT-003` | [pkg/agent/model_resolution_test.go](../../pkg/agent/model_resolution_test.go), [pkg/providers/factory_test.go](../../pkg/providers/factory_test.go), [pkg/providers/fallback_test.go](../../pkg/providers/fallback_test.go), [pkg/providers/oauth/codex_provider_test.go](../../pkg/providers/oauth/codex_provider_test.go) |
| `FR-AGENT-004`, `FR-AGENT-005` | [pkg/agent/pipeline_execute_test.go](../../pkg/agent/pipeline_execute_test.go), [pkg/agent/error_format_test.go](../../pkg/agent/error_format_test.go), [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go) |
| `FR-AGENT-007` | [pkg/agent/subturn_test.go](../../pkg/agent/subturn_test.go), [pkg/tools/subagent_tool_test.go](../../pkg/tools/subagent_tool_test.go), [pkg/tools/spawn_status_test.go](../../pkg/tools/spawn_status_test.go) |
| `FR-AGENT-009` | [cmd/picoclaw/internal/agent/command_test.go](../../cmd/picoclaw/internal/agent/command_test.go), [cmd/picoclaw/internal/model/command_test.go](../../cmd/picoclaw/internal/model/command_test.go) |
| `FR-AGENT-010` | [pkg/agent/reasoning_effort_test.go](../../pkg/agent/reasoning_effort_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go), [pkg/providers/openai_compat/provider_test.go](../../pkg/providers/openai_compat/provider_test.go), [pkg/providers/azure/provider_test.go](../../pkg/providers/azure/provider_test.go), [pkg/providers/oauth/codex_provider_test.go](../../pkg/providers/oauth/codex_provider_test.go) |
| `FR-AGENT-011` | [pkg/providers/promptir/conversion_test.go](../../pkg/providers/promptir/conversion_test.go), [pkg/providers/common/common_test.go](../../pkg/providers/common/common_test.go), [pkg/providers/openai_responses_common/responses_common_test.go](../../pkg/providers/openai_responses_common/responses_common_test.go), [pkg/tokenizer/estimator_test.go](../../pkg/tokenizer/estimator_test.go) |

## Implementation Anchors

- [pkg/agent/pipeline.go](../../pkg/agent/pipeline.go)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/providers/factory.go](../../pkg/providers/factory.go)
