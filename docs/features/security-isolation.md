# Security, Credentials, And Isolation

## Feature ID

`FR-SEC`

## Behavior Summary

PicoClaw protects credentials at rest and across launcher, gateway, provider,
workflow, agent, tool, filesystem, and network boundaries. Secure configuration
is redacted from ordinary reads, credential identity is cross-checked before
use, browser management is authenticated and CSRF-safe, network clients reject
unsafe redirects and destinations, and command/model execution receives only
the capabilities explicitly required for its task.

Unified development work follows the same least-authority rule. The browser
sees one bounded `devw_` aggregate through authenticated launcher proxying;
private prompts, gate subjects, workflow runs, checkout paths, Git bearers,
publication markers, and provider credentials never enter that projection.
Review uses immutable exact diff evidence in an isolated no-tool model context.
Implementation uses an edit-confined local agent and exact pinned workspace;
validation, scope audit, gates, review submission, branch push, issue creation,
and merge authority are all separate.

Schema v20 destructively drops v19 workspaces and the older PR
review/development schema before creating the `devw_` model. `/development` and
`/api/development-workspaces` are the only browser families; there is no legacy
read, redirect, dual-write, ID adapter, or restored-archive mount path. Generic
workflow routes continue to treat development lifecycle Gate runs as private.

## Reconstruction Notes

- Similarity target: capability-separated secret handling, network/filesystem
  confinement, private workflow/model context, and browser-safe projection.
- Core types/functions: secure config and credential stores, dashboard auth,
  URL/network guards, immutable subprocess `ExecutionPolicy`, isolation runner,
  frozen workflow context,
  development-workspace/notification proxies, isolated AI profiles, pinned Git
  browsing/operations, Web Push state, and narrow provider publishers.
- Runtime ordering: authenticate and canonicalize; validate identity and bounds;
  acquire one exact runtime/config/workspace generation; freeze evidence;
  subprocess startup additionally freezes one policy/root projection through
  pre-start and post-start handling; execute through the narrow capability;
  postflight and persist a sanitized result; release authority before any human
  wait.
