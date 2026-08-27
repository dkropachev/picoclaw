# Trusted Hook-Provided Tool Results

PicoClaw hooks can provide a synthetic result for a tool call with the
`before_tool` `respond` action. This is a fulfillment mechanism for an existing
model-visible tool; it is not a way to create new model authority.

## Required Boundaries

A hook-provided result is accepted only when all of these conditions hold:

1. The tool was registered and callable in the exact registry catalog captured
   for the request.
2. The exact tool definition was offered to the successful provider attempt.
3. The provider's original call and the final hook-rewritten call remain inside
   the active turn profile and protected-tool rules.
4. The hook is explicitly administratively trusted. In-process `NamedHook`
   registrations are trusted compatibility registrations. Process hooks default
   untrusted and require `trusted: true`.
5. The final name and arguments satisfy both the exact offered schema and the
   frozen registry descriptor.
6. The central `ToolPolicy` allows the `hook_respond` fulfillment and every
   configured approval hook approves it.

Only then may the hook result produce a policy-decision event, virtual execution
start/end events, tool feedback, user/media output, or a model-visible result.
The registered tool's `Execute` method and `AfterTool` hooks are not called for
an accepted synthetic result.

The host rejects untrusted `modify`/`respond` actions, nil responses, unknown or
unoffered tool names, policy errors, cancellation, and denied approvals before
any hook result is exposed.

## Process-Hook Configuration

Process hooks are untrusted by default. Observation and narrowing
(`deny_tool`, `abort_turn`, or `hard_abort`) remain available without transform
authority.

```json
{
  "hooks": {
    "processes": {
      "weather-cache": {
        "enabled": true,
        "trusted": true,
        "transport": "stdio",
        "command": ["python3", "/opt/picoclaw/weather_hook.py"],
        "intercept": ["before_tool"]
      }
    }
  }
}
```

Setting `trusted: true` is an administrative capability grant. Hook code runs
outside the policy seam before it returns and can perform its own process or
network effects, so only operator-controlled code should receive it.

## Minimal Protocol Example

The tool must already be registered and present in the provider's offered tool
definitions. A trusted process hook can then answer a matching call:

```python
import json
import sys

for line in sys.stdin:
    request = json.loads(line)
    if request.get("method") != "hook.before_tool":
        print(json.dumps({"jsonrpc": "2.0", "id": request.get("id"), "result": {}}), flush=True)
        continue

    params = request.get("params", {})
    if params.get("tool") != "weather_lookup":
        result = {"action": "continue"}
    else:
        city = params.get("arguments", {}).get("city", "")
        result = {
            "action": "respond",
            "request": {
                **params,
                "hook_result": {
                    "for_llm": f"Cached weather for {city}: sunny",
                    "silent": True
                }
            }
        }

    print(json.dumps({
        "jsonrpc": "2.0",
        "id": request.get("id"),
        "result": result
    }), flush=True)
```

See [Hook JSON Protocol](hook-json-protocol.md) for the exact RPC envelope and
result fields.

## In-Process Example

```go
func (h *WeatherCacheHook) BeforeTool(
    ctx context.Context,
    call *agent.ToolCallHookRequest,
) (*agent.ToolCallHookRequest, agent.HookDecision, error) {
    if call.Tool != "weather_lookup" {
        return call, agent.HookDecision{Action: agent.HookActionContinue}, nil
    }
    next := call.Clone()
    next.HookResult = tools.SilentResult("cached weather: sunny")
    return next, agent.HookDecision{Action: agent.HookActionRespond}, nil
}

registration := agent.NamedHook("weather-cache", &WeatherCacheHook{})
```

Use `UntrustedNamedHook` for observers or narrowing hooks that must not change
LLM/tool data or synthesize results.

## What Hooks Cannot Do

- A `before_llm` hook cannot add a new tool definition; PicoClaw preserves the
  trusted definition set.
- `respond` cannot make an unregistered, hidden-expired, profile-denied,
  reserved, or unoffered tool callable.
- `respond` cannot bypass central policy or approval.
- Hook transport/source does not imply trust.
- A policy allow does not weaken the registered tool's schema, turn profile, or
  exact registry-entry fence.

For a real plugin tool, register its descriptor/traits and implementation through
the tool registry/factory system. Use trusted `respond` only when the registered
capability intentionally permits an alternate synthetic fulfillment such as a
cache or test double.
