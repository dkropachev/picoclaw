# Tool Execution

## Feature ID

`FR-TOOL`

## Behavior Summary

PicoClaw exposes built-in tools to the agent for filesystem access, shell
execution, web search/fetch, media delivery, hardware access, and channel
actions. The registry presents tool schemas to providers and executes tool calls
with context, limits, filtering, and error normalization. A file-backed media
store can additionally expose an optional bounded snapshot read that detaches
the bytes of one currently live media capability without disclosing its backing
path. Trusted callers can opt one otherwise generic tool loop into sequential
response-order execution and private diagnostic suppression, and can construct
an `apply_patch` tool whose complete path set is guarded before mutation.
An owner-aware factory path also constructs strict registries with
frozen schemas, conservative execution traits, isolated mutable instances, and
explicit immutable sharing while legacy registration and shallow cloning keep
their existing behavior. A compatibility registry can additionally attach that
factory metadata to its existing live root tool and construct an exact selected
owned subset without invoking or publishing excluded entries. Production
catalogs classify base filesystem, web, Git, hardware, delivery, image, plan,
skill-search, MCP/discovery, Seahorse, install-skill, and recursion wrappers
through the same factory contracts and atomic multi-registry installer. Root
execution remains compatibility-backed; SubTurns intersect exact authority and
construct fresh selected turn owners. Tracked `spawn` is backgrounded by its
owner-local manager while its direct child uses synchronous result mode; the
committed task identity drives one Agent Conversations exact-session result
envelope instead of the generic async callback and direct-SubTurn channel paths.

## Reconstruction Notes

- Similarity target: recreate a concurrent tool registry plus built-in tools for filesystem, exec, web, media, hardware, and channel action behavior.
- Core types/functions: `Tool` interface, `ToolRegistry`, tool result types,
  filesystem tool constructors, exec session manager, web search/fetch
  providers, tool schema transforms, `media.SnapshotReader`, and
  `media.Snapshot`, plus suppression-aware `ToolLoopConfig` and guarded patch
  construction, owner-local `SubagentManager`, committed completion callback
  context, and `SpawnTool` direct-runner configuration.
