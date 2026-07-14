# Launcher Management UX

## Feature ID

`FR-LAUNCHER`

## Behavior Summary

The web launcher provides authenticated browser management for configuration,
models, OAuth credentials, tools, skills, sessions, gateway process lifecycle,
startup behavior, update, and runtime version metadata.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-LAUNCHER-001` | MUST | Dashboard access requires password setup/login and an HttpOnly session cookie; local bootstrap auto-login is loopback-only. | Browser management must be gated. |
| `FR-LAUNCHER-002` | MUST | Config GET/PUT/PATCH/reset preserves schema defaults, secure string semantics, and runtime log-level application. | Launcher config editing must not corrupt config. |
| `FR-LAUNCHER-003` | MUST | Model management lists, adds, updates, deletes, tests, fetches, and sets default model entries without exposing stored secret values. | Users need safe model administration. |
| `FR-LAUNCHER-004` | MUST | OAuth login flow creates, polls, completes, and logs out provider credentials through bounded flow state. | OAuth-backed providers need browser setup. |
| `FR-LAUNCHER-005` | MUST | Gateway lifecycle endpoints report status/logs and start/stop/restart managed gateway processes without losing log diagnostics. | Desktop users need process control. |
| `FR-LAUNCHER-006` | MUST | Startup, launcher config, update, and version endpoints report or mutate only their documented system settings. | System management must be narrow and auditable. |
| `FR-LAUNCHER-007` | SHOULD | API errors return JSON responses with actionable messages and appropriate status codes. | Frontend UX needs consistent failures. |

## Auxiliary Interfaces

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

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `/api/auth*`, `/api/config*`, `/api/models*`, `/api/oauth*`, `/api/system*`, `/api/update`, `/api/weixin*`, `/api/wecom*` | Authenticated launcher management endpoints. | `FR-LAUNCHER-001` through `FR-LAUNCHER-007` |
| CLI | `picoclaw auth`, `picoclaw config`, `picoclaw onboard`, `picoclaw migrate` | Non-browser setup, auth, and migration helpers. | `FR-LAUNCHER-002`, `FR-LAUNCHER-004` |
| Config | Launcher config file beside app config | Port/public/access options and dashboard auth migration. | `FR-LAUNCHER-001`, `FR-LAUNCHER-006` |

## Cross-Feature Behavior

Launcher surfaces expose other features but do not define them. Model management
feeds agent conversations. Gateway endpoints control chat-channel runtime.
Session endpoints are owned by session memory.

## Failure And Edge Cases

- GET logout is rejected; logout requires POST JSON.
- Login is rate-limited per client IP.
- OAuth flow IDs expire and unknown states fail.
- Model update preserves existing secrets unless explicitly changed.
- Public launcher access obeys configured host/CIDR policy.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-LAUNCHER-001` | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go), [web/backend/middleware/access_control_test.go](../../web/backend/middleware/access_control_test.go) |
| `FR-LAUNCHER-002`, `FR-LAUNCHER-007` | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go) |
| `FR-LAUNCHER-003` | [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/backend/api/model_status_test.go](../../web/backend/api/model_status_test.go), [web/backend/api/model_catalog_test.go](../../web/backend/api/model_catalog_test.go) |
| `FR-LAUNCHER-004` | [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [cmd/picoclaw/internal/auth](../../cmd/picoclaw/internal/auth) |
| `FR-LAUNCHER-005`, `FR-LAUNCHER-006` | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/startup_test.go](../../web/backend/api/startup_test.go), [web/backend/api/version_test.go](../../web/backend/api/version_test.go) |

## Implementation Anchors

- [web/backend/api/router.go](../../web/backend/api/router.go)
- [web/backend/middleware](../../web/backend/middleware)
- [web/backend/launcherconfig](../../web/backend/launcherconfig)
