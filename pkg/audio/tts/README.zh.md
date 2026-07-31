# TTS（文本转语音）

这个目录负责 PicoClaw 的语音合成能力。

如果你是第一次配置 TTS，可以参照下面这个流程：

1. 在 `model_list` 里添加一个支持 TTS 的具体账户。
2. 在 `model_aliases` 里添加一个精确的 TTS 别名。
3. 分别设置 `voice.tts_account_ref` 和别名值 `voice.tts_model_name`。
4. 在 `.security.yml` 里配置对应账户的 API Key。

## 快速推荐

对于大多数用户，建议优先从下面两种开始：

| 提供商 | 推荐理由 |
| --- | --- |
| [OpenAI](https://platform.openai.com/docs/guides/text-to-speech) | 这是 PicoClaw 当前最稳定、最直接的 TTS 路径。当前实现围绕 OpenAI 兼容的 `/audio/speech` 接口格式构建；仍需显式配置账户和别名。 |
| [Xiaomi MiMo](https://platform.xiaomimimo.com) | 由于响应速度和语音音色对于中国用户更友好，MiMo 是一个不错的第二选择。 |

## TTS 配置是如何工作的

PicoClaw 不会把 TTS 的 API Key 放在 `voice` 配置里。

推荐方式是：

- `voice.tts_account_ref` 选择具体账户。
- `voice.tts_model_name` 精确选择 `model_aliases[].name`。
- 别名提供具体模型 ID，也可以为具体账户设置覆盖值。
- `model_list` 账户提供 provider、`api_base`、代理和凭据。
- `.security.yml` 负责保存该账户的 API Key。

这是当前推荐且受支持的配置方式。

## 推荐配置方式

### 方案 A：OpenAI

`config.json`

```json
{
  "voice": {
    "tts_account_ref": "openai-voice",
    "tts_model_name": "tts"
  },
  "model_aliases": [
    {
      "name": "tts",
      "model": "tts-1"
    }
  ],
  "model_list": [
    {
      "model_name": "openai-voice",
      "provider": "openai",
      "model": ""
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  openai-voice:
    api_keys:
      - "sk-openai-your-key"
```

### 方案 B：Xiaomi MiMo

`config.json`

```json
{
  "voice": {
    "tts_account_ref": "mimo-voice",
    "tts_model_name": "tts"
  },
  "model_aliases": [
    {
      "name": "tts",
      "model": "mimo-v2-tts"
    }
  ],
  "model_list": [
    {
      "model_name": "mimo-voice",
      "provider": "mimo",
      "model": ""
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  mimo-voice:
    api_keys:
      - "your-mimo-key"
```

如果你使用自定义的 MiMo 接口地址，也可以显式设置 `api_base`。如果不设置，PicoClaw 会自动使用该 provider 的默认地址。

## PicoClaw 当前实际发送的 TTS 请求

当前 TTS 运行时使用的是 OpenAI 兼容的语音合成请求，并带有以下默认值：

- Endpoint：`/audio/speech`
- 返回格式：`opus`
- Voice：`alloy`
- Model：来自精确解析后的 `model_aliases` 别名

这意味着：

- `openai/tts-1` 可以自然工作。
- 已注册的 OpenAI 兼容 provider 系列（包括 `litellm` 代理）也可以工作，
  前提是上游接受相同的请求格式。
- 未知 provider 名称会直接失败；自定义端点应使用已注册的 provider 系列并设置
  对应的 `api_base`。
- PicoClaw 目前还没有对用户暴露一个配置项来修改 TTS voice，当前固定为 `alloy`。

## PicoClaw 如何选择 TTS Provider

`DetectTTS` 会严格按下面顺序选择 TTS：

1. 根据 `voice.tts_account_ref` 选择一个已启用的具体账户。语音配置不接受
   账户路由器。
2. 精确解析 `voice.tts_model_name` 对应的别名，并应用该具体账户的覆盖值。
3. 使用解析后的 provider、模型和账户凭据创建 TTS provider。

不存在 `model_list` 扫描或 provider 默认模型。缺少或无效的账户/别名会使
TTS 不可用，并返回 `no model configured`。

## 关于 API Base 的处理方式

PicoClaw 会对 TTS 的 `api_base` 做规范化处理：

- 对 OpenAI 来说，像 `https://api.openai.com` 或 `https://api.openai.com/v1` 这样的地址，会自动变成 `https://api.openai.com/v1/audio/speech`。
- 对其他 OpenAI 兼容 provider，PicoClaw 会尽量保留你提供的基础路径，只确保它最终以 `/audio/speech` 结尾。
- 如果没有设置 `api_base`，并且模型前缀是已知 provider，PicoClaw 会自动使用该 provider 的默认地址。

## 常见错误

- `voice.tts_account_ref` 或精确别名不存在。
- 配置了 TTS 账户，但忘了在 `.security.yml` 中配置对应 API Key。
- 误以为 PicoClaw 会自动支持 provider 自定义 voice 参数。
- 使用了不兼容 OpenAI `/audio/speech` 请求格式的接口地址。

## 最小检查清单

在测试 `send_tts` 之前，请确认：

- `voice.tts_account_ref` 能匹配已启用的具体账户。
- `voice.tts_model_name` 能精确匹配 `model_aliases[].name`。
- `.security.yml` 中对应账户已经配置了有效 API Key。
- 你所选的 provider 支持 OpenAI 兼容的语音合成接口。
- 你选择的模型本身确实支持 TTS。
