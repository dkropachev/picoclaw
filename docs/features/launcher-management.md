# Launcher Management UX

## Feature ID

`FR-LAUNCHER`

## Behavior Summary

The web launcher provides authenticated browser management for configuration,
models, OAuth credentials, tools, skills, MCP servers, sessions, gateway
process lifecycle, startup behavior, updates, and runtime metadata. Scoped
configuration operations share one revision-fenced mutation boundary with the
generic config surface.

Pull-request work has one canonical launcher route family rooted at
`/pull-requests`.
The launcher proxies the unified `/api/pr-workspaces` tree to the managed
gateway, replaces browser authority with the process bearer, and exposes
separate revision-fenced `/api/pr-lifecycle/workflow-configurations` and
`/api/pr-lifecycle/repository-assignments` ownership boundaries.
Review, implementation, corrections, nudges, gates, deferred work, and their
separate publication actions render from one `prw_` aggregate. The former
`/reviews`, `/api/reviews`, and `/api/pr-development` surfaces are removed
and are not redirects.

The launcher can start the gateway in limited mode when no model alias is
selected, so non-model management remains available. Model-dependent execution
continues to fail locally until a concrete model is configured.

## Reconstruction Notes

- Similarity target: authenticated launcher APIs and a responsive management UI
  that replace browser credentials with narrow local-runtime authority.
- Core types/functions: API handler/router, dashboard auth middleware, config
  mutation coordinator, gateway process manager, PR-workspace proxy, lifecycle
  Workflow-configuration and repository-assignment handlers, typed frontend clients, and the pull-request route.
- Runtime ordering: authenticate, canonicalize and bound the request, reject
  cross-site mutation, load one exact config or PID generation, call the narrow
  owner, validate and bound the response, then return non-cacheable JSON.
