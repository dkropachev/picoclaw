# Security, Credentials, And Isolation

## Feature ID

`FR-SEC`

## Behavior Summary

PicoClaw protects credentials, dashboard access, local files, network requests,
tool execution, and optional isolated subprocesses. These requirements define
security behavior that other feature specs rely on. Event-derived AI
classification can explicitly remove model tool authority while leaving any
approved side effect as a separately declared workflow action. Workflow
inspection exposes only bounded structural metadata and conservative
possible-effect classifications, never the underlying source or sensitive
values. Workflow-authoring capability discovery similarly exposes exact
addressable identities and typed parameter shapes from one leased live runtime,
never descriptions, raw schemas, configuration, paths, or internal errors.
Structured job/action authoring treats draft YAML and mutation operations as
bounded untrusted input, remains a stateless AST transformation, and requires
an exact-identity conservative effect review before the separate execution
path. Current configuration also separates non-secret model aliases from
provider accounts, validates every reachable account-and-alias combination,
and revision-fences whole-config mutations so neither a missing model nor a
stale writer can silently delegate behavior to a provider default.
Compiler-generated attention workflows keep gate subjects and a pre-captured
PR-chat snapshot in an owner-local private root. Capture replaces every
structured media locator with an immutable reference and persists the strict
self-contained `FrozenSet` before revision binding; generic run, event, tool,
UI, and result surfaces expose only fixed lifecycle state and explicitly
generated human-task questions, never that context, media, or session
capability.
Review-attention policy is likewise trusted operator configuration rather than
repository content. Its authenticated policy-only read and same-origin strict
replacement share the complete public-plus-security config revision, preserve
unrelated configuration through compare-and-swap, and expose neither security
sidecar bytes nor runtime/session authority. The browser policy editor keeps
that projection only in one memory-resident lossless draft, never delegates
arbitrary question numbers to JavaScript numeric coercion, and cannot launch a
gate, workflow, model, tool, review action, or repository mutation.
An automatic outgoing-review attention occurrence keeps its effective policy
pin and worker fencing state owner-local. The worker establishes that pin from
trusted configuration before private effects, and every retry strictly
validates the same bytes rather than granting a database row or reviewed
checkout fresh policy authority. Only the gate engine's deliberately generated
bounded human task may declassify configured questions.
The case-owned browser attention bridge extends that deliberate
declassification only with fixed lifecycle state, answered text, and at most
one opaque actionable response fence. The launcher replaces browser authority
before reaching the protected gateway; every private identifier and diagnostic
remains server-side, and exact reserved attention runs are absent from generic
workflow observation and mutation surfaces.
Incoming feedback on the configured user's own PR has a separate read-only
capture boundary. Exact-body webhook authentication and a bounded provider
re-read can bind one review database ID, author, state, commit, time, URL, and
current PR/fork/head snapshot, but they do not make the review body trusted
instructions or grant model, checkout, repository, or GitHub-write authority.
The current GitHub MCP review result omits the webhook node ID and its comment
result omits a parent review database ID, so capture preserves the former only
as trigger evidence and claims no inline comment-to-review association.
A later own-PR development viewer declassifies only an explicit safe projection
of those immutable captures through protected
`/runtime/eventing/pr-development` and authenticated
`/api/pr-development` GET routes. It omits capture provenance and internal
identities, labels provider/ref/SHA values as capture-time facts, renders
feedback as plain text, and grants no provider refresh, gate, workflow,
checkout, filesystem, Git, CI, or mutation authority. A separate exact chat
POST may append a bounded local transcript and consult an isolated advisory
model over explicit historical data; the model receives no tools, history,
cache, default/workspace/runtime prompt context, checkout, provider credential,
or action capability. That transcript is separate two-table local event state:
one per-case high-water row binds count/version, total bytes, and a rolling
canonical digest to append-only messages, and every read/append validates the
complete relation without changing capture ordering or conferring identity.
A later controller-only repair primitive still does not inherit authority from
that capture, DTO, transcript, or answer. It independently refreshes the exact
review and current open PR/head, then passes only an exact pin and one
already-resolved concrete provider target into a runner that cannot receive a
raw checkout path or release capability. The runner constructs a fresh registry
with four guarded repository-content tools, validates complete tool-call and
patch path sets before mutation, serializes writes, denies Git control paths and
symlink aliases, suppresses argument values from logs, and postflight-verifies
the still-owned pin on every exit. It has no ambient agent, session, account,
workflow, process, network, Git, CI, or publication surface.
Schema v5 removes aliases mechanically derived from account names or concrete
model IDs, clears their references rather than guessing replacements, and
preserves legacy web-search mappings as explicit custom aliases instead of
silently assigning them to a predefined semantic role. Explicit custom aliases
and deliberately configured predefined roles are preserved. Optional frozen
session media treats every locator, decoded byte, copied metadata field, and
serialized frozen record as bounded untrusted input; capture and
materialization fail closed without exposing local paths or payload detail.

## Reconstruction Notes

- Similarity target: recreate secret-preserving config behavior, credential
  store CRUD, dashboard auth controls, HTTP guard checks, and optional process
  isolation with fail-closed setup.
- Core types/functions: secure string config helpers, credential store,
  dashboard auth middleware, CSRF/logout handlers, HTTP guard, isolation runtime,
  token, OAuth response parsing, PKCE helpers, strict bounded request decoders,
  raw-only AST classification for structured workflow authoring,
  `media.SnapshotReader`, `media.FreezeInputs`, and `media.FrozenSet` validation.
- Security boundaries also include compiler-only private workflow admission,
  integrity-bound local context and frozen-media persistence, pseudonymous
  provider affinity, mandatory observation projection, and the case-owned
  attention response fence plus generic-workflow suppression boundary. The
  own-PR intake boundary additionally includes exact signed-routing validation,
  a generation-fenced read-only GitHub provider snapshot, strict bounded JSON
  or confined regular-file artifact consumption, immutable local capture, and
  a separate whitelist-only read projection whose runtime and launcher routes
  replace authority without serializing the durable case record.
- Runtime ordering: load security config, normalize protected values, validate
  access or target, execute guarded storage/network/process operation, redact
  sensitive output, and emit clear errors; for frozen media, preflight the
  complete locator graph, capture only bounded no-follow regular files, then
  validate the complete self-contained set again before materialization.
