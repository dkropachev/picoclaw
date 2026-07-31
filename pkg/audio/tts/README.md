# TTS (Text-to-Speech)

This package handles speech synthesis for PicoClaw.

If you are new to TTS setup, the simplest workflow is:

1. Configure a concrete TTS account in `model_list`.
2. Add an exact TTS alias in `model_aliases`.
3. Set `voice.tts_account_ref` and alias-valued `voice.tts_model_name`.
4. Put the account API key in `.security.yml`.

## Quick Recommendation

For most users, these are the best starting points:

| Provider | Why start here |
| --- | --- |
| [OpenAI](https://platform.openai.com/docs/guides/text-to-speech) | Best-supported path in PicoClaw today. The current TTS implementation is built around the OpenAI-compatible `/audio/speech` API shape, and OpenAI is the safest default. |
| [Xiaomi MiMo](https://platform.xiaomimimo.com) | A good second option if you want an OpenAI-compatible provider endpoint and are already using MiMo models in the rest of your stack. |

## How TTS Configuration Works

PicoClaw does not keep TTS API keys inside `voice`.

Instead:

- `voice.tts_account_ref` selects a concrete `model_list` account.
- `voice.tts_model_name` selects an exact `model_aliases[].name`.
- The alias provides the concrete model ID, including a per-account override.
- The account provides the provider, API base, proxy, and credentials.
- For providers that need model-specific TTS parameters, use `model_list[].extra_body`
  to pass fields such as `voice` and `response_format`.
- `.security.yml` stores the API key for the same named account entry.

This is the recommended and supported configuration pattern.

## Recommended Setup

### Option A: OpenAI

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

### Option B: Xiaomi MiMo

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

If you use a custom MiMo endpoint, set `api_base` explicitly. Otherwise
PicoClaw uses MiMo's registered API base; this does not select a model.

### Option C: OpenRouter MAI Voice 2

Some OpenAI-compatible TTS routes require provider-specific request fields.
OpenRouter's `microsoft/mai-voice-2` is one example: it needs a model-specific
voice name and works best with `response_format: "mp3"`.

`config.json`

```json
{
  "voice": {
    "tts_account_ref": "openrouter-voice",
    "tts_model_name": "tts"
  },
  "model_aliases": [
    {
      "name": "tts",
      "model": "microsoft/mai-voice-2"
    }
  ],
  "model_list": [
    {
      "model_name": "openrouter-voice",
      "provider": "openrouter",
      "model": "",
      "api_base": "https://openrouter.ai/api/v1",
      "extra_body": {
        "voice": "en-US-Harper:MAI-Voice-2",
        "response_format": "mp3"
      }
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  openrouter-voice:
    api_keys:
      - "sk-or-your-openrouter-key"
```

## What PicoClaw Sends Today

The current TTS runtime uses an OpenAI-compatible speech request with these defaults:

- Endpoint: `/audio/speech`
- Response format: `opus`
- Voice: `alloy`
- Model: produced by exact alias resolution for the selected account

These defaults can now be overridden per model through `model_list[].extra_body`.

That means:

- `openai/tts-1` works naturally.
- Registered OpenAI-compatible provider families, including `litellm` proxies,
  can work if the upstream accepts the same request format.
- Unknown provider identifiers fail closed; use a registered provider family
  and set its `api_base` for a custom endpoint.
- Provider-specific TTS models may need their own `voice` and `response_format` values.
- If a provider rejects `response_format`, PicoClaw retries once without that field.

## How PicoClaw Chooses a TTS Provider

`DetectTTS` resolves TTS in this order:

1. Resolve `voice.tts_account_ref` to one concrete account.
2. Resolve the exact `voice.tts_model_name` alias for that account.
3. If the resolved account has credentials, create the TTS provider with the
   resolved concrete model and account transport settings.

There is no model-list scan or provider-default model. Missing/invalid
selections leave TTS unavailable, and `send_tts` reports
`no model configured`.

## Notes About API Base Handling

PicoClaw normalizes the configured base URL for TTS:

- For OpenAI, a base like `https://api.openai.com` or `https://api.openai.com/v1` becomes `https://api.openai.com/v1/audio/speech`.
- For other OpenAI-compatible providers, PicoClaw preserves the configured base path and ensures it ends with `/audio/speech`.
- If `api_base` is omitted, PicoClaw uses the provider's registered API base
  when known; model selection still comes only from the alias.

## Common Mistakes

- Setting `voice.tts_model_name` to a name that does not exist in
  `model_aliases`.
- Omitting `voice.tts_account_ref`.
- Adding a TTS model but forgetting to put its API key in `.security.yml`.
- Assuming PicoClaw will automatically infer provider-specific custom voices.
- Forgetting to set `model_list[].extra_body.voice` or `model_list[].extra_body.response_format` for TTS models that require them.
- Using a provider endpoint that is not compatible with the OpenAI `/audio/speech` request format.

## Minimal Checklist

Before testing `send_tts`, make sure:

- `voice.tts_account_ref` names an enabled concrete account.
- `voice.tts_model_name` exactly matches `model_aliases[].name`.
- The account's `.security.yml` entry contains a valid API key.
- The chosen provider supports an OpenAI-compatible speech synthesis endpoint.
- Your selected model is actually a TTS-capable model.
