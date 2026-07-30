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
- Core types/functions: `AgentLoop`, agent instance creation, context builder,
  pipeline setup/execute/finalize helpers, turn reservations and scoped steering,
  message-scoped channel capabilities, provider factory, and tool registry.
- Runtime ordering: normalize input, resolve route/session, build prompt, select model candidate, call provider, execute tool calls, stream/finalize response, persist history, emit runtime events.
- Non-obvious constraints: tool iteration limits, media limits, turn profile block
  disabling, fallback candidates, child-turn concurrency, exact transient-UX
  ownership, and source-compatible channel/streaming fallbacks must stay
  explicit.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-AGENT-001` | MUST | A turn starts from normalized input and creates a scoped runtime context containing agent, session, channel, chat, sender, turn ID, and media metadata when available. | Downstream tools, events, and persistence need stable context. |
| `FR-AGENT-002` | MUST | Prompt construction includes configured identity, workspace instructions, memory, session history, skills, and tool definitions unless the turn profile disables a block. | Current behavior depends on composable prompt contributors. |
| `FR-AGENT-003` | MUST | Model resolution uses configured agent model candidates first, then defaults, then model list fallbacks, preserving provider/model identity for retries and updating the active provider when a fallback candidate succeeds; model-router aliases evaluate the current turn and select a configured model or account-router target before provider execution; router aliases expand credential-account blocks and load-balanced blocks to selected account candidates before provider execution and record fallback outcomes by stable account identity; Pico chat may provide a per-turn selected account and upstream model ID, which apply only to that turn across router candidates, including isolated `/btw`, while preserving account-scoped providers; startup may create the first runnable reachable account provider but retains the router alias so graph fallback, not bootstrap-provider fallthrough, governs execution; Codex OAuth and GitHub Copilot credential-backed requests preserve any non-empty requested model name and only substitute the provider default for an empty model; GitHub Copilot credential-backed entries resolve the selected stored credential into a direct HTTPS API client while non-credential entries keep the local bridge path. | Multi-provider behavior must be reproducible and provider-side model rollout names must not be rewritten locally. |
| `FR-AGENT-004` | MUST | LLM responses with tool calls execute registered tools until the configured maximum tool iterations is reached or no tool calls remain. | Prevents unbounded loops while preserving agent tool use. |
| `FR-AGENT-005` | MUST | Tool execution errors are returned to the model or user in normalized error text without panicking the turn loop. | Tool failures are normal runtime outcomes. |
| `FR-AGENT-006` | MUST | Streaming output emits deltas when supported and still produces a final assistant message for session storage. | Streaming and durable history must stay consistent. |
| `FR-AGENT-007` | MUST | Subturn and spawn tools run child work with bounded depth, concurrency, timeout, and token budget. | Background work must not exhaust the parent turn. |
| `FR-AGENT-008` | SHOULD | Thinking or reasoning content is preserved for surfaces that display it and omitted from ordinary final replies unless configured. | Reasoning display is auxiliary, not the answer itself. |
| `FR-AGENT-009` | MUST | The root CLI composes independently owned feature command trees without changing their behavior. Direct-agent commands use the same agent runtime path as gateway turns, with command-specific input/output wrapping only. | Adding an operator command must not fork agent behavior or entangle unrelated command implementations. |
| `FR-AGENT-010` | MUST | Per-model OpenAI-style `reasoning_effort` is normalized before provider calls; blank/default values are omitted, `off` maps to `none`, and unsupported values are rejected by config validation. | Provider requests must not forward invalid reasoning controls. |
| `FR-AGENT-011` | MUST | Provider prompt serialization preserves ordered text/media parts, scoped context, tool call/result identifiers, and token estimates through the provider-neutral prompt representation before mapping to provider-specific wire formats. | Multi-provider turns need one canonical prompt model so media, summaries, cache hints, and tool relationships are not silently lost or double-counted. |
| `FR-AGENT-012` | MUST | Each primary or fallback provider attempt derives tool adaptation from the concrete provider/model profile after router resolution, applies that profile's visible surface to a candidate-specific base schema, and retains the successful candidate's surface and schema for tool execution and observations; explicit profile overrides still obey configured runtime visible-change policy. | Routed and fallback turns must not probe or expose tools using a virtual router identity or another candidate's schema. |
| `FR-AGENT-013` | MUST | Provider/config reload pauses admission of new root runtime users, drains the current registry generation before replacement, and never closes a retained provider while a turn, workflow, summarizer, child turn, or gateway-owned background action can still use it. One inbound lease covers workflow-trigger matching, route/session placeholder selection, and a synchronously retained queued worker so semaphore backlog cannot split an ingress decision across generations. Other asynchronous work retains its generation before goroutine launch; independently cancelable subturn contexts preserve that lease marker; stale captured agent pointers are resolved by ID against the current registry before a root turn starts; and independently launched summarizers/background consumers require the exact config and registry generation that created them. The first reload pause synchronously removes the generation-owned runtime-event workflow subscription and the final nested resume recreates it for only the committed/restored config before opening admission. Terminal Stop is remembered even before `Run` registers; shutdown first quiesces producers, then cancels/joins AgentLoop-owned automation and permanently pauses/drains runtime users, then closes channel/media dependencies and the provider. | Reload, rollback, and shutdown must not create use-after-close provider calls, admit provisional runtime state/events, execute stale cross-workspace work, leak queued session placeholders, or deadlock nested work behind their own drain. |
| `FR-AGENT-014` | MUST | When a credential-backed Codex OAuth request fails with the structured `usage_limit_reached` error, the provider rechecks the authoritative main Codex rate-limit state and automatically consumes one earned reset only when that main limit is eligible and exhausted; reset attempts are serialized, reuse an idempotency key across redemption retries, and reconcile through a post-consume usage read. A confirmed redemption or concurrent reset retries the original provider request at most once, while a redemption that is not verified to recover the same exhaustion episode suppresses further automatic consumption for that episode. Generic `429` responses, workspace credit or spend-control limits, additional-model-only limits, unsupported reset state, and accounts with no resets must not consume one. | Automatic recovery must restore an exhausted eligible account without double-spending finite resets or masking other quota and billing failures. |
| `FR-AGENT-015` | MUST | An admitted inbound message's opaque turn-UX identity remains attached to its session reservation, active or rescued continuation, same-chat tool/stream/final/error output, and exact cleanup decision. A successfully buffered output stops only that identity's typing registration and leaves its reaction/placeholder for delivery-time cleanup; a no-output, rejected, canceled, failed, or panicked turn removes only its exact artifacts. Same-chat steering atomically queues and rebinds its identity to the pinned active owner after slow message preparation rechecks ownership; cross-chat steering, enqueue failure, and abandoned ownership clean the secondary exact identity, while committed steering is rescued or transferred instead of being stranded. Channel-side typing/reaction callbacks remain pinned to the provider generation that created them, so late older callbacks cannot clear a newer turn. | Concurrent turns and steering must not strand transient UX, erase a newer provider generation, or lose a committed user message. |
| `FR-AGENT-016` | MUST | The original `ChannelManager` and four-argument `MessageBus.GetStreamer` contracts remain sufficient for agent integrations. Exact typing stop, cleanup, rebind, placeholder, and turn-scoped streaming are additive optional capabilities: legacy managers fall back to chat-scoped typing stop and placeholder calls plus one detached bounded tool-feedback cleanup, rebind is a no-op, and legacy buses use `GetStreamer`; capable implementations receive the exact turn identity instead. | Existing channel and message-bus implementations must remain source-compatible while built-in channels gain exact ownership. |
| `FR-AGENT-017` | MUST | Agent-owned inbound snapshots preserve process-local turn/event identity, deduplication, occurrence time, subject, conversation, safe attachment descriptors, and transport trust facts through primary turns, queued continuations, and derived outbound contexts. Mutable maps, occurrence-time pointers, and attachment slices are detached when copied, and these fields remain excluded from serialized routing context. | Asynchronous turn and delivery work must retain admission facts without aliasing caller-owned state or expanding the serialized contract. |
| `FR-AGENT-018` | MUST | The authenticated Agent management API and responsive Agent UI project an implicit `main` policy without writing an empty config and support ordered create, inspect, edit, default-selection, and delete operations against an explicit opaque config revision. The surface preserves model inheritance versus an explicit empty fallback list, labels editable values as configured policy, preserves fields it does not expose, reports whether a gateway restart is required, and never deletes workspaces, sessions, threads, history, runs, or workflow files. | Operators need complete, concurrency-safe browser management of persistent agent policy without mistaking configured values for workspace overrides or destroying runtime data. |

## Data And State Model

Agent state includes configured defaults, resolved candidate providers, registered
tools, skills filter, MCP allowlist, context builder cache, runtime event bus,
turn scope, and session store references. A turn records user input, media,
assistant content, tool calls/results, optional reasoning, and runtime metadata.
Inbound session reservations additionally retain a process-local turn-UX
identity and detached inbound-context snapshot. Per-session handoff locks
serialize reservation, steering enqueue/dequeue, rebind, and abandonment;
rescue markers explicitly own committed steering until a live continuation or
competing turn takes it.

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
Owns: CODE web/backend/api/agents*
Owns: CODE web/frontend/src/api/agents.ts
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
Owns: HTTP * /api/agents*
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
| CLI | root `picoclaw` command registration | Compose feature-owned subcommands such as workflow and event operations while leaving their implementation and policy in the owning package. | `FR-AGENT-009` |
| Config | `agents.*`, `model_list.*` | Agent defaults, per-agent models, fallbacks, turn profile, retry, token, media, tool iteration policy, and optional model price metadata used by workflow-managed child selection. | `FR-AGENT-002`, `FR-AGENT-003`, `FR-AGENT-004` |
| Config | `model_list[].reasoning_effort` | Optional OpenAI-style reasoning effort forwarded only after shared normalization and validation. | `FR-AGENT-003`, `FR-AGENT-010` |
| Tools | `spawn`, `spawn_status`, `subagent`, `delegate` | Child work delegation and status reporting. | `FR-AGENT-007` |
| Runtime | `AgentLoop.PauseRuntimeForReload`, retained runtime leases, provider/config reload | Quiesce root and asynchronous runtime users across a registry generation swap, service commit, or rollback. | `FR-AGENT-013` |
| Go API | `interfaces.ChannelManager`, optional `MessageScopedTypingStopper`, `MessageScopedTurnUXCleaner`, `MessageScopedTurnUXRebinder`, and `MessageScopedPlaceholderSender` | Keep the legacy manager surface sufficient while allowing built-in channels to stop, clean, transfer, and create transient UX for one opaque turn identity. | `FR-AGENT-015`, `FR-AGENT-016` |
| Go API | `interfaces.MessageBus.GetStreamer`, optional `interfaces.TurnScopedMessageBus.GetStreamerForTurn` | Use turn-scoped streaming when implemented and otherwise call the original four-argument streamer lookup. | `FR-AGENT-015`, `FR-AGENT-016` |
| Runtime | `bus.InboundContext`, `DispatchRequest`, turn reservations, continuation targets, and outbound context derivation | Carry detached process-local event and transient-UX metadata across one turn without adding it to serialized routing context. | `FR-AGENT-015`, `FR-AGENT-017` |
| Events | `agent.*` | Turn, LLM, tool, steering, interrupt, subturn, and error telemetry. | `FR-AGENT-001`, `FR-AGENT-004`, `FR-AGENT-006` |
| HTTP/UI | `/api/agents*`, `/agent/agents` | Project and mutate persistent configured agent policy with ordered results, revision fencing, explicit model fallback semantics, and restart feedback. | `FR-AGENT-018` |

## Algorithms And Ordering

1. Build an `InboundContext` and resolve the route/session before prompt work.
2. Resolve prompt contributors and turn profile decisions before provider calls.
3. Select model candidates, normalize optional provider controls such as
   `reasoning_effort`, then execute provider attempts with retry/fallback
   policy. A credential-backed Codex attempt that returns the structured usage
   exhaustion error serializes by account, rechecks the authoritative main
   limit and reset count, consumes at most one eligible reset, reconciles the
   same window, and retries the same provider request once after a confirmed
   redemption before fallback observes the error. Failed verification
   suppresses another automatic reset for that exhaustion episode.
4. For each tool-call response, validate tool availability and arguments, run hooks and registry execution, append tool results, and re-enter provider execution until done or capped.
5. Keep the detached inbound snapshot on the reservation and real turn. After
   any slow inbound preparation, recheck the session owner under the handoff
   lock; either claim the idle session or atomically enqueue and rebind
   same-chat steering to the pinned owner. Retire cross-chat transient UX
   immediately after its queue commit because the active turn cannot own that
   chat key. If a reservation is abandoned after steering commits, a bounded
   rescue continues the queue or transfers it to a competing live owner.
6. Write final messages and summaries after the assistant response is known.
   Propagate the inbound snapshot to same-chat output. Once buffered delivery
   accepts output, stop only exact typing and let channel pre-send own
   reaction/placeholder cleanup; otherwise perform exact full cleanup. Use the
   optional message-scoped manager and streamer capabilities when present and
   their legacy fallbacks when absent.
7. Before replacing provider/config state, pause new runtime admission and wait
   for current generation leases. Acquire before inbound trigger/routing
   decisions and transfer a retained lease to any worker waiting for a
   semaphore. Retain before launching other asynchronous workflows or spawn
   work, propagate through independently cancelable child contexts, require
   exact config/registry identity for summarizers and gateway-owned
   scheduled/event work, remove the runtime-event subscription for the outer
   transaction, and recreate it for the final config before admission resumes.
8. On terminal shutdown, remember Stop even if `Run` has not registered yet,
   quiesce runtime producers, cancel and join the AgentLoop and its automation
   controller, and hold a permanent runtime pause until active leases reach
   zero. Only then stop channel/media dependencies and close provider, bus, and
   registry resources. A timeout leaves dependencies/resources open for
   process teardown.

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
Account router aliases plug into this same candidate-selection step: the turn
loop expands the router to concrete account candidates, can reselect after
context compression, and records fallback outcomes without changing provider
prompt serialization. Credential-backed GitHub Copilot entries use the same
account identity and fallback accounting as other provider accounts while
non-credential GitHub Copilot entries continue to represent the local bridge.
Model-router aliases plug into the same step before light/heavy routing and can
target either a concrete model alias or an account-router alias. Pico chat
account/model overrides are turn-scoped and do not rewrite persisted
`model_list[]`, `account_routers[]`, or `model_routers[]` entries.
[Chat channels](chat-channels.md) create the opaque turn-UX identity and own the
provider-specific typing, reaction, placeholder, and generation-pinned callback
implementations. This feature carries that identity through turn ownership,
steering, streaming, tools, and outbound delivery and requests only exact
transitions. [Durable external event automation](event-automation.md) owns
admission and event normalization; the agent preserves its process-local
metadata but does not persist or reinterpret those trust facts.

## Failure And Edge Cases

- Missing or disabled providers fail the turn with a clear model/provider error.
- Missing GitHub Copilot credentials fail before provider execution, while
  local bridge Copilot entries continue to report local transport failures.
- Codex reset lookup or redemption failure preserves the original
  fallback-eligible usage-limit error, except that caller cancellation and
  deadline errors remain caller-visible. Generic rate limits, workspace spend
  controls, additional-model-only limits, and zero-credit accounts never spend
  a reset.
- Tool lookup misses produce a tool-skipped result instead of a panic.
- Iteration limits stop repeated tool-call loops.
- Media too large for configured limits is rejected before provider execution.
- Child turns that cannot deliver results report orphan or failed status.
- A reload waits for turns and retained asynchronous work from the old
  generation. Nested subturn work borrows the retained marker and cannot block
  behind the pause that is waiting for its parent generation to drain.
- Background work created by a provisional or cached config waits at the same
  gate and fails closed if that exact config generation is not active when
  admission resumes.
- A message routed before its worker semaphore is available retains that route
  generation through placeholder replacement and turn completion. A stale
  summarizer cannot retarget the same agent/session key in a replacement
  workspace.
- Runtime events emitted by provisional replacement services have no workflow
  subscription and cannot be replayed against restored workflows after
  rollback. Workflow enable/disable reloads synchronously create or remove
  exactly one subscription.
- If buffered output is rejected, no-output cleanup removes the exact
  typing/reaction/placeholder generation. If it is accepted, only exact typing
  is stopped early because channel pre-send still owns the matching reaction
  and placeholder.
- A queued steering owner that panics or abandons setup is rescued with its
  detached inbound identity; a competing live owner wins without surfacing an
  error, and cross-chat steering cleans only the secondary chat's exact UX.
- A legacy channel manager cannot express exact ownership, so cleanup makes one
  bounded detached best-effort chat-scoped call. Missing optional rebind support
  is a no-op, and missing turn-scoped streaming support uses the original
  streamer lookup.
- Cloning an inbound snapshot must not share its maps, occurrence-time pointer,
  or attachment backing slice with the producer. Event and transport-trust
  fields remain process-local even when copied to an outbound context.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-AGENT-001`, `FR-AGENT-002`, `FR-AGENT-006`, `FR-AGENT-008` | [pkg/agent/context_test.go](../../pkg/agent/context_test.go), [pkg/agent/pipeline_streaming_test.go](../../pkg/agent/pipeline_streaming_test.go), [pkg/agent/thinking_test.go](../../pkg/agent/thinking_test.go) |
