# Launcher Management UX

## Feature ID

`FR-LAUNCHER`

## Behavior Summary

The web launcher provides authenticated browser management for configuration,
models, OAuth credentials, tools, skills, sessions, gateway process lifecycle,
startup behavior, update, and runtime version metadata.

## Reconstruction Notes

- Similarity target: recreate authenticated launcher APIs for dashboard auth, config/model/OAuth/tool/skill/session/gateway/system management, and JSON error behavior.
- Core types/functions: API handler/router, dashboard auth middleware/store, launcher config, model handlers, OAuth flow state, gateway process manager, startup/update/version handlers.
- Runtime ordering: authenticate dashboard requests, load config, validate request body, mutate specific subsystem, save atomically where applicable, apply runtime side effects, return JSON.
- Non-obvious constraints: secrets are preserved/redacted, logout is POST-only, login is rate-limited, OAuth flow state expires, and gateway logs remain inspectable after failures.

## Requirements

| ID                | Level  | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Rationale                                                                                                                                                      |
| ----------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-LAUNCHER-001` | MUST   | Dashboard access requires password setup/login and an HttpOnly session cookie; local bootstrap auto-login is loopback-only.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Browser management must be gated.                                                                                                                              |
| `FR-LAUNCHER-002` | MUST   | Config GET/PUT/PATCH/reset preserves schema defaults, secure string semantics, model API-key payloads, existing model secrets across equivalent model alias changes, and runtime log-level application.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Launcher config editing must not corrupt config or credentials.                                                                                                |
| `FR-LAUNCHER-003` | MUST   | Model management lists, updates, deletes, tests, fetches, and sets default model entries without exposing stored secret values; model edit forms must expose `reasoning_effort` next to the model identifier and validate it with the same rules as runtime config; model updates must not create blank stored secret entries when no key exists; default and example configs must not seed runnable model aliases; the Accounts header must not expose Add Model or Saved Catalogs actions, account credentials are created through Add Account only, and existing runnable model aliases such as `deepseek-chat`, `ark-code-latest`, or `azure-gpt5` must not render as account cards; list/default APIs may expose stored credentials as generated virtual `credential:<provider>:<id>` account choices for Chat without persisting them in `model_list[]`; account-router entries can be created, edited, listed, deleted, and set as default through the Accounts surface without storing API secrets or a model field on the router entry, render in their own Accounts section instead of provider account cards, and appear in Chat under a separate Account Routers selector group, with a fullscreen create UI that starts empty and prompts for an account, load-balancer, or branch block, a fullscreen UI graph editor that connects credential accounts and router blocks on a draggable, pannable, zoomable canvas, branch controls for account-limit comparisons and basic math expressions, plus a raw JSON graph editor; after a router is chosen, Chat discovers selectable model IDs for its referenced accounts, reports per-account fetch failures while retaining IDs returned by responding accounts, and supplies the user's choice only as a turn-scoped model ID without saving it on the router; model-router entries are stored in `model_routers[]`, materialized as virtual `model-router` rows for model management/default selection, edited from the Models surface, and never persisted in `model_list[]`. | Users need safe model and account administration without silently creating an unintended router block, duplicating account setup as model setup, coupling an account graph to a model, or hiding usable Chat choices when one account is down. |
| `FR-LAUNCHER-004` | MUST   | OAuth login flow creates, polls, completes, and logs out provider credentials through bounded flow state; token login supports registered providers that require pasted credentials, including `github-copilot`, plus every creatable provider from the backend model provider catalog such as DeepSeek and Google Gemini; login persists provider credentials only and must not create default model entries, runnable model entries, or account-router blocks; the accounts UI lists only registered provider accounts, exposes a separate onboarding surface that can assign named credential IDs, infers a missing OpenAI account name from the OAuth email local-part, displays OpenAI account headers as provider plus auth method and subscription type when known, displays GitHub Copilot token-backed accounts with provider labels/icons, and displays sanitized ChatGPT Codex and GitHub Copilot account usage limits by reading Picoclaw credentials and calling provider-specific usage APIs without exposing raw upstream error bodies or CLI config state; when Codex reports earned usage-limit reset availability, the OpenAI account summary shows the authoritative available count including zero and indicates that an available reset is used automatically for eligible exhaustion. | OAuth-backed providers need browser setup without presenting unregistered accounts as active entries, creating default models, or duplicating accounts as models. |
| `FR-LAUNCHER-005` | MUST   | Gateway lifecycle endpoints report status/logs and start/stop/restart managed gateway processes without losing log diagnostics.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Desktop users need process control.                                                                                                                            |
| `FR-LAUNCHER-006` | MUST   | Startup, launcher config, update, and version endpoints report or mutate only their documented system settings.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | System management must be narrow and auditable.                                                                                                                |
| `FR-LAUNCHER-007` | SHOULD | API errors return JSON responses with actionable messages and appropriate status codes.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Frontend UX needs consistent failures.                                                                                                                         |
| `FR-LAUNCHER-008` | MUST   | Model fetch distinguishes regular OpenAI API-key listings from OpenAI OAuth/token Codex subscription listings; credential-backed OpenAI fetches use the stored credential, account headers, and the current minimum Codex-compatible client version required for GPT-5.6 model visibility against the ChatGPT Codex models endpoint, while API-key fetches continue to use the OpenAI-compatible `/models` endpoint; GitHub Copilot model fetch exposes static metadata/common models without a credential, uses direct Copilot model listing with the stored token for credential-backed fetches, and credential-backed status checks validate stored credentials instead of probing the local bridge.                                                                                                                                                                                                                                                             | Subscription and API-key accounts have different upstream auth and must not fail or mix credentials.                                                           |
| `FR-LAUNCHER-009` | SHOULD | Shared launcher layout, theme, and primitive controls remain responsive, token-driven, keyboard-accessible, and free of clipped controls across desktop and narrow mobile widths.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Dashboard navigation and process controls must stay usable while visual styling evolves.                                                                       |

## Data And State Model

Launcher state includes dashboard password/session storage, launcher-specific
config, OAuth flow maps, config file path, gateway process state/logs, model
catalog entries, model fetch auth method and credential IDs, startup settings,
and update request status.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/auth/**
Owns: CODE cmd/picoclaw/internal/cliui/**
Owns: CODE cmd/picoclaw/internal/config/**
Owns: CODE cmd/picoclaw/internal/helpers.go
Owns: CODE cmd/picoclaw/internal/migrate/**
Owns: CODE cmd/picoclaw/internal/onboard/**
Owns: CODE pkg/migrate/**
Owns: CODE web/backend/**
Owns: CODE web/frontend/src/api/launcher-auth.ts
Owns: CODE web/frontend/src/api/models.ts
Owns: CODE web/frontend/src/api/oauth.ts
Owns: CODE web/frontend/src/api/system.ts
Owns: CODE web/frontend/src/app-providers.tsx
Owns: CODE web/frontend/src/components/app-*
Owns: CODE web/frontend/src/components/config/**
Owns: CODE web/frontend/src/components/credentials/**
Owns: CODE web/frontend/src/components/models/**
Owns: CODE web/frontend/src/components/page-header.tsx
Owns: CODE web/frontend/src/components/tour/**
Owns: CODE web/frontend/src/components/ui/**
Owns: CODE web/frontend/src/hooks/use-credentials-page.ts
Owns: CODE web/frontend/src/hooks/use-theme.ts
Owns: CODE web/frontend/src/i18n/**
Owns: CODE web/frontend/src/index.css
Owns: CODE web/frontend/src/lib/**
Owns: CODE web/frontend/src/main.tsx
Owns: CODE web/frontend/src/routes/agent.tsx
Owns: CODE web/frontend/src/routes/config*
Owns: CODE web/frontend/src/routes/accounts.account-router.$index.tsx
Owns: CODE web/frontend/src/routes/accounts.account-router.new.tsx
Owns: CODE web/frontend/src/routes/accounts.tsx
Owns: CODE web/frontend/src/routes/credentials.tsx
Owns: CODE web/frontend/src/routes/launcher-*
Owns: CODE web/frontend/src/routes/models.tsx
Owns: CODE web/frontend/src/store/**
Owns: CODE web/frontend/src/test/**
Owns: CLI cmd/picoclaw/internal/auth/* *
Owns: CLI cmd/picoclaw/internal/config/* *
Owns: CLI cmd/picoclaw/internal/migrate/* *
Owns: CLI cmd/picoclaw/internal/onboard/* *
Owns: HTTP /api/update
Owns: HTTP * /api/auth*
Owns: HTTP * /api/config*
Owns: HTTP * /api/accounts/models*
Owns: HTTP * /api/oauth*
Owns: HTTP GET /oauth/callback
Owns: HTTP * /api/system*
Owns: HTTP * /api/wecom*
Owns: HTTP * /api/weixin*
Owns: HTTP * /api/workflows*
Owns: TEST cmd/picoclaw/internal/auth/* *
Owns: TEST cmd/picoclaw/internal/cliui/* *
Owns: TEST cmd/picoclaw/internal/config/* *
Owns: TEST cmd/picoclaw/internal/helpers_test.go *
Owns: TEST cmd/picoclaw/internal/migrate/* *
Owns: TEST cmd/picoclaw/internal/onboard/* *
Owns: TEST pkg/migrate/* *
Owns: TEST pkg/migrate/internal/* *
Owns: TEST pkg/migrate/sources/openclaw/* *
Owns: TEST scripts/featuretools_lib_test.go *
Owns: TEST web/backend/* *
Owns: TEST web/backend/api/auth*
Owns: TEST web/backend/api/config*
Owns: TEST web/backend/api/launcher*
Owns: TEST web/backend/api/model*
Owns: TEST web/backend/api/models*
Owns: TEST web/backend/api/oauth*
Owns: TEST web/backend/api/startup*
Owns: TEST web/backend/api/version*
Owns: TEST web/backend/api/wecom*
Owns: TEST web/backend/api/weixin*

## Auxiliary Interfaces

| Type     | Surface                                                                                                                                                                                      | Contract                                                                                                                                                                                               | Requirement IDs                                         |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------- |
| HTTP     | `/api/auth*`, `/api/config*`, `/api/accounts/models*`, `/api/oauth*`, `/api/system*`, `/api/update`, `/api/weixin*`, `/api/wecom*`                                                           | Authenticated launcher management endpoints.                                                                                                                                                           | `FR-LAUNCHER-001` through `FR-LAUNCHER-007`             |
| CLI      | `picoclaw auth`, `picoclaw config`, `picoclaw onboard`, `picoclaw migrate`                                                                                                                   | Non-browser setup, auth, and migration helpers.                                                                                                                                                        | `FR-LAUNCHER-002`, `FR-LAUNCHER-004`                    |
| Config   | Launcher config file beside app config                                                                                                                                                       | Port/public/access options and dashboard auth migration.                                                                                                                                               | `FR-LAUNCHER-001`, `FR-LAUNCHER-006`                    |
| Frontend | `web/frontend/AGENTS.md`, `docs/design/frontend-guidelines.md`, `docs/features/frontend-ownership.json`, `web/frontend/scripts/lint-ui-rules.mjs`, and `web/frontend/tests/ui-smoke.spec.ts` | Agent-facing launcher UI guidance plus static, formatting, accessibility, ownership, and mocked-route browser checks. Feature-specific UI behavior remains owned by the relevant product feature spec. | `FR-LAUNCHER-002`, `FR-LAUNCHER-007`, `FR-LAUNCHER-009` |

## Algorithms And Ordering

1. Route launcher requests through access control and dashboard authentication
   before handler-specific parsing.
2. For config and model writes, decode JSON, normalize provider/model fields and
   optional model controls, validate schema-specific fields, preserve stored
   secure strings when masked values are submitted, reapply explicit model
   API-key payloads after security-file merges, retain existing model secrets
   across equivalent alias/name changes, clear credential fields for
   account-router entries, validate router credential account refs and legacy
   model-name refs, validate model-router targets before saving top-level
   router lists, write the config atomically, and apply runtime log-level
   changes.
3. For OAuth requests, create bounded flow state, redirect or poll provider
   login, exchange callback state for credentials, then persist or clear
   provider auth records. Token-backed account providers such as GitHub Copilot
   validate the submitted token before storage and create the default provider
   model entry using the provider-scoped credential ID. The launcher accounts
   page renders stored credentials and account-router aliases in one account
   card grid while keeping new account onboarding behind an explicit add-account
   surface. When an OpenAI OAuth account name is omitted, the saved credential ID
   uses the email local-part as the provider-scoped suffix. OpenAI usage-limit
   lookup uses Picoclaw credential records instead of Codex CLI config and maps
   an upstream earned-reset count to the matching account summary without
   exposing credentials or making the read path consume a reset.
4. For model fetch requests, resolve stored model auth when a model index is
   supplied, prefer explicit request credentials otherwise, route OpenAI
   OAuth/token fetches to the ChatGPT Codex model list endpoint with a
   Codex-compatible `client_version` new enough to surface GPT-5.6 subscription
   models, expose GitHub Copilot static metadata models for Copilot requests,
   and keep regular API-key fetches on the OpenAI-compatible `/models` path.
5. For gateway lifecycle requests, inspect current process state first, execute
   start/stop/restart transitions only when valid, and retain log buffers for
   status and diagnostics responses.
6. Return JSON for success and error paths with status codes that match
   validation, auth, not-found, conflict, or internal failure classes.

## Cross-Feature Behavior

Launcher surfaces expose other features but do not define them. Model management
feeds agent conversations. Gateway endpoints control chat-channel runtime.
Session endpoints are owned by session memory. Thread endpoints and
thread-specific UI are owned by threads, while launcher management still owns
shared authenticated dashboard layout and routing shell components.
Workflow HTTP endpoints and dashboard routes are exposed through the launcher
router and shared shell, while workflow definition, run, graph, cancel, retry,
and event semantics remain owned by the workflows feature.
Event operator proxy endpoints and the Events dashboard route are likewise
registered through the shared launcher router and navigation shell and inherit
dashboard authentication. Event inspection, payload, replay, and live-gateway
authorization semantics remain owned by event automation and security
isolation.
Git workspace config fields, API routes, sidebar navigation, and dashboard entry
points are exposed through shared launcher surfaces, while workspace allocation,
inventory, cleanup, drop, and retention semantics are owned by the git
workspaces feature.

## Failure And Edge Cases

- GET logout is rejected; logout requires POST JSON.
- Login is rate-limited per client IP.
- OAuth flow IDs expire and unknown states fail.
- Config update preserves model API-key payloads and keeps existing model
  secrets when equivalent provider/model/API-base entries are renamed.
- Model update preserves existing secrets unless explicitly changed and avoids
  persisting blank secret placeholders for models with no key.
- Account-router add/update rejects unknown, router, or ambiguous account
  references as validation failures and does not persist API keys on router
  entries.
- Chat model discovery for a chosen account router reports referenced accounts
  whose fetch fails or returns no models while continuing to show selectable
  IDs from responding accounts; the chosen ID is turn-scoped and is not
  persisted on the router.
- Model-router add/update rejects unknown targets and stores the router graph in
  `model_routers[]` instead of `model_list[]`.
- Model add/update rejects unsupported `reasoning_effort` values before saving.
- OpenAI Codex model fetch fails with an actionable credential error when the
  selected OAuth/token credential is missing or empty.
- OpenAI Codex model fetch reports a concise upstream response detail when the
  model list endpoint rejects the request.
- Codex account-limit responses that omit earned-reset state leave the reset
  summary hidden; supported zero-credit responses remain visible and do not
  trigger redemption from the launcher read path.
- GitHub Copilot model fetch does not call OpenAI-compatible `/models`; missing
  credential-backed entries fail with a credential setup error, while
  non-credential entries keep local bridge probing.
- Public launcher access obeys configured host/CIDR policy.
- Header controls collapse without clipping at extra-narrow mobile widths.
- Global theme and CSS token changes preserve semantic colors instead of raw
  ad hoc color values.

## Acceptance Evidence

| Requirement IDs                      | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-LAUNCHER-001`                    | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [web/backend/middleware/access_control_test.go](../../web/backend/middleware/access_control_test.go)                                                                                                                                                                                                                                                        |
| `FR-LAUNCHER-002`, `FR-LAUNCHER-007` | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go)                                                                                                                                                                                                                                                                                                                                                                                               |
| `FR-LAUNCHER-003`                    | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/backend/api/model_status_test.go](../../web/backend/api/model_status_test.go), [web/backend/api/model_catalog_test.go](../../web/backend/api/model_catalog_test.go), [web/frontend/src/components/models/model-card.test.tsx](../../web/frontend/src/components/models/model-card.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-004`                    | [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [cmd/picoclaw/internal/auth](../../cmd/picoclaw/internal/auth)                                                                                                                                                                                                         |
| `FR-LAUNCHER-005`, `FR-LAUNCHER-006` | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/startup_test.go](../../web/backend/api/startup_test.go), [web/backend/api/version_test.go](../../web/backend/api/version_test.go)                                                                                                                                                                                                                                                                                                       |
| `FR-LAUNCHER-008`                    | [web/backend/api/models_test.go](../../web/backend/api/models_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `FR-LAUNCHER-009`                    | [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/scripts/lint-ui-rules.mjs](../../web/frontend/scripts/lint-ui-rules.mjs)                                                                                                                                                                                                                                                     |

## Implementation Anchors

- [web/backend/api/router.go](../../web/backend/api/router.go)
- [web/backend/middleware](../../web/backend/middleware)
- [web/backend/launcherconfig](../../web/backend/launcherconfig)
