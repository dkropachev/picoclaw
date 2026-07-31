# Troubleshooting

## `no model configured`, unknown alias, or an invalid upstream model

**Symptom:** You see either:

- `no model configured`
- `model alias "..." is not configured`
- OpenRouter returns 400: `"free is not a valid model ID"`

**Cause:** Config version 4 resolves account and model independently:

1. `account_ref` selects a concrete account or an account router.
2. An exact model alias supplies the concrete upstream model, including any
   override for the selected concrete account.

PicoClaw never promotes `model_list[].model`, a fetched model ID, or provider
metadata into the missing alias. That prevents an unrelated model such as
`gpt-5.3` from appearing through a hidden default.

For OpenRouter, set `provider` on the account and put the OpenRouter model ID on
the alias:

**Fix:** In `~/.picoclaw/config.json` (or your config path):

1. `agents.defaults.account_ref` must select an enabled account or account
   router.
2. `agents.defaults.model_name` must exactly match `model_aliases[].name`.
3. The alias `model` must be a valid OpenRouter model ID, for example:
   - `"free"` – auto free-tier
   - `"google/gemini-2.0-flash-exp:free"`
   - `"meta-llama/llama-3.1-8b-instruct:free"`

Example snippet:

```json
{
  "version": 4,
  "agents": {
    "defaults": {
      "account_ref": "openrouter-main",
      "model_name": "free-chat"
    }
  },
  "model_aliases": [
    {
      "name": "free-chat",
      "model": "google/gemini-2.0-flash-exp:free"
    }
  ],
  "model_list": [
    {
      "model_name": "openrouter-main",
      "provider": "openrouter",
      "model": "",
      "api_keys": ["sk-or-v1-YOUR_OPENROUTER_KEY"],
      "api_base": "https://openrouter.ai/api/v1"
    }
  ]
}
```

Get your key at [OpenRouter Keys](https://openrouter.ai/keys).
