# GitHub Copilot Subscription Accounts

## Feature ID

`FR-GITHUB-COPILOT`

## Behavior Summary

PicoClaw can use a user's GitHub Copilot subscription as a credential-backed
model account. A user registers a named `github-copilot` account in the
launcher Accounts surface, stores a supported GitHub user token in the shared
auth store, and selects a `github-copilot` model entry anywhere a normal chat
model can be selected.

This feature is distinct from the existing local GitHub Copilot CLI bridge that
talks to a user-managed `localhost:4321` Copilot service. Subscription-backed
accounts must use explicit stored credentials and direct HTTPS requests to the
GitHub/Copilot API. They must not start, locate, install, or shell out to a
`copilot` binary.

## Reconstruction Notes

- Similarity target: recreate the Codex subscription-account pattern for
  GitHub Copilot: a provider-specific account type in auth/UI, a credential
  resolver in the provider factory, a subscription-backed API client, and
  launcher model/status plumbing.
- Core types/functions: `auth.AuthCredential`, `auth.NormalizeCredentialID`,
  OAuth provider registration in `web/backend/api/oauth.go`,
  `providers.ModelProviderOption`, `providers.CreateProviderFromConfig`,
  `GitHubCopilotProvider`, `config.AccountRouterCredentialAccountProvider`,
  `/api/accounts/models/fetch`, and the frontend `OAuthProvider` account flow.
- Runtime ordering: normalize model/provider config, resolve
  `github-copilot[:name]` credentials, validate token form, exchange or apply
  the GitHub token for direct Copilot API bearer auth, call Copilot HTTPS
  endpoints with the requested model, and return a normal `LLMResponse`.
- Non-obvious constraints: OpenAI Codex account auth and GitHub Copilot account
  auth are separate products; GitHub's public REST Copilot APIs cover
  management/metrics rather than chat completions; the Go Copilot SDK client
  starts a CLI-backed server for session/model RPCs, so credential-backed
  PicoClaw accounts must not use that path. Model discovery must call the
  direct Copilot `/models` API instead of falling back to the static provider
  metadata list. GitHub Copilot quota payloads are not guaranteed to match
  OpenAI Codex usage shapes, so account-limit display must parse known quota
  maps defensively and degrade to an available/unavailable status when detailed
  counters are absent.

Reference findings checked on 2026-07-24:

