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
accounts must use explicit stored credentials and the public GitHub Copilot SDK
authentication surface, not undocumented Copilot proxy endpoints.

## Reconstruction Notes

- Similarity target: recreate the Codex subscription-account pattern for
  GitHub Copilot: a provider-specific account type in auth/UI, a credential
  resolver in the provider factory, a subscription-backed API client, and
  launcher model/status plumbing.
- Core types/functions: `auth.AuthCredential`, `auth.NormalizeCredentialID`,
  OAuth provider registration in `web/backend/api/oauth.go`,
  `providers.ModelProviderOption`, `providers.CreateProviderFromConfig`,
  `GitHubCopilotProvider`, and the frontend `OAuthProvider` account flow.
- Runtime ordering: normalize model/provider config, resolve
  `github-copilot[:name]` credentials, validate token form, create a Copilot
  SDK client with explicit token auth, start a session with the requested model,
  send the translated prompt, return a normal `LLMResponse`, and stop client
  resources when closed.
- Non-obvious constraints: OpenAI Codex account auth and GitHub Copilot account
  auth are separate products; GitHub's public REST Copilot APIs cover
  management/metrics rather than chat completions; the Copilot SDK documents
  GitHub signed-in, OAuth GitHub App, environment token, and BYOK modes, with
  explicit `gitHubToken` taking priority.

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
- The Go SDK starts a client, creates a session, and sends prompts with
  `SendAndWait`:
  [Copilot SDK getting started](https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md).

## Requirements

| ID                      | Level  | Requirement                                                                                                                                                                                                                                                                                                                                         | Rationale                                                                                                                     |
| ----------------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `FR-GITHUB-COPILOT-001` | MUST   | Provider metadata exposes `github-copilot` as both a selectable model provider and a credential-backed account provider, with display name `GitHub Copilot`, alias `copilot`, default model `auto`, and a token-based default auth method.                                                                                                          | Users need to discover Copilot alongside other subscription-backed accounts instead of configuring a local transport by hand. |
| `FR-GITHUB-COPILOT-002` | MUST   | `/api/oauth/providers` lists `github-copilot` with token login support; login normalizes credential IDs as `github-copilot` or `github-copilot:<name>`, rejects invalid names, stores the token in `auth.AuthCredential`, and logout removes only the selected credential.                                                                          | Account routers and model entries need stable, provider-scoped credential references.                                         |
| `FR-GITHUB-COPILOT-003` | MUST   | Token login accepts supported GitHub token families used by Copilot SDK/CLI (`gho_`, `ghu_`, `github_pat_`) and rejects classic `ghp_` PATs with an actionable error before persisting.                                                                                                                                                             | Unsupported tokens should fail at setup time rather than during a chat turn.                                                  |
| `FR-GITHUB-COPILOT-004` | MUST   | A `model_list` entry with provider `github-copilot` and credential auth (`auth_method` `token` or `oauth`) resolves the stored credential and constructs a Copilot SDK client with explicit `GitHubToken` and `UseLoggedInUser=false`. It must not silently fall back to environment variables, keychain state, `gh auth`, or the local CLI bridge. | Named accounts must run as the selected user, not whichever Copilot/GitHub identity happens to be logged in locally.          |
| `FR-GITHUB-COPILOT-005` | MUST   | The existing local Copilot bridge remains available when `github-copilot` is configured without credential auth and with local connection settings; the subscription-backed client does not require `localhost:4321` or a pre-started external Copilot server.                                                                                      | Current local users must not lose compatibility while account-backed usage gets a cleaner setup path.                         |
| `FR-GITHUB-COPILOT-006` | MUST   | Chat translation preserves the existing `LLMProvider` contract: messages become a Copilot prompt with role/content context, the requested model is passed through unless blank, blank model uses `auto`, SDK responses become `LLMResponse.Content`, and SDK/client/session shutdown is idempotent.                                                 | Agent turns, fallback, and account routers should treat Copilot like any other provider.                                      |
| `FR-GITHUB-COPILOT-007` | MUST   | Model fetch/status logic does not call OpenAI-compatible `/models` for `github-copilot`; it exposes static common models from provider metadata, treats credential-backed entries as configured only when the selected credential exists, and only probes the local TCP bridge for non-credential configs.                                          | GitHub does not document a public Copilot chat model-list REST API, so fetch/status behavior must be honest and supportable.  |
| `FR-GITHUB-COPILOT-008` | MUST   | The Accounts UI, onboarding sheet, provider icon/label handling, TypeScript OAuth types, token placeholder text, status cards, and i18n strings include `github-copilot` and display registered Copilot credentials exactly like other named accounts.                                                                                              | Browser setup should be first-class and consistent with OpenAI, Anthropic, and Google Code Assist accounts.                   |
| `FR-GITHUB-COPILOT-009` | MUST   | Errors from SDK startup, session creation, entitlement/subscription denial, invalid tokens, unsupported token prefixes, timeouts, and canceled contexts are returned as concise provider errors with secrets redacted.                                                                                                                              | Users need actionable setup failures without leaking credentials in logs or launcher responses.                               |
| `FR-GITHUB-COPILOT-010` | SHOULD | If a GitHub OAuth App client is later configured, the same account provider may add browser/device OAuth login, but token login remains supported and OAuth credentials still normalize to the same `github-copilot[:name]` keys.                                                                                                                   | The initial implementation can ship without app credentials while leaving a compatible path for richer login.                 |

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

