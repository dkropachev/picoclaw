# Hook JSON-RPC 协议详解

所有 hook 使用 `JSON-RPC 2.0` 格式，每行一个 JSON 消息，通过 stdio 传输。

---

## 基础协议结构

### 请求（PicoClaw → Hook）

```json
{"jsonrpc":"2.0","id":1,"method":"hook.xxx","params":{...}}
```

### 响应（Hook → PicoClaw）

成功：
```json
{"jsonrpc":"2.0","id":1,"result":{...}}
```

错误：
```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"错误信息"}}
```

---

## 1. `hook.hello`（握手）

启动时必须完成握手，否则 hook 进程会被终止。

### 请求

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "hook.hello",
  "params": {
    "name": "py_review_gate",
    "version": 1,
    "modes": ["observe", "tool", "approve"]
  }
}
```

| 字段 | 说明 |
|------|------|
| `name` | hook 名称（来自配置） |
| `version` | 协议版本，当前为 `1` |
| `modes` | hook 支持的能力模式 |

### 响应

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "ok": true,
    "name": "python-review-gate"
  }
}
```

---

## 2. `hook.before_llm`

在发送请求给 LLM 之前触发。可用于注入工具。

### 请求

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "hook.before_llm",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "ParentTurnID": "",
      "SessionKey": "session-1",
      "Iteration": 0,
      "TracePath": "runTurn",
      "Source": "turn.llm.request"
    },
    "model": "claude-sonnet",
    "messages": [
      {"role": "user", "content": "hello"}
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "echo",
          "description": "echo text",
          "parameters": {"type": "object"}
        }
      }
    ],
    "options": {
      "temperature": 0.7
    },
    "channel": "cli",
    "chat_id": "chat-1",
    "graceful_terminal": false
  }
}
```

| 字段 | 说明 |
|------|------|
| `meta` | 事件元数据，用于追踪 |
| `model` | 请求的模型名称 |
| `messages` | 对话历史 |
| `tools` | 可用工具定义列表 |
| `options` | LLM 参数（temperature、max_tokens 等） |
| `channel` | 请求来源通道 |
| `chat_id` | 会话 ID |

### 响应（注入工具示例）

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "action": "modify",
    "request": {
      "model": "claude-sonnet",
      "messages": [{"role": "user", "content": "hello"}],
      "tools": [
        {
          "type": "function",
          "function": {
            "name": "echo",
            "description": "echo",
            "parameters": {}
          }
        },
        {
          "type": "function",
          "function": {
            "name": "my_plugin_tool",
            "description": "插件注入的工具",
            "parameters": {
              "type": "object",
              "properties": {
                "query": {"type": "string"}
              }
            }
          }
        }
      ]
    }
  }
}
```

| 字段 | 说明 |
|------|------|
| `action` | 决策动作（见下表） |
| `request` | 修改后的请求对象 |

---

## 3. `hook.after_llm`

在收到 LLM 响应后触发。可修改响应内容。

### 请求

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "hook.after_llm",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "model": "claude-sonnet",
    "response": {
      "role": "assistant",
      "content": "Hi!",
      "tool_calls": [
        {
          "id": "tc-1",
          "type": "function",
          "function": {
            "name": "echo",
            "arguments": "{\"text\":\"hi\"}"
          }
        }
      ]
    },
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

### 响应

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "action": "continue"
  }
}
```

---

## 4. `hook.before_tool`

在执行工具前触发。可修改工具名称和参数，或拒绝执行，或直接返回结果。

### 请求

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "hook.before_tool",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "tool": "echo_text",
    "arguments": {
      "text": "hello"
    },
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

| 字段 | 说明 |
|------|------|
| `tool` | 工具名称 |
| `arguments` | 工具参数 |

### 响应（改写参数）

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "action": "modify",
    "call": {
      "tool": "echo_text",
      "arguments": {
        "text": "modified hello"
      }
    }
  }
}
```

### 响应（拒绝执行）

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "action": "deny_tool",
    "reason": "参数不合法"
  }
}
```

### 响应（直接返回结果 - respond）

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "action": "respond",
    "call": {
      "tool": "my_plugin_tool",
      "arguments": {
        "query": "hello"
      }
    },
    "result": {
      "for_llm": "Plugin tool executed successfully",
      "for_user": "",
      "silent": false,
      "is_error": false
    }
  }
}
```

`respond` action 允许管理员可信 hook 为已注册且实际提供的工具返回合成结果。结果对外可见前仍必须通过中央工具 policy 和 approval。适用于：
1. **替代履行方式**：通过已批准的缓存或服务响应现有工具
2. **工具结果缓存**：对重复调用返回缓存结果
3. **工具模拟**：测试时返回模拟结果

| 字段 | 说明 |
|------|------|
| `action` | 必须为 `respond` |
| `call` | 修改后的调用信息（可选） |
| `result` | 直接返回的工具结果 |

---

## 5. `hook.after_tool`

在工具执行完成后触发。可修改返回给 LLM 的结果。

### 请求

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "hook.after_tool",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "tool": "echo_text",
    "arguments": {
      "text": "hello"
    },
    "result": {
      "for_llm": "echoed: hello",
      "for_user": "",
      "silent": false,
      "is_error": false,
      "async": false,
      "media": [],
      "artifact_tags": [],
      "response_handled": false
    },
    "duration": 15000000,
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

