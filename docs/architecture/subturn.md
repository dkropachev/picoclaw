# 🔄 SubTurn Mechanism

> Back to [README](../README.md)

## Overview

The `SubTurn` mechanism is a core feature in PicoClaw that allows tools to spawn isolated, nested agent loops to handle complex sub-tasks.

By using a SubTurn, an agent can break down a problem and run a separate LLM invocation in an independent, ephemeral session. This ensures that intermediate reasoning, background tasks, or sub-agent outputs do not pollute the main conversation history.

## Core Capabilities

- **Context Isolation**: Each SubTurn uses an `ephemeralSessionStore`. Its message history does not leak into the parent task and is destroyed upon completion. The ephemeral session holds at most **50 messages**; older messages are automatically truncated when this limit is reached.
- **Depth & Concurrency Limits**: Prevents infinite loops and resource exhaustion.
  - **Maximum Depth**: Up to 3 nested levels.
  - **Maximum Concurrency**: Up to 5 concurrent sub-turns per parent turn (managed via a semaphore with a 30-second timeout).
- **Context Protection**: Supports soft context limits (`MaxContextRunes`). It proactively truncates old messages (while preserving system prompts and recent context) before hitting the provider's hard context window limit.
- **Error Recovery**: Automatically detects and recovers from provider context length exceeded errors and truncation errors by compressing history and retrying.
- **Tracked Spawn Delivery**: `spawn` is backgrounded by its owner-local
  manager, but its direct SubTurn uses `Async:false`. One committed manager
  completion enters an exact-named-session, composite-ID result envelope rather
  than the legacy direct-SubTurn pending channel.

## Configuration (`SubTurnConfig`)

When spawning a SubTurn, you must provide a `SubTurnConfig`:

