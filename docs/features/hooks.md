# Hooks And Interception

## Feature ID

`FR-HOOKS`

## Behavior Summary

Hooks let PicoClaw observe runtime events and intercept LLM/tool stages.
Built-in and process hooks can continue, modify, respond, deny, or approve
according to stage-specific rules.

## Reconstruction Notes

- Similarity target: recreate hook mounting, observer/interceptor/approval dispatch, process hook JSON protocol, stage-specific actions, and timeout/error handling.
- Core types/functions: hook config loader, mount registry, process hook client, hook decision types, before/after LLM/tool handlers, and approval path.
- Runtime ordering: mount enabled hooks by priority, dispatch observer events, invoke interceptors around LLM/tool stages, apply valid decisions, enforce timeout, continue on failures.
- Non-obvious constraints: `respond` and `deny_tool` are tool-stage only, observer hooks must not mutate execution, and malformed process responses must not crash host runtime.

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

## Data And State Model

Hook state includes global defaults, built-in and process hook definitions,
priority order, timeout values, observe/intercept stage lists, process command
state, JSON-RPC request IDs, and hook decisions.

## Surface Ownership

Owns: CODE pkg/agent/hook*
Owns: CODE pkg/agent/hooks.go
Owns: CONFIG.hooks*
Owns: TEST pkg/agent/hooks*
Owns: TEST pkg/agent/hook*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `hooks.enabled`, `hooks.defaults`, `hooks.builtins`, `hooks.processes` | Hook enablement, priority, timeout, process transport, observe, and intercept fields. | `FR-HOOKS-001`, `FR-HOOKS-002` |
| Runtime | Hook mount and process pipeline | Stage dispatch, decisions, and tool short-circuiting. | `FR-HOOKS-003` through `FR-HOOKS-007` |

## Algorithms And Ordering

1. Read hook config and skip all mounting when disabled.
2. Sort enabled hooks by priority and attach observer/interceptor/approval capabilities.
3. At each runtime stage, call matching hooks with bounded timeout.
4. Validate returned action for the stage before mutating requests/results.
5. Continue host execution after hook errors unless a valid deny/respond decision applies.

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