- Non-obvious constraints: signed provider payloads remain untrusted content;
  ownership and head writability are independent; an AI finding or gate result
  is evidence, not code/provider authority; unknown external outcomes are
  reconciled, not retried; destructive v20 migration has no legacy read path;
  read-only code display must remain bound to exact parked Git objects.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SEC-001` | MUST | Secure string config fields avoid plaintext exposure in launcher read paths and preserve secret values on partial updates; router model entries must not persist provider API keys, and router graph account refs are limited to non-secret credential account identifiers such as `credential:openai:work`. | Credentials must not leak through management surfaces. |
| `FR-SEC-002` | MUST | Credential store operations save, load, list, delete, and transactionally update credentials with provider/auth-method identity; provider aliases such as `copilot` are canonicalized before credential lookup, persistence, and per-account refresh locking; provider construction and model discovery reject stored provider-identity mismatches, and named token refresh and persistence remain bound to the normalized credential ID; provider-specific token validators reject unsupported token forms before storage. Every mutation acquires a process-local lock and, on supported Unix and Windows hosts, an OS file lock, reloads the latest store while locked, and writes through a same-directory, collision-resistant temporary file before overwrite-capable atomic replacement, including write-through replacement on Windows, so concurrent processes cannot lose unrelated credentials, fail on temp-name reuse or an existing target, or overwrite a replacement during refresh. Network refresh is serialized per exact credential across goroutines and supported processes but does not hold the store mutation lock; its final compare-and-swap commits only against the source snapshot, returns a concurrent launcher renewal as authoritative, and reports whether the caller actually committed response-only metadata. | Auth-backed providers and MCP servers require durable credentials without crossing account identity or losing updates during concurrent launcher and gateway activity. |
| `FR-SEC-003` | MUST | Sensitive-data filtering redacts configured secrets from model-visible tool output when enabled. Durable external-event store construction also receives detached resolved secure-config values longer than three bytes for exact-value redaction, without logging or serializing that trusted list. | Tool results and channel-origin event text can contain credentials. |
| `FR-SEC-004` | MUST | Dashboard auth rejects unauthenticated access, uses CSRF-safe logout, and rate-limits login attempts. | Web management is sensitive. |
| `FR-SEC-005` | MUST | HTTP guard blocks private/internal targets unless explicitly allowed or proxy first-hop rules apply. Configured MCP URLs reject embedded credentials; credential-bearing remote servers require HTTPS except for intentional loopback development, and remote MCP redirects remain same-origin. MCP OAuth discovery, token exchange, authenticated probing, and refresh clients additionally reject cross-origin or downgrade redirects, disable environment proxies, resolve and pin an approved address into the actual dial, block private and special-use destinations from public-looking hosts, and restrict intentionally local discovery to the configured local address. | Web tools and browser-managed authentication must not become SSRF or credential-exfiltration primitives. |
| `FR-SEC-006` | MUST | Subprocess isolation exposes an opaque `ExecutionPolicy` created from one effective `IsolationConfig`. Construction has no filesystem/process effect, recursively detaches the ordered `ExposePaths` while preserving nil versus allocated-empty compatibility, and yields a copyable concurrently reusable value; its zero value is invalid and no explicit-policy launch falls back to global/default state. Each `Start` or `Run` clones one private launch projection, resolves one absolute instance root when enabled, validates the exact config/platform/root before directory creation or command mutation, and carries the same config/root through deterministic environment and mount/access projection, platform pre-start setup, exactly one process start, post-start setup, cleanup, and wait; `Run` uses that exact start path and waits once. A constructed disabled policy remains valid on unsupported platforms and may retain dormant invalid exposure data, while invalid/zero policy, nil command, enabled unsupported platform, enabled relative root, enabled invalid or NUL-bearing exposure, missing Linux `bwrap`, and enabled Windows exposure fail before an ungoverned process starts. Linux optional system mounts are deterministic for one fixed host-filesystem view rather than identical across distributions. Child environment ordering is stable; Windows names are collapsed case-insensitively with canonical redirected home/temp/data keys taking precedence and other aliases preserving last-value semantics. Legacy nil environment application is harmless. Legacy prepare-only fails closed when Windows isolation is enabled because it cannot complete Job Object setup. Enabled Windows post-start requires exact pending restricted-token resources and fails closed so the child is terminated when those resources are missing or type-invalid. Deprecated global `Configure`/`CurrentConfig`/preflight/prepare/start/run compatibility initializes a valid disabled policy, clones config on ingress/egress, and snapshots one last-writer-wins policy per operation without holding its lock across filesystem/process work. Current production subprocess owners still use that compatibility selector: per-runtime-generation propagation, removal of agent-construction global mutation, and empty-base-plus-allowlist restricted environments are explicitly later boundaries. | Optional isolation must not silently weaken execution, mix config/root generations inside one launch, or expose mutable caller storage as live process authority. |
| `FR-SEC-007` | SHOULD | Key generation and token helpers produce unique, parseable, and revocable values for auth flows. | Auth flows need reliable primitives. |
| `FR-SEC-008` | MUST | Model-list and tool-adaptation config validation rejects unsupported provider-control values such as invalid `reasoning_effort`, invalid account-router account references, invalid model-router target references, and invalid tool-adaptation policy values before those values are persisted or used; profile-specific tool-adaptation overrides normalize provider/model identity and replace earlier duplicate identities as whole entries. Config schema v5 makes exact, unique `model_aliases[]` the only user-facing model selectors: each alias has a non-empty concrete default mapping, optional `account_overrides` and `disabled_accounts` may name only enabled concrete model-list or supported credential accounts and never an account or model router, and the same account cannot appear in both. Every account reachable through a router must be provider-compatible with every alias it can receive, while explicitly disabled alias/account pairs are excluded from that router's runnable candidates. Account routers and model routers are stored in top-level router lists rather than as secret-bearing `model_list[]` entries; agent and subagent model persistence preserves inherited fallbacks separately from an explicit empty fallback list; strict diagnostics tolerate deprecated `account_routers[].model` input, but runtime and output ignore it so routers remain model-agnostic. Default configuration seeds neither a runnable account nor a model alias, and an empty effective selection fails locally with `no model configured` before any provider request. Migration from schemas v1-v4 separates `account_ref` from alias-valued `model_name`, promotes only unambiguous explicitly configured legacy models, and never invents a provider or model default. | Invalid config should fail early instead of producing unsafe or broken provider requests, and persistence must not silently broaden an explicit model policy or delegate selection to an upstream default. |
| `FR-SEC-009` | MUST | OAuth token response parsing extracts non-secret account email claims from ID-token or access-token JWT payloads when present, preserves the email across refreshes, and leaves the email empty without failing when claims are absent or malformed. | Launcher account naming and credential metadata need stable non-secret account identity without weakening token validation or persistence. |
| `FR-SEC-010` | MUST | Event webhook secrets use the existing secure-string persistence path: JSON exposes only `[NOT_HERE]`, `.security.yml` stores plaintext/encrypted/file references, masked updates preserve a current value, and security-only connector entries cannot create or resurrect JSON configuration. Reference resolution follows final master enablement without touching inactive credential files, and an explicit management replacement can repair an active broken reference without resolving the old value first. Connector map keys, the JSON-owned format discriminator, and client-controlled durable identity fields cannot contain a configured signing secret; those conflicts fail opaquely before persistence. Enabled secrets are validated for their selected Standard Webhooks or GitHub format and used for exact-value durable content redaction as well as HMAC verification. | A listener credential must neither leak through config, error, identity, or event-storage surfaces nor survive as stale active configuration after its connector is removed. |
| `FR-SEC-011` | MUST | Delta Chat email automation requires both a verified sender contact and a correctly encrypted/signed message unless the connector explicitly enables unverified email. Durable metadata excludes local blob paths, remote references, and bytes; declared and streamed attachment sizes are capped before materialization; receipt time is preferred over sender-controlled mail `Date`. | Email addresses, message dates, filenames, and attachment declarations are attacker-controlled and must not silently become workflow authority or expose private account storage. |
| `FR-SEC-012` | MUST | Native GitHub webhook admission verifies `X-Hub-Signature-256` against the exact bounded raw body with the connector's secret before JSON parsing, but never represents `X-GitHub-Event` or `X-GitHub-Delivery` as signature-authenticated. The normalized envelope records the body/header distinction, public deployment requires trusted TLS termination, and no unsigned timestamp or retained delivery ID is presented as cryptographic replay prevention. | GitHub's HMAC protects payload integrity but not transport headers or freshness; workflows and operators need the actual trust boundary rather than implied authority. |
| `FR-SEC-013` | MUST | An event-derived classifier may declare `tools: none`, which removes tool definitions and model-authored tool execution from its initial request, structured-output repair, managed fallbacks, and child work. The GitHub issue-triage workflow treats signed issue/repository content as untrusted, permits model influence only through validated category and priority enums plus a comment boolean, and performs any approved effect only through a separately declared `mcp/github/add_issue_comment` step whose repository/issue identity comes from the signed body and whose text is fixed. Installation and GitHub MCP enablement are explicit, the MCP write credential remains separate from the webhook signing secret, and no classifier failure gains fallback action authority. | Payload integrity does not make user-authored issue text safe instructions, and a classifier does not need authority-bearing tools to produce a bounded decision. |
| `FR-SEC-014` | MUST | Event operator runtime routes require the gateway's process-local PID bearer using constant-time comparison. The authenticated launcher injects that credential server-side without forwarding browser cookies or authorization, maps internal authorization/stale-process failures to unavailable rather than a new login challenge, and applies same-origin checks to replay. The local CLI obtains the credential only from the owner-readable live PID file. Public event/dispatch projections have no deduplication or lease-token fields, ordinary detail omits payload, the explicit exact-payload response is non-cacheable, and all clients bound response size and error text; CLI payload output validates an object but emits its original bytes without normalization. | Durable operator data contains worker fencing credentials and potentially sensitive redacted-at-rest content; management access must not expose internal authority, become a CSRF primitive, or silently weaken during reload. |
| `FR-SEC-015` | MUST | Shared durable file primitives reject empty paths, create each missing parent directory before its child reaches a durable boundary, write a synced same-directory temporary file before atomic replacement, and durably remove a file or empty directory while preserving ordinary missing/non-empty errors. POSIX implementations sync the containing directory after creation, replacement, and removal. Windows implementations use write-through moves for directory creation and replacement, and make logical deletion durable by moving the original to a collision-resistant same-parent tombstone before best-effort tombstone cleanup. | Workflow journals and other local state must not report a committed first-directory creation, replacement, or deletion that can disappear or revert after power loss, and Windows must not depend on replacing an open file or syncing a directory handle. |
| `FR-SEC-016` | MUST | Authenticated workflow definition and built-in-template inspection reads one bounded exact source; a published read opens the configured definition nonblocking through workspace- and definitions-root-confined handles and verifies the opened handle is regular, so neither a symlink race nor a swap to a FIFO can escape or indefinitely hold mutation boundaries. It releases the cross-process file lock before parsing and the handler-local config mutation lock before encoding or response writing. The response contains only path-free whitelisted trigger fields, declaration names and required/default-presence metadata, declared action targets, fixed validation/limit codes, and conservative possible-effect classifications. Whole-family trigger preflight, aggregate entry limits, bounded topology fields, rejection of control and Unicode format characters, and a fixed encoded-response ceiling prevent YAML aliases or invalid definitions from amplifying or visually spoofing a bounded review response. The response cannot represent raw YAML, prompts, arbitrary `with` or `if` values, session or delivery values, input default values, secret values or mappings, output expressions, filesystem paths, captured event payloads, or raw parser/filesystem/provider errors. Every truncation or field omission is explicit, dependency/effect aggregation is independent from topology truncation, and a known effect class survives target omission; unknown or reusable actions cannot be presented as read-only. | A convenient browser review surface must not become a definition-file reader, sensitive-value oracle, or false assurance that an automation is side-effect-free. |
| `FR-SEC-017` | MUST | Workflow-authoring capability discovery runs only against the existing PID-bearer-protected gateway loop while a runtime-use lease pins its generation; the authenticated launcher reads process authority without cleaning a stale PID file or loading, migrating, backing up, or saving configuration, substitutes the credential on one exact bounded GET, and never forwards browser credentials or upstream error detail. Projection requires agent IDs in the runtime's exact canonical `[a-z0-9][a-z0-9_-]{0,63}` form, sorts and validates every other exact UTF-8 identity, rejects control and Unicode format characters, uses the default agent's effective core registration keys, excludes the recursive workflow tool, and derives MCP targets only from exact separator-safe server/tool identity rather than a lossy provider-facing name. Tool and MCP parameter maps are untrusted: calls are panic-contained and transactionally projected into an ordered typed whitelist containing only fixed type, property/required membership, items, bounded scalar enum, and additional-properties shape. Non-whitelisted schema metadata is outside that DTO rather than a partially projected constraint. Bounded source selection plus shared collection, depth, property, enum, numeric-text, work, and encoded-response limits prevent cycles, aliases, panic loops, huge registry copies, oversized numeric parsing, or many discarded shapes from amplifying a response; every removed identity, omitted whole shape, and collection truncation has a fixed code, and an unsafe structural declaration omits its shape whole. Responses cannot contain agent/tool/MCP descriptions, prompts, raw schema maps, defaults, examples, constants, patterns, formats, references or compositions, provider/model/MCP configuration, URLs, commands, headers, environment, credentials, source paths, durable state, or raw runtime/proxy/panic errors. Discovery never initializes or connects MCP, emits MCP lifecycle events, or refreshes OAuth credentials: disabled MCP is a complete empty category; enabled MCP includes only a live collision-free manager's ready tools; a live identity-colliding manager produces an empty partial MCP category with a fixed unsafe-omission code; and no live manager produces a fixed unavailable partial state. It cannot construct another agent loop, edit configuration or YAML, create sessions/runs, execute a capability, or perform a durable mutation. | Production-equivalent discovery must not become a prompt, configuration, filesystem, credential, or denial-of-service oracle, and a read-only UI must not silently change persistent infrastructure. |
| `FR-SEC-018` | MUST | Authenticated structured job/action inspect and render routes strictly decode one bounded JSON object containing caller-supplied exact YAML, and render additionally requires its nonblank opaque revision plus exactly one allow-listed typed operation; unknown or trailing JSON, duplicate object members, null required members, invalid UTF-8, unpaired JSON surrogate escapes, unsafe/browser-inexact numbers, excessive nesting, collections, strings, YAML, operation work, validation detail, or encoded output fail closed through fixed public codes. The server hashes and parses only those supplied bytes, never reads a configured definition, active development session, configuration, PID authority, capability registry, credential, provider, filesystem path, event, or run. Ordered projection and rendering operate on YAML nodes rather than flattened maps: version/tag directives that the AST cannot retain, anchors/aliases, merge keys, unsafe tags, duplicate or non-string structural keys, malformed containers, and normalization-prone values never enter typed mutation state. Step IDs and every `needs` job-identity reference use the fixed 256-byte single-line identity bound. Global source directives, anchors/aliases, container ambiguity, complexity exhaustion, `jobs_truncated`, or `steps_truncated` blocks all operations; aggregate `validation_truncated` only marks diagnostics incomplete and does not block an otherwise structurally safe operation. Outside a global block, `unsafe_fields_omitted` can mark local raw-only state, a locally raw-only job rejects patch/delete, a locally raw-only step rejects patch/delete/move, and an editable step cannot move across a raw-only step in its inclusive source-to-destination span; other safe sibling edits or structurally safe insertions remain available. One successful render fences the exact revision before typed operation decoding, mutates only a transient AST copy, preserves unrelated unknown nodes/comments/order/style, returns original bytes for a semantic no-op, and emits only data derived from the caller's source—plus candidate YAML after render—through a bounded typed projection, fixed limits, and sanitized validation; raw parser/runtime/filesystem/provider errors are never returned. A set-only job-ID rename rejects collisions and replaces only the mapping key scalar, never performs a broad textual rewrite, and never implicitly retargets `needs` or authority-bearing expressions. Inspect, render, tab selection, capability selection, and effect review cannot save a draft or config, initialize or invoke a capability, create a session/run, or mutate runtime or durable state. Ready catalog targets are suggestions only and exact manual targets remain untrusted. The draft-test review conservatively treats every tool, MCP, agent, native function, local reusable workflow, unrecognized/manual target, raw-only action, and incomplete or empty projection as potentially effectful; its acknowledgement is component-local and keyed to the exact YAML/scenario/review identity, is cleared on any identity change, and is rechecked at final confirmation before the separate existing test endpoint may execute. | Draft editing handles attacker- and model-authored YAML; it must not become an alias-expansion denial of service, a secret/configuration oracle, a stale-consent execution path, or a new way to exercise runtime authority. |
| `FR-SEC-019` | MUST | Authenticated trigger simulation and confirmed draft execution strictly decode one bounded tagged JSON object and reject unknown, duplicate, trailing, null-required, invalid UTF-8, unpaired-surrogate, browser-inexact-number, over-depth, over-collection, over-string, over-YAML, or oversized input with fixed public errors. Simulation parses only the supplied exact draft plus an ID-selected protected event when applicable, shares production trigger/context helpers, and returns only fixed match/suppression codes, bounded scenario metadata, the provided secret count without names, conservative action effects, completeness, validation, and an opaque review token; it never reflects secret names or values, protected event or runtime payloads, raw YAML, delivery internals, filesystem/configuration/provider errors, credentials, or runtime authority. Both routes obtain one current-schema config and exact public-plus-security revision through a read-only snapshot; a legacy schema fails closed without migration, backup, or save, while protected-event authority comes from a bounded PID-file peek or captured process record and never performs stale-PID cleanup. The token is an HMAC under a process-local random key over length-delimited exact session and draft fences, prompt, target, YAML bytes, trigger/index, normalized typed scenario including secret values, server-derived review, exact public-plus-security config revision, and, for a protected event, a digest of the exact server-loaded redacted envelope; restart invalidates it. Payload, MAC, and nonce encodings must be canonical, MAC comparison is constant time, and the consumed identity hashes the length-delimited decoded payload and MAC so textual base64 aliases cannot bypass one-use admission. Confirmed execution rejects caller-origin, caller-effect, and sync/async controls, repeats and re-simulates the exact reviewed request, and reloads protected event state. A preflight-invalid token, request, initial session/draft fence, trigger, protected event digest, config revision, match, or review fails before runtime construction. Execution constructs only an unpruned lazy runtime; final token expiry, config, session, candidate-validation, and running-test fences repeat under the workflow/config mutation locks immediately before durable run creation or development mutation, so a concurrent final-fence failure closes the unused runtime without creating a run, writing development state, or pruning retained history. A token admits at most one durable run even when its development claim or HTTP response projection fails, and every post-create response is a bounded `202` with the run ID plus a fixed omission or degraded-reconciliation marker when the full session cannot be returned. Browser consent remains component-local, token- and exact-identity-bound, clears synchronously on any identity change, and is disabled after one confirmation. | Draft scenarios contain attacker-controlled messages, runtime metadata, event IDs, secrets, and authority-bearing actions; preview and confirmation must not become a secret/payload oracle, side-effecting matcher, stale-consent path, or forged run-origin channel. |
| `FR-SEC-020` | MUST | Agent capability reads and mutations resolve the runtime-equivalent workspace, open bounded regular `AGENT.md` and legacy `AGENTS.md` state without following symlinks or blocking on special files, and fence exact active bytes plus the public-and-security config generation. Existing-file commits on a supported host bind the intended candidate identity and bytes, use an atomic entry exchange, validate the exact displaced file, and establish the platform durability boundary before best-effort displaced-file cleanup. Conflict recovery repeats the exchange when a newer edit races restoration so that edit returns to the canonical path; any entry that cannot safely be recovered is retained at a logged operator-visible path. Create and legacy-upgrade commits atomically require an absent current file, recheck the alternate legacy source after creation, and conditionally quarantine only the generated file on conflict. A platform without a handle-safe primitive—including Windows while `ReplaceFileW` cannot provide no-follow target binding—projects a fixed read-only issue instead of accepting a save that cannot be safe. Structured YAML-node mutation rejects normalization-unsafe, malformed, and unterminated documents, whose runtime tools, MCP, and structured tasks fail closed, and preserves unrelated nodes, comments, order, style, exact permissions, and body; legacy upgrade requires an explicit acknowledgement and leaves the legacy file untouched. Capability catalogs expose only fixed bounded fields and never paths, URLs, commands, arguments, environment, headers, auth state, credentials, or raw parser/filesystem/provider errors. Per-agent activity similarly filters a fixed typed event allowlist before its bounded queue; the gateway writes a concrete numeric address selected from the listener that actually opened, including a single-stack localhost fallback, and the launcher proxy peeks rather than repairs PID authority, rejects hostname and wildcard PID authority, sends the bearer only to loopback or a literal local-interface address through a no-proxy/no-redirect bounded client, forwards no browser credentials or headers, strictly revalidates the upstream DTO, and returns fixed local errors. | Workspace editing and live telemetry combine attacker-controlled files, runtime payloads, and process authority; management convenience must not create a symlink/FIFO escape, lost update, secret/configuration oracle, credential-forwarding path, or bearer exfiltration primitive. |
| `FR-SEC-021` | MUST | Whole-config and scoped development-policy writes use one exact public-plus-security revision under the shared process/advisory lock. The scoped API replaces only `development`, strictly validates verified repository descriptors, named Workflow configurations, exact `(workflow-ref, gates.<id>)` bindings, complete action overrides, repository assignments, nudge bounds, scope thresholds, per-type strict/relaxed disposition, bounded custom prompts, and deferred mode, and returns independent catalog/config revisions plus restart effect. Reads never migrate or save, stale writes change nothing, and configuration never executes a Gate or external effect. Retired configuration and Gate V2 serialized fields are rejected. | Configuration concurrency must not lose secrets, broaden policy, or become execution authority. |
| `FR-SEC-022` | MUST | Frozen-session media capture and materialization treat the complete locator batch and serialized `FrozenSet` as untrusted. They admit at most 32 locator occurrences, 16 distinct nonempty assets, 2 MiB decoded bytes per asset, 3 MiB decoded bytes counted per occurrence, and 5 MiB for both materialized encoding and frozen-set JSON. Occurrence counting happens before snapshot cloning; complete locator and supplied-metadata shape validation happens before any live read, and decoded/read/materialized bounds never trust declared lengths. At most four `FreezeInputs` calls hold capture admission concurrently; an excess caller waits only until a slot is available or its context is cancelled. A live `media://` snapshot retains store lifecycle synchronization while it safely opens and validates a regular handle and executes one bounded read: Unix uses no-follow/nonblocking open plus a status-change token, Windows rejects every handle carrying the reparse-point attribute and compares handle change time, and other platforms fail closed. Registration and final deletion use cleaned absolute exact lexical lifecycle keys without an approximate case fold. One live key coalesces only the same captured entry identity. A `SameFile` identity found under a distinct key is not coalesced because it may be a hard link; instead, all such live lifecycles become non-deleting. Re-registration permanently cancels older pending deletion through either its exact key or captured `SameFile` identity, and deletion rechecks `Lstat`/`SameFile` so an already replaced entry is preserved. These operations share store synchronization, so an old cleanup cannot delete a newly registered or read-pinned path. Only canonical frozen identities, canonical `media://` UUID capabilities, and `data:` locators with canonical parameter-free MIME plus canonical padded base64 are accepted. Raw filenames are bounded at 4 KiB before basename sanitization to valid UTF-8 without controls and at most 255 bytes; supplied MIME is at most 127 bytes and captured MIME is bounded at 1 KiB before canonicalization to at most 127 bytes. Frozen records have one supported explicit version, deterministic unique identities/order, bounded canonical metadata, exact decoded sizes, and content digests; strict decode also rejects invalid UTF-8 and unpaired JSON surrogates, and decode/use reject unknown, duplicate, missing, unused, reordered/noncanonical, trailing, digest/size-inconsistent, reference-inconsistent, or authoritative occurrence-metadata-inconsistent state before returning rewritten history. Snapshot/freeze/materialize failures use fixed bounded classification and omit locator text, local paths, filenames/content types, decoded or encoded payload bytes, and raw filesystem/JSON/base64 errors. Cancellation or any failure returns no partial snapshot, reference list, set, or materialized output and leaves caller-owned inputs unchanged. | Media locators can name private temporary files and carry attacker-sized inline content; restart-safe capture must not become a symlink/special-file read, resource-amplification path, tampered-context downgrade, or diagnostic data leak. |
| `FR-SEC-023` | MUST | A compiler-private Gate V3 context is admitted only for the exact trusted in-memory workflow snapshot, static gate catalog, normalized private values, and unexported compiler stamps. `gate/exec` freezes workflow, gate, subject, selected complete action, and revisions before durable creation. Human and composed action-workflow continuation survives restart with exact task/input/idempotency fences. Private action workflows admit only `gate/exec` and safe no-tool AI/default actions; tool, MCP, arbitrary function, reusable, delivery, secret, and broader session authority are rejected. Deterministic child identity and parent binding reconcile a crash between child creation and parent proxy persistence without duplicating work or permitting cross-run splicing. Every default JSON/event/browser boundary redacts roots, child/task identity, outputs, errors, frozen media, and continuation metadata; only the bounded static Gate form and validated field values are deliberately declassified. | Frozen evidence and mixed Gate execution must survive waits without exposing PR data or turning a configurable workflow into arbitrary effect authority. |
| `FR-SEC-053` | MUST | Development-workspace, notification, Workflow-configuration, and repository-assignment browser access uses canonical authenticated launcher routes and the exact PID-bearer-protected gateway subtree. `/api/development-workspaces*`, `/api/notifications*`, `/api/notification-views`, `/api/notification-settings`, and `/api/push-subscriptions*` reject path aliases, unsafe/repeated queries, unsupported encoding/method/content, oversized JSON, browser credentials, and cross-site mutations; the launcher injects only the process bearer into one bounded no-proxy/no-redirect local request and returns non-cacheable, nosniff JSON. Public Gate/notification/device DTOs omit private subjects, prompt context, workflow/task identity, checkout/Git capability, provider credentials, push endpoints/keys, VAPID private key, and raw errors. | Unified browser access must not become a private-workflow oracle, credential relay, push-secret leak, or local checkout capability. |
| `FR-SEC-054` | MUST | Review, implementation, completion, scope audit, development Ask/Steer, and Gate V3 actions operate on exact frozen evidence. Review/completion/scope/Ask/classification AI and AI gate actions are schema-bounded and no-tool; private-session AI uses only an explicitly captured owner-matched snapshot. A supplied chat candidate must still equal the latest privately fenced browsable repair. Deterministic and Human actions receive no ambient session. The application interprets validated fields and queued in-charter steering without allowing them to widen Git, provider, workflow, or tool authority; scope-changing steering becomes clarification rather than an edit. The dedicated hard-scope form has no approval option and cannot be replaced by the ordinary scope form. | Shared context must improve consistency without letting text or a model conclusion expand authority. |
| `FR-SEC-055` | MUST | Review submission, branch push, deferred-issue creation, and ambiguous-effect reconciliation use distinct narrow adapters and application-specific Gate V3 values over exact workspace/head evidence. Private fences are durable before effects, unknown effects require exact reconciliation, and no gate value authorizes a different effect. Schema v20 destructively removes v19 workspace data and exposes only `devw_`, `/development`, `/api/development-workspaces`, and Gate V3; it has no legacy table reader, gate-decision/V2 task, old route, identifier translation, config placeholder, permissive record downgrade, or compatibility redirect. | External writes and destructive cutovers require explicit non-composable authority and must not create duplicate effects or covert legacy surfaces. |
| `FR-SEC-056` | MUST | Code tree/blob/diff routes are authenticated GET-only views over the latest repair carrying a private publication fence and nonempty candidate SHA. Every request allow-lists query keys and binds the requested candidate/base revision to the fence. Git browsing holds the inventory and parked-line lock, verifies exact line ID/version/base/tip/tree and workspace ownership before and after Git plumbing, disables submodule recursion, and times out. Paths must be canonical relative repository paths without traversal or controls. Tree output is NUL-safe, sorted, capped at 8 MiB and 500 entries with an opaque continuation. Blob reads use `ls-tree`/`cat-file`, cap content at 1 MiB, require UTF-8 without NUL, and reject symlink mode `120000`, submodule mode `160000`, and every non-blob. Diff output comes from exact private candidate evidence and path filtering cannot broaden it. The frontend lazy-loads Monaco with `readOnly`, `domReadOnly`/`originalEditable: false`, disposes every model on path/revision change, uses side-by-side desktop and inline mobile rendering, and falls back to a read-only plain-text diff on user choice, worker failure, or editor error. No code endpoint or Monaco action can edit, save, execute, commit, or publish. | Source inspection must not become traversal, special-object reading, stale-candidate confusion, checkout access, or browser edit authority. |
| `FR-SEC-057` | MUST | Web Push state persists VAPID private material, subscription endpoint, `auth`, `p256dh`, delivery generations, and last delivery only in private eventing state; public settings expose at most the VAPID public key/privacy toggle/version and public devices expose only ID, name, enabled/version, and timestamps. Subscription creation requires a bounded HTTPS endpoint and canonical bounded base64url keys; device updates/deletes are revision-fenced. Only newly opened critical/high notification generations are sent, once per enabled device; resolved, low/medium, snoozed, or already delivered generations do not create push authority. Payload content is the fixed PicoClaw title, bounded reason, notification identity, and repository only after explicit opt-in—never issue/brief/PR text, summary, source URL, prompt, candidate, credential, or provider response. Gone/not-found endpoints are disabled. Browser permission is requested only by explicit user action, and notification clicks reveal detail only after launcher authentication. | Lock-screen delivery and durable endpoint keys must not become a workspace-content leak, unsolicited permission prompt, or duplicate nag channel. |