| `FR-AGENT-003` | [pkg/agent/model_resolution_test.go](../../pkg/agent/model_resolution_test.go), [pkg/agent/account_router_test.go](../../pkg/agent/account_router_test.go), [pkg/providers/factory_test.go](../../pkg/providers/factory_test.go), [pkg/providers/fallback_test.go](../../pkg/providers/fallback_test.go), [pkg/providers/oauth/codex_provider_test.go](../../pkg/providers/oauth/codex_provider_test.go) |
| `FR-AGENT-004`, `FR-AGENT-005` | [pkg/agent/pipeline_execute_test.go](../../pkg/agent/pipeline_execute_test.go), [pkg/agent/error_format_test.go](../../pkg/agent/error_format_test.go), [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go) |
| `FR-AGENT-007` | [pkg/agent/subturn_test.go](../../pkg/agent/subturn_test.go), [pkg/tools/subagent_tool_test.go](../../pkg/tools/subagent_tool_test.go), [pkg/tools/spawn_status_test.go](../../pkg/tools/spawn_status_test.go) |
| `FR-AGENT-009` | [cmd/picoclaw/main_test.go](../../cmd/picoclaw/main_test.go), [cmd/picoclaw/internal/agent/command_test.go](../../cmd/picoclaw/internal/agent/command_test.go), [cmd/picoclaw/internal/model/command_test.go](../../cmd/picoclaw/internal/model/command_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-AGENT-010` | [pkg/agent/reasoning_effort_test.go](../../pkg/agent/reasoning_effort_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go), [pkg/providers/openai_compat/provider_test.go](../../pkg/providers/openai_compat/provider_test.go), [pkg/providers/azure/provider_test.go](../../pkg/providers/azure/provider_test.go), [pkg/providers/oauth/codex_provider_test.go](../../pkg/providers/oauth/codex_provider_test.go) |
| `FR-AGENT-011` | [pkg/providers/promptir/conversion_test.go](../../pkg/providers/promptir/conversion_test.go), [pkg/providers/common/common_test.go](../../pkg/providers/common/common_test.go), [pkg/providers/openai_responses_common/responses_common_test.go](../../pkg/providers/openai_responses_common/responses_common_test.go), [pkg/tokenizer/estimator_test.go](../../pkg/tokenizer/estimator_test.go) |
| `FR-AGENT-012` | [pkg/agent/pipeline_llm_adaptation_test.go](../../pkg/agent/pipeline_llm_adaptation_test.go), [pkg/agent/instance_test.go](../../pkg/agent/instance_test.go) |
| `FR-AGENT-013` | [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/agent/runtime_event_logger_test.go](../../pkg/agent/runtime_event_logger_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go) |
| `FR-AGENT-014` | [pkg/providers/oauth/codex_rate_limit_reset_test.go](../../pkg/providers/oauth/codex_rate_limit_reset_test.go) |
| `FR-AGENT-015` | [pkg/agent/agent_turn_ux_test.go](../../pkg/agent/agent_turn_ux_test.go), [pkg/agent/steering_test.go](../../pkg/agent/steering_test.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/channels/base_test.go](../../pkg/channels/base_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go) |
| `FR-AGENT-016` | [pkg/agent/channel_manager_compat_test.go](../../pkg/agent/channel_manager_compat_test.go), [pkg/agent/pipeline_streaming_test.go](../../pkg/agent/pipeline_streaming_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go) |
| `FR-AGENT-017` | [pkg/agent/turn_context_test.go](../../pkg/agent/turn_context_test.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/agent/steering_test.go](../../pkg/agent/steering_test.go), [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go) |
| `FR-AGENT-018` | [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts), [web/frontend/src/components/agent/agents/agents-page.test.tsx](../../web/frontend/src/components/agent/agents/agents-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [pkg/agent/pipeline.go](../../pkg/agent/pipeline.go)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/agent/agent.go](../../pkg/agent/agent.go)
- [pkg/agent/channel_manager_compat.go](../../pkg/agent/channel_manager_compat.go)
- [pkg/agent/steering.go](../../pkg/agent/steering.go)
- [pkg/agent/turn_context.go](../../pkg/agent/turn_context.go)
- [pkg/agent/runtime_gate.go](../../pkg/agent/runtime_gate.go)
- [pkg/providers/factory.go](../../pkg/providers/factory.go)
- [web/backend/api/agents.go](../../web/backend/api/agents.go)
- [web/frontend/src/components/agent/agents](../../web/frontend/src/components/agent/agents)