The existing local bridge keeps its local connection fields, for example
`provider: "github-copilot"`, `model: "auto"`, `api_base: "localhost:4321"`,
and `connect_mode: "grpc"`, but it is selected only when credential auth is not
requested.

## Auxiliary Interfaces

| Type       | Surface                                                | Contract                                                                                                                            | Requirement IDs                                                                  |
| ---------- | ------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| Config     | `model_list[]`                                         | Provider `github-copilot`, model ID, auth method, credential ID, and optional local bridge connection settings.                     | `FR-GITHUB-COPILOT-001`, `FR-GITHUB-COPILOT-004` through `FR-GITHUB-COPILOT-007` |
| Auth Store | `auth.AuthCredential`                                  | Store provider-scoped Copilot access tokens under normalized credential IDs with no plaintext exposure through read APIs.           | `FR-GITHUB-COPILOT-002`, `FR-GITHUB-COPILOT-003`, `FR-GITHUB-COPILOT-009`        |
| HTTP       | `/api/oauth*`, `/api/models*`                          | List/login/logout Copilot credentials, expose provider metadata, save model entries, and test model availability.                   | `FR-GITHUB-COPILOT-001` through `FR-GITHUB-COPILOT-010`                          |
| Provider   | `providers.CreateProviderFromConfig` and `LLMProvider` | Select the subscription client or local bridge, create sessions, send prompts, close resources, and return normal responses/errors. | `FR-GITHUB-COPILOT-004` through `FR-GITHUB-COPILOT-009`                          |
| Frontend   | Accounts and model setup UI                            | Render Copilot as an account/provider option and submit token credentials through launcher-authenticated API helpers.               | `FR-GITHUB-COPILOT-001`, `FR-GITHUB-COPILOT-002`, `FR-GITHUB-COPILOT-008`        |

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
   non-empty access token, and create the SDK client with explicit token auth
   and logged-in-user fallback disabled.
5. For credential-backed chat, start the SDK client, create a session for the
   requested model or `auto`, send the translated prompt, return the response
   content, and close the client/session on normal and error paths.
6. For local bridge chat, preserve the current `api_base` / `connect_mode`
   behavior and default endpoint. Do not read stored credentials in that mode.
7. For model fetch, return provider metadata/common models instead of calling
   `/models`; for status, validate the selected stored credential for
   account-backed entries and keep the existing local TCP probe only for bridge
   configs.
8. Map SDK startup, session, send, entitlement, and context errors into
   provider errors; redact tokens and avoid logging request prompts unless
   existing provider diagnostics already allow them.

## Cross-Feature Behavior

Agent conversations own turn execution and fallback. This feature only adds a
new provider/account implementation that satisfies the existing provider
contract. Launcher management owns the authenticated HTTP shell and shared
layout; this spec owns Copilot-specific account options and provider behavior.
Security isolation owns secret redaction and atomic credential writes, which
Copilot credentials must reuse. Account routers may route to Copilot credential
accounts without special routing behavior.

## Failure And Edge Cases

- Missing Copilot credential returns a setup error naming the credential ID and
  provider.
- Empty tokens, classic `ghp_` PATs, unknown token prefixes, and malformed
  credential IDs are rejected before saving.
- A token that is syntactically valid but lacks Copilot entitlement fails during
  SDK startup/session/chat with a subscription/auth error.
