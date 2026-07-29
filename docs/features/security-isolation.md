# Security, Credentials, And Isolation

## Feature ID

`FR-SEC`

## Behavior Summary

PicoClaw protects credentials, dashboard access, local files, network requests,
tool execution, and optional isolated subprocesses. These requirements define
security behavior that other feature specs rely on. Event-derived AI
classification can explicitly remove model tool authority while leaving any
approved side effect as a separately declared workflow action.

## Reconstruction Notes

- Similarity target: recreate secret-preserving config behavior, credential
  store CRUD, dashboard auth controls, HTTP guard checks, and optional process
  isolation with fail-closed setup.
- Core types/functions: secure string config helpers, credential store,
  dashboard auth middleware, CSRF/logout handlers, HTTP guard, isolation runtime,
  token, OAuth response parsing, and PKCE helpers.
- Runtime ordering: load security config, normalize protected values, validate
  access or target, execute guarded storage/network/process operation, redact
  sensitive output, and emit clear errors.
- Non-obvious constraints: masked secure values preserve existing secrets,
  private network denial is the default, unsupported isolation does not fall back
  to unisolated execution, and generated auth tokens must remain revocable.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SEC-001` | MUST | Secure string config fields avoid plaintext exposure in launcher read paths and preserve secret values on partial updates; router model entries must not persist provider API keys, and router graph account refs are limited to non-secret credential account identifiers such as `credential:openai:work`. | Credentials must not leak through management surfaces. |
| `FR-SEC-002` | MUST | Credential store operations save, load, list, delete, and transactionally update credentials with provider/auth-method identity; provider aliases such as `copilot` are canonicalized before credential lookup or persistence; provider construction and model discovery reject stored provider-identity mismatches, and named token refresh and persistence remain bound to the normalized credential ID; provider-specific token validators reject unsupported token forms before storage. Every mutation acquires a process-local lock and, on supported Unix and Windows hosts, an OS file lock, reloads the latest store while locked, and writes through a same-directory, collision-resistant temporary file before overwrite-capable atomic replacement, including write-through replacement on Windows, so concurrent processes cannot lose unrelated credentials, fail on temp-name reuse or an existing target, or overwrite a replacement during refresh. | Auth-backed providers and MCP servers require durable credentials without crossing account identity or losing updates during concurrent launcher and gateway activity. |
| `FR-SEC-003` | MUST | Sensitive-data filtering redacts configured secrets from model-visible tool output when enabled. Durable external-event store construction also receives detached resolved secure-config values longer than three bytes for exact-value redaction, without logging or serializing that trusted list. | Tool results and channel-origin event text can contain credentials. |
| `FR-SEC-004` | MUST | Dashboard auth rejects unauthenticated access, uses CSRF-safe logout, and rate-limits login attempts. | Web management is sensitive. |
| `FR-SEC-005` | MUST | HTTP guard blocks private/internal targets unless explicitly allowed or proxy first-hop rules apply. Configured MCP URLs reject embedded credentials; credential-bearing remote servers require HTTPS except for intentional loopback development, and remote MCP redirects remain same-origin. MCP OAuth discovery, token exchange, authenticated probing, and refresh clients additionally reject cross-origin or downgrade redirects, disable environment proxies, resolve and pin an approved address into the actual dial, block private and special-use destinations from public-looking hosts, and restrict intentionally local discovery to the configured local address. | Web tools and browser-managed authentication must not become SSRF or credential-exfiltration primitives. |
| `FR-SEC-006` | MUST | Isolation runtime starts supported commands with configured exposed paths and fails closed on unsupported/invalid setup. | Optional isolation must not silently weaken execution. |
| `FR-SEC-007` | SHOULD | Key generation and token helpers produce unique, parseable, and revocable values for auth flows. | Auth flows need reliable primitives. |
| `FR-SEC-008` | MUST | Model-list and tool-adaptation config validation rejects unsupported provider-control values such as invalid `reasoning_effort`, invalid account-router account references, invalid model-router target references, and invalid tool-adaptation policy values before those values are persisted or used; profile-specific tool-adaptation overrides normalize provider/model identity and replace earlier duplicate identities as whole entries; account routers and model routers are stored in top-level router lists rather than as secret-bearing `model_list[]` entries; strict diagnostics tolerate deprecated `account_routers[].model` input, but runtime and output ignore it so routers remain model-agnostic. | Invalid config should fail early instead of producing unsafe or broken provider requests. |
| `FR-SEC-009` | MUST | OAuth token response parsing extracts non-secret account email claims from ID-token or access-token JWT payloads when present, preserves the email across refreshes, and leaves the email empty without failing when claims are absent or malformed. | Launcher account naming and credential metadata need stable non-secret account identity without weakening token validation or persistence. |
| `FR-SEC-010` | MUST | Event webhook secrets use the existing secure-string persistence path: JSON exposes only `[NOT_HERE]`, `.security.yml` stores plaintext/encrypted/file references, masked updates preserve a current value, and security-only connector entries cannot create or resurrect JSON configuration. Reference resolution follows final master enablement without touching inactive credential files, and an explicit management replacement can repair an active broken reference without resolving the old value first. Connector map keys, the JSON-owned format discriminator, and client-controlled durable identity fields cannot contain a configured signing secret; those conflicts fail opaquely before persistence. Enabled secrets are validated for their selected Standard Webhooks or GitHub format and used for exact-value durable content redaction as well as HMAC verification. | A listener credential must neither leak through config, error, identity, or event-storage surfaces nor survive as stale active configuration after its connector is removed. |
| `FR-SEC-011` | MUST | Delta Chat email automation requires both a verified sender contact and a correctly encrypted/signed message unless the connector explicitly enables unverified email. Durable metadata excludes local blob paths, remote references, and bytes; declared and streamed attachment sizes are capped before materialization; receipt time is preferred over sender-controlled mail `Date`. | Email addresses, message dates, filenames, and attachment declarations are attacker-controlled and must not silently become workflow authority or expose private account storage. |
| `FR-SEC-012` | MUST | Native GitHub webhook admission verifies `X-Hub-Signature-256` against the exact bounded raw body with the connector's secret before JSON parsing, but never represents `X-GitHub-Event` or `X-GitHub-Delivery` as signature-authenticated. The normalized envelope records the body/header distinction, public deployment requires trusted TLS termination, and no unsigned timestamp or retained delivery ID is presented as cryptographic replay prevention. | GitHub's HMAC protects payload integrity but not transport headers or freshness; workflows and operators need the actual trust boundary rather than implied authority. |
| `FR-SEC-013` | MUST | An event-derived classifier may declare `tools: none`, which removes tool definitions and model-authored tool execution from its initial request, structured-output repair, managed fallbacks, and child work. The GitHub issue-triage workflow treats signed issue/repository content as untrusted, permits model influence only through validated category and priority enums plus a comment boolean, and performs any approved effect only through a separately declared `mcp/github/add_issue_comment` step whose repository/issue identity comes from the signed body and whose text is fixed. Installation and GitHub MCP enablement are explicit, the MCP write credential remains separate from the webhook signing secret, and no classifier failure gains fallback action authority. | Payload integrity does not make user-authored issue text safe instructions, and a classifier does not need authority-bearing tools to produce a bounded decision. |

| `FR-SEC-014` | MUST | Event operator runtime routes require the gateway's process-local PID bearer using constant-time comparison. The authenticated launcher injects that credential server-side without forwarding browser cookies or authorization, maps internal authorization/stale-process failures to unavailable rather than a new login challenge, and applies same-origin checks to replay. The local CLI obtains the credential only from the owner-readable live PID file. Public event/dispatch projections have no deduplication or lease-token fields, ordinary detail omits payload, the explicit exact-payload response is non-cacheable, and all clients bound response size and error text; CLI payload output validates an object but emits its original bytes without normalization. | Durable operator data contains worker fencing credentials and potentially sensitive redacted-at-rest content; management access must not expose internal authority, become a CSRF primitive, or silently weaken during reload. |
| `FR-SEC-015` | MUST | Shared durable file primitives reject empty paths, create each missing parent directory before its child reaches a durable boundary, write a synced same-directory temporary file before atomic replacement, and durably remove a file or empty directory while preserving ordinary missing/non-empty errors. POSIX implementations sync the containing directory after creation, replacement, and removal. Windows implementations use write-through moves for directory creation and replacement, and make logical deletion durable by moving the original to a collision-resistant same-parent tombstone before best-effort tombstone cleanup. | Workflow journals and other local state must not report a committed first-directory creation, replacement, or deletion that can disappear or revert after power loss, and Windows must not depend on replacing an open file or syncing a directory handle. |

## Data And State Model

Security state includes secure-string sentinels, credential records keyed by
provider and auth method with optional non-secret account email and OAuth
refresh metadata, process and supported-host cross-process auth-store locks, dashboard
password/session data, login attempt counters, configured secret filters,
private-host allowlists, isolation exposed paths, generated token IDs,
revocation metadata, per-connector event webhook formats/signing secrets, and
explicit normalized webhook body/header trust metadata. Workflow agent requests
also carry an explicit inherited-or-none tool policy; declared MCP actions use
their ordinary independently configured credentials rather than ingress signing
secrets. The gateway PID bearer remains process-local management authority;
launcher sessions and owner-readable local CLI access can use it only through
the bounded event proxy/client, and event DTO types make lease and
deduplication credentials unrepresentable.

## Surface Ownership

Owns: CODE pkg/auth/**
Owns: CODE pkg/config/**
Owns: CODE pkg/credential/**
Owns: CODE pkg/fileutil/**
Owns: CODE pkg/isolation/**
Owns: CODE pkg/logger/**
Owns: CODE pkg/netbind/**
Owns: CODE pkg/pid/**
Owns: CODE pkg/utils/**
Owns: CONFIG.isolation*
Owns: TEST pkg/auth/*
Owns: TEST pkg/credential/*
Owns: TEST pkg/isolation/*
Owns: TEST pkg/netbind/*
Owns: TEST pkg/pid/*
Owns: TEST pkg/logger/*
Owns: TEST pkg/fileutil/*
Owns: TEST pkg/utils/*
Owns: TEST pkg/config/security*
Owns: TEST pkg/config/migration*
Owns: TEST pkg/config/config*
Owns: TEST pkg/config/gateway*
Owns: TEST pkg/config/model*
Owns: TEST pkg/config/mcp*
Owns: TEST pkg/config/multikey*
Owns: TEST pkg/config/register*
Owns: TEST pkg/config/version*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | Secure strings, `isolation.*`, filtering fields, model-list validation | Secret preservation, isolation controls, sensitive-data filtering, and early rejection of unsupported provider-control values. | `FR-SEC-001`, `FR-SEC-003`, `FR-SEC-006`, `FR-SEC-008` |
| Config | `events.ingress.webhooks.*.{format,secret}` | JSON-owned `standard`/`github` format plus masked JSON and secure-YAML merge/preservation for the corresponding per-connector secret, without security-only connector resurrection. | `FR-SEC-010`, `FR-SEC-012` |
| HTTP | `GET /api/config`, `PUT /api/config`, `PATCH /api/config` | Management reads expose `[NOT_HERE]`; omitted or masked webhook secrets preserve the current value, and a concrete replacement rotates it through the same secure persistence path. | `FR-SEC-010` |
| HTTP | `POST /webhooks/events/{connector}` with `format: github` | Exact-body HMAC-SHA256 authentication, bounded parsing, explicit unauthenticated-header metadata, and durable delivery-ID deduplication behind trusted TLS. | `FR-SEC-012` |
| HTTP / CLI | protected `/runtime/eventing/*`, launcher `/api/events*`, `picoclaw events *` | Translate authenticated launcher or owner-local PID authority into bounded live-gateway operator calls without exposing PID credentials, lease tokens, deduplication keys, or automatically fetched payloads. | `FR-SEC-014` |
| Workflow / MCP | `agent/*` with `with.tools: none`; `mcp/github/add_issue_comment` | Remove tools from every classifier model path, then permit a GitHub mutation only as a declared conditional MCP step with signed-body identity and fixed output text. The GitHub MCP server and its write credential are configured explicitly and independently from ingress authentication. | `FR-SEC-013` |
| Storage | Credential store | Provider and MCP credential CRUD, transactional refresh updates, auth/OAuth metadata, cross-process serialization on supported hosts, and optional non-secret account email metadata extracted from OAuth token responses. | `FR-SEC-002`, `FR-SEC-007`, `FR-SEC-009` |
| Storage | `pkg/fileutil` durable path operations | Durable recursive parent creation, synced same-directory atomic replacement, and durable logical removal with POSIX directory sync or Windows write-through moves. | `FR-SEC-015` |
| Network | Safe HTTP clients, MCP OAuth transports, and net binding helpers | Private/special-use host controls, DNS-pinned MCP OAuth discovery/token/probe/refresh requests, same-origin redirects, explicit local-development policy, and bind behavior. | `FR-SEC-005` |

## Algorithms And Ordering

1. Normalize config and request inputs before comparing or persisting any secret
   values or optional model-list controls.
2. Preserve existing secure-string values when updates contain masked values;
   replace, clear, or reject secrets only through explicit update semantics.
3. Canonicalize provider-scoped credential aliases before lookup or persistence
   and run provider-specific token validators before saving token-backed
   credentials. Serialize each credential mutation with a process-local mutex
   and an OS file lock where the host supports it, reload the latest store under
   that lock, and keep the lock across a refresh read-modify-write transaction
   so a concurrent launcher replacement or unrelated credential update is not
   lost.
4. Parse OAuth token responses into credential records, copy non-secret email
   claims from JWT payloads when available, and retain an existing email when a
   refresh response omits it.
5. Authenticate dashboard requests before protected handlers and require POST
   semantics for logout so browser navigation cannot clear sessions.
6. Resolve HTTP targets to concrete host/IP data, deny private or internal
   destinations unless allow rules apply, then execute the request through the
   guarded client. For credential-bearing MCP OAuth discovery, exchange,
   probing, and refresh, validate each endpoint and keep each redirect on its
   request origin, disable environment proxies, pin an approved address into
   the transport dial, and allow a private result only when the configured
   intentional-local endpoint resolves to that same address.
7. Build isolation command specs from supported runtime configuration, validate
   exposed paths, start only supported commands, and return errors rather than
   weakening to unisolated execution.
8. Merge webhook secrets only into connector names already loaded from JSON,
   defer event-secret reference resolution until final master enablement is
   known, reject signing-secret substrings in connector and client-controlled
   durable identity fields with opaque errors, validate enabled values against
   their explicit effective format before listener/storage construction,
   compare signatures without disclosing mismatch details, and pass the same
   secret values into recursive durable content redaction. For GitHub, verify
   the exact body while separately marking event/delivery headers
   unauthenticated; require TLS rather than treating HMAC or durable
   deduplication as header authentication or signature freshness.
9. At durable event-store construction, collect resolved secure configuration
   values into a detached process-local list, discard values of three bytes or
   fewer to avoid redacting common text fragments, and pass the rest to exact
   recursive redaction without exposing the list through config JSON or logs.
10. Admit Delta Chat email as trusted automation only when contact verification
    and encrypted/signature authentication are both present, unless an explicit
    per-connector opt-in accepts unverified mail. Prefer provider receipt time,
    expose only bounded attachment metadata, and cap the actual copied stream
    independently of its declared size.
11. For event-derived classification configured with `tools: none`, remove
    tools from initial, repair, managed fallback, and child model execution.
    Treat the signed GitHub body projection as untrusted data, validate the
    required enum/enum/boolean result locally, and only then evaluate the
    separately declared MCP action using signed-body identity and fixed text.
12. Wrap the live event operator controller in the gateway's existing
    constant-time PID bearer check. The launcher validates its dashboard
    session and affirmative same-origin replay metadata, allow-lists the path
    and query, overwrites client credentials with the PID token, bypasses
    environment HTTP proxies, bounds upstream responses, and maps stale
    internal authority to unavailable. Project rows
    into types without lease/deduplication fields and return payload only from
    the explicit no-store exact-text endpoint. The CLI uses the owner-readable
    PID file, validates bounded payload output without trimming or re-encoding
    it, requires deliberate replay confirmation, and never retries a replay
    POST.
13. For durable local mutation, create and persist missing directory entries
    from parent to child, sync replacement data before exposing the new name,
    and durably remove the old name. Use containing-directory sync on POSIX and
    write-through same-parent moves on Windows; a Windows deletion may leave a
    hidden tombstone after a crash, but never resurrects the original path.

## Cross-Feature Behavior

Launcher, tool execution, MCP stdio transports, providers, and web search all
depend on security behavior. Isolation can wrap command transports. Config
migration must preserve security defaults. Thread policy config shares the same
normalization and persistence path, while thread-specific behavior is owned by
the threads feature.
Workflow configuration, including definitions-directory defaulting and other
effective-value helpers, workflow tools, and workflow steps reuse the same config
validation, secret filtering, HTTP guard, and isolation policies instead of
introducing separate security controls. Model price and subscription metadata
remain non-secret config values while model credentials continue to use secure
string preservation.
Workflow template and publish transactions also reuse the shared durable
directory, replacement, and removal primitives; their multi-file journaling and
recovery policy remain owned by the workflows feature.
Git workspace configuration and tool enablement reuse the same config
normalization and defaulting path, while checkout retention, dirty preservation,
and workspace inventory security boundaries are owned by the git workspaces
feature.
Account router entries use first-class `account_routers[]` validation and
launcher normalization, and intentionally clear credential-bearing fields
because underlying account entries own secrets. Branch condition expressions are
validated as numeric account metrics and math constants, so routing decisions do
not require storing new secret material on router entries.
Generic durable event webhooks and channel-message adapters reuse secure-string
persistence and exact-value redaction; their transport lifecycle and request
contract are owned by
[Durable External Event Automation](event-automation.md).
The opt-in GitHub issue-triage workflow composes that authenticated body with
workflow-owned `tools: none` isolation and an ordinary declared MCP action. The
signing secret establishes ingress integrity only; it neither sanitizes issue
text nor supplies the MCP server's write credential.
Event operator access composes launcher authentication or local PID-file
authority with the gateway's protected runtime route. It does not add a second
database opener, listener credential, or browser-visible bearer.
MCP bearer and OAuth records reuse the shared auth store but remain keyed by
normalized server-scoped IDs, with collision-resistant forks when
rename/recreate/shared-name conflicts would otherwise reuse an active
credential. Runtime refresh and launcher replacement share the same
transactional locking boundary, while MCP-specific protocol and management
behavior remains owned by
[MCP Integration And Discovery](mcp-integration.md).

## Failure And Edge Cases

- Partial secret updates preserve old value unless an explicit clear is requested.
- Concurrent atomic writes do not fail due to temporary filename collisions.
- Concurrent auth-store writers preserve unrelated credentials; on hosts with
  OS file locking, a stale OAuth refresh cannot overwrite a credential replaced
  by another process while it waited for that lock.
- An empty durable path, failed parent-directory durability boundary, failed
  replacement, or failed logical deletion returns an error instead of treating
  the mutation as committed. A crash may leave a hidden Windows tombstone, but
  the removed original name does not reappear.
- Unsupported provider token families, such as classic GitHub PATs for
  Copilot-backed accounts, are rejected before persistence.
- Invalid protected command patterns fail validation.
- Unsupported isolation platform returns clear error.
- Private host requests are denied unless whitelisted.
- Public-looking MCP OAuth hosts cannot pivot to loopback, private, CGNAT, or
  other special-use addresses through DNS rebinding or environment proxies;
  redirects cannot change origin or downgrade HTTPS.
- A deliberately local MCP server may use loopback HTTP, but discovered OAuth
  endpoints cannot pivot to a different local address.
- Missing or malformed OAuth JWT email claims do not fail token parsing.
- Security YAML cannot enable, add, or resurrect a removed event webhook
  connector; malformed enabled secrets fail before the shared route is active.
- Inactive file/encrypted webhook references are preserved without resolution;
  credential-bearing connector/type/deduplication identities fail before config
  or event persistence and error responses omit the conflicting values.
- GitHub webhook bodies are authenticated before decoding, but event and
  delivery headers remain unauthenticated and there is no signed timestamp.
  Trusted TLS protects those headers; retained delivery-ID deduplication limits
  retries but is not cryptographic replay prevention and ends when retention
  pruning removes the original event.
- A valid GitHub body signature does not make issue title, body, author, or
  repository text trusted instructions. Unsupported no-tool modes fail
  validation; classifier output that fails its enum/boolean contract cannot
  invoke a hidden fallback action.
- Installing the issue-triage template never enables ingress or MCP. The
  declared GitHub action fails normally when its explicitly enabled,
  non-deferred MCP capability or separate write credential is unavailable; it
  does not borrow the webhook signing secret.
- Resolved channel and provider credentials are detached only at the trusted
  event-store construction boundary; they are never added to event identity,
  payload, config JSON, or logs.
- Missing/wrong PID bearer credentials never reach the event store. The
  launcher does not pass browser cookies or authorization upstream, and an
  internal `401` becomes operator unavailability rather than a dashboard login
  response. Cross-site or malformed replay requests create no event.
- Event and dispatch responses cannot include live lease tokens or
  deduplication keys through accidental struct serialization. Payload responses
  are opt-in, exact, and non-cacheable; clients render them only as text.
- Unverified email is skipped by default; an explicit opt-in marks it
  unverified. Private Delta Chat blob paths and copy errors do not enter durable
  events or attachment diagnostics, and oversized files are not materialized.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SEC-001`, `FR-SEC-003` | [pkg/config/config_struct_test.go](../../pkg/config/config_struct_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-SEC-002`, `FR-SEC-007` | [pkg/credential/store_test.go](../../pkg/credential/store_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go), [pkg/auth/token_test.go](../../pkg/auth/token_test.go), [pkg/auth/pkce_test.go](../../pkg/auth/pkce_test.go), [pkg/mcp/auth_test.go](../../pkg/mcp/auth_test.go) |
| `FR-SEC-004` | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go) |
| `FR-SEC-005`, `FR-SEC-006` | [pkg/utils/http_guard.go](../../pkg/utils/http_guard.go), [pkg/isolation/runtime_test.go](../../pkg/isolation/runtime_test.go), [pkg/netbind/netbind_test.go](../../pkg/netbind/netbind_test.go), [pkg/mcp/network_test.go](../../pkg/mcp/network_test.go), [pkg/mcp/oauth_test.go](../../pkg/mcp/oauth_test.go), [web/backend/api/mcp_oauth_test.go](../../web/backend/api/mcp_oauth_test.go) |
| `FR-SEC-008` | [pkg/config/model_config_test.go](../../pkg/config/model_config_test.go), [pkg/config/account_router_test.go](../../pkg/config/account_router_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go) |
| `FR-SEC-009` | [pkg/auth/oauth_test.go](../../pkg/auth/oauth_test.go), [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go) |
| `FR-SEC-010` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_webhook_deferred_test.go](../../web/backend/api/config_event_webhook_deferred_test.go) |
| `FR-SEC-011` | [pkg/config/events_channels_test.go](../../pkg/config/events_channels_test.go), [pkg/eventing/channelmessage/backend_test.go](../../pkg/eventing/channelmessage/backend_test.go), [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-SEC-012` | [pkg/config/events_webhook_format_test.go](../../pkg/config/events_webhook_format_test.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go) |
| `FR-SEC-013` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go) |
| `FR-SEC-014` | [pkg/health/server_test.go](../../pkg/health/server_test.go), [pkg/eventing/operator](../../pkg/eventing/operator), [pkg/gateway/event_operator_test.go](../../pkg/gateway/event_operator_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-SEC-015` | [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/fileutil/durable.go](../../pkg/fileutil/durable.go), [pkg/fileutil/durable_unix.go](../../pkg/fileutil/durable_unix.go), [pkg/fileutil/durable_windows.go](../../pkg/fileutil/durable_windows.go) |

## Implementation Anchors

- [pkg/config/config_struct.go](../../pkg/config/config_struct.go)
- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/config/events.go](../../pkg/config/events.go)
- [web/backend/api/config.go](../../web/backend/api/config.go)
- [pkg/auth/oauth.go](../../pkg/auth/oauth.go)
- [pkg/auth/store.go](../../pkg/auth/store.go)
- [pkg/mcp/network.go](../../pkg/mcp/network.go)
- [pkg/mcp/oauth.go](../../pkg/mcp/oauth.go)
- [pkg/credential](../../pkg/credential)
- [pkg/fileutil](../../pkg/fileutil)
- [pkg/isolation](../../pkg/isolation)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