- Non-obvious constraints: masked secure values preserve existing secrets,
  private network denial is the default, unsupported isolation does not fall back
  to unisolated execution, generated auth tokens must remain revocable, and a
  workflow-authoring projection or acknowledgement never grants runtime
  authority. A frozen-media digest is an internal consistency binding, not
  authentication against an attacker who can replace the complete containing
  record. A review-attention editor draft is memory-only capability-bearing
  configuration: it preserves accepted JSON number tokens exactly, never
  automatically rebases or retries a stale save, and gains no execution
  authority from previewing or validating an effective policy. A durable
  outgoing-review trigger pin is separate owner-local capability state: it is
  never browser-projected or logged, and corruption fails before private
  session, model, function, human-task, or run admission.
  A provider-verified own-PR case is still data, not authority: review text is
  never interpreted during capture, the workflow marker is only explicit local
  opt-in, and every future checkout or provider action must establish its own
  current authority rather than inheriting trust from either record.
  The development workbench does not change that rule: “current” review and PR
  values are names of capture-time fields, replay is a distinct public case,
  feedback is plain text, and event/dispatch/run/workflow/connector provenance,
  target user, provider node/review IDs, capture hashes, payloads, credentials,
  lease state, and raw errors remain unrepresentable.
  Its advisory conversation is separate append-only local data: the model sees
  only a bounded explicit projection under an exact replacement system prompt;
  complete-transcript count, byte, and digest validation fences corruption;
  conversation versions never confer capture or action authority; and
  plain-text answers are not evidence that a repository or provider was
  inspected. Draft, transcript, mutation, and ambiguous-response recovery may
  live in the keyed UI component and query cache but never in browser
  persistence.
  An attention response token is not a task identifier: it is a scoped digest
  over the exact server-loaded case-to-waiting-task chain, is issued only while
  that one task is actionable, remains memory-only in the browser, and grants no
  generic workflow or review-case mutation authority.

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
| `FR-SEC-021` | MUST | A whole-config read-modify-write operation captures the parsed update-safe configuration and one opaque revision of the exact public JSON plus security sidecar under the shared process/advisory lock, validates a complete candidate, and saves only when that revision is still current. The revision hashes security bytes without returning them. A stale save returns `config revision mismatch`, writes neither file, and preserves the winning writer's aliases, credentials, and unrelated configuration; callers surface a reload/retry conflict instead of blindly retrying an operation whose intent may no longer be valid. Current-schema read-only snapshots never migrate or save, while update snapshots may complete the explicit legacy migration lifecycle before returning a coherent current generation. A scoped review-attention replacement uses the same combined revision but raw-patches only `reviews.attention` in the public JSON: it never serializes environment/default-derived state, rewrites the security sidecar, or migrates a legacy read. The authenticated review-attention UI holds one memory-only lossless draft from one captured revision, rejects malformed or numerically lossy projections before hydration, sends at most one full replacement for an explicit save, and never automatically retries, rebases, persists the draft in the browser, or turns validation/preview into execution authority. | CLI, launcher, runtime, and browser config writers must not lose concurrent public or secret state, persist ambient runtime state through an unrelated scoped save, corrupt arbitrary JSON numbers, retain trusted policy in a less protected store, or turn a read/edit path into implicit mutation or execution. |
| `FR-SEC-022` | MUST | Frozen-session media capture and materialization treat the complete locator batch and serialized `FrozenSet` as untrusted. They admit at most 32 locator occurrences, 16 distinct nonempty assets, 2 MiB decoded bytes per asset, 3 MiB decoded bytes counted per occurrence, and 5 MiB for both materialized encoding and frozen-set JSON. Occurrence counting happens before snapshot cloning; complete locator and supplied-metadata shape validation happens before any live read, and decoded/read/materialized bounds never trust declared lengths. At most four `FreezeInputs` calls hold capture admission concurrently; an excess caller waits only until a slot is available or its context is cancelled. A live `media://` snapshot retains store lifecycle synchronization while it safely opens and validates a regular handle and executes one bounded read: Unix uses no-follow/nonblocking open plus a status-change token, Windows rejects every handle carrying the reparse-point attribute and compares handle change time, and other platforms fail closed. Registration and final deletion use cleaned absolute exact lexical lifecycle keys without an approximate case fold. One live key coalesces only the same captured entry identity. A `SameFile` identity found under a distinct key is not coalesced because it may be a hard link; instead, all such live lifecycles become non-deleting. Re-registration permanently cancels older pending deletion through either its exact key or captured `SameFile` identity, and deletion rechecks `Lstat`/`SameFile` so an already replaced entry is preserved. These operations share store synchronization, so an old cleanup cannot delete a newly registered or read-pinned path. Only canonical frozen identities, canonical `media://` UUID capabilities, and `data:` locators with canonical parameter-free MIME plus canonical padded base64 are accepted. Raw filenames are bounded at 4 KiB before basename sanitization to valid UTF-8 without controls and at most 255 bytes; supplied MIME is at most 127 bytes and captured MIME is bounded at 1 KiB before canonicalization to at most 127 bytes. Frozen records have one supported explicit version, deterministic unique identities/order, bounded canonical metadata, exact decoded sizes, and content digests; strict decode also rejects invalid UTF-8 and unpaired JSON surrogates, and decode/use reject unknown, duplicate, missing, unused, reordered/noncanonical, trailing, digest/size-inconsistent, reference-inconsistent, or authoritative occurrence-metadata-inconsistent state before returning rewritten history. Snapshot/freeze/materialize failures use fixed bounded classification and omit locator text, local paths, filenames/content types, decoded or encoded payload bytes, and raw filesystem/JSON/base64 errors. Cancellation or any failure returns no partial snapshot, reference list, set, or materialized output and leaves caller-owned inputs unchanged. | Media locators can name private temporary files and carry attacker-sized inline content; restart-safe capture must not become a symlink/special-file read, resource-amplification path, tampered-context downgrade, or diagnostic data leak. |
| `FR-SEC-023` | MUST | A compiler-private workflow context is local capability state admitted only for the exact trusted in-memory gate workflow and normalized value snapshot. Admission rejects custom JSON/text marshalers without invoking them, encodes the caller-owned workflow once, verifies the unexported compiler hash against those exact bytes, and executes only the detached workflow decoded from that same capture; serialization or post-compile workflow/value mutation cannot retain authority. An initial context cannot claim retry provenance. It is persisted before effects with an integrity revision plus a domain-separated binding over run ID, workflow reference, retry source, private-root revision, and persisted-workflow revision, and cannot mix with public inputs, event, secrets, origin, session, delivery, parent/reusable context, or arbitrary workflow targets. A separate durable owner-local marker preserves private classification if JSON visibility/root fields are removed; store reads also require the decoded ID to match both the directory key and exact requested ID. Its exact read-only session is captured before durable run creation. During that one capture, every structured media locator is replaced by an immutable frozen reference and the complete canonical versioned `FrozenSet` is strictly validated and persisted with the snapshot before the private-root revision is computed. Resume, retry, restart, managed children, repairs, and provider fallback reuse only that frozen snapshot and set: they validate the frozen revision before materializing integrity-checked embedded bytes and never reread a live session or media store. The explicit private encoding also preserves runtime-only message/system-block prompt provenance and tool-call name/arguments/thought signature exactly across wait/restart. Provider cache and account affinity use only a domain-separated agent-plus-history-revision pseudonym, never the raw key, scope, inbound delivery, locator, or materialized payload. Every private agent step carries an explicit execution marker: account-router health keeps only failure classification plus fixed error text, and side-question vision fallback suppresses raw-error runtime events. `Run.MarshalJSON` recognizes private visibility and redacts invocation context, ancestry, delivery, outputs, raw errors, job/step diagnostics, frozen references, and the embedded media set by default. Only the trusted owner-local file-store encoder may bypass that default to write the exact raw checkpoint, execution, task, and private continuation state; its strict private-root decoder rejects unknown fields and invalid frozen-media state. The unexported root remains absent from every ordinary `Run` JSON, and every HTTP, SSE, browser/development, workflow-tool, runtime-event, stored event, cancellation result, and direct-result boundary removes private state and event messages/payloads before traversing caller-controlled graphs instead of falling back to the raw record. Event append/read first classifies a readable owning run under the store lock; an orphaned or unclassifiable record fails with the fixed private-context error. Human-task resume clones secrets at entry and rejects additions again in the authoritative locked claim and returned-run boundary before consuming or continuing a private response. Wrapped public sentinel errors are canonicalized before return, including fixed `ErrRunAdmissionConflict` and `ErrRunAdmissionUnavailable` boundaries that the HTTP owner maps respectively to the existing dependency-revision-mismatch `409` and dependency-check-unavailable `503` responses without exposing internal text. Only the compiler-generated bounded human task may deliberately declassify its title/questions. A missing, corrupt, hash/revision/binding/media-mismatched, mixed-visibility, or unprojectable private record fails closed with a fixed error. | Local gate evidence and exact provider prompt construction must survive process restarts and human delays without turning generic JSON, workflow observability, provider routing, media persistence, or error handling into a PR-chat and code/findings exfiltration path. |
| `FR-SEC-024` | MUST | The authenticated browser may access review attention only through exact case-owned projection and response routes after the launcher replaces all browser authority with the managed gateway's process bearer on one same-origin, no-proxy, no-redirect, strictly bounded JSON request. The launcher peeks PID metadata without cleanup, attachment, migration, process inspection, or health probing and requires a valid nonzero port plus a numeric loopback or literal current local-interface address; hostname, wildcard/unspecified, multicast or remote numeric, and incomplete authority fail with fixed unavailability before the bearer is put on a request. A valid non-submitted case short-circuits to `none` without reading an occurrence or run. Submitted projection validates the latest submission and trigger first: historical trigger absence is `none`; pending/claimed accept only coherent absent pin state or a strictly decoded canonical pin with matching recomputed revision and read no run; no-op requires a canonical all-zero pin, no run, and terminal completion; only delivered requires a canonical active pin, terminal completion, deterministic run ID, exact decision link, and stable bounded run/task snapshot. Before declassification, every task's exact title, questions, and response schema must re-hash to its stored input hash. The DTO exposes only the positive case version, fixed aggregate status, `can_respond`, configured title/questions, public turn status including non-actionable `canceled`, and a durably accepted response for answered/continuing/recovery state. At most one current actionable waiting or recovery turn receives an opaque lowercase SHA-256 fence, absent whenever `can_respond` is false or the turn is continuing, answered, or canceled. The fence is a domain-separated length-prefixed digest over the exact server-loaded case/version, submission, decision, policy revision, run, task, original waiting revision, and input hash; the server accepts no private linkage and derives a separate response ID from the fence plus normalized bounded answer. Exact persisted replay or lost-response recovery is idempotent, while stale, old, cross-case, cross-task, or altered answers conflict. GET, navigation, polling, and failure mutate nothing; POST may change only the exact private task/run continuation and never review, event, policy, repository, provider, or GitHub state. Private IDs/revisions, session, policy body, task/run/workflow identity, input hashes, trigger lease/retry state, process bearer, and raw stored/upstream errors are unrepresentable in the DTO, route, browser storage, and fixed errors. The canonical URL contains only `case` and `focus=chat`. Generic workflow surfaces suppress exact reserved-reference runs; visible ordinary runs scrub direct hidden parent/caller, retry, child, and origin-root references; graphs omit hidden nodes and incident edges; and cancel/retry/task mutations return not found for the entire normalized transitive parent/child/retry component. Production web and CLI retention preserve every exact reserved-reference run regardless of terminal age while ordinary related runs keep normal retention. With workflows disabled, the bridge is deliberately read-only even if given an executor: waiting/recovery has no fence, no new answer is consumed, exact persisted replay remains projection-only, and exact-reserved task resume returns not found before disabled-runtime disclosure. This outgoing-review declassification grants no inbound own-PR feedback or generic workflow authority. | A necessary human handoff must resume one provably current private gate without turning the browser, URL, launcher proxy, generic workflow APIs, replay, retention, or error handling into a capability-confusion or private-context exfiltration path. |
| `FR-SEC-025` | MUST | The own-PR development workbench read is a declassification over exact protected `GET /runtime/eventing/pr-development` and `GET /runtime/eventing/pr-development/{pdc_...}` routes mirrored only as authenticated launcher GETs. List input is limited to one validated repository, canonical positive pull number, canonical limit, and opaque filter-bound cursor; detail accepts no query. Both layers reject aliases, malformed encoding, unknown or repeated input, bodies, and non-GET methods before store access, and the launcher replaces all browser authority with one process bearer sent only to numeric loopback or a literal current local-interface address through a bounded no-proxy/no-redirect client. The runtime constructs dedicated DTOs instead of serializing durable cases. List summaries may contain only public case ID, repository, pull number/URL/author/state/draft/merged, head repository/ref/SHA, review author, submitted/current review state, review submitted time/URL, and capture time; detail may additionally contain base repository/ref/SHA, review commit SHA, bounded valid-UTF-8 feedback, and the bounded public conversation fields owned by `FR-SEC-026`. Event, dispatch, run, workflow, connector, target-user, provider node/review database IDs, capture hash, raw payload, credential, lease/retry state, internal errors, and every mutation capability are unrepresentable. Provider state, refs, and SHAs are explicitly captured-snapshot facts even when a field is named current; a replay is a distinct case rather than hidden replacement. The canonical browser view contains only fixed `view=development`, optional repository, optional canonical positive pull number, and selected public case ID, keeps cursors and DTOs memory-only, renders feedback and messages as plain text, isolates deliberate external links, and performs no provider refresh, model call, gate, workflow, checkout, filesystem write, Git/CI command, commit, push, merge, acknowledgement, provider call, or mutation merely by reading or navigating. | Viewing untrusted review feedback must not turn the browser, proxy, durable case, misleading “current” label, replay, route, or renderer into a provenance oracle, stored-script path, ambient authority bridge, or implicit local/provider action. |
| `FR-SEC-026` | MUST | Own-PR development conversation is available only through exact authenticated `POST /api/pr-development/{pdc_...}/chat` and protected `POST /runtime/eventing/pr-development/{pdc_...}/chat`. Before PID access the launcher rejects absent, ambiguous, or cross-site browser provenance; after it strips browser authority, the protected runtime rejects any `Origin`, `Referer`, or `Sec-Fetch-Site` header before store/model access. Both layers reject noncanonical or escaped aliases, extra segments, wrong methods, every raw query or bare `?`, unsupported or ambiguous content type/charset/encoding, missing/streaming/over-one-MiB bodies, invalid UTF-8 or Unicode scalars, excessive JSON depth, exact or case-colliding duplicates, unknown keys, trailing values, and anything except the case-sensitive keys `expected_version` and `content`. Canonical `pdc_` identity, integer version range zero through 256, configured agent, and Go-`TrimSpace`-normalized nonempty NUL-free at-most-32-KiB human text are preflighted before store/model use; stale version and complete-turn capacity necessarily wait for the current case and integrity-checked transcript read. The launcher replaces every browser credential and ambient header with the process bearer on one numeric-local no-proxy/no-redirect request, bounds it to 120 seconds and its JSON response to 32 MiB, and the shared protected server keeps a 135-second write timeout. One process-wide same-case lock prevents interleaving across service generations, while each `Service` independently admits one configurable bounded set of AI calls. Under both fences the service binds the captured case and conversation, requires the exact current version, and reserves two remaining rows plus the normalized human bytes and maximum 64-KiB assistant bytes before committing the human. It then sends a detached at-most-512-KiB context containing only safe captured-snapshot fields and at most 50 recent public messages to one canonical configured agent. The request is private, ephemeral, single-run, `tools=none`, `history=none`, `cache=none`, and managed-off, and its exact isolated system prompt replaces the configured default, workspace/bootstrap, identity, memory, skills, contributors, tool rules, summary, time, and dynamic runtime context. Repository, refs, feedback, transcript, and latest message are explicitly untrusted historical data. The validated assistant text is appended separately. Schema v7 uses one conversation high-water row and append-only message rows; every read/append validates contiguous count/version, total bytes, and a domain-separated length-prefixed rolling SHA-256 digest over all canonical message fields. The transcript is bounded to 256 rows, 64 KiB each, and 4 MiB total, and chat never updates capture ordering. Any model, response-validation, assistant-append, or later validation failure after human append preserves that committed row. An error may declassify detail only after an independent two-second safe reload; failed reload and list/detail errors omit detail, and raw provider/model/store text is never returned. Public DTOs omit agent/session/account/model identity, prompts, tool state, tokens, cache keys, capture provenance, provider credentials, filesystem paths, run/gate identity, and all action capability. The case-keyed UI retains detail, transcript, draft, mutation, and ambiguity state only in memory, applies Go-compatible trim semantics, renders plain text in an accessible live log, rejects loose case/version/message binding, independently preserves the newest conversation and repair dimensions, and takes unversioned repair capability from authoritative GETs while mutation detail may only downgrade it. A matching expected-version-plus-one human row is recovered as a committed response failure without blind retry; the same row followed by an assistant at expected version plus two is recovered as completed. Refetch failure retains and announces visible detail/draft, and mobile list/detail focus is restored. | Prompt injection in review text, stale tabs, cross-site requests, malformed gateway state, ambiguous outcomes, model/storage failures, or a plausible answer must not become an ambient model proxy, script path, transcript corruption, current-state claim, repository inspection claim, or authority to gate, acknowledge, edit, validate, commit, push, merge, refresh, or mutate GitHub. |
| `FR-SEC-027` | MUST | Controller-owned PR repair is admitted only after `GitHubVerifier.VerifyCase` reconstructs one valid immutable case and independently confirms an open unmerged pull, the unchanged exact non-dismissed review evidence and base target, and a credential-free canonical current-head clone endpoint. `VerifyCase` alone binds canonical pull/review/clone shapes to one strict HTTPS `WebOrigin`, defaulting to `https://github.com` and explicitly configurable for GitHub Enterprise; capture-time `Verify` retains its existing provider contract. While a trusted caller pins one concrete provider/model generation, `LocalRepairRunner` accepts only a bounded exact `PinnedAcquireRequest`, user instruction, and explicitly untrusted context; it never accepts a raw path, generic agent/instance/loop, session, workflow runner, account router, fallback chain, tool registry, or release capability. Exact pin identity is process-serialized and acquired before provider access. One construction-local registry allowlists exactly line-bounded `read_file`, bounded/filtering `list_dir`, size-bounded `edit_file`, and whole-path-preflighted bounded `apply_patch`, all restricted to the acquired checkout with no allow-path escape. Paths must be canonical repository-relative names; lexical case/trailing-dot-or-space/alternate-stream aliases of `.git`, traversal, absolute paths, outside symlinks, resolved Git-control ancestors, mutable symlinks/non-regular files, and oversized targets fail before delegation. Apply-patch parses first and validates every add/delete/update/move path before operation one. The fixed prompt distinguishes the user task from untrusted context and accurately denies shell, process, network, web, MCP, hook, workflow, message, runtime-event, Git, CI, commit, push, merge, and provider-write capability. Provider calls receive only the four exact definitions plus `max_tokens` and `temperature`, with no cache/affinity metadata; messages are detached, response/tool/argument counts and bytes are bounded, names and IDs are nonempty/unique/allowlisted before any tool executes, nil/panic/malformed responses fail, thought signatures survive follow-up, mutations execute sequentially in response order, and argument values never enter tool-loop logs. After any exit following acquisition—including cancellation through a bounded detached cleanup context—the runner reacquires and compares the same workspace/repository/ref/path/lock identity. It never releases, cleans, resets, tests, commits, or publishes the pin. Only ordinary repository-content files plus manager-owned repository/workspace creation and exact pin lock, heartbeat, and history state may change; a later failure can intentionally leave allowed partial edits locked for explicit inspection. | Untrusted review/code content and a tool-calling model must not turn an internal repair step into Git-control tampering, path escape, ambient-agent reuse, unintended provider selection/affinity, concurrent write races, hidden execution/publication, or silent loss of partially completed local work. |
| `FR-SEC-028` | MUST | Browser-started PR repair crosses only exact authenticated launcher and bearer-protected runtime routes with strict same-origin replacement, bounded exact JSON, two optimistic revisions, and a random idempotency identity. The public aggregate is a dedicated declassification: it may expose availability, opaque case/session/attempt identities, canonical selected agent ID, pinned public head repository/ref/SHA, at-most-4-KiB instruction, lifecycle/timestamps, at-most-4-KiB sanitized summary, and a fixed error code, but cannot represent clone URL, reservation, workspace identity or checkout path, lease/worker identity, provider/model/account selection, prompt, tool call or arguments, review digest, capture provenance, credentials, or raw diagnostics. The durable controller holds the exact runtime generation for a worker iteration, uses the stored agent only to construct one concrete no-fallback runner, feeds captured review/conversation/repair material only as bounded explicitly untrusted context, and performs no implicit repair from advisory chat. Preparation is reclaimable only before runner invocation; after the durable `running` fence, every lost lease, crash, runner error, or uncertain finish preserves the pin and becomes `recovery_required` without automatic replay. Existing session/history remains visible when new admission is unavailable. The browser keeps draft, retry identity, and baseline next-attempt ordinal memory-only; it consumes ambiguous intent only when that exact ordinal matches the submitted conversation version and canonical instruction, renders every instruction/summary as plain text, independently preserves the newest conversation and repair dimensions, treats authoritative GET detail as the only capability-upgrade source while mutation detail may downgrade it, polls only active status, and labels completion as local edits without review, test, commit, push, merge, reset, release, or provider acknowledgement. | A durable asynchronous controller and its UI must not turn retry, polling, config reload, untrusted transcript/code, error projection, or an ambiguous crash into leaked local/provider authority, duplicated edits, silently discarded work, or a false claim that local mutation passed a later gate. |

