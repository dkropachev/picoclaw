# MCP Integration And Discovery

## Feature ID

`FR-MCP`

## Behavior Summary

PicoClaw connects configured MCP servers, discovers tools, wraps remote calls as
agent tools, supports eager and deferred discovery, and provides CLI and launcher
management for server configuration.

## Reconstruction Notes

- Similarity target: recreate an MCP manager that connects configured servers, lists tools, wraps remote tools, handles reconnect cases, and exposes CLI config management.
- Core types/functions: MCP manager, server connection, command/HTTP transport setup, tool wrapper, runtime event publisher, and Cobra MCP subcommands.
- Runtime ordering: load enabled servers, connect transport, initialize session, list tools, register wrappers eagerly or behind discovery, execute remote calls, publish events.
- Non-obvious constraints: CLI mutates config only, server names prefix tool names, env files and headers are transport-specific, and empty server removal disables MCP globally.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-MCP-001` | MUST | Enabled MCP servers connect over stdio, HTTP streamable transport, or SSE-compatible mode using configured command, URL, env, env file, and headers. | MCP compatibility is a core extension point. |
| `FR-MCP-002` | MUST | Tool discovery registers remote tool names with server prefixes and preserves remote descriptions and schemas. | The model needs unambiguous callable tool definitions. |
| `FR-MCP-003` | MUST | Deferred discovery hides remote tools behind search/open behavior until selected. | Large MCP setups must not exhaust context. |
| `FR-MCP-004` | MUST | Remote tool calls forward JSON arguments, return text/media results, and publish start/end runtime events including failures. | MCP execution must be observable and model-readable. |
| `FR-MCP-005` | MUST | MCP CLI add/list/show/test/edit/remove changes only config state and does not keep servers running. | CLI is a configuration manager, not a daemon. |
| `FR-MCP-006` | MUST | Removing the final MCP server disables global MCP enablement. | Empty MCP config should not imply active integration. |
| `FR-MCP-007` | SHOULD | Live server inspection reports reachable status and tool counts without mutating configuration. | Operators need safe diagnostics. |

## Data And State Model

MCP state includes global discovery config, per-server config, live client
sessions, discovered remote tool definitions, generated local tool names,
runtime event metadata, and CLI-managed JSON config entries.

## Surface Ownership

Owns: CLI cmd/picoclaw/internal/mcp/*
Owns: CONFIG.tools.mcp*
Owns: TEST cmd/picoclaw/internal/mcp/*
Owns: TEST pkg/mcp/*
Owns: TEST pkg/tools/integration/mcp*
Owns: INTEGRATION *
Owns: EVENT mcp.*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `tools.mcp.*` | Global enablement, discovery settings, and per-server transport details. | `FR-MCP-001`, `FR-MCP-003` |
| CLI | `picoclaw mcp add/list/show/test/edit/remove` | Config management and live diagnostics. | `FR-MCP-005`, `FR-MCP-006`, `FR-MCP-007` |
| Runtime | MCP manager and MCP tool wrapper | Connection lifecycle, discovery, and remote tool execution. | `FR-MCP-001`, `FR-MCP-002`, `FR-MCP-004` |
| Integration | Docker-backed MCP streamable suite | Real server protocol compatibility. | `FR-MCP-001`, `FR-MCP-004` |

## Algorithms And Ordering

1. Normalize server transport from config or CLI flags.
2. For stdio, build command/env/env-file transport; for remote, build streamable HTTP transport with headers.
3. Initialize the client session and list remote tools.
4. Register tools eagerly or hide them behind discovery based on global/per-server deferral.
5. On tool call, forward arguments and convert MCP content into PicoClaw tool result text/media.

## Cross-Feature Behavior

Agent conversations consume MCP tools through the normal registry. Tool
execution handles schema export and result formatting. Runtime events expose
server and tool lifecycle. Security and isolation affect stdio process startup.

## Failure And Edge Cases

- Disabled servers are skipped.
- Missing commands, invalid URLs, and connection failures produce server failed events.
- Session-missing errors can trigger reconnection behavior.
- HTTP headers are attached only to configured remote transports.
- Deferred per-server override wins over global discovery defaults.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-MCP-001`, `FR-MCP-002`, `FR-MCP-004`, `FR-MCP-007` | [pkg/mcp/manager_test.go](../../pkg/mcp/manager_test.go), [pkg/mcp/manager_integration_test.go](../../pkg/mcp/manager_integration_test.go), [pkg/mcp/manager_real_server_integration_test.go](../../pkg/mcp/manager_real_server_integration_test.go) |
| `FR-MCP-003` | [pkg/tools/search_tools_test.go](../../pkg/tools/search_tools_test.go), [docs/reference/tools_configuration.md](../reference/tools_configuration.md) |
| `FR-MCP-005`, `FR-MCP-006` | [cmd/picoclaw/internal/mcp/command_test.go](../../cmd/picoclaw/internal/mcp/command_test.go), [docs/reference/mcp-cli.md](../reference/mcp-cli.md) |
| `FR-MCP-001`, `FR-MCP-004` | [integration/README.md](../../integration/README.md), [integration/suites/mcp-streamable](../../integration/suites/mcp-streamable) |

## Implementation Anchors

- [pkg/mcp/manager.go](../../pkg/mcp/manager.go)
- [pkg/tools/integration/mcp_tool.go](../../pkg/tools/integration/mcp_tool.go)
- [cmd/picoclaw/internal/mcp](../../cmd/picoclaw/internal/mcp)