| 字段 | 说明 |
|------|------|
| `result.for_llm` | 返回给 LLM 的内容 |
| `result.for_user` | 发送给用户的内容 |
| `result.silent` | 是否静默（不发送给用户） |
| `result.is_error` | 是否为错误 |
| `result.async` | 是否异步执行 |
| `result.media` | 媒体引用列表 |
| `result.artifact_tags` | 本地产物路径标签 |
| `result.response_handled` | 是否已处理响应 |
| `duration` | 执行耗时（纳秒） |

### 响应

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "action": "continue"
  }
}
```

---

## 6. `hook.approve_tool`

审批型 hook，用于决定是否允许执行敏感工具。

### 请求

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "hook.approve_tool",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "tool": "bash",
    "arguments": {
      "command": "rm -rf /"
    },
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

### 响应（批准）

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "approved": true
  }
}
```

### 响应（拒绝）

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "approved": false,
    "reason": "危险命令，禁止执行"
  }
}
```

---

## 7. `hook.runtime_event`（notification）

runtime 观察型事件，仅广播，无需响应。`id` 为 `0` 或不存在。

```json
{
  "jsonrpc": "2.0",
  "method": "hook.runtime_event",
  "params": {
    "kind": "agent.tool.exec_start",
    "source": {
      "component": "agent",
      "name": "agent-1"
    },
    "scope": {
      "agent_id": "agent-1",
      "session_key": "session-1",
      "turn_id": "turn-1",
      "channel": "cli",
      "chat_id": "chat-1"
    },
    "payload": {
      "Tool": "echo_text",
      "Arguments": {"text": "hello"}
    }
  }
}
```

常见 `Kind` 值：
- `agent.turn.start` / `agent.turn.end`
- `agent.llm.request` / `agent.llm.response`
- `agent.tool.policy_decision`
- `agent.tool.exec_start` / `agent.tool.exec_end` / `agent.tool.exec_skipped`
- `agent.steering.injected`
- `agent.interrupt.received`
- `agent.error`

旧 observe 配置名如 `turn_end`、`tool_exec_start`、`tool_policy_decision` 仍然可用，并会归一化为 runtime event 名称。新的 process hook 通知使用 `hook.runtime_event`。

---

## action 可选值

| action | 适用 hook | 效果 |
|--------|----------|------|
| `continue` | 所有拦截型 | 放行，不做修改 |
| `modify` | `before_llm`, `before_tool`, `after_llm`, `after_tool` | 改写请求/响应后放行 |
| `respond` | `before_tool` | 直接返回工具结果，跳过实际执行 |
| `deny_tool` | `before_tool` | 拒绝执行该工具 |
| `abort_turn` | 所有拦截型 | 中止当前 turn，返回错误 |
| `hard_abort` | 所有拦截型 | 强制终止整个 agent loop |

---

## 完整流程示例

```json
{"jsonrpc":"2.0","id":1,"method":"hook.hello","params":{"name":"my_hook","version":1,"modes":["tool","approve"]}}
{"jsonrpc":"2.0","id":1,"result":{"ok":true,"name":"my_hook"}}
{"jsonrpc":"2.0","id":2,"method":"hook.before_llm","params":{"model":"claude-sonnet","messages":[{"role":"user","content":"hello"}],"tools":[]}}
{"jsonrpc":"2.0","id":2,"result":{"action":"continue"}}
{"jsonrpc":"2.0","id":3,"method":"hook.before_tool","params":{"tool":"bash","arguments":{"command":"ls"}}}
{"jsonrpc":"2.0","id":3,"result":{"action":"continue"}}
{"jsonrpc":"2.0","id":4,"method":"hook.approve_tool","params":{"tool":"bash","arguments":{"command":"ls"}}}
{"jsonrpc":"2.0","id":4,"result":{"approved":true}}
{"jsonrpc":"2.0","id":5,"method":"hook.after_tool","params":{"tool":"bash","arguments":{"command":"ls"},"result":{"for_llm":"file1.txt\nfile2.txt"},"duration":5000000}}
{"jsonrpc":"2.0","id":5,"result":{"action":"continue"}}
{"jsonrpc":"2.0","id":6,"method":"hook.after_llm","params":{"model":"claude-sonnet","response":{"role":"assistant","content":"已列出文件"}}}
{"jsonrpc":"2.0","id":6,"result":{"action":"continue"}}
```

---

## 通过 `before_tool` 提供可信替代结果

`before_llm` 不能注入新工具定义；PicoClaw 会保留可信定义集合。必须先通过
registry/factory 系统注册工具。若成功 provider 请求实际收到该精确工具，配置了
`trusted: true` 的 process hook 才可提供替代结果：

```python
def handle_before_tool(params: dict) -> dict:
    tool = params.get("tool", "")

    if tool == "weather_lookup":
        args = params.get("arguments", {})
        city = args.get("city", "")

        # 结果对外可见前仍会执行 policy 和 approval。
        return {
            "action": "respond",
            "result": {
                "for_llm": f"{city} 的缓存天气：晴",
                "silent": False,
                "is_error": False
            }
        }

    return {"action": "continue"}
```

不可信 process hook 可以观察或拒绝该调用，但其 `modify` 或 `respond` 结果会被丢弃。
详情请参阅[可信 Hook 提供工具结果](plugin-tool-injection.zh.md)。
