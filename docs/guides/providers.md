# 🔌 Providers & Model Configuration

> Back to [README](../README.md)

> [!IMPORTANT]
> Config version 4 separates accounts from model aliases. `model_list[]` and
> credentials configure accounts; agents, chat, workflows, and voice select an
> `account_ref` plus an exact `model_aliases[].name`. Standalone provider
> fragments below are account examples, not executable model defaults. PicoClaw
> does not infer a model from them; a missing alias fails with
> `no model configured`.

### Providers

> [!NOTE]
> Voice transcription requires `voice.account_ref` plus an exact alias in
> `voice.model_name`. Groq Whisper is available only when explicitly configured
> as that account-and-alias pair.

| Provider     | Purpose                                 | Get API Key                                                  |
| ------------ | --------------------------------------- | ------------------------------------------------------------ |
| `gemini`     | LLM (Gemini direct)                     | [aistudio.google.com](https://aistudio.google.com)           |
| `zhipu`      | LLM (Zhipu direct)                      | [bigmodel.cn](https://bigmodel.cn)                           |
| `zai-coding` | LLM (Z.AI Coding Plan)                | [z.ai](https://z.ai/manage-apikey/apikey-list)           |
| `volcengine` | LLM(Volcengine direct)                  | [volcengine.com](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw)                 |
| `openrouter` | LLM (recommended, access to all models) | [openrouter.ai](https://openrouter.ai)                       |
| `anthropic`  | LLM (Claude direct)                     | [console.anthropic.com](https://console.anthropic.com)       |
| `openai`     | LLM (GPT direct)                        | [platform.openai.com](https://platform.openai.com)           |
| `venice`     | LLM (Venice AI direct)                  | [venice.ai](https://venice.ai)                               |
| `nearai`     | LLM (NEAR AI Cloud TEE inference)       | [near.ai](https://near.ai)                                   |
| `deepseek`   | LLM (DeepSeek direct)                   | [platform.deepseek.com](https://platform.deepseek.com)       |
| `qwen`       | LLM (Qwen direct)                       | [dashscope.console.aliyun.com](https://dashscope.console.aliyun.com) |
| `groq`       | LLM + **Voice transcription** (Whisper) | [console.groq.com](https://console.groq.com)                 |
| `cerebras`   | LLM (Cerebras direct)                   | [cerebras.ai](https://cerebras.ai)                           |
| `vivgrid`    | LLM (Vivgrid direct)                    | [vivgrid.com](https://vivgrid.com)                           |
| `nvidia`     | LLM (NVIDIA NIM)                        | [build.nvidia.com](https://build.nvidia.com)                 |
| `moonshot`   | LLM (Kimi/Moonshot direct)              | [platform.moonshot.cn](https://platform.moonshot.cn)         |
| `minimax`    | LLM (Minimax direct)                    | [platform.minimaxi.com](https://platform.minimaxi.com)      |
| `avian`      | LLM (Avian direct)                      | [avian.io](https://avian.io)                                 |
| `mistral`    | LLM (Mistral direct)                    | [console.mistral.ai](https://console.mistral.ai)            |
| `longcat`    | LLM (Longcat direct)                    | [longcat.ai](https://longcat.ai)                             |
| `modelscope` | LLM (ModelScope direct)                 | [modelscope.cn](https://modelscope.cn)                       |
| `mimo`       | LLM (Xiaomi MiMo direct)                | [platform.xiaomimimo.com](https://platform.xiaomimimo.com)   |

### Provider Account Configuration (`model_list`)

`model_list[]` owns provider transport and account identity. Create a separate
`model_aliases[]` entry for every concrete model policy you intend to execute.

For agent dispatch and light-model routing examples, see the [Routing Guide](routing-guide.md).

This design also enables **multi-agent support** with flexible provider selection:

- **Different agents, different accounts**: Each agent can select its own
  `account_ref`
- **Alias fallbacks**: Configure exact primary and fallback aliases
- **Load balancing**: Distribute requests across multiple endpoints
- **Centralized configuration**: Manage all providers in one place

#### 📋 All Supported Vendors

| Vendor              | `provider` Value  | Default API Base                                    | Protocol  | API Key                                                          |
| ------------------- | ----------------- |-----------------------------------------------------| --------- | ---------------------------------------------------------------- |
| **OpenAI**          | `openai`          | `https://api.openai.com/v1`                         | OpenAI    | [Get Key](https://platform.openai.com)                           |
| **Venice AI**       | `venice`          | `https://api.venice.ai/api/v1`                      | OpenAI    | [Get Key](https://venice.ai)                                     |
| **NEAR AI Cloud**   | `nearai`          | `https://cloud-api.near.ai/v1`                      | OpenAI    | [Get Key](https://near.ai)                                       |
| **Anthropic**       | `anthropic`       | `https://api.anthropic.com/v1`                      | Anthropic | [Get Key](https://console.anthropic.com)                         |
| **智谱 AI (GLM)**   | `zhipu`           | `https://open.bigmodel.cn/api/paas/v4`              | OpenAI    | [Get Key](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) |
| **Z.AI Coding Plan** | `openai`         | `https://api.z.ai/api/coding/paas/v4`               | OpenAI    | [Get Key](https://z.ai/manage-apikey/apikey-list)                |
| **DeepSeek**        | `deepseek`        | `https://api.deepseek.com/v1`                       | OpenAI    | [Get Key](https://platform.deepseek.com)                         |
| **Google Gemini**   | `gemini`          | `https://generativelanguage.googleapis.com/v1beta`  | Gemini    | [Get Key](https://aistudio.google.com/api-keys)                  |
| **Groq**            | `groq`            | `https://api.groq.com/openai/v1`                    | OpenAI    | [Get Key](https://console.groq.com)                              |
| **Moonshot**        | `moonshot`        | `https://api.moonshot.cn/v1`                        | OpenAI    | [Get Key](https://platform.moonshot.cn)                          |
| **通义千问 (Qwen)** | `qwen`            | `https://dashscope.aliyuncs.com/compatible-mode/v1` | OpenAI    | [Get Key](https://dashscope.console.aliyun.com)                  |
| **NVIDIA**          | `nvidia`          | `https://integrate.api.nvidia.com/v1`               | OpenAI    | [Get Key](https://build.nvidia.com)                              |
| **Ollama**          | `ollama`          | `http://localhost:11434/v1`                         | OpenAI    | Local (no key needed)                                            |
| **LM Studio**       | `lmstudio`        | `http://localhost:1234/v1`                          | OpenAI    | Optional (local default: no key)                                 |
| **OpenRouter**      | `openrouter`      | `https://openrouter.ai/api/v1`                      | OpenAI    | [Get Key](https://openrouter.ai/keys)                            |
| **LiteLLM Proxy**   | `litellm`         | `http://localhost:4000/v1`                          | OpenAI    | Your LiteLLM proxy key                                           |
| **VLLM**            | `vllm`            | `http://localhost:8000/v1`                          | OpenAI    | Local                                                            |
| **Cerebras**        | `cerebras`        | `https://api.cerebras.ai/v1`                        | OpenAI    | [Get Key](https://cerebras.ai)                                   |
| **VolcEngine (Doubao)** | `volcengine`  | `https://ark.cn-beijing.volces.com/api/v3`          | OpenAI    | [Get Key](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) |
| **神算云**          | `shengsuanyun`    | `https://router.shengsuanyun.com/api/v1`            | OpenAI    | -                                                                |
| **BytePlus**        | `byteplus`        | `https://ark.ap-southeast.bytepluses.com/api/v3`    | OpenAI    | [Get Key](https://www.byteplus.com)                              |
| **Vivgrid**         | `vivgrid`         | `https://api.vivgrid.com/v1`                        | OpenAI    | [Get Key](https://vivgrid.com)                                   |
| **LongCat**         | `longcat`         | `https://api.longcat.chat/openai`                   | OpenAI    | [Get Key](https://longcat.chat/platform)                         |
| **ModelScope (魔搭)**| `modelscope`     | `https://api-inference.modelscope.cn/v1`            | OpenAI    | [Get Token](https://modelscope.cn/my/tokens)                     |
| **Xiaomi MiMo**     | `mimo`            | `https://api.xiaomimimo.com/v1`                     | OpenAI    | [Get Key](https://platform.xiaomimimo.com)                       |
| **Azure OpenAI**    | `azure`           | `https://{resource}.openai.azure.com`               | Azure     | [Get Key](https://portal.azure.com)                              |
| **Antigravity**     | `antigravity`     | Google Cloud                                        | Custom    | OAuth only                                                       |
| **GitHub Copilot**  | `github-copilot`  | `localhost:4321`                                    | gRPC      | -                                                                |

#### Basic Configuration

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "volcengine-work",
      "provider": "volcengine",
      "model": "",
      "api_keys": ["sk-your-api-key"]
    },
    {
      "model_name": "openai-work",
      "provider": "openai",
      "model": "",
      "api_keys": ["sk-your-openai-key"]
    },
    {
      "model_name": "anthropic-work",
      "provider": "anthropic",
      "model": "",
      "api_keys": ["sk-ant-your-key"]
    },
    {
      "model_name": "zhipu-work",
      "provider": "zhipu",
      "model": "",
      "api_keys": ["your-zhipu-key"]
    }
  ],
  "model_aliases": [
    {
      "name": "coding",
      "model": "gpt-5.4",
      "account_overrides": {
        "volcengine-work": "ark-code-latest",
        "anthropic-work": "claude-sonnet-4.6",
        "zhipu-work": "glm-4.7"
      }
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "openai-work",
      "model_name": "coding"
    }
  }
}
```

#### `model_list` Entry Fields

| Field | Type | Required | Description                                                                                                                                                                                                                                 |
|-------|------|----------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `model_name` | string | Yes | Concrete account/transport identity selected through `account_ref` |
| `provider` | string | No | Provider identifier used by this account |
| `model` | string | No | Legacy provider-profile metadata; v4 execution gets the concrete model from an exact alias |
| `api_keys` | string[] | Yes* | API key(s) for authentication. Multiple keys enable per-request rotation. Not required for local providers (Ollama, LM Studio, VLLM)                                                                                                        |
| `auth_method` | string | No | Authentication method for providers with non-API-key auth. Supported values include `oauth` and `token`                                                                                                                                     |
| `credential_id` | string | No | Named OAuth/token credential from the auth store. Empty uses the provider default (for example `openai`); bare names are provider-scoped (for example `work` becomes `openai:work`)                                                        |
| `api_base` | string | No | Override the default API endpoint URL                                                                                                                                                                                                       |
| `proxy` | string | No | HTTP proxy URL for this account entry |
| `user_agent` | string | No | Custom `User-Agent` header sent with API requests (supported by OpenAI-compatible, Gemini, Anthropic, and Azure providers)                                                                                                                  |
| `request_timeout` | int | No | Request timeout in seconds (default varies by provider)                                                                                                                                                                                     |
| `max_tokens_field` | string | No | Override the max tokens field name in request body (e.g., `max_completion_tokens` for o1 models)                                                                                                                                            |
| `thinking_level` | string | No | Extended thinking level: `off`, `low`, `medium`, `high`, `xhigh`, or `adaptive`                                                                                                                                                             |
| `reasoning_effort` | string | No | OpenAI-style reasoning effort: `none`, `minimal`, `low`, `medium`, `high`, or `xhigh`                                                                                                                                                         |
| `tool_schema_transform` | string | No | Optional compatibility transform for tool parameter schemas. Default: disabled. Supported values: `simple`.                                                                                             |
| `extra_body` | object | No | Additional fields to inject into every request body                                                                                                                                                                                         |
| `custom_headers` | object | No | Additional HTTP headers to inject into every request (e.g., `{"X-Source":"coding-plan"}`). If a key matches a built-in header, the custom value overrides the built-in one (e.g., `Authorization`, `User-Agent`, `Content-Type`, `Accept`). |
| `streaming.enabled` | bool | No | Opt-in for provider streaming on this account entry. Defaults to `false` and also requires the active channel's `settings.streaming.enabled` to be `true`. |
| `rpm` | int | No | Per-minute request rate limit                                                                                                                                                                                                               |
| `fallbacks` | string[] | No | Legacy field; configure alias-valued fallbacks under the agent model policy |
| `enabled` | bool | No | Whether this account entry can be selected |

When streaming is disabled, omit the `streaming` block. Writing `"streaming": {"enabled": false}` is optional and not needed in generated or hand-written config.

`extra_body` is especially useful for model-specific TTS fields on OpenAI-compatible
speech routes, for example custom `voice` names or `response_format: "mp3"`.

`model_aliases[]` requires an exact `name` and base concrete `model`.
`account_overrides` may map concrete account refs to alternate concrete model
IDs; router names are invalid override keys.

#### Multiple OAuth Accounts for the Same Provider

OAuth/token credentials can be saved under provider-scoped names and selected
per `model_list` account with `credential_id`. Use an account router when
several credentials should participate in account failover; model aliases
remain independent.

```bash
picoclaw auth login --provider openai --device-code --credential-id work
picoclaw auth login --provider openai --device-code --credential-id personal
```

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "codex-work",
      "provider": "openai",
      "model": "",
      "auth_method": "oauth",
      "credential_id": "openai:work"
    },
    {
      "model_name": "codex-personal",
      "provider": "openai",
      "model": "",
      "auth_method": "oauth",
      "credential_id": "openai:personal"
    }
  ],
  "model_aliases": [
    {
      "name": "codex",
      "model": "gpt-5.3-codex"
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "codex-work",
      "model_name": "codex"
    }
  }
}
```

Changing `account_ref` changes the credential account without changing the
alias. An account router can automate that choice while preserving the same
exact alias.

#### Tool Schema Compatibility

By default, PicoClaw now forwards tool JSON Schemas unchanged.

Some providers reject advanced JSON Schema features such as `$ref`, `$defs`, `anyOf`, `oneOf`, `allOf`, `pattern`, or numeric/string constraints inside tool declarations. For those models, you can opt into a compatibility transform per model entry with `tool_schema_transform`.

Use `simple` when the upstream provider expects the conservative style function schema subset:

```json
{
  "model_name": "gemini-2.5-flash-safe-tools",
  "provider": "gemini",
  "model": "gemini-2.5-flash",
  "api_keys": ["your-gemini-key"],
  "tool_schema_transform": "simple"
}
```

Notes:

- Default behavior is disabled. If you omit `tool_schema_transform`, PicoClaw sends the original tool schema.
- The setting is per model entry, so you can enable it only for the providers that need it.

#### Account / Alias Resolution

PicoClaw resolves execution using these rules:

1. Resolve `account_ref`, including any account-router choice, to one concrete
   account.
2. Resolve the exact alias from `model_aliases[]`.
3. Apply that alias's override for the selected concrete account, or its base
   model when no override exists.
4. Send the concrete model through the account's provider transport.

Raw model IDs, provider metadata, and fetched model catalogs never substitute
for a missing alias.

#### Voice Transcription

Configure audio transcription with `voice.account_ref` plus an exact alias in
`voice.model_name`. Missing fields disable ASR; PicoClaw does not scan for a
Groq account or infer a model.

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "gemini-voice",
      "provider": "gemini",
      "model": "",
      "api_keys": ["your-gemini-key"]
    }
  ],
  "model_aliases": [
    {
      "name": "voice",
      "model": "gemini-2.5-flash"
    }
  ],
  "voice": {
    "account_ref": "gemini-voice",
    "model_name": "voice",
    "echo_transcription": false
  }
}
```

#### Voice Synthesis

Configure text-to-speech with `voice.tts_account_ref` plus an exact alias in
`voice.tts_model_name`.
When the provider needs model-specific TTS request fields, put them in
`model_list[].extra_body`.

Example with OpenRouter `microsoft/mai-voice-2`:

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openrouter-voice",
      "provider": "openrouter",
      "model": "",
      "api_base": "https://openrouter.ai/api/v1",
      "extra_body": {
        "voice": "en-US-Harper:MAI-Voice-2",
        "response_format": "mp3"
      },
      "api_keys": ["sk-or-your-openrouter-key"]
    }
  ],
  "model_aliases": [
    {
      "name": "voice",
      "model": "microsoft/mai-voice-2"
    }
  ],
  "voice": {
    "tts_account_ref": "openrouter-voice",
    "tts_model_name": "voice"
  }
}
```

Notes that matter:

- PicoClaw still uses the OpenAI-compatible `/audio/speech` route for this setup.
- The default TTS request uses `voice: alloy` and `response_format: opus`.
- Override those defaults with `extra_body` when your selected TTS model requires different values.

#### Vendor-Specific Examples

**OpenAI**

```json
{
  "model_name": "gpt-5.4",
  "provider": "openai",
  "model": "gpt-5.4",
  "api_keys": ["sk-..."]
}
```

**NEAR AI Cloud**

```json
{
  "model_name": "nearai-glm",
  "provider": "nearai",
  "model": "zai-org/GLM-5.1-FP8",
  "api_keys": ["your-nearai-api-key"]
}
```

**VolcEngine (Doubao)**

```json
{
  "model_name": "ark-code-latest",
  "provider": "volcengine",
  "model": "ark-code-latest",
  "api_keys": ["sk-..."]
}
```

**智谱 AI (GLM)**

```json
{
  "model_name": "glm-4.7",
  "provider": "zhipu",
  "model": "glm-4.7",
  "api_keys": ["your-key"]
}
```

**Z.AI Coding Plan (GLM)**
> Z.AI and 智谱 AI are two brands of the same provider. For the Z.AI Coding Plan use the `openai` model key and the api base as follows, rather than the zhipu config
```json
{
  "model_name": "glm-4.7",
  "provider": "openai",
  "model": "glm-4.7",
  "api_keys": ["your-z.ai-key"],
  "api_base": "https://api.z.ai/api/coding/paas/v4"
}
```

**DeepSeek**

```json
{
  "model_name": "deepseek-chat",
  "provider": "deepseek",
  "model": "deepseek-chat",
  "api_keys": ["sk-..."]
}
```

**Anthropic (with API key)**

```json
{
  "model_name": "claude-sonnet-4.6",
  "provider": "anthropic",
  "model": "claude-sonnet-4.6",
  "api_keys": ["sk-ant-your-key"]
}
```

> Run `picoclaw auth login --provider anthropic` to paste your API token.

**Anthropic Messages API (native format)**

For direct Anthropic API access or custom endpoints that only support Anthropic's native message format:

```json
{
  "model_name": "claude-opus-4-6",
  "provider": "anthropic-messages",
  "model": "claude-opus-4-6",
  "api_keys": ["sk-ant-your-key"],
  "api_base": "https://api.anthropic.com"
}
```

> Use `anthropic-messages` protocol when:
> - Using third-party proxies that only support Anthropic's native `/v1/messages` endpoint (not OpenAI-compatible `/v1/chat/completions`)
> - Connecting to services like MiniMax, Synthetic that require Anthropic's native message format
> - The existing `anthropic` protocol returns 404 errors (indicating the endpoint doesn't support OpenAI-compatible format)
>
> **Note:** The `anthropic` protocol uses OpenAI-compatible format (`/v1/chat/completions`), while `anthropic-messages` uses Anthropic's native format (`/v1/messages`). Choose based on your endpoint's supported format.

**Ollama (local)**

```json
{
  "model_name": "llama3",
  "provider": "ollama",
  "model": "llama3"
}
```

**LM Studio (local)**

```json
{
  "model_name": "lmstudio-local",
  "provider": "lmstudio",
  "model": "openai/gpt-oss-20b"
}
```

`api_base` defaults to `http://localhost:1234/v1`. API key is optional unless your LM Studio server enables authentication.<br/>
With explicit `provider`, PicoClaw sends `openai/gpt-oss-20b` unchanged to the LM Studio server. The legacy compatibility form `"model": "lmstudio/openai/gpt-oss-20b"` still resolves to the same upstream model ID when `provider` is omitted.

**Custom Proxy/API**

```json
{
  "model_name": "my-custom-model",
  "provider": "openai",
  "model": "custom-model",
  "api_base": "https://my-proxy.com/v1",
  "api_keys": ["sk-..."],
  "user_agent": "MyApp/1.0",
  "request_timeout": 300
}
```

**LiteLLM Proxy**

```json
{
  "model_name": "lite-gpt4",
  "provider": "litellm",
  "model": "lite-gpt4",
  "api_base": "http://localhost:4000/v1",
  "api_keys": ["sk-..."]
}
```

With explicit `provider`, PicoClaw sends `model` unchanged. That means `"provider": "litellm", "model": "lite-gpt4"` sends `lite-gpt4`, while `"provider": "litellm", "model": "openai/gpt-4o"` sends `openai/gpt-4o`. The legacy compatibility forms `litellm/lite-gpt4` and `litellm/openai/gpt-4o` still resolve the same way when `provider` is omitted.

**Z.AI Coding Plan**

If the standard Zhipu endpoint (`https://open.bigmodel.cn/api/paas/v4`) returns 429 (code 1113: insufficient balance), try using the Z.AI Coding Plan endpoint instead:

```json
{
  "model_name": "glm-4.7",
  "provider": "openai",
  "model": "glm-4.7",
  "api_keys": ["your-zhipu-api-key"],
  "api_base": "https://api.z.ai/api/coding/paas/v4"
}
```

**Note:** The Z.AI Coding Plan endpoint and standard Zhipu endpoint use the same API key format but have separate billing. If you encounter 429 errors with the standard Zhipu endpoint, the Z.AI Coding Plan endpoint may have available balance.

#### Load Balancing

Configure multiple endpoints for the same account identity; PicoClaw can
round-robin between them while the selected alias independently supplies the
concrete model:

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openai-pool",
      "provider": "openai",
      "model": "",
      "api_base": "https://api1.example.com/v1",
      "api_keys": ["sk-key1"]
    },
    {
      "model_name": "openai-pool",
      "provider": "openai",
      "model": "",
      "api_base": "https://api2.example.com/v1",
      "api_keys": ["sk-key2"]
    }
  ],
  "model_aliases": [
    {
      "name": "chat",
      "model": "gpt-5.4"
    }
  ]
}
```

#### Automatic Model Failover (Cascade)

PicoClaw supports automatic failover when `primary` plus `fallbacks` contain
exact aliases in the agent model settings.
The runtime fallback chain retries the next candidate for retriable failures such as HTTP `429`, quota/rate-limit errors, and timeout errors.
It also applies cooldown tracking per candidate to avoid immediately retrying a recently failed target.

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openrouter-main",
      "provider": "openrouter",
      "model": "",
      "api_keys": ["sk-or-main"]
    }
  ],
  "model_aliases": [
    {
      "name": "qwen",
      "model": "qwen/qwen3.5"
    },
    {
      "name": "deepseek",
      "model": "deepseek/deepseek-chat"
    },
    {
      "name": "gemini",
      "model": "google/gemini-2.5-flash"
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "openrouter-main",
      "model_name": "qwen",
      "model_fallbacks": ["deepseek", "gemini"]
    }
  }
}
```

Key rotation and account routing choose transport credentials. Alias fallbacks
choose model policy; they do not replace `account_ref`.

#### Migration from Legacy `providers` Config

The old `providers` configuration is **deprecated** and has been removed in V2. Existing V0/V1 configs are auto-migrated.

**Old Config (deprecated):**

```json
{
  "providers": {
    "zhipu": {
      "api_key": "your-key",
      "api_base": "https://open.bigmodel.cn/api/paas/v4"
    }
  },
  "agents": {
    "defaults": {
      "provider": "zhipu",
      "model": "glm-4.7"
    }
  }
}
```

**New Config (recommended):**

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "zhipu-work",
      "provider": "zhipu",
      "model": "",
      "api_keys": ["your-key"]
    }
  ],
  "model_aliases": [
    {
      "name": "chat",
      "model": "glm-4.7"
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "zhipu-work",
      "model_name": "chat"
    }
  }
}
```

For detailed migration guide, see [migration/model-list-migration.md](../migration/model-list-migration.md).

### Provider Architecture

PicoClaw routes providers by protocol family:

- OpenAI-compatible protocol: OpenRouter, OpenAI-compatible gateways, Groq, Zhipu, and vLLM-style endpoints.
- Gemini native protocol: Google Gemini via the native `models/*:generateContent` and `models/*:streamGenerateContent` endpoints.
- Anthropic protocol: Claude-native API behavior.
- Codex/OAuth path: OpenAI OAuth/token authentication route.

This keeps the runtime lightweight while making new OpenAI-compatible backends mostly a config operation (`api_base` + `api_keys`).

<details>
<summary><b>Zhipu</b></summary>

**1. Get API key and base URL**

* Get [API key](https://bigmodel.cn/usercenter/proj-mgmt/apikeys)

**2. Configure**

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "zhipu-work",
      "provider": "zhipu",
      "model": "",
      "api_keys": ["Your API Key"],
      "api_base": "https://open.bigmodel.cn/api/paas/v4"
    }
  ],
  "model_aliases": [
    {
      "name": "chat",
      "model": "glm-4.7"
    }
  ],
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/workspace",
      "account_ref": "zhipu-work",
      "model_name": "chat",
      "max_tokens": 8192,
      "temperature": 0.7,
      "max_tool_iterations": 20
    }
  }
}
```

**3. Run**

```bash
picoclaw agent -m "Hello"
```

</details>

<details>
<summary><b>Full config example</b></summary>

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openrouter-main",
      "provider": "openrouter",
      "model": "",
      "api_keys": ["sk-or-v1-xxx"]
    },
    {
      "model_name": "groq-voice",
      "provider": "groq",
      "model": "",
      "api_keys": ["gsk_xxx"]
    }
  ],
  "model_aliases": [
    {
      "name": "chat",
      "model": "anthropic/claude-opus-4.5"
    },
    {
      "name": "asr",
      "model": "whisper-large-v3-turbo"
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "openrouter-main",
      "model_name": "chat"
    }
  },
  "session": {
    "dm_scope": "per-channel-peer"
  },
  "voice": {
    "account_ref": "groq-voice",
    "model_name": "asr",
    "echo_transcription": false
  },
  "channel_list": {
    "telegram": {
      "enabled": true,
      "type": "telegram",
      "token": "123456:ABC...",
      "allow_from": ["123456789"]
    },
    "discord": {
      "enabled": true,
      "type": "discord",
      "token": "",
      "allow_from": [""]
    },
    "whatsapp": {
      "enabled": false,
      "type": "whatsapp",
      "bridge_url": "ws://localhost:3001",
      "use_native": false,
      "session_store_path": "",
      "allow_from": []
    },
    "feishu": {
      "enabled": false,
      "type": "feishu",
      "app_id": "cli_xxx",
      "app_secret": "xxx",
      "encrypt_key": "",
      "verification_token": "",
      "allow_from": []
    },
    "qq": {
      "enabled": false,
      "type": "qq",
      "app_id": "",
      "app_secret": "",
      "allow_from": []
    }
  },
  "tools": {
    "web": {
      "brave": {
        "enabled": false,
        "api_key": "BSA...",
        "max_results": 5
      },
      "duckduckgo": {
        "enabled": true,
        "max_results": 5
      },
      "perplexity": {
        "enabled": false,
        "api_key": "",
        "max_results": 5
      },
      "searxng": {
        "enabled": false,
        "base_url": "http://localhost:8888",
        "max_results": 5
      }
    },
    "cron": {
      "exec_timeout_minutes": 5,
      "allow_command": true,
      "command_allowed_remotes": []
    }
  },
  "heartbeat": {
    "enabled": true,
    "interval": 30
  }
}
```

</details>

---

## 📝 API Key Comparison

| Service          | Pricing                  | Use Case                              |
| ---------------- | ------------------------ | ------------------------------------- |
| **OpenRouter**   | Free: 200K tokens/month  | Multiple models (Claude, GPT-4, etc.) |
| **Volcengine CodingPlan** | ¥9.9/first month | Best for Chinese users, multiple SOTA models (Doubao, DeepSeek, etc.) |
| **Zhipu**        | Free: 200K tokens/month  | Suitable for Chinese users                |
| **Brave Search** | $5/1000 queries          | Web search functionality              |
| **SearXNG**      | Free (self-hosted)       | Privacy-focused metasearch (70+ engines) |
| **Groq**         | Free tier available      | Fast inference (Llama, Mixtral)       |
| **Cerebras**     | Free tier available      | Fast inference (Llama, Qwen, etc.)    |
| **LongCat**      | Free: up to 5M tokens/day | Fast inference                       |
| **ModelScope**   | Free: 2000 requests/day  | Inference (Qwen, GLM, DeepSeek, etc.) |

---

<div align="center">
  <img src="../../assets/logo.jpg" alt="PicoClaw Meme" width="512">
</div>