For `FR-SEC-028`, the exact GitHub read tool controls generation wiring, while
public availability separately requires a side-effect-free proof that the
selected agent has a usable local workspace plus at least one concrete
model/provider binding. Selection uses the configured default only before a
session exists; afterward projection, admission, and execution keep using the
stored immutable session agent. Changing the default never retargets that
session, and removing its agent leaves history visible but disables new
admission instead of falling back. The queue still reconciles while admission
is disabled. Safe preparation reclaim rotates only
private lease authority; parent-generation or intentional heartbeat cancellation
cannot manufacture a public lease failure. The transition to `running`
atomically refreshes the execution lease immediately before runner invocation;
after `running`, only a pinned recovery outcome is safe. Revision headroom is
reserved before admission so an active attempt can always reach a terminal
public state. After acquiring its SQLite immediate transaction, each claim
uses one scan clock for a fixed batch of at most 32 ordered candidates, then
samples a fresh clock immediately before the ownership write and grants the
complete requested lease from that instant. It durably suppresses only
semantically invalid stored aggregates through private state, preserves
operational store failures, and advances to later work on the next poll after
a full corrupt batch, so corruption cannot head-of-line block another case or
create an unbounded write-lock scan. Expired running attempts are likewise
terminalized in batches of at most 32. Renewal takes the same immediate lock,
samples time after acquisition, and never shortens a newer execution deadline.
The 4-KiB durable/public instruction and summary caps keep a maximally populated
and worst-case escaped detail or error wrapper within the 32-MiB launcher
response ceiling. The browser treats capability freshness separately from
durable content versions, prevents its own chat and repair mutations from
overlapping, and requires recovery continuation to acknowledge possible
preserved edits.

For `FR-SEC-021`, the review-attention agent companion is a separate
identity-only read boundary. `GET /api/reviews/attention-agents` requires the
opaque complete-config revision from the policy response as one strong quoted
`If-Match`; an optional canonical decimal cursor is valid only when paired with
that same generation. Each page exposes at most 256 normalized `id`/`name`
pairs plus default/revision pagination metadata. Workspace, account, model,
skills, subagents, runtime effects, security bytes, local paths, and raw errors
are unrepresentable. Current-schema loading is inert; stale, legacy, orphaned
security, invalid agent, malformed header/query, and noncanonical route states
fail closed without migration, backup, public write, or security-sidecar write.

For `FR-SEC-023`, an automatic outgoing-review occurrence stores at most one
3-MiB canonical effective-policy pin inside the owner-local event database.
Only a generation-fenced worker may create that pin from the trusted policy
source, and it must do so under the current opaque lease before any private
effect. Every subsequent worker and the browser bridge treat the bytes as
untrusted persisted state:
strictly decode one versioned envelope, revalidate the detached resolution,
recompute its decision digest, and require its independent stored revision to
match before session projection, workflow admission, or browser projection.
Pin presence must match `policy_revision`; pending/claimed may legitimately be
pre-pin, no-op requires an all-zero effective policy and forbids a run, and
delivered requires an active effective policy plus a canonical private run ID.
Both terminal states require completion. Trigger rows, policy bytes,
lease identity/deadline, retry detail, and run linkage have no HTTP, browser,
event, log, or generic workflow projection. Errors are bounded and sanitized;
the automatic path invokes no repository or provider write, and only the
compiler-generated human task deliberately exposes bounded configured
questions.

For `FR-SEC-023`, infrastructure failure while rechecking a private durable-create
fence is likewise reduced to a fixed admission-unavailable sentinel; the HTTP
owner maps it to the existing dependency-check-unavailable response without
exposing filesystem or configuration diagnostics.

For `FR-SEC-024`, `sha256:` identifies only the opaque response-fence format;
the digest input and all of its private components remain server-side. A fence
is emitted only for the sole actionable current task and is replaced by a fresh
authoritative projection after response. It is never a bearer for generic run
inspection, task lookup, or review-case mutation.

## Data And State Model

