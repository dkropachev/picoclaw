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

| ID                | Level  | Requirement                                                                                                                                                                                                                                                                                                                                                  | Rationale                                                                                            |
| ----------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| `FR-LAUNCHER-001` | MUST   | Dashboard access requires password setup/login and an HttpOnly session cookie; local bootstrap auto-login is loopback-only.                                                                                                                                                                                                                                  | Browser management must be gated.                                                                    |
| `FR-LAUNCHER-002` | MUST   | Config GET/PUT/PATCH/reset preserves schema defaults, secure string semantics, model API-key payloads, existing model secrets across equivalent model alias changes, and runtime log-level application.                                                                                                                                                      | Launcher config editing must not corrupt config or credentials.                                      |
| `FR-LAUNCHER-003` | MUST   | Model management lists, adds, updates, deletes, tests, fetches, and sets default model entries without exposing stored secret values; model add/edit forms must expose `reasoning_effort` next to the model identifier and validate it with the same rules as runtime config; model updates must not create blank stored secret entries when no key exists.  | Users need safe model administration.                                                                |
| `FR-LAUNCHER-004` | MUST   | OAuth login flow creates, polls, completes, and logs out provider credentials through bounded flow state.                                                                                                                                                                                                                                                    | OAuth-backed providers need browser setup.                                                           |
| `FR-LAUNCHER-005` | MUST   | Gateway lifecycle endpoints report status/logs and start/stop/restart managed gateway processes without losing log diagnostics.                                                                                                                                                                                                                              | Desktop users need process control.                                                                  |
| `FR-LAUNCHER-006` | MUST   | Startup, launcher config, update, and version endpoints report or mutate only their documented system settings.                                                                                                                                                                                                                                              | System management must be narrow and auditable.                                                      |
| `FR-LAUNCHER-007` | SHOULD | API errors return JSON responses with actionable messages and appropriate status codes.                                                                                                                                                                                                                                                                      | Frontend UX needs consistent failures.                                                               |
| `FR-LAUNCHER-008` | MUST   | Model fetch distinguishes regular OpenAI API-key listings from OpenAI OAuth/token Codex subscription listings; credential-backed OpenAI fetches use the stored credential, account headers, and a Codex-compatible client version against the ChatGPT Codex models endpoint, while API-key fetches continue to use the OpenAI-compatible `/models` endpoint. | Subscription and API-key accounts have different upstream auth and must not fail or mix credentials. |
| `FR-LAUNCHER-009` | SHOULD | Shared launcher layout, theme, and primitive controls remain responsive, token-driven, keyboard-accessible, and free of clipped controls across desktop and narrow mobile widths.                                                                                                                                                                            | Dashboard navigation and process controls must stay usable while visual styling evolves.             |

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
Owns: CODE web/frontend/src/routes/credentials.tsx
Owns: CODE web/frontend/src/routes/launcher-*
Owns: CODE web/frontend/src/routes/models.tsx
Owns: CODE web/frontend/src/store/**
Owns: CODE web/frontend/src/test/**
Owns: CLI cmd/picoclaw/internal/auth/*
Owns: CLI cmd/picoclaw/internal/config/*
Owns: CLI cmd/picoclaw/internal/migrate/*
Owns: CLI cmd/picoclaw/internal/onboard/*
Owns: HTTP /api/update
Owns: HTTP * /api/auth*
Owns: HTTP * /api/config*
Owns: HTTP * /api/models*
Owns: HTTP * /api/oauth*
Owns: HTTP GET /oauth/callback
Owns: HTTP * /api/system*
Owns: HTTP * /api/wecom*
Owns: HTTP * /api/weixin*
Owns: TEST cmd/picoclaw/internal/auth/*
Owns: TEST cmd/picoclaw/internal/cliui/*
Owns: TEST cmd/picoclaw/internal/config/*
Owns: TEST cmd/picoclaw/internal/helpers_test.go *
Owns: TEST cmd/picoclaw/internal/migrate/*
Owns: TEST cmd/picoclaw/internal/onboard/*
Owns: TEST scripts/featuretools_lib_test.go *
Owns: TEST web/backend/*
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
Owns: TEST pkg/migrate/*

## Auxiliary Interfaces

| Type     | Surface                                                                                                                                                                                      | Contract                                                                                                                                                                                               | Requirement IDs                                         |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------- |
| HTTP     | `/api/auth*`, `/api/config*`, `/api/models*`, `/api/oauth*`, `/api/system*`, `/api/update`, `/api/weixin*`, `/api/wecom*`                                                                    | Authenticated launcher management endpoints.                                                                                                                                                           | `FR-LAUNCHER-001` through `FR-LAUNCHER-007`             |
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
   across equivalent alias/name changes, write the config atomically, and apply
   runtime log-level changes.
3. For OAuth requests, create bounded flow state, redirect or poll provider
   login, exchange callback state for credentials, then persist or clear
   provider auth records.
4. For model fetch requests, resolve stored model auth when a model index is
   supplied, prefer explicit request credentials otherwise, route OpenAI
   OAuth/token fetches to the ChatGPT Codex model list endpoint with a
   Codex-compatible `client_version`, and keep regular API-key fetches on the
   OpenAI-compatible `/models` path.
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

## Failure And Edge Cases

- GET logout is rejected; logout requires POST JSON.
- Login is rate-limited per client IP.
- OAuth flow IDs expire and unknown states fail.
- Config update preserves model API-key payloads and keeps existing model
  secrets when equivalent provider/model/API-base entries are renamed.
- Model update preserves existing secrets unless explicitly changed and avoids
  persisting blank secret placeholders for models with no key.
- Model add/update rejects unsupported `reasoning_effort` values before saving.
- OpenAI Codex model fetch fails with an actionable credential error when the
  selected OAuth/token credential is missing or empty.
- OpenAI Codex model fetch reports a concise upstream response detail when the
  model list endpoint rejects the request.
- Public launcher access obeys configured host/CIDR policy.
- Header controls collapse without clipping at extra-narrow mobile widths.
- Global theme and CSS token changes preserve semantic colors instead of raw
  ad hoc color values.

## Acceptance Evidence

| Requirement IDs                      | Evidence                                                                                                                                                                                                                                                                                                                 |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `FR-LAUNCHER-001`                    | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go), [web/backend/middleware/access_control_test.go](../../web/backend/middleware/access_control_test.go)                                                                   |
| `FR-LAUNCHER-002`, `FR-LAUNCHER-007` | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go)                                                                                                                                                                                     |
| `FR-LAUNCHER-003`                    | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/backend/api/model_status_test.go](../../web/backend/api/model_status_test.go), [web/backend/api/model_catalog_test.go](../../web/backend/api/model_catalog_test.go) |
| `FR-LAUNCHER-004`                    | [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [cmd/picoclaw/internal/auth](../../cmd/picoclaw/internal/auth)                                                                                                                                                                                     |
| `FR-LAUNCHER-005`, `FR-LAUNCHER-006` | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/startup_test.go](../../web/backend/api/startup_test.go), [web/backend/api/version_test.go](../../web/backend/api/version_test.go)                                                                                             |
| `FR-LAUNCHER-008`                    | [web/backend/api/models_test.go](../../web/backend/api/models_test.go)                                                                                                                                                                                                                                                   |
| `FR-LAUNCHER-009`                    | [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/scripts/lint-ui-rules.mjs](../../web/frontend/scripts/lint-ui-rules.mjs)                                                                                                                                                 |

## Implementation Anchors

- [web/backend/api/router.go](../../web/backend/api/router.go)
- [web/backend/middleware](../../web/backend/middleware)
- [web/backend/launcherconfig](../../web/backend/launcherconfig)
