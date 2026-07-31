# Launcher Management UX

## Feature ID

`FR-LAUNCHER`

## Behavior Summary

The web launcher provides authenticated browser management for configuration,
models, OAuth credentials, tools, skills, MCP servers, sessions, gateway
process lifecycle, startup behavior, update, and runtime version metadata.
Full-config writes and workflow-specific config-coupled operations share one
launcher mutation boundary so a scoped workflow settings response cannot pair
values from one in-process config generation with another generation's
revision.
The authenticated launcher also exposes a narrowly validated same-origin
review API that projects browser requests to the managed gateway using only the
gateway process bearer and fixed request, response, redirect, and network
boundaries.

## Reconstruction Notes

- Similarity target: recreate authenticated launcher APIs for dashboard auth, config/model/OAuth/tool/skill/session/gateway/system management, and JSON error behavior.
- Core types/functions: API handler/router, dashboard auth middleware/store, launcher config, model handlers, provider and MCP OAuth flow state, gateway process manager, startup/update/version handlers.
- Runtime ordering: authenticate dashboard requests, load config, validate request body, mutate specific subsystem, save atomically where applicable, apply runtime side effects, return JSON.
- Non-obvious constraints: secrets are preserved/redacted, logout is POST-only, login is rate-limited, OAuth flow state expires, feature-specific management stays outside the generic config form, and gateway logs remain inspectable after failures.

## Requirements