Security state includes secure-string sentinels, credential records keyed by
provider and auth method with optional non-secret account email and OAuth
refresh metadata, process and supported-host cross-process auth-store locks, dashboard
password/session data, login attempt counters, configured secret filters,
private-host allowlists, isolation exposed paths, generated token IDs,
revocation metadata, per-connector event webhook formats/signing secrets, and
explicit normalized webhook body/header trust metadata. Non-secret model
selection state consists of concrete provider accounts, exact model aliases,
per-concrete-account alias overrides, independent account/model routers, and an
opaque public-plus-security revision used only as compare-and-save authority.
Workflow agent requests
also carry an explicit inherited-or-none tool policy; declared MCP actions use
their ordinary independently configured credentials rather than ingress signing
secrets. A compiler-private launch carries an unexported workflow hash that is
verified before admission. Its resulting run carries an explicit private-
visibility marker and integrity-bound root in the owner-local run file. That
root may contain normalized gate values and a detached read-only session
snapshot whose structured locators are frozen references, the corresponding
strict versioned `FrozenSet`, and an explicit representation of runtime-only
prompt provenance and tool-call internal fields; it is not a remotely managed
secret store. Default `Run` JSON is redacted, while only the local store bypass
encodes exact raw continuation state. Its raw key and scope remain usable only
for local evidence validation, while provider affinity derives from a
domain-separated pseudonym and public observation DTOs contain neither the root
nor frozen/materialized media or derived execution output. The gateway PID
bearer remains process-local management authority;
launcher sessions and owner-readable local CLI access can use it only through
the bounded event proxy/client, and event DTO types make lease and
deduplication credentials unrepresentable. Structured job/action editor
revisions are opaque hashes of caller-supplied draft bytes, not durable
authority. Editor inspections, operations, capability choices, and
effect-review acknowledgements are transient request/browser state and add no
configuration, credential, workflow, session, or run record. A detached media
snapshot and its versioned `FrozenSet` contain bounded user-supplied bytes and
sanitized metadata, not a source path or live store authority. They may be
sensitive durable context when an owning feature serializes them, but this
security contract does not itself choose a persistence location, encryption,
retention, or access-control policy. Their content-derived identities and
digests establish internal consistency only; they do not authenticate a whole
record controlled by a local storage attacker.
The attention response fence, response ID, browser draft, and focused route add
no security store. The first two are derived from existing owner-local state,
the draft is memory-only, and route state carries only public case selection and
the fixed focus affordance.
The own-PR development projection and advisory conversation add no security or
browser-persistent state. The opaque cursor is bound to validated filters and
retained only for the active read; safe DTOs are constructed from explicit
capture and transcript fields, never raw record serialization. Conversation
rows are local event-store state but contain no agent/session identity or
authority that a later action could reuse.
The controller-only repair runtime adds no security, eventing, session, or
browser store. Its only permitted durable effects are exact pinned-checkout
acquisition, including manager-owned repository/workspace inventory, lock,
history, and heartbeat state, plus ordinary content changes inside that
checkout. Failed or partial content changes remain under that exact pin; this
primitive never releases or resets them.

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
| Config | Secure strings, `isolation.*`, filtering fields, `model_list`, `model_aliases`, and model-selection validation | Secret preservation, isolation controls, sensitive-data filtering, strict account-plus-alias selection, safe v1-v3 migration, and early rejection of unsupported or provider-incompatible values. | `FR-SEC-001`, `FR-SEC-003`, `FR-SEC-006`, `FR-SEC-008` |
| Go API | `ConfigRevision`, `LoadCurrentConfigSnapshot`, `LoadCurrentConfigForUpdateSnapshot`, `LoadConfigForUpdateSnapshot`, `SaveConfigIfRevision`, `SaveReviewAttentionIfRevision` | Capture the exact public-plus-security generation, load current schema without migration, compare-and-save a complete update, or atomically patch only persisted review-attention policy without exposing or rewriting security bytes or overwriting a concurrent winner. | `FR-SEC-021` |
| Config | `events.ingress.webhooks.*.{format,secret}` | JSON-owned `standard`/`github` format plus masked JSON and secure-YAML merge/preservation for the corresponding per-connector secret, without security-only connector resurrection. | `FR-SEC-010`, `FR-SEC-012` |
| HTTP | `GET /api/config`, `PUT /api/config`, `PATCH /api/config` | Management reads expose `[NOT_HERE]`; omitted or masked webhook secrets preserve the current value, and a concrete replacement rotates it through the same secure persistence path. | `FR-SEC-010` |
| HTTP | `POST /webhooks/events/{connector}` with `format: github` | Exact-body HMAC-SHA256 authentication, bounded parsing, explicit unauthenticated-header metadata, and durable delivery-ID deduplication behind trusted TLS. | `FR-SEC-012` |
| Workflow / MCP / storage | `github-pr-development`, `prdevelopment.GitHubVerifier`, `pr_development_cases` | Treat the signed review projection and provider-returned body as untrusted data; use only exact generation-fenced read calls and bounded strict JSON from an inline result or confined artifact to bind review-level identity and current PR/fork/head facts; retain the webhook node ID as trigger evidence only; and grant no model, inline-comment association, checkout, repository mutation, or provider-write authority. | `FR-SEC-012` |
| HTTP / CLI | protected `/runtime/eventing/*`, launcher `/api/events*`, `picoclaw events *` | Translate authenticated launcher or owner-local PID authority into bounded live-gateway operator calls without exposing PID credentials, lease tokens, deduplication keys, or automatically fetched payloads. | `FR-SEC-014` |
| HTTP / UI | `/api/workflows/definitions/inspect`, `/api/workflows/templates/{name}/inspect`, `/agent/workflows` | Return and render one non-cacheable, fixed-code, bounded structural projection without exposing definition source, sensitive values, source paths, event payloads, or raw internal errors. | `FR-SEC-016` |
| HTTP / UI | protected `/runtime/workflows/authoring/capabilities`, launcher `/api/workflows/authoring/capabilities`, `/agent/workflows` | Translate the authenticated dashboard session into one bounded live-generation catalog containing only exact targets, fixed readiness, and typed parameter shapes; the browser can search and copy a ready target but cannot invoke it from this surface. | `FR-SEC-017` |
| HTTP / UI | `POST /api/workflows/development/jobs/inspect`, `POST /api/workflows/development/jobs/render`, `/agent/workflows` Jobs & actions/effect review | Transform only exact bounded caller-supplied YAML through a strictly decoded ordered AST projection or one revision-fenced operation; retain unsafe shapes as raw-only, keep all state in the browser/request, and require exact-identity conservative acknowledgement before the separate draft-test endpoint. | `FR-SEC-018` |
| HTTP / UI | `POST /api/workflows/development/triggers/simulate`, `POST /api/workflows/development/test/execute`, `/agent/workflows` trigger simulator/review | Strictly bound, payload-safe simulation uses a read-only current-config/PID snapshot to produce the only server review token accepted for one exact active draft and scenario; confirmed execution uses an unpruned lazy runtime and rechecks token expiry, identity, config, match, protected-event, and effect state before durable mutation or runtime authority. | `FR-SEC-019` |
| Workflow / HTTP / SSE / tool | Compiler-private `RunRequest.PrivateRoot`, file-run persistence, run/result/event projections, and the `workflow` tool | Admit and preserve exact owner-local gate evidence, including its rewritten snapshot and strict self-contained `FrozenSet`, while making private invocation context and derived diagnostics unrepresentable on generic observation surfaces; only a bounded generated human task is an explicit declassification. | `FR-SEC-023` |
| Storage / runtime | `pr_review_attention_triggers`, `eventing.ReviewAttentionTriggerQueue`, `reviews.AttentionTriggerWorker` | Keep one bounded canonical effective-policy pin and fresh lease authority owner-local; create the pin only from a trusted generation before effects, strictly revalidate it on every retry, expose no trigger state publicly, and invoke only the private gate launcher without repository or provider write authority. | `FR-SEC-023` |
| HTTP / UI / workflow | Protected and launcher case-owned review-attention GET/response routes, `/reviews?case={case}&focus=chat`, and generic workflow observation/mutation routes | Peek and numeric-local-validate gateway authority without process/PID side effects; validate the trigger-status-specific case authority and task payload hash; project only bounded deliberate declassification plus at most one opaque actionable fence; resume only through server-resolved identity with exact recovery; retain response state in memory; suppress exact reserved runs; scrub hidden relationships from ordinary reads/graphs; fence the transitive relationship component from mutation; and preserve reserved replay authority from ordinary retention. | `FR-SEC-024` |
| HTTP / UI | Protected `/runtime/eventing/pr-development` list/detail routes, launcher `/api/pr-development` mirrors, and `/reviews?view=development` | Replace browser authority, strictly validate the exact GET/filter/case surface, construct only the safe captured-snapshot DTO, retain response and cursor state in memory, render untrusted feedback as plain text, isolate external links, expose replay as a separate case, and provide no refresh or action capability. | `FR-SEC-025` |
| HTTP / UI / model / storage | Exact protected `POST /runtime/eventing/pr-development/{pdc_...}/chat`, launcher `POST /api/pr-development/{pdc_...}/chat`, and the selected case conversation | Require same-origin evidence only at the launcher, strip browser authority there, and reject every provenance header at the protected boundary. Repeat canonical path, no-query/ForceQuery, known-length identity JSON, exact two-key/case-sensitive, Unicode, depth, version, and text validation before authority use; load and integrity-check the two-table transcript before stale/capacity decisions; reserve a complete two-row/worst-case-byte turn; serialize the case process-wide while bounding AI per service; invoke one exact replacement-prompt isolated advisory model; and expose partial detail only after a fresh bounded reload. Keep UI state in memory, normalize like Go, bind and adopt detail strictly and monotonically, recover matching version-plus-one/version-plus-two outcomes, announce live/refetch state, preserve mobile focus, render plain text, and expose no repository/provider/gate action capability. The shared 135-second protected write timeout exceeds the 120-second launcher application budget. | `FR-SEC-026` |
| Internal Go API | `prdevelopment.GitHubVerifier.VerifyCase`, `agent.LocalRepairRunner`, guarded `tools.RunToolLoop`, and controller-only `gitworkspace.Manager.AcquirePinned` | Re-establish exact current provider/head authority, then confine one borrowed concrete model to four serialized bounded repository-content tools over one exact pin, denying Git control paths and unconditionally postflight-verifying ownership without receiving release, execution, Git, CI, or publication authority. | `FR-SEC-027` |
| HTTP / Go API / storage | Protected and launcher PR-repair routes, `eventing.PRDevelopmentRepairAdmitter`, `eventing.PRDevelopmentRepairQueue`, `prdevelopment.RepairWorker`, `agent.AgentLoop.NewControllerLocalRepairRunner` | Replace browser authority, persist only bounded intent and private controller state, declassify a narrow lifecycle, resolve one exact no-fallback edit capability under the active generation, and terminalize ambiguous post-invocation work without replay or pin release. | `FR-SEC-028` |
| Config / HTTP / UI | `reviews.attention`, `GET` and `PUT /api/reviews/attention-policies`, `/reviews?view=policies` | Keep gate authority in operator-owned configuration outside reviewed checkouts; expose only bounded non-secret policy plus opaque revisions/effect status, parse and serialize arbitrary question JSON losslessly, and retain the editable projection only in memory. Replace it only through one explicit strict same-origin public-plus-security compare-and-swap that raw-patches only persisted `reviews.attention`, preserves unrelated persisted values and numeric tokens, and leaves security state byte-identical; a conflict retains the local draft for explicit reload/discard and never retries or rebases automatically. Route state contains only the fixed policy-view selector, and editing, validation, effective preview, reload, and discard grant no workflow or provider authority. Broad config GET omits this subresource; broad PUT accepts only an empty compatibility placeholder, broad PATCH rejects the field, and both preserve its exact value during unrelated updates. | `FR-SEC-021` |
| HTTP | `GET /api/reviews/attention-agents` | Project only one fixed-256 page of canonical configured identity and default metadata, fenced by one strong policy-generation `If-Match` plus an optional canonical offset; broader agent configuration and all security state remain outside the DTO. | `FR-SEC-021` |
| Workflow / MCP | `agent/*` with `with.tools: none`; `mcp/github/add_issue_comment` | Remove tools from every classifier model path, then permit a GitHub mutation only as a declared conditional MCP step with signed-body identity and fixed output text. The GitHub MCP server and its write credential are configured explicitly and independently from ingress authentication. | `FR-SEC-013` |
| Storage | Credential store | Provider and MCP credential CRUD, transactional refresh updates, auth/OAuth metadata, cross-process serialization on supported hosts, and optional non-secret account email metadata extracted from OAuth token responses. | `FR-SEC-002`, `FR-SEC-007`, `FR-SEC-009` |
| Storage | `pkg/fileutil` durable path operations | Durable recursive parent creation, synced same-directory atomic replacement, and durable logical removal with POSIX directory sync or Windows write-through moves. | `FR-SEC-015` |
| Network | Safe HTTP clients, MCP OAuth transports, and net binding helpers | Private/special-use host controls, DNS-pinned MCP OAuth discovery/token/probe/refresh requests, same-origin redirects, explicit local-development policy, and bind behavior. | `FR-SEC-005` |
| Go API | `media.SnapshotReader.ReadSnapshot`, `media.FreezeInputs`, `media.FrozenSet.Materialize`, session frozen-media wrappers | Capture one live file capability and freeze/materialize one complete locator batch with fixed resource ceilings, strict schemes/encoding, no-follow regular-file handling, consistency validation, all-or-nothing output, and redacted errors. | `FR-SEC-022` |
| JSON | Versioned `media.FrozenSet` serialized beside a frozen session snapshot or embedded by its owning durable record | Strictly round-trip only canonical bounded records; reject unknown, duplicate, trailing, inconsistent, or over-budget state before any locator is materialized. | `FR-SEC-022` |

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
    For own-PR development intake, treat the installed workflow output only as
    an opt-in marker, strictly validate the complete signed routing identity,
    consume only bounded exact JSON from the generation-fenced read-only GitHub
    tool, and compare exposed provider fields before immutable capture. Never
    infer the missing provider review node or inline-comment parent identity,
    and never pass review feedback to a model or action in this capture stage.
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
14. For workflow inspection, open one exact source nonblocking under
    root-confined handles, verify the opened handle is regular, perform one
    bounded read, release the cross-process file lock, hash before projection,
    and construct the response from an explicit whitelist rather than
    serializing parser or runtime objects. Release the handler-local config
    mutation lock before encoding or response writing. Preflight each trigger
    family and omit it whole when its fixed field or aggregate-entry budget is
    exhausted or its visible text contains control or Unicode format
    characters. Apply independent topology, dependency, effect, validation,
    field, and encoded-response limits; return only fixed validation and limit
    codes, mark every omission incomplete, and preserve conservative action
    classification even when a target cannot be returned.