- Non-obvious constraints: launcher reads never attach to or repair process
  metadata; browser credentials are never forwarded; scoped config writes use
  the public-plus-security revision; workspace mutations use workspace version,
  request ID, and provider head fences; removed PR routes have no compatibility
  handler.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-LAUNCHER-001` | MUST   | Dashboard access requires password setup/login and an HttpOnly session cookie; local bootstrap auto-login is loopback-only.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Browser management must be gated.                                                                                                                              |
| `FR-LAUNCHER-002` | MUST   | Config GET/PUT/PATCH/reset preserves schema defaults, secure string semantics, model API-key payloads, existing model secrets across equivalent model alias changes, and runtime log-level application.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Launcher config editing must not corrupt config or credentials.                                                                                                |
| `FR-LAUNCHER-003` | MUST   | Account management and model-alias management are separate. `model_list[]` and credential records describe concrete provider accounts; a disabled persisted account does not suppress the enabled virtual account projected from its live credential. `model_aliases[]` maps exact user-facing names to a default concrete model ID, optional overrides keyed only by concrete accounts, and optional `disabled_accounts` where that alias must not run. The Models UI fetches every concrete account's advertised models, deduplicates the union for the default-model selector, scopes each account-override selector to models advertised by its selected account, lets users type to filter every alias model selector, shows account availability for every option, and edits aliases in a modal; when no enabled concrete account exists, model selectors and override creation remain disabled while the modal explains how to restore an account. Provider-account management and a global runtime-model selection are not duplicated there. Index-addressed model and alias updates and deletes require the opaque revision returned by the model-list read and reject stale revisions before interpreting an index. Account routers remain model-free, skip explicit disabled alias/account pairs, model-router terminals target aliases only, and chat uses independent account and alias selectors. No management path invents or persists a provider-default model. | Users need safe account and model administration without coupling an account graph to a model, overwriting a concurrent edit, deleting a shifted row, or silently selecting a provider model. |
| `FR-LAUNCHER-004` | MUST   | OAuth login flow creates, polls, completes, and logs out provider credentials through bounded flow state; token login supports registered providers that require pasted credentials, including `github-copilot`, plus every creatable provider from the backend model provider catalog such as DeepSeek and Google Gemini; login persists provider credentials only and must not create default model entries, runnable model entries, or account-router blocks; the accounts UI lists only registered provider accounts, exposes a separate onboarding surface that can assign named credential IDs, infers a missing OpenAI account name from the OAuth email local-part, displays OpenAI account headers as provider plus auth method and subscription type when known, displays GitHub Copilot token-backed accounts with provider labels/icons, and displays sanitized ChatGPT Codex and GitHub Copilot account usage limits by reading Picoclaw credentials and calling provider-specific usage APIs without exposing raw upstream error bodies or CLI config state; when Codex reports earned usage-limit reset availability, the OpenAI account summary shows the authoritative available count including zero and indicates that an available reset is used automatically for eligible exhaustion. | OAuth-backed providers need browser setup without presenting unregistered accounts as active entries, creating default models, or duplicating accounts as models. |
| `FR-LAUNCHER-005` | MUST   | Gateway lifecycle endpoints report status/logs and start/stop/restart managed gateway processes without losing log diagnostics. A missing global model alias does not block process startup: the managed child starts with `--allow-empty`, remains available in limited mode, and rejects model-dependent execution locally until an execution context supplies a configured alias. Gateway status separately reports whether model setup is required, and the dashboard displays that notice independently of process state until the predefined `chat` alias has an explicit mapping.                                                                                                                                                                                                                                                                                                                                                                                                        | Process availability and model selection are separate concerns; desktop users still need process control and a visible configuration path before a model is selected. |
| `FR-LAUNCHER-006` | MUST   | Startup, launcher config, update, and version endpoints report or mutate only their documented system settings.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | System management must be narrow and auditable.                                                                                                                |
| `FR-LAUNCHER-007` | SHOULD | API errors return JSON responses with actionable messages and appropriate status codes.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Frontend UX needs consistent failures.                                                                                                                         |
| `FR-LAUNCHER-008` | MUST   | Model fetch distinguishes regular OpenAI API-key listings from OpenAI OAuth/token Codex subscription listings; credential-backed OpenAI fetches use the stored credential, account headers, and the current minimum Codex-compatible client version required for GPT-5.6 model visibility against the ChatGPT Codex models endpoint, while API-key fetches continue to use the OpenAI-compatible `/models` endpoint; GitHub Copilot model fetch exposes static metadata/common models without a credential, uses direct Copilot model listing with the stored token for credential-backed fetches, and credential-backed status checks validate stored credentials instead of probing the local bridge.                                                                                                                                                                                                                                                             | Subscription and API-key accounts have different upstream auth and must not fail or mix credentials.                                                           |
| `FR-LAUNCHER-009` | SHOULD | Shared launcher layout, theme, and primitive controls remain responsive, token-driven, keyboard-accessible, and free of clipped controls across desktop and narrow mobile widths. Destructive controls use paired background/foreground theme tokens with sufficient contrast in light and dark modes instead of translucent destructive text treatments that fail automated accessibility checks.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Dashboard navigation and process controls must stay usable while visual styling evolves.                                                                       |
| `FR-LAUNCHER-010` | MUST   | The authenticated launcher composition registers the feature-owned MCP management and OAuth callback routes, exposes a dedicated Agent → MCP navigation entry, and removes MCP editing from the generic config form. Gateway restart detection includes enabled MCP discovery, server transport, custom-header, and nonsecret auth-revision changes. Shared forms announce validation errors and provide keyboard-accessible, labeled secret visibility controls.                                                                                                                                                                                                                                                                                                                                                                                                                                                        | MCP management must be easy to find, must not conflict with generic config saves, and must clearly apply runtime-relevant changes without weakening shared form accessibility. |
| `FR-LAUNCHER-011` | MUST   | Full-config PUT/PATCH/reset, generic tool-state writes, agent policy mutations, workflow-specific settings, template-install, publish, and workflow Run/Retry admission are serialized by one handler mutation boundary. Every cooperating `SaveConfig` call also holds a config-path advisory process/file lock, with the opaque generation covering both public JSON and the security sidecar. Full-config PUT/PATCH, generic tool-state, agent policy, and workflow settings mutations load an update-safe snapshot and perform final compare-and-swap saves against that exact generation; reset holds the lock across backup, secret preservation, and replacement. Stable scoped reads derive their opaque revision from the same snapshot without migration, backup, or save side effects. Agent responses derive restart effects from that captured config and a read-only in-memory gateway snapshot without discovering processes, attaching to them, or sanitizing PID metadata. Workflow Run and Retry reacquire that same advisory lock after their final readiness fence, compare the current public-plus-security generation with the admitted generation, and retain the lock through exact compatibility checking and durable root-run creation. The authenticated launcher registers agent management routes and navigation, and the gateway restart signature includes the complete ordered agent policy while preserving nil-versus-empty distinctions. | Scoped or merge-patch management must not return values or effects from one config generation with another generation's revision, lose a concurrent secret-only update, overwrite a mutation from another launcher or gateway process, hide an unapplied agent policy change, mutate gateway process metadata during an agent read, or admit execution from one generation while another process publishes a replacement before the run exists. |
| `FR-LAUNCHER-012` | MUST   | The authenticated launcher registers agent capability and activity routes without replacing existing management surfaces. Capability mutation holds the shared handler and advisory config boundaries through its final composite config/file fence and atomic workspace write, while gateway restart comparison combines the filesystem-pure config signature with only runtime-relevant `AGENT.md` frontmatter semantics. Activity is read-only: the gateway records a concrete numeric address from the listener that actually opened, including a single-stack localhost fallback; the launcher peeks PID authority without attachment, cleanup, or migration, rejects hostname and wildcard authority, validates the numeric target as loopback or a literal local-interface address, injects the process bearer into one exact bounded no-proxy/no-redirect request, forwards no browser credentials or ambient headers, and strictly reprojects the response. | Workspace policy must not race config ownership, prose-only edits must not spuriously require restart, and a browser activity view must not mutate process metadata or leak runtime bearer authority. |
| `FR-LAUNCHER-021` | MUST | The authenticated shared shell owns the canonical `/pull-requests` route family. | Pull requests has Work, Workflow configurations, Repository assignments, and Lifecycle settings children. Canonical destinations are `/pull-requests`, `/pull-requests/:workspaceID`, `/pull-requests/workflow-configurations`, `/pull-requests/workflow-configurations/:configurationID`, `/pull-requests/repository-assignments`, and `/pull-requests/settings`; each settings tab, gate modal, and discard modal has URL-owned state. Retired Gate V2 paths and query-driven views have no compatibility UI. Navigation preserves a validated workspace origin, blocks dirty-draft exits behind one URL-backed discard decision, and performs no model, workflow, Git, provider, or publication action. | Invalid or repeated state canonicalizes before rendering; refresh and Back/Forward reopen the exact list, editor, flow, tab, or modal. | First-class routes prevent hidden tab state and ambiguous same-path navigation. |
| `FR-LAUNCHER-022` | MUST | The launcher renders the unified PR workspace and Gate V3 configuration surfaces. | The workspace renders generic Gate forms and submits only `field-values`; the workflow configuration list/editor shows the Review and Implementation flow graph, static `(workflow-ref, gate-ref)`, workflow `default-action`, exact configuration override, effective `human`, `ai`, `deterministic`, or `workflow` action, and that configuration's deferred-issue mode. AI overrides expose ephemeral, private-snapshot, and originating-snapshot profiles; fixed history, cache, agent, and tool properties are read-only with accessible explanations, and an inherited AI default remains fully visible read-only. Source mode is offered only for a catalog Gate capable of supplying one source-bearing finding. Repository assignment is a separate page backed by a safe name/deferred-policy summary that exposes no bindings, actions, prompts, or Gate catalog. Global Nudging and Scope grades remain separate settings tabs. Both scoped saves use the same full-config revision fence, preserve the other surface's fields, and never execute a gate. Gateway and deferred-policy restart effects are distinct, so unrelated pending lifecycle edits do not disable issue controls. The shared draft survives configuration-route changes and explicit discard restores the server snapshot. | Malformed static forms, invalid kebab IDs or refs, partial action overrides, unknown agents, unavailable or ambiguous originating provenance, stale revisions, assigned-configuration deletion, retired Gate V2 configuration and result fields, unsafe URLs, and unknown provider outcomes fail closed. | The UI must expose one generic gate contract without leaking private workflow state or giving configuration reads execution authority. |

## Data And State Model

The launcher owns an HttpOnly dashboard session, process-local login throttles,
a shared config mutation lock, public-plus-security config revisions, and
managed gateway PID metadata. It does not own PR-workspace state.

PR browser state is deliberately shallow. Workspaces use
`/pull-requests/prw_...`; named Workflow configurations use
`/pull-requests/workflow-configurations` and `/pull-requests/workflow-configurations/:configurationID`;
repository assignments use `/pull-requests/repository-assignments`;
global lifecycle settings use `/pull-requests/settings`. The configuration
flow, settings tab, Gate editor, active
discard decision, and optional validated workspace origin are the only public
query state. A direct discard URL remains a stable modal surface and closes to
its owner when no blocked navigation is pending. Filters, cursors, DTOs, draft
bodies, request IDs, conflicts, prompts, private gate subjects, and provider
reconciliation evidence remain memory-only.
The gateway's unified aggregate is authoritative after every mutation.

The workflow configuration response contains named workflow configurations, a
default configuration, atomic gate-action bindings, independent
review/completion nudge bounds, scope-size thresholds, the resolved
Gate catalog, the normalized
Review and Implementation flow graph and its content revision, a catalog
digest, the config revision, and the `restart-required` effect. The browser
renders that graph directly; it does not
carry a second hard-coded PR lifecycle topology. The repository-assignment
response separately contains exact assignments and name/deferred-policy-only
configuration summaries from the same full-config revision. Each workflow diagram lays out
only the nodes present in an active topological band, so a completed route does
not reserve an empty column in later bands. Responsive measured connectors keep
the exact source, target, branch label, loop, and merge relationships visible as
those bands reflow, while gate nodes remain keyboard-operable controls that open
the gate editor dialog. Plain compact cards represent actions without a repeated
type label; editable gates are full-card controls labeled by gate format, and
locked safeguards retain a distinct non-interactive treatment. Adjacent branch
families use separate curved ports so unrelated routes do not form false visual
junctions. Gutter routes reserve exterior source ports and distinct launch
shelves, and use a background underlay to keep neighboring connectors visually
separate. Backward edges are connected SVG return rails from the source through
an exterior gutter to the earlier target, rather than detached return callouts;
an explicit branch label stays on the rail, while an unlabeled return adds no
visible text. Return rails are remeasured with the responsive bands, preserve
their workflow edge mode, and expose one semantic "returns to" relationship to
assistive technology without duplicating the visual connector.

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
Owns: CODE web/frontend/src/components/gateway-setup-notice.tsx
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

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| HTTP | `/api/pr-workspaces*` | Authenticated, canonical, bounded proxy for the matching protected gateway tree; mutations require same-origin provenance and JSON. | `FR-LAUNCHER-021`, `FR-LAUNCHER-022` |
| HTTP | `GET/PUT /api/pr-lifecycle/workflow-configurations` | Read or revision-fenced replace workflow configurations, default selection, nudge bounds, and scope thresholds while preserving assignments. | `FR-LAUNCHER-011`, `FR-LAUNCHER-022` |
| HTTP | `GET/PUT /api/pr-lifecycle/repository-assignments` | Read safe configuration summaries or revision-fenced replace only repository assignments. | `FR-LAUNCHER-011`, `FR-LAUNCHER-022` |
| UI | `/pull-requests`, `/pull-requests/:workspaceID`, `/pull-requests/workflow-configurations*`, `/pull-requests/repository-assignments`, `/pull-requests/settings` | Portfolio, one aggregate workspace, named workflow configuration pages, separate repository assignments, URL-owned Gate/discard modal state, and tabbed lifecycle settings under one navigation group. | `FR-LAUNCHER-009`, `FR-LAUNCHER-021`, `FR-LAUNCHER-022` |
| HTTP | `/api/config*`, `/api/models*`, `/api/oauth*`, `/api/system*`, `/api/agents*`, `/api/workflows*` | Existing authenticated management surfaces retain their scoped contracts and shared mutation fencing. | `FR-LAUNCHER-001` through `FR-LAUNCHER-012` |

## Algorithms And Ordering

For a PR-workspace request, the launcher first validates the exact path,
escaping, query, method, content type, encoding, body bound, and same-origin
provenance. It then peeks—but never attaches to—the managed process record,
requires a numeric local address and bearer, disables redirects and environment
proxies, replaces browser credentials with the process bearer, applies an
operation-specific timeout, and accepts only bounded JSON. Provider-facing
locations are reprojected only when safe.

For a lifecycle settings write, the launcher strictly decodes one complete
catalog, validates Gate bindings and atomic action overrides, acquires the shared
mutation boundary, reloads the exact update-safe config, compares the supplied
revision, saves by compare-and-swap, and returns the new revision and restart
effect. It never executes a gate while saving configuration.

The frontend strictly validates workspace aggregates and publication URLs,
keeps mutation drafts in memory, sends fresh random request IDs, and replaces
local state only with an authoritative equal-or-newer aggregate. Unknown
publication outcomes offer reconciliation, never blind retry.

## Cross-Feature Behavior

Durable External Event Automation owns PR-workspace lifecycle state, gateway
runtime routes, provider adapters, and PR frontend feature components. Workflows
owns static gate declarations, `gate/exec`, action resolution, and private
continuation. Security owns
dashboard authentication, bearer replacement, config-secret handling, and
network confinement. Git Workspaces owns pinned local candidates and branch
push fences. The launcher composes these surfaces but gains no model, Git,
provider, publication, or merge authority from navigation or configuration.

## Failure And Edge Cases

Unauthenticated requests fail before config or process access. Noncanonical
paths, repeated/unknown query keys, aliases for removed PR routes, cross-site
mutations, missing or streaming bodies, unsafe encodings, oversized JSON,
unknown fields, stale config/workspace/head revisions, unavailable runtime
authority, redirects, non-local targets, malformed upstream JSON, and unsafe
external URLs fail closed through bounded public errors.

A gateway outage leaves configuration and other launcher management available.
A PR mutation conflict retains the user's draft and requires refresh. An unknown
provider effect retains reconciliation state. Narrow layouts preserve all
controls without horizontal page overflow. No browser read, filter, selection,
or settings navigation starts a model, workflow, checkout, provider effect, or
publication.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
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
| `FR-LAUNCHER-021`                    | [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/src/routes/-pull-requests-route.test.tsx](../../web/frontend/src/routes/-pull-requests-route.test.tsx), [web/frontend/src/routes/-pull-requests.test.ts](../../web/frontend/src/routes/-pull-requests.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-022`                    | [web/frontend/src/api/pr-workspaces.test.ts](../../web/frontend/src/api/pr-workspaces.test.ts), [web/frontend/src/api/pr-lifecycle-workflow-configurations.test.ts](../../web/frontend/src/api/pr-lifecycle-workflow-configurations.test.ts), [web/frontend/src/components/pr-workspaces/pr-workspace-pages.test.tsx](../../web/frontend/src/components/pr-workspaces/pr-workspace-pages.test.tsx), [web/frontend/src/routes/-pull-requests-route.test.tsx](../../web/frontend/src/routes/-pull-requests-route.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [web/backend/api/router.go](../../web/backend/api/router.go)
- [web/backend/api/pr_workspaces.go](../../web/backend/api/pr_workspaces.go)
- [web/backend/api/pr_workspace_proxy.go](../../web/backend/api/pr_workspace_proxy.go)
- [web/backend/api/pr_lifecycle_workflow_configurations.go](../../web/backend/api/pr_lifecycle_workflow_configurations.go)
- [web/backend/api/gateway.go](../../web/backend/api/gateway.go)
- [web/backend/main.go](../../web/backend/main.go)
- [web/backend/middleware](../../web/backend/middleware)
- [pkg/config/mutation.go](../../pkg/config/mutation.go)
- [web/frontend/src/components/app-sidebar.tsx](../../web/frontend/src/components/app-sidebar.tsx)
- [web/frontend/src/routes/pull-requests.tsx](../../web/frontend/src/routes/pull-requests.tsx)
- [web/frontend/src/routes/pull-requests_.workflow-configurations.tsx](../../web/frontend/src/routes/pull-requests_.workflow-configurations.tsx)
- [web/frontend/src/routes/pull-requests_.workflow-configurations.$configurationID.tsx](../../web/frontend/src/routes/pull-requests_.workflow-configurations.$configurationID.tsx)
- [web/frontend/src/routes/pull-requests_.repository-assignments.tsx](../../web/frontend/src/routes/pull-requests_.repository-assignments.tsx)
- [web/frontend/src/routes/pull-requests_.settings.tsx](../../web/frontend/src/routes/pull-requests_.settings.tsx)
- [web/frontend/src/routes/pull-requests_.$workspaceID.tsx](../../web/frontend/src/routes/pull-requests_.$workspaceID.tsx)
- [web/frontend/src/api/pr-workspaces.ts](../../web/frontend/src/api/pr-workspaces.ts)
- [web/frontend/src/api/pr-lifecycle-workflow-configurations.ts](../../web/frontend/src/api/pr-lifecycle-workflow-configurations.ts)
- [web/frontend/src/api/pr-lifecycle-repository-assignments.ts](../../web/frontend/src/api/pr-lifecycle-repository-assignments.ts)
- [web/frontend/src/components/pr-workspaces](../../web/frontend/src/components/pr-workspaces)
