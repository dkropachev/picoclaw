# Hooks And Interception

## Feature ID

`FR-HOOKS`

## Behavior Summary

Hooks let PicoClaw observe runtime events and intercept LLM/tool stages.
Built-in and process hooks can continue, modify, respond, deny, or approve
according to stage-specific rules.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-HOOKS-001` | MUST | Hooks are globally enabled or disabled through config before runtime mounting. | Operators need a single kill switch. |
| `FR-HOOKS-002` | MUST | Built-in and process hooks execute in priority order with configured timeouts. | Hook decisions must be deterministic and bounded. |
| `FR-HOOKS-003` | MUST | Observer hooks receive configured runtime events without modifying execution. | Monitoring must not alter behavior. |
| `FR-HOOKS-004` | MUST | Interceptor hooks can modify before/after LLM and tool payloads when the action is valid for that stage. | Extensions need controlled mutation points. |
| `FR-HOOKS-005` | MUST | `before_tool` hooks can respond with a tool result or deny execution, skipping the actual tool call. | Plugin-like tool behavior and policy gates rely on this. |
| `FR-HOOKS-006` | MUST | Approval hooks decide whether sensitive tool execution may proceed. | Tool approval is a safety boundary. |
| `FR-HOOKS-007` | SHOULD | Process hook JSON protocol failures are reported and do not crash the host. | External hook processes are unreliable by nature. |

## Auxiliary Interfaces

Owns: CONFIG.hooks*
Owns: TEST pkg/agent/hooks*
Owns: TEST pkg/agent/hook*

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `hooks.enabled`, `hooks.defaults`, `hooks.builtins`, `hooks.processes` | Hook enablement, priority, timeout, process transport, observe, and intercept fields. | `FR-HOOKS-001`, `FR-HOOKS-002` |
| Runtime | Hook mount and process pipeline | Stage dispatch, decisions, and tool short-circuiting. | `FR-HOOKS-003` through `FR-HOOKS-007` |

## Cross-Feature Behavior

Agent conversations call hooks around LLM and tool stages. Tool execution may be
modified or skipped. Runtime events are observable input to hooks. Security
policies may be implemented through approval hooks.

## Failure And Edge Cases

- Invalid actions for a stage are ignored or treated as continue according to hook processing rules.
- Timeout ends hook wait and preserves host progress.
- Process hook malformed JSON is logged as hook failure.
- Denied tools return user/model-visible denial text.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-HOOKS-001`, `FR-HOOKS-002`, `FR-HOOKS-003`, `FR-HOOKS-004`, `FR-HOOKS-005`, `FR-HOOKS-006`, `FR-HOOKS-007` | [pkg/agent/hooks_test.go](../../pkg/agent/hooks_test.go), [pkg/agent/hook_mount_test.go](../../pkg/agent/hook_mount_test.go), [pkg/agent/hook_process_test.go](../../pkg/agent/hook_process_test.go), [docs/architecture/hooks/README.md](../architecture/hooks/README.md) |

## Implementation Anchors

- [pkg/agent/hooks.go](../../pkg/agent/hooks.go)
- [pkg/agent/hook_process.go](../../pkg/agent/hook_process.go)
- [docs/architecture/hooks/hook-json-protocol.md](../architecture/hooks/hook-json-protocol.md)