15. For workflow-authoring capability discovery, read the bounded PID record
    without cleanup and without loading configuration, validate its live
    authority, and authenticate one exact launcher-to-gateway GET with the
    process-local bearer. Acquire the existing loop's runtime-use lease and
    retain it across bounded registry selection, readiness checks,
    sanitization, and pre-marshalling. Never initialize MCP from this path;
    inspect ready MCP tools only when the pinned generation already owns a live
    manager. Address ordinary tools by effective core registration key and MCP
    tools by exact immutable server/tool identity. Treat every identity and
    parameter map as untrusted, contain per-tool panics, reject unsafe visible
    text before target construction, traverse only whitelisted shape keys with
    cycle and shared work budgets, and discard an unsafe shape as a whole.
    Bound and revalidate the internal response at the launcher, replace every
    internal failure with a fixed unavailable or partial state, and never
    forward browser credentials, process authority, descriptions, raw
    schema/config values, or error text.
16. For structured job/action authoring, authenticate through the existing
    dashboard boundary, strictly and incrementally decode one bounded request,
    and reject duplicates, unknown members, trailing values, invalid text,
    unpaired surrogate escapes, excessive numeric precision, and aggregate
    budgets before AST mutation.
    Hash the exact YAML, parse one document, and classify every structural node
    before projection; source directives, anchors/aliases, merges, unsafe tags,
    ambiguous mappings, and lossy values never enter typed mutation state.
    Global source directives, anchors/aliases, container ambiguity, and topology
    truncation block all mutation; a locally raw-only job/step
    remains present but does not block safe siblings. Share one aggregate
    budget across validation issue paths/messages, set fixed
    `validation_truncated` plus
    incomplete state whenever any diagnostic is omitted, but do not block an
    otherwise safe edit; pre-marshal under the final success ceiling. Compare
    the render revision, decode one operation and its set/remove envelopes under
    aggregate request work plus separate recursive budgets for each dynamic
    JSON value, reject operations whose exact target is raw-only, and reject a
    step move whose inclusive source-to-destination span contains a raw-only
    step. Apply the 256-byte single-line identity bound to step IDs and every
    `needs` reference. Expose every source-order insertion boundary in the
    browser, including boundaries beside raw-only nodes that cannot be crossed
    by a later move. Before sending a mutation, mirror the server's bounded
    string, key, control/format, collection, encoded-value, target, and numeric
    checks in the client while retaining the server as authority. Then edit one
    local AST and project the result without filesystem, config,
    development-store, runtime, event, or provider access.
    A set-only job-ID rename collision-checks the new exact key and edits only
    that key scalar; it never performs a broad textual rewrite or implicitly
    retargets `needs`. Sanitize validation and failures into fixed bounded
    data. In the browser, fence asynchronous editor/catalog/dependency responses
    to exact identities, treat manual and unprojectable actions as unknown
    authority, bind effect acknowledgement to the exact draft/scenario/review
    identity, and compare that captured identity again before invoking the
    separately authorized test request.
17. For current model selection, validate exact alias names and concrete model
    mappings, reject router names as override keys, expand every reachable
    account-router/model-router pairing, and verify the resolved model against
    the concrete account provider. Resolve an alias override only after account
    routing selects its concrete account. If the model alias is absent, stop
    with `no model configured`; if the account is absent, stop with
    `no account configured`. Never ask a provider for a default.
18. For a whole-config mutation, load the update-safe config and exact
    public-plus-security revision together under the shared lock, derive and
    validate one complete candidate, and compare-and-save against that revision.
    If another writer changed either file, return the stale-revision error
    without writing or automatically replaying the requested mutation. For the
    browser review-attention editor, strictly parse the complete GET projection
    into lossless tokens before hydration, keep the captured revision with the
    memory-only draft, locally validate one complete projected replacement,
    issue one PUT on explicit save, and replace draft state only with the
    authoritative successful response. A conflict or later observed revision
    remains an explicit reload/discard choice and never authorizes a rebase,
    retry, or gate execution.
19. For frozen media, enumerate and statically validate the entire locator
    batch before capture; preflight occurrence, locator, and supplied-metadata
    bounds, acquire one of four context-cancellable global capture slots, then
    charge distinct-asset, decoded, aggregate-occurrence, captured metadata, and
    encoded work while capturing. Open each live file through the platform's
    no-follow regular-file primitive while its store mapping is pinned, and
    compare its status-change token after the bounded read. Normalize a
    registered path to a cleaned absolute exact lexical lifecycle key and bind
    that live key to the first entry identity. For the same identity under a
    distinct key, retain both paths but make both lifecycles non-deleting; on
    re-registration, cancel older pending deletions by exact key and verified
    identity. Before final removal, recheck that token and entry identity under
    store synchronization; preserve a detected replacement.
    Build a deterministic complete set only after every asset succeeds.
    On decode or materialization, strictly validate the JSON envelope, version,
    canonical identities/order/metadata, size, digest, reference closure, and
    the same budgets before emitting any locator. Map all failures through the
    fixed redacted taxonomy and discard partial internal output.
20. For a compiler-private gate, reproduce the trusted in-memory workflow's
    compiler hash and verify its restricted shape, reject mixed public context, clone and bound the
    values, and capture exact owned conversation evidence before creating the
    run. Freeze every structured media locator during that capture, persist the
    strict versioned `FrozenSet` beside the rewritten snapshot, preserve
    runtime-only prompt/tool-call fields explicitly, and compute one immutable
    integrity revision over that frozen representation. Resume, retry, and
    restart validate the revision before materializing embedded media and never
    reread the live session or media store; provider calls derive only a
    domain-separated pseudonym.
    At every generic run, result, event, HTTP, SSE, development, and workflow-
    tool boundary, project by private visibility before encoding or publishing;
    default `Run` marshaling performs that redaction even without an explicit
    projector, while only the local store's encoder writes raw continuation.
    If projection authority or private integrity is unavailable, return the
    fixed failure rather than raw fallback. Present only the compiler-generated
    bounded human task when attention is deliberately requested.
    For automatic outgoing-review attention, commit only the occurrence beside
    the submitted transition. Under the current worker lease and runtime
    generation, resolve the first successful trusted policy capture and persist
    its bounded canonical envelope before private effects. On every attempt,
    strictly decode, validate, and re-hash the stored pin; a mismatch fails
    before session or run work. Retain the pin across retry and expose neither
    it nor lease, error, or linked-run state through public projections or logs.
21. For browser attention, authenticate first and replace browser authority at
    the launcher. Peek PID metadata without lifecycle effects and require an
    explicit nonzero port plus numeric loopback or literal current local-interface
    host before constructing a bearer request. On the protected gateway, validate
    the submitted case, immutable submission, and trigger first; branch historical,
    pending/claimed, no-op, and delivered state as specified by `FR-SEC-024`, and
    only for delivered load the exact decision link, private run, and stable task
    chain. Recompute each task payload hash before declassifying fixed state,
    configured questions, or an accepted response. Derive a length-prefixed domain-separated
    SHA-256 fence only for the sole current actionable waiting/recovery task; do
    not issue it when runtime resume is unavailable. For POST, reject all
    caller-supplied private linkage, recompute the fence from server state,
    derive a separate response ID from it and the normalized answer, resume the
    exact loaded task, and reproject so exact persisted replay recovers without
    accepting changed text. Keep the fence/draft out of route and persistent
    browser state. Independently classify the exact reserved workflow reference
    before every generic list, detail, events, SSE, graph, task, resume, cancel,
    or retry operation and return omission/not-found instead of raw fallback.
    Scrub direct hidden relationships and graph nodes/edges from visible ordinary
    reads, and deny mutation for the entire normalized transitive parent/child/retry
    component. Preserve exact reserved-reference runs from production web/CLI
    retention. With workflows disabled use no executor, expose no new response
    authority, keep exact replay read-only, and classify reserved resume before
    returning disabled state.
22. For an own-PR development read, authenticate first, match only the exact
    list or canonical public-case detail route, and reject every unsupported
    method, body, encoding, query, filter, cursor, or ID before store access.
    At the launcher, peek and numeric-local-validate PID authority without
    lifecycle effects, replace all browser authority, and make one bounded
    no-proxy/no-redirect protected GET. At the runtime, read one immutable page
    or case and construct the dedicated whitelist DTO field by field; never
    marshal a durable case. Treat every provider/ref/SHA value as captured at
    the returned time, preserve replay as a separate case, and keep feedback
    valid, bounded, and plain text. Omit all provenance, internal IDs, raw
    payload/error state, and capabilities. Navigation, list, detail, retry, and
    failure perform no provider read, model/gate/workflow action, checkout,
    filesystem/Git/CI operation, acknowledgement, or mutation.
23. For an own-PR development chat, require unambiguous same-origin provenance
    at the launcher, replace browser authority, and reject every provenance
    header at the protected runtime. At both boundaries reject a noncanonical
    route, raw query or bare `?`, invalid method/length/media/encoding/Unicode,
    excessive JSON depth/size, or anything except exact case-sensitive
    `expected_version` plus `content` before PID/store/model access. Preflight
    canonical case ID, version range, Go-trimmed bounded text, and agent config;
    then take the process-wide same-case lock and a per-service AI slot. Load
    and bind the case and complete count/byte/digest-validated transcript before
    rejecting stale state or reserving two rows plus worst-case assistant bytes.
    Append the human under the optimistic high-water fence and invoke only the
    exact replacement-prompt isolated no-tools/no-history/no-cache agent over a
    bounded detached historical projection. Validate and append its response
    separately. Any later failure preserves the human row and returns fixed
    text; detail is allowed only after an independent two-second safe reload.
    Keep browser state per case and memory-only, adopt details strictly and
    monotonically, recover matching version-plus-one human and version-plus-two
    completed outcomes, and never expose prompt/runtime/agent identity or infer
    checkout, provider, gate, or action authority from the conversation.