## Data And State Model

Secrets live in the security sidecar or credential store and are represented by
redacted sentinels in public configuration. Process bearer, CSRF/session state,
OAuth state, signing secrets, provider tokens, operation leases, Git reservation
bearers, workflow private roots, and publication markers are authority-bearing
and never ordinary DTO fields.

An `ExecutionPolicy` is an in-memory opaque handle to a recursively detached
isolation-config snapshot; it is not serialized and exports no mutable config or
path slice. The zero handle is invalid. Every launch derives a separate
ephemeral projection containing the exact detached config and actual platform;
when isolation is enabled, it also contains the single resolved instance root
used by validation, preparation, process start, and post-start handling. The
deprecated process-global compatibility store holds
one constructed policy and remains last-writer-wins, but readers retain one
immutable handle before releasing its lock. That compatibility state is not a
per-agent or per-runtime-generation ownership boundary.

A public development workspace may contain verified provider display facts, charter,
findings, corrections, scope grades and measurements, nudge summaries, deferred
groups, validation summaries, public Gate state/static forms/field values,
publication state, ordered Ask/Steer status, safe result URLs, activity, and
optimistic version. Private persisted state may
add prompt/spec/subject digests, exact diff/candidate evidence, gate run roots,
operation and publication fences, and reconciliation markers. Storage adapters
must preserve that private/public distinction across restart.