| Field | Type | Description |
| :--- | :--- | :--- |
| `Model` | `string` | The LLM model to use for the sub-turn (e.g., `gpt-4o-mini`). **Required.** |
| `Tools` | `[]tools.Tool` | Tools granted to the sub-turn. If empty, it inherits the parent's tools. |
| `SystemPrompt` | `string` | The task description for the sub-turn. Sent as the first user message to the LLM (not as a system prompt override). |
| `ActualSystemPrompt` | `string` | Optional explicit system prompt to replace the agent's default. Leave empty to inherit the parent agent's system prompt. |
| `MaxTokens` | `int` | Maximum tokens for the generated response. |
| `Async` | `bool` | Controls the result delivery mode (Synchronous vs. Asynchronous). |
| `Critical` | `bool` | If `true`, the sub-turn continues running even if the parent finishes gracefully. |
| `Timeout` | `time.Duration` | Maximum execution time (default: 5 minutes). |
| `MaxContextRunes`| `int` | Soft context limit. `0` = auto-calculate (75% of model's context window, recommended), `-1` = no limit (disable soft truncation, rely only on hard context error recovery), `>0` = use specified rune limit. |

> **Note:** The `Async` flag does **not** make the call non-blocking. It only controls whether the result is also delivered to the parent's `pendingResults` channel. Both modes block the caller until the sub-turn completes. For true non-blocking execution, the caller must spawn the sub-turn in a separate goroutine.

The first-party `spawn` tool follows that rule deliberately: its manager owns
the background goroutine and invokes the direct child with `Async:false` and
`Critical:true`. Its result is delivered through the tracked envelope described
below. Generic callers of `SpawnSubTurn` retain the modes in this section.

## Execution Modes

### Synchronous (`Async: false`)

This is the standard mode where the caller needs the result immediately to proceed.

- The caller blocks until the sub-turn completes.
- The result is **only** returned directly via the function return value.
- It is **not** delivered to the parent's pending results channel.

**Example:**
```go
cfg := agent.SubTurnConfig{
    Model:        "gpt-4o-mini",
    SystemPrompt: "Analyze the provided codebase...",
    Async:        false,
}
result, err := agent.SpawnSubTurn(ctx, cfg)
// Process result immediately
```

### Asynchronous (`Async: true`)

Used for "fire-and-forget" operations or parallel processing where the parent turn collects results later.

- The result is delivered to the parent turn's `pendingResults` channel.
- The result is **also** returned via the function return value (for consistency).
- The parent's Agent Loop will poll this channel in subsequent iterations and automatically inject the results into the ongoing conversation context as `[SubTurn Result]`.

**Example:**
```go
cfg := agent.SubTurnConfig{
    Model:        "gpt-4o-mini",
    SystemPrompt: "Run a background security scan...",
    Async:        true,
}
result, err := agent.SpawnSubTurn(ctx, cfg)
// The result will also be injected into the parent loop later via channel
```

## Error Recovery and Retries

SubTurns implement automatic retry mechanisms for transient errors:

| Error Type | Max Retries | Recovery Action |
|:-----------|:------------|:----------------|
| Context Length Exceeded | 2 | Force compress history and retry |
| Response Truncated (`finish_reason="truncated"`) | 2 | Inject recovery prompt and retry |

### Truncation Recovery
When the LLM response is truncated (`finish_reason="truncated"`), SubTurn automatically:
1. Detects the truncation from `turnState.lastFinishReason`
2. Injects a recovery prompt: "Your previous response was truncated due to length. Please provide a shorter, complete response..."
3. Retries up to 2 times

### Context Error Recovery
When the provider returns a context length error (e.g., `context_length_exceeded`):
1. Force compresses the message history (drops oldest 50% of conversation)
2. Retries with the compressed context
3. Up to 2 retries before failing

## Lifecycle and Cancellation

SubTurns operate within an independent context but maintain a structural link to their parent `turnState`.

### Graceful Parent Finish
When the parent task finishes naturally:
- **Non-critical** sub-turns receive a signal to exit gracefully without throwing an error.
- **Critical** (`Critical: true`) sub-turns continue running in the background.
  A direct `Async:true` caller may subsequently classify its legacy-channel
  result as orphaned; tracked `spawn` instead offers one exact-session result
  envelope that can start a late continuation.

An error or panic is not a graceful finish: every nonterminal descendant,
including critical work, is cancellation-requested and ends with error status.

### Hard Abort
When the parent task is forcefully aborted (e.g., user interrupts with `/stop`):
- `HardAbort` and `InterruptHard` only request cancellation. They atomically
  mark the exact retained root/child/descendant graph before invoking any
  context cancellation; they do not finish the turn or edit session history.
- The `runTurn` supervisor commits one immutable aborted outcome, restores the
  captured history and summary restore point once, performs bounded detached
  Git-workspace cleanup, attempts one ordered `agent.turn.end` publication, and
  removes only its exact active owner. Cleanup steps are individually
  panic-isolated so later steps still run; exact owner removal and local
  cancellation are mandatory. A panic follows the same rollback ordering and
  is re-raised after bookkeeping, with the original turn panic taking
  precedence over a cleanup panic.
- Exact child pointers remain reachable through terminal intermediate parents,
  so a critical grandchild cannot escape a later ancestor hard abort merely
  because its direct parent already left the active-turn registry.

## Agent Loop Integration

### Message Routing and Steering

When a message enters the `Run()` loop, the agent determines whether to start a new worker or enqueue to steering:

- If **no active turn** exists for the message's session key, the session is atomically reserved and a **worker goroutine** is spawned. The worker processes the full turn lifecycle: `processMessage` → tool execution → steering drain → `Continue` for queued messages.
- If an **active turn already exists** for the same session, the message is enqueued directly into that session's steering queue. It will be picked up by the existing worker's steering drain loop.

This ensures that:
- Messages from **different sessions** are processed **in parallel** (up to `max_parallel_turns` concurrent workers)
- Messages from the **same session** are strictly **serialized** — they go to the steering queue and are processed sequentially within the active turn
- No background drain goroutine is needed; steering is handled by the worker itself after processing

### Legacy Direct-SubTurn Result Polling

For direct callers that select `Async:true`, the agent loop polls the
compatibility `pendingResults` channel at the established checkpoints:
1. **Before the LLM call**: injects any arrived results as `[SubTurn Result]` messages into the conversation context.
2. **After each tool/hook-response checkpoint**: polls during the tool loop to catch results that arrived during tool execution.

There is no compatibility-channel terminal drain. A result that arrives after
the last checkpoint is classified by the legacy parent-terminal policy below;
only tracked `spawn` receives the exact-session terminal wake described next.

Each turn receives a buffered mailbox before active publication. The mailbox
is never closed; `Finished` is the terminal signal and garbage collection owns
mailbox lifetime. Delivery and terminal commitment take the same parent lock,
so each async result is classified exactly once. A running parent with space
receives it; a terminal parent, missing mailbox, nil result, or full channel
emits an orphan with reason `parent_finished`,
`parent_mailbox_unavailable`, `nil_result`, or `channel_full`.

Tracked `spawn` never writes this channel.

### Tracked Spawn Result Delivery

`spawn` returns its manager-local `subagent-N` acknowledgement immediately.
The manager owns the only background goroutine and runs the direct SubTurn with
`Async:false` and `Critical:true`. After it commits `completed`, `failed`, or
`canceled`, its sole callback carries the committed task ID/status and offers
one filtered, bounded result envelope to AgentLoop delivery.

The envelope freezes the exact named destination agent/session/channel/chat and
uses the source parent turn ID plus manager-local task ID as its composite
identity. This matters because separate owner-local managers can both allocate
`subagent-1`. Identical callback replay is ignored, while conflicting identity
reuse or an invalid route/result is orphaned rather than overwriting another
completion.

The model-visible form is
`[Subagent Result task_id=... status=... source_turn_id=...] ...`, so nested
owner-local ID reuse remains distinguishable. A future active turn may claim
only when agent, canonical session, semantic scope, channel, and chat all match
the frozen route. The late continuation reapplies the root's effective profile
and process caps; it retains no full parent prompt/context snapshot.

An eligible active turn claims the envelope at a result checkpoint. If the
named session is idle when the child finishes, one continuation claims it and
resolves only that named agent/session through a fresh coherent runtime
generation. It never routes through raw callback `ForUser` output, synthetic
`system` inbound, or the default agent/session. Claim is terminal: a later
provider, persistence, finalization, cancellation, or publication failure does
not replay the envelope because replay could expose it twice.

Late pumping waits until the original output owner finishes publication, then
uses one exact placeholder. Placeholder deletion wakes pending results, and
same-session steering committed during the continuation is drained through the
same strict agent/scope route. Manual steering fallback is never consumed. A
result claimed at the final tool checkpoint permits one additional no-tool LLM
call but cannot extend the configured tool loop.

The late path performs strict non-creating session reads both before claim and
at the run boundary. Linearizing a concurrent external administrative session
deletion against later history appends requires the process/session ownership
work planned after P007; this in-memory mailbox does not itself add a new
cross-component deletion transaction.

This is process-lifetime, in-memory at-most-once delivery. It is not durable
across crash/restart and does not promise exactly-once provider execution.
Generic asynchronous tool callbacks, direct `Async:true` SubTurns,
`subagent`, `delegate`, and `/subagents` keep their existing behavior.
An explicitly authorized child `message` call also remains a separate tool
side effect; the single-envelope guarantee governs terminal completion
delivery, not independently requested child delivery actions.

### Turn State Tracking

All active turns are registered in `AgentLoop.activeTurnStates` (`sync.Map`, keyed by session key). A reservation sentinel is stored atomically via `LoadOrStore` before the worker starts, then replaced with the real `*turnState` when `runTurn` registers. This prevents a TOCTOU race where multiple messages for the same session could spawn concurrent workers. The sentinel is cleaned up by the worker's deferred cleanup. This allows `HardAbort` and `/subagents` observability commands to find and operate on active turns.

## Runtime Event Integration

SubTurns emit runtime events through `pkg/events` for observability and debugging:

| Event Kind | When Emitted | Payload |
|:------|:-------------|:--------|
| `agent.subturn.spawn` | Sub-turn successfully initialized | `SubTurnSpawnPayload{AgentID, Label, ParentTurnID}` |
| `agent.subturn.end` | Sub-turn finishes (success or error) | `SubTurnEndPayload{AgentID, Status}` |
| `agent.subturn.result_delivered` | Direct `Async:true` result reaches its compatibility parent channel, or a tracked envelope is claimed by its exact turn | `SubTurnResultDeliveredPayload{TargetChannel, TargetChatID, SourceTurnID, TaskID, Status, ContentLen}` |
| `agent.subturn.orphan` | A direct `Async:true` result cannot use its compatibility parent channel, or a tracked envelope fails admission/routing before claim | `SubTurnOrphanPayload{ParentTurnID, ChildTurnID, SourceTurnID, TaskID, Status, Reason}` |

## API Reference

### SpawnSubTurn (Public Entry Point)

```go
func SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*tools.ToolResult, error)
```

This is the exported package-level entry point for agent-internal code (e.g., tests, direct invocations). It retrieves `AgentLoop` and `turnState` from context and delegates to the internal `spawnSubTurn`.

**Requirements:**
- `AgentLoop` must be injected into context via `WithAgentLoop()`
- Parent `turnState` must exist in context (automatically set when called from tools)

**Returns:**
- `*tools.ToolResult`: Contains `ForLLM` field with the sub-turn's output
- `error`: One of the defined error types or context errors

### AgentLoopSpawner (Interface Implementation)

```go
type AgentLoopSpawner struct { al *AgentLoop }

func (s *AgentLoopSpawner) SpawnSubTurn(ctx context.Context, cfg tools.SubTurnConfig) (*tools.ToolResult, error)
```

This implements the `tools.SubTurnSpawner` interface for use by tools that need to spawn sub-turns without a direct import of the `agent` package (avoiding circular dependencies). It converts `tools.SubTurnConfig` → `agent.SubTurnConfig` before delegating to the internal `spawnSubTurn`.

### NewSubTurnSpawner

```go
func NewSubTurnSpawner(al *AgentLoop) *AgentLoopSpawner
```

Creates a new spawner instance for the given AgentLoop. Pass the returned value to `SpawnTool.SetSpawner()` or `SubagentTool.SetSpawner()` during tool registration.

### Continue

```go
func (al *AgentLoop) Continue(ctx context.Context, sessionKey, channel, chatID string) (string, error)
```

Resumes an idle agent turn by dequeuing steering messages for the given session and running them through the agent loop. Returns the response string if processing occurred, or empty string if no steering messages were pending. Uses session-aware active turn checking — it only blocks if a turn is active for the *same* session, not for unrelated sessions.

## Context Propagation

SubTurn relies on context values for proper operation:

| Context Key | Purpose |
|:------------|:--------|
| `agentLoopKey` | Stores `*AgentLoop` for tool access and SubTurn spawning |
| `turnStateKey` | Stores `*turnState` for hierarchy tracking and result delivery |

### Injecting Dependencies

```go
// Before calling tools that may spawn SubTurns
ctx = WithAgentLoop(ctx, agentLoop)
ctx = withTurnState(ctx, turnState)
```

### Independent Child Context

**Important**: The child SubTurn uses an independently cancelable context
derived with `context.WithoutCancel` from the retained runtime context. Values
and the runtime-generation lease remain available, while parent context
cancellation does not implicitly decide child policy. This design choice:

- Allows critical SubTurns to continue only after the parent commits a
  successful graceful completion.
- Lets the structural supervisor explicitly cancel every child after parent
  error, external cancellation, timeout, panic, or hard abort.
- Keeps the child timeout as independent self-protection (`Timeout` config or
  5 minutes by default).

## Error Types

| Error | Condition |
|:------|:----------|
| `ErrDepthLimitExceeded` | SubTurn depth exceeds 3 levels |
| `ErrInvalidSubTurnConfig` | Required field `Model` is empty |
| `ErrConcurrencyTimeout` | All 5 concurrency slots occupied for 30+ seconds |
| Context errors | Parent context cancelled during semaphore acquisition |

## Thread Safety

SubTurns are designed for concurrent execution:

- **Parent-child relationships**: Managed under mutex (`parentTS.mu.Lock()`)
- **Child publication**: Parent edge and active child are published atomically;
  terminal or cancellation-requested parents reject attachment.
- **Cascade graph**: Exact child pointers are retained and validated by parent
  pointer and ID before traversal.
- **Active turn tracking**: Uses `sync.Map` for concurrent access to `activeTurnStates`
- **ID generation**: Uses `atomic.Int64` for unique SubTurn IDs (format: `subturn-N`, globally monotonic per `AgentLoop` instance)
- **Direct async result delivery**: Performs one non-blocking compatibility-
  channel send while holding the same parent lock used by terminal commitment;
  no channel close or panic recovery is part of synchronization.
- **Tracked spawn result delivery**: Atomically deduplicates a composite source-
  turn/task ID, reserves at most one exact-session continuation, and makes claim
  terminal before model execution so the same completion cannot be replayed.

## Direct Async Orphan Results

For a direct `Async:true` SubTurn, an orphan result occurs when:
1. Parent turn finishes before the SubTurn completes
2. The `pendingResults` channel is full (buffer size: 16)

When a result becomes orphan:
- `agent.subturn.orphan` is emitted to the runtime event bus
- The result is **NOT** delivered to the LLM context
- External systems can listen to this event for custom handling

### Preventing Orphan Results
- Use `Critical: true` for important SubTurns that must complete
- Monitor `agent.subturn.orphan` for observability
- Consider the 16-buffer limit when spawning many async SubTurns

Tracked `spawn` does not use this 16-entry compatibility channel. Its separate
envelope can continue the exact named session after the spawning turn exits,
but invalid/conflicting/full admission is still orphaned and process exit loses
unclaimed in-memory state.

## Tool Inheritance

### When `cfg.Tools` is empty:
- SubTurn inherits **all** tools from the parent agent
- Tools are registered in a new `ToolRegistry` instance
- Tool TTL is managed independently from parent

### When `cfg.Tools` is specified:
- Only the specified tools are available to the SubTurn
- Parent tools are **NOT** merged
- Use this to restrict SubTurn capabilities for security or focus

**Example - Restricted SubTurn:**
```go
cfg := agent.SubTurnConfig{
    Model: "gpt-4o-mini",
    Tools: []tools.Tool{readOnlyTool}, // Only read-only access
    SystemPrompt: "Analyze the file structure...",
}
```

## Reference

| Constant | Value |
|:---------|:------|
| `maxSubTurnDepth` | 3 |
| `maxConcurrentSubTurns` | 5 |
| `concurrencyTimeout` | 30s |
| `defaultSubTurnTimeout` | 5m |
| `maxEphemeralHistorySize` | 50 messages |
| direct `Async:true` `pendingResults` buffer | 16 |
| `MaxContextRunes` default | 75% of model context window |