24. For controller-owned repair, independently validate the stored case and
    current provider review/PR/head before deriving the pin. Hold the concrete
    provider generation outside the runner, process-serialize the exact pin,
    acquire it, and validate the returned real locked checkout before constructing
    any model-visible capability. Create a new four-tool registry per call; bind
    each path through lexical and resolved-ancestor Git-control denial, validate
    the complete patch and tool-call batch before operation one, and execute
    mutations sequentially. Keep provider input detached and cache/affinity-free,
    preserve tool thought signatures, suppress argument logs, and reject a nil,
    malformed, oversized, unknown, duplicate, empty, or exhausted response. Once
    acquisition succeeds, always reacquire and compare the pin on exit, using a
    bounded cancellation-detached context if needed. Preserve allowed partial
    edits under the lock; never release, reset, execute, test, commit, or publish.

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
string preservation. Model aliases and their concrete-account override keys are
also non-secret selection policy; they cannot contain provider credentials or
use an account router as override authority.
Workflow template and publish transactions also reuse the shared durable
directory, replacement, and removal primitives; their multi-file journaling and
recovery policy remain owned by the workflows feature.
Workflow definition inspection is likewise owned by the workflows feature. Its
authenticated UI and API expose a path-free whitelist rather than source YAML,
captured event content, authoring values, secrets, output expressions, or raw
internal errors.
Compiler-private gate storage and execution are likewise owned by the workflows
feature, while Agent Conversations owns frozen-provider execution. This feature
defines their shared non-declassification boundary: local persistence does not
grant generic observation authority, the raw PR-chat capability cannot become
provider cache/account identity, and a projection failure cannot fall back to a
raw run or event. The generated human task is an explicit bounded user-facing
handoff rather than an accidental diagnostic channel.
Workflow-authoring capability discovery is also owned by the workflows feature.
It reuses the process-local gateway authority and live runtime lease, reports
only exact addressable identity, fixed readiness, and bounded typed parameter
shape, and does not weaken the normal publish or execution readiness checks.
Structured job/action authoring is owned by the workflows feature. Security
defines its untrusted-input, statelessness, resource, stale-result, and
effect-acknowledgement boundary. The renderer does not inherit the catalog's
gateway authority; a catalog choice or manual target is merely draft text, and
the normal dependency, compatibility, workflow, tool, MCP, credential, and
isolation policies remain authoritative when a separately confirmed test or
published run executes it.
Review-attention policy selection, gate composition, and editor semantics are
owned by event automation, while launcher management owns authenticated route
registration and discoverability. Security owns the editor's lossless
projection, memory-only retention, exact captured-revision save fence, and
non-execution boundary. A reviewed repository cannot supply or override this
operator catalog, and neither an effective-policy preview nor an agent choice
grants model, tool, workflow, repository, provider, or GitHub authority. Event
automation also owns the outgoing submitted-review occurrence and its worker;
security requires its persisted policy pin and lease state to remain local,
strictly revalidated, and unavailable to browser or generic workflow surfaces.
The case-owned attention handoff is the outgoing-review browser
declassification: event automation owns its authoritative chain and lifecycle,
launcher management owns same-origin authority replacement and canonical focus,
and workflows own private task continuation. Security requires the opaque fence
to authorize only one current task, exact replay to remain idempotent, all
private identity and diagnostics to stay unrepresentable, the exact reserved
run to remain absent from generic workflow routes and ordinary retention, direct
hidden relationships to be scrubbed, and transitive relationship components to
be mutation-fenced. This outgoing-review bridge does not itself establish an
inbound own-PR feedback/development-case contract.
Event automation's separate own-PR development-capture contract establishes
only immutable provider-verified review-level intake. Workflows own its
explicit installed read-only trigger/action/output and ordered idempotent sink
handoff. Security requires the webhook and provider bodies to remain untrusted
data, the provider read to bind only the exposed database identity and current
PR/review facts, the trigger node ID never to be relabelled provider-verified,
and absent parent-review identity on inline comments never to be guessed. This
capture stage provides no browser/API/CLI chat, gate, model, checkout, edit,
push, merge, acknowledgement, or GitHub action authority. Its later workbench
composes event automation's safe runtime projection with launcher authentication
and authority replacement. Security restricts reads to captured public facts
and plain-text feedback, keeps provenance and browser persistence outside the
boundary, and makes replay visible as another case. Its separate chat composes
only a bounded append-only transcript and isolated advisory model call; neither
viewer nor model acquires omitted development or provider authority. Any future
action must independently establish current local and provider state rather
than inheriting authority from capture, DTO, transcript, or answer.
The internal repair primitive performs that provider refresh only for one
edit-confined turn. Event automation owns durable case meaning and refreshed
review/head validation; Git workspaces owns exact pin acquisition, inventory,
control-plane checks, and later release; agent execution owns the isolated
borrowed-provider loop. Security requires their composition to expose only four
repository-content tools and never upgrades successful editing into review,
test, Git, CI, commit, push, merge, or provider-write authority.
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
Frozen session media composes the tool feature's optional live-reference reader
with the session feature's versioned set and locator rewrite. Security owns the
safe-open, resource-bound, consistency-validation, and diagnostic-redaction
invariants; it does not wire the capability into agent/provider execution or
promise that a provider consumes every materialized modality.

## Failure And Edge Cases

- Partial secret updates preserve old value unless an explicit clear is requested.
- Empty defaults or a missing alias fail with `no model configured` before a
  provider transport is invoked. Raw model IDs are not accepted in
  alias-valued selection fields, and an account-router name cannot be used as
  an alias override key.
- A whole-config compare-and-save whose public JSON or security sidecar changed
  since its snapshot returns a revision mismatch and writes neither file; the
  concurrent winner remains intact for an explicit reload and retry.
- A malformed, duplicate, trailing, unsafe-Unicode, over-bound, or numerically
  lossy attention-policy response never partially hydrates the editor. Policy,
  questions, repositories, revisions, validation detail, and agent catalogs
  remain absent from route state, browser storage, logs, and toast text.
- A dirty or stale attention-policy draft stays only in memory. Background
  reads cannot overwrite it, save conflict never triggers an automatic rebase
  or retry, and only a confirmed explicit reload/discard may destroy it.
- Editing, validation, effective preview, refresh, save-conflict handling, and
  discard cannot launch a gate or workflow, call a model or tool, create a
  review action, write a repository, emit an event, or mutate a provider or
  GitHub resource.
- Submitted projection validates status-specific authority: historical absence
  is `none`, pending/claimed require no run, no-op requires a canonical all-zero
  pin/no run, and delivered requires a canonical active pin/link/run/task chain
  whose displayed payload matches its input hash. Malformed required state fails
  without raw fallback. Disabled runtime is read-only and may show waiting or
  recovery lifecycle but exposes neither `can_respond` nor a response fence;
  exact persisted replay remains idempotent and reserved resume returns 404 first.
- Noncanonical attention paths or queries, cross-site POST, malformed or
  oversized JSON, missing/zero-port, hostname, wildcard/unspecified, multicast
  or remote PID authority, redirect, proxy use, timeout, invalid upstream
  content type/body, stale/cross-case/cross-task fence, and altered replay fail
  without mutating a review case or consuming another task. An exact answer
  already persisted before continuation/transport failure recovers through its
  separate response ID and authoritative reprojection.
- Attention URLs, DTOs, browser storage, and fixed errors cannot contain process
  authority, private run/task/workflow/session/policy identity or revision,
  input hashes, trigger lease/retry state, or raw stored/upstream errors. Generic
  workflow routes suppress exact reserved attention references before any
  observation or mutation, including malformed impostors. Ordinary projections
  and graphs scrub direct hidden relationships; the normalized transitive
  parent/child/retry component is denied mutation; and production web/CLI
  retention preserves exact reserved-reference runs for restart replay.
- Development-chat prompt injection remains quoted historical data under an
  exact isolated replacement prompt. The launcher rejects missing/ambiguous or
  cross-site evidence and strips it; the runtime rejects any browser-provenance
  header. Invalid route/ForceQuery/media/encoding/Unicode/exact-key JSON, stale
  version, insufficient complete-turn capacity, corrupt transcript, unavailable
  model, invalid output, timeout, assistant append, or later validation failure
  exposes no raw diagnostic or capability. Any committed human row remains
  durable, but error detail is visible only through a separate fresh validated
  reload. Chat never changes capture ordering or grants a checkout, tool, gate,
  provider, or Git action.
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
- A valid signature or successful provider read likewise does not make own-PR
  review feedback instructions or development authority. Missing, duplicate,
  trailing, malformed, deep, or oversized provider data; an unsafe confined
  artifact; exact review/PR mismatch; or bounded review-scan exhaustion fails
  before case creation. Provider omission of review node and inline parent
  identity is preserved as a stated limitation rather than guessed linkage.
- Development list/detail reads reject noncanonical paths, methods, encodings,
  filters, cursors, IDs, unsafe gateway authority, redirects, malformed or
  oversized JSON, and invalid stored text before returning a partial DTO. They
  never fall back to raw durable-case serialization or include provenance in an
  error.
- A development read never refetches GitHub or upgrades captured “current”
  fields into live authority. Feedback remains plain text, external links have
  opener isolation, replay remains separately visible, and no route, browser
  storage, or DTO retains cursors, credentials, internal IDs, payloads, or an
  action capability.
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
- Missing or wrong PID authority, a stale or old gateway, timeout, malformed or
  oversized internal capability response, and base runtime unavailability all
  become one fixed non-cacheable dashboard `503`; upstream credentials,
  headers, bodies, and error strings are never reflected. A missing live MCP
  manager instead returns a fixed partial catalog without initializing it or
  revealing its cause.
- Capability identities containing invalid UTF-8, control or Unicode format
  characters, unsafe separators, or over-bound text are omitted. Panicking,
  cyclic, composition-heavy, reference-bearing, or over-budget parameter maps
  cannot escape the typed shape whitelist or consume a fresh full budget per
  discarded schema; one omitted shape never exposes its raw value or error.
- Job/action requests with duplicate or unknown members, trailing JSON, missing
  required values, stale revision, invalid operation identity, invalid UTF-8 or
  unpaired surrogate escapes, non-JSON-compatible dynamic values,
  browser-unsafe numbers, or exceeded source/work/response budgets return fixed
  bounded failures and mutate nothing. A semantically invalid but structurally
  safe candidate is returned only with sanitized bounded validation so it can
  be repaired locally. A colliding job rename is rejected, and a successful
  rename cannot silently rewrite authority-bearing expressions or dependency
  references elsewhere.
- Validation count or aggregate text exhaustion sets the fixed
  `validation_truncated` limit, marks the result incomplete, and retains only
  bounded sanitized diagnostics; it cannot create an oversized or opaque
  success response.