Development work type, scope distance, size, ownership, writability, provider capability,
validation, and application-specific Gate values are separate checks. No
boolean is inferred from another. Human waiting state retains no live runtime
lease.

Schema v20 contains no legacy `pr_review_*`/`pr_development_*` runtime and does
not retain v19 workspace rows. Versions 18 and 19 are validated then
destructively cut over; versions 1–17 and corrupt/future schemas fail closed.
An archive of pre-cutover records is external operator data, not
runtime-readable state.

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
| Config | secure strings and `.security.yml` | Redacted public values, encrypted/file-backed secret persistence, masked update preservation, and exact identity binding. | `FR-SEC-001` through `FR-SEC-003`, `FR-SEC-008`, `FR-SEC-010` |
| Credential | credential store | Canonical provider/auth/account identity with locked atomic persistence and refresh. | `FR-SEC-002`, `FR-SEC-009` |
| Network | URL guard, pinned dialers, no-proxy/no-redirect clients | Reject credential leaks, unsafe redirects, public-to-private DNS resolution, and untrusted process authority. | `FR-SEC-005`, `FR-SEC-014`, `FR-SEC-017`, `FR-SEC-019`, `FR-SEC-053` |
| Process | `ExecutionPolicy.Start` / `ExecutionPolicy.Run` and deprecated global wrappers | One detached config/root projection survives validation, platform pre-start, exactly one start, post-start, cleanup, and wait without global rereads; invalid or unsupported enabled launches fail closed. | `FR-SEC-006` |
| Workflow | private gate root and frozen session/media | Admit only compiler-stamped bounded context; redact default JSON/event/browser views; resume exact frozen evidence. | `FR-SEC-022`, `FR-SEC-023`, `FR-SEC-054` |
| HTTP | `/api/development-workspaces*`, `/runtime/eventing/development-workspaces*` | Same-origin authenticated launcher authority replacement and bounded public aggregate/conversation/code projection. | `FR-SEC-053`, `FR-SEC-056` |
| HTTP/PWA | notification/settings/view/subscription APIs and Web Push | Authenticated inbox plus privacy-minimal generation-deduplicated push backed by private subscription/VAPID state. | `FR-SEC-053`, `FR-SEC-057` |
| Git/UI | code tree/blob/diff and read-only Monaco | Exact parked-line Git-object inspection with canonical paths, bounded text, stale-fence rejection, and no edit action. | `FR-SEC-056` |
| Runtime | isolated review/scope/completion AI and edit-only repair | No ambient session, tools, provider fallback, or authority beyond the injected exact evidence/capabilities. | `FR-SEC-054` |
| Provider/Git | review, branch, issue, and reconciliation adapters | Independent least-authority external effects with private durable fences and unknown-outcome handling. | `FR-SEC-055` |