| ID                | Level  | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Rationale                                                                                                                                                      |
| ----------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-LAUNCHER-001` | MUST   | Dashboard access requires password setup/login and an HttpOnly session cookie; local bootstrap auto-login is loopback-only.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Browser management must be gated.                                                                                                                              |
| `FR-LAUNCHER-002` | MUST   | Config GET/PUT/PATCH/reset preserves schema defaults, secure string semantics, model API-key payloads, existing model secrets across equivalent model alias changes, and runtime log-level application.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Launcher config editing must not corrupt config or credentials.                                                                                                |
| `FR-LAUNCHER-003` | MUST   | Account management and model-alias management are separate. `model_list[]` and credential records describe concrete provider accounts; `model_aliases[]` maps exact user-facing names to base concrete model IDs and optional overrides keyed only by concrete accounts. The default-selection API atomically validates and stores `agents.defaults.account_ref` plus alias-valued `model_name`; model/account/router create and edit may request that same default change through `set_as_default`, but the entry and selection must validate and save as one compare-and-swap mutation. Index-addressed model and alias updates and deletes require the opaque revision returned by the model-list read and reject stale revisions before interpreting an index. Account routers remain model-free, model-router terminals target aliases only, and chat uses the same independent selectors. No management path invents or persists a provider-default model. | Users need safe account and model administration without coupling an account graph to a model, partially persisting a failed default change, overwriting a concurrent edit, deleting a shifted row, or silently selecting a provider model. |
| `FR-LAUNCHER-004` | MUST   | OAuth login flow creates, polls, completes, and logs out provider credentials through bounded flow state; token login supports registered providers that require pasted credentials, including `github-copilot`, plus every creatable provider from the backend model provider catalog such as DeepSeek and Google Gemini; login persists provider credentials only and must not create default model entries, runnable model entries, or account-router blocks; the accounts UI lists only registered provider accounts, exposes a separate onboarding surface that can assign named credential IDs, infers a missing OpenAI account name from the OAuth email local-part, displays OpenAI account headers as provider plus auth method and subscription type when known, displays GitHub Copilot token-backed accounts with provider labels/icons, and displays sanitized ChatGPT Codex and GitHub Copilot account usage limits by reading Picoclaw credentials and calling provider-specific usage APIs without exposing raw upstream error bodies or CLI config state; when Codex reports earned usage-limit reset availability, the OpenAI account summary shows the authoritative available count including zero and indicates that an available reset is used automatically for eligible exhaustion. | OAuth-backed providers need browser setup without presenting unregistered accounts as active entries, creating default models, or duplicating accounts as models. |
| `FR-LAUNCHER-005` | MUST   | Gateway lifecycle endpoints report status/logs and start/stop/restart managed gateway processes without losing log diagnostics.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Desktop users need process control.                                                                                                                            |
| `FR-LAUNCHER-006` | MUST   | Startup, launcher config, update, and version endpoints report or mutate only their documented system settings.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | System management must be narrow and auditable.                                                                                                                |
| `FR-LAUNCHER-007` | SHOULD | API errors return JSON responses with actionable messages and appropriate status codes.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Frontend UX needs consistent failures.                                                                                                                         |
| `FR-LAUNCHER-008` | MUST   | Model fetch distinguishes regular OpenAI API-key listings from OpenAI OAuth/token Codex subscription listings; credential-backed OpenAI fetches use the stored credential, account headers, and the current minimum Codex-compatible client version required for GPT-5.6 model visibility against the ChatGPT Codex models endpoint, while API-key fetches continue to use the OpenAI-compatible `/models` endpoint; GitHub Copilot model fetch exposes static metadata/common models without a credential, uses direct Copilot model listing with the stored token for credential-backed fetches, and credential-backed status checks validate stored credentials instead of probing the local bridge.                                                                                                                                                                                                                                                             | Subscription and API-key accounts have different upstream auth and must not fail or mix credentials.                                                           |
| `FR-LAUNCHER-009` | SHOULD | Shared launcher layout, theme, and primitive controls remain responsive, token-driven, keyboard-accessible, and free of clipped controls across desktop and narrow mobile widths. Destructive controls use paired background/foreground theme tokens with sufficient contrast in light and dark modes instead of translucent destructive text treatments that fail automated accessibility checks.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Dashboard navigation and process controls must stay usable while visual styling evolves.                                                                       |
| `FR-LAUNCHER-010` | MUST   | The authenticated launcher composition registers the feature-owned MCP management and OAuth callback routes, exposes a dedicated Agent → MCP navigation entry, and removes MCP editing from the generic config form. Gateway restart detection includes enabled MCP discovery, server transport, custom-header, and nonsecret auth-revision changes. Shared forms announce validation errors and provide keyboard-accessible, labeled secret visibility controls.                                                                                                                                                                                                                                                                                                                                                                                                                                                        | MCP management must be easy to find, must not conflict with generic config saves, and must clearly apply runtime-relevant changes without weakening shared form accessibility. |
| `FR-LAUNCHER-011` | MUST   | Full-config PUT/PATCH/reset, generic tool-state writes, agent policy mutations, workflow-specific settings, template-install, publish, and workflow Run/Retry admission are serialized by one handler mutation boundary. Every cooperating `SaveConfig` call also holds a config-path advisory process/file lock, with the opaque generation covering both public JSON and the security sidecar. Full-config PUT/PATCH, generic tool-state, agent policy, and workflow settings mutations load an update-safe snapshot and perform final compare-and-swap saves against that exact generation; reset holds the lock across backup, secret preservation, and replacement. Stable scoped reads derive their opaque revision from the same snapshot without migration, backup, or save side effects. Agent responses derive restart effects from that captured config and a read-only in-memory gateway snapshot without discovering processes, attaching to them, or sanitizing PID metadata. Workflow Run and Retry reacquire that same advisory lock after their final readiness fence, compare the current public-plus-security generation with the admitted generation, and retain the lock through exact compatibility checking and durable root-run creation. The authenticated launcher registers agent management routes and navigation, and the gateway restart signature includes the complete ordered agent policy while preserving nil-versus-empty distinctions. | Scoped or merge-patch management must not return values or effects from one config generation with another generation's revision, lose a concurrent secret-only update, overwrite a mutation from another launcher or gateway process, hide an unapplied agent policy change, mutate gateway process metadata during an agent read, or admit execution from one generation while another process publishes a replacement before the run exists. |
| `FR-LAUNCHER-012` | MUST   | The authenticated launcher registers agent capability and activity routes without replacing existing management surfaces. Capability mutation holds the shared handler and advisory config boundaries through its final composite config/file fence and atomic workspace write, while gateway restart comparison combines the filesystem-pure config signature with only runtime-relevant `AGENT.md` frontmatter semantics. Activity is read-only: the gateway records a concrete numeric address from the listener that actually opened, including a single-stack localhost fallback; the launcher peeks PID authority without attachment, cleanup, or migration, rejects hostname and wildcard authority, validates the numeric target as loopback or a literal local-interface address, injects the process bearer into one exact bounded no-proxy/no-redirect request, forwards no browser credentials or ambient headers, and strictly reprojects the response. | Workspace policy must not race config ownership, prose-only edits must not spuriously require restart, and a browser activity view must not mutate process metadata or leak runtime bearer authority. |
| `FR-LAUNCHER-013` | MUST   | The authenticated launcher registers the exact `/api/reviews` list/detail routes and their bounded chat, finding-edit, drop/restore/rephrase, submit, and reconcile mutations. It rejects noncanonical paths, unexpected queries, methods, encodings, content types, cross-site mutations, invalid identifiers, and oversized bodies before proxying one exact route to the managed gateway. Proxying peeks PID authority without attaching, cleaning, or migrating process state; accepts only a numeric loopback or literal local-interface address; replaces browser credentials and ambient headers with the process bearer; disables proxy and redirect behavior; uses operation-specific timeouts; bounds the upstream response; and strictly reprojects status, content type, and JSON. Gateway composition starts and stops the durable review controller, poller, and submission workers with event automation and registers the protected runtime review routes. | Browser review operations need same-origin authentication without exposing runtime bearer authority, forwarding browser credentials, accepting an open proxy surface, or separating worker lifecycle from gateway ownership. |

## Data And State Model

Launcher state includes dashboard password/session storage, launcher-specific
config, provider and feature-owned OAuth flow maps, config file path, gateway
process state/logs and restart signature, model catalog entries, model fetch
auth method and credential IDs, startup settings, and update request status.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/auth/**
Owns: CODE cmd/picoclaw/internal/cliui/**
Owns: CODE cmd/picoclaw/internal/config/**
Owns: CODE cmd/picoclaw/internal/helpers.go
Owns: CODE cmd/picoclaw/internal/migrate/**
Owns: CODE cmd/picoclaw/internal/onboard/**
Owns: CODE pkg/migrate/**
Owns: CODE pkg/config/mutation*.go
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
Owns: CODE web/frontend/src/components/shared-form.tsx
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
Owns: HTTP * /api/accounts/model-aliases*
Owns: HTTP * /api/oauth*
Owns: HTTP GET /oauth/callback
Owns: HTTP * /api/system*
Owns: HTTP * /api/agents*
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
Owns: TEST pkg/config/mutation_test.go *
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
| HTTP     | Feature-owned `/api/workflows*` routes                                                                                                                                                       | Register workflow endpoints behind launcher authentication and shared bounded JSON response behavior; workflow projection, authoring, execution, and persistence semantics remain owned by the workflows and security-isolation features.                              | `FR-LAUNCHER-001`, `FR-LAUNCHER-007`                    |
| HTTP     | Feature-owned `/api/mcp*` and `/mcp/oauth/callback` routes                                                                                                                                | Register MCP management behind launcher authentication, retain the bounded callback exception, and cancel outstanding flows during handler shutdown.                                                  | `FR-LAUNCHER-010`                                       |
| HTTP     | Feature-owned `/api/agents*` routes                                                                                                                                                         | Register ordered agent policy reads, revision-fenced config/workspace mutations, and bounded activity proxying behind launcher authentication, navigation, strict JSON handling, and gateway restart reporting. | `FR-LAUNCHER-011`, `FR-LAUNCHER-012` |
| Go API   | `config.ConfigRevision`, `config.LoadConfigSnapshot`, `config.LoadConfigForUpdateSnapshot`, `config.SaveConfigIfRevision`, `config.WithConfigMutationLock`                                  | Derive, atomically capture, compare-and-save, or retain the canonical public-plus-security config generation under the shared process and advisory file lock.                                          | `FR-LAUNCHER-011`                                       |
| CLI      | `picoclaw auth`, `picoclaw config`, `picoclaw onboard`, `picoclaw migrate`                                                                                                                   | Non-browser setup, auth, and migration helpers.                                                                                                                                                        | `FR-LAUNCHER-002`, `FR-LAUNCHER-004`                    |
| Config   | Launcher config file beside app config                                                                                                                                                       | Port/public/access options and dashboard auth migration.                                                                                                                                               | `FR-LAUNCHER-001`, `FR-LAUNCHER-006`                    |
| Frontend | `web/frontend/AGENTS.md`, `docs/design/frontend-guidelines.md`, `docs/features/frontend-ownership.json`, `web/frontend/scripts/lint-ui-rules.mjs`, and `web/frontend/tests/ui-smoke.spec.ts` | Agent-facing launcher UI guidance plus static, formatting, accessibility, ownership, and mocked-route browser checks. Feature-specific UI behavior remains owned by the relevant product feature spec. | `FR-LAUNCHER-002`, `FR-LAUNCHER-007`, `FR-LAUNCHER-009` |

## Algorithms And Ordering

1. Route launcher requests through access control and dashboard authentication
   before handler-specific parsing.
2. For config and model writes, decode JSON, normalize account transport fields,
   validate exact alias names/base models, reject account-router override keys,
   and require model-router terminals to target aliases. Preserve stored secure
   strings, keep account routers model-free, atomically validate an optional
   `set_as_default` with the entry mutation, and store default `account_ref`
   plus alias-valued `model_name` in the same compare-and-swap save. Require the
   model-list read revision before interpreting an update or delete index. Then
   write config and apply runtime log-level changes.
3. Serialize full-config writes, generic tool-state writes, and
   workflow-specific config-coupled operations through the handler mutation
   boundary. Generic tool state and workflow settings atomically load an
   update-safe public-plus-security snapshot under the config-path advisory
   lock, compare the submitted revision when supplied, apply only their owned
   fields, validate their scoped values, and then compare and save that exact
   generation atomically under the same advisory lock shared by all config
   saves. Hash both public JSON and security-sidecar bytes into the opaque
   generation. Full PUT/PATCH
   also compare-and-swap the generation they loaded; reset holds the same lock
   for its complete backup-and-replace transaction. For workflow Run and
   Retry, acquire the workflow mutation lock first, repeat the complete
   readiness fence, then acquire the config-path advisory lock and reject any
   generation change before holding both locks through exact compatibility
   validation and durable root-run creation.
4. For OAuth requests, create bounded flow state, redirect or poll provider
   login, exchange callback state for credentials, then persist or clear
   provider auth records. Token-backed account providers such as GitHub Copilot
   validate the submitted token before storage and do not create a default
   model alias or runnable model entry. The launcher accounts
   page renders stored credentials and account-router aliases in one account
   card grid while keeping new account onboarding behind an explicit add-account
   surface. When an OpenAI OAuth account name is omitted, the saved credential ID
   uses the email local-part as the provider-scoped suffix. OpenAI usage-limit
   lookup uses Picoclaw credential records instead of Codex CLI config and maps
   an upstream earned-reset count to the matching account summary without
   exposing credentials or making the read path consume a reset.
5. For model fetch requests, resolve stored model auth when a model index is
   supplied, prefer explicit request credentials otherwise, route OpenAI
   OAuth/token fetches to the ChatGPT Codex model list endpoint with a
   Codex-compatible `client_version` new enough to surface GPT-5.6 subscription
   models, expose GitHub Copilot static metadata models for Copilot requests,
   and keep regular API-key fetches on the OpenAI-compatible `/models` path.
6. For gateway lifecycle requests, inspect current process state first, execute
   start/stop/restart transitions only when valid, and retain log buffers for
   status and diagnostics responses. Include the complete enabled MCP config,
   including custom-header values, in the internal restart signature while
   representing external bearer/OAuth token changes only through their
   nonsecret revision so token bytes never enter the signature.
7. Register feature-specific MCP handlers through the authenticated launcher
   router, expose their dedicated route in the shared navigation shell, remove
   their fields from the generic config editor, and cancel unfinished browser
   login flows when the handler shuts down.
8. Return JSON for success and error paths with status codes that match
   validation, auth, not-found, conflict, or internal failure classes.

## Cross-Feature Behavior

Launcher surfaces expose other features but do not define them. Model management
feeds agent conversations. Gateway endpoints control chat-channel runtime.
Session endpoints are owned by session memory. Thread endpoints and
thread-specific UI are owned by threads, while launcher management still owns
shared authenticated dashboard layout and routing shell components.
Workflow HTTP endpoints and dashboard routes are exposed through the launcher
router and shared shell, including stateless structured-authoring helpers, while
workflow definition, projection, authoring, run, graph, cancel, retry, and event
semantics remain owned by the workflows and security-isolation features.
Reviewed trigger routes keep their process-local signing key and bounded
one-use bookkeeping on the authenticated launcher handler; token binding,
admission, and execution semantics remain owned by those same feature specs.
Event operator proxy endpoints and the Events dashboard route are likewise
registered through the shared launcher router and navigation shell and inherit
dashboard authentication. Event inspection, payload, replay, and live-gateway
authorization semantics remain owned by event automation and security
isolation.
Git workspace config fields, API routes, sidebar navigation, and dashboard entry
points are exposed through shared launcher surfaces, while workspace allocation,
inventory, cleanup, drop, and retention semantics are owned by the git
workspaces feature.
MCP API registration, sidebar navigation, gateway restart signaling, and shared
form accessibility compose through launcher-owned surfaces. Server lifecycle,
tool discovery, credentials, probes, and OAuth protocol behavior remain owned by
the MCP integration and security-isolation features.

## Failure And Edge Cases

- GET logout is rejected; logout requires POST JSON.
- Login is rate-limited per client IP.
- OAuth flow IDs expire and unknown states fail.
- Config update preserves model API-key payloads and keeps existing model
  secrets when equivalent provider/model/API-base entries are renamed.
- A concurrent full-config or workflow-scoped write from the same or another
  process that changes the config revision before a workflow settings
  compare-and-swap receives a conflict; the scoped request does not write.
- Model update preserves existing secrets unless explicitly changed and avoids
  persisting blank secret placeholders for models with no key.
- Account-router add/update rejects unknown, router, or ambiguous account
  references as validation failures and does not persist API keys on router
  entries.
- Chat requires an enabled `account_ref` and an exact alias/model-router
  selection. Missing aliases surface `no model configured`; fetched or raw
  upstream IDs are never implicit selections.
- Model-router add/update rejects non-alias terminals and stores the router graph
  in `model_routers[]` instead of `model_list[]`.
- Model-alias add/update rejects blank base models, duplicate names, unknown
  override accounts, and any override keyed by an account router.
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
- Generic config saves do not overwrite MCP settings or credentials managed on
  the dedicated page.
- An MCP server, discovery, custom-header, or credential-revision change reports
  a required gateway restart. Custom-header config participates in the internal
  hash, while external bearer/OAuth token bytes do not and neither value is
  returned by the signature comparison.
- Shared validation messages are announced to assistive technology, and secret
  reveal controls remain keyboard reachable with an explicit accessible name.

## Acceptance Evidence

| Requirement IDs                      | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-LAUNCHER-001`                    | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [web/backend/api/workflow_jobs_editor_test.go](../../web/backend/api/workflow_jobs_editor_test.go), [web/backend/middleware/access_control_test.go](../../web/backend/middleware/access_control_test.go)                                                                                                                                                                                                                         |
| `FR-LAUNCHER-002`, `FR-LAUNCHER-007` | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go)                                                                                                                                                                                                                                                                                                                                                                                               |
| `FR-LAUNCHER-003`                    | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/backend/api/model_aliases_test.go](../../web/backend/api/model_aliases_test.go), [web/backend/api/model_mutation_default_test.go](../../web/backend/api/model_mutation_default_test.go), [web/backend/api/model_update_revision_test.go](../../web/backend/api/model_update_revision_test.go), [web/backend/api/model_status_test.go](../../web/backend/api/model_status_test.go), [web/backend/api/model_catalog_test.go](../../web/backend/api/model_catalog_test.go), [web/frontend/src/api/models.test.ts](../../web/frontend/src/api/models.test.ts), [web/frontend/src/components/models/model-card.test.tsx](../../web/frontend/src/components/models/model-card.test.tsx), [web/frontend/src/components/models/model-mutation-default.test.tsx](../../web/frontend/src/components/models/model-mutation-default.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-004`                    | [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [cmd/picoclaw/internal/auth](../../cmd/picoclaw/internal/auth)                                                                                                                                                                                                         |
| `FR-LAUNCHER-005`, `FR-LAUNCHER-006` | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/startup_test.go](../../web/backend/api/startup_test.go), [web/backend/api/version_test.go](../../web/backend/api/version_test.go)                                                                                                                                                                                                                                                                                                       |
| `FR-LAUNCHER-008`                    | [web/backend/api/models_test.go](../../web/backend/api/models_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `FR-LAUNCHER-009`                    | [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/src/components/ui/button.tsx](../../web/frontend/src/components/ui/button.tsx), [web/frontend/src/index.css](../../web/frontend/src/index.css), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/scripts/lint-ui-rules.mjs](../../web/frontend/scripts/lint-ui-rules.mjs)                                                                                                                                                       |
| `FR-LAUNCHER-010`                    | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/src/components/agent/mcp/mcp-server-card.test.tsx](../../web/frontend/src/components/agent/mcp/mcp-server-card.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-011`                    | [pkg/config/mutation.go](../../pkg/config/mutation.go), [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/workflows/mutation_lock_test.go](../../pkg/workflows/mutation_lock_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_writer_cas_test.go](../../web/backend/api/config_writer_cas_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go), [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/workflow_settings_test.go](../../web/backend/api/workflow_settings_test.go), [web/backend/api/workflow_templates_test.go](../../web/backend/api/workflow_templates_test.go), [web/backend/api/workflow_publish_test.go](../../web/backend/api/workflow_publish_test.go), [web/backend/api/workflow_dependencies.go](../../web/backend/api/workflow_dependencies.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_run_readiness_test.go](../../web/backend/api/workflow_run_readiness_test.go), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-012`                    | [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go), [web/backend/api/agent_activity_test.go](../../web/backend/api/agent_activity_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [pkg/gateway/agent_activity_test.go](../../pkg/gateway/agent_activity_test.go), [web/frontend/src/components/agent/agents/agent-capabilities-panel.test.tsx](../../web/frontend/src/components/agent/agents/agent-capabilities-panel.test.tsx), [web/frontend/src/components/agent/agents/agent-activity-panel.test.tsx](../../web/frontend/src/components/agent/agents/agent-activity-panel.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-013`                    | [web/backend/api/reviews.go](../../web/backend/api/reviews.go), [web/backend/api/reviews_test.go](../../web/backend/api/reviews_test.go), [web/backend/api/gateway.go](../../web/backend/api/gateway.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [pkg/gateway/event_review_readiness_test.go](../../pkg/gateway/event_review_readiness_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go) |

## Implementation Anchors

- [web/backend/api/router.go](../../web/backend/api/router.go)
- [web/backend/api/gateway.go](../../web/backend/api/gateway.go)
- [web/backend/api/agent_capabilities.go](../../web/backend/api/agent_capabilities.go)
- [web/backend/api/agent_activity.go](../../web/backend/api/agent_activity.go)
- [web/backend/api/reviews.go](../../web/backend/api/reviews.go)
- [pkg/config/mutation.go](../../pkg/config/mutation.go)
- [web/backend/middleware](../../web/backend/middleware)
- [web/backend/launcherconfig](../../web/backend/launcherconfig)
- [web/frontend/src/components/app-sidebar.tsx](../../web/frontend/src/components/app-sidebar.tsx)
- [web/frontend/src/components/shared-form.tsx](../../web/frontend/src/components/shared-form.tsx)
