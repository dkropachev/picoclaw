# 可信 Hook 提供工具结果

PicoClaw hook 可以通过 `before_tool` 的 `respond` action 为工具调用提供合成结果。
这是已有模型可见工具的一种履行方式，不是创建新模型权限的机制。

## 必须满足的边界

只有同时满足以下条件，hook 结果才会被接受：

1. 工具已注册，并且在本次请求捕获的精确 registry catalog 中可调用。
2. 成功 provider 尝试实际收到过该工具的精确定义。
3. provider 原始调用和 hook 改写后的最终调用都符合当前 turn profile 和受保护工具规则。
4. hook 被显式声明为管理员可信。进程内 `NamedHook` 是兼容性可信注册；process hook
   默认不可信，必须配置 `trusted: true`。
5. 最终名称和参数同时符合实际提供给 provider 的 schema 与冻结的 registry descriptor。
6. 中央 `ToolPolicy` 允许 `hook_respond` 履行方式，且所有 approval hook 都批准。

满足这些条件后，hook 结果才可以产生 policy-decision、虚拟执行开始/结束事件、
工具反馈、用户/媒体输出或模型可见结果。接受合成结果时，不会调用已注册工具的
`Execute`，也不会调用 `AfterTool` hook。

不可信的 `modify`/`respond`、空结果、未知或未提供的工具名、policy 错误、取消以及
approval 拒绝都会在 hook 结果对外可见前被拒绝。

## Process Hook 配置

Process hook 默认不可信。未授予变换权限时，仍可观察并收窄权限（`deny_tool`、
`abort_turn` 或 `hard_abort`）。

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

`trusted: true` 是管理员能力授权。Hook 代码在返回之前位于 policy seam 之外，可以自行
产生进程或网络副作用，因此只能授予由 operator 控制的代码。

## 最小协议示例

工具必须已经注册，并出现在 provider 实际收到的工具定义中。可信 process hook 随后
可以响应匹配调用：

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
                    "for_llm": f"{city} 的缓存天气：晴",
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

精确 RPC envelope 和结果字段请参阅 [Hook JSON 协议](hook-json-protocol.zh.md)。

## 进程内示例

```go
func (h *WeatherCacheHook) BeforeTool(
    ctx context.Context,
    call *agent.ToolCallHookRequest,
) (*agent.ToolCallHookRequest, agent.HookDecision, error) {
    if call.Tool != "weather_lookup" {
        return call, agent.HookDecision{Action: agent.HookActionContinue}, nil
    }
    next := call.Clone()
    next.HookResult = tools.SilentResult("缓存天气：晴")
    return next, agent.HookDecision{Action: agent.HookActionRespond}, nil
}

registration := agent.NamedHook("weather-cache", &WeatherCacheHook{})
```

仅需观察或收窄、不能变更 LLM/工具数据或生成结果时，请使用 `UntrustedNamedHook`。

## Hook 不能做什么

- `before_llm` hook 不能新增工具定义；PicoClaw 会保留可信定义集合。
- `respond` 不能让未注册、隐藏 TTL 已过期、profile 禁止、保留或未提供的工具变得可调用。
- `respond` 不能绕过中央 policy 或 approval。
- Hook transport/source 不代表可信。
- Policy 允许不会削弱工具 schema、turn profile 或精确 registry-entry fence。

真正的插件工具应通过 tool registry/factory 系统注册 descriptor、traits 和实现。只有在
已注册能力明确允许缓存或测试替身等合成履行方式时，才使用可信 `respond`。
