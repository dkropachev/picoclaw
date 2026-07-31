# ASR（自动语音识别）

这个目录负责 PicoClaw 的语音转文字能力。

如果你是第一次配置 ASR，可以参考如下步骤：

1. 在 `model_list` 里添加一个支持 ASR 的具体账户。
2. 在 `model_aliases` 里添加一个精确的 ASR 别名。
3. 分别设置 `voice.account_ref` 和别名值 `voice.model_name`。
4. 在 `.security.yml` 里配置对应账户的 API Key。

## 快速推荐

对于大多数新用户，建议先从下面两种开始：

| 提供商 | 示例模型 | 推荐理由 |
| --- | --- | --- |
| [Groq](https://console.groq.com/keys) | `groq/whisper-large-v3-turbo` | Whisper 风格转录速度快，并且提供 OpenAI 兼容接口，配置比较直接。Groq 目前官方提供2000请求每日的免费套餐。 |
| [ElevenLabs](https://elevenlabs.io/pricing) | `elevenlabs/scribe_v1` | 上手简单，语音转文字质量也不错。ElevenLabs 目前官方免费套餐包含 STT 用量。 |

价格和免费额度可能会变化，正式使用前请以官网定价页为准。

## ASR 配置是如何工作的

PicoClaw 不会把 ASR 的 API Key 放在 `voice` 配置里。

推荐的方式是：

- `voice.account_ref` 选择一个具体的 `model_list` 账户。
- `voice.model_name` 精确选择 `model_aliases[].name`。
- 别名提供具体模型 ID，也可以为具体账户设置覆盖值。
- 账户提供 provider、API 地址、代理和凭据。
- `.security.yml` 负责保存该账户的 API Key。

这种方式更明确、更安全，也和 PicoClaw 其他模型配置方式保持一致。

## 推荐配置方式

### 方案 A：Groq Whisper

`config.json`

```json
{
  "voice": {
    "account_ref": "groq-voice",
    "model_name": "asr",
    "echo_transcription": true
  },
  "model_aliases": [
    {
      "name": "asr",
      "model": "whisper-large-v3-turbo"
    }
  ],
  "model_list": [
    {
      "model_name": "groq-voice",
      "provider": "groq",
      "model": ""
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  groq-voice:
    api_keys:
      - "gsk_your_groq_key"
```

说明：

- 你可以不写 `api_base`，PicoClaw 会自动使用 Groq 默认接口地址。
- 如果你手动设置 Groq Whisper 的 `api_base`，下面两种写法都可以：
  - `https://api.groq.com/openai/v1`
  - `https://api.groq.com/openai/v1/audio/transcriptions`
- 只要是 OpenAI 兼容、并且模型名里包含 `whisper` 的模型，都可以走 Whisper 转录路径，不仅限于 `whisper-large-v3-turbo`。

### 方案 B：ElevenLabs

`config.json`

```json
{
  "voice": {
    "account_ref": "elevenlabs-voice",
    "model_name": "asr",
    "echo_transcription": true
  },
  "model_aliases": [
    {
      "name": "asr",
      "model": "scribe_v1"
    }
  ],
  "model_list": [
    {
      "model_name": "elevenlabs-voice",
      "provider": "elevenlabs",
      "model": ""
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  elevenlabs-voice:
    api_keys:
      - "sk-elevenlabs-your-key"
```

### 方案 C：OpenAI Whisper

`config.json`

```json
{
  "voice": {
    "account_ref": "openai-voice",
    "model_name": "asr"
  },
  "model_aliases": [
    {
      "name": "asr",
      "model": "whisper-1"
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

## 其他支持 ASR 的模型类型

PicoClaw 目前主要支持三种 ASR 路径：

| 路径 | 示例模型 | 行为说明 |
| --- | --- | --- |
| ElevenLabs ASR | `provider: elevenlabs`，`model: scribe_v1` | 使用 ElevenLabs 的语音转录接口。 |
| Whisper 接口模型 | `openai/whisper-1`、`groq/whisper-large-v3` | 使用 OpenAI 兼容的 `/audio/transcriptions` 接口。 |
| 支持音频的聊天模型 **（重构中）** | `openai/gpt-4o-audio-preview`、`gemini/gemini-2.5-flash` | 把音频发给多模态聊天模型，并要求它返回转录结果。 |

如果你不确定该选哪种，建议优先使用 Groq Whisper 或 ElevenLabs。

## PicoClaw 如何选择转录器

`DetectTranscriber` 会严格按下面顺序选择 ASR：

1. 根据 `voice.account_ref` 选择一个已启用的具体账户。语音配置不接受
   账户路由器。
2. 精确解析 `voice.model_name` 对应的模型别名，并应用该具体账户的覆盖值。
3. 如果解析后的模型属于以下类型：
   - `provider=elevenlabs` 的模型，则使用 ElevenLabs transcriber。
   - OpenAI 兼容的 Whisper 模型，则使用 Whisper transcriber。
   - 支持音频输入的聊天模型，则使用 `AudioModelTranscriber`。

不存在兼容扫描或 provider 默认模型。缺少任一选择时，ASR 不可用。

## 常见错误

- 配置了 ASR 账户，但忘了同时设置 `voice.account_ref` 和精确别名。
- 把 API Key 写进了 `voice`，而不是 `.security.yml`。
- 选择了不支持 ASR 的模型，却期望得到 Whisper 风格的转录结果。
- 自定义了错误的 `api_base`，导致请求打到错误的接口地址。

## 最小检查清单

在测试语音输入前，请确认：

- `voice.account_ref` 能匹配一个已启用的具体账户。
- `voice.model_name` 能精确匹配 `model_aliases[].name`。
- `.security.yml` 中对应账户已经配置了有效 API Key。
- 你选择的模型确实支持 ASR。
- 你当前使用的频道已经启用了语音输入能力。
