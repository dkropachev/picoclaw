# MCP Integration And Discovery

## Feature ID

`FR-MCP`

## Behavior Summary

PicoClaw enables MCP integration by default, connects configured servers,
discovers tools, wraps remote calls as agent tools, supports eager and deferred
discovery, and provides CLI plus dedicated launcher management for server
configuration, connectivity testing, bearer credentials, and browser OAuth
login. One shared canonical server/tool naming contract is used by wrapper
registration, direct workflow MCP execution, and workflow dependency readiness;
ambiguous canonical names fail closed. Structured CLI add/remove mutations are
revision-fenced so they cannot replace unrelated configuration written by
another launcher, CLI, or gateway process.

## Reconstruction Notes

- Similarity target: recreate an MCP manager that connects configured servers, lists tools, wraps remote tools, handles reconnect cases, and exposes CLI config management.
- Core types/functions: MCP manager, server connection, command/HTTP transport setup, auth-store credential resolution, tool wrapper, runtime event publisher, launcher MCP handlers, and Cobra MCP subcommands.
- Runtime ordering: load enabled servers, connect transport, initialize session, list tools, register wrappers eagerly or behind discovery, execute remote calls, publish events.
- Non-obvious constraints: CLI mutates config only, server names prefix tool
  names through one canonicalizer, collisions are rejected before partial
  registration, env files and headers are transport-specific, and empty server
  removal disables MCP globally. Add and remove operate on one
  public-plus-security config snapshot and reject a stale compare-and-save
  instead of replaying a partial MCP change over newer state. Edit performs the
  same fenced validation/normalization preflight before handing the config file
  to the operator's editor; the subsequent external editor write is not a
  compare-and-save mutation.

## Requirements