- OpenAI Codex upstream adds bearer auth and `ChatGPT-Account-ID` headers for
  subscription-backed requests in
  [bearer_auth_provider.rs](https://github.com/openai/codex/blob/main/codex-rs/model-provider/src/bearer_auth_provider.rs).
- GitHub documents Copilot SDK auth as subscription-backed for GitHub signed-in
  users, OAuth GitHub Apps, and environment tokens, while BYOK does not require
  Copilot subscription:
  [GitHub Copilot SDK authentication](https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate).
- GitHub Copilot CLI auth supports `gho_`, `ghu_`, and `github_pat_` token
  forms and does not support classic `ghp_` PATs:
  [Authenticating GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/authenticate-copilot-cli).
- The Go SDK starts a local CLI-backed server for client/session RPCs; that is
  acceptable only for the legacy local bridge mode, not for subscription-backed
  credentials:
  [Copilot SDK getting started](https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md).
- GitHub/Copilot direct clients exchange GitHub user tokens through
  `https://api.github.com/copilot_internal/v2/token` when available, honor the
  returned `endpoints.api` value, then call `GET /models` with Copilot bearer
  auth and headers such as `Copilot-Integration-Id: vscode-chat`. If token
  exchange returns `404` for an account type, the client may fall back to the
  raw GitHub token against the default Copilot API base.

## Requirements

| ID                      | Level  | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Rationale                                                                                                                                  |
| ----------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `FR-GITHUB-COPILOT-001` | MUST   | Provider metadata exposes `github-copilot` as both a selectable model provider and a credential-backed account provider, with display name `GitHub Copilot`, alias `copilot`, default model `auto`, and a token-based default auth method.                                                                                                                                                                                                                                                                                                                                         | Users need to discover Copilot alongside other subscription-backed accounts instead of configuring a local transport by hand.              |
| `FR-GITHUB-COPILOT-002` | MUST   | `/api/oauth/providers` lists `github-copilot` with token login support; login normalizes credential IDs as `github-copilot` or `github-copilot:<name>`, rejects invalid names, stores the token in `auth.AuthCredential`, and logout removes only the selected credential.                                                                                                                                                                                                                                                                                                         | Account routers and model entries need stable, provider-scoped credential references.                                                      |
| `FR-GITHUB-COPILOT-003` | MUST   | Token login accepts supported GitHub token families used by Copilot SDK/CLI (`gho_`, `ghu_`, `github_pat_`) and rejects classic `ghp_` PATs with an actionable error before persisting.                                                                                                                                                                                                                                                                                                                                                                                            | Unsupported tokens should fail at setup time rather than during a chat turn.                                                               |
| `FR-GITHUB-COPILOT-004` | MUST   | A `model_list` entry with provider `github-copilot` and credential auth (`auth_method` `token` or `oauth`) resolves the stored credential and uses direct HTTPS API calls with only that credential. It must not construct the Go Copilot SDK CLI client, start a CLI server, execute a `copilot` binary, or silently fall back to environment variables, keychain state, `gh auth`, or the local CLI bridge.                                                                                                                                                                      | Named accounts must run as the selected user, not whichever Copilot/GitHub identity happens to be logged in locally.                       |
| `FR-GITHUB-COPILOT-005` | MUST   | The existing local Copilot bridge remains available when `github-copilot` is configured without credential auth and with local connection settings; the subscription-backed client does not require `localhost:4321` or a pre-started external Copilot server.                                                                                                                                                                                                                                                                                                                     | Current local users must not lose compatibility while account-backed usage gets a cleaner setup path.                                      |
| `FR-GITHUB-COPILOT-006` | MUST   | Chat translation preserves the existing `LLMProvider` contract: messages become direct Copilot API request messages with role/content context, the requested model is passed through unless blank, blank model uses the provider default, Copilot responses become `LLMResponse.Content`, and close is idempotent without requiring SDK/client/session shutdown for credential-backed mode.                                                                                                                                                                                        | Agent turns, fallback, and account routers should treat Copilot like any other provider.                                                   |
| `FR-GITHUB-COPILOT-007` | MUST   | Model fetch/status logic does not call OpenAI-compatible `/models` for `github-copilot`; credential-backed fetch resolves the selected stored credential, exchanges it for Copilot API auth when applicable, and calls direct Copilot `GET /models`; if token exchange returns `403`, model fetch retries once against the fixed default Copilot API with the raw token and succeeds only when that API accepts it; account-specific API endpoints from successful exchanges are honored; static common models are exposed only as provider metadata/bootstrap hints; status treats credential-backed entries as configured only when the selected credential exists and only probes the local TCP bridge for non-credential configs. | Copilot subscriptions can expose more or different models than a static shortlist, and the UI needs the selected account's real model set. |
| `FR-GITHUB-COPILOT-008` | MUST   | The Accounts UI, onboarding sheet, provider icon/label handling, TypeScript OAuth types, token placeholder text, status cards, and i18n strings include `github-copilot` and display registered Copilot credentials exactly like other named accounts.                                                                                                                                                                                                                                                                                                                             | Browser setup should be first-class and consistent with OpenAI, Anthropic, and Google Code Assist accounts.                                |
| `FR-GITHUB-COPILOT-009` | MUST   | Errors from token exchange, direct model listing, direct chat calls, local-bridge SDK startup, session creation, entitlement/subscription denial, invalid tokens, unsupported token prefixes, timeouts, and canceled contexts are returned as concise provider errors with secrets redacted.                                                                                                                                                                                                                                                                                       | Users need actionable setup failures without leaking credentials in logs or launcher responses.                                            |
| `FR-GITHUB-COPILOT-010` | SHOULD | If a GitHub OAuth App client is later configured, the same account provider may add browser/device OAuth login, but token login remains supported and OAuth credentials still normalize to the same `github-copilot[:name]` keys.                                                                                                                                                                                                                                                                                                                                                  | The initial implementation can ship without app credentials while leaving a compatible path for richer login.                              |
| `FR-GITHUB-COPILOT-011` | MUST   | Account-router validation, API save, and execution accept Copilot credential account references in the exact form `credential:github-copilot` and `credential:github-copilot:<name>`, including names such as `credential:github-copilot:gh-copilot`, in both `account` blocks and `load_balance.accounts` for `blind`, `tokens_spent`, and `closest_limit`; saving a router with an existing stored Copilot credential must not report the reference as an unknown account, fallback/error accounting must keep that full credential identity, and account-router entries using Copilot accounts must render with the account routers rather than as Copilot provider account cards.                                | Named Copilot accounts are provider-scoped credential IDs with colons, so routers must not treat them like plain model names.              |
| `FR-GITHUB-COPILOT-012` | SHOULD | Account-limit APIs and UI include GitHub Copilot credentials when possible by calling GitHub/Copilot account usage endpoints with only the selected stored token, parsing known Copilot quota fields such as premium interactions, chat, completion, entitlement, remaining, reset, exhausted, and overage flags, and showing sanitized unavailable status when limits cannot be fetched or parsed.                                                                                                                                                                                | Operators need the same account-health visibility they get for OpenAI accounts without exposing raw GitHub/Copilot payloads or secrets.    |

## Data And State Model

Credentials live in the existing auth store:

```json
{
  "credentials": {
    "github-copilot:work": {
      "access_token": "github_pat_...",
      "provider": "github-copilot",
      "auth_method": "token"
    }
  }
}
```

Model configuration uses the existing `model_list` shape:

```json
{
  "model_name": "copilot-work",
  "provider": "github-copilot",
  "model": "auto",
  "auth_method": "token",
  "credential_id": "github-copilot:work"
}
```

Credential IDs are non-secret stable account references. Account-router graphs
may reference Copilot accounts as `credential:github-copilot` or
`credential:github-copilot:work`.

Example account-router block:

```json
{
  "id": "account-1",
  "type": "account",
  "account": "credential:github-copilot:gh-copilot"
}
```

Example account-router load balancer:

```json
{
  "id": "pool",
  "type": "load_balance",
  "accounts": [
    "credential:github-copilot:gh-copilot",
    "credential:github-copilot:backup"
  ],
  "strategy": "tokens_spent"
}
```

The existing local bridge keeps its local connection fields, for example
`provider: "github-copilot"`, `model: "auto"`, `api_base: "localhost:4321"`,
and `connect_mode: "grpc"`, but it is selected only when credential auth is not
requested.

## Auxiliary Interfaces

| Type       | Surface                                                | Contract                                                                                                                                                                                                                             | Requirement IDs                                                                                                             |
| ---------- | ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| Config     | `model_list[]`                                         | Provider `github-copilot`, model ID, auth method, credential ID, account-router credential references, and optional local bridge connection settings.                                                                                | `FR-GITHUB-COPILOT-001`, `FR-GITHUB-COPILOT-004` through `FR-GITHUB-COPILOT-007`, `FR-GITHUB-COPILOT-011`                   |
| Auth Store | `auth.AuthCredential`                                  | Store provider-scoped Copilot access tokens under normalized credential IDs with no plaintext exposure through read APIs.                                                                                                            | `FR-GITHUB-COPILOT-002`, `FR-GITHUB-COPILOT-003`, `FR-GITHUB-COPILOT-009`                                                   |
| HTTP       | `/api/oauth*`, `/api/accounts/models*`                 | List/login/logout Copilot credentials without auto-creating model entries, expose provider metadata, save explicit model/router entries, fetch direct Copilot API model IDs for selected credentials, fetch sanitized account limits when available, and test model availability. | `FR-GITHUB-COPILOT-001` through `FR-GITHUB-COPILOT-012`                                                                     |
| Provider   | `providers.CreateProviderFromConfig` and `LLMProvider` | Select the subscription client or local bridge, list models, create sessions, send prompts, close resources, and return normal responses/errors.                                                                                     | `FR-GITHUB-COPILOT-004` through `FR-GITHUB-COPILOT-009`                                                                     |
| Frontend   | Accounts and model setup UI                            | Render Copilot as an account/provider option, submit token credentials, route selected credential refs, display account-limit summaries when returned, and fetch shared router models through launcher-authenticated API helpers.    | `FR-GITHUB-COPILOT-001`, `FR-GITHUB-COPILOT-002`, `FR-GITHUB-COPILOT-008`, `FR-GITHUB-COPILOT-011`, `FR-GITHUB-COPILOT-012` |

## Algorithms And Ordering

1. Normalize incoming provider aliases so `copilot` resolves to
   `github-copilot`.
2. When listing OAuth providers, include `github-copilot` in provider order and
   enumerate all credentials whose normalized key belongs to that provider.
3. On token login, normalize the requested credential ID, validate the token
   family, optionally fetch the GitHub user identity for display metadata, then
   atomically persist `AuthCredential{Provider:"github-copilot",
AuthMethod:"token"}`.
4. During provider construction, treat `auth_method` `token` or `oauth` as the
   subscription-backed path. Resolve the configured credential, require a
   non-empty access token, and build direct Copilot API requests with no
   ambient logged-in-user fallback.
5. For credential-backed chat, exchange or apply the stored GitHub token for
   Copilot API bearer auth, send the translated prompt to the direct Copilot
   HTTPS chat endpoint for the requested model or default model, and return the
   response content. This path must not invoke SDK startup or any executable.
6. For local bridge chat, preserve the current `api_base` / `connect_mode`
   behavior and default endpoint. Do not read stored credentials in that mode.
7. Account-router validation identifies `credential:` references before plain
   model-name lookups, parses the full provider-scoped credential ID after the
   prefix, and accepts `credential:github-copilot[:name]` in single-account and
   load-balance blocks when the referenced credential exists.
8. For credential-backed model fetch, resolve the selected stored credential,
   call `copilot_internal/v2/token` where supported, honor any returned
   account-specific `endpoints.api`, call direct Copilot `GET /models`, and
   return all non-empty model IDs. If exchange returns `403`, retry model
   listing once with the raw token against the fixed default Copilot API; do
   not retry other exchange failures, and accept the fallback only when the
   direct model request succeeds. For provider metadata without a selected
   credential, return only curated common-model hints; for status, validate
   the selected stored credential for account-backed entries and keep the
   existing local TCP probe only for bridge configs.
9. Account-limit fetch resolves each stored Copilot credential independently,
   validates token shape before network use, calls GitHub/Copilot usage
   endpoints with that token, maps recognized quota fields into the existing
   account-limit entry shape, deduplicates entries, and falls back to a
   sanitized available/unavailable status when detailed counters are missing.
10. Map token exchange, model listing, chat, SDK startup for bridge mode,
    session, send, entitlement, and context errors into provider errors; redact
    tokens and avoid logging request prompts unless existing provider diagnostics
    already allow them.

## Cross-Feature Behavior

Agent conversations own turn execution and fallback. This feature only adds a
new provider/account implementation that satisfies the existing provider
contract. Launcher management owns the authenticated HTTP shell, shared
account-router UI, and model management workflow; this spec owns
Copilot-specific account options, credential references, direct API model
listing, and provider behavior.
Security isolation owns secret redaction and atomic credential writes, which
Copilot credentials must reuse. Account routers may route to Copilot credential
accounts without special routing behavior.

## Failure And Edge Cases

- Missing Copilot credential returns a setup error naming the credential ID and
  provider.
- Empty tokens, classic `ghp_` PATs, unknown token prefixes, and malformed
  credential IDs are rejected before saving.
- A token that is syntactically valid but lacks Copilot entitlement fails during
  token exchange, model listing, or chat with a subscription/auth error.
- Environment variables such as `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, and
  `GITHUB_TOKEN` do not override a named credential-backed model entry.
- Local bridge failures mention the local endpoint or CLI transport; account
  client failures mention GitHub Copilot credential setup and never mention a
  missing local `copilot` executable.
- Direct model-list failures during router shared-model discovery are surfaced
  as per-account warnings without hiding models returned by other selected
  accounts.
- Account-limit failures for one Copilot credential do not suppress limit
  summaries for other OpenAI or Copilot credentials.
- Closing an already stopped direct credential provider is a no-op.
- Account-router fallback records Copilot errors against the stable
  `credential:github-copilot[:name]` identity.
- `credential:github-copilot:<name>` references with more than one colon are
  parsed as a single credential ID, not rejected or compared as plain model
  names.

## Acceptance Evidence

| Requirement IDs                                                                                    | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| -------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-GITHUB-COPILOT-001`                                                                            | Existing: [pkg/providers/provider_metadata.go](../../pkg/providers/provider_metadata.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go). Expected implementation tests: `pkg/providers/provider_metadata_test.go`, `web/backend/api/models_test.go`.                                                                                                                                                                                                                                                                 |
| `FR-GITHUB-COPILOT-002`, `FR-GITHUB-COPILOT-003`                                                   | Existing: [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go). Expected implementation tests: `web/backend/api/oauth_test.go`, `pkg/auth/store_test.go`.                                                                                                                                                                                                                                                                                                             |
| `FR-GITHUB-COPILOT-004`, `FR-GITHUB-COPILOT-005`, `FR-GITHUB-COPILOT-006`, `FR-GITHUB-COPILOT-009` | Existing: [pkg/providers/cli/github_copilot_provider.go](../../pkg/providers/cli/github_copilot_provider.go), [pkg/providers/cli/codex_cli_credentials_test.go](../../pkg/providers/cli/codex_cli_credentials_test.go). Expected implementation tests: `pkg/providers/cli/github_copilot_provider_test.go`, `pkg/providers/cli/github_copilot_provider_live_test.go`, `pkg/providers/factory_provider_test.go`. Tests must assert credential-backed construction/listing does not construct the SDK client or require a `copilot` executable. |
| `FR-GITHUB-COPILOT-007`                                                                            | Existing: [web/backend/api/models_test.go](../../web/backend/api/models_test.go). Expected implementation tests: `web/backend/api/models_test.go`, `pkg/providers/cli/github_copilot_provider_test.go`. Tests must cover direct token exchange, returned `endpoints.api`, raw-token fallback on exchange `404`, guarded model-only raw-token fallback on exchange `403`, fallback denial, no fallback for other exchange failures, dynamic `/models` results, dedupe, blank IDs, and no OpenAI-compatible `/models` call.                                                                                                                                        |
| `FR-GITHUB-COPILOT-008`, `FR-GITHUB-COPILOT-012`                                                   | Existing: [web/frontend/src/components/credentials/accounts-page.tsx](../../web/frontend/src/components/credentials/accounts-page.tsx), [web/frontend/src/components/credentials/account-onboarding-sheet.tsx](../../web/frontend/src/components/credentials/account-onboarding-sheet.tsx), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go). Expected implementation tests: `web/frontend/src/components/credentials/accounts-page.test.tsx`, `web/frontend/tests/ui-smoke.spec.ts`.       |
| `FR-GITHUB-COPILOT-010`                                                                            | Expected implementation tests when OAuth is added: `web/backend/api/oauth_test.go`, `web/frontend/src/components/credentials/accounts-page.test.tsx`.                                                                                                                                                                                                                                                                                                                                                                                         |
| `FR-GITHUB-COPILOT-011`                                                                            | Expected implementation tests: `pkg/config/account_router_test.go`, `web/backend/api/models_test.go`, `pkg/agent/account_router_test.go`, `pkg/accountrouter/router_test.go`. Tests must cover Copilot credential refs in both account blocks and load-balance blocks for all supported strategies.                                                                                                                                                                                                                                                 |