## Algorithms And Ordering

Secure config and credential operations normalize identity before lookup,
acquire process and file locks, reload while locked, validate secret/provider
binding, write a synced same-directory temporary file, atomically replace, and
return only redacted metadata.

Explicit subprocess startup rejects an invalid zero policy or nil command,
clones one private policy snapshot, and, when enabled, rejects unsupported
platforms before resolving and retaining one absolute instance root. It then
validates exposed paths and the platform projection, prepares instance
directories, deterministically projects the existing child environment, applies
platform pre-start isolation, and starts the command exactly once. A start error
cleans pending native resources. Post-start processing consumes the same retained
config/root; failure cleans resources and terminates and waits for the child.
`Run` invokes this exact start path and then waits once. Linux filters optional
system mounts against the fixed host view observed for that launch. Windows
rejects every nonempty exposure before token allocation and case-folds logical
environment names before canonical redirected values are applied.

Deprecated global entry points acquire one immutable policy handle, release the
selection lock, and delegate to the same internal ordering. They may select
either complete generation during a concurrent reconfiguration, but never mix
generations inside one operation. They retain ambient child variables and
last-writer-wins process-global selection until subprocess owners receive the
policy attached to their exact runtime/config generation and restricted
environments are built from an empty base plus explicit allowlists.