| ID           | Level  | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Rationale                                                                                                                                     |
| ------------ | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-MCP-001` | MUST   | Enabled MCP servers connect over stdio, HTTP streamable transport, or SSE-compatible mode using configured command, URL, env, env file, and headers.                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | MCP compatibility is a core extension point.                                                                                                  |
| `FR-MCP-002` | MUST   | Tool discovery registers remote tool names with server prefixes and preserves remote descriptions and schemas.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | The model needs unambiguous callable tool definitions.                                                                                        |
| `FR-MCP-003` | MUST   | Deferred discovery hides remote tools behind search/open behavior until selected. Its TTL is measured in agent tool-execution ticks, and a per-server deferred override wins over the global setting. The launcher accepts enabled discovery settings only when TTL and maximum results are at least one and at least one BM25 or regex matcher is enabled; runtime config with nonpositive TTL or maximum results falls back to five, while no enabled matcher fails initialization.                                                                                                                                                                                                              | Large MCP setups must not exhaust context or expire selected tools on an ambiguous clock.                                                      |
| `FR-MCP-004` | MUST   | Remote tool calls forward JSON arguments, return text/media results, and publish start/end runtime events including failures.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | MCP execution must be observable and model-readable.                                                                                          |
| `FR-MCP-005` | MUST   | MCP CLI add/list/show/test/edit/remove changes only config state and does not keep servers running. Add and remove load one update-safe config snapshot with its opaque public-plus-security revision, validate the complete candidate, and save only if that revision is still current. A conflict reports that configuration changed and requires an explicit reload/retry; it neither applies the stale structured MCP mutation nor replaces unrelated concurrent state such as model aliases. Edit applies this fence only to its validation/normalization preflight, then launches the operator's editor against the config path; the editor's direct write is outside PicoClaw's compare-and-save boundary. | CLI is a configuration manager, not a daemon; structured mutations must not lose concurrent configuration changes, while the explicit editor boundary must remain accurate. |
| `FR-MCP-006` | MUST   | CLI add enables MCP globally, creating the first server through the launcher auto-enables it, and removing the final server through either management surface disables global MCP enablement.                                                                                                                                                                                                                                                                                                                                                                                                                                  | Add/remove boundaries should produce an immediately usable and internally consistent global state.                                            |
| `FR-MCP-007` | SHOULD | Live server inspection reports reachable status and tool counts without mutating configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Operators need safe diagnostics.                                                                                                              |
| `FR-MCP-008` | MUST   | A missing `tools.mcp.enabled` value defaults to true, while an explicit false remains disabled. New default configurations leave the server map empty and discovery disabled until an operator opts in.                                                                                                                                                                                                                                                                                                                                                                                                                        | A newly installed launcher should be ready to add an MCP server without overriding an explicit opt-out or exposing unnecessary discovery tools. |
| `FR-MCP-009` | MUST   | The authenticated launcher exposes Agent → MCP navigation to the dedicated `/agent/mcp/servers` collection and `/api/mcp*` endpoints instead of a duplicate editor in the generic config form. Collection summaries and details contain sanitized env/header key names and auth status but never values; explicit key inventories let updates preserve unchanged secret values. Operators use dedicated create/edit routes to add or update validated stdio/HTTP/SSE definitions, enable or disable servers, delete with confirmation, and test unsaved or saved definitions without mutating unrelated config; the first add auto-enables MCP. The responsive, keyboard-accessible guided form starts from local-command or remote-URL intent, offers Save, Save & Test, and Save & log in actions when applicable, progressively reveals transport/auth details, and reports actionable probe results, tool counts, validation errors, and gateway-restart state. Every mutation, including a stdio probe that may execute a process, requires same-origin browser context; body-bearing mutations require JSON. | MCP setup and diagnosis should not require raw JSON, expose secret values, or hide process-launching mutations behind weak browser controls. |
| `FR-MCP-010` | MUST   | Remote server auth config may reference an external auth-store credential by `credential_id` with `bearer` or `oauth` type and a nonsecret `revision` counter. Credential identities are normalized and server-scoped; a rename preserves the existing explicit reference, while a recreated or shared-name attachment forks a collision-resistant ID rather than inheriting another server incarnation's credential, and cleanup occurs only after the final reference. The runtime resolves the exact authoritative stored credential on every bearer-token acquisition and injects its access token without persisting tokens in `config.json` or returning them from launcher APIs, so an in-place launcher renewal takes effect without rebuilding the MCP transport; credential changes bump `revision` so gateway restart detection observes them. The launcher supports setting, replacing, and removing bearer tokens plus browser OAuth authorization-code login with PKCE, state validation, expiry, dynamic client registration, popup-safe save-before-login, sanitized polling, refresh persistence, and reconnect. Custom headers and bearer/OAuth auth require HTTPS except for intentional loopback development; configured MCP URLs reject userinfo, launcher-managed headers reject reserved transport names and runtime skips such configured overrides, secrets stay bound to the configured origin, and runtime redirects remain same-origin. OAuth completion rechecks the server transport, URL, auth, and latest-flow snapshot before persistence; OAuth discovery, token, authenticated-probe, and refresh transports disable proxies and DNS-pin policy-approved addresses; refresh is serialized per exact credential and a stale serialized refresh cannot overwrite or mask a replacement credential. | Server credentials need a simple UI while remaining out of ordinary config reads, logs, frontend state, unrelated network origins, and stale concurrent flows. |
| `FR-MCP-011` | MUST   | Every remote identity is represented as a canonical local tool name derived from its server and tool components by the same function used for eager wrapper registration, deferred discovery, direct `mcp/<server>/<tool>` workflow execution, workflow readiness, and live workflow-authoring projection. Every registered wrapper also retains its exact original server and tool components; direct execution and readiness require those components to equal the workflow request before calling the manager. Before exposing any wrapper, registration preflights the complete discovered set and every affected agent registry, including hidden entries. It rejects two distinct remote identities that canonicalize to one name and rejects a canonical name already occupied by a built-in, local, plugin, or different MCP tool; only the exact same MCP identity may be refreshed. The manager maintains collision safety when connections change and permits a bounded, read-only visit of one coherent live snapshot; authoring projection returns no MCP rows and reports an unsafe partial result when distinct live identities collide or an exact registered identity disagrees. Runtime/readiness likewise report collision or exact-identity mismatch instead of selecting or replacing a tool by iteration order. | Normalization must not make a workflow invoke a different remote capability from the one it reviewed, overwrite an unrelated capability, or make startup behavior depend on map ordering. |
| `FR-MCP-012` | MUST   | An MCP wrapper snapshots exact server/tool identity, canonical local name, final description, nested parameter schema, prompt metadata, workspace, inline-text limit, and runtime-event publisher instead of retaining a mutable SDK tool pointer. Every parameter projection is detached. Legacy wrappers keep their source-compatible empty-object fallback for malformed schema and synchronized runtime setters; strict factory snapshots fail on malformed, trailing, non-object, cyclic, unsupported, non-finite, or typed-nil non-nil schema, seal workspace/limit/publisher setters, and keep only owner-local media mutable. A strict per-owner factory ignores remote safety annotations, creates a distinct wrapper per call with conservative traits, and borrows rather than closes the exact manager and event bus. The factory API itself does not publish a wrapper or change an agent registry. | Connected servers own mutable SDK declarations, while provider caches, owner construction, media delivery, and runtime events require stable identity/schema and isolated wrapper state for one exact generation. |
| `FR-MCP-014` | MUST   | Successful runtime initialization stages one deterministic detached catalog containing strict MCP factories plus configured destination-bound Regex/BM25 factories and commits all admitted agent registries through one all-or-none transaction. Invalid discovery configuration fails before network access. Partial server connection exposes only connected configured servers; identical exact declarations deduplicate and conflicting/canonical identities fail. Per-agent MCP-server allowlists select staged remote capabilities, tool allowlists decide transaction admissions, and exact same-identity refresh uses occupant CAS without overwriting another tool. Candidate manager ownership transfers irreversibly at registry commit, so every precommit error/panic/cancellation closes it and every postcommit prompt/projection fault retains it beneath published wrappers. MCP and discovery prompt text is absent before success and is generated only from exact admissions, with collision-resistant exact-server source IDs and exact allowed-tool intersection. Factory-backed owner selections receive distinct wrapper/media/discovery state while borrowing the live generation manager and event bus. | Lazy multi-agent registration must not leave a partial tool surface, dangling prompt, stale SDK schema, closed borrowed manager, or lossy prompt authorization after a late collision or reload. |

| `FR-MCP-013` | MUST | MCP servers expose a name-addressed shared collection list/detail contract with bounded `query`, opaque `cursor`, `limit`, `total`, `next_cursor`, `canonical_query`, and typed allowlisted `query_schema`. Bulk delete accepts at most 200 explicit server names and the current config revision, computes selection-aware credential/reference blockers, removes every valid server and final unreferenced credential in one fully validated fenced save, reports `deleted_ids`, stable failures/blockers, the new revision, and restart effects, and preserves unrelated config. The responsive UI uses the shared List/Table/Grid collection at `/agent/mcp/servers` with dedicated `/new`, `/{name}`, and `/{name}/edit` routes; global enablement and discovery settings live only at `/agent/mcp/settings`. Direct detail links use the item endpoint, selection reconciles partial success, route-scoped query canonicalization cannot interrupt navigation away from the collection, and legacy `/agent/mcp` receives no compatibility rendering. | Server administration needs stable identities and safe multi-delete while keeping global integration policy and full server forms off the summary route. |

## Data And State Model

MCP collection identity is normalized server `name`. Query fields are sortable
`name`, `enabled`, `deferred`, `type`, and `auth`; transport suggestions are
`stdio`, `http`, and `sse`, auth suggestions are `none`, `custom`, `bearer`, and
`oauth`, and default ordering is name plus stable name. Bulk failure codes
include `invalid_id`, `duplicate_id`, `not_found`, and `referenced`; blockers
contain bounded safe labels only. The returned config revision fences server and
settings mutations.

MCP state includes global discovery config, per-server transport and auth
references, sanitized env/header key inventories, credential ownership and
reference IDs, bearer and OAuth tokens held in the external auth store, OAuth
refresh metadata, short-lived browser-login flows and server snapshots, live
client sessions, discovered remote tool definitions, generated local tool
names, runtime event metadata, a deterministic gateway restart signature,
CLI- or launcher-managed JSON config entries, and the opaque config revision
captured for each structured CLI add/remove operation and edit preflight.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/mcp/**
Owns: CODE integration/**
Owns: CODE pkg/mcp/**
Owns: CODE pkg/tools/integration/mcp/**
Owns: CODE web/backend/api/mcp*
Owns: CODE web/frontend/src/api/mcp*
Owns: CODE web/frontend/src/components/agent/mcp/**
Owns: CODE web/frontend/src/routes/agent/mcp*.tsx
Owns: CODE pkg/tools/integration/mcp_tool.go
Owns: CODE pkg/agent/agent_mcp.go
Owns: CLI cmd/picoclaw/internal/mcp/*
Owns: CONFIG.tools.mcp*
Owns: HTTP * /api/mcp*
Owns: HTTP GET /mcp/oauth/callback
Owns: UI /agent/mcp/servers*
Owns: UI /agent/mcp/settings
Owns: TEST cmd/picoclaw/internal/mcp/*
Owns: TEST pkg/mcp/*
Owns: TEST pkg/tools/integration/mcp*
Owns: TEST web/backend/api/mcp*
Owns: TEST web/frontend/src/api/mcp*
Owns: INTEGRATION *
Owns: EVENT mcp.*

## Auxiliary Interfaces

| Type        | Surface                                       | Contract                                                                                                               | Requirement IDs                                        |
| ----------- | --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| Config      | `tools.mcp.*`                                 | Default-on global enablement, discovery settings, per-server transport details, and external credential references.    | `FR-MCP-001`, `FR-MCP-003`, `FR-MCP-008`, `FR-MCP-010` |
| CLI         | `picoclaw mcp add/list/show/test/edit/remove` | Config management and live diagnostics; add/remove use snapshot-plus-CAS persistence, while edit uses the same fence only before launching the external editor. | `FR-MCP-005`, `FR-MCP-006`, `FR-MCP-007` |
| Runtime     | MCP manager, canonical tool identity, and MCP tool wrapper | Connection lifecycle, collision-safe atomic factory registration, detached wrapper/factory metadata, admission-derived prompts, canonical readiness, and remote tool execution. | `FR-MCP-001`, `FR-MCP-002`, `FR-MCP-004`, `FR-MCP-011`, `FR-MCP-012`, `FR-MCP-014` |
| HTTP        | `/api/mcp*`, `/mcp/oauth/callback`            | Sanitized settings/server inventory, isolated mutations, probes, bearer management, and short-lived OAuth flows.        | `FR-MCP-007`, `FR-MCP-009`, `FR-MCP-010`               |
| Frontend    | `/agent/mcp/servers*`, `/agent/mcp/settings`  | Standard server collection plus dedicated forms, settings, testing, status, token actions, and OAuth login/reconnect.   | `FR-MCP-009`, `FR-MCP-010`, `FR-MCP-013`              |
| Integration | Docker-backed MCP streamable suite            | Real server protocol compatibility.                                                                                    | `FR-MCP-001`, `FR-MCP-004`                             |

| HTTP/UI | `/api/mcp/servers*`; `/agent/mcp/servers*`; `/agent/mcp/settings` | Typed name-addressed list/detail, revision-fenced explicit-name bulk delete with safe blockers, dedicated server routes, and isolated global settings. | `FR-MCP-009`, `FR-MCP-010`, `FR-MCP-013` |

## Algorithms And Ordering

1. Normalize server transport from config or CLI flags.
2. For stdio, build command/env/env-file transport; for remote, build
   streamable HTTP transport with headers and resolve any configured auth-store
   credential.
3. Inject a resolved remote credential as an Authorization Bearer header
   only for the configured origin, without copying the token into server config
   or management responses. Reject cross-origin redirects before the redirected
   request is sent.
4. For browser login, discover protected-resource and authorization metadata,
   dynamically register a PKCE client, publish a short-lived authorization URL,
   consume the state-bound callback once, probe the MCP server, and then
   recheck that this remains the latest flow and that the server transport, URL,
   and auth snapshot is unchanged before transactionally persisting the
   credential reference and refresh metadata. OAuth network requests disable
   proxies and pin policy-approved resolved addresses, and refresh metadata is
   captured only from actual outbound token requests.
5. Refresh expiring OAuth access tokens at runtime and atomically persist the
   replacement token while preserving a refresh token omitted by a refresh
   response. Serialize the read-refresh-write transaction with process and,
   where supported, cross-process auth-store locks; after acquiring the lock,
   re-read identity so a stale refresh cannot replace a newer credential.
6. Initialize the client session and list remote tools.
7. Canonicalize every server/tool component, preflight the full discovered set
   and all affected visible/hidden registry occupants for collisions, then
   register tools eagerly or hide them behind discovery based on
   global/per-server deferral. Retain the exact original server/tool components
   in each wrapper; an exact-identity registration refreshes its manager-bound
   wrapper.
8. On agent or workflow tool call, resolve the same canonical identity, verify
   the wrapper's exact original components against the request, forward
   arguments only on an exact match, and convert MCP content into PicoClaw tool
   result text/media.
9. Launcher mutations load the latest config, validate one MCP operation,
   preserve unrelated config and secrets, save atomically, auto-enable on the
   first add, disable after the final delete, and then report restart-required
   state separately from the saved result. The deterministic restart signature
   includes complete enabled MCP config and nonsecret auth revisions; it ignores
   disabled MCP details and never reads or hashes external bearer/OAuth token
   bytes.
10. CLI add and remove capture the update-safe configuration and its exact
    public-plus-security revision together, derive and validate one complete
    candidate, and compare-and-save against that revision. Edit performs that
    fenced save as a preflight before launching the configured editor, whose
    direct file write is outside this transaction. A preflight or structured
    mutation mismatch preserves the newer configuration and requires a reload
    before the operator decides whether to retry.

## Cross-Feature Behavior

Agent conversations consume MCP tools through the normal registry. Tool
execution handles schema export and result formatting. Runtime events expose
server and tool lifecycle. Security and isolation affect stdio process startup.

## Failure And Edge Cases

- Disabled servers are skipped.
- Launcher-managed server names reject invalid or case-insensitive duplicate
  identities.
- The launcher rejects enabled discovery with invalid TTL/result bounds or no
  matcher; runtime config defaults nonpositive bounds to five but fails
  initialization when neither BM25 nor regex is enabled.
- Missing commands, invalid URLs, and connection failures produce server failed events.
- Session-missing errors can trigger reconnection behavior.
- HTTP headers are attached only to configured remote transports.
- Missing, expired, or invalid external credentials fail without disclosing
  token material; sanitized launcher responses expose only whether auth is
  configured and usable.
- OAuth login rejects insecure non-loopback URLs, expired or replayed state,
  superseded parallel logins, local-network pivots from a public server,
  cross-origin or insecure redirects, and callbacks prepared through untrusted
  forwarded-host headers.
- Browser MCP mutations reject cross-origin requests and non-JSON mutation
  bodies, including unsaved probes that can launch a stdio process.
- Configured remote URLs reject embedded userinfo; launcher-managed custom
  headers reject reserved transport names while runtime config skips those
  overrides, and credential-bearing public HTTP is rejected.
- Changing a remote server to another origin disconnects its credential and
  does not carry blank preserved secret headers to the new origin.
- An unsaved probe for a changed origin cannot reuse saved auth or blank
  preserved secret headers from the old origin.
- OAuth refresh failures ask the operator to reconnect without exposing the
  refresh token or dynamic-client secret.
- Stdio servers reject remote-only auth configuration.
- Launcher probes do not save an unsaved server definition, and failed
  mutations do not partially update the MCP server map.
- A concurrent config or security-sidecar change makes a structured CLI
  add/remove or edit preflight stale. The command returns a revision conflict,
  preserves that newer state, and does not apply the stale snapshot. Once the
  external editor starts, conflict handling is the editor/operator's
  responsibility rather than PicoClaw CAS.
- Credential cleanup deletes only an unreferenced credential; same-origin rename
  and shared references keep the active record.
- Enabled config or auth-revision changes require a gateway restart, while
  disabled MCP details do not.
- Deferred per-server override wins over global discovery defaults.
- Distinct remote identities that normalize to the same canonical local name
  fail registration, workflow readiness, and direct workflow execution without
  exposing a partially registered set or choosing an arbitrary winner.
- A built-in, local, plugin, or different MCP tool already occupying a
  canonical name is preserved; initialization fails before exposing any new
  MCP wrapper.
- A wrapper registered at the expected canonical name but carrying different
  original server/tool components is not ready and cannot execute.

## Acceptance Evidence

| Requirement IDs                                        | Evidence                                                                                                                                                                                                                                                                                       |
| ------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-MCP-001`, `FR-MCP-002`, `FR-MCP-004`, `FR-MCP-007` | [pkg/mcp/manager_test.go](../../pkg/mcp/manager_test.go), [pkg/mcp/manager_integration_test.go](../../pkg/mcp/manager_integration_test.go), [pkg/mcp/manager_real_server_integration_test.go](../../pkg/mcp/manager_real_server_integration_test.go)                                           |
| `FR-MCP-003`                                           | [pkg/tools/search_tools_test.go](../../pkg/tools/search_tools_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [docs/reference/tools_configuration.md](../reference/tools_configuration.md)                                                                                                                        |
| `FR-MCP-005`, `FR-MCP-006`                             | [cmd/picoclaw/internal/mcp/command_test.go](../../cmd/picoclaw/internal/mcp/command_test.go), [cmd/picoclaw/internal/mcp/helpers.go](../../cmd/picoclaw/internal/mcp/helpers.go), [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [docs/reference/mcp-cli.md](../reference/mcp-cli.md) |
| `FR-MCP-008`                                           | [pkg/config/config_test.go](../../pkg/config/config_test.go), [config/config.example.json](../../config/config.example.json)                                                                                                                                                                   |
| `FR-MCP-009`                                           | [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [web/frontend/src/api/mcp.test.ts](../../web/frontend/src/api/mcp.test.ts), [web/frontend/src/components/agent/mcp/mcp-server-card.test.tsx](../../web/frontend/src/components/agent/mcp/mcp-server-card.test.tsx), [web/frontend/src/components/agent/mcp/mcp-server-form.test.ts](../../web/frontend/src/components/agent/mcp/mcp-server-form.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-MCP-010`                                           | [cmd/picoclaw/internal/mcp/command_test.go](../../cmd/picoclaw/internal/mcp/command_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go), [pkg/mcp/auth_test.go](../../pkg/mcp/auth_test.go), [pkg/mcp/network_test.go](../../pkg/mcp/network_test.go), [pkg/mcp/oauth_test.go](../../pkg/mcp/oauth_test.go), [web/backend/api/mcp_oauth_test.go](../../web/backend/api/mcp_oauth_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [web/frontend/src/api/mcp.test.ts](../../web/frontend/src/api/mcp.test.ts), [web/frontend/src/components/agent/mcp/use-mcp-oauth.test.ts](../../web/frontend/src/components/agent/mcp/use-mcp-oauth.test.ts) |
| `FR-MCP-001`, `FR-MCP-004`                             | [integration/README.md](../../integration/README.md), [integration/suites/mcp-streamable](../../integration/suites/mcp-streamable)                                                                                                                                                             |
| `FR-MCP-011`                                           | [pkg/mcp/toolname_test.go](../../pkg/mcp/toolname_test.go), [pkg/mcp/manager_test.go](../../pkg/mcp/manager_test.go), [pkg/tools/integration/mcp_tool_test.go](../../pkg/tools/integration/mcp_tool_test.go), [pkg/agent/agent_mcp_test.go](../../pkg/agent/agent_mcp_test.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [web/backend/api/workflow_runtime_test.go](../../web/backend/api/workflow_runtime_test.go) |
| `FR-MCP-012`                                           | [pkg/tools/integration/mcp_tool.go](../../pkg/tools/integration/mcp_tool.go), [pkg/tools/integration/mcp_tool_test.go](../../pkg/tools/integration/mcp_tool_test.go), [pkg/tools/mcp_factory.go](../../pkg/tools/mcp_factory.go), [pkg/tools/mcp_factory_test.go](../../pkg/tools/mcp_factory_test.go), [pkg/tools/registry_factory_test.go](../../pkg/tools/registry_factory_test.go) |
| `FR-MCP-014`                                           | [pkg/agent/agent_mcp.go](../../pkg/agent/agent_mcp.go), [pkg/agent/agent_mcp_catalog.go](../../pkg/agent/agent_mcp_catalog.go), [pkg/agent/agent_mcp_catalog_test.go](../../pkg/agent/agent_mcp_catalog_test.go), [pkg/agent/agent_mcp_catalog_runtime_test.go](../../pkg/agent/agent_mcp_catalog_runtime_test.go), [pkg/agent/agent_mcp_runtime_test.go](../../pkg/agent/agent_mcp_runtime_test.go), [pkg/agent/prompt_contributors.go](../../pkg/agent/prompt_contributors.go), [pkg/agent/prompt_test.go](../../pkg/agent/prompt_test.go), [pkg/tools/registry_transaction_test.go](../../pkg/tools/registry_transaction_test.go), [pkg/tools/registry_selection_test.go](../../pkg/tools/registry_selection_test.go) |

| `FR-MCP-013` | [web/backend/api/collection_apis_test.go](../../web/backend/api/collection_apis_test.go), [web/backend/api/mcp_test.go](../../web/backend/api/mcp_test.go), [web/frontend/src/api/mcp.test.ts](../../web/frontend/src/api/mcp.test.ts), [web/frontend/src/components/agent/mcp/mcp-server-form.test.ts](../../web/frontend/src/components/agent/mcp/mcp-server-form.test.ts), [web/frontend/tests/collection-visual.spec.ts](../../web/frontend/tests/collection-visual.spec.ts) |

## Implementation Anchors

- [pkg/mcp/manager.go](../../pkg/mcp/manager.go)
- [pkg/mcp/auth.go](../../pkg/mcp/auth.go)
- [pkg/mcp/network.go](../../pkg/mcp/network.go)
- [pkg/mcp/oauth.go](../../pkg/mcp/oauth.go)
- [pkg/mcp/toolname.go](../../pkg/mcp/toolname.go)
- [pkg/tools/integration/mcp_tool.go](../../pkg/tools/integration/mcp_tool.go)
- [pkg/agent/agent_mcp.go](../../pkg/agent/agent_mcp.go)
- [cmd/picoclaw/internal/mcp](../../cmd/picoclaw/internal/mcp)
- [web/backend/api/mcp.go](../../web/backend/api/mcp.go)
- [web/backend/api/mcp_oauth.go](../../web/backend/api/mcp_oauth.go)
- [web/frontend/src/components/agent/mcp](../../web/frontend/src/components/agent/mcp)