- Runtime ordering: register enabled tools, export provider schemas, validate tool call args/context, enforce path/network/command policies, execute, filter result, normalize output; when an isolated consumer requests media capture, resolve and bounded-read the live capability atomically before detaching its bytes.
- Non-obvious constraints: response-handled tools suppress duplicate assistant text, registry must recover panics, workspace restriction and allow path patterns must be checked before file mutation, and snapshot reading is an optional capability that neither exposes a local path nor makes an expired in-memory reference durable. Sequential execution and argument suppression are explicit per-loop options whose zero values preserve existing behavior; guarded patches validate every source and move destination before operation one. The tracked spawn manager provides background execution while its direct child uses `Async:false` and `Critical:true`; only the committed callback may hand one result to Agent Conversations. Generic async tools and direct `Async:true` SubTurns retain their existing delivery behavior.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-TOOL-001` | MUST | Tool registry registration, unregistration, lookup, definition export, cloning, allowlist filtering, and execution are concurrency-safe. A bounded read-only visitor can inspect admitted entries under one coherent registry snapshot for workflow authoring without copying the complete registry, exposing hidden entries, or mutating tool state. | Agent turns can execute tools while discovery and authoring inspection change visibility. |
| `FR-TOOL-002` | MUST | Filesystem tools respect workspace restriction, allow path patterns, file size limits, and operation-specific semantics for read/write/edit/append/list/image/send. | Local file access is powerful and must be bounded. |
| `FR-TOOL-003` | MUST | Exec runs commands with configured timeout and deny/allow patterns, supports managed sessions, and returns captured output or structured failure. | Shell access must be useful and controllable. |
| `FR-TOOL-004` | MUST | Web search selects configured providers, honors result/range options, and web fetch observes fetch limits and private host controls. | Search and fetch must be deterministic from config. |
| `FR-TOOL-005` | MUST | Sensitive-data filtering redacts configured secrets from tool results before model exposure when enabled. | Models must not see credentials through tool output. |
| `FR-TOOL-006` | SHOULD | Media, reaction, message, TTS, and hardware tools return handled responses when user-visible delivery is completed outside normal assistant text. | The agent should not duplicate already-delivered output. |
| `FR-TOOL-007` | MUST | Tool schema transformations preserve provider compatibility for OpenAI, Anthropic, Gemini, and compatibility adapters. | Provider-specific schemas must not change tool behavior. |
| `FR-TOOL-008` | MUST | Chat account selection excludes internal virtual model entries and model-router rows but keeps enabled account routers and credential accounts selectable as `account_ref` targets. A separate selector exposes configured exact model aliases and enabled model routers as `model_name`; when a persisted account is valid but the persisted alias is empty, it uses an explicitly configured `chat` alias as a turn-local fallback without rewriting configuration. It never promotes a fetched upstream model ID into a turn selection. | Stored credentials and model-agnostic account routers must remain usable without mixing account identity with model identity or inventing a router/provider default. |
| `FR-TOOL-009` | SHOULD | Tool adaptation config chooses and pins a visible tool surface per model/API profile, supports provider/model-specific surface and cache-policy overrides, records provider-reported cache-token observations and per-tool success/failure outcomes when available, treats runtime visible tool changes as cache-aware decisions, supports explicit harmless targeted tool-call probes, persists learned cache/probe state in a stable local state file, exposes searchable resolved/learned/probe state for router-expanded profiles in the UI, and exposes truthful Codex-compatible wrappers for shell, input-only stdin, patch, and image capabilities when the Codex surface is selected. Volatile plan state remains reserved until it is backed by the durable coding-task runtime. | PicoClaw runs many providers; equivalent capabilities should be exposed in the shape each model uses best without breaking prompt/tool cache unnecessarily or presenting process-local placeholders as durable coding state. |
| `FR-TOOL-010` | SHOULD | The `/agent/tools` page stores the selected Tool Library, Web Search, Thread Policy, or Adaptation tab in the route search params so tab views can be linked, refreshed, and restored through browser history. | Tool configuration work often spans multiple views, and URL-addressable tabs make navigation predictable. |
| `FR-TOOL-011` | MUST | Before `spawn` launches background subturn work, an AgentLoop-backed spawner synchronously retains the caller's runtime generation and a short parent tool-source construction lease. SpawnTool first snapshots the exact manager, direct spawner, model, nil-versus-empty fallbacks, token/temperature values, prompt, target, callback, and trusted source agent/session/channel/chat. At most the configured child-concurrency count of queued source admissions may coexist per parent; validation, authorization, preparation, invalid delivery route, or excess-admission failure creates no manager record or goroutine. After successful preparation, the exact manager shared with that owner's `spawn_status` inserts one `running` record and manager-lifetime task ID before owning the only goroutine; the immediate acknowledgement preserves its wording and includes that ID. The goroutine never rereads wrapper/manager configuration and waits only to the configured concurrency timeout or retained-context cancellation for one execution slot before consuming the source lease or calling a factory. It runs the direct child with `Tools:nil`, `Async:false`, and `Critical:true`: the manager supplies background execution, while direct synchronous-result mode prevents the legacy SubTurn pending channel from becoming another completion producer. Using the manager's legacy tool-loop/spawner snapshot for production is forbidden. The child releases its source lease after strict construction/attachment and its execution slot after `runTurn`. Exactly one copied terminal manager snapshot is committed before a non-nil callback result carrying the committed task ID/status is attempted outside manager locks; callback panic is contained, and runtime-generation ownership releases exactly once afterward. SpawnTool additionally wraps only this callback with a private first-party provenance marker, so public manager-backed/custom async tools remain on the generic callback path. The marked callback exclusively offers Agent Conversations one bounded filtered, composite-ID, exact-named-session, process-local at-most-once envelope; it never performs generic direct user output or synthetic system ingress. The envelope is not crash durable and does not promise exactly-once provider execution; a late continuation uses a fresh coherent runtime generation with no default-agent/session fallback. Generic asynchronous tools and direct `Async:true` SubTurns remain unchanged. | A parent turn may finish immediately after spawn acknowledgement; reload must drain admitted child work and later continue delivery coherently, status must be immediately truthful, and record/goroutine ownership must not regress P005c authority, leak mutable wrapper state, release the generation before completion handoff, or surface one tracked result more than once. |
| `FR-TOOL-012` | MUST | Tool execution context carries the active turn's opaque transient-UX identity. A child SubTurn inheriting the cloned inbound context retains that identity, and `message` propagates it only to a delivery targeting the same channel and chat as the tool context. Same-turn tool and stream output uses the additive turn-scoped delivery interfaces when available, with source-compatible legacy fallback, so stale output cannot consume a newer turn's typing, reaction, placeholder, or stream-finalization state. | Tool and subturn output can overlap later turns in the same chat; exact ownership prevents delayed cleanup from corrupting newer user-visible UX without leaking a chat-local identity to another destination. |
| `FR-TOOL-013` | MUST | Tool argument validation accepts finite lossless `json.Number` values for JSON Schema `number` fields and accepts them for `integer` fields only when their exact decimal value is integral. Exponent handling is bounded by the input length and never allocates or computes an exponent-sized value. | Durable event payloads preserve JSON numbers without `float64` precision loss, while untrusted numeric text must not cause precision drift or resource amplification during workflow tool calls. |
| `FR-TOOL-014` | MUST | Tool registry inspection applies the same allowlist decision used by registration and can return an existing occupant even when it is a hidden tool with expired TTL. MCP initialization uses this concurrency-safe inspection to validate every admitted canonical name before registering any wrapper, rejects collisions with built-in or differently identified MCP tools, and permits replacement only for the exact same original MCP server/tool identity. | Flattened MCP names can collide with existing or differently partitioned identities; preflight must fail without partially exposing the new MCP surface or overwriting the current occupant. |
| `FR-TOOL-015` | MUST | Agent-callable workflow `dev_publish` fails closed unless its runtime injects effective workflow enablement, definitions directory, call-depth policy, and a live dependency resolver. The gate parses and validates the exact active draft, walks reusable dependencies within fixed workflow analysis budgets, resolves production readiness, and returns an opaque revision bound to the exact draft, sorted reachable dependency bytes or absence, effective gate values, and readiness report. Publish submits the active session, draft, target pre-image, and dependency revisions to the fenced workflow publisher, which rejects a missing, blocked, stale, or changed gate result. | Agent-side publishing must enforce the same exact dependency and optimistic-concurrency fences as dashboard publishing rather than bypassing production readiness. |
| `FR-TOOL-016` | MUST | The authenticated tool library exposes the configured workflow-tool flag independently from workflow master enablement: configured off is `disabled`, configured on while workflows are off is `blocked` with `requires_workflows`, and both on is `enabled`. The blocked switch remains checked and operable so the raw flag can be turned off, and its reason is accessibly associated. `PUT /api/tools/{name}/state` requires exactly one `application/json` media type and accepts one bounded strict JSON object with a required boolean `enabled`, rejects null/scalar/array, duplicate/unknown/trailing data, and saves an update-safe public-plus-security config snapshot only when its exact generation still matches under the shared handler and advisory mutation locks. Changing the workflow tool never implicitly changes workflow master enablement; the tool library and workflow settings refetch each other after every mutation outcome. | Browser tool state must match runtime registration without hiding a disabled prerequisite, implicitly enabling automation, or overwriting a concurrent settings or credential update. |
| `FR-TOOL-017` | MUST | A model-backed web-search provider requires an explicit configured model alias and never substitutes a provider default. Since Gemini and Perplexity search own their API credentials rather than selecting a model account, they resolve the alias base mapping; account overrides apply only to executions with a concrete model account. | Search must use the same explicit model-selection vocabulary without pretending that a provider-owned credential is an account-router target. |
| `FR-TOOL-018` | MUST | `FileMediaStore` implements the optional `media.SnapshotReader.ReadSnapshot(ctx, ref, maxBytes)` capability. For one currently registered canonical `media://` UUID reference and a positive caller limit, it holds lifecycle synchronization from lookup through close, opens the mapped entry without following a final symlink or blocking on a special file, verifies the opened handle is regular, and performs one bounded read whose actual bytes cannot exceed the caller limit. Unix uses no-follow plus nonblocking open and a status-change token; Windows opens the final entry itself, rejects every handle carrying the reparse-point attribute, and compares handle change time; other platforms fail closed instead of using a race-prone fallback. Success returns detached bytes and copied metadata, never the backing path or mutable store state. Missing, expired, malformed, unsafe, observably changed-size/modtime/identity/change-token, oversized, cancelled, and safe-open-unavailable captures fail with fixed redacted errors that contain no reference, path, metadata, payload, or raw operating-system error. Store registration normalizes a cleaned absolute exact lexical lifecycle key without an approximate case fold and only coalesces that key when `Lstat` reports the same captured entry identity. A `SameFile` identity found under a distinct key is retained separately and makes all such live lifecycles non-deleting, conservatively covering Windows path aliases without conflating hard-link paths. Re-registration permanently cancels older pending deletion through either the exact key or captured `SameFile` identity; final removal rechecks its token and identity under store synchronization, preserving an already replaced entry. Consequently, an earlier release cannot delete a newly registered path or race a pinned snapshot. The base `MediaStore` interface remains source-compatible, and capture does not promise recovery after release, expiry, store reconstruction, or process restart. | A session freezer needs a race-safe way to detach live capability bytes without extending ordinary temporary-media lifetime or turning local file details into model-visible diagnostics. |
| `FR-TOOL-019` | MUST | A `ToolLoopConfig` caller may independently request response-order sequential tool execution and suppression of tool arguments and result-derived detail in process logs; both options default off. Sequential mode executes one model-authored call at a time in declared order, preserves thought signatures and call/result identifiers in provider follow-up messages, stops at cancellation boundaries, and normalizes nil or panic-derived results without concurrent sibling execution. Suppression propagates through registry execution into shared filesystem helpers so tool names and coarse outcomes remain observable while raw arguments, paths, validation detail, panic detail, and result/error content do not. `NewApplyPatchToolWithPathGuard` additionally parses the whole patch and validates every source path and move destination before its first operation, while the ordinary constructor retains existing behavior. | Narrow repair controllers need reusable tool primitives that preserve deterministic edit ordering and diagnostics without leaking confined checkout details or allowing a later patch operation to bypass preflight. |
| `FR-TOOL-020` | MUST | Tool registrations may declare a validated construction `ToolOwner`, conservative `ToolTraits` for risk, parallel behavior, idempotency, and sharing, and a concurrency-safe `ToolFactory` with one exact recursively frozen descriptor. Each per-owner construction returns a non-nil pointer that is not reserved by another live strict owner. Strict owner instantiation transactionally creates distinct per-owner mutable instances, recursively resolves exact dependencies, keeps an owner-local service cache, injects the destination media store, preserves registry key, core/hidden status, TTL, allowlist, prompt metadata, provider schema, and version, and publishes no partial registry on nil, panic, dependency, identity, schema, media-generation, or factory failure. Newly created pointer-valued services use the same live-owner isolation, while immutable scalar services require no lease. Only an explicitly immutable and parallel-safe non-media-aware tool may share its pointer. Every immutable-shared registry and descendant lifetime holds a ref-counted strong shared lease; shared leases coexist with each other but exclude compatibility, factory-product, and pointer-service exclusive leases until the last share closes. Global strong live-instance reservations prevent singleton reuse across overlapping live owner lifetimes, whether those owners are constructed sequentially or concurrently and whether they come from different entries, factories, registry lineages, or roots. After its owner supervisor has externally quiesced every registry API, factory registration or instantiation, tool execution, and retained tool use, idempotent strict-registry `Close` releases owner-created resources in reverse order before releasing reservations; cleanup error or panic permanently quarantines the reservations, while legacy unowned close is a no-op. Factory-backed validation and every outward projection, including workflow-authoring inspection, use detached frozen metadata; traits remain runtime-only and model-invisible. Legacy `Register` and `RegisterHidden` remain source-compatible. Legacy unowned shallow `Clone` retains its behavior, while cloning an owned registry fails closed with an empty unowned view. | Child, task, and agent owners need stable metadata and isolated mutable tool state without silently changing the legacy general-agent registry surface or allowing schema/state leakage between siblings. |
| `FR-TOOL-021` | MUST | An open compatibility registry may atomically register one live core or hidden tool together with a descriptor-identical per-owner factory without calling `Factory.New`; allowlist rejection never calls `Factory.New`, collisions never overwrite, the root continues executing the supplied live pointer, and a process-wide strong compatibility lease prevents any strict exclusive or immutable-shared owner from receiving that pointer. After external quiescence, compatibility-source `Close` clears the source and releases those leases without closing caller-owned live tools, while a plain unowned legacy registry with no compatibility leases retains no-op close behavior and shallow clones acquire no lease. `InstantiationCapabilities` returns a sorted detached classification for every live entry without exposing pointers or factories. `InstantiateForOwnerSelection` requires a non-nil exact duplicate-free root set: empty constructs an empty exact-registration-capped owned registry without calling `Factory.New`; unknown, blank, duplicate, or selected legacy roots fail before construction; unselected legacy entries are ignored. The immutable exact-name cap survives mutable case-insensitive allowlist changes and suppresses unselected discovery exceptions. Selected roots alone retain callable core/hidden status, TTL, and per-entry visibility revision. A selected factory may resolve another classified entry as an owner-retained private dependency. Resolved owner-built products remain in a disjoint private product map, while the complete frozen classified spec catalog is copied without invoking constructors so owner-dependent descendants can resolve a different dependency. Both remain absent from capability snapshots, registry lookup, provider definitions, discovery, execution, and direct selection unless an entry is also a selected root. Owner media changes include built private products, and factory re-registration may resolve them without exposing them. Every constructed pointer is distinct from every source and owner product, and source close, entry/version, media generation, private-product/spec identity, or selected visibility-revision change aborts atomically with the cleanup and quarantine guarantees of `FR-TOOL-020`. These APIs do not by themselves change agent registration or SubTurn behavior. | Child-authority intersection must filter before constructors run, represent an exact empty surface, and support wrapper dependencies without either exposing them or cloning mutable root pointers. |
| `FR-TOOL-022` | MUST | `NewToolFactoryFromPrototype` panic-safely freezes the complete descriptor and prompt metadata of one live prototype. A compatibility registry may retain a per-owner `RegisterFactoryDependency` outside its public allowlist without constructing it; the dormant factory is absent from lookup, execution, definitions, discovery, capabilities, and direct selection, but an owner-construction factory may resolve it privately. An exact same-factory public registration transactionally promotes and replaces the dormant entry, while allowlist rejection leaves it intact and descriptor, trait, factory-identity, owner-product, source-version, media-generation, close, or catalog mutation ambiguity fails without publication or leaked products. The base production catalog uses these contracts for exactly `read_file`, `edit_file`, `append_file`, `write_file`, `apply_patch`, `list_dir`, `update_plan`, `web_search`, `web_fetch`, `git_workspace`, `i2c`, `spi`, `serial`, `message`, `reaction`, `send_file`, `send_tts`, `load_image`, `view_image`, and `find_skills`. Every mutable wrapper is fresh per owner; configuration inputs are frozen and slices detached; generation Git, channel, TTS, and skills services are borrowed and not closed by the wrapper owner; media is destination-injected; `view_image` privately resolves `load_image`. Exec compatibility, threads, workflow, cron, and external injected tools remain explicitly unclassified; dynamic MCP/discovery, Seahorse, install-skill, and recursion catalogs attach their owning feature's factories through `FR-TOOL-023`. Root execution remains compatibility-backed, while SubTurns use exact selected owner construction through Agent Conversations. | Production registration needs an auditable, non-widening catalog before child paths construct exact isolated surfaces. |
| `FR-TOOL-023` | MUST | `InstallFactoryBackedTransaction` accepts ordered batches for distinct open compatibility registries and atomically inserts or exact-pointer-replaces admitted core or hidden factory-backed entries across all batches without calling `ToolFactory.New`. A nil expected occupant means the public slot must be absent; a non-nil expected occupant must be the exact current non-nil tool pointer. Structural errors, duplicate registry aliases or names, owned or uninitialized registries, an unexpected occupant, a private-product collision, and a public-plus-dormant ambiguity fail before publication. Allowlist-denied entries retain their input-order detached admission result but their live descriptor, media setter, pointer reservation, public slot, and dormant catalog remain untouched. Every admitted live pointer is globally reserved before compatibility code runs; its frozen factory descriptor is checked before and after panic-contained media injection. All participating media and registry locks use one deterministic order, and close, ownership, definition-version, relevant allowlist, media-generation, exact entry, visibility-revision, expected-occupant, or dormant-catalog changes abort the whole transaction. Commit publishes every map and new compatibility lease before exposing any version increment, then advances each affected registry once per admitted entry. Failure publishes no candidate and releases every new reservation; replacement keeps the prior compatibility lease until source close and never closes either caller-owned live wrapper. | Dynamic MCP, discovery, Seahorse, install-skill, and recursion catalogs must be able to stage one generation and expose it to every agent registry as an all-or-none factory-backed update. |
| `FR-TOOL-024` | MUST | `NewMCPToolWithFactory` validates one non-nil borrowed MCP manager, exact non-empty server/tool identity, and one untrusted SDK tool declaration, then returns a compatibility wrapper plus a descriptor-identical per-owner factory without retaining the SDK pointer or trusting remote annotations. Nil input schema freezes as an empty object; direct maps are recursively detached, while raw bytes and other JSON-marshalable shapes decode with lossless numbers as exactly one object. Typed-nil, malformed, trailing, non-object, cyclic, unsupported, and non-finite schemas fail without a wrapper or factory. The snapshot fixes canonical identity, final prefixed description, nested schema, prompt metadata, trimmed workspace, effective inline-text limit, and event publisher. Traits are always unknown-risk, serialized, unknown-idempotency, and per-owner. Each factory call returns a distinct wrapper using the same borrowed manager/runtime-event service and independent destination-injected media state; legacy workspace/limit/publisher setters cannot rewrite a strict snapshot, wrapper/registry close never closes borrowed services, and parameter projections are detached on every read. The source-compatible legacy MCP constructor also snapshots SDK identity/description/schema immediately and retains its historical empty-object fallback for malformed non-nil schema, while its mutable runtime setters are synchronized with execution. This API alone does not change current agent MCP registration. | A later atomic MCP catalog must construct isolated owner wrappers without a remote SDK object mutating provider identity/schema, unsafe annotations widening policy, or one owner's media state leaking into another. |
| `FR-TOOL-025` | MUST | The Phase-0 `exec_command` compatibility schema is a closed object containing only `cmd`, `workdir`, `background`, and `tty`; `cmd` is required and nonblank, and `tty:true` requires `background:true`. The `write_stdin` schema is a closed object containing required nonblank `session_id` and required nonempty `chars`; characters are forwarded without trimming and the result reports status without reading buffered output. Removed yield, timeout, shell, login, and output-budget fields, arbitrary unknown fields, and wrong types fail in direct calls as well as registry calls before backend dispatch. Background execution returns snake-case `session_id` plus status so it chains directly into input-only stdin. The factory-backed `update_plan` remains available to trusted direct/native callers but is omitted from every normal adapted Agent Pipeline definition, rejected when model-authored before or after a tool-hook rewrite, excluded from callable-tool prompts and Codex probes, and reported blocked with `requires_durable_plan` in the capability catalog. | Compatibility schemas must describe behavior the current runtime actually implements; full yielding, polling, output cursors, escalation, and durable plan state belong to their task-scoped runtimes. |
| `FR-TOOL-026` | MUST | Every managed background process is owned by one exact nonblank `ProcessSessionOwner{AgentID, SessionKey}` taken from trusted tool context. Owner fields containing surrounding whitespace, missing one member, or otherwise invalid fail before process start or managed lookup and are never normalized. The process-global manager stores owner and an immutable safe metadata snapshot privately; list, get, poll, destructive read, write, PTY keys, kill, and exact-pointer removal require exact owner equality. A foreign existing ID is indistinguishable from an absent ID and cannot expose metadata/status/output, drain output, write input, inspect key mode, signal a process, or remove a record. Owner is absent from responses, logs, and errors. Session IDs are reserved through opaque exact tokens before process start; visible records and reservations reject collision, invalid/incomplete session publication, or pointer reuse. Fully initialized records promote before reader/wait goroutines start; every failed setup/promotion releases only its token and closes/terminates/waits each acquired process resource exactly once. PTY completion fences new input as soon as the tracked command is reaped, drains terminal output through an interruptible master for a bounded interval, closes and joins the reader, and only then publishes terminal status and signals wait completion; a detached slave holder cannot extend that lifecycle indefinitely. Removal checks not-found/foreign before the expected-pointer fence, so kill/cleanup/ID-reuse races never delete a replacement. Ownerless synchronous commands remain compatible, while every ownerless managed operation fails closed. Arbitrary trusted in-process Go retaining an authorized `*ProcessSession` remains outside this model/tool-context boundary. | All agent and compatibility exec tools borrow one global process manager; a short session ID must not become a cross-agent capability or be silently rebound by collision/reload/race. |
| `FR-TOOL-027` | MUST | `apply_patch` parses exact patch text and builds one complete immutable preflight plan before its first filesystem effect. A process-global coordinator serializes only aliases of the same canonical workspace while unrelated workspaces remain independent and cancellation-aware. The planner checks permissions, caller guard, portable raw/resolved Git aliases, every exact supplied protected root, terminal symlink and regular-file policy, fresh handle-derived link count, canonical/ancestor/file-identity overlap, move/add destination absence, parent viability, complete source bytes/modes, strict ordered unique hunks, EOF/no-final-newline markers, and cancellation; it rejects every duplicate or ancestor/descendant role, alias, cross-role reuse, move chain/cycle/fan-in/self-edge, ambiguous/stale/unanchored hunk, and planning drift without changing bytes, modes, symlinks, or directories. Safe intermediate symlinks resolve to fenced canonical paths; final/dangling symlinks and multiply-linked sources fail closed. Source reads use no-follow, nonblocking where supported platform handles with context-aware chunks, so a swap to a symlink/reparse point/FIFO cannot escape or wedge preflight. A whole-plan workspace/root/lexical-and-canonical-ancestor/leaf snapshot recheck and final cancellation check define the point of no return. The current sequential commit then consumes only planned paths/bytes/summaries and is not interrupted by later cancellation. Existing constructors, permissions, allow-paths, local-repair guard, schema, and authored-order success summaries remain compatible; exact mode-preserving staging, runtime rollback, cleanup, and one bounded candidate diff remain P011. | A later invalid operation, path alias, stale hunk, control path, cancellation, or move collision must not leave a partially edited tree, while this preflight PR must not falsely claim the effect-time transaction guarantees assigned to P011. |