Browser operations authenticate first. Mutations additionally validate
same-origin provenance. The PR proxy canonicalizes and bounds the complete
request before peeking process metadata, then uses one no-proxy/no-redirect
numeric-local request with only the process bearer and safe headers. It validates
status, content type, JSON, response size, and any reprojected external URL.

Review resolves and cross-binds provider identity, observes exact head, obtains a
bounded exact diff, and observes again before the isolated model sees it.
Implementation acquires the exact pinned workspace and operation lock, runs only
confined edit tools, revalidates after the model, snapshots/validates/audits the
candidate, and releases mutation authority before Human waits. Gate compilation
freezes only the context allowed by the resolved complete action.

Code inspection starts from the latest repair's private publication fence,
locks and verifies the parked development line, maps only `base` or its exact
base/tip SHA, and reads repository Git objects rather than arbitrary checkout
paths. It repeats parked-line verification after bounded plumbing and returns
only canonical paths and UTF-8 text. Monaco consumes that DTO read-only and
owns no endpoint capable of writing it.

Notification projection derives actionable rows from durable Gate, blocker,
steering, and publication state. Push delivery occurs only on a newly open
critical/high generation, loads private subscription/VAPID state, omits private
fields from every management DTO, records successful per-device generation,
and disables permanently gone endpoints without resolving the inbox row.

Before any external write, persist exact authorization and reconciliation
evidence. Invoke only the matching narrow adapter. On ambiguity, store unknown
and require a separate gate plus exact marker/head reconciliation. Never turn
an error string or model result into retry authority.

## Cross-Feature Behavior

Durable External Event Automation owns development aggregate, notification, and publication
state. Workflows owns private gate compilation and frozen continuation. Git
Workspaces owns checkout and branch fences. Agent Conversations owns model and
edit-tool isolation. Launcher Management owns browser authentication and local
proxy composition. Security defines the boundaries all of them must preserve;
it does not advance lifecycle state.

Hooks and Tool Execution own the model-action policy seam. Shared security
configuration exposes process-hook transform/respond authority only through the
explicit `hooks.processes.*.trusted` boolean: omission/false remains untrusted,
transport/source never implies trust, and the hook runtime still applies exact
offered/profile/policy/approval checks before a synthetic result or registry
effect. This is an administrative capability declaration, not an OS sandbox or
a substitute for later network/process isolation.

The explicit isolation policy is the subprocess-start capability, but current
shell/background, Cron-through-exec, process-hook, stdio MCP, and CLI-provider
owners still select it through deprecated process-global compatibility. A
follow-up boundary must store the policy on the exact runtime/config generation,
pass it to every one of those owners, remove agent-construction `Configure`, and
replace ambient-environment inheritance with an empty base plus an explicit
allowlist. The current contract does not claim that concurrent agent generations
select different policies, that isolation is default-on, or that Linux network/
PID namespaces, Windows filesystem remapping/ACLs, or a macOS backend exist.

Signed webhooks authenticate bytes, not author intent. A confirmed charter
authorizes a product scope, not arbitrary model tools. A Gate field value can
trigger only the application branch that explicitly handles it. A successful
review publication does not
authorize branch push, issue creation, acknowledgement, or merge.

## Failure And Edge Cases

Fail closed on identity mismatch, stale config/runtime/workspace/head
generations, malformed or oversized structures, secret appearance in identity,
unsafe Unicode/control text, unsafe filesystem objects, symlink/FIFO/special
file swaps, private/special-use network targets, redirect or proxy use, typed-nil
capabilities, unexpected provider/model output, incomplete postflight, or
cancellation.

Subprocess launch also fails closed on an invalid zero execution policy, nil
command, relative or unresolved enabled instance root, malformed or duplicate
exposure while enabled, enabled unsupported platform, unavailable Linux
`bwrap`, or enabled Windows exposure. Such rejection occurs before an
ungoverned process starts; unsupported-platform failure precedes directory and
command mutation. A copied
explicit policy is unaffected by later source or global compatibility mutation.
Ubuntu cross-compilation proves Windows source/build portability only and is not
native evidence for Windows path construction, restricted-token creation,
low-integrity enforcement, or Job Object assignment.