## Implementation Anchors

- [pkg/providers/cli/github_copilot_provider.go](../../pkg/providers/cli/github_copilot_provider.go)
- [pkg/providers/cli_facade.go](../../pkg/providers/cli_facade.go)
- [pkg/providers/factory.go](../../pkg/providers/factory.go)
- [pkg/providers/factory_provider.go](../../pkg/providers/factory_provider.go)
- [pkg/providers/provider_catalog.go](../../pkg/providers/provider_catalog.go)
- [pkg/providers/provider_metadata.go](../../pkg/providers/provider_metadata.go)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/auth/store.go](../../pkg/auth/store.go)
- [pkg/auth/token.go](../../pkg/auth/token.go)
- [web/backend/api/oauth.go](../../web/backend/api/oauth.go)
- [web/backend/api/models.go](../../web/backend/api/models.go)
- [web/backend/api/model_status.go](../../web/backend/api/model_status.go)
- [web/frontend/src/api/oauth.ts](../../web/frontend/src/api/oauth.ts)
- [web/frontend/src/components/credentials/account-router-editor-page.tsx](../../web/frontend/src/components/credentials/account-router-editor-page.tsx)
- [web/frontend/src/components/credentials/account-onboarding-sheet.tsx](../../web/frontend/src/components/credentials/account-onboarding-sheet.tsx)
- [web/frontend/src/components/credentials/accounts-page.tsx](../../web/frontend/src/components/credentials/accounts-page.tsx)