## Data And State Model

Tool state includes visible and hidden registry maps, allowlists, TTL metadata,
optional immutable owner identity, per-entry frozen descriptors, conservative
traits, factories, owner-local service/cache state, tool context, media store
references, removable tool entries, exec background
sessions, filesystem roots, web provider config, redaction caches for sensitive
values, profile-specific tool adaptation overrides in `tools.adaptation.profile_overrides`,
and the runtime-learned tool adaptation state file at
`$PICOCLAW_HOME/tool_adaptation_state.json`. Per-execution context may also
carry an opaque process-local turn UX identity used only to bind same-chat
delivery and cleanup to its originating turn. Arguments decoded from durable
JSON may retain numeric values as `json.Number` through schema validation and
tool execution. Workflow-tool dependency gate snapshots are transient and bind
exact active-draft bytes to sorted reachable definition snapshots and the
effective readiness report; the resulting opaque revision is used only by the
in-process publish request and repeated transaction checks. A `media.Snapshot`
is a transient detached byte-and-metadata value. It has no source path, scope,
cleanup authority, or durable media-store identity, and capturing it does not
change reference counts or TTL state.
Sequential and suppression flags are request-local loop configuration and add no
persistent state. The suppression marker exists only in the execution context;
the optional patch guard is bound to one constructed tool instance and receives
canonical operation paths before any patch mutation begins.
Each owner-local subagent manager retains only its process-local task catalog.
After terminal commit, the callback context carries a detached task ID/status
pair long enough for Agent Conversations to form its composite result identity;
the tool layer persists no result envelope. That separate envelope and its
deduplication/claim state are in-memory only, not crash durable or a guarantee
of exactly-once provider execution.
Compatibility factory entries additionally retain source-pointer leases until
their quiesced registry closes. Selected destinations retain only their exact
callable root set; privately built dependencies share the owner cleanup ledger
and a disjoint private product map, while the complete classified spec catalog
is copied without construction for owner-dependent descendants. Neither enters
the callable tool map or outward capability projections. Owner media updates
apply to public and built private products, never dormant specs.
Immutable-shared source and child lifetimes hold ref-counted shared leases
that exclude every exclusive owner;
an immutable exact-name registration cap remains distinct from the mutable
case-insensitive legacy allowlist. Per-entry visibility revisions fence TTL
changes and ABA transitions without changing the registry definition version.

