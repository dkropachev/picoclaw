# Security, Credentials, And Isolation

## Feature ID

`FR-SEC`

## Behavior Summary

PicoClaw protects credentials, dashboard access, local files, network requests,
tool execution, and optional isolated subprocesses. These requirements define
security behavior that other feature specs rely on.

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
| `FR-SEC-002` | MUST | Credential store operations save, load, list, and delete provider credentials with provider/auth-method identity; provider aliases such as `copilot` are canonicalized before credential lookup or persistence; provider construction and model discovery reject stored provider-identity mismatches, and named token refresh and persistence remain bound to the normalized credential ID; provider-specific token validators reject unsupported token forms before storage; storage writes use same-directory, collision-resistant temp files before atomic rename so concurrent saves do not fail on temp-name reuse. | Auth-backed providers require durable credentials without crossing account or provider identity. |
| `FR-SEC-003` | MUST | Sensitive-data filtering redacts configured secrets from model-visible tool output when enabled. Durable external-event store construction also receives detached resolved secure-config values longer than three bytes for exact-value redaction, without logging or serializing that trusted list. | Tool results and channel-origin event text can contain credentials. |
| `FR-SEC-004` | MUST | Dashboard auth rejects unauthenticated access, uses CSRF-safe logout, and rate-limits login attempts. | Web management is sensitive. |
| `FR-SEC-005` | MUST | HTTP guard blocks private/internal targets unless explicitly allowed or proxy first-hop rules apply. | Web tools must not become SSRF primitives. |
| `FR-SEC-006` | MUST | Isolation runtime starts supported commands with configured exposed paths and fails closed on unsupported/invalid setup. | Optional isolation must not silently weaken execution. |
| `FR-SEC-007` | SHOULD | Key generation and token helpers produce unique, parseable, and revocable values for auth flows. | Auth flows need reliable primitives. |
| `FR-SEC-008` | MUST | Model-list and tool-adaptation config validation rejects unsupported provider-control values such as invalid `reasoning_effort`, invalid account-router account references, invalid model-router target references, and invalid tool-adaptation policy values before those values are persisted or used; profile-specific tool-adaptation overrides normalize provider/model identity and replace earlier duplicate identities as whole entries; account routers and model routers are stored in top-level router lists rather than as secret-bearing `model_list[]` entries; strict diagnostics tolerate deprecated `account_routers[].model` input, but runtime and output ignore it so routers remain model-agnostic. | Invalid config should fail early instead of producing unsafe or broken provider requests. |
| `FR-SEC-009` | MUST | OAuth token response parsing extracts non-secret account email claims from ID-token or access-token JWT payloads when present, preserves the email across refreshes, and leaves the email empty without failing when claims are absent or malformed. | Launcher account naming and credential metadata need stable non-secret account identity without weakening token validation or persistence. |
| `FR-SEC-010` | MUST | Generic event webhook secrets use the existing secure-string persistence path: JSON exposes only `[NOT_HERE]`, `.security.yml` stores plaintext/encrypted/file references, masked updates preserve a current value, and security-only connector entries cannot create or resurrect JSON configuration. Reference resolution follows final master enablement without touching inactive credential files, and an explicit management replacement can repair an active broken reference without resolving the old value first. Connector map keys and client-controlled durable identity fields cannot contain a configured signing secret; those conflicts fail opaquely before persistence. Enabled secrets are validated and used for exact-value durable content redaction as well as Standard Webhooks HMAC verification. | A listener credential must neither leak through config, error, identity, or event-storage surfaces nor survive as stale active configuration after its connector is removed. |
| `FR-SEC-011` | MUST | Delta Chat email automation requires both a verified sender contact and a correctly encrypted/signed message unless the connector explicitly enables unverified email. Durable metadata excludes local blob paths, remote references, and bytes; declared and streamed attachment sizes are capped before materialization; receipt time is preferred over sender-controlled mail `Date`. | Email addresses, message dates, filenames, and attachment declarations are attacker-controlled and must not silently become workflow authority or expose private account storage. |

## Data And State Model

Security state includes secure-string sentinels, credential records keyed by
provider and auth method with optional non-secret account email metadata,
dashboard password/session data, login attempt
counters, configured secret filters, private-host allowlists, isolation exposed
paths, generated token IDs, revocation metadata, and per-connector event
webhook signing secrets.

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
| Config | `events.ingress.webhooks.*.secret` | Masked JSON plus secure-YAML merge/preservation for per-connector Standard Webhooks secrets, without security-only connector resurrection. | `FR-SEC-010` |
| HTTP | `GET /api/config`, `PUT /api/config`, `PATCH /api/config` | Management reads expose `[NOT_HERE]`; omitted or masked webhook secrets preserve the current value, and a concrete replacement rotates it through the same secure persistence path. | `FR-SEC-010` |
| Storage | Credential store | Provider credential CRUD, auth method metadata, and optional non-secret account email metadata extracted from OAuth token responses. | `FR-SEC-002`, `FR-SEC-007`, `FR-SEC-009` |
| Network | Safe HTTP client and net binding helpers | Private host controls and bind behavior. | `FR-SEC-005` |