A missing provider capability disables only its matching effect. No model alias
causes local model-dependent failure before provider access. Human gates retain
durable private state but no runtime lease. Unknown provider effects cannot be
blindly retried. Unsafe URLs are omitted. Raw errors are mapped to fixed bounded
public codes.

A missing/stale candidate fence, changed parked line, invalid revision/path,
oversized tree/blob, binary content, symlink, submodule, or non-blob makes code
inspection unavailable; it never falls back to a live filesystem read. Missing
Web Push support, permission denial, insecure origin, invalid endpoint/keys,
delivery failure, or revoked device leaves the durable notification readable in
the authenticated inbox and grants no fallback content disclosure.

Opening corrupt v18, v19, or v20 storage fails before destructive success.
Opening v1–v17 with v20 code fails rather than partially migrating. Successful
v18/v19 cutover discards old development data transactionally before recording
version 20. The absence of a legacy UI/API is intentional; restored archives
must be inspected with an appropriately isolated older release, never mounted
into the current runtime.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SEC-001`, `FR-SEC-003` | [pkg/config/config_struct_test.go](../../pkg/config/config_struct_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-SEC-002`, `FR-SEC-007` | [pkg/credential/store_test.go](../../pkg/credential/store_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go), [pkg/auth/token_test.go](../../pkg/auth/token_test.go), [pkg/auth/pkce_test.go](../../pkg/auth/pkce_test.go), [pkg/mcp/auth_test.go](../../pkg/mcp/auth_test.go) |
| `FR-SEC-004` | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go) |
| `FR-SEC-005` | [pkg/utils/http_guard.go](../../pkg/utils/http_guard.go), [pkg/netbind/netbind_test.go](../../pkg/netbind/netbind_test.go), [pkg/mcp/network_test.go](../../pkg/mcp/network_test.go), [pkg/mcp/oauth_test.go](../../pkg/mcp/oauth_test.go), [web/backend/api/mcp_oauth_test.go](../../web/backend/api/mcp_oauth_test.go) |
| `FR-SEC-006` | [pkg/isolation/execution_policy_test.go](../../pkg/isolation/execution_policy_test.go), [pkg/isolation/runtime_test.go](../../pkg/isolation/runtime_test.go), [pkg/isolation/platform_linux_test.go](../../pkg/isolation/platform_linux_test.go) |
| `FR-SEC-008` | [pkg/config/model_config_test.go](../../pkg/config/model_config_test.go), [pkg/config/model_alias_test.go](../../pkg/config/model_alias_test.go), [pkg/config/model_alias_migration_test.go](../../pkg/config/model_alias_migration_test.go), [pkg/config/model_selection_test.go](../../pkg/config/model_selection_test.go), [pkg/config/account_router_test.go](../../pkg/config/account_router_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go) |
| `FR-SEC-009` | [pkg/auth/oauth_test.go](../../pkg/auth/oauth_test.go), [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go) |
| `FR-SEC-010` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_webhook_deferred_test.go](../../web/backend/api/config_event_webhook_deferred_test.go) |
| `FR-SEC-011` | [pkg/config/events_channels_test.go](../../pkg/config/events_channels_test.go), [pkg/eventing/channelmessage/backend_test.go](../../pkg/eventing/channelmessage/backend_test.go), [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-SEC-012` | [pkg/config/events_webhook_format_test.go](../../pkg/config/events_webhook_format_test.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go) |
| `FR-SEC-013` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go) |
| `FR-SEC-014` | [pkg/health/server_test.go](../../pkg/health/server_test.go), [pkg/eventing/operator](../../pkg/eventing/operator), [pkg/gateway/event_operator_test.go](../../pkg/gateway/event_operator_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-SEC-015` | [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/fileutil/durable.go](../../pkg/fileutil/durable.go), [pkg/fileutil/durable_unix.go](../../pkg/fileutil/durable_unix.go), [pkg/fileutil/durable_windows.go](../../pkg/fileutil/durable_windows.go) |
| `FR-SEC-016` | [pkg/workflows/inspection.go](../../pkg/workflows/inspection.go), [pkg/workflows/inspection_open_unix.go](../../pkg/workflows/inspection_open_unix.go), [pkg/workflows/inspection_open_other.go](../../pkg/workflows/inspection_open_other.go), [pkg/workflows/inspection_test.go](../../pkg/workflows/inspection_test.go), [pkg/workflows/inspection_open_unix_test.go](../../pkg/workflows/inspection_open_unix_test.go), [web/backend/api/workflow_inspection.go](../../web/backend/api/workflow_inspection.go), [web/backend/api/workflow_inspection_test.go](../../web/backend/api/workflow_inspection_test.go), [web/frontend/src/components/workflows/workflow-definition-inspector.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.tsx), [web/frontend/src/components/workflows/workflow-definition-inspector.test.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.test.tsx) |
| `FR-SEC-017` | [pkg/workflows/authoring_capabilities.go](../../pkg/workflows/authoring_capabilities.go), [pkg/workflows/authoring_capabilities_test.go](../../pkg/workflows/authoring_capabilities_test.go), [pkg/agent/workflow_authoring.go](../../pkg/agent/workflow_authoring.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/gateway/workflow_authoring.go](../../pkg/gateway/workflow_authoring.go), [pkg/gateway/workflow_authoring_test.go](../../pkg/gateway/workflow_authoring_test.go), [web/backend/api/workflow_authoring.go](../../web/backend/api/workflow_authoring.go), [web/backend/api/workflow_authoring_test.go](../../web/backend/api/workflow_authoring_test.go), [web/frontend/src/api/workflow-capabilities.test.ts](../../web/frontend/src/api/workflow-capabilities.test.ts), [web/frontend/src/components/workflows/workflow-capability-catalog.test.tsx](../../web/frontend/src/components/workflows/workflow-capability-catalog.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-018` | [pkg/workflows/editor_jobs.go](../../pkg/workflows/editor_jobs.go), [pkg/workflows/editor_jobs_test.go](../../pkg/workflows/editor_jobs_test.go), [web/backend/api/workflow_jobs_editor.go](../../web/backend/api/workflow_jobs_editor.go), [web/backend/api/workflow_jobs_editor_test.go](../../web/backend/api/workflow_jobs_editor_test.go), [web/frontend/src/api/workflow-jobs-editor.test.ts](../../web/frontend/src/api/workflow-jobs-editor.test.ts), [web/frontend/src/components/workflows/workflow-job-editor.test.tsx](../../web/frontend/src/components/workflows/workflow-job-editor.test.tsx), [web/frontend/src/components/workflows/workflow-capability-target-field.test.tsx](../../web/frontend/src/components/workflows/workflow-capability-target-field.test.tsx), [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx), [web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx](../../web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-019` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/workflows/trigger_simulation_test.go](../../pkg/workflows/trigger_simulation_test.go), [pkg/workflows/development_test_admission_test.go](../../pkg/workflows/development_test_admission_test.go), [web/backend/api/workflow_event_context_test.go](../../web/backend/api/workflow_event_context_test.go), [web/backend/api/workflow_trigger_simulation_test.go](../../web/backend/api/workflow_trigger_simulation_test.go), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows/workflow-trigger-simulator.test.tsx](../../web/frontend/src/components/workflows/workflow-trigger-simulator.test.tsx), [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx), [web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx](../../web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-020` | [pkg/agent/definition_test.go](../../pkg/agent/definition_test.go), [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go), [web/backend/api/agent_capabilities_cas_test.go](../../web/backend/api/agent_capabilities_cas_test.go), [web/backend/api/agent_capabilities_replace_linux_test.go](../../web/backend/api/agent_capabilities_replace_linux_test.go), [web/backend/api/agent_capabilities_request_test.go](../../web/backend/api/agent_capabilities_request_test.go), [web/backend/api/agent_capabilities_unix_test.go](../../web/backend/api/agent_capabilities_unix_test.go), [pkg/agent/activity_test.go](../../pkg/agent/activity_test.go), [pkg/gateway/agent_activity_test.go](../../pkg/gateway/agent_activity_test.go), [pkg/gateway/listen_test.go](../../pkg/gateway/listen_test.go), [web/backend/api/agent_activity_test.go](../../web/backend/api/agent_activity_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts) |
| `FR-SEC-021` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/config/pr_lifecycle_test.go](../../pkg/config/pr_lifecycle_test.go), [web/backend/api/config_writer_cas_test.go](../../web/backend/api/config_writer_cas_test.go), [web/frontend/src/api/pr-lifecycle-workflow-configurations.test.ts](../../web/frontend/src/api/pr-lifecycle-workflow-configurations.test.ts), [web/frontend/src/api/pr-lifecycle-repository-assignments.test.ts](../../web/frontend/src/api/pr-lifecycle-repository-assignments.test.ts), [cmd/picoclaw/internal/auth/config_revision_test.go](../../cmd/picoclaw/internal/auth/config_revision_test.go) |
| `FR-SEC-022` | [pkg/media/store_test.go](../../pkg/media/store_test.go), [pkg/media/snapshot_test.go](../../pkg/media/snapshot_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go), [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go), [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go) |
| `FR-SEC-023` | [pkg/workflows/private_context_security_test.go](../../pkg/workflows/private_context_security_test.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go), [pkg/workflows/gates_v3_runtime_test.go](../../pkg/workflows/gates_v3_runtime_test.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/backend/api/workflow_pr_lifecycle_privacy_test.go](../../web/backend/api/workflow_pr_lifecycle_privacy_test.go) |
| `FR-SEC-053` | [web/backend/api/pr_workspaces_test.go](../../web/backend/api/pr_workspaces_test.go), [web/backend/api/pr_workspace_proxy.go](../../web/backend/api/pr_workspace_proxy.go), [web/backend/middleware/launcher_dashboard_auth_test.go](../../web/backend/middleware/launcher_dashboard_auth_test.go), [web/frontend/src/api/development-workspaces.test.ts](../../web/frontend/src/api/development-workspaces.test.ts), [web/frontend/src/api/notifications.test.ts](../../web/frontend/src/api/notifications.test.ts) |
| `FR-SEC-054` | [pkg/prworkspace/ai_test.go](../../pkg/prworkspace/ai_test.go), [pkg/prworkspace/conversation.go](../../pkg/prworkspace/conversation.go), [pkg/prworkspace/implementation_test.go](../../pkg/prworkspace/implementation_test.go), [pkg/workflows/gates_v3_runtime_test.go](../../pkg/workflows/gates_v3_runtime_test.go) |
| `FR-SEC-055` | [pkg/prworkspace/eventing_store_sqlite_test.go](../../pkg/prworkspace/eventing_store_sqlite_test.go), [pkg/prworkspace/lifecycle_resume_publication_test.go](../../pkg/prworkspace/lifecycle_resume_publication_test.go), [pkg/eventing/schema_v19_cutover_sqlite_test.go](../../pkg/eventing/schema_v19_cutover_sqlite_test.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go) |
| `FR-SEC-056` | [pkg/gitworkspace/development_line_browse.go](../../pkg/gitworkspace/development_line_browse.go), [pkg/prworkspace/code_browser.go](../../pkg/prworkspace/code_browser.go), [pkg/gateway/pr_workspace_implementation.go](../../pkg/gateway/pr_workspace_implementation.go), [web/frontend/src/components/development-workspaces/development-code-browser.test.tsx](../../web/frontend/src/components/development-workspaces/development-code-browser.test.tsx), [web/frontend/src/api/development-workspaces.test.ts](../../web/frontend/src/api/development-workspaces.test.ts) |
| `FR-SEC-057` | [pkg/prworkspace/notifications.go](../../pkg/prworkspace/notifications.go), [pkg/prworkspace/push.go](../../pkg/prworkspace/push.go), [web/frontend/src/components/notifications/push-notification-settings.test.tsx](../../web/frontend/src/components/notifications/push-notification-settings.test.tsx), [web/frontend/src/lib/pwa-notifications.test.ts](../../web/frontend/src/lib/pwa-notifications.test.ts), [web/frontend/public/service-worker.js](../../web/frontend/public/service-worker.js) |

## Implementation Anchors

- [pkg/config](../../pkg/config)
- [pkg/credential](../../pkg/credential)
- [pkg/auth](../../pkg/auth)
- [pkg/netbind](../../pkg/netbind)
- [pkg/isolation](../../pkg/isolation)
- [pkg/fileutil](../../pkg/fileutil)
- [pkg/workflows/private_context.go](../../pkg/workflows/private_context.go)
- [pkg/workflows/gates_v3_runtime.go](../../pkg/workflows/gates_v3_runtime.go)
- [pkg/workflows/gatetypes/gates_v3.go](../../pkg/workflows/gatetypes/gates_v3.go)
- [pkg/prworkspace/ai.go](../../pkg/prworkspace/ai.go)
- [pkg/prworkspace/workflow_gates.go](../../pkg/prworkspace/workflow_gates.go)
- [pkg/prworkspace/http.go](../../pkg/prworkspace/http.go)
- [pkg/prworkspace/code_browser.go](../../pkg/prworkspace/code_browser.go)
- [pkg/prworkspace/notifications.go](../../pkg/prworkspace/notifications.go)
- [pkg/prworkspace/push.go](../../pkg/prworkspace/push.go)
- [pkg/gitworkspace/development_line_browse.go](../../pkg/gitworkspace/development_line_browse.go)
- [pkg/gateway/pr_workspace_provider.go](../../pkg/gateway/pr_workspace_provider.go)
- [pkg/gateway/pr_workspace_implementation.go](../../pkg/gateway/pr_workspace_implementation.go)
- [pkg/gateway/pr_workspace_publication.go](../../pkg/gateway/pr_workspace_publication.go)
- [web/backend/api/pr_workspace_proxy.go](../../web/backend/api/pr_workspace_proxy.go)
- [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go)
- [pkg/eventing/pr_workspace_schema_sqlite.go](../../pkg/eventing/pr_workspace_schema_sqlite.go)