## Surface Ownership

Owns: CODE pkg/commands/**
Owns: CODE pkg/media/**
Owns: CODE pkg/tools/**
Owns: CODE web/backend/api/tools.go
Owns: CODE web/frontend/src/api/tools.ts
Owns: CODE web/frontend/src/components/agent/tools/**
Owns: CODE web/frontend/src/hooks/use-chat-models.ts
Owns: CODE web/frontend/src/routes/agent/tools.tsx
Owns: CONFIG.tools.allow_read_paths
Owns: CONFIG.tools.allow_write_paths
Owns: CONFIG.tools
Owns: CONFIG.tools.adaptation*
Owns: CONFIG.tools.append_file*
Owns: CONFIG.tools.edit_file*
Owns: CONFIG.tools.exec*
Owns: CONFIG.tools.filter*
Owns: CONFIG.tools.i2c*
Owns: CONFIG.tools.list_dir*
Owns: CONFIG.tools.load_image*
Owns: CONFIG.tools.media_cleanup*
Owns: CONFIG.tools.message*
Owns: CONFIG.tools.read_file*
Owns: CONFIG.tools.send_file*
Owns: CONFIG.tools.send_tts*
Owns: CONFIG.tools.serial*
Owns: CONFIG.tools.spi*
Owns: CONFIG.tools.web*
Owns: CONFIG.tools.write_file*
Owns: HTTP GET /api/tools
Owns: HTTP PUT /api/tools/*
Owns: HTTP GET /api/tools/web-search-config
Owns: HTTP PUT /api/tools/web-search-config
Owns: HTTP GET /api/tools/adaptation
Owns: HTTP PUT /api/tools/adaptation
Owns: HTTP POST /api/tools/adaptation/probe
Owns: TEST pkg/tools/*
Owns: TEST pkg/seahorse/*
Owns: TEST pkg/media/*
Owns: TOOL append_file
Owns: TOOL apply_patch
Owns: TOOL edit_file
Owns: TOOL exec
Owns: TOOL exec_command
Owns: TOOL i2c
Owns: TOOL list_dir
Owns: TOOL load_image
Owns: TOOL message
Owns: TOOL reaction
Owns: TOOL read_file
Owns: TOOL send_file
Owns: TOOL send_tts
Owns: TOOL serial
Owns: TOOL spi
Owns: TOOL update_plan
Owns: TOOL view_image
Owns: TOOL web_fetch
Owns: TOOL web_search
Owns: TOOL write_stdin
Owns: TOOL write_file

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Tools | `read_file`, `write_file`, `edit_file`, `append_file`, `list_dir`, `load_image`, `send_file`, `exec`, `web_search`, `web_fetch`, hardware and delivery tools | Built-in tool schemas and execution behavior. | `FR-TOOL-001` through `FR-TOOL-008` |
| HTTP | `/api/tools`, `/api/tools/{name}/state`, `/api/tools/web-search-config`, `/api/tools/adaptation`, `/api/tools/adaptation/probe` | Effective launcher tool state, strict generation-safe state mutation, web search configuration, and model-aware tool surface policy/probing. | `FR-TOOL-004`, `FR-TOOL-009`, `FR-TOOL-016`, `FR-TOOL-017` |
| Config | `tools.*` subtrees except MCP, skills, and cron ownership in their feature specs | Tool enablement, limits, providers, filtering, and policies. | `FR-TOOL-002` through `FR-TOOL-006` |
| Frontend | Tool library, adaptation, and web-search configuration pages under `web/frontend/src/components/agent/tools/**` | Browser tool management follows shared frontend API, accessibility, formatting, and route smoke-test rules while preserving configured/effective tool enablement, blocked dependency, and adaptation semantics. | `FR-TOOL-001`, `FR-TOOL-004`, `FR-TOOL-009`, `FR-TOOL-016` |
| Tool | `spawn` with asynchronous manager execution and owner-local `SubagentManager` | Retain AgentLoop runtime/source ownership, snapshot direct child inputs, insert one ID-bearing running record before the manager-owned launch, run the direct child with `Async:false`/`Critical:true`, and pass committed task identity once to exact-session delivery before releasing each boundary. | `FR-TOOL-011` |
| Go context | `WithToolTurnUXContext`, `ToolTurnUXID`, `message` delivery callback | Preserve the active turn identity through tool and child execution with cloned inbound context, but copy it to outbound delivery only for the exact originating channel/chat. | `FR-TOOL-012` |
| Tool validation | JSON Schema `number` and `integer` arguments | Validate finite `float64` values and lossless `json.Number` values, including exact integrality without exponent expansion. | `FR-TOOL-013` |
| Go API | `ToolRegistry.AllowsRegistration`, `ToolRegistry.GetRegistered` | Inspect effective allowlist admission and the exact registered occupant, including dormant hidden tools, before MCP registration mutates a registry. | `FR-TOOL-014` |
| Tool | `workflow` action `dev_publish` | Evaluate exact structural and production dependency readiness, derive an opaque dependency revision, and invoke revision-fenced transactional workflow publishing. | `FR-TOOL-015` |
| Go API | `media.SnapshotReader.ReadSnapshot(ctx, ref, maxBytes)` | Optionally return one path-free detached snapshot of a currently live `media://` capability through a bounded, no-follow, regular-file read; fixed redacted errors preserve optional-capability and temporary-lifetime semantics. | `FR-TOOL-018` |
| Go API | `ToolLoopConfig.SequentialToolCalls`, `ToolLoopConfig.SuppressToolArguments`, `NewApplyPatchToolWithPathGuard` | Opt a trusted caller into response-order execution, private tool diagnostics, and whole-patch path preflight without changing ordinary loop or patch defaults. | `FR-TOOL-019` |
| Go API | `ToolOwner`, `ToolTraits`, `ToolDescriptor`, `ToolFactory`, `NewOwnedToolRegistry`, strict factory/immutable-shared registration, `ToolRegistry.InstantiateForOwner`, `ToolRegistry.Close` | Declare conservative trusted metadata, transactionally construct isolated owner registries, and release their resources without changing legacy registration or shallow-clone behavior. | `FR-TOOL-020` |
| Go API | `ToolRegistry.RegisterFactoryBacked`, `RegisterHiddenFactoryBacked`, `InstantiationCapabilities`, `InstantiateForOwnerSelection` | Attach construction metadata to a live compatibility entry and construct only an exact selected owned root set, retaining any resolved dependency privately. | `FR-TOOL-021` |
| Go API | `NewToolFactoryFromPrototype`, `ToolRegistry.RegisterFactoryDependency` | Freeze a live prototype and retain a non-public dependency-only factory for exact private owner resolution and transactional public promotion. | `FR-TOOL-022` |
| Go API | `InstallFactoryBackedTransaction`, `FactoryBackedBatch`, `FactoryBackedInstall`, `FactoryBackedAdmission` | Atomically install or exact-pointer-replace a staged factory-backed catalog across distinct compatibility registries and return detached ordered allowlist decisions. | `FR-TOOL-023` |
| Go API | `NewMCPToolWithFactory` | Freeze one remote MCP definition and runtime binding into a live wrapper plus distinct per-owner products borrowing the same manager and event service. | `FR-TOOL-024` |

## Algorithms And Ordering

1. Build the registry from config, registering only enabled tools and preserving discovery tools where allowed.
   For strict owner construction, freeze admitted metadata, snapshot registry
   policy, recursively instantiate exact dependencies and owner-local services
   outside the source lock, inject media state, validate exact descriptor
   parity, and publish the complete private registry only after every entry
   succeeds. Failure closes newly created resources in reverse order and
   exposes no partial destination.
   Compatibility factory registration validates and leases the supplied live
   root without constructing a replacement. Selected construction validates
   every requested root before calling any factory, builds only roots and
   recursively requested classified dependencies, publishes roots only, and
   retains built dependency products in the private product map. It also copies
   all frozen classified specs without invoking their constructors, preserving
   owner-dependent resolution for nested descendants. It then rechecks source
   map identity, definition version, media generation, and selected per-entry
   visibility revision before committing the private owner service/resource
   ledger. Unselected visibility changes do not abort a selected construction.
2. Convert registry definitions to provider-specific tool schemas.
3. On execution, inject context, validate args, enforce security constraints,
   then call the tool. Numeric validation preserves `json.Number`; integer
   checks compare decimal scale and trailing zeroes with exponent parsing
   saturated to a bound derived from the input length.
4. Recover panics and nil results into normalized tool errors.
5. Apply sensitive-data filtering before returning model-visible content.
6. Resolve `account_ref`, then resolve the exact model alias for the selected
   concrete account, and only then derive the tool-adaptation decision from the
   resulting provider/model profile. Apply a matching canonical profile
   override before global auto-resolution; omitted adaptation-policy fields
   inherit the global adaptation setting. Account routers own no model profile,
   and alias overrides may be keyed only by concrete accounts.
7. When a model profile may resolve to `codex`, register compatibility wrappers (`exec_command`, `write_stdin`, `apply_patch`, `view_image`) over PicoClaw's native backends while preserving the underlying security checks. Retain the internal factory-backed `update_plan` identity for native callers, but omit and deny it for model-authored turns until durable coding-task plan state exists.
8. After LLM responses that report cached input tokens, record a model/API observation keyed by provider, model, visible surface, and stable tool-schema hash. In `auto` cache-sensitivity mode, positive cached-token observations override provider-name heuristics for future runtime downgrade/promotion decisions; cache misses remain visible telemetry but do not prove that mid-session tool-shape changes are safe.
9. After tool execution, record per-tool success/failure counters keyed by provider, model, pinned visible surface, and tool name when adaptation learning is enabled. These counters are persisted and exposed through the adaptation API for future tool-by-tool tuning.
10. When explicitly triggered and `run_model_probes` is enabled, run a bounded no-side-effect LLM call against the requested provider/model profile. `POST /api/tools/adaptation/probe` accepts optional `{provider, model}` JSON; an empty body preserves active-profile behavior. The server resolves only configured concrete upstream model/account credentials, never credentials supplied by the request. The probe validates whether the model emits the expected tool call and records the result as a learned tool outcome without executing the requested probe tool.
11. The adaptation API returns the active resolved provider/model profile plus a deduplicated list of effective provider/model profiles expanded from enabled model-list entries, enabled account routers, enabled model routers, and saved manual overrides. Account routers are expansion sources only: they are not returned as adaptation providers, do not create per-account rows, and do not expose router/account labels in the profile list. Each row reports whether a configured upstream target makes probing available. The Adaptation UI supports local search, per-row probes, and add/edit/remove of overrides for configured provider/model rows; account credentials remain hidden and account-collapsed.
12. For background spawn, ask an AgentLoop-backed spawner to retain runtime
    ownership and a short parent construction-source lease while snapshotting
    the manager, direct runner, wrapper inputs, callback, and trusted origin.
    After preparation, insert one running record and task ID in the paired
    owner-local manager before that manager starts the only goroutine. The
    goroutine waits boundedly for one child-concurrency slot before consuming
    the source or constructing tools. Run the direct child with `Async:false`
    and `Critical:true`, so the manager owns asynchronous execution without
    also writing the legacy direct-SubTurn result channel. Release the source
    after exact child construction/attachment and the slot after `runTurn`;
    commit manager status before attaching its task ID/status to the sole
    callback, which offers one composite-ID exact-session result envelope.
    Never use the generic raw-user/synthetic-system callback path for tracked
    spawn. Release runtime ownership after that handoff attempt. Every failure
    path releases each acquired boundary exactly once; later envelope
    continuation is Agent Conversations' fresh-generation responsibility.
13. Inject the active transient-UX identity into every tool execution. A child
    turn inheriting that cloned inbound context retains the identity; a `message`
    send copies it only when its destination matches the originating
    channel/chat, and stream lookup prefers the turn-scoped capability before
    falling back to the legacy interface.
14. Before adding MCP wrappers, inspect every allowed canonical name and its
    current registered occupant, including hidden expired entries. Reject the
    complete MCP registration set on any non-MCP or different-identity
    collision before exposing its first tool; an exact identity may replace a
    stale wrapper with the currently configured eager or deferred visibility.
15. For workflow `dev_publish`, load, parse, and validate the exact persisted
    active draft; snapshot the complete reusable closure within fixed budgets;
    resolve live dependencies; hash length-delimited draft, sorted dependency,
    and effective readiness values; then pass that revision with all active
    development fences to the workflow publisher for repeated gate checks and
    transactional commit.
16. For generic tool-state mutation, strictly decode one bounded request,
    serialize through the handler mutation boundary, load one update-safe
    public-plus-security config generation, apply the selected tool's
    allowlisted state transition, and compare-and-save that same generation
    under the advisory lock. The workflow transition changes only its raw tool
    flag; its status is then resolved with workflow master enablement without
    mutating that prerequisite.
17. For an optional file-media snapshot, validate the context, positive byte
    limit, and exact capability form; retain the store read lock through lookup,
    platform-safe no-follow open, handle validation, status-change-token
    capture, bounded read, post-read token comparison, and close; then copy
    metadata and bytes into a detached result. Normalize registrations to a
    cleaned absolute exact lexical lifecycle key and bind each live key to its
    first entry identity. Keep a verified same-file identity under a distinct
    key separate and make both lifecycles non-deleting. Coordinate exact-key and
    identity-matched registration, pending-deletion tokens, same-entry
    revalidation, and final managed-path deletion under the store lock.
    Classify every snapshot failure before returning a fixed redacted error, and
    never resolve to a path for the caller.
18. When a caller selects the confined loop profile, preserve model response
    order and execute each tool call only after the previous result completes.
    Carry a private suppression marker through registry and nested filesystem
    helpers so logs retain only tool identity and coarse outcome. For a guarded
    patch, parse all operations and validate every source and move destination
    before applying operation one; return the first rejection without partial
    patch execution.
19. For a managed process action, extract the exact agent/session owner from
    trusted tool context. Reserve one opaque owner-bound ID before process
    start, fully initialize and atomically promote the record, then authorize
    every list/lookup/read/write/key/kill/removal against that same owner. Treat
    foreign IDs exactly like missing IDs and use an exact pointer fence before
    deletion.
20. For `apply_patch`, parse exact text, acquire the canonical-workspace gate,
    resolve and authorize every source/destination, snapshot all roots,
    ancestors, leaves, bytes, modes, and identities, reject graph/hunk conflicts,
    derive complete postimages, then recheck the whole plan and cancellation.
    Cross the mutation boundary only after that succeeds; commit consumes only
    the immutable plan and does not reinterpret patch or filesystem semantics.

## Cross-Feature Behavior

Agent conversations execute tools. MCP and skills add tool-like behavior through
separate features. Hooks can modify, deny, or short-circuit tool calls. Security
policies control credentials, HTTP guards, and isolation. Threads provide a
thread-specific tool and policy surface while relying on the generic registry,
schema export, execution, and settings UI mechanics defined here.
Agent Conversations also owns the exact recursion/install-skill enablement,
agent policy snapshots, workspace-lock coordination, and startup/reload failure
handling; this feature supplies only the generic factory, owner-construction,
compatibility lease, and all-agent transaction contracts used by that catalog.
Workflows add an agent-callable management tool and execute step-level tools
through this same registry, including context injection, sensitive-data
filtering, response-handled media delivery, and channel delivery tools. Its
agent-callable publish action additionally reuses the production dependency
gate and fenced workflow transaction instead of trusting model-visible state.
Git workspaces contribute a built-in agent tool registered through this generic
registry, while acquire, release, cleanup, drop, inventory, and the optional
fresh-checkout semantics are owned by the git workspaces feature. The generic
registry only carries the `fresh` boolean from the validated tool call to that
feature; it does not refresh or select a Git ref itself.
Channel delivery owns typing, reaction, placeholder, and stream-marker storage;
tool execution supplies only the opaque turn identity needed for exact
same-chat consumption.
Session memory may consume the optional media snapshot capability to build a
self-contained frozen set. That set's encoding, locator rewriting, limits, and
restart behavior are owned by the session feature; this media-store capability
only captures a reference that is live at the instant of the call.
The unified PR-workspace implementation service composes the optional
sequential, suppression, and guarded-patch primitives for controller-only
local repair. It owns checkout confinement and
lifecycle policy; these generic primitives do not grant workspace, Git,
provider, commit, push, CI, or merge authority.

## Failure And Edge Cases

- Missing required tool args return tool errors.
- Panics inside a tool are recovered by the registry.
- Nil tool results are normalized.
- Sequential mode stops before the next call when its context is canceled and
  never schedules sibling calls concurrently. Suppression also covers invalid
  arguments, panics, nested filesystem debug logs, and result-derived errors;
  callers receive the normal bounded tool result while logs retain no raw
  arguments or result content.
- A guarded patch rejects an invalid source or move destination before applying
  its first operation. Guard failure does not fall back to unguarded patching.
- Denied commands and path violations never execute the requested side effect.
- Web providers fail over only according to configured provider behavior.
- Spawn admission failure returns synchronously and launches no goroutine;
  successful admission remains visible to provider reload even after the
  parent turn returns. Missing committed callback identity or invalid
  exact-session routing fails closed instead of falling back to generic async
  output, synthetic system ingress, or the default session. Duplicate callback
  replay cannot create another tracked envelope. Once an envelope is claimed,
  a provider or publication failure is not retried; process exit can lose an
  unclaimed envelope because the contract is at-most-once and in-memory, not
  crash durable or exactly-once execution.
- Cross-chat `message` sends omit the originating turn UX identity, and legacy
  channel-manager or stream implementations retain their pre-scoped behavior.
- Invalid, fractional integer, and non-finite numeric arguments fail before
  tool execution. Extremely large positive or negative decimal exponents are
  classified without constructing exponent-sized integers or powers of ten.
- A canonical MCP name collision aborts initialization before any tool from
  the candidate MCP registration set is exposed and preserves the incumbent.
- Workflow `dev_publish` fails without an injected live resolver, when
  workflows or any dependency are not ready, or when reachable definition
  content changes between dependency evaluation and fenced publication.
- Invalid, oversized, or stale tool-state writes do not mutate configuration;
  a configured workflow tool whose workflow prerequisite is disabled remains
  explicitly blocked rather than appearing disabled or enabled.
- A media snapshot rejects a blank or noncanonical reference, a nonpositive
  limit, an unknown or concurrently expired entry, a symlink, directory, FIFO,
  socket, device, changing-to-unsafe entry, and any stream exceeding the caller
  limit. It returns no partial bytes.
- `ReleaseAll` and TTL cleanup cannot remove a registered entry between
  snapshot lookup and close. They may proceed after capture returns, while the
  detached result remains valid. Registrations retain a cleaned absolute path,
  so dot aliases and later working-directory changes cannot split or retarget
  one exact lexical lifecycle. A verified same-file identity under a different
  spelling or hard-link path disables automatic deletion for all live keys
  rather than guessing whether they are aliases. Re-registering either the same
  key or identity cancels an older pending final-ref deletion, including when
  the new registration is forget-only and is itself released before that older
  cleanup resumes. A live key whose entry identity changed rejects coalescing;
  a replacement detected before pending removal is preserved. Capture after
  release, expiry, store reconstruction, or restart fails closed rather than
  searching the filesystem or guessing a stale path.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-TOOL-001`, `FR-TOOL-007` | [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/providers/tool_schema_transform_test.go](../../pkg/providers/tool_schema_transform_test.go) |
| `FR-TOOL-002` | [pkg/tools/fs](../../pkg/tools/fs), [pkg/tools/fs/filesystem_test.go](../../pkg/tools/fs/filesystem_test.go), [pkg/tools/fs/edit_test.go](../../pkg/tools/fs/edit_test.go) |
| `FR-TOOL-003`, `FR-TOOL-005` | [pkg/tools/shell_test.go](../../pkg/tools/shell_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-TOOL-004` | [pkg/tools/integration/web_test.go](../../pkg/tools/integration/web_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go) |
| `FR-TOOL-006` | [pkg/tools/result_test.go](../../pkg/tools/result_test.go), [pkg/tools/integration](../../pkg/tools/integration), [pkg/tools/hardware](../../pkg/tools/hardware) |
| `FR-TOOL-008` | [web/frontend/src/hooks/use-chat-models.test.ts](../../web/frontend/src/hooks/use-chat-models.test.ts) |
| `FR-TOOL-009` | [pkg/tools/adaptation.go](../../pkg/tools/adaptation.go), [pkg/tools/adaptation_state.go](../../pkg/tools/adaptation_state.go), [pkg/tools/adaptation_probe.go](../../pkg/tools/adaptation_probe.go), [pkg/tools/codex_compat.go](../../pkg/tools/codex_compat.go), [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go), [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go), [web/backend/api/tools.go](../../web/backend/api/tools.go), [web/frontend/src/components/agent/tools/tool-adaptation-tab.tsx](../../web/frontend/src/components/agent/tools/tool-adaptation-tab.tsx) |
| `FR-TOOL-010` | [web/frontend/src/routes/agent/tools.tsx](../../web/frontend/src/routes/agent/tools.tsx), [web/frontend/src/components/agent/tools/tools-page.tsx](../../web/frontend/src/components/agent/tools/tools-page.tsx), [web/frontend/src/components/agent/tools/use-tools-page.ts](../../web/frontend/src/components/agent/tools/use-tools-page.ts) |
| `FR-TOOL-011` | [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/agent/subturn_effective_tools_test.go](../../pkg/agent/subturn_effective_tools_test.go), [pkg/agent/subagent_result_pipeline_test.go](../../pkg/agent/subagent_result_pipeline_test.go), [pkg/agent/subagent_result_runtime_test.go](../../pkg/agent/subagent_result_runtime_test.go), [pkg/agent/pipeline_execute.go](../../pkg/agent/pipeline_execute.go), [pkg/tools/spawn.go](../../pkg/tools/spawn.go), [pkg/tools/spawn_test.go](../../pkg/tools/spawn_test.go), [pkg/tools/subagent.go](../../pkg/tools/subagent.go), [pkg/tools/subagent_manager_test.go](../../pkg/tools/subagent_manager_test.go), [pkg/tools/spawn_status.go](../../pkg/tools/spawn_status.go), [pkg/tools/spawn_status_test.go](../../pkg/tools/spawn_status_test.go) |
| `FR-TOOL-012` | [pkg/tools/integration/message_test.go](../../pkg/tools/integration/message_test.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/agent/agent_turn_ux_test.go](../../pkg/agent/agent_turn_ux_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go) |
| `FR-TOOL-013` | [pkg/tools/validate.go](../../pkg/tools/validate.go), [pkg/tools/validate_test.go](../../pkg/tools/validate_test.go), [pkg/workflows/store_test.go](../../pkg/workflows/store_test.go) |
| `FR-TOOL-014` | [pkg/tools/registry.go](../../pkg/tools/registry.go), [pkg/agent/agent_mcp_test.go](../../pkg/agent/agent_mcp_test.go) |
| `FR-TOOL-015` | [pkg/tools/workflow_publish.go](../../pkg/tools/workflow_publish.go), [pkg/tools/workflow_publish_test.go](../../pkg/tools/workflow_publish_test.go), [pkg/workflows/development_publish_test.go](../../pkg/workflows/development_publish_test.go) |
| `FR-TOOL-016` | [web/backend/api/tools.go](../../web/backend/api/tools.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go), [web/backend/api/workflow_settings_test.go](../../web/backend/api/workflow_settings_test.go), [web/frontend/src/api/tools.test.ts](../../web/frontend/src/api/tools.test.ts), [web/frontend/src/components/agent/tools/tool-library-tab.test.tsx](../../web/frontend/src/components/agent/tools/tool-library-tab.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-TOOL-017` | [pkg/config/model_selection_test.go](../../pkg/config/model_selection_test.go), [pkg/tools/integration/web_test.go](../../pkg/tools/integration/web_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go) |
| `FR-TOOL-018` | [pkg/media/snapshot_test.go](../../pkg/media/snapshot_test.go), [pkg/media/store_test.go](../../pkg/media/store_test.go), [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go), [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go), [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go) |
| `FR-TOOL-019` | [pkg/tools/toolloop_test.go](../../pkg/tools/toolloop_test.go), [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go), [pkg/tools/apply_patch_test.go](../../pkg/tools/apply_patch_test.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go) |
| `FR-TOOL-020` | [pkg/tools/traits.go](../../pkg/tools/traits.go), [pkg/tools/factory.go](../../pkg/tools/factory.go), [pkg/tools/registry_factory.go](../../pkg/tools/registry_factory.go), [pkg/tools/registry_factory_test.go](../../pkg/tools/registry_factory_test.go), [pkg/tools/search_tool.go](../../pkg/tools/search_tool.go), [pkg/seahorse/tool_factory_test.go](../../pkg/seahorse/tool_factory_test.go) |
| `FR-TOOL-021` | [pkg/tools/registry_selection.go](../../pkg/tools/registry_selection.go), [pkg/tools/registry_selection_test.go](../../pkg/tools/registry_selection_test.go), [pkg/tools/registry_selection_coverage_test.go](../../pkg/tools/registry_selection_coverage_test.go), [pkg/tools/factory_coverage_test.go](../../pkg/tools/factory_coverage_test.go), [pkg/tools/registry.go](../../pkg/tools/registry.go), [pkg/seahorse/tool_factory_test.go](../../pkg/seahorse/tool_factory_test.go) |
| `FR-TOOL-022` | [pkg/tools/registry_dependency.go](../../pkg/tools/registry_dependency.go), [pkg/tools/registry_dependency_test.go](../../pkg/tools/registry_dependency_test.go), [pkg/tools/codex_compat.go](../../pkg/tools/codex_compat.go), [pkg/tools/codex_compat_test.go](../../pkg/tools/codex_compat_test.go), [pkg/agent/tool_factory_catalog.go](../../pkg/agent/tool_factory_catalog.go), [pkg/agent/tool_factory_catalog_test.go](../../pkg/agent/tool_factory_catalog_test.go), [pkg/agent/recursion_tool_factory_catalog.go](../../pkg/agent/recursion_tool_factory_catalog.go), [pkg/agent/recursion_tool_factory_catalog_test.go](../../pkg/agent/recursion_tool_factory_catalog_test.go), [pkg/agent/subturn_effective_tools.go](../../pkg/agent/subturn_effective_tools.go), [pkg/agent/subturn_effective_tools_test.go](../../pkg/agent/subturn_effective_tools_test.go), [pkg/agent/instance.go](../../pkg/agent/instance.go), [pkg/agent/agent_init.go](../../pkg/agent/agent_init.go) |
| `FR-TOOL-023` | [pkg/tools/registry_transaction.go](../../pkg/tools/registry_transaction.go), [pkg/tools/registry_transaction_test.go](../../pkg/tools/registry_transaction_test.go), [pkg/tools/registry_selection.go](../../pkg/tools/registry_selection.go), [pkg/tools/registry_dependency.go](../../pkg/tools/registry_dependency.go), [pkg/tools/registry.go](../../pkg/tools/registry.go), [pkg/agent/context_seahorse_catalog_test.go](../../pkg/agent/context_seahorse_catalog_test.go), [pkg/agent/recursion_tool_factory_catalog.go](../../pkg/agent/recursion_tool_factory_catalog.go), [pkg/agent/recursion_tool_factory_catalog_test.go](../../pkg/agent/recursion_tool_factory_catalog_test.go) |
| `FR-TOOL-024` | [pkg/tools/mcp_factory.go](../../pkg/tools/mcp_factory.go), [pkg/tools/mcp_factory_test.go](../../pkg/tools/mcp_factory_test.go), [pkg/tools/integration/mcp_tool.go](../../pkg/tools/integration/mcp_tool.go), [pkg/tools/integration/mcp_tool_test.go](../../pkg/tools/integration/mcp_tool_test.go), [pkg/tools/registry_factory.go](../../pkg/tools/registry_factory.go) |
| `FR-TOOL-025` | [pkg/tools/codex_compat.go](../../pkg/tools/codex_compat.go), [pkg/tools/codex_compat_test.go](../../pkg/tools/codex_compat_test.go), [pkg/tools/adaptation_probe.go](../../pkg/tools/adaptation_probe.go), [pkg/tools/adaptation_probe_test.go](../../pkg/tools/adaptation_probe_test.go), [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go), [pkg/agent/pipeline_execute.go](../../pkg/agent/pipeline_execute.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/agent/pipeline_llm_adaptation_test.go](../../pkg/agent/pipeline_llm_adaptation_test.go), [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go) |
| `FR-TOOL-026` | [pkg/tools/session.go](../../pkg/tools/session.go), [pkg/tools/session_test.go](../../pkg/tools/session_test.go), [pkg/tools/session_owner_test.go](../../pkg/tools/session_owner_test.go), [pkg/tools/shell.go](../../pkg/tools/shell.go), [pkg/tools/shell_test.go](../../pkg/tools/shell_test.go), [pkg/tools/process_session_ownership_test.go](../../pkg/tools/process_session_ownership_test.go), [pkg/tools/sysproc_unix.go](../../pkg/tools/sysproc_unix.go), [pkg/tools/sysproc_windows.go](../../pkg/tools/sysproc_windows.go), [pkg/tools/codex_compat_test.go](../../pkg/tools/codex_compat_test.go) |
| `FR-TOOL-027` | [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go), [pkg/tools/apply_patch_preflight.go](../../pkg/tools/apply_patch_preflight.go), [pkg/tools/apply_patch_preflight_test.go](../../pkg/tools/apply_patch_preflight_test.go), [pkg/tools/apply_patch_preflight_unix_test.go](../../pkg/tools/apply_patch_preflight_unix_test.go), [pkg/tools/apply_patch_preflight_windows_test.go](../../pkg/tools/apply_patch_preflight_windows_test.go), [pkg/tools/apply_patch_preflight_darwin_test.go](../../pkg/tools/apply_patch_preflight_darwin_test.go), [pkg/tools/apply_patch_links_unix.go](../../pkg/tools/apply_patch_links_unix.go), [pkg/tools/apply_patch_links_windows.go](../../pkg/tools/apply_patch_links_windows.go), [pkg/tools/apply_patch_source_unix.go](../../pkg/tools/apply_patch_source_unix.go), [pkg/tools/apply_patch_source_windows.go](../../pkg/tools/apply_patch_source_windows.go), [pkg/tools/apply_patch_path_key.go](../../pkg/tools/apply_patch_path_key.go), [pkg/tools/apply_patch_path_key_darwin.go](../../pkg/tools/apply_patch_path_key_darwin.go), [pkg/tools/apply_patch_path_key_windows.go](../../pkg/tools/apply_patch_path_key_windows.go), [pkg/tools/apply_patch_path_policy_windows.go](../../pkg/tools/apply_patch_path_policy_windows.go), [pkg/agent/apply_patch_policy.go](../../pkg/agent/apply_patch_policy.go), [pkg/agent/apply_patch_policy_test.go](../../pkg/agent/apply_patch_policy_test.go), [pkg/agent/instance.go](../../pkg/agent/instance.go) |

## Implementation Anchors

- [pkg/tools/registry.go](../../pkg/tools/registry.go)
- [pkg/tools/traits.go](../../pkg/tools/traits.go)
- [pkg/tools/factory.go](../../pkg/tools/factory.go)
- [pkg/tools/registry_selection.go](../../pkg/tools/registry_selection.go)
- [pkg/tools/registry_dependency.go](../../pkg/tools/registry_dependency.go)
- [pkg/tools/registry_transaction.go](../../pkg/tools/registry_transaction.go)
- [pkg/tools/mcp_factory.go](../../pkg/tools/mcp_factory.go)
- [pkg/seahorse/tool_factory.go](../../pkg/seahorse/tool_factory.go)
- [pkg/tools/fs](../../pkg/tools/fs)
- [pkg/tools/integration/web.go](../../pkg/tools/integration/web.go)
- [pkg/tools/shared/base.go](../../pkg/tools/shared/base.go)
- [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go)
- [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go)
- [pkg/tools/validate.go](../../pkg/tools/validate.go)
- [pkg/tools/integration/message.go](../../pkg/tools/integration/message.go)
- [pkg/tools/spawn.go](../../pkg/tools/spawn.go)
- [pkg/tools/workflow_publish.go](../../pkg/tools/workflow_publish.go)
- [pkg/media/store.go](../../pkg/media/store.go)
- [pkg/media/snapshot.go](../../pkg/media/snapshot.go)
- [pkg/media/snapshot_file.go](../../pkg/media/snapshot_file.go)
- [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go)
- [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go)
- [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go)