## Algorithms And Ordering

1. Normalize config and request inputs before comparing or persisting any secret
   values or optional model-list controls.
2. Preserve existing secure-string values when updates contain masked values;
   replace, clear, or reject secrets only through explicit update semantics.
3. Canonicalize provider-scoped credential aliases before lookup or persistence
   and run provider-specific token validators before saving token-backed
   credentials.
4. Parse OAuth token responses into credential records, copy non-secret email
   claims from JWT payloads when available, and retain an existing email when a
   refresh response omits it.
5. Authenticate dashboard requests before protected handlers and require POST
   semantics for logout so browser navigation cannot clear sessions.
6. Resolve HTTP targets to concrete host/IP data, deny private or internal
   destinations unless allow rules apply, then execute the request through the
   guarded client.
7. Build isolation command specs from supported runtime configuration, validate
   exposed paths, start only supported commands, and return errors rather than
   weakening to unisolated execution.
8. Merge webhook secrets only into connector names already loaded from JSON,
   defer event-secret reference resolution until final master enablement is
   known, reject signing-secret substrings in connector and client-controlled
   durable identity fields with opaque errors, validate enabled values before
   listener/storage construction, compare signatures without disclosing
   mismatch details, and pass the same secret values into recursive durable
   content redaction.
9. At durable event-store construction, collect resolved secure configuration
   values into a detached process-local list, discard values of three bytes or
   fewer to avoid redacting common text fragments, and pass the rest to exact
   recursive redaction without exposing the list through config JSON or logs.
10. Admit Delta Chat email as trusted automation only when contact verification
    and encrypted/signature authentication are both present, unless an explicit
    per-connector opt-in accepts unverified mail. Prefer provider receipt time,
    expose only bounded attachment metadata, and cap the actual copied stream
    independently of its declared size.

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

## Failure And Edge Cases

- Partial secret updates preserve old value unless an explicit clear is requested.
- Concurrent atomic writes do not fail due to temporary filename collisions.
- Unsupported provider token families, such as classic GitHub PATs for
  Copilot-backed accounts, are rejected before persistence.
- Invalid protected command patterns fail validation.
- Unsupported isolation platform returns clear error.
- Private host requests are denied unless whitelisted.
- Missing or malformed OAuth JWT email claims do not fail token parsing.
- Security YAML cannot enable, add, or resurrect a removed event webhook
  connector; malformed enabled secrets fail before the shared route is active.
- Inactive file/encrypted webhook references are preserved without resolution;
  credential-bearing connector/type/deduplication identities fail before config
  or event persistence and error responses omit the conflicting values.
- Resolved channel and provider credentials are detached only at the trusted
  event-store construction boundary; they are never added to event identity,
  payload, config JSON, or logs.
- Unverified email is skipped by default; an explicit opt-in marks it
  unverified. Private Delta Chat blob paths and copy errors do not enter durable
  events or attachment diagnostics, and oversized files are not materialized.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SEC-001`, `FR-SEC-003` | [pkg/config/config_struct_test.go](../../pkg/config/config_struct_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-SEC-002`, `FR-SEC-007` | [pkg/credential/store_test.go](../../pkg/credential/store_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/auth/token_test.go](../../pkg/auth/token_test.go), [pkg/auth/pkce_test.go](../../pkg/auth/pkce_test.go) |
| `FR-SEC-004` | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go) |
| `FR-SEC-005`, `FR-SEC-006` | [pkg/utils/http_guard.go](../../pkg/utils/http_guard.go), [pkg/isolation/runtime_test.go](../../pkg/isolation/runtime_test.go), [pkg/netbind/netbind_test.go](../../pkg/netbind/netbind_test.go) |
| `FR-SEC-008` | [pkg/config/model_config_test.go](../../pkg/config/model_config_test.go), [pkg/config/account_router_test.go](../../pkg/config/account_router_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go) |
| `FR-SEC-009` | [pkg/auth/oauth_test.go](../../pkg/auth/oauth_test.go), [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go) |
| `FR-SEC-010` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_webhook_deferred_test.go](../../web/backend/api/config_event_webhook_deferred_test.go) |
| `FR-SEC-011` | [pkg/config/events_channels_test.go](../../pkg/config/events_channels_test.go), [pkg/eventing/channelmessage/backend_test.go](../../pkg/eventing/channelmessage/backend_test.go), [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |

## Implementation Anchors

- [pkg/config/config_struct.go](../../pkg/config/config_struct.go)
- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/config/events.go](../../pkg/config/events.go)
- [web/backend/api/config.go](../../web/backend/api/config.go)
- [pkg/auth/oauth.go](../../pkg/auth/oauth.go)
- [pkg/credential](../../pkg/credential)
- [pkg/isolation](../../pkg/isolation)
