# Hooks And Interception

## Feature ID

`FR-HOOKS`

## Behavior Summary

Hooks let PicoClaw observe runtime events and intercept LLM/tool stages.
Built-in and process hooks can continue, modify, respond, deny, or approve
according to stage-specific rules. Administrative trust is independent from
in-process versus process transport: untrusted hooks may observe and narrow but
cannot mutate execution data or synthesize a tool result.

## Reconstruction Notes

- Similarity target: recreate hook mounting, observer/interceptor/approval dispatch, process hook JSON protocol, stage-specific actions, and timeout/error handling.
- Core types/functions: hook config loader, mount registry, `HookSource`, `HookTrust`, `NamedHook`, `UntrustedNamedHook`, process hook client/options, hook decision types, before/after LLM/tool handlers, and approval path.
- Runtime ordering: mount enabled hooks by source/priority, detach each invocation, dispatch observer/interceptor/approval capabilities, accept only trust- and stage-valid decisions, enforce timeout, and compose tool fulfillment with the central policy seam.
- Non-obvious constraints: `respond` and `deny_tool` are tool-stage only, transport does not confer mutation authority, every hook receives detached data, a trusted hook response still requires central policy and approval, and malformed process responses must not crash host runtime.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-HOOKS-001` | MUST | Hooks are globally enabled or disabled through config before runtime mounting. | Operators need a single kill switch. |
| `FR-HOOKS-002` | MUST | Built-in and process hooks execute in priority order with configured timeouts. | Hook decisions must be deterministic and bounded. |
| `FR-HOOKS-003` | MUST | Observer hooks receive configured runtime events without modifying execution. | Monitoring must not alter behavior. |
| `FR-HOOKS-004` | MUST | Only an administratively trusted interceptor may modify before/after LLM or tool payloads when the action is valid for that stage. Every hook invocation and every accepted trusted return is recursively detached; untrusted direct mutation, `modify`, `respond`, replacement response/result, and identity relabeling are discarded, while deny/abort decisions remain narrowing. `AfterTool` may change only result presentation and cannot rename the already-authorized tool or arguments. Existing system-prompt and tool-definition controls remain authoritative even for trusted hooks. | Extension code must not gain model/effect authority from shared object aliasing or transport classification. |
| `FR-HOOKS-005` | MUST | A `before_tool` hook may deny or abort to skip execution, but only an administratively trusted hook may rewrite the final name/arguments or return `respond`. A trusted response requires a non-nil detached result and crosses the same exact offered/prepared central tool policy plus approval chain as registry fulfillment before any feedback, user/media output, or synthetic result. Allowed response performs no registry dispatch or `AfterTool`; a missing/invalid response is a bounded skip and never falls through to ordinary execution. | Plugin-like fulfillment must remain possible without making hook short-circuiting an approval or policy bypass. |
| `FR-HOOKS-006` | MUST | Approval hooks receive detached final tool identity and arguments only after the mandatory central policy allows; the complete approval chain runs for registry and trusted-hook-response fulfillment. No approver is compatibility allow inside that seam, while deny, failure, or timeout fails the approval meet and cannot widen a central denial. | Legacy approval remains a narrowing safety boundary for every client-dispatched fulfillment kind. |
| `FR-HOOKS-007` | SHOULD | Process hook JSON protocol failures are reported and do not crash the host; interceptor failure preserves safe host progress, while approval failure denies the pending fulfillment. | External hook processes are unreliable by nature. |
| `FR-HOOKS-008` | MUST | `HookSource` controls provenance and deterministic source ordering only; separate `HookTrust` controls mutation and synthetic-response authority, and its zero value is untrusted. Raw registrations and configured/direct process hooks default untrusted; process hooks require explicit `trusted: true`, while `NamedHook` and configured built-ins explicitly represent administratively trusted in-process installation and `UntrustedNamedHook` provides the narrowing form. Trusted rewrite/respond provenance is manager-owned, excluded from hook JSON, and passed to tool policy only for the exact administrative hook that supplied the final authority-bearing change. Hook code may itself cause effects before returning, so trust is an operator capability rather than a sandbox or a property inferred from transport. | A local/process label does not prove administrative authority, and untrusted extension output must never fabricate its own trusted provenance. |

## Data And State Model

Hook state includes global defaults, built-in and process hook definitions,
priority order, timeout values, observe/intercept stage lists, process command
state, JSON-RPC request IDs, hook decisions, source ordering, and independently
declared administrative trust. Trust is configuration/runtime metadata, not a
field accepted from process-hook JSON responses.

## Surface Ownership

Owns: CODE pkg/agent/hook*
Owns: CODE pkg/agent/hooks.go
Owns: CONFIG.hooks*
Owns: TEST pkg/agent/hooks*
Owns: TEST pkg/agent/hook*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `hooks.enabled`, `hooks.defaults`, `hooks.builtins`, `hooks.processes.*.trusted` | Hook enablement, priority, timeout, process transport, explicit process-hook trust, observe, and intercept fields. | `FR-HOOKS-001`, `FR-HOOKS-002`, `FR-HOOKS-008` |
| Go API | `HookRegistration`, `HookSource`, `HookTrust`, `NamedHook`, `UntrustedNamedHook`, `ProcessHookOptions.Trusted` | Declare provenance separately from administrative mutation/respond authority; zero/raw/process defaults remain untrusted. | `FR-HOOKS-004`, `FR-HOOKS-008` |
| Runtime | Hook mount and process pipeline | Detached stage dispatch, trust-constrained decisions, tool short-circuiting, and post-policy approval composition. | `FR-HOOKS-003` through `FR-HOOKS-008` |

## Algorithms And Ordering

1. Read hook config and skip all mounting when disabled.
2. Assign source and trust independently, then sort enabled hooks by source,
   priority, and stable name before attaching observer/interceptor/approval
   capabilities.
3. At each runtime stage, recursively detach the current payload for each
   matching hook and call it with a bounded timeout.
4. Validate returned action for the stage. Accept trusted mutation only after
   detaching it again; discard untrusted mutation/respond but preserve
   deny/abort narrowing.
5. For a trusted tool response, return detached final identity, arguments,
   result, and manager-owned provenance to Agent Conversations. That feature
   completes exact offered/prepared checks, central policy, and the ordinary
   approval chain before publishing the response.
6. Continue host execution after interceptor errors unless a valid narrowing
   decision applies; fail an approval meet on approver error or timeout.

## Cross-Feature Behavior

Agent conversations call hooks around LLM and tool stages and own the central
post-hook policy/approval ordering. Tool Execution owns the neutral policy and
prepared-invocation boundary. A trusted hook may narrow or propose synthetic
fulfillment, but neither transport nor `respond` bypasses the exact offered set,
central policy, approval chain, or decision telemetry. Runtime events are
observable input to hooks; observer delivery confers no mutation authority.

## Failure And Edge Cases

- Invalid actions for a stage are ignored or treated as continue according to hook processing rules.
- Timeout ends hook wait and preserves host progress.
- Process hook malformed JSON is logged as hook failure.
- Untrusted mutation/respond is discarded without copying the hook's altered
  graph back into host execution; an untrusted deny/abort still narrows.
- A trusted `respond` with no valid result is skipped and never executes the
  named registry tool as fallback. Policy or approval denial discards its result
  and media before output.
- Denied tools return bounded user/model-visible denial text.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-HOOKS-001`, `FR-HOOKS-002`, `FR-HOOKS-003`, `FR-HOOKS-007` | [pkg/agent/hooks_test.go](../../pkg/agent/hooks_test.go), [pkg/agent/hook_mount_test.go](../../pkg/agent/hook_mount_test.go), [pkg/agent/hook_process_test.go](../../pkg/agent/hook_process_test.go), [docs/architecture/hooks/README.md](../architecture/hooks/README.md) |
| `FR-HOOKS-004`, `FR-HOOKS-008` | [pkg/agent/hooks.go](../../pkg/agent/hooks.go), [pkg/agent/hooks_test.go](../../pkg/agent/hooks_test.go), [pkg/agent/hook_mount.go](../../pkg/agent/hook_mount.go), [pkg/agent/hook_mount_test.go](../../pkg/agent/hook_mount_test.go), [pkg/agent/hook_process.go](../../pkg/agent/hook_process.go), [pkg/agent/hook_process_test.go](../../pkg/agent/hook_process_test.go), [pkg/config/config.go](../../pkg/config/config.go) |
| `FR-HOOKS-005`, `FR-HOOKS-006` | [pkg/agent/pipeline_tool_policy_test.go](../../pkg/agent/pipeline_tool_policy_test.go), [pkg/agent/pipeline_execute.go](../../pkg/agent/pipeline_execute.go), [pkg/agent/tool_policy.go](../../pkg/agent/tool_policy.go), [pkg/agent/hooks_test.go](../../pkg/agent/hooks_test.go) |

## Implementation Anchors

- [pkg/agent/hooks.go](../../pkg/agent/hooks.go)
- [pkg/agent/hook_mount.go](../../pkg/agent/hook_mount.go)
- [pkg/agent/hook_process.go](../../pkg/agent/hook_process.go)
- [docs/architecture/hooks/hook-json-protocol.md](../architecture/hooks/hook-json-protocol.md)