- YAML version/tag directives, anchors/aliases, merges, unsafe tags, duplicate
  or non-string keys, ambiguous containers, cycles, and lossy scalar or dynamic
  shapes cannot be expanded, flattened, or partially edited by the job/action
  API. They stay outside typed
  mutation state; a safe sibling render preserves their nodes, values,
  comments, order, and scalar style, while only a semantic no-op guarantees
  byte-exact source identity. Global ambiguity or topology truncation blocks
  every structured operation; validation-only truncation does not. A locally
  raw-only job rejects patch/delete and a locally raw-only step rejects
  patch/delete/move or being crossed by another step's move, while other safe
  sibling edits and structurally safe insertions remain available.
- A capability-picker result, editor response, dependency report, or effect
  acknowledgement for another exact identity cannot authorize or change the
  current draft. Unknown/manual targets, `workflows/` calls, raw-only actions,
  and incomplete or empty projections remain conservatively effectful even
  when mixed with known targets. Closing or changing the review clears consent,
  and a final identity mismatch creates no run.
- A caller cannot manufacture private authority by serializing a compiled
  workflow, setting a visibility string, or attaching a private root to another
  workflow. Mutating a compiled workflow changes its hash and fails admission.
  Capture or integrity failure creates no executable context; an update cannot
  strip or swap the root. Resume, retry, and restart cannot recapture newer chat
  history, reread cleaned-up live media, lose runtime-only prompt/tool-call
  metadata, or replace the persisted `FrozenSet`; revision and strict set
  validation precede any materialization. Missing projection authority drops
  private event content or returns a fixed error rather than encoding raw
  inputs, outputs, errors, session, delivery, scope, frozen/materialized media,
  or provider identity.
- Unverified email is skipped by default; an explicit opt-in marks it
  unverified. Private Delta Chat blob paths and copy errors do not enter durable
  events or attachment diagnostics, and oversized files are not materialized.
- Frozen media rejects raw paths, network/file/unknown schemes, noncanonical
  `media://` UUID capabilities, malformed or noncanonical padded-base64 data,
  a resulting filename basename that is invalid UTF-8, contains a control, or
  exceeds 255 bytes, a MIME value that cannot normalize to parameter-free
  canonical form within 127 bytes, and any locator whose live store entry is
  absent at capture.
  It does not search temporary directories or reuse a path from an earlier
  process generation.
- A mapped symlink, reparse point, directory, FIFO, socket, or device is never
  read as frozen media. A safe open that cannot be established
  fails with a fixed unavailable/unsafe class; it does not fall back to ordinary
  `os.Open`, follow the entry, or wait on a special stream; non-Unix,
  non-Windows targets fail closed.
- Declared sizes and JSON/base64 lengths are hints only. Raw locator and
  metadata inputs are capped before linear scans or copies. Actual decoded/read
  bytes, repeated occurrence cost, distinct assets, metadata, and final encoded
  form are independently charged; an empty asset, exceeding 32 locators, 16
  assets, 2 MiB per asset, 3 MiB occurrence-weighted raw bytes, 5 MiB
  materialized encoding, or 5 MiB frozen-set JSON returns no partial result.
  At most four captures hold admission at once, and a saturated waiter remains
  cancellable before its reader is invoked.
- An unsupported version, duplicate, missing, or unused record, unknown or
  trailing JSON field, invalid UTF-8 or unpaired surrogate, invalid canonical
  order/identity, size/digest mismatch, dangling frozen reference, or changed
  provider-authoritative occurrence metadata fails before output. Returned
  snapshot, freeze, and materialization failures never echo a reference, path,
  filename, MIME value, data-URI/base64 text, decoded bytes, or underlying
  parser/filesystem error.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SEC-001`, `FR-SEC-003` | [pkg/config/config_struct_test.go](../../pkg/config/config_struct_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-SEC-002`, `FR-SEC-007` | [pkg/credential/store_test.go](../../pkg/credential/store_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go), [pkg/auth/token_test.go](../../pkg/auth/token_test.go), [pkg/auth/pkce_test.go](../../pkg/auth/pkce_test.go), [pkg/mcp/auth_test.go](../../pkg/mcp/auth_test.go) |
