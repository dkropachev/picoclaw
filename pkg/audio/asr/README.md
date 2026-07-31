# ASR (Automatic Speech Recognition)

This package handles speech-to-text for PicoClaw voice input.

If you are new to ASR setup, the simplest mental model is:

1. Configure a concrete ASR account in `model_list`.
2. Add an exact ASR alias in `model_aliases`.
3. Set `voice.account_ref` and alias-valued `voice.model_name`.
4. Put the account API key in `.security.yml`.

## Quick Recommendation

For most new users, start with one of these:

| Provider | Example model | Why start here |
| --- | --- | --- |
| [Groq](https://console.groq.com/keys) | `groq/whisper-large-v3-turbo` | Fast Whisper-style transcription and a straightforward OpenAI-compatible API. Groq currently advertises a free tier plan for 2000 reqs/day. |
| [ElevenLabs](https://elevenlabs.io/pricing) | `elevenlabs/scribe_v1` | Easy setup and strong speech-to-text quality. ElevenLabs currently advertises a free plan that includes speech-to-text usage. |

Pricing and free-plan limits can change, so check the linked pricing pages before depending on them in production.

## How ASR Configuration Works

PicoClaw does not keep ASR API keys inside the `voice` section.

Instead:

- `voice.account_ref` chooses a concrete `model_list` account.
- `voice.model_name` chooses an exact `model_aliases[].name`.
- The alias supplies the concrete upstream model, including a per-account
  override when configured.
- `.security.yml` stores the API key for the account entry.

This is the recommended pattern because it is explicit, reusable, and consistent with the rest of PicoClaw's model configuration.

## Recommended Setup

### Option A: Groq Whisper

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

Notes:

- You can omit `api_base` and PicoClaw will use Groq's default API base automatically.
- If you set `api_base` manually for Groq Whisper, both of these forms work:
  - `https://api.groq.com/openai/v1`
  - `https://api.groq.com/openai/v1/audio/transcriptions`
- Any OpenAI-compatible Whisper model name containing `whisper` can use the Whisper transcription path, not only `whisper-large-v3-turbo`.

### Option B: ElevenLabs

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

### Option C: OpenAI Whisper

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

## Other ASR-Capable Model Types

PicoClaw currently supports three main ASR routes:

| Route | Example models | Behavior |
| --- | --- | --- |
| ElevenLabs ASR | `provider: elevenlabs`, `model: scribe_v1` | Uses the ElevenLabs transcription API. |
| Whisper endpoint models | `openai/whisper-1`, `groq/whisper-large-v3` | Uses an OpenAI-compatible `/audio/transcriptions` endpoint. |
| Audio-capable chat models **(Under construction)** | `openai/gpt-4o-audio-preview`, `gemini/gemini-2.5-flash` | Sends audio to a multimodal chat model and asks it to transcribe. |

If you are unsure which one to pick, choose Groq Whisper or ElevenLabs first.

## How PicoClaw Chooses a Transcriber

`DetectTranscriber` resolves ASR in this order:

1. Resolve `voice.account_ref` to one concrete account.
2. Resolve the exact `voice.model_name` alias for that account.
3. If that resolved model is:
   - an `elevenlabs` provider model, PicoClaw uses the ElevenLabs transcriber.
   - an OpenAI-compatible Whisper model, PicoClaw uses the Whisper transcriber.
   - an audio-capable chat model, PicoClaw uses `AudioModelTranscriber`.

There is no compatibility scan or provider-default model. If either selection
is missing or invalid, ASR remains unavailable.

## Common Mistakes

- Defining an ASR account but forgetting either `voice.account_ref` or the alias.
- Putting the API key in `voice` instead of `.security.yml`.
- Using a non-ASR model and expecting Whisper-style transcription behavior.
- Setting a custom `api_base` that points to the wrong provider endpoint.

## Minimal Checklist

Before testing voice input, make sure:

- `voice.account_ref` names an enabled concrete account.
- `voice.model_name` exactly matches `model_aliases[].name`.
- The account's `.security.yml` entry contains a valid API key.
- The selected model is actually ASR-capable.
- Voice input is enabled for the channel you are using.
