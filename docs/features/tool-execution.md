# Tool Execution

## Feature ID

`FR-TOOL`

## Behavior Summary

PicoClaw exposes built-in tools to the agent for filesystem access, shell
execution, web search/fetch, media delivery, hardware access, and channel
actions. The registry presents tool schemas to providers and executes tool calls
with context, limits, filtering, and error normalization.

## Reconstruction Notes

- Similarity target: recreate a concurrent tool registry plus built-in tools for filesystem, exec, web, media, hardware, and channel action behavior.
- Core types/functions: `Tool` interface, `ToolRegistry`, tool result types, filesystem tool constructors, exec session manager, web search/fetch providers, and tool schema transforms.
- Runtime ordering: register enabled tools, export provider schemas, validate tool call args/context, enforce path/network/command policies, execute, filter result, normalize output.
- Non-obvious constraints: response-handled tools suppress duplicate assistant text, registry must recover panics, workspace restriction and allow path patterns must be checked before file mutation.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-TOOL-001` | MUST | Tool registry registration, unregistration, lookup, definition export, cloning, allowlist filtering, and execution are concurrency-safe. | Agent turns can execute tools while discovery changes visibility. |
| `FR-TOOL-002` | MUST | Filesystem tools respect workspace restriction, allow path patterns, file size limits, and operation-specific semantics for read/write/edit/append/list/image/send. | Local file access is powerful and must be bounded. |
| `FR-TOOL-003` | MUST | Exec runs commands with configured timeout and deny/allow patterns, supports managed sessions, and returns captured output or structured failure. | Shell access must be useful and controllable. |
| `FR-TOOL-004` | MUST | Web search selects configured providers, honors result/range options, and web fetch observes fetch limits and private host controls. | Search and fetch must be deterministic from config. |
| `FR-TOOL-005` | MUST | Sensitive-data filtering redacts configured secrets from tool results before model exposure when enabled. | Models must not see credentials through tool output. |
| `FR-TOOL-006` | SHOULD | Media, reaction, message, TTS, and hardware tools return handled responses when user-visible delivery is completed outside normal assistant text. | The agent should not duplicate already-delivered output. |
| `FR-TOOL-007` | MUST | Tool schema transformations preserve provider compatibility for OpenAI, Anthropic, Gemini, and compatibility adapters. | Provider-specific schemas must not change tool behavior. |
| `FR-TOOL-008` | MUST | Chat account selection excludes internal virtual model entries and model-router rows but keeps virtual account-router entries and virtual credential-account entries selectable as chat account targets. The account selector groups choices into Accounts and Account Routers only, and selecting either discovers upstream model IDs with its `account_ref`; account-router discovery intersects the usable listings or fallbacks returned by reachable accounts and reports per-account issues. | Stored credentials and model-agnostic account routers must remain usable from Chat without exposing unrelated generated rows, mixing routers into API-key account choices, or inventing a router-owned model. |
| `FR-TOOL-009` | SHOULD | Tool adaptation config chooses and pins a visible tool surface per model/API profile, supports provider/model-specific surface and cache-policy overrides, records provider-reported cache-token observations and per-tool success/failure outcomes when available, treats runtime visible tool changes as cache-aware decisions, supports explicit harmless targeted tool-call probes, persists learned cache/probe state in a stable local state file, exposes searchable resolved/learned/probe state for router-expanded profiles in the UI, and exposes Codex-compatible wrappers for shell, stdin, patch, image, and plan capabilities when the Codex surface is selected. | PicoClaw runs many providers; equivalent capabilities should be exposed in the shape each model uses best without breaking prompt/tool cache unnecessarily. |
| `FR-TOOL-010` | SHOULD | The `/agent/tools` page stores the selected Tool Library, Web Search, Thread Policy, or Adaptation tab in the route search params so tab views can be linked, refreshed, and restored through browser history. | Tool configuration work often spans multiple views, and URL-addressable tabs make navigation predictable. |
| `FR-TOOL-011` | MUST | Before `spawn` launches background subturn work, an AgentLoop-backed spawner synchronously retains the caller's runtime generation and transfers that ownership to the goroutine through completion and callback delivery. Failure to retain returns a tool error without launching work. | A parent turn may finish immediately after spawn acknowledgement; reload must still see and drain the admitted child before closing its provider. |
| `FR-TOOL-012` | MUST | Tool execution context carries the active turn's opaque transient-UX identity. A cloned subturn retains that identity, and `message` propagates it only to a delivery targeting the same channel and chat as the tool context. Same-turn tool and stream output uses the additive turn-scoped delivery interfaces when available, with source-compatible legacy fallback, so stale output cannot consume a newer turn's typing, reaction, placeholder, or stream-finalization state. | Tool and subturn output can overlap later turns in the same chat; exact ownership prevents delayed cleanup from corrupting newer user-visible UX without leaking a chat-local identity to another destination. |
| `FR-TOOL-013` | MUST | Tool argument validation accepts finite lossless `json.Number` values for JSON Schema `number` fields and accepts them for `integer` fields only when their exact decimal value is integral. Exponent handling is bounded by the input length and never allocates or computes an exponent-sized value. | Durable event payloads preserve JSON numbers without `float64` precision loss, while untrusted numeric text must not cause precision drift or resource amplification during workflow tool calls. |

## Data And State Model

Tool state includes visible and hidden registry maps, allowlists, TTL metadata,
tool context, media store references, removable tool entries, exec background
sessions, filesystem roots, web provider config, redaction caches for sensitive
values, profile-specific tool adaptation overrides in `tools.adaptation.profile_overrides`,
and the runtime-learned tool adaptation state file at
`$PICOCLAW_HOME/tool_adaptation_state.json`. Per-execution context may also
carry an opaque process-local turn UX identity used only to bind same-chat
delivery and cleanup to its originating turn. Arguments decoded from durable
JSON may retain numeric values as `json.Number` through schema validation and
tool execution.

## Surface Ownership

Owns: CODE pkg/commands/**
Owns: CODE pkg/media/**
Owns: CODE pkg/tools/**
Owns: CODE web/backend/api/tools.go
Owns: CODE web/frontend/src/api/tools.ts
Owns: CODE web/frontend/src/components/agent/tools/**
Owns: CODE web/frontend/src/hooks/use-chat-models.ts
Owns: CODE web/frontend/src/routes/agent/tools.tsx
Owns: CONFIG.tools.allow_read_paths
Owns: CONFIG.tools.allow_write_paths
Owns: CONFIG.tools
Owns: CONFIG.tools.adaptation*
Owns: CONFIG.tools.append_file*
Owns: CONFIG.tools.edit_file*
Owns: CONFIG.tools.exec*
Owns: CONFIG.tools.filter*
Owns: CONFIG.tools.i2c*
Owns: CONFIG.tools.list_dir*
Owns: CONFIG.tools.load_image*
Owns: CONFIG.tools.media_cleanup*
Owns: CONFIG.tools.message*
Owns: CONFIG.tools.read_file*
Owns: CONFIG.tools.send_file*
Owns: CONFIG.tools.send_tts*
Owns: CONFIG.tools.serial*
Owns: CONFIG.tools.spi*
Owns: CONFIG.tools.web*
Owns: CONFIG.tools.write_file*
Owns: HTTP GET /api/tools
Owns: HTTP PUT /api/tools/*
Owns: HTTP GET /api/tools/web-search-config
Owns: HTTP PUT /api/tools/web-search-config
Owns: HTTP GET /api/tools/adaptation
Owns: HTTP PUT /api/tools/adaptation
Owns: HTTP POST /api/tools/adaptation/probe
Owns: TEST pkg/tools/*
Owns: TEST pkg/seahorse/*
Owns: TEST pkg/media/*
Owns: TOOL append_file
Owns: TOOL apply_patch
Owns: TOOL edit_file
Owns: TOOL exec
Owns: TOOL exec_command
Owns: TOOL i2c
Owns: TOOL list_dir
Owns: TOOL load_image
Owns: TOOL message
Owns: TOOL reaction
Owns: TOOL read_file
Owns: TOOL send_file
Owns: TOOL send_tts
Owns: TOOL serial
Owns: TOOL spi
Owns: TOOL update_plan
Owns: TOOL view_image
Owns: TOOL web_fetch
Owns: TOOL web_search
Owns: TOOL write_stdin
Owns: TOOL write_file

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Tools | `read_file`, `write_file`, `edit_file`, `append_file`, `list_dir`, `load_image`, `send_file`, `exec`, `web_search`, `web_fetch`, hardware and delivery tools | Built-in tool schemas and execution behavior. | `FR-TOOL-001` through `FR-TOOL-008` |
| HTTP | `/api/tools`, `/api/tools/{name}/state`, `/api/tools/web-search-config`, `/api/tools/adaptation`, `/api/tools/adaptation/probe` | Launcher tool state, web search configuration, and model-aware tool surface policy/probing. | `FR-TOOL-004`, `FR-TOOL-009` |
| Config | `tools.*` subtrees except MCP, skills, and cron ownership in their feature specs | Tool enablement, limits, providers, filtering, and policies. | `FR-TOOL-002` through `FR-TOOL-006` |
| Frontend | Tool library, adaptation, and web-search configuration pages under `web/frontend/src/components/agent/tools/**` | Browser tool management follows shared frontend API, accessibility, formatting, and route smoke-test rules while preserving tool enablement and adaptation semantics. | `FR-TOOL-001`, `FR-TOOL-004`, `FR-TOOL-009` |
| Tool | `spawn` with optional asynchronous context preparation | Retain AgentLoop runtime ownership synchronously before background goroutine launch, then release after subturn and callback completion. | `FR-TOOL-011` |
| Go context | `WithToolTurnUXContext`, `ToolTurnUXID`, `message` delivery callback | Preserve the active turn identity through tool and cloned-subturn execution, but copy it to outbound delivery only for the exact originating channel/chat. | `FR-TOOL-012` |
| Tool validation | JSON Schema `number` and `integer` arguments | Validate finite `float64` values and lossless `json.Number` values, including exact integrality without exponent expansion. | `FR-TOOL-013` |

## Algorithms And Ordering

1. Build the registry from config, registering only enabled tools and preserving discovery tools where allowed.
2. Convert registry definitions to provider-specific tool schemas.
3. On execution, inject context, validate args, enforce security constraints,
   then call the tool. Numeric validation preserves `json.Number`; integer
   checks compare decimal scale and trailing zeroes with exponent parsing
   saturated to a bound derived from the input length.
4. Recover panics and nil results into normalized tool errors.
5. Apply sensitive-data filtering before returning model-visible content.
6. Resolve the tool adaptation decision from `tools.adaptation`, provider, and model. Apply a matching canonical provider/model profile override before global auto-resolution; omitted override policy fields inherit the global setting. When the visible surface is `auto`, prefer a persisted learned surface with successful tool outcomes for that profile, otherwise select the best-known provider/model heuristic before the session starts, then pin that resolved surface for the session. Account routers own no model profile: router-backed sessions resolve their initial adaptation identity from the first concrete upstream account, and each turn-scoped model override or fallback attempt derives tool definitions from its actual concrete provider/model profile.
7. When the resolved visible surface is `codex`, register compatibility wrappers (`exec_command`, `write_stdin`, `apply_patch`, `view_image`, `update_plan`) over PicoClaw's native backends while preserving the underlying security checks.
8. After LLM responses that report cached input tokens, record a model/API observation keyed by provider, model, visible surface, and stable tool-schema hash. In `auto` cache-sensitivity mode, positive cached-token observations override provider-name heuristics for future runtime downgrade/promotion decisions; cache misses remain visible telemetry but do not prove that mid-session tool-shape changes are safe.
9. After tool execution, record per-tool success/failure counters keyed by provider, model, pinned visible surface, and tool name when adaptation learning is enabled. These counters are persisted and exposed through the adaptation API for future tool-by-tool tuning.
10. When explicitly triggered and `run_model_probes` is enabled, run a bounded no-side-effect LLM call against the requested provider/model profile. `POST /api/tools/adaptation/probe` accepts optional `{provider, model}` JSON; an empty body preserves active-profile behavior. The server resolves only configured concrete upstream model/account credentials, never credentials supplied by the request. The probe validates whether the model emits the expected tool call and records the result as a learned tool outcome without executing the requested probe tool.
11. The adaptation API returns the active resolved provider/model profile plus a deduplicated list of effective provider/model profiles expanded from enabled model-list entries, enabled account routers, enabled model routers, and saved manual overrides. Account routers are expansion sources only: they are not returned as adaptation providers, do not create per-account rows, and do not expose router/account labels in the profile list. Each row reports whether a configured upstream target makes probing available. The Adaptation UI supports local search, per-row probes, and add/edit/remove of overrides for configured provider/model rows; account credentials remain hidden and account-collapsed.
12. For background spawn, ask an AgentLoop-backed spawner to retain runtime
    ownership before starting the goroutine. Run the subturn and callback with
    that retained context and release it exactly once on every completion path.
13. Inject the active transient-UX identity into every tool execution. A child
    turn cloned from that inbound context retains the identity; a `message`
    send copies it only when its destination matches the originating
    channel/chat, and stream lookup prefers the turn-scoped capability before
    falling back to the legacy interface.

## Cross-Feature Behavior

Agent conversations execute tools. MCP and skills add tool-like behavior through
separate features. Hooks can modify, deny, or short-circuit tool calls. Security
policies control credentials, HTTP guards, and isolation. Threads provide a
thread-specific tool and policy surface while relying on the generic registry,
schema export, execution, and settings UI mechanics defined here.
Workflows add an agent-callable management tool and execute step-level tools
through this same registry, including context injection, sensitive-data
filtering, response-handled media delivery, and channel delivery tools.
Git workspaces contribute a built-in agent tool registered through this generic
registry, while acquire, release, cleanup, drop, and inventory semantics are
owned by the git workspaces feature.
Channel delivery owns typing, reaction, placeholder, and stream-marker storage;
tool execution supplies only the opaque turn identity needed for exact
same-chat consumption.

## Failure And Edge Cases

- Missing required tool args return tool errors.
- Panics inside a tool are recovered by the registry.
- Nil tool results are normalized.
- Denied commands and path violations never execute the requested side effect.
- Web providers fail over only according to configured provider behavior.
- Spawn admission failure returns synchronously and launches no goroutine;
  successful admission remains visible to provider reload even after the
  parent turn returns.
- Cross-chat `message` sends omit the originating turn UX identity, and legacy
  channel-manager or stream implementations retain their pre-scoped behavior.
- Invalid, fractional integer, and non-finite numeric arguments fail before
  tool execution. Extremely large positive or negative decimal exponents are
  classified without constructing exponent-sized integers or powers of ten.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-TOOL-001`, `FR-TOOL-007` | [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go), [pkg/providers/tool_schema_transform_test.go](../../pkg/providers/tool_schema_transform_test.go) |
| `FR-TOOL-002` | [pkg/tools/fs](../../pkg/tools/fs), [pkg/tools/fs/filesystem_test.go](../../pkg/tools/fs/filesystem_test.go), [pkg/tools/fs/edit_test.go](../../pkg/tools/fs/edit_test.go) |
| `FR-TOOL-003`, `FR-TOOL-005` | [pkg/tools/shell_test.go](../../pkg/tools/shell_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-TOOL-004` | [pkg/tools/integration/web_test.go](../../pkg/tools/integration/web_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go) |
| `FR-TOOL-006` | [pkg/tools/result_test.go](../../pkg/tools/result_test.go), [pkg/tools/integration](../../pkg/tools/integration), [pkg/tools/hardware](../../pkg/tools/hardware) |
| `FR-TOOL-008` | [web/frontend/src/hooks/use-chat-models.test.ts](../../web/frontend/src/hooks/use-chat-models.test.ts) |
| `FR-TOOL-009` | [pkg/tools/adaptation.go](../../pkg/tools/adaptation.go), [pkg/tools/adaptation_state.go](../../pkg/tools/adaptation_state.go), [pkg/tools/adaptation_probe.go](../../pkg/tools/adaptation_probe.go), [pkg/tools/codex_compat.go](../../pkg/tools/codex_compat.go), [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go), [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go), [web/backend/api/tools.go](../../web/backend/api/tools.go), [web/frontend/src/components/agent/tools/tool-adaptation-tab.tsx](../../web/frontend/src/components/agent/tools/tool-adaptation-tab.tsx) |
| `FR-TOOL-010` | [web/frontend/src/routes/agent/tools.tsx](../../web/frontend/src/routes/agent/tools.tsx), [web/frontend/src/components/agent/tools/tools-page.tsx](../../web/frontend/src/components/agent/tools/tools-page.tsx), [web/frontend/src/components/agent/tools/use-tools-page.ts](../../web/frontend/src/components/agent/tools/use-tools-page.ts) |
| `FR-TOOL-011` | [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/tools/spawn.go](../../pkg/tools/spawn.go) |
| `FR-TOOL-012` | [pkg/tools/integration/message_test.go](../../pkg/tools/integration/message_test.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/agent/agent_turn_ux_test.go](../../pkg/agent/agent_turn_ux_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go) |
| `FR-TOOL-013` | [pkg/tools/validate.go](../../pkg/tools/validate.go), [pkg/tools/validate_test.go](../../pkg/tools/validate_test.go), [pkg/workflows/store_test.go](../../pkg/workflows/store_test.go) |

## Implementation Anchors

- [pkg/tools/registry.go](../../pkg/tools/registry.go)
- [pkg/tools/fs](../../pkg/tools/fs)
- [pkg/tools/integration/web.go](../../pkg/tools/integration/web.go)
- [pkg/tools/shared/base.go](../../pkg/tools/shared/base.go)
- [pkg/tools/validate.go](../../pkg/tools/validate.go)
- [pkg/tools/integration/message.go](../../pkg/tools/integration/message.go)
- [pkg/tools/spawn.go](../../pkg/tools/spawn.go)
