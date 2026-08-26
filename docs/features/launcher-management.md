# Launcher Management UX

## Feature ID

`FR-LAUNCHER`

## Behavior Summary

The web launcher provides authenticated browser management for configuration,
models, OAuth credentials, tools, skills, MCP servers, sessions, gateway
process lifecycle, startup behavior, updates, and runtime metadata. Scoped
configuration operations share one revision-fenced mutation boundary with the
generic config surface.

Development work has one canonical launcher route family rooted at
`/development`. Intake makes **Implement feature** and **Pick up PR** mutually
exclusive: feature work accepts one GitHub issue or one brief plus configured
repository, while PR pickup accepts one existing pull request. The launcher
proxies `/api/development-workspaces` to the managed gateway, replaces browser
authority with the process bearer, and renders review, implementation, gates,
validation, code evidence, chat, and publication from one `devw_` aggregate.
Pre-v20 pull-request workspace URLs, APIs, and IDs have no redirect, adapter,
dual read, or translation.

Actionable development attention accumulates in the canonical `/notifications`
inbox. The installed launcher PWA can receive privacy-minimal Web Push for new
critical/high items after an explicit per-device opt-in; the inbox remains the
authoritative fallback when push is unsupported or disabled.

The launcher can start the gateway in limited mode when no model alias is
selected, so non-model management remains available. Model-dependent execution
continues to fail locally until a concrete model is configured.

## Reconstruction Notes

- Similarity target: authenticated launcher APIs and a responsive management UI
  that replace browser credentials with narrow local-runtime authority.
- Core types/functions: API handler/router, dashboard auth middleware, config
  mutation coordinator, gateway process manager, development-workspace and
  notification proxies, lifecycle configuration handlers, typed frontend
  clients, PWA service worker, and development/notification routes.
- Runtime ordering: authenticate, canonicalize and bound the request, reject
  cross-site mutation, load one exact config or PID generation, call the narrow
  owner, validate and bound the response, then return non-cacheable JSON.