- Environment variables such as `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, and
  `GITHUB_TOKEN` do not override a named credential-backed model entry.
- Local bridge failures mention the local endpoint or CLI transport; account
  client failures mention GitHub Copilot credential setup.
- Closing an already stopped SDK client is a no-op.
- Account-router fallback records Copilot errors against the stable
  `credential:github-copilot[:name]` identity.

## Acceptance Evidence

| Requirement IDs                                                                                    | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                            |
| -------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-GITHUB-COPILOT-001`                                                                            | Existing: [pkg/providers/provider_metadata.go](../../pkg/providers/provider_metadata.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go). Expected implementation tests: `pkg/providers/provider_metadata_test.go`, `web/backend/api/models_test.go`.                                                                                                                                                       |
| `FR-GITHUB-COPILOT-002`, `FR-GITHUB-COPILOT-003`                                                   | Existing: [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go). Expected implementation tests: `web/backend/api/oauth_test.go`, `pkg/auth/store_test.go`.                                                                                                                                                                                                   |
| `FR-GITHUB-COPILOT-004`, `FR-GITHUB-COPILOT-005`, `FR-GITHUB-COPILOT-006`, `FR-GITHUB-COPILOT-009` | Existing: [pkg/providers/cli/github_copilot_provider.go](../../pkg/providers/cli/github_copilot_provider.go), [pkg/providers/cli/codex_cli_credentials_test.go](../../pkg/providers/cli/codex_cli_credentials_test.go). Expected implementation tests: `pkg/providers/cli/github_copilot_provider_test.go`, `pkg/providers/cli/github_copilot_provider_live_test.go`, `pkg/providers/factory_provider_test.go`.                     |
| `FR-GITHUB-COPILOT-007`                                                                            | Existing: [web/backend/api/models_test.go](../../web/backend/api/models_test.go). Expected implementation tests: `web/backend/api/models_test.go`.                                                                                                                                                                                                                                                                                  |
| `FR-GITHUB-COPILOT-008`                                                                            | Existing: [web/frontend/src/components/credentials/accounts-page.tsx](../../web/frontend/src/components/credentials/accounts-page.tsx), [web/frontend/src/components/credentials/account-onboarding-sheet.tsx](../../web/frontend/src/components/credentials/account-onboarding-sheet.tsx). Expected implementation tests: `web/frontend/src/components/credentials/accounts-page.test.tsx`, `web/frontend/tests/ui-smoke.spec.ts`. |
| `FR-GITHUB-COPILOT-010`                                                                            | Expected implementation tests when OAuth is added: `web/backend/api/oauth_test.go`, `web/frontend/src/components/credentials/accounts-page.test.tsx`.                                                                                                                                                                                                                                                                               |

## Implementation Anchors

- [pkg/providers/cli/github_copilot_provider.go](../../pkg/providers/cli/github_copilot_provider.go)
- [pkg/providers/cli_facade.go](../../pkg/providers/cli_facade.go)
- [pkg/providers/factory.go](../../pkg/providers/factory.go)
- [pkg/providers/factory_provider.go](../../pkg/providers/factory_provider.go)
- [pkg/providers/provider_catalog.go](../../pkg/providers/provider_catalog.go)
- [pkg/providers/provider_metadata.go](../../pkg/providers/provider_metadata.go)
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
Owns: CODE pkg/providers/cli_facade.go *
Owns: CODE pkg/providers/factory.go _
Owns: CODE pkg/providers/factory_provider.go _
Owns: CODE pkg/providers/provider*catalog.go *
Owns: CODE pkg/providers/provider*metadata.go *
Owns: CODE pkg/auth/oauth.go _
Owns: CODE pkg/auth/store.go _
Owns: CODE pkg/auth/token.go _
Owns: CODE web/backend/api/oauth.go _
Owns: CODE web/backend/api/models.go _
Owns: CODE web/backend/api/model_status.go _
Owns: CODE web/frontend/src/api/oauth.ts _
Owns: CODE web/frontend/src/components/credentials/account-router-editor-page.tsx _
Owns: CODE web/frontend/src/components/credentials/account-onboarding-sheet.tsx _
Owns: CODE web/frontend/src/components/credentials/accounts-page.tsx _
Owns: CODE web/frontend/src/hooks/use-credentials-page.ts _
Owns: CODE web/frontend/src/i18n/locales/bn-in.json _
Owns: CODE web/frontend/src/i18n/locales/cs.json _
Owns: CODE web/frontend/src/i18n/locales/en.json _
Owns: CODE web/frontend/src/i18n/locales/pt-br.json _
Owns: CODE web/frontend/src/i18n/locales/zh.json _
Owns: CONFIG.model*list*
Owns: HTTP _ /api/oauth_
Owns: HTTP _ /api/models_
Owns: TEST pkg/auth/token_test.go
Owns: TEST pkg/providers/cli/github_copilot_provider_live_test.go
Owns: TEST pkg/providers/cli/github_copilot_provider_test.go
Owns: TEST pkg/providers/factory_provider_test.go
Owns: TEST web/backend/api/oauth_test.go
Owns: TEST web/backend/api/models_test.go
Owns: TEST web/frontend/src/components/credentials/accounts-page.test.tsx