| `FR-SEC-004` | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go) |
| `FR-SEC-005`, `FR-SEC-006` | [pkg/utils/http_guard.go](../../pkg/utils/http_guard.go), [pkg/isolation/runtime_test.go](../../pkg/isolation/runtime_test.go), [pkg/netbind/netbind_test.go](../../pkg/netbind/netbind_test.go), [pkg/mcp/network_test.go](../../pkg/mcp/network_test.go), [pkg/mcp/oauth_test.go](../../pkg/mcp/oauth_test.go), [web/backend/api/mcp_oauth_test.go](../../web/backend/api/mcp_oauth_test.go) |
| `FR-SEC-008` | [pkg/config/model_config_test.go](../../pkg/config/model_config_test.go), [pkg/config/model_alias_test.go](../../pkg/config/model_alias_test.go), [pkg/config/model_alias_migration_test.go](../../pkg/config/model_alias_migration_test.go), [pkg/config/model_selection_test.go](../../pkg/config/model_selection_test.go), [pkg/config/account_router_test.go](../../pkg/config/account_router_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go) |
| `FR-SEC-009` | [pkg/auth/oauth_test.go](../../pkg/auth/oauth_test.go), [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go) |
| `FR-SEC-010` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_webhook_deferred_test.go](../../web/backend/api/config_event_webhook_deferred_test.go) |
| `FR-SEC-011` | [pkg/config/events_channels_test.go](../../pkg/config/events_channels_test.go), [pkg/eventing/channelmessage/backend_test.go](../../pkg/eventing/channelmessage/backend_test.go), [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-SEC-012` | [pkg/config/events_webhook_format_test.go](../../pkg/config/events_webhook_format_test.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [pkg/prdevelopment/capture_test.go](../../pkg/prdevelopment/capture_test.go), [pkg/gateway/pr_development_capture_test.go](../../pkg/gateway/pr_development_capture_test.go) |
| `FR-SEC-013` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go) |
| `FR-SEC-014` | [pkg/health/server_test.go](../../pkg/health/server_test.go), [pkg/eventing/operator](../../pkg/eventing/operator), [pkg/gateway/event_operator_test.go](../../pkg/gateway/event_operator_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-SEC-015` | [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/fileutil/durable.go](../../pkg/fileutil/durable.go), [pkg/fileutil/durable_unix.go](../../pkg/fileutil/durable_unix.go), [pkg/fileutil/durable_windows.go](../../pkg/fileutil/durable_windows.go) |
| `FR-SEC-016` | [pkg/workflows/inspection.go](../../pkg/workflows/inspection.go), [pkg/workflows/inspection_open_unix.go](../../pkg/workflows/inspection_open_unix.go), [pkg/workflows/inspection_open_other.go](../../pkg/workflows/inspection_open_other.go), [pkg/workflows/inspection_test.go](../../pkg/workflows/inspection_test.go), [pkg/workflows/inspection_open_unix_test.go](../../pkg/workflows/inspection_open_unix_test.go), [web/backend/api/workflow_inspection.go](../../web/backend/api/workflow_inspection.go), [web/backend/api/workflow_inspection_test.go](../../web/backend/api/workflow_inspection_test.go), [web/frontend/src/components/workflows/workflow-definition-inspector.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.tsx), [web/frontend/src/components/workflows/workflow-definition-inspector.test.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.test.tsx) |
| `FR-SEC-017` | [pkg/workflows/authoring_capabilities.go](../../pkg/workflows/authoring_capabilities.go), [pkg/workflows/authoring_capabilities_test.go](../../pkg/workflows/authoring_capabilities_test.go), [pkg/agent/workflow_authoring.go](../../pkg/agent/workflow_authoring.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/gateway/workflow_authoring.go](../../pkg/gateway/workflow_authoring.go), [pkg/gateway/workflow_authoring_test.go](../../pkg/gateway/workflow_authoring_test.go), [web/backend/api/workflow_authoring.go](../../web/backend/api/workflow_authoring.go), [web/backend/api/workflow_authoring_test.go](../../web/backend/api/workflow_authoring_test.go), [web/frontend/src/api/workflow-capabilities.test.ts](../../web/frontend/src/api/workflow-capabilities.test.ts), [web/frontend/src/components/workflows/workflow-capability-catalog.test.tsx](../../web/frontend/src/components/workflows/workflow-capability-catalog.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-018` | [pkg/workflows/editor_jobs.go](../../pkg/workflows/editor_jobs.go), [pkg/workflows/editor_jobs_test.go](../../pkg/workflows/editor_jobs_test.go), [web/backend/api/workflow_jobs_editor.go](../../web/backend/api/workflow_jobs_editor.go), [web/backend/api/workflow_jobs_editor_test.go](../../web/backend/api/workflow_jobs_editor_test.go), [web/frontend/src/api/workflow-jobs-editor.test.ts](../../web/frontend/src/api/workflow-jobs-editor.test.ts), [web/frontend/src/components/workflows/workflow-job-editor.test.tsx](../../web/frontend/src/components/workflows/workflow-job-editor.test.tsx), [web/frontend/src/components/workflows/workflow-capability-target-field.test.tsx](../../web/frontend/src/components/workflows/workflow-capability-target-field.test.tsx), [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx), [web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx](../../web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-019` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/workflows/trigger_simulation_test.go](../../pkg/workflows/trigger_simulation_test.go), [pkg/workflows/development_test_admission_test.go](../../pkg/workflows/development_test_admission_test.go), [web/backend/api/workflow_event_context_test.go](../../web/backend/api/workflow_event_context_test.go), [web/backend/api/workflow_trigger_simulation_test.go](../../web/backend/api/workflow_trigger_simulation_test.go), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows/workflow-trigger-simulator.test.tsx](../../web/frontend/src/components/workflows/workflow-trigger-simulator.test.tsx), [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx), [web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx](../../web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-020` | [pkg/agent/definition_test.go](../../pkg/agent/definition_test.go), [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go), [web/backend/api/agent_capabilities_cas_test.go](../../web/backend/api/agent_capabilities_cas_test.go), [web/backend/api/agent_capabilities_replace_linux_test.go](../../web/backend/api/agent_capabilities_replace_linux_test.go), [web/backend/api/agent_capabilities_request_test.go](../../web/backend/api/agent_capabilities_request_test.go), [web/backend/api/agent_capabilities_unix_test.go](../../web/backend/api/agent_capabilities_unix_test.go), [pkg/agent/activity_test.go](../../pkg/agent/activity_test.go), [pkg/gateway/agent_activity_test.go](../../pkg/gateway/agent_activity_test.go), [pkg/gateway/listen_test.go](../../pkg/gateway/listen_test.go), [web/backend/api/agent_activity_test.go](../../web/backend/api/agent_activity_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts) |
| `FR-SEC-021` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [web/backend/api/config_writer_cas_test.go](../../web/backend/api/config_writer_cas_test.go), [web/backend/api/review_attention_policies_test.go](../../web/backend/api/review_attention_policies_test.go), [web/backend/api/review_attention_agents_test.go](../../web/backend/api/review_attention_agents_test.go), [cmd/picoclaw/internal/auth/config_revision_test.go](../../cmd/picoclaw/internal/auth/config_revision_test.go), [cmd/picoclaw/internal/mcp/command_test.go](../../cmd/picoclaw/internal/mcp/command_test.go), [cmd/picoclaw/internal/model/command_test.go](../../cmd/picoclaw/internal/model/command_test.go), [web/frontend/src/api/review-attention-agents.test.ts](../../web/frontend/src/api/review-attention-agents.test.ts), [web/frontend/src/api/review-attention-json.test.ts](../../web/frontend/src/api/review-attention-json.test.ts), [web/frontend/src/api/review-attention-policies.test.ts](../../web/frontend/src/api/review-attention-policies.test.ts), [web/frontend/src/components/reviews/review-attention-policy-model.test.ts](../../web/frontend/src/components/reviews/review-attention-policy-model.test.ts), [web/frontend/src/components/reviews/review-attention-policies-page.test.tsx](../../web/frontend/src/components/reviews/review-attention-policies-page.test.tsx), [web/frontend/src/components/reviews/reviews-page.test.tsx](../../web/frontend/src/components/reviews/reviews-page.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts) |
| `FR-SEC-022` | [pkg/media/store_test.go](../../pkg/media/store_test.go), [pkg/media/snapshot_test.go](../../pkg/media/snapshot_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go), [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go), [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go) |
| `FR-SEC-023` | [pkg/workflows/private_context.go](../../pkg/workflows/private_context.go), [pkg/workflows/private_session.go](../../pkg/workflows/private_session.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/workflows/store.go](../../pkg/workflows/store.go), [pkg/workflows/development.go](../../pkg/workflows/development.go), [pkg/workflows/gates_test.go](../../pkg/workflows/gates_test.go), [pkg/workflows/private_context_security_test.go](../../pkg/workflows/private_context_security_test.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/accountrouter/router.go](../../pkg/accountrouter/router.go), [pkg/accountrouter/router_test.go](../../pkg/accountrouter/router_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [pkg/reviews/attention_test.go](../../pkg/reviews/attention_test.go), [pkg/eventing/review_decision_run_sqlite_test.go](../../pkg/eventing/review_decision_run_sqlite_test.go), [pkg/eventing/review_attention_trigger_sqlite_test.go](../../pkg/eventing/review_attention_trigger_sqlite_test.go), [pkg/gateway/review_attention_trigger_test.go](../../pkg/gateway/review_attention_trigger_test.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_run_readiness_test.go](../../web/backend/api/workflow_run_readiness_test.go), [web/backend/api/workflow_runtime.go](../../web/backend/api/workflow_runtime.go), [web/backend/api/workflow_runtime_test.go](../../web/backend/api/workflow_runtime_test.go) |
| `FR-SEC-024` | [pkg/reviews/attention_bridge.go](../../pkg/reviews/attention_bridge.go), [pkg/reviews/attention_bridge_test.go](../../pkg/reviews/attention_bridge_test.go), [pkg/reviews/attention_bridge_sqlite_test.go](../../pkg/reviews/attention_bridge_sqlite_test.go), [pkg/reviews/workflow_retention.go](../../pkg/reviews/workflow_retention.go), [pkg/gateway/review_attention_bridge_test.go](../../pkg/gateway/review_attention_bridge_test.go), [web/backend/api/reviews_test.go](../../web/backend/api/reviews_test.go), [web/backend/api/agent_activity.go](../../web/backend/api/agent_activity.go), [web/backend/api/workflow_attention_privacy.go](../../web/backend/api/workflow_attention_privacy.go), [web/backend/api/review_attention_workflow_suppression_test.go](../../web/backend/api/review_attention_workflow_suppression_test.go), [cmd/picoclaw/internal/workflow/retention_test.go](../../cmd/picoclaw/internal/workflow/retention_test.go), [web/frontend/src/api/review-attention.test.ts](../../web/frontend/src/api/review-attention.test.ts), [web/frontend/src/components/reviews/reviews-page.test.tsx](../../web/frontend/src/components/reviews/reviews-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts) |
| `FR-SEC-025` | [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/eventing/operator/pr_development_delegation_test.go](../../pkg/eventing/operator/pr_development_delegation_test.go), [web/backend/main.go](../../web/backend/main.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/backend/middleware/launcher_dashboard_auth_test.go](../../web/backend/middleware/launcher_dashboard_auth_test.go), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-026` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_conversation_schema_sqlite.go](../../pkg/eventing/pr_development_conversation_schema_sqlite.go), [pkg/eventing/pr_development_conversation_store_sqlite.go](../../pkg/eventing/pr_development_conversation_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/review_store_sqlite.go](../../pkg/eventing/review_store_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/pr_development_conversation_store_sqlite_test.go](../../pkg/eventing/pr_development_conversation_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/prdevelopment/chat_test.go](../../pkg/prdevelopment/chat_test.go), [pkg/workflows/context.go](../../pkg/workflows/context.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/channels/manager.go](../../pkg/channels/manager.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [web/backend/api/reviews.go](../../web/backend/api/reviews.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/i18n/locales/en.json](../../web/frontend/src/i18n/locales/en.json), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-027` | [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/github_case_test.go](../../pkg/prdevelopment/github_case_test.go), [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go), [pkg/tools/toolloop_test.go](../../pkg/tools/toolloop_test.go), [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go), [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-SEC-028` | [pkg/eventing/pr_development_repair_schema_sqlite.go](../../pkg/eventing/pr_development_repair_schema_sqlite.go), [pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go), [pkg/eventing/pr_development_repair_store_sqlite_test.go](../../pkg/eventing/pr_development_repair_store_sqlite_test.go), [pkg/prdevelopment/repair_worker.go](../../pkg/prdevelopment/repair_worker.go), [pkg/prdevelopment/repair_worker_test.go](../../pkg/prdevelopment/repair_worker_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/agent/local_repair_factory.go](../../pkg/agent/local_repair_factory.go), [pkg/agent/local_repair_factory_test.go](../../pkg/agent/local_repair_factory_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [pkg/gateway/pr_development_repair_runtime_test.go](../../pkg/gateway/pr_development_repair_runtime_test.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [pkg/config/config_struct.go](../../pkg/config/config_struct.go)
- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/config/model_selection.go](../../pkg/config/model_selection.go)
- [pkg/config/model_selection_compatibility.go](../../pkg/config/model_selection_compatibility.go)
- [pkg/config/mutation.go](../../pkg/config/mutation.go)
- [pkg/config/events.go](../../pkg/config/events.go)
- [pkg/prdevelopment/capture.go](../../pkg/prdevelopment/capture.go)
- [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go)
- [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go)
- [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go)
- [pkg/eventing/pr_development_conversation_schema_sqlite.go](../../pkg/eventing/pr_development_conversation_schema_sqlite.go)
- [pkg/eventing/pr_development_conversation_store_sqlite.go](../../pkg/eventing/pr_development_conversation_store_sqlite.go)
- [pkg/agent/agent.go](../../pkg/agent/agent.go)
- [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go)
- [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go)
- [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go)
- [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go)
- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/channels/manager.go](../../pkg/channels/manager.go)
- [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go)
- [web/backend/main.go](../../web/backend/main.go)
- [web/backend/api/config.go](../../web/backend/api/config.go)
- [web/backend/api/review_attention_policies.go](../../web/backend/api/review_attention_policies.go)
- [web/frontend/src/api/review-attention-json.ts](../../web/frontend/src/api/review-attention-json.ts)
- [web/frontend/src/api/review-attention.ts](../../web/frontend/src/api/review-attention.ts)
- [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts)
- [web/frontend/src/components/reviews](../../web/frontend/src/components/reviews)
- [web/frontend/src/i18n/locales/en.json](../../web/frontend/src/i18n/locales/en.json)
- [pkg/auth/oauth.go](../../pkg/auth/oauth.go)
- [pkg/auth/store.go](../../pkg/auth/store.go)
- [pkg/mcp/network.go](../../pkg/mcp/network.go)
- [pkg/mcp/oauth.go](../../pkg/mcp/oauth.go)
- [pkg/credential](../../pkg/credential)
- [pkg/fileutil](../../pkg/fileutil)
- [pkg/media/store.go](../../pkg/media/store.go)
- [pkg/media/snapshot.go](../../pkg/media/snapshot.go)
- [pkg/media/snapshot_file.go](../../pkg/media/snapshot_file.go)
- [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go)
- [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go)
- [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go)
- [pkg/media/frozen.go](../../pkg/media/frozen.go)
- [pkg/session/frozen_media.go](../../pkg/session/frozen_media.go)
- [pkg/isolation](../../pkg/isolation)
- [pkg/workflows/inspection.go](../../pkg/workflows/inspection.go)
- [pkg/workflows/inspection_open_unix.go](../../pkg/workflows/inspection_open_unix.go)
- [pkg/workflows/inspection_open_other.go](../../pkg/workflows/inspection_open_other.go)
- [pkg/workflows/authoring_capabilities.go](../../pkg/workflows/authoring_capabilities.go)
- [pkg/workflows/editor_jobs.go](../../pkg/workflows/editor_jobs.go)
- [pkg/workflows/trigger_simulation.go](../../pkg/workflows/trigger_simulation.go)
- [pkg/workflows/development_test_admission.go](../../pkg/workflows/development_test_admission.go)
- [pkg/workflows/context.go](../../pkg/workflows/context.go)
- [pkg/workflows/private_context.go](../../pkg/workflows/private_context.go)
- [pkg/workflows/private_session.go](../../pkg/workflows/private_session.go)
- [pkg/workflows/executor.go](../../pkg/workflows/executor.go)
- [pkg/workflows/store.go](../../pkg/workflows/store.go)
- [pkg/reviews/attention_bridge.go](../../pkg/reviews/attention_bridge.go)
- [pkg/accountrouter/router.go](../../pkg/accountrouter/router.go)
- [pkg/tools/workflow.go](../../pkg/tools/workflow.go)
- [pkg/agent/workflow_authoring.go](../../pkg/agent/workflow_authoring.go)
- [pkg/gateway/workflow_authoring.go](../../pkg/gateway/workflow_authoring.go)
- [web/backend/api/workflow_inspection.go](../../web/backend/api/workflow_inspection.go)
- [web/backend/api/workflow_authoring.go](../../web/backend/api/workflow_authoring.go)
- [web/backend/api/workflow_jobs_editor.go](../../web/backend/api/workflow_jobs_editor.go)
- [web/backend/api/workflow_event_context.go](../../web/backend/api/workflow_event_context.go)
- [web/backend/api/workflows.go](../../web/backend/api/workflows.go)
- [web/backend/api/reviews.go](../../web/backend/api/reviews.go)
- [web/backend/api/workflow_human_tasks.go](../../web/backend/api/workflow_human_tasks.go)
- [web/backend/api/workflow_trigger_simulation.go](../../web/backend/api/workflow_trigger_simulation.go)
- [web/frontend/src/components/workflows/workflow-capability-catalog.tsx](../../web/frontend/src/components/workflows/workflow-capability-catalog.tsx)
- [web/frontend/src/components/workflows/workflow-job-editor.tsx](../../web/frontend/src/components/workflows/workflow-job-editor.tsx)
- [web/frontend/src/components/workflows/workflow-trigger-simulator.tsx](../../web/frontend/src/components/workflows/workflow-trigger-simulator.tsx)
- [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.tsx)
- [web/frontend/src/components/workflows/workflow-definition-inspector.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.tsx)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