- Non-obvious constraints: launcher reads never attach to or repair process
  metadata; browser credentials are never forwarded; scoped config writes use
  the public-plus-security revision; workspace mutations use workspace version,
  request ID, and provider head fences; push permission is user initiated;
  removed development predecessors have no compatibility handler.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-LAUNCHER-001` | MUST   | Dashboard access requires password setup/login and an HttpOnly session cookie; local bootstrap auto-login is loopback-only.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Browser management must be gated.                                                                                                                              |
| `FR-LAUNCHER-002` | MUST   | Config GET/PUT/PATCH/reset preserves schema defaults, secure string semantics, model API-key payloads, existing model secrets across equivalent model alias changes, and runtime log-level application.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Launcher config editing must not corrupt config or credentials.                                                                                                |
| `FR-LAUNCHER-003` | MUST   | Account management and model-alias management are separate. `model_list[]` and credential records describe concrete provider accounts; a disabled persisted account does not suppress the enabled virtual account projected from its live credential. `model_aliases[]` maps exact user-facing names to a default concrete model ID, optional overrides keyed only by concrete accounts, and optional `disabled_accounts` where that alias must not run. The Models UI fetches every concrete account's advertised models, deduplicates the union for both the default-model and account-override selectors, retains configured values missing from that union, lets users type to filter every alias model selector, and annotates each option with its availability for all accounts or the selected override account; when no enabled concrete account exists, model selectors and override creation remain disabled while the modal explains how to restore an account. Provider-account management and a global runtime-model selection are not duplicated there. The safe model-list projection includes optional input/output price, subscription, and subscription-equivalent-model metadata for authenticated consumers without exposing credentials. Index-addressed model and alias updates and deletes require the opaque revision returned by the model-list read and reject stale revisions before interpreting an index. Account routers remain model-free, skip explicit disabled alias/account pairs, model-router terminals target aliases only, and chat uses independent account and alias selectors. No management path invents or persists a provider-default model. | Users need safe account and model administration without coupling an account graph to a model, overwriting a concurrent edit, deleting a shifted row, or silently selecting a provider model. |
| `FR-LAUNCHER-004` | MUST   | OAuth login flow creates, polls, completes, and logs out provider credentials through bounded flow state; token login supports registered providers that require pasted credentials, including `github-copilot`, plus every account-store-capable provider from the backend model provider catalog such as DeepSeek and Google Gemini; providers whose runtime cannot consume account-store credentials are neither advertised nor accepted by the account API; login persists provider credentials only and must not create default model entries, runnable model entries, or account-router blocks; the accounts UI lists only registered provider accounts, exposes a separate onboarding surface that can assign named credential IDs, and lets every registered account renew or proactively rotate its credentials through that same sheet in a distinct renewal mode. Renewal presents the provider, login method, and credential identity as locked/read-only fields, submits the exact stored fully qualified credential ID unchanged, and replaces only that auth-store slot without creating an inferred duplicate or changing sibling credentials. The replacement becomes authoritative for subsequent requests in the running gateway without a restart; an in-flight refresh must not overwrite it, and renewal clears only the exact credential's stale account-router authentication cooldown across configured agent workspaces while preserving sibling and non-authentication health. The accounts UI infers a missing OpenAI account name from the OAuth email local-part, displays OpenAI account headers as provider plus auth method and subscription type when known, displays GitHub Copilot token-backed accounts with provider labels/icons, and displays sanitized ChatGPT Codex and GitHub Copilot account usage limits by reading Picoclaw credentials and calling provider-specific usage APIs without exposing raw upstream error bodies or CLI config state; known exhaustion is projected as `limit_reached`, distinct from unavailable telemetry. When Codex reports earned usage-limit reset availability, the OpenAI account summary shows the authoritative available count including zero and indicates that an available reset is used automatically for eligible exhaustion. | Provider accounts need browser setup and in-place credential recovery or rotation without presenting unregistered accounts as active entries, changing account identity, creating default models, duplicating accounts as models, requiring a gateway restart, or overwriting a sibling account. |
| `FR-LAUNCHER-005` | MUST   | Gateway lifecycle endpoints report status/logs and start/stop/restart managed gateway processes without losing log diagnostics. A missing global model alias does not block process startup: the managed child starts with `--allow-empty`, remains available in limited mode, and rejects model-dependent execution locally until an execution context supplies a configured alias. Gateway status separately reports whether model setup is required, and the dashboard displays that notice independently of process state until the predefined `chat` alias has an explicit mapping.                                                                                                                                                                                                                                                                                                                                                                                                        | Process availability and model selection are separate concerns; desktop users still need process control and a visible configuration path before a model is selected. |
| `FR-LAUNCHER-006` | MUST   | Startup, launcher config, update, and version endpoints report or mutate only their documented system settings.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | System management must be narrow and auditable.                                                                                                                |
| `FR-LAUNCHER-007` | SHOULD | API errors return JSON responses with actionable messages and appropriate status codes.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Frontend UX needs consistent failures.                                                                                                                         |
| `FR-LAUNCHER-008` | MUST   | Model fetch distinguishes regular OpenAI API-key listings from OpenAI OAuth/token Codex subscription listings; credential-backed OpenAI fetches use the stored credential, account headers, and the current minimum Codex-compatible client version required for GPT-5.6 model visibility against the ChatGPT Codex models endpoint, while API-key fetches continue to use the OpenAI-compatible `/models` endpoint; GitHub Copilot model fetch exposes static metadata/common models without a credential, uses direct Copilot model listing with the stored token for credential-backed fetches, and credential-backed status checks validate stored credentials instead of probing the local bridge. Shared selectable-account validation rejects expired credentials, unsupported credential auth methods, provider mismatches, and malformed GitHub Copilot tokens before any feature presents or admits the account. | Subscription and API-key accounts have different upstream auth and must not fail or mix credentials.                                                           |
| `FR-LAUNCHER-009` | SHOULD | Shared launcher layout, theme, and primitive controls remain responsive, token-driven, keyboard-accessible, and free of clipped controls across desktop and narrow mobile widths. The Services group includes a collapsible Repository reviews section with separate Review runs, Repositories, Profiles, and Model review probes destinations; it has no separate Results item, and nested review detail/report/finding/issue routes keep Review runs active while automatically revealing the child without changing route behavior. Destructive controls use paired background/foreground theme tokens with sufficient contrast in light and dark modes instead of translucent destructive text treatments that fail automated accessibility checks. | Dashboard navigation and process controls must stay usable while visual styling evolves. |
| `FR-LAUNCHER-010` | MUST   | The authenticated launcher composition registers the feature-owned MCP management and OAuth callback routes, exposes a dedicated Agent → MCP navigation entry, and removes MCP editing from the generic config form. Gateway restart detection includes enabled MCP discovery, server transport, custom-header, and nonsecret auth-revision changes. Shared forms announce validation errors and provide keyboard-accessible, labeled secret visibility controls.                                                                                                                                                                                                                                                                                                                                                                                                                                                        | MCP management must be easy to find, must not conflict with generic config saves, and must clearly apply runtime-relevant changes without weakening shared form accessibility. |
| `FR-LAUNCHER-011` | MUST   | Full-config PUT/PATCH/reset, generic tool-state writes, agent policy mutations, workflow-specific settings, template-install, publish, and workflow Run/Retry admission are serialized by one handler mutation boundary. Every cooperating `SaveConfig` call also holds a config-path advisory process/file lock, with the opaque generation covering both public JSON and the security sidecar. Full-config PUT/PATCH, generic tool-state, agent policy, and workflow settings mutations load an update-safe snapshot and perform final compare-and-swap saves against that exact generation; reset holds the lock across backup, secret preservation, and replacement. Stable scoped reads derive their opaque revision from the same snapshot without migration, backup, or save side effects. Agent responses derive restart effects from that captured config and a read-only in-memory gateway snapshot without discovering processes, attaching to them, or sanitizing PID metadata. Workflow Run and Retry reacquire that same advisory lock after their final readiness fence, compare the current public-plus-security generation with the admitted generation, and retain the lock through exact compatibility checking and durable root-run creation. The authenticated launcher registers agent management routes and navigation, and the gateway restart signature includes the complete ordered agent policy while preserving nil-versus-empty distinctions. | Scoped or merge-patch management must not return values or effects from one config generation with another generation's revision, lose a concurrent secret-only update, overwrite a mutation from another launcher or gateway process, hide an unapplied agent policy change, mutate gateway process metadata during an agent read, or admit execution from one generation while another process publishes a replacement before the run exists. |
| `FR-LAUNCHER-012` | MUST   | The authenticated launcher registers agent capability and activity routes without replacing existing management surfaces. Capability mutation holds the shared handler and advisory config boundaries through its final composite config/file fence and atomic workspace write, while gateway restart comparison combines the filesystem-pure config signature with only runtime-relevant `AGENT.md` frontmatter semantics. Activity is read-only: the gateway records a concrete numeric address from the listener that actually opened, including a single-stack localhost fallback; the launcher peeks PID authority without attachment, cleanup, or migration, rejects hostname and wildcard authority, validates the numeric target as loopback or a literal local-interface address, injects the process bearer into one exact bounded no-proxy/no-redirect request, forwards no browser credentials or ambient headers, and strictly reprojects the response. | Workspace policy must not race config ownership, prose-only edits must not spuriously require restart, and a browser activity view must not mutate process metadata or leak runtime bearer authority. |
| `FR-LAUNCHER-021` | MUST | The authenticated shell owns `/development`, `/development/new`, `/development/:workspaceID`, `/development/workflow-configurations`, `/development/repositories`, and `/development/settings`. Intake mounts exactly one discriminated form: issue or brief for `implement_feature`, or one PR URL for `pickup_pr`; mixed fields are cleared in the UI and rejected by strict server decoding. Workspace IDs use only `devw_`; every pre-v20 route, API, and identifier form has no compatibility behavior. | Feature creation and existing-PR continuation must be unmistakably separate and deep-linkable. |
| `FR-LAUNCHER-022` | MUST | A development workspace exposes URL-owned Overview, Changes, Files, and Activity views plus responsive Ask/Steer chat. Overview renders current lifecycle, validation, and only actionable Gate or unknown-publication controls. Changes/Files lazy-load an exact read-only Monaco candidate/base view with inline mobile layout and accessible plain-text fallback. Workflow configurations, repository assignments, and lifecycle settings remain separate revision-fenced pages and never execute a Gate. | Development evidence must be inspectable and steerable without turning browser navigation into code or publication authority. |
| `FR-LAUNCHER-024` | MUST | `/notifications` and `/notifications/:notificationID` provide a durable responsive attention inbox with open/unread/snoozed/resolved state, filtered previous/next navigation, bulk read/unread/snooze/archive, bounded JQL-like filtering, URL-owned queries, and pinned/default saved views. Desktop uses split list/detail; mobile preserves filtered position and uses sheets without unsolicited modals. Production composition registers the branded PicoClaw service worker; install caches only fixed public branding assets, activation removes older PicoClaw cache generations, push clicks open an authenticated notification detail, and browser/PWA support failure leaves the inbox usable. Permission is requested only from **Enable mobile notifications**. Users can name, disable, rename, or revoke devices and independently opt into repository display. Push delivery is limited to newly open critical/high generations and lock-screen content contains only the fixed PicoClaw title, bounded reason category, optional opted-in repository, and notification identity. | Attention should interrupt only when action is important while preserving a sortable, private, durable backlog. |

| `FR-LAUNCHER-023` | MUST | The authenticated shared shell exposes a `/model-evaluations` Model review probes collection and `/api/model-evaluations*` control surface. Setup lives at `/model-evaluations/new` and accepts a repository, reusable review profile, two-to-eight-model candidate experiment, and optional ref; it displays inherited reviewer/account/focus/scope/parallelism and work-sizing maxima, rejects custom scope/quota/selector/judge controls, and creates a version-fenced draft. Detail and item-owned language, corpus, and report routes run asynchronous preflight, deterministic sizing points, judging, and analysis; lock configuration after Run; poll only active durable state; page safe corpus references; show bounded run history, sizing/score/token statistics, degradation ceilings, and honest partial/failure/unattained/unknown-cost states. The canonical report route is `/model-evaluations/{evaluation_id}/report`. The UI states that probes read a profile snapshot but do not start repository reviews, mutate profiles/assignments, or write finding ledgers. | Model comparison must be a discoverable, restart-safe controlled experiment rather than a hidden repository-review mode, a combined list/editor workspace, unrelated custom options, or a browser-owned timer. |

| `FR-LAUNCHER-025` | MUST | Administrative collections use the registered shared collection shell, server-authoritative bounded query schema, canonical `q` URL state, browser-local recent queries and view preference, explicit cross-page selection, partial-success reconciliation, and dedicated list/new/detail/edit routes defined by `docs/design/collection-ui-system.md`. Plain click selects one row, Shift-click selects a contiguous range, Control/Command-click toggles an item, double-click or Enter opens detail, and right-click opens item actions; an explicitly additive collection may make plain click/tap toggle selection so cross-page subset workflows remain usable on touch devices. Collection rows render neither selection checkboxes nor persistent action triggers. Shared detail shells accept a routed surface's restored scroll container, and route-match-scoped query reconciliation never reclaims a navigation to another collection; new collection debt and modified legacy collection implementations are rejected by the manifest and base/head gates. Model Aliases specifically expose configured aliases only through name-addressed list/detail/create/update/delete and at-most-200 bulk-delete APIs with opaque config-revision fencing, allowlisted typed filtering/sorting, additive `total`, `next_cursor`, `canonical_query`, and `query_schema`, selection-aware reference blockers, full candidate validation, restart effects, and no unrelated config mutation. The UI uses `/models/aliases`, `/models/aliases/new`, `/models/aliases/{name}`, and `/models/aliases/{name}/edit`; developer catalog entries are creation templates rather than list items, direct details use the item endpoint, and legacy `/models` is not compatibility-rendered. | Shared collection behavior must remain consistent and enforceable, while aliases need stable names and safe concurrency instead of mutable array indexes or catalog placeholders masquerading as configured resources. |

## Data And State Model

Model Alias collection identity is normalized alias `name`. Query fields are
sortable `name`, `model`, `overrides`, and `disabled_accounts`; default ordering
is name plus stable name. Bulk failure codes include `invalid_id`,
`duplicate_id`, `not_found`, and `referenced`; blockers contain bounded safe
labels only. The returned config revision fences name-addressed and bulk
mutations. Browser-local collection state stores at most eight successful recent
queries and one preferred view per manifest key; selection and scroll position
remain in memory and are never durable server state.

The launcher owns an HttpOnly dashboard session, process-local login throttles,
a shared config mutation lock, public-plus-security config revisions, managed
gateway PID metadata, PWA registration, and browser push subscription UX. It
does not own durable development aggregates, notification rows, private push
keys, or delivery ledgers.

Development browser state is shallow and URL-owned: workspace tab, validated
candidate path/revision, notification query, selected notification, and safe
attention target. Intake drafts, API cursors, request IDs, conflicts, prompts,
private Gate subjects, checkout paths, and reconciliation evidence remain out of
the URL. The gateway aggregate and notification store are authoritative after
every mutation.

The workflow configuration response contains named workflow configurations, a
default configuration, atomic gate-action bindings, independent
review/completion nudge bounds, scope-size thresholds, the resolved
Gate catalog, the normalized
Review and Implementation flow graph and its content revision, a catalog
digest, the config revision, and the `restart-required` effect. The browser
renders that graph directly; it does not
carry a second hard-coded development lifecycle topology. The repository-assignment
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
Owns: CODE pkg/collectionquery/**
Owns: CODE web/backend/**
Owns: CODE web/frontend/src/api/launcher-auth.ts
Owns: CODE web/frontend/src/api/collection.ts
Owns: CODE web/frontend/src/api/models.ts
Owns: CODE web/frontend/src/api/oauth.ts
Owns: CODE web/frontend/src/api/system.ts
Owns: CODE web/frontend/src/app-providers.tsx
Owns: CODE web/frontend/index.html
Owns: CODE web/frontend/public/service-worker.js
Owns: CODE web/frontend/public/site.webmanifest
Owns: CODE web/frontend/src/api/notifications.ts
Owns: CODE web/frontend/src/components/app-*
Owns: CODE web/frontend/src/components/config/**
Owns: CODE web/frontend/src/components/collection/**
Owns: CODE web/frontend/src/components/collections/pilots/model-alias*
Owns: CODE web/frontend/src/components/collections/pilots/model-collections.tsx
Owns: CODE web/frontend/src/components/collections/pilots/standard-pilot-collection-page.tsx
Owns: CODE web/frontend/src/components/credentials/**
Owns: CODE web/frontend/src/components/gateway-setup-notice.tsx
Owns: CODE web/frontend/src/components/models/**
Owns: CODE web/frontend/src/components/page-header.tsx
Owns: CODE web/frontend/src/components/notifications/**
Owns: CODE web/frontend/src/components/shared-form.tsx
Owns: CODE web/frontend/src/components/tour/**
Owns: CODE web/frontend/src/components/ui/**
Owns: CODE web/frontend/src/hooks/use-credentials-page.ts
Owns: CODE web/frontend/src/hooks/use-collection-route-state.ts
Owns: CODE web/frontend/src/hooks/use-theme.ts
Owns: CODE web/frontend/src/i18n/**
Owns: CODE web/frontend/src/index.css
Owns: CODE web/frontend/src/lib/**
Owns: CODE web/frontend/src/main.tsx
Owns: CODE web/frontend/src/routes/agent.tsx
Owns: CODE web/frontend/src/routes/__root.tsx
Owns: CODE web/frontend/src/routes/config*
Owns: CODE web/frontend/src/routes/accounts.account-router.$index.tsx
Owns: CODE web/frontend/src/routes/accounts.account-router.new.tsx
Owns: CODE web/frontend/src/routes/accounts.tsx
Owns: CODE web/frontend/src/routes/credentials.tsx
Owns: CODE web/frontend/src/routes/launcher-*
Owns: CODE web/frontend/src/routes/models.tsx
Owns: CODE web/frontend/src/routes/models_*alias*.tsx
Owns: CODE web/frontend/src/routes/notifications*
Owns: CODE web/frontend/src/store/**
Owns: CODE web/frontend/src/test/**
Owns: CLI cmd/picoclaw/internal/auth/* *
Owns: CLI cmd/picoclaw/internal/config/* *
Owns: CLI cmd/picoclaw/internal/migrate/* *
Owns: CLI cmd/picoclaw/internal/onboard/* *
Owns: CONFIG.development*
Owns: HTTP /api/update
Owns: HTTP * /api/auth*
Owns: HTTP * /api/config*
Owns: HTTP * /api/accounts/models*
Owns: HTTP * /api/accounts/model-aliases*
Owns: HTTP * /api/model-aliases*
Owns: UI /models/aliases*
Owns: HTTP * /api/oauth*
Owns: HTTP GET /oauth/callback
Owns: HTTP * /api/system*
Owns: HTTP * /api/agents*
Owns: HTTP * /api/wecom*
Owns: HTTP * /api/weixin*
Owns: HTTP * /api/workflows*
Owns: HTTP GET /api/development/repositories
Owns: HTTP PUT /api/development/repositories
Owns: HTTP GET /api/development/workflow-configurations
Owns: HTTP PUT /api/development/workflow-configurations
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
Owns: TEST pkg/collectionquery/**
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
| HTTP | `/api/development-workspaces*` | Authenticated, canonical, bounded proxy for the matching protected gateway tree; mutations require same-origin provenance and JSON. | `FR-LAUNCHER-021`, `FR-LAUNCHER-022` |
| HTTP | `/api/notifications*`, `/api/notification-views`, `/api/notification-settings`, `/api/push-subscriptions*` | Authenticated inbox, saved-view, privacy-setting, SSE refresh, and revision-fenced device management proxies. | `FR-LAUNCHER-024` |
| HTTP | `GET/PUT /api/development/workflow-configurations` | Read or revision-fenced replace workflow configurations, default selection, nudge bounds, and scope thresholds while preserving assignments. | `FR-LAUNCHER-011`, `FR-LAUNCHER-022` |
| HTTP | `GET/PUT /api/development/repositories` | Read verified repository descriptors and safe configuration summaries or revision-fenced replace repositories and assignments while preserving workflow configuration. | `FR-LAUNCHER-011`, `FR-LAUNCHER-022` |
| UI | `/development*` | Mutually exclusive intake, portfolio, aggregate workspace, read-only code evidence, workflow configurations, repositories, and lifecycle settings. | `FR-LAUNCHER-009`, `FR-LAUNCHER-021`, `FR-LAUNCHER-022` |
| UI/PWA | `/notifications*`, manifest, service worker | Sortable attention backlog, saved queries, device controls, app badge, privacy-minimal push, and authenticated deep links. | `FR-LAUNCHER-009`, `FR-LAUNCHER-024` |
| HTTP | `/api/config*`, `/api/models*`, `/api/oauth*`, `/api/system*`, `/api/agents*`, `/api/workflows*` | Existing authenticated management surfaces retain their scoped contracts and shared mutation fencing. | `FR-LAUNCHER-001` through `FR-LAUNCHER-012` |

| HTTP/UI | `/api/model-aliases*`; `/models/aliases*` | Name-addressed typed query/list/detail/CRUD, revision-fenced explicit-name bulk delete, catalog templates on creation only, and standard collection/detail/editor routes. | `FR-LAUNCHER-025` |
| UI governance | `web/frontend/collection-surfaces.json`, shared collection subsystem, UI linter, and base/head guard | Audited standard/legacy/exempt inventory, canonical collection state/presentation, static route/shell enforcement, and touched-legacy migration enforcement. | `FR-LAUNCHER-009`, `FR-LAUNCHER-025` |

## Algorithms And Ordering

For a development-workspace or notification request, the launcher first validates the exact path,
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

The notification inbox parses only the server's bounded query language and
binds pagination/neighbors to the exact query. Enabling push first obtains the
server public VAPID key, then requests browser permission and registers the
subscription; no page load or notification read may prompt. The service worker
accepts only a safe notification identity and fixed reason/repository fields,
focuses or opens the authenticated detail route, and never receives a workspace
summary, source body, credential, or provider capability.

For an account renewal, the launcher reopens the existing onboarding surface
with provider and fully qualified credential identity locked, while retaining
only login methods supported by that provider. A successful flow replaces that
one auth-store slot. Request-time credential resolution makes the replacement
effective in the running gateway; serialized compare-and-swap refresh prevents
older refresh work from restoring stale tokens, and per-credential router
invalidation clears only the renewed account's authentication cooldown across
agent workspaces.

## Cross-Feature Behavior

Durable External Event Automation owns development-workspace lifecycle state,
gateway runtime routes, provider adapters, and development frontend components. Workflows
owns static gate declarations, `gate/exec`, action resolution, and private
continuation. Security owns
dashboard authentication, bearer replacement, config-secret handling, and
network confinement. Git Workspaces owns pinned local candidates and branch
push fences. The launcher composes these surfaces but gains no model, Git,
provider, publication, or merge authority from navigation or configuration.

Repository Reviews owns its profiles, ledgers, controller decisions, workflow
admission, and issue-publication state. Launcher startup registers its
authenticated API and starts exactly one controller after routes exist;
shutdown cancels that controller before dependent process state disappears.
The launcher proxy admits only the dedicated protected gateway publication
path, while sidebar navigation and model/account metadata reads remain inert.

## Failure And Edge Cases

Unauthenticated requests fail before config or process access. Noncanonical
paths, repeated/unknown query keys, aliases for removed development routes, cross-site
mutations, missing or streaming bodies, unsafe encodings, oversized JSON,
unknown fields, stale config/workspace/head revisions, unavailable runtime
authority, redirects, non-local targets, malformed upstream JSON, and unsafe
external URLs fail closed through bounded public errors.

A gateway outage leaves configuration and other launcher management available.
A development mutation conflict retains the user's draft and requires refresh. An unknown
provider effect retains reconciliation state. Narrow layouts preserve all
controls without horizontal page overflow. No browser read, filter, selection,
or settings navigation starts a model, workflow, checkout, provider effect, or
publication. Denied permission, insecure origin, missing PushManager, failed
worker registration, or a revoked endpoint disables OS push only; it never
removes or resolves the durable inbox item.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-LAUNCHER-023` | [web/backend/api/repository_model_evaluations_test.go](../../web/backend/api/repository_model_evaluations_test.go), [web/backend/api/repository_model_evaluation_controller_test.go](../../web/backend/api/repository_model_evaluation_controller_test.go), [web/frontend/src/api/model-evaluations.test.ts](../../web/frontend/src/api/model-evaluations.test.ts), [web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx](../../web/frontend/src/components/model-evaluations/model-evaluations-page.test.tsx), [web/frontend/src/routes/-model-evaluations-route.test.tsx](../../web/frontend/src/routes/-model-evaluations-route.test.tsx), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-001`                    | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [web/backend/api/workflow_jobs_editor_test.go](../../web/backend/api/workflow_jobs_editor_test.go), [web/backend/middleware/access_control_test.go](../../web/backend/middleware/access_control_test.go)                                                                                                                                                                                                                         |
| `FR-LAUNCHER-002`, `FR-LAUNCHER-007` | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go)                                                                                                                                                                                                                                                                                                                                                                                               |
| `FR-LAUNCHER-003`                    | [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/backend/api/model_aliases_test.go](../../web/backend/api/model_aliases_test.go), [web/backend/api/model_mutation_default_test.go](../../web/backend/api/model_mutation_default_test.go), [web/backend/api/model_update_revision_test.go](../../web/backend/api/model_update_revision_test.go), [web/backend/api/model_status_test.go](../../web/backend/api/model_status_test.go), [web/backend/api/model_catalog_test.go](../../web/backend/api/model_catalog_test.go), [web/frontend/src/api/models.test.ts](../../web/frontend/src/api/models.test.ts), [web/frontend/src/components/models/model-card.test.tsx](../../web/frontend/src/components/models/model-card.test.tsx), [web/frontend/src/components/models/model-mutation-default.test.tsx](../../web/frontend/src/components/models/model-mutation-default.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-004`                    | [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go), [web/backend/api/codex_account_limits_test.go](../../web/backend/api/codex_account_limits_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go), [pkg/accountrouter/router_test.go](../../pkg/accountrouter/router_test.go), [pkg/providers/factory_provider_test.go](../../pkg/providers/factory_provider_test.go), [web/frontend/src/components/credentials/account-onboarding-sheet.test.tsx](../../web/frontend/src/components/credentials/account-onboarding-sheet.test.tsx), [web/frontend/src/hooks/use-credentials-page.test.ts](../../web/frontend/src/hooks/use-credentials-page.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [cmd/picoclaw/internal/auth](../../cmd/picoclaw/internal/auth) |
| `FR-LAUNCHER-005`, `FR-LAUNCHER-006` | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/startup_test.go](../../web/backend/api/startup_test.go), [web/backend/api/version_test.go](../../web/backend/api/version_test.go)                                                                                                                                                                                                                                                                                                       |
| `FR-LAUNCHER-008`                    | [web/backend/api/models_test.go](../../web/backend/api/models_test.go)                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `FR-LAUNCHER-009`                    | [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/src/components/ui/button.tsx](../../web/frontend/src/components/ui/button.tsx), [web/frontend/src/index.css](../../web/frontend/src/index.css), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/scripts/lint-ui-rules.mjs](../../web/frontend/scripts/lint-ui-rules.mjs)                                                                                                                                                       |
| `FR-LAUNCHER-010`                    | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/src/components/agent/mcp/mcp-server-card.test.tsx](../../web/frontend/src/components/agent/mcp/mcp-server-card.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-011`                    | [pkg/config/mutation.go](../../pkg/config/mutation.go), [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/workflows/mutation_lock_test.go](../../pkg/workflows/mutation_lock_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_writer_cas_test.go](../../web/backend/api/config_writer_cas_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go), [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/workflow_settings_test.go](../../web/backend/api/workflow_settings_test.go), [web/backend/api/workflow_templates_test.go](../../web/backend/api/workflow_templates_test.go), [web/backend/api/workflow_publish_test.go](../../web/backend/api/workflow_publish_test.go), [web/backend/api/workflow_dependencies.go](../../web/backend/api/workflow_dependencies.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_run_readiness_test.go](../../web/backend/api/workflow_run_readiness_test.go), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-012`                    | [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go), [web/backend/api/agent_activity_test.go](../../web/backend/api/agent_activity_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [pkg/gateway/agent_activity_test.go](../../pkg/gateway/agent_activity_test.go), [web/frontend/src/components/agent/agents/agent-capabilities-panel.test.tsx](../../web/frontend/src/components/agent/agents/agent-capabilities-panel.test.tsx), [web/frontend/src/components/agent/agents/agent-activity-panel.test.tsx](../../web/frontend/src/components/agent/agents/agent-activity-panel.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-LAUNCHER-021` | [web/frontend/src/api/development-workspaces.test.ts](../../web/frontend/src/api/development-workspaces.test.ts), [web/frontend/src/components/development-workspaces/development-intake-page.test.tsx](../../web/frontend/src/components/development-workspaces/development-intake-page.test.tsx), [web/frontend/src/routes/-development.test.ts](../../web/frontend/src/routes/-development.test.ts), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx) |
| `FR-LAUNCHER-022` | [web/frontend/src/components/development-workspaces/development-pages.test.tsx](../../web/frontend/src/components/development-workspaces/development-pages.test.tsx), [web/frontend/src/components/development-workspaces/development-code-browser.test.tsx](../../web/frontend/src/components/development-workspaces/development-code-browser.test.tsx), [web/frontend/src/components/development-workspaces/development-action-panel.test.tsx](../../web/frontend/src/components/development-workspaces/development-action-panel.test.tsx) |
| `FR-LAUNCHER-024` | [web/frontend/src/api/notifications.test.ts](../../web/frontend/src/api/notifications.test.ts), [web/frontend/src/components/notifications/notification-inbox-page.test.tsx](../../web/frontend/src/components/notifications/notification-inbox-page.test.tsx), [web/frontend/src/components/notifications/notification-query.test.ts](../../web/frontend/src/components/notifications/notification-query.test.ts), [web/frontend/src/components/notifications/push-notification-settings.test.tsx](../../web/frontend/src/components/notifications/push-notification-settings.test.tsx), [web/frontend/src/lib/pwa-notifications.test.ts](../../web/frontend/src/lib/pwa-notifications.test.ts) |

| `FR-LAUNCHER-025` | [web/backend/api/collection_apis_test.go](../../web/backend/api/collection_apis_test.go), [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/frontend/src/api/models.test.ts](../../web/frontend/src/api/models.test.ts), [web/frontend/scripts/check-collection-delta.test.mjs](../../web/frontend/scripts/check-collection-delta.test.mjs), [web/frontend/tests/collection-visual.spec.ts](../../web/frontend/tests/collection-visual.spec.ts) |

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
- [web/frontend/src/routes/development.tsx](../../web/frontend/src/routes/development.tsx)
- [web/frontend/src/routes/development_.new.tsx](../../web/frontend/src/routes/development_.new.tsx)
- [web/frontend/src/routes/development_.$workspaceID.tsx](../../web/frontend/src/routes/development_.$workspaceID.tsx)
- [web/frontend/src/routes/notifications.tsx](../../web/frontend/src/routes/notifications.tsx)
- [web/frontend/src/routes/notifications_.$notificationID.tsx](../../web/frontend/src/routes/notifications_.$notificationID.tsx)
- [web/frontend/src/api/development-workspaces.ts](../../web/frontend/src/api/development-workspaces.ts)
- [web/frontend/src/api/notifications.ts](../../web/frontend/src/api/notifications.ts)
- [web/frontend/src/api/pr-lifecycle-workflow-configurations.ts](../../web/frontend/src/api/pr-lifecycle-workflow-configurations.ts)
- [web/frontend/src/api/pr-lifecycle-repository-assignments.ts](../../web/frontend/src/api/pr-lifecycle-repository-assignments.ts)
- [web/frontend/src/components/development-workspaces](../../web/frontend/src/components/development-workspaces)
- [web/frontend/src/components/notifications](../../web/frontend/src/components/notifications)