## Surface Ownership

Owns: CODE pkg/providers/cli/github*copilot_provider.go
Owns: CODE pkg/providers/cli_facade.go
Owns: CODE pkg/providers/factory.go
Owns: CODE pkg/providers/factory_provider.go
Owns: CODE pkg/providers/provider*catalog.go
Owns: CODE pkg/providers/provider*metadata.go
Owns: CODE pkg/agent/instance.go
Owns: CODE pkg/config/config.go
Owns: CODE pkg/auth/oauth.go
Owns: CODE pkg/auth/store.go
Owns: CODE pkg/auth/token.go
Owns: CODE web/backend/api/oauth.go
Owns: CODE web/backend/api/models.go
Owns: CODE web/backend/api/model_status.go
Owns: CODE web/frontend/src/api/oauth.ts
Owns: CODE web/frontend/src/components/credentials/account-router-editor-page.tsx
Owns: CODE web/frontend/src/components/credentials/account-onboarding-sheet.tsx
Owns: CODE web/frontend/src/components/credentials/accounts-page.tsx
Owns: CODE web/frontend/src/hooks/use-credentials-page.ts
Owns: CODE web/frontend/src/i18n/locales/bn-in.json
Owns: CODE web/frontend/src/i18n/locales/cs.json
Owns: CODE web/frontend/src/i18n/locales/en.json
Owns: CODE web/frontend/src/i18n/locales/pt-br.json
Owns: CODE web/frontend/src/i18n/locales/zh.json
Owns: CONFIG.model*list*
Owns: HTTP * /api/oauth*
Owns: HTTP * /api/accounts/models\*
Owns: TEST pkg/auth/token_test.go
Owns: TEST pkg/providers/cli/github_copilot_provider_live_test.go
Owns: TEST pkg/providers/cli/github_copilot_provider_test.go
Owns: TEST pkg/providers/factory_provider_test.go
Owns: TEST pkg/config/account_router_test.go
Owns: TEST pkg/agent/account_router_test.go
Owns: TEST web/backend/api/oauth_test.go
Owns: TEST web/backend/api/models_test.go
Owns: TEST web/frontend/src/components/credentials/accounts-page.test.tsx
