# Migration Guide: Provider Accounts and Model Aliases

This guide covers the legacy `providers` to `model_list` migration and the
version 4 separation of provider accounts from model aliases.

> [!IMPORTANT]
> In version 4, `model_list[].model_name` is an account/transport identity.
> Executions select that identity through `account_ref` and independently select
> an exact entry from `model_aliases[]`. It is no longer valid to rely on a
> provider default or treat a raw model ID as the selected alias.

## Why Migrate?

The new `model_list` configuration offers several advantages:

- **Zero-code provider addition**: Add OpenAI-compatible providers with configuration only
- **Load balancing**: Configure multiple endpoints for the same model
- **Explicit provider resolution**: Prefer `provider` + native `model`, with legacy `provider/model` compatibility when needed
- **Cleaner configuration**: Model-centric instead of vendor-centric

## Timeline

| Version | Status |
|---------|--------|
| v1.x | `model_list` introduced, `providers` deprecated but functional |
| v1.x+1 | Prominent deprecation warnings, migration tool available |
| v2.0 | `providers` configuration removed |
| v3 | Model and account identity still shared legacy fields |
| v4 | `model_aliases[]` and `account_ref` become independent and strict |

## Before and After

### Before: Legacy `providers` Configuration

```json
{
  "providers": {
    "openai": {
      "api_key": "sk-your-openai-key",
      "api_base": "https://api.openai.com/v1"
    },
    "anthropic": {
      "api_key": "sk-ant-your-key"
    },
    "deepseek": {
      "api_key": "sk-your-deepseek-key"
    }
  },
  "agents": {
    "defaults": {
      "provider": "openai",
      "model": "gpt-5.4"
    }
  }
}
```

### After: Version 4 Account and Alias Configuration

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openai-main",
      "provider": "openai",
      "model": "",
      "api_keys": ["sk-your-openai-key"],
      "api_base": "https://api.openai.com/v1"
    },
    {
      "model_name": "anthropic-main",
      "provider": "anthropic",
      "model": "",
      "api_keys": ["sk-ant-your-key"]
    },
    {
      "model_name": "deepseek-main",
      "provider": "deepseek",
      "model": "",
      "api_keys": ["sk-your-deepseek-key"]
    }
  ],
  "model_aliases": [
    {
      "name": "default-chat",
      "model": "gpt-5.4",
      "account_overrides": {
        "anthropic-main": "claude-sonnet-4.6",
        "deepseek-main": "deepseek-chat"
      }
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "openai-main",
      "model_name": "default-chat"
    }
  }
}
```

For new account rows, set `enabled` explicitly when you want the account to
participate in execution. Enabling an account never selects a model alias.

## Account / Alias Resolution

`model_list[]` describes the provider transport and account identity:

```json
{
  "model_name": "openai-main",
  "provider": "openai",
  "api_base": "https://api.openai.com/v1"
}
```

`model_aliases[]` independently describes the concrete model policy:

```json
{
  "name": "default-chat",
  "model": "gpt-5.4",
  "account_overrides": {
    "anthropic-main": "claude-sonnet-4.6"
  }
}
```

Resolution rules:

1. Resolve `account_ref` to an enabled concrete account. If it is an account
   router, select its concrete account first.
2. Resolve the exact model alias.
3. Use the alias override for that concrete account when present; otherwise use
   the alias base `model`.
4. Send that concrete model through the selected account's provider transport.

Alias lookup is exact and case-sensitive. Account names, raw model IDs, and
provider/model strings are not treated as aliases unless that exact string was
explicitly configured as an alias name.

## ModelConfig Fields

| Field | Required | Description |
|-------|----------|-------------|
| `model_name` | Yes | Concrete account/transport identity referenced by `account_ref` |
| `provider` | No | Provider identifier used by this account |
| `model` | No | Legacy concrete model metadata; it does not select the execution model in v4 |
| `api_base` | No | API endpoint URL |
| `api_keys` | No | API authentication keys (array; supports multiple keys for load balancing) |
| `enabled` | No | Whether this account entry can be selected |
| `proxy` | No | HTTP proxy URL |
| `auth_method` | No | Authentication method: `oauth`, `token` |
| `connect_mode` | No | Connection mode for CLI providers: `stdio`, `grpc` |
| `rpm` | No | Requests per minute limit |
| `max_tokens_field` | No | Field name for max tokens |
| `request_timeout` | No | HTTP request timeout in seconds; `<=0` uses default `120s` |

> **Note**: `api_key` (singular) has been **removed** in V2 configs. Only `api_keys` (array) is supported. During migration from V0/V1, both `api_key` and `api_keys` are automatically merged into the new `api_keys` array.

`model_aliases[]` fields are:

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Exact stable alias used by agents, chat, workflows, voice, and model routers |
| `model` | Yes | Base concrete upstream model ID |
| `account_overrides` | No | Map from concrete account refs to concrete model IDs; account-router and model-router keys are invalid |

## Load Balancing

There are two ways to configure load balancing:

### Option 1: Multiple API Keys in `api_keys` (Recommended)

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openai-pool",
      "provider": "openai",
      "model": "",
      "api_keys": ["sk-key1", "sk-key2", "sk-key3"],
      "api_base": "https://api.openai.com/v1"
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

Or via `.security.yml`:

```yaml
model_list:
  openai-pool:
    api_keys:
      - "sk-key1"
      - "sk-key2"
      - "sk-key3"
