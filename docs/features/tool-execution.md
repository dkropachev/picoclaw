# Tool Execution

## Feature ID

`FR-TOOL`

## Behavior Summary

PicoClaw exposes built-in tools to the agent for filesystem access, shell
execution, web search/fetch, media delivery, hardware access, and channel
actions. The registry presents tool schemas to providers and executes tool calls
with context, limits, filtering, and error normalization.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-TOOL-001` | MUST | Tool registry registration, lookup, definition export, cloning, allowlist filtering, and execution are concurrency-safe. | Agent turns can execute tools while discovery changes visibility. |
| `FR-TOOL-002` | MUST | Filesystem tools respect workspace restriction, allow path patterns, file size limits, and operation-specific semantics for read/write/edit/append/list/image/send. | Local file access is powerful and must be bounded. |
| `FR-TOOL-003` | MUST | Exec runs commands with configured timeout and deny/allow patterns, supports managed sessions, and returns captured output or structured failure. | Shell access must be useful and controllable. |
| `FR-TOOL-004` | MUST | Web search selects configured providers, honors result/range options, and web fetch observes fetch limits and private host controls. | Search and fetch must be deterministic from config. |
| `FR-TOOL-005` | MUST | Sensitive-data filtering redacts configured secrets from tool results before model exposure when enabled. | Models must not see credentials through tool output. |
| `FR-TOOL-006` | SHOULD | Media, reaction, message, TTS, and hardware tools return handled responses when user-visible delivery is completed outside normal assistant text. | The agent should not duplicate already-delivered output. |
| `FR-TOOL-007` | MUST | Tool schema transformations preserve provider compatibility for OpenAI, Anthropic, Gemini, and compatibility adapters. | Provider-specific schemas must not change tool behavior. |

## Auxiliary Interfaces

Owns: CONFIG.tools.allow_read_paths
Owns: CONFIG.tools.allow_write_paths
Owns: CONFIG.tools
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
Owns: TEST pkg/tools/*
Owns: TEST pkg/seahorse/*
Owns: TEST pkg/media/*
Owns: TOOL append_file
Owns: TOOL edit_file
Owns: TOOL exec
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
Owns: TOOL web_fetch
Owns: TOOL web_search
Owns: TOOL write_file

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Tools | `read_file`, `write_file`, `edit_file`, `append_file`, `list_dir`, `load_image`, `send_file`, `exec`, `web_search`, `web_fetch`, hardware and delivery tools | Built-in tool schemas and execution behavior. | `FR-TOOL-001` through `FR-TOOL-007` |
| HTTP | `/api/tools`, `/api/tools/{name}/state`, `/api/tools/web-search-config` | Launcher tool state and web search configuration. | `FR-TOOL-004` |
| Config | `tools.*` subtrees except MCP, skills, and cron ownership in their feature specs | Tool enablement, limits, providers, filtering, and policies. | `FR-TOOL-002` through `FR-TOOL-006` |

## Cross-Feature Behavior

Agent conversations execute tools. MCP and skills add tool-like behavior through
separate features. Hooks can modify, deny, or short-circuit tool calls. Security
policies control credentials, HTTP guards, and isolation.

## Failure And Edge Cases

- Missing required tool args return tool errors.
- Panics inside a tool are recovered by the registry.
- Nil tool results are normalized.
- Denied commands and path violations never execute the requested side effect.
- Web providers fail over only according to configured provider behavior.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-TOOL-001`, `FR-TOOL-007` | [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go), [pkg/providers/tool_schema_transform_test.go](../../pkg/providers/tool_schema_transform_test.go) |
| `FR-TOOL-002` | [pkg/tools/fs](../../pkg/tools/fs), [pkg/tools/fs/filesystem_test.go](../../pkg/tools/fs/filesystem_test.go), [pkg/tools/fs/edit_test.go](../../pkg/tools/fs/edit_test.go) |
| `FR-TOOL-003`, `FR-TOOL-005` | [pkg/tools/shell_test.go](../../pkg/tools/shell_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-TOOL-004` | [pkg/tools/integration/web_test.go](../../pkg/tools/integration/web_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go) |
| `FR-TOOL-006` | [pkg/tools/result_test.go](../../pkg/tools/result_test.go), [pkg/tools/integration](../../pkg/tools/integration), [pkg/tools/hardware](../../pkg/tools/hardware) |

## Implementation Anchors

- [pkg/tools/registry.go](../../pkg/tools/registry.go)
- [pkg/tools/fs](../../pkg/tools/fs)
- [pkg/tools/integration/web.go](../../pkg/tools/integration/web.go)