```

### Option 2: Multiple Model Entries

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openai-pool",
      "provider": "openai",
      "model": "",
      "api_keys": ["sk-key1"],
      "api_base": "https://api1.example.com/v1"
    },
    {
      "model_name": "openai-pool",
      "provider": "openai",
      "model": "",
      "api_keys": ["sk-key2"],
      "api_base": "https://api2.example.com/v1"
    },
    {
      "model_name": "openai-pool",
      "provider": "openai",
      "model": "",
      "api_keys": ["sk-key3"],
      "api_base": "https://api3.example.com/v1"
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

When `account_ref` selects this repeated account identity, requests can be
distributed across the configured endpoints. The independently selected alias
still determines the concrete model.

## Adding a New OpenAI-Compatible Provider

With `model_list`, adding a new provider requires zero code changes:

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "custom-account",
      "provider": "openai",
      "model": "",
      "api_keys": ["your-api-key"],
      "api_base": "https://api.your-provider.com/v1"
    }
  ],
  "model_aliases": [
    {
      "name": "custom-chat",
      "model": "my-model-v1"
    }
  ]
}
```

Set `provider` and the API base on the concrete account, then create an alias
for each concrete model policy you intend to execute.

## Backward Compatibility

During the migration period, your existing V0/V1 config will be auto-migrated to V2:

1. If `model_list` is empty and `providers` has data, the system auto-converts internally
2. Both `api_key` (singular) and `api_keys` (array) in V0/V1 configs are merged into the new `api_keys` array
3. A deprecation warning is logged: `"providers config is deprecated, please migrate to model_list"`
4. All existing functionality remains unchanged

### Version 3 to Version 4

On first load, PicoClaw backs up a version 3 config and creates aliases only for
unambiguous concrete selections:

- Equal duplicate `model_list` rows may seed one alias.
- Rows with the same name but different concrete models do not seed an alias.
- A legacy raw model ref is preserved only when exactly one concrete
  account/model pair matches it.
- A concrete selection moves its account identity to `account_ref` and keeps
  the generated alias.
- Credential-only, account-router, unknown, or ambiguous selectors move to
  `account_ref` but have their alias cleared.
- Invalid legacy fallbacks, image/light/subagent refs, voice refs, and
  model-router terminals are filtered rather than replaced with provider
  defaults.

The migration never guesses a provider model. Review cleared selections and
create an alias before restarting execution.

## Migration Checklist

- [ ] Identify all providers you're currently using
- [ ] Create enabled `model_list` or credential accounts for each provider
- [ ] Prefer explicit `provider` values and native model IDs
- [ ] Create exact `model_aliases[]` entries for every model policy
- [ ] Set `agents.defaults.account_ref` to a concrete account or account router
- [ ] Set `agents.defaults.model_name` to an exact alias or model router
- [ ] Ensure alias `account_overrides` keys name concrete accounts, never routers
- [ ] Update chat, agents, workflows, voice, fallbacks, and model-router
      terminals to reference aliases
- [ ] Test that all models work correctly
- [ ] Remove or comment out the old `providers` section

## Troubleshooting

### No model configured

```
no model configured
```

**Solution**: Configure an exact `model_aliases[].name`, then set
`agents.defaults.model_name` to that alias. Also configure
`agents.defaults.account_ref`; an account alone never implies a model.

### Alias not configured

```text
model alias "xxx" is not configured
```

**Solution**: Use the exact case-sensitive alias name. Do not provide a
`model_list` account name, raw upstream ID, or provider/model string unless that
exact string was intentionally created as an alias.

### Unknown protocol error

```
unknown provider "xxx" in model "xxx/model-name"
```

**Solution**: Use a supported `provider` value, or use the legacy `provider/model` compatibility form correctly. See [Provider / Model Resolution](#provider--model-resolution).

### Missing API key error

```
api_key or api_base is required for HTTP-based protocol "xxx"
```

**Solution**: Provide `api_keys` and/or `api_base` for HTTP-based providers.

## Need Help?

- [GitHub Issues](https://github.com/sipeed/picoclaw/issues)
- [Discussion #122](https://github.com/sipeed/picoclaw/discussions/122): Original proposal
