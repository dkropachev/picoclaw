# Agent Conversations And Turn Execution

## Feature ID

`FR-AGENT`

## Behavior Summary

PicoClaw accepts a user turn, builds prompt context, selects provider
candidates, calls an LLM, executes requested tools, streams or finalizes
responses, and records turn state. Provider, model, CLI, and config surfaces are
auxiliary to this capability. The loop also exposes an isolated provider-call
profile for workflow decisions that need a frozen existing conversation as
evidence without registering a turn, executing tools, invoking hooks, or
writing the conversation, plus a stateless profile that evaluates only supplied
workflow context under a request-local identity that never becomes a session or
account-router affinity. Compiler-private decisions capture the exact existing
conversation before a durable workflow run is created, freeze every structured
media locator into a strict self-contained `FrozenSet`, and later evaluate only
that persisted evidence under a domain-separated pseudonymous
cache/account-affinity identity, never the raw session or live-media
capability. A controller-only local-repair profile can also run one fresh,
bounded model/tool loop inside an already pinned checkout without inheriting
an interactive agent, session, workflow, prompt, or ambient tool surface. For
durable PR-repair orchestration, the agent layer additionally exposes only an
opaque identity of that exact fixed prompt and the concrete production
workspace manager already installed in the loop; neither bridge is
model-facing or makes trusted controller lifecycle authority public. A
separate controller-only local-review profile resolves the same immutable
session agent under the caller's runtime-generation lease and evaluates only a
bounded detached parked-candidate context. It uses one fresh no-tools,
no-history, no-cache, no-affinity private request and returns only a strict
structured outcome and findings, with no repository or lifecycle capability.
Development workspaces also expose a durable Ask/Steer conversation that is
separate from ordinary interactive sessions. Ask answers from bounded frozen
workspace evidence without edit authority. Steer classifies scope first and
queues only charter-compatible instructions for the next implementation repair
boundary; it never interrupts or edits inside the message request.
Shared-tool construction additionally stages one all-agent factory catalog for
recursion and skill installation. SubTurns now meet parent, target, explicit,
profile, and eligibility authority before constructing fresh turn-owned
wrappers; root execution remains compatibility-backed. Each admitted async
spawn is recorded by the exact owner-local recursion manager before launch and
returns its manager-local task ID for status inspection. Its committed terminal
result enters one process-local, composite-ID result envelope for at-most-once
delivery to the exact named parent session; it is never duplicated through the
legacy pending-result channel, direct callback output, or default-session
system ingress.
Quiesced shutdown and reload also close each retired agent's tool registry and
session store before generation-owned dependencies disappear.

## Reconstruction Notes

- Similarity target: recreate an agent loop that builds prompt context, selects provider candidates, executes tool calls, and stores a final turn.
- Core types/functions: `AgentLoop`, agent instance creation, context builder,
  pipeline setup/execute/finalize helpers, turn reservations and scoped steering,
  message-scoped channel capabilities, isolated side-question execution,
  frozen and stateless side-question profiles, provider factory, and tool
  registry, plus the recursion factory catalog, owner-local recursion service,
  generation-local install-workspace lock coordinator, and the effective
  SubTurn capability selector/strict-resource supervisor, tracked-spawn result
  envelope mailbox, and exact-session continuation coordinator. Controller-only
  integration points are
  `ControllerLocalRepairPromptDigest` and
  `AgentLoop.ControllerGitWorkspaceManager`, plus
  `ControllerLocalReviewPromptDigest`, `AgentLoop.ControllerLocalReviewReady`,
  and `AgentLoop.NewControllerLocalReviewRunner`.
- Runtime ordering: normalize input, resolve route/session, build prompt, select model candidate, call provider, execute tool calls, stream/finalize response, persist history, emit runtime events.
- Non-obvious constraints: tool iteration limits, media limits, turn profile block
  disabling, fallback candidates, child-turn concurrency, exact transient-UX
  ownership, and source-compatible channel/streaming fallbacks must stay
  explicit. Recursion enablement and manager/status pairing must remain exact,
  while every nested child repeats the immediate-parent authority meet and
  cannot regain removed tools. Tracked spawn completion is manager-asynchronous
  even though its direct SubTurn uses `Async:false`; its one in-memory delivery
  envelope is keyed by the source parent turn plus manager-local task ID,
  consumed at most once, and never falls back to the default agent/session.
  This is neither crash durability nor exactly-once provider execution, and it
  does not change generic async tool callbacks or direct `Async:true` SubTurns.
  A child-authorized `message` call remains a separate explicit tool side
  effect under Tool Execution; it is not another manager-completion producer.
  Request-forced skills and caller-owned usage/result sinks are active-turn-only
  caps, so they also forbid constructing a detached late result continuation.
  An isolated workflow
  decision uses caller-supplied frozen context
  rather than assembling live history again, exposes no tools, and rejects a
  provider response that still attempts a tool call. A compiler-private
  decision additionally accepts only pre-captured owner/revision-matched
  evidence whose complete media graph was frozen at capture, a blank
  live-session key, and no inbound delivery or session scope. It validates the
  frozen revision before materializing embedded bytes without a live-session or
  media-store read, and exposes fixed private markers while preserving stable
  pseudonymous cache and account selection for that exact snapshot. A stateless workflow
  decision additionally assembles no session context, disables prompt cache and
  session-affine account routing, and never exposes its request-local identity
  outside the call. Local repair instead receives one concrete provider/model
  and one exact workspace pin from its trusted controller, exposes only four
  confined file tools, serializes provider use, and leaves the pin held for the
  controller's later verification and disposition. Its exact fixed system
  prompt has a deterministic domain-separated digest for pre-call durable
  evidence without exposing the prompt text. Access to repository lifecycle is
  not generalized: under its own runtime-generation lease the trusted
  controller may obtain only the concrete production workspace manager already
  installed in the loop, while nil, typed-nil, and alternate interface
  implementations fail closed. Local review instead receives only caller-built
  bounded immutable context, uses a fresh provider instance with detached
  messages, and rejects every tool call or malformed structured outcome.
  Development Ask/Steer messages additionally fence the exact conversation
  revision and, when present, the latest browsable candidate revision; scope
  changes become clarification state rather than queued edit instructions.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-AGENT-001` | MUST | A turn starts from normalized input and creates a scoped runtime context containing agent, session, channel, chat, sender, turn ID, and media metadata when available. | Downstream tools, events, and persistence need stable context. |
| `FR-AGENT-002` | MUST | Prompt construction includes configured identity, workspace instructions, memory, session history, skills, tool-use guidance, and tool definitions unless the turn profile disables a block. A separately admitted isolated turn may set the internal `SuppressDefaultContext` process option only through its owning execution profile. Prompt construction then retains the explicit system overlay while suppressing the configured/default system prompt and its bootstrap, workspace, identity, memory, skill, contributor, current-time, and dynamic-runtime blocks; it also suppresses the tool-use rule without enabling the tool fallback. Combined with that profile's no-history, no-tools, and no-cache controls, the provider sees only the exact explicit system overlay and supplied user content. | Ordinary turns depend on composable prompt contributors, while a narrowly authorized isolated model call must not inherit ambient agent/workspace instructions merely because it reuses the same prompt builder. |
| `FR-AGENT-003` | MUST | Every execution selection consists of an `account_ref` and an exact model alias. Per-agent empty fields inherit the corresponding agent defaults; primary may name an enabled model router, while fallbacks name exact aliases only. Account routers first select a concrete account, model routers select an alias, and the alias then resolves to its default concrete model or a per-concrete-account override. Alias configuration may explicitly disable an alias for a concrete account: direct use fails clearly, account-router candidate construction excludes that pair, and a fallback disabled for one account does not prevent its other aliases from running. Override and disabled-account keys may never name account routers. Empty aliases fail before provider/network setup with `no model configured`; unknown aliases, raw model IDs, fuzzy model-list matches, and provider defaults are never substituted. Retries and fallbacks preserve the selected concrete account identity. Explicit provider safety/refusal errors and finish reasons may use the bounded retry/fallback path, but never become a successful response. Credential-backed provider attempts resolve the selected exact credential again at request time and use serialized compare-and-swap refresh, so an in-place launcher renewal becomes effective for an already-running provider and a stale refresh cannot replace it. | Multi-provider behavior must be explicit and reproducible, with account choice independent from model choice. |
| `FR-AGENT-004` | MUST | LLM responses with tool calls execute registered tools until the configured maximum tool iterations is reached or no tool calls remain. | Prevents unbounded loops while preserving agent tool use. |
| `FR-AGENT-005` | MUST | Tool execution errors are returned to the model or user in normalized error text without panicking the turn loop. | Tool failures are normal runtime outcomes. |
| `FR-AGENT-006` | MUST | Streaming output emits deltas when supported and still produces a final assistant message for session storage. | Streaming and durable history must stay consistent. |
| `FR-AGENT-007` | MUST | Subturn and spawn tools run child work with bounded depth, concurrency, timeout, and token budget. | Background work must not exhaust the parent turn. |
| `FR-AGENT-008` | SHOULD | Thinking or reasoning content is preserved for surfaces that display it and omitted from ordinary final replies unless configured. | Reasoning display is auxiliary, not the answer itself. |
| `FR-AGENT-009` | MUST | The root CLI composes independently owned feature command trees without changing their behavior. Direct-agent commands use the same agent runtime path as gateway turns, with command-specific input/output wrapping only. | Adding an operator command must not fork agent behavior or entangle unrelated command implementations. |
| `FR-AGENT-010` | MUST | Per-model OpenAI-style `reasoning_effort` is normalized before provider calls; blank/default values are omitted, `off` maps to `none`, and unsupported values are rejected by config validation. | Provider requests must not forward invalid reasoning controls. |
| `FR-AGENT-011` | MUST | Provider prompt serialization preserves ordered text/media parts, scoped context, tool call/result identifiers, and token estimates through the provider-neutral prompt representation before mapping to provider-specific wire formats. | Multi-provider turns need one canonical prompt model so media, summaries, cache hints, and tool relationships are not silently lost or double-counted. |
| `FR-AGENT-012` | MUST | Each primary or fallback provider attempt derives tool adaptation from the concrete provider/model profile after router resolution, applies that profile's visible surface to a candidate-specific base schema, and retains the successful candidate's surface and schema for tool execution and observations; explicit profile overrides still obey configured runtime visible-change policy. | Routed and fallback turns must not probe or expose tools using a virtual router identity or another candidate's schema. |
| `FR-AGENT-013` | MUST | Provider/config reload pauses admission of new root runtime users, drains the current registry generation before replacement, and never closes a retained provider while a turn, workflow, summarizer, child turn, or gateway-owned background action can still use it. One inbound lease covers workflow-trigger matching, route/session placeholder selection, and a synchronously retained queued worker so semaphore backlog cannot split an ingress decision across generations. Other asynchronous work retains its generation before goroutine launch; independently cancelable subturn contexts preserve that lease marker; stale captured agent pointers are resolved by ID against the current registry before a root turn starts; and independently launched summarizers/background consumers require the exact config and registry generation that created them. The first reload pause synchronously removes the generation-owned runtime-event workflow subscription and the final nested resume recreates it for only the committed/restored config before opening admission. Terminal Stop is remembered even before `Run` registers; shutdown first quiesces producers, then cancels/joins AgentLoop-owned automation and permanently pauses/drains runtime users, then closes channel/media dependencies and the provider. | Reload, rollback, and shutdown must not create use-after-close provider calls, admit provisional runtime state/events, execute stale cross-workspace work, leak queued session placeholders, or deadlock nested work behind their own drain. |
| `FR-AGENT-014` | MUST | When a credential-backed Codex OAuth request fails with the structured `usage_limit_reached` error, the provider rechecks the authoritative main Codex rate-limit state and automatically consumes one earned reset only when that main limit is eligible and exhausted; reset attempts are serialized, reuse an idempotency key across redemption retries, and reconcile through a post-consume usage read. A confirmed redemption or concurrent reset retries the original provider request at most once, while a redemption that is not verified to recover the same exhaustion episode suppresses further automatic consumption for that episode. Generic `429` responses, workspace credit or spend-control limits, additional-model-only limits, unsupported reset state, and accounts with no resets must not consume one. | Automatic recovery must restore an exhausted eligible account without double-spending finite resets or masking other quota and billing failures. |
| `FR-AGENT-015` | MUST | An admitted inbound message's opaque turn-UX identity remains attached to its session reservation, active or rescued continuation, same-chat tool/stream/final/error output, and exact cleanup decision. A successfully buffered output stops only that identity's typing registration and leaves its reaction/placeholder for delivery-time cleanup; a no-output, rejected, canceled, failed, or panicked turn removes only its exact artifacts. Same-chat steering atomically queues and rebinds its identity to the pinned active owner after slow message preparation rechecks ownership; cross-chat steering, enqueue failure, and abandoned ownership clean the secondary exact identity, while committed steering is rescued or transferred instead of being stranded. Channel-side typing/reaction callbacks remain pinned to the provider generation that created them, so late older callbacks cannot clear a newer turn. | Concurrent turns and steering must not strand transient UX, erase a newer provider generation, or lose a committed user message. |
| `FR-AGENT-016` | MUST | The original `ChannelManager` and four-argument `MessageBus.GetStreamer` contracts remain sufficient for agent integrations. Exact typing stop, cleanup, rebind, placeholder, and turn-scoped streaming are additive optional capabilities: legacy managers fall back to chat-scoped typing stop and placeholder calls plus one detached bounded tool-feedback cleanup, rebind is a no-op, and legacy buses use `GetStreamer`; capable implementations receive the exact turn identity instead. | Existing channel and message-bus implementations must remain source-compatible while built-in channels gain exact ownership. |
| `FR-AGENT-017` | MUST | Agent-owned inbound snapshots preserve process-local turn/event identity, deduplication, occurrence time, subject, conversation, safe attachment descriptors, and transport trust facts through primary turns, queued continuations, and derived outbound contexts. Mutable maps, occurrence-time pointers, and attachment slices are detached when copied, and these fields remain excluded from serialized routing context. | Asynchronous turn and delivery work must retain admission facts without aliasing caller-owned state or expanding the serialized contract. |
| `FR-AGENT-018` | MUST | The authenticated Agent management API and responsive Agent UI project an implicit `main` policy without writing an empty config and support ordered create, inspect, edit, default-selection, and delete operations against an explicit opaque config revision. Each resource exposes an optional `account_ref`; model primary/fallback values are validated as exact aliases, except that primary may be an enabled model router. Empty per-agent values inherit defaults, and the surface preserves that inheritance versus an explicit empty fallback list. | Operators need concurrency-safe browser management of the same strict account-plus-alias policy used at runtime. |
| `FR-AGENT-019` | MUST | The selected-agent UI exposes deep-linkable Overview, Capabilities, and Activity sections on the dedicated `/agent/agents/{id}`, `/{id}/capabilities`, and `/{id}/activity` routes without embedding them in the collection. Capabilities use a separate composite config-plus-workspace revision and preserve the exact tools all/none/selected, skills inherit/none/selected, and MCP all/none/selected states; an edit changes only requested frontmatter nodes while preserving unrelated YAML nodes, comments, ordering, and prompt body, retains unknown existing selections, and upgrades legacy `AGENTS.md` only after explicit confirmation without deleting it. Malformed, unterminated, unsafe, or unsupported-platform definition state is fail-closed and read-only. Capability and activity views use only bounded sanitized projections, retain dirty drafts across conflicts until explicit reload, report required gateway restart, and never expose a raw-file editor or persist activity cursors. | Operators need full browser control and visibility for one agent without collapsing workspace policy into global config, overwriting concurrent prompt edits, leaking runtime payloads, or discarding forward-compatible declarations. |
| `FR-AGENT-020` | MUST | An exact read-only workflow decision invokes the selected configured agent through the isolated side-question provider path with one deep-cloned history/summary/scope snapshot. The ordinary profile captures a strict owner-matched existing session when the step begins. The compiler-private profile exposes `ReadOnlySessionCapturer`, which resolves the exact configured agent and strict existing session once under a runtime-use lease before durable workflow creation, verifies any caller-supplied expected store revision before live-media capture, then delegates complete structured-locator freezing to Session Memory using Tool Execution's optional bounded live-media snapshot capability. Capture returns a graph-detached snapshot whose locators are immutable frozen references, one canonical versioned self-contained `FrozenSet`, and an opaque history revision computed from that rewritten snapshot; private persistence strictly round-trips the set plus message/system-block prompt layer/slot/source and tool-call runtime name/arguments/thought signature. Later `RunAgent` requires that exact canonical agent, `history: read_only`, `tools: none`, a blank live session key, and a frozen snapshot whose key owner and recomputed revision—including frozen references and runtime-only fields—match. It validates that revision before asking Session Memory to materialize integrity-checked embedded bytes, and never rereads the live session or media store on initial execution, resume, retry, restart, managed execution, repair, or fallback. It removes inbound delivery and session scope, derives one stable domain-separated pseudonymous internal identity from only the agent and history revision for account routing and enabled prompt caching, uses no raw session key, media locator, or payload in either identity, and returns fixed `session: private` and `session_mode: private` markers with an empty public cache key/message ID. `cache: none` remains disabled; any other admitted private cache mode normalizes to the pseudonymous session cache. When an existing session is already structured as `review`, live-turn admission rejects it before metadata, history, provider, or context-manager mutation, and Seahorse excludes it from both startup bootstrap and live ingest. The paired atomic admission contract in `FR-AGENT-022` arbitrates deterministic-key ownership before ordinary session use. Both profiles preserve normal account/model alias and fallback resolution plus explicit model and reasoning-effort overrides, but do not register or reserve an interactive turn, append user/assistant/tool messages, compact or summarize a session, initialize MCP, expose tool definitions, execute tool calls, or invoke before/after LLM hooks. Initial calls, structured-output repairs, managed calibration/children/fallbacks, and every provider attempt reuse separately graph-detached copies of the same validated and materialized frozen evidence and no-tool profile. Every compiler-private isolated or read-only request carries an explicit private-execution marker: provider failure classification still updates account health, but shared router state stores only a fixed error, vision fallback emits no raw-error runtime event, and frozen references, sets, materialized data, and capture diagnostics remain inside the workflow's private projection. Cancellation propagates, and any provider response containing tool calls fails instead of entering a tool loop. | Gate evaluation must be able to consult one exact private conversation revision and its captured media across waits and retries without becoming a turn, changing provider prompt provenance after restart, observing later writes, depending on temporary media lifetime, leaking the session/media capability or provider diagnostics through shared routing/event state, or acquiring action authority. |
| `FR-AGENT-021` | MUST | A workflow ephemeral decision invokes the selected configured agent through the stateless side-question provider path with no history/summary snapshot and a cryptographically random identity scoped to that one visible agent request. Initial output, structured repairs, managed calibration, fallback, and concurrent child calls preserve ordinary account/model alias resolution plus explicit model and reasoning-effort overrides and reuse the same stateless no-history, no-prompt-cache, no-hook, no-MCP, no-tool profile. The identity is used only in request-local process options: account-router selection receives a blank affinity key, session scope and inherited delivery routing context are absent, and no interactive turn, active-turn reservation, context-manager assembly, session metadata/catalog/history/summary operation, provider prompt-cache key/directive, hook, tool definition, or tool execution is created. Every provider attempt receives detached messages and options, stateful provider instances remain isolated per call, provider-authored tool calls fail closed, and cancellation propagates. The random identity is never emitted in runtime events, logs, outputs, or account-router state; callers receive only a fixed ephemeral audit marker. | Concurrent isolated gates need normal provider selection and structured execution without colliding on a synthetic session, leaving a 30-day account-affinity record, or acquiring conversation/action authority. |
| `FR-AGENT-022` | MUST | Every ordinary live message atomically admits its final structured session scope before command handling or turn execution. Live admission rejects both an existing and a caller-requested `review` scope, while protected review projection uses the paired review admission mode; whichever wins ownership prevents the other from using protected transcript content, invoking a provider for it, or mutating that key. Existing review sessions are also rejected immediately after final key resolution, before message-tool reset, asynchronous `/stop`, steering continuation, `/clear`, `/btw`, `/context`, metadata, history, provider, or context-manager access, and the direct turn boundary repeats admission for callers that bypass message routing. A replacement-capable path without a usable ordinary scope fails closed instead of bypassing ownership admission. Public workflow `history: read_only` rejects review scope, while compiler-private frozen gate capture remains allowed. Review sessions are excluded from Seahorse startup/live ingest; startup filtering uses the strict snapshot and deliberately skips an unreadable or ambiguous session instead of falling back to tolerant history without proven ownership. Unsupported atomic admission fails closed for replacement-capable stores, while legacy stores incapable of review projection retain compatibility. | A protected internal transcript must not become an ordinary chat because a command bypasses the turn loop, a projection lands between check and metadata write, an inbound caller spoofs the review channel, or a public read-only workflow targets its key. |
| `FR-AGENT-023` | MUST | Under an exact runtime-generation lease, its paused construction boundary, or a generation-owned readiness admission that is drained before reload pause, `AgentLoop.ControllerLocalRepairReady` requires one exact canonical current agent and reports ready only when its configuration, pinned-workspace manager, limits, and at least one concrete provider/model are usable; it may resolve concrete provider instances but never invokes one or selects an account in a way that creates session affinity. While a trusted development-workspace implementation service holds the exact runtime-generation lease, `AgentLoop.ControllerGitWorkspaceManager` returns only the concrete production `gitworkspace.Manager` already installed in the loop and fails closed for nil, typed-nil, or alternate interface implementations; it does not acquire or retain the lease and is not a model, tool, or workflow extension point. Under that same caller-held lease, `AgentLoop.NewControllerLocalRepairRunner` repeats the exact current-agent and dependency checks, selects from only bounded untrusted routing text with no history and blank session affinity, binds the first resolved concrete provider/model without a fallback chain, and returns identity-safe errors. Before model execution, trusted orchestration may record `ControllerLocalRepairPromptDigest`, the deterministic domain-separated SHA-256 identity of the exact isolated system prompt; this evidence surface exposes neither prompt text nor model capability. The controller can then invoke `LocalRepairRunner` with one exact manager-issued checkout pin, bounded untrusted instructions and context, and fixed iteration/output limits. The runner acquires the reservation-derived cross-process Git-workspace operation lock before checkout access and retains it across exact acquisition, every provider/tool edit, and detached postflight; pinned snapshot, commit, and release use the same lock order. Under that lock it reacquires and validates the exact still-locked workspace before any provider call, creates a fresh isolated model loop with no agent instance, session/history, account routing, fallback, prompt cache, hooks, MCP, workflow, shell, network, Git, CI, commit, push, or release authority, and exposes only confined `read_file`, `list_dir`, `edit_file`, and guarded `apply_patch` tools. Provider calls are serialized per runner; tool calls run sequentially in response order; malformed, oversized, conflicting, nil, or panic-derived provider data fails closed; cancellation is checked before each provider and tool boundary. One unconditional bounded detached postflight revalidates the pin and heartbeat after every outcome, while the runner never releases it and returns only bounded sanitized content, iteration count, and workspace identity. | Repair may edit the exact locally verified checkout, but readiness and controller bridge access alone invoke no model and repair must not inherit ambient agent authority, leak the fixed prompt or checkout/provider identity through diagnostics, create account affinity, race another process, commit, or release on the same reservation, execute fallback intent, or advance the development lifecycle itself. |

| `FR-AGENT-024` | MUST | An ephemeral no-history/no-cache/no-tool workflow request may select one exact configured model alias as a request-local override. The runner trims and validates the alias before any provider call, keeps the configured concrete account and normal alias resolution/fallback safety, applies the override to initial and structured-repair calls, records provider-reported usage, and returns the actual concrete model when available. Empty preserves the agent's normal model; malformed, unknown, disabled, or account-incompatible aliases fail before model I/O. | A fair model evaluation needs explicit alias selection without creating a session, accepting raw model IDs, changing account policy, or concealing a provider fallback. |
| `FR-AGENT-025` | MUST | A trusted controller starts an isolated local review against its bounded immutable context. The runner uses a fixed system prompt that treats all caller context as untrusted and requires diagnosis only, with strict outcome, summary, and finding fields for severity, title, file, optional line, message, evidence, impact, and validation. The model cannot supply or suggest a fix, remediation, mitigation, workaround, patch, replacement code, refactor, design/configuration/test change, or next-step advice; no caller-controlled text can override that rule. Unknown fields, including recommendation-like fields, fail strict decoding before a result is returned. | Controller-local review must report evidence-backed defects without acquiring implementation authority or allowing repository text to convert a review into repair advice. |
| `FR-AGENT-026` | MUST | Development conversation POSTs accept exactly `ask` or `steer`, bounded content, a fresh request ID, and the exact current conversation revision. If a candidate revision is supplied, it must equal the latest privately fenced browsable repair and its loaded evidence must still bind that candidate. Ask runs one isolated schema-bounded no-edit answer over provider snapshot, confirmed charter, findings, question, and optional candidate evidence; it cannot claim or invoke code, command, Gate, scope, or publication effects. Steer runs only an isolated scope classifier. A scope-changing request persists `needs_clarification` plus a bounded explanation and creates attention; an in-scope request persists `queued`. Queued steering is sorted into the next implementation instruction only at a later repair boundary, constrained again to the confirmed charter, and receives an `applied` system marker only after repair work ran. The message endpoint itself never interrupts an active file operation, exposes repair tools, or edits a candidate. | Chat must let users understand and guide development without turning text submission into immediate, stale, or scope-expanding mutation authority. |
| `FR-AGENT-027` | MUST | The legacy context manager resolves the configured agent that owns a session before assembling or clearing history and summary, performing proactive or retry overflow compression, or scheduling post-turn summarization. Structured session scope is authoritative, a legacy agent-scoped key remains compatible while its named agent exists, every unresolved explicit opaque or legacy agent-scoped key fails closed, and only an unscoped noncanonical internal key retains the historical default-agent fallback. A named session reads and mutates only its owner's store; compression events carry that owner; and asynchronous summarization re-resolves the same agent ID inside the captured runtime generation before selecting its provider and concrete model. Concurrent default and named sessions never exchange history, summaries, providers, model selections, or event ownership. | Named agents use separate durable session stores; default-agent context access silently discards their continuity and can mutate, summarize, clear, or misattribute another agent's conversation. |
| `FR-AGENT-028` | MUST | Turn-profile authority composes as a monotonic meet between configured global defaults and an incoming runtime profile. A disabled whole profile is the identity; `off` dominates `default` for history and system prompt; and skills/tools follow the `default`, case-insensitive `custom` intersection, `off` lattice. Custom names are trimmed, normalized, deterministically deduplicated, and detached; an empty custom set becomes `off`; stale allowlists are cleared outside custom mode; and any unknown enabled mode fails before provider or tool execution. Existing no-history and no-tools process caps remain authoritative, `DisableTools` is applied last, and history-off disables persistence and summarization. Repeated resolution, including message, side-question, and child paths, is idempotent, while the meet is commutative and associative so no later layer can restore authority removed by an earlier layer. | Task, workflow, and child callers must be able to narrow ambient agent defaults without a second resolution pass or broader global profile silently restoring history, prompt, skill, or tool authority. |
| `FR-AGENT-029` | MUST | `runTurn` admits each turn state once, binds its owning loop, non-closing compatibility pending-result channel, and effective subturn-concurrency limit before active publication, and owns one exactly-once terminal transition for completed, error, hard-abort, and panic outcomes. Terminal commitment rejects later child attachment and legacy-channel result delivery, closes only `Finished`, and preserves exact active ownership through descendant policy, restore-point rollback for hard abort or panic, bounded cancellation-detached Git cleanup, and exactly one ordered `agent.turn.end` publication attempt before exact-pointer removal. Each external cleanup step is panic-isolated so later steps still run; exact owner removal and local cancellation are mandatory, cleanup panic is re-raised afterward, and an original turn panic takes precedence. Hard-interrupt APIs only atomically mark and cancel the exact retained root/child/descendant pointer graph before any cancellation and never call `Finish` or truncate history; repeated or post-terminal hard abort has no effect. Completion signals children so only critical work may survive, while error or panic cancels every nonterminal descendant without relabeling it hard-aborted, and hard abort reaches critical descendants through terminal intermediate parents. Direct `Async:true` SubTurn result delivery remains serialized with terminal commitment, never sends to a closed channel, and classifies each attempt exactly once as delivered or orphaned (`parent_finished`, `parent_mailbox_unavailable`, `nil_result`, or `channel_full`); tracked `spawn` does not use that compatibility channel and instead follows `FR-AGENT-040`. | Turn completion, rollback, child supervision, workspace release, and legacy direct-SubTurn result delivery must have one linearizable owner so a stop cannot race session mutation, a child cannot escape through a removed parent, and late direct results cannot panic or be counted twice. |
| `FR-AGENT-030` | MUST | Agent-side capability inspection preserves the exact executable registry key and, for factory-backed entries, uses only a recursively detached frozen descriptor for parameter-shape projection. A mutable or panicking live tool name, description, parameter map, or prompt-metadata method cannot alter or block the projected workflow-authoring capability after strict registration. Legacy entries retain their compatibility behavior. | Owner-isolated tool construction must not be bypassed by an auxiliary agent projection calling mutable live schema methods or exposing a descriptor graph shared with execution. |
| `FR-AGENT-031` | MUST | Once shutdown or reload has quiesced the retiring runtime generation, every `AgentInstance.Close` panic-isolates and attempts both ToolRegistry and session-store cleanup and joins their failures. AgentInstance construction tracks those owners as soon as they exist and closes them before rethrowing a constructor panic; AgentRegistry construction likewise closes every completed partial agent before rethrowing. A successfully constructed reload candidate remains cleanup-owned until the exact registry/config swap commits, so cancellation or drain failure closes it without retiring the current generation. A committed reload closes the retired context/evolution owners, extracts any retained provider, closes the old AgentRegistry so compatibility-source tool leases are released, and only then closes borrowed old MCP/provider generation services; terminal shutdown uses the same agent close path. Cleanup failure is reported without reopening a retired registry, while a plain legacy tool registry retains its existing no-op close behavior. | Factory-backed live catalog entries hold process-wide leases; failing to close partial, rejected, or retired agent registries leaks those leases across reload and can strand or alias later owner construction. |
| `FR-AGENT-032` | MUST | Agent construction attaches per-owner factories and conservative trusted traits to the exact base catalog defined by the Tool Execution feature while preserving enabled-tool, agent allowlist, read-mode, path-policy, write-alternative, patch-permission, web-provider readiness, tool-adaptation, media, and result behavior. Factory closures retain detached scalar/slice snapshots and never reread mutable config. Mutable wrappers and caches are distinct across owners; Git, channel callbacks, TTS providers, and skills registry managers remain borrowed generation services. A `view_image`-only agent retains `load_image` solely as an unexposed factory dependency and receives media state through the wrapper. Zero-sized hardware wrappers have stable distinct owner identities, and shared concrete TTS providers perform concurrent synthesis without mutating their model selection. Exec compatibility, threads, workflow, cron, and external injection remain legacy or deferred; dynamic MCP/discovery, Seahorse, and install-skill/recursion factories are governed by `FR-AGENT-035` through `FR-AGENT-037`. Root registries remain compatibility-backed, while `FR-AGENT-038` selects this catalog into fresh child turn owners. | Catalog migration must preserve today's root surface, isolate child mutable state, and let narrow authority construct exact subsets with private dependencies. |
| `FR-AGENT-033` | MUST | Every MCP initialization that can create a manager or mutate tool registries owns or reuses one runtime-generation lease and then snapshots the exact config/AgentRegistry pair; static disabled, empty, filtered-empty, or all-disabled generations remain no-op without depending on admission. Reload invokes a generation-explicit initializer inside its already paused/drained boundary. MCP lazy initialization, reset, and terminal manager take share one serialization boundary, so a `sync.Once` is never replaced while `Do` runs; reset clears the manager/error and permits exactly one initializer for the next generation. A blocked generation-A initialization retains its runtime lease so reload B cannot swap, retire A, or initialize B until A completes, after which B contains only B MCP state. Media-store setters and reload candidate media application/publication share a second serialized lock ordered before the AgentLoop state lock; callers replace media only under startup or paused/drained runtime quiescence. A setter finishes propagation before another setter can overtake it, no setter retains a registry across its retirement, candidate tools receive the current retained store before publication/dynamic initialization, and any setter waiting across the swap applies only to the newly published registry. Existing retirement ordering still closes A registries before A MCP manager/provider. | Dynamic factory wrappers borrow generation services and media state; unsynchronized lazy init/reset or a stale media setter can mix generations, race `sync.Once`, publish into a retired registry, or leave reloaded media tools unusable. |
| `FR-AGENT-035` | MUST | For a captured generation with at least one enabled selected MCP server, initialization validates and freezes effective discovery settings before creating a manager or making a network request, snapshots sorted agents with distinct open compatibility registries and context builders, and owns the candidate manager through explicit private, registry-committed, and runtime-published states. After load, only actually connected configured servers participate; each mutable SDK declaration is read and strictly frozen once, identical repeated exact identities deduplicate, conflicting repeats or canonical collisions fail, and every per-agent wrapper/factory is rebuilt from that detached spec with the agent's workspace. MCP server allowlists select candidates; registry allowlists remain the authoritative transaction admissions. Exact existing MCP identity may refresh through occupant pointer CAS, while built-in/different identity collisions fail. Existing Regex/BM25 destination-bound factories use frozen effective TTL/result limits and are staged only for an allowed connected deferred server with a nonempty tool surface. One deterministic `InstallFactoryBackedTransaction` publishes every agent's admitted MCP/discovery catalog or none. Cancellation or panic before commit closes the private manager, leaks no lease/tool/prompt, and becomes one cached initialization error; transaction success is irreversible because wrappers already borrow the candidate, so every later projection/prompt fault retains and publishes rather than closes it. Prompt contributors are absent at agent construction and derive only from detached transaction admissions. Exact admitted names, not lossy server prefixes, govern request allowlists and counts; exact lowercase-safe server IDs preserve historical prompt IDs, while every lossy/case/truncated identity uses a domain-separated full SHA-256 suffix. Deferred server prompts also require an admitted discovery root. Successful selected-owner construction returns fresh MCP and discovery wrappers with owner-local media, cache, TTL, and promotion state while borrowing the same live manager/event service; `FR-AGENT-038` now uses those products for strict child turns and owner-local promotion. | Dynamic MCP must not expose a partial cross-agent generation, close a manager under committed wrappers, advertise failed or unauthorized tools, let prompt normalization cross-authorize servers, or alias mutable wrapper/search state across owners. |
| `FR-AGENT-036` | MUST | Selecting Seahorse constructs one context-aware candidate for the exact current AgentRegistry. Before engine I/O it snapshots sorted agents, requires distinct compatibility ToolRegistries and one default agent, freezes distinct canonical per-agent database paths, and rejects any same-path, case-insensitive existing-ancestor, hard-link, or resolved-parent alias. After every engine opens and before bootstrap, it rejects persistent path-identity drift and databases that now resolve to one physical file. Under one private ownership guard it creates and records every engine, then bootstraps sorted detached session keys with the construction context; review sessions and strict unreadable/corrupt snapshots remain skipped, while cancellation, panic, actual bootstrap failure, or a later-engine failure closes the complete candidate and exposes no retrieval tool. Only after every engine/bootstrap succeeds does it stage fresh `short_grep` and `short_expand` compatibility wrappers with descriptor-identical per-owner factories. Wrappers are always read-only, parallel-safe, idempotent, and per-owner, borrow the exact agent retrieval engine, use no owner service cache, and never close it. One deterministic `InstallFactoryBackedTransaction` publishes every admitted registry or none: ordinary per-agent tool allowlists remain authoritative, a denied name leaves its occupant untouched, and an allowed occupied name collides rather than refreshing an unverifiable engine binding. Transaction success irreversibly transfers engine lifetime to the context manager, so later admission projection or panic cannot close it beneath committed wrappers. A both-denied agent still retains its context engine for Assemble/Ingest. Selected child and sibling owners receive distinct wrappers while borrowing only their agent's engine; closing them cannot affect the source, siblings, or context manager. Startup uses a background construction context; paused reload closes A's context manager before constructing B, installs B or a legacy/no-`short_*` fallback before admission resumes, and never overlaps or reuses A engines. Terminal shutdown closes context engines before registry source leases. Unsupported platforms remain legacy-only; on supported platforms `FR-AGENT-038` now selects fresh retrieval wrappers into strict child owners. The isolation fence assumes the workspace filesystem namespace is stable during paused construction; adversarial out-of-process symlink, mount, or rename swaps during SQLite's internal open are outside this contract because the current engine exposes no opened-file identity. | Per-agent retrieval must not partially publish, share a database in the stable trusted-workspace namespace, survive on a closed generation engine, close borrowed context state from a child, or let reload resume with wrappers bound to stale or rejected SQLite owners. |
| `FR-AGENT-037` | MUST | Initial construction and paused reload sort exact agents after ordinary shared-tool registration, require distinct open compatibility ToolRegistries, and stage a fixed-order insert-only catalog containing enabled `install_skill`, `spawn`, `subagent`, `spawn_status`, and `delegate` entries with nil expected occupants. Enablement remains exact: `(spawn or spawn_status) and subagent` constructs the manager-backed recursion bundle; enabled spawn stages `spawn` plus synchronous `subagent`; enabled status stages `spawn_status`; subagent alone stages neither; more than one configured agent stages `delegate`; and enabled skills plus install-skill stages `install_skill`. Each agent-generation recursion specification fixes the source agent ID, workspace, model, cloned nil-versus-empty fallbacks, token/temperature/media limits, pre-recursion compatibility-tool snapshot, provider, AgentLoop, and authorization callbacks. Root compatibility wrappers use one separate root manager. Every strict owner selecting `spawn`, `subagent`, or `spawn_status` obtains one fresh namespaced manager service shared by those selected wrappers but never by another owner or agent; `delegate` instead receives a fresh wrapper over the exact generation spawner and source-agent policy without acquiring manager task tracking. All selected wrappers are pointer-distinct from the root and sibling owners. The direct `AgentLoopSpawner` remains installed so asynchronous preparation retains runtime-generation ownership, while central `spawnSubTurn` remains authoritative for target policy, depth, concurrency, timeout, and result delivery. Trusted traits are process-risk, serialized, non-idempotent, per-owner for `spawn`, `subagent`, and `delegate`; read-only, parallel-safe, idempotent, per-owner for `spawn_status`; and external-write, serialized, non-idempotent, per-owner for `install_skill`. Within one generation, root and owner install wrappers for an exact cleaned absolute workspace, a successfully resolved symlink alias, or an existing `os.SameFile` alias borrow one shared process mutex under the Skills feature's borrowed-lock contract; distinct identities receive distinct locks. One deterministic `InstallFactoryBackedTransaction` publishes all registry-allowlisted entries across all agents or none: a denied entry leaves its occupant untouched, while an allowed initial or late collision aborts without candidate leases. Positional admissions must report no replacement, exact committed pointers, and exact version deltas; a malformed postcommit projection is logged without undoing committed maps. Initial failure appends a configuration error to every agent, while reload failure rejects and closes the candidate registry before the swap. Root execution remains compatibility-backed; `FR-AGENT-038` selects recursion/install wrappers into strict child turns and `FR-AGENT-039` activates their owner-local spawn tracking. | Recursion and skill-install wrappers must be constructible for isolated owners without widening child authority, aliasing manager state, racing one workspace install, or partially publishing a multi-agent generation. |

| `FR-AGENT-034` | MUST | The Agent management list accepts the shared bounded `query`, opaque `cursor`, and `limit` contract and returns ordered agent summaries with `total`, `next_cursor`, `canonical_query`, and an allowlisted typed `query_schema`; direct `/api/agents/{id}` lookup never depends on the list response. Bulk delete accepts at most 200 explicitly selected stable IDs plus the current config revision, preserves the implicit/default agent and every referenced or otherwise blocked agent, removes all valid agents in one fenced candidate-config save, and reports `deleted_ids`, stable per-ID failures and blockers, the new revision, and restart effects without mutating unrelated config. The UI uses the standard List/Table/Grid collection at `/agent/agents`, dedicated `/new`, `/{id}`, and `/{id}/edit` routes, and item-owned `/{id}/capabilities` and `/{id}/activity` routes; query is held in `q`, selection is explicit and partial-success aware, route-scoped query canonicalization cannot interrupt navigation away from the collection, direct details use the item endpoint, and legacy `?agent=` selection is not interpreted. | Agent administration needs stable deep links, server-authoritative paging, and concurrency-safe multi-item mutations without coupling details to one loaded grid or losing default/reference blockers. |

| `FR-AGENT-038` | MUST | Every SubTurn computes one exact child capability meet before factory calls, attachment, spawn events, or provider I/O. The immediate parent's current selected registry supplies authority and, for same-agent descendants, implementation; a named target supplies implementation only after central `CanSpawnSubagent` authorization. `SubTurnConfig.Tools` is name-only: nil adds no cap, non-nil empty selects none, and non-empty selectors are panic/typed-nil/blank safe, case-insensitively resolved back to exact target keys, harmlessly deduplicated, and rejected as unknown or unavailable without retaining their pointers. The final sorted roots are the constructible parent/target/explicit/inherited-profile/eligibility intersection; `threads`, `workflow`, `cron`, `exec`, `exec_command`, and `write_stdin` are always excluded, recursion disappears at the depth cap, and status requires spawn. One exact scoped turn owner transactionally constructs fresh roots and private dependencies, so descendants cannot regain removed names and sibling mutable state, media, discovery promotion, caches, managers, and TTL remain isolated. Native `web_search` authority obeys the same meet and freezes the root request's actually observed routed/fallback/hook-narrowed surface into async preparation. When a physical `web_search` root is selected, each actual provider independently uses native search when supported or retains that client tool otherwise. Without a physical root, pseudo-only search is admitted only when the exact configured implementation provider set is statically proven native-capable; model-router and custom same-agent model/fallback sets are conservatively unproven, so implicit inheritance omits search and an explicit selector is unavailable. A later hook rewrite to an unsupported provider fails unless that hook also narrows search, in which case the request has neither native nor client search. Hook-added definitions are intersected back to physical strict roots, and graceful terminal requests cannot re-enable search. The effective custom/off profile governs prompts, discovery contributors, provider definitions, hook rewrites, execution rechecks, and native options. Async preparation retains runtime/source admission and acknowledges immediately while no more than the configured concurrency count of queued source admissions coexist per parent; excess scheduling fails before launch. Its background path acquires one configured child-execution slot before consuming the source lease or constructing a factory, then releases that slot after `runTurn`. Terminal ownership stops new construction and boundedly drains pre-admitted source leases. A successful drain applies descendant/Git policy, closes strict tools then the ephemeral session exactly once, converts close failure to an error while preserving an original panic, commits immutable status, emits `turn.end`, removes the exact active pointer, and only then releases runtime ownership. A drain timeout instead fences late attachment through failure traversal, commits error without closing beneath construction, and lets the last runtime-retained construction release synchronously perform the same eventual tool/session close; repeated outer cleanup cannot bypass that pending state. Async spawn snapshots caller/wrapper slices and never dereferences a closed owner product. Root turns remain compatibility-backed. | Child work must monotonically narrow authority, construct isolated mutable state, preserve exact native/tool behavior across real provider selection, bound queued owner construction, and release every strict owner without closing it beneath scheduled construction or active execution. |
| `FR-AGENT-039` | MUST | After `spawn` validation, target authorization, and synchronous runtime/source preparation succeed, the exact manager shared by that owner-local `spawn`/`spawn_status` bundle inserts one `running` record before launching the only background goroutine. The existing immediate acknowledgement includes its manager-lifetime `subagent-N` ID. IDs and records are in-memory, generation/owner-local, and intentionally neither global nor durable; root, strict sibling, target-agent, and later-generation managers never share catalogs. The record freezes task, label, target metadata, source agent, canonical session key, channel, chat, and one creation time. The goroutine rereads no mutable wrapper/manager configuration and executes the detached direct SubTurn runner with `Tools:nil`, `Async:false`, and `Critical:true`: the manager supplies background execution, while disabling direct-SubTurn async delivery prevents the legacy parent channel from becoming a second producer. Synchronous `subagent`, `delegate`, and `/subagents` remain untracked. Exactly one locked transition changes `running` to `completed`, `failed`, or `canceled`: completion requires a non-nil non-error result; ordinary errors, error results, nil success, and panic fail; wrapped cancellation or deadline cancels. The committed task ID and status accompany a non-nil callback result attempted after the terminal snapshot is visible and outside the manager lock; callback panic is contained, and prepared runtime/source ownership releases exactly once afterward. Preparation failure creates no record or goroutine. Manager Get/List APIs return detached copies. A status caller with a complete trusted agent/session pair, complete channel/chat pair, or both sees only records exactly matching every supplied field; a partial pair fails closed, originless and foreign records are hidden, and exact-ID probing is reported as not found. A direct programmatic call with no injected origin retains compatibility list/get-all behavior. The callback is the sole tracked-result producer and hands delivery to `FR-AGENT-040`; it never publishes raw user output or synthetic system ingress. | Background work needs one immediately queryable lifecycle record without weakening P005c authority, leaking another conversation's task text/result, racing manager state, or creating a duplicate delivery path. |
| `FR-AGENT-040` | MUST | Before executing a first-party tracked `spawn`, the parent validates and snapshots one delivery route containing the source parent turn ID, exact canonical destination agent and named session, semantic session scope, channel/chat, detached safe inbound projection, and graph-detached effective profile/process caps. The SpawnTool adds a private provenance marker distinct from generic manager completion metadata. After `FR-AGENT-039` commits terminal manager state, its sole callback filters and UTF-8 bounds the non-nil completion and offers exactly one process-local result envelope keyed by the composite source-parent-turn ID plus manager-local task ID within that exact destination namespace. Identical replay is a no-op; changed content/authority reuse, invalid result, or capacity failure is orphaned without replacing an existing envelope. An active eligible turn claims at most one matching envelope at established result checkpoints and only when its agent/session/scope/channel/chat match; a claimed final-checkpoint result keeps exactly one no-tool follow-up alive beyond the ordinary iteration cap. If no eligible turn owns the session, one reserved continuation claims it only after the original output owner has published or disposed its response, resolves the exact existing named agent/session/scope through a fresh coherent runtime generation, re-applies the captured authority caps, and never falls back to a default or manual steering scope. Roots with no-history, non-detachable call/usage policy, a custom system overlay, a live prompt-cache partition, or explicit model/fallback/account/reasoning selectors may deliver while their exact root remains active but orphan with `root_not_persistent` or `root_continuation_policy` instead of constructing a semantically different late turn. Exact placeholder deletion wakes pending work, while steering committed during a late continuation is drained under the same strict route, including after an initial provider error. Pre-claim transient setup/snapshot failures and strict steering rescue failures receive bounded delayed retries; permanent routing/session failures orphan. Claim is terminal for model visibility: provider, persistence, finalization, cancellation, or publication failure after claim is not retried because replay could duplicate the result. A successfully continued response is published at most once. Pending queues are bounded to 16 per root; process-lifetime terminal tombstones retain only compact identity/fingerprint/status rather than result/profile/context payload. The mailbox provides neither crash/restart durability nor exactly-once provider execution. Generic asynchronous tool callbacks, synchronous `subagent`/`delegate`, `/subagents`, explicit child `message` effects, and direct `Async:true` SubTurn compatibility-channel behavior are unchanged. | One tracked completion must become at most one parent-visible result in its exact named conversation, even when the child finishes before or after the spawning turn, task IDs collide across owner-local managers, a callback is replayed, output publication overlaps completion, steering arrives during continuation, or reload replaces the generation. |

| `FR-AGENT-041` | MUST | Until structured plans are backed by durable coding-task state, normal Agent Pipeline provider definitions omit the factory-backed `update_plan` identity for PicoClaw, Simple, and Codex surfaces and reapply that omission after BeforeLLM rewrites, fallback selection, and runtime adaptation. A provider-authored call is converted to one skipped-tool result/event before `BeforeTool`, and a `BeforeTool` rename to the reserved identity is rejected again before approval or dispatch. Prompt callable-tool and allowed-name calculations omit the reserved identity and suppress the generic tool-use rule when no other callable definition remains. The capability catalog reports `update_plan` blocked with `requires_durable_plan`; explicit adaptation probes use a non-executed truthful compatibility tool instead. Trusted direct registry, workflow, and owner-factory calls remain available. | Removing an advertised schema is insufficient if stale history, a provider, a hook, or a prompt can still invoke or encourage volatile plan state; model turns must fail closed without breaking certified native factory contracts. |

## Data And State Model

Agent collection identity is canonical agent `id`. Query fields are sortable
`id`, `name`, `workspace`, `account`, `model`, `default`, `implicit`, and
`position`; booleans suggest only `true` and `false`, and default ordering is
ascending position plus stable ID. Bulk failure codes include `invalid_id`,
`duplicate_id`, `invalid_agent_id`, `agent_not_found`,
`implicit_agent_required`, and `agent_referenced`; blockers contain bounded safe
labels only. The list/config revision fences configuration-backed mutations.

Agent state includes configured defaults, resolved candidate providers, registered
tools, skills filter, MCP allowlist, context builder cache, runtime event bus,
turn scope, and session store references. A turn records user input, media,
assistant content, tool calls/results, optional reasoning, and runtime metadata.
Inbound session reservations additionally retain a process-local turn-UX
identity and detached inbound-context snapshot. Per-session handoff locks
serialize reservation, steering enqueue/dequeue, rebind, and abandonment;
rescue markers explicitly own committed steering until a live continuation or
competing turn takes it. Workspace capability state also retains its active
definition source and exact composite revision independently from the global
agent-config revision; runtime activity remains process-local and bounded. The
frozen context used by an isolated workflow decision is never installed as the
live session state. An ordinary read-only call holds it for that execution; a
compiler-private workflow may persist the detached snapshot and revision in its
owner-local private root so a later resume or retry can supply the same
evidence. Before that root is created, Session Memory rewrites every structured
media locator to a frozen reference and returns the strict versioned
self-contained `FrozenSet` persisted beside the snapshot. Its explicit private
representation includes that set plus runtime-only prompt provenance and
tool-call name/arguments/thought signature that ordinary session JSON
deliberately omits. The opaque history revision binds the rewritten snapshot;
the enclosing private-root revision also binds the strict set. The agent runner
validates those frozen forms before materialization and retains no raw session capability in provider cache
or account-router identity: it derives a stable domain-separated pseudonym from
the canonical agent and opaque history revision, omits inbound/session scope,
and emits only fixed `private` session markers. A stateless workflow decision holds
only one request-local random identity and its supplied prompt/context/scope;
neither the identity nor an empty synthetic session is entered into the session
store, active-turn map, runtime-event scope, prompt cache, or account-router
session map. A local repair run likewise creates no conversation or account
state. Its transient state is limited to the exact checkout reservation,
bounded provider messages, four-tool registry, iteration counter, and sanitized
result; ordinary repository content mutations remain inside the still-held
workspace pin. A local review run likewise creates no conversation, cache,
affinity, hook, workflow, MCP, or tool state; only its bounded request and
normalized structured result survive at the caller-owned durable boundary.
Development conversation state is instead an ordered aggregate-owned message
stream. Its revision is the exact message count; user, assistant, and system
records retain mode/status plus charter/head provenance. Candidate content is
loaded only transiently from the exact private repair fence and is not copied
into the public message record.
Recursion catalog state is generation-local and immutable after staging: it
contains per-agent construction snapshots, root compatibility wrappers,
factory descriptors and traits, one owner-service namespace, and one
process-local install-lock identity map. Each manager now owns in-memory
manager-lifetime spawn IDs and copied running/terminal records for its exact
root or strict owner namespace. It creates no durable/global task ID, process
session, synchronous-subagent record, or delegate record. Separately, the
AgentLoop owns a bounded process-local tracked-result mailbox. Each envelope
freezes the composite source-parent-turn/task identity, terminal status,
filtered bounded content, exact destination agent/session/channel/chat,
detached inbound context, and claim state. Composite deduplication prevents
owner-local `subagent-N` collisions and replay; claimed entries are terminal
for visibility and are not a durable queue or restart record.

## Surface Ownership

Owns: CODE cmd/picoclaw/main.go
Owns: CODE cmd/picoclaw/dns_noresolv.go
Owns: CODE cmd/picoclaw/internal/agent/**
Owns: CODE cmd/picoclaw/internal/model/**
Owns: CODE cmd/picoclaw/internal/status/**
Owns: CODE cmd/picoclaw/internal/version/**
Owns: CODE pkg/agent/**
Owns: CODE pkg/audio/**
Owns: CODE pkg/devices/**
Owns: CODE pkg/providers/**
Owns: CODE pkg/tokenizer/**
Owns: CODE web/backend/api/agents*
Owns: CODE web/backend/api/agent_capabilities*
Owns: CODE web/frontend/src/api/agents*.ts
Owns: CODE web/frontend/src/components/agent/**
Owns: CODE web/frontend/src/components/collections/pilots/agent*
Owns: CODE web/frontend/src/routes/agent/**
Owns: CLI cmd/picoclaw/main.go *
Owns: CLI cmd/picoclaw/internal/agent/*
Owns: CLI cmd/picoclaw/internal/model/*
Owns: CLI cmd/picoclaw/internal/status/*
Owns: CLI cmd/picoclaw/internal/version/*
Owns: CONFIG.agents*
Owns: CONFIG.model_list*
Owns: CONFIG.model_aliases*
Owns: CONFIG.build_info
Owns: CONFIG.version
Owns: CONFIG.voice*
Owns: CONFIG.tools.spawn*
Owns: UI /agent/agents*
Owns: CONFIG.tools.spawn_status*
Owns: CONFIG.tools.subagent*
Owns: CONFIG.devices*
Owns: HTTP /api/agents*
Owns: HTTP * /api/agents*
Owns: TEST cmd/picoclaw/main_test.go *
Owns: TEST cmd/picoclaw/internal/agent/*
Owns: TEST cmd/picoclaw/internal/model/*
Owns: TEST cmd/picoclaw/internal/status/*
Owns: TEST cmd/picoclaw/internal/version/*
Owns: TEST pkg/agent/*
Owns: TEST pkg/providers/*
Owns: TEST pkg/tokenizer/*
Owns: TEST pkg/audio/*
Owns: TEST pkg/config/voice_selection_test.go *
Owns: TOOL spawn
Owns: TOOL spawn_status
Owns: TOOL subagent
Owns: TOOL delegate
Owns: EVENT agent.*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw agent`, `picoclaw model`, `picoclaw status`, `picoclaw version` | Direct agent use, model selection, status, and build metadata. | `FR-AGENT-003`, `FR-AGENT-009` |
| CLI | root `picoclaw` command registration | Compose feature-owned subcommands such as workflow and event operations while leaving their implementation and policy in the owning package. | `FR-AGENT-009` |
| Config | `agents.*`, `model_aliases[]`, `account_routers[]`, `model_routers[]`, `model_list[]` | Default/per-agent account refs, exact aliases and fallback aliases, alias-to-concrete-model mappings, account overrides, routing, provider transport configuration, and execution policy. | `FR-AGENT-002`, `FR-AGENT-003`, `FR-AGENT-004` |
| Config | `model_list[].reasoning_effort` | Optional OpenAI-style reasoning effort forwarded only after shared normalization and validation. | `FR-AGENT-003`, `FR-AGENT-010` |
| Tools | `spawn`, `spawn_status`, `subagent`, `delegate` | Child work delegation and status reporting with strict owner-local construction, monotonic nested authority, one tracked manager record for each admitted async spawn, and one at-most-once exact-session result envelope for its terminal completion. | `FR-AGENT-007`, `FR-AGENT-037`, `FR-AGENT-038`, `FR-AGENT-039`, `FR-AGENT-040` |
| Runtime | `AgentLoop.PauseRuntimeForReload`, retained runtime leases, provider/config reload | Quiesce root and asynchronous runtime users across a registry generation swap, service commit, or rollback. | `FR-AGENT-013` |
| Go API | `interfaces.ChannelManager`, optional `MessageScopedTypingStopper`, `MessageScopedTurnUXCleaner`, `MessageScopedTurnUXRebinder`, and `MessageScopedPlaceholderSender` | Keep the legacy manager surface sufficient while allowing built-in channels to stop, clean, transfer, and create transient UX for one opaque turn identity. | `FR-AGENT-015`, `FR-AGENT-016` |
| Go API | `interfaces.MessageBus.GetStreamer`, optional `interfaces.TurnScopedMessageBus.GetStreamerForTurn` | Use turn-scoped streaming when implemented and otherwise call the original four-argument streamer lookup. | `FR-AGENT-015`, `FR-AGENT-016` |
| Runtime | `bus.InboundContext`, `DispatchRequest`, turn reservations, continuation targets, and outbound context derivation | Carry detached process-local event and transient-UX metadata across one turn without adding it to serialized routing context. | `FR-AGENT-015`, `FR-AGENT-017` |
| Runtime | `workflowAgentRunner.CaptureReadOnlySession`, `workflowAgentRunner.RunAgent`, and `AgentLoop.askSideQuestionWithOptions` frozen-context profile | Capture one strict existing-session snapshot, use Session Memory and Tool Execution primitives to freeze its complete media graph into the persisted private form, or validate/materialize that form without live rereads, then perform no-tool/no-hook provider decisions without joining or mutating the interactive turn lifecycle or exposing raw capabilities through provider identity. | `FR-AGENT-020` |
| Runtime | `AgentLoop.askSideQuestionWithOptions` stateless profile | Perform no-history/no-cache/no-tool provider calls under one request-local identity while suppressing session-affine account routing and all durable or observable session identity. | `FR-AGENT-021` |
| Go API | `AgentLoop.ControllerLocalRepairReady`, `AgentLoop.ControllerGitWorkspaceManager`, `ControllerLocalRepairPromptDigest`, `AgentLoop.NewControllerLocalRepairRunner`, `LocalRepairRunner.Run` | Under a caller-held runtime-generation lease, paused construction boundary, or drained generation-owned readiness admission, verify one exact agent has a concrete repair target without a provider call or affinity; expose only the installed concrete production workspace manager and an opaque domain-separated identity of the exact fixed repair prompt for trusted orchestration; under the lease, resolve untrusted routing text with no history and blank account affinity to the first concrete model/provider only, then run a fresh controller-only repair loop against one exact held checkout reservation with four confined file tools and detached postflight validation. The bridges acquire no lease and add no model-facing, workflow-extension, or public authority; the concrete manager remains a trusted-controller-only lifecycle capability. | `FR-AGENT-023` |
| Go API | `AgentLoop.ControllerLocalReviewReady`, `AgentLoop.NewControllerLocalReviewRunner`, `ControllerLocalReviewRunner.Run`, `ControllerLocalReviewPromptDigest` | Under the caller's runtime-generation lease, resolve one exact usable agent and run a detached, no-history/no-cache/no-tool local review using the immutable diagnosis-only prompt and strict bounded schema; return only normalized findings or fixed safe errors. | `FR-AGENT-025` |
| HTTP/UI | `/api/development-workspaces/:id/conversation/messages`, development chat | Revision-fenced Ask answers and safe-boundary Steer queues over optional exact candidate evidence, with explicit queued/applied/clarification status. | `FR-AGENT-026` |
| Events | `agent.*` | Turn, LLM, tool, steering, interrupt, subturn, and error telemetry. | `FR-AGENT-001`, `FR-AGENT-004`, `FR-AGENT-006` |
| HTTP/UI | `/api/agents*`, `/agent/agents` | Project and mutate persistent configured agent policy with ordered results, revision fencing, explicit model fallback semantics, workspace capability CAS, sanitized live activity, deep links, and restart feedback. | `FR-AGENT-018`, `FR-AGENT-019` |
| Runtime | `AgentInstance.Close`, `AgentRegistry.Close`, provider/config reload | After generation quiescence, close tool/session owners and release compatibility-source leases before borrowed retired generation services. | `FR-AGENT-031` |
| Runtime | `AgentLoop.EnsureMCPInitialized`, generation-explicit MCP initialization, `mcpRuntime` reset/take, `AgentLoop.SetMediaStore`, provider/config reload | Serialize lazy dynamic-service ownership across exact runtime generations and converge every published registry on the latest retained media store. | `FR-AGENT-033` |
| Runtime | MCP catalog staging, multi-registry factory installation, admission-derived prompt publication, candidate-manager ownership | Publish one detached constructible MCP/discovery generation across every agent or preserve the complete prior state. | `FR-AGENT-035` |
| Runtime | Seahorse generation construction, session bootstrap, multi-registry retrieval-factory installation, and context-manager ownership | Construct every isolated per-agent engine first, publish one atomic admitted `short_*` catalog, and close engines only with their exact context generation. | `FR-AGENT-036` |
| Runtime | Recursion/install-skill catalog staging, owner-local manager services, workspace install-lock coordination, and shared-tool reload | Publish one exact constructible recursion/install generation across all agents or preserve the current generation. | `FR-AGENT-037` |
| Runtime | SubTurn effective-tool selection, short source-construction leases, strict turn resource ownership, and terminal cleanup | Intersect authority before construction and close exact owner state after execution quiesces. | `FR-AGENT-038` |
| Runtime | Owner-local `SubagentManager` task catalog and exact-origin status projection | Allocate one task before async launch, commit one copied terminal snapshot, and expose it only within its source owner/origin namespace. | `FR-AGENT-039` |
| Runtime | Tracked-spawn result mailbox and exact-session continuation | Deduplicate by composite parent-turn/task identity, claim one filtered bounded completion at most once, and continue only the exact named conversation through a fresh coherent runtime generation. | `FR-AGENT-040` |

| HTTP/UI | `/api/agents*`; `/agent/agents`, `/new`, `/{id}`, `/{id}/edit`, `/{id}/capabilities`, `/{id}/activity` | Typed query/cursor list, direct stable-ID detail, revision-fenced explicit-ID bulk delete with partial results, and standard collection/detail/editor presentation. | `FR-AGENT-018`, `FR-AGENT-019`, `FR-AGENT-034` |

## Algorithms And Ordering

1. Build an `InboundContext` and resolve the route/session before prompt work.
2. Resolve prompt contributors and turn profile decisions before provider calls.
   For an already-admitted isolated process profile, carry its exact explicit
   system overlay and set `SuppressDefaultContext`; suppress default system,
   workspace/bootstrap, identity, memory, skills, contributors, tool-use rule,
   time, and runtime blocks without creating a tool fallback. The owning
   no-history/no-tools/no-cache path must therefore serialize exactly that
   system overlay and the supplied user content, with no prompt-cache control.
3. Resolve the effective `account_ref` and exact alias. Expand an account
   router to concrete account candidates, evaluate any model router to an
   alias, resolve that alias separately for each concrete account, and only
   then build providers. Normalize optional controls such as `reasoning_effort`
   and execute provider attempts with retry/fallback policy. A credential-backed Codex attempt that returns the structured usage
   exhaustion error serializes by account, rechecks the authoritative main
   limit and reset count, consumes at most one eligible reset, reconciles the
   same window, and retries the same provider request once after a confirmed
   redemption before fallback observes the error. Failed verification
   suppresses another automatic reset for that exhaustion episode.
4. For each tool-call response, validate tool availability and arguments, run hooks and registry execution, append tool results, and re-enter provider execution until done or capped.
5. For an exact read-only workflow decision, accept the already-captured
   history and summary instead of consulting the context manager. For a
   compiler-private launch, first capture and detach one strict session under a
   runtime lease before durable creation, require any supplied expected store
   revision to match before live-media access, freeze its complete structured media
   graph through the Session Memory helper and Tool Execution snapshot reader,
   and persist the rewritten snapshot with its strict self-contained
   `FrozenSet`. When execution later supplies it, preserve
   message/system-block prompt provenance and tool-call runtime fields through
   the workflow's explicit private encoding, validate exact agent ownership and
   the strict set, and recompute the frozen-snapshot revision before
   materializing embedded bytes. This path never rereads live history or media,
   including after resume, retry, or restart. Construct
   the normal agent prompt and provider candidates with zero tool definitions,
   skip hooks and interactive turn registration, reject tool-call responses,
   and return only the isolated response. Every repair or managed call receives
   separately detached copies of the same validated, materialized snapshot.
   Private calls omit inbound/session scope, use a
   domain-separated agent-plus-revision pseudonym for enabled prompt cache and
   account affinity, and replace raw session/cache/message identities with fixed
   public private markers. For a stateless workflow decision, build the prompt
   without a snapshot or context-manager assembly, retain one random
   request-local identity across the visible execution, and pass a blank key to
   every account-router selection. Every initial, repair, calibration, fallback,
   and managed-child provider call uses no history, prompt cache, hooks, MCP, or
   tools; only a fixed ephemeral marker survives the request.
   Before any ordinary live command, inbound media preparation, steering/turn
   mutation, message-tool reset, or provider use, atomically admit the final
   session key and scope. Reject an existing or requested `review` scope; when
   a legacy internal caller supplies no scope, preserve its existing ordinary
   scope or establish a stable ordinary internal claim instead of bypassing
   admission. Seahorse independently omits review sessions from startup
   bootstrap and live ingest.
6. Keep the detached inbound snapshot on the reservation and real turn. After
   any slow inbound preparation, recheck the session owner under the handoff
   lock; either claim the idle session or atomically enqueue and rebind
   same-chat steering to the pinned owner. Retire cross-chat transient UX
   immediately after its queue commit because the active turn cannot own that
   chat key. If a reservation is abandoned after steering commits, a bounded
   rescue continues the queue or transfers it to a competing live owner.
7. Write final messages and summaries after the assistant response is known.
   Propagate the inbound snapshot to same-chat output. Once buffered delivery
   accepts output, stop only exact typing and let channel pre-send own
   reaction/placeholder cleanup; otherwise perform exact full cleanup. Use the
   optional message-scoped manager and streamer capabilities when present and
   their legacy fallbacks when absent.
8. Before replacing provider/config state, pause new runtime admission and wait
   for current generation leases. Acquire before inbound trigger/routing
   decisions and transfer a retained lease to any worker waiting for a
   semaphore. Retain before launching other asynchronous workflows or spawn
   work, propagate through independently cancelable child contexts, require
   exact config/registry identity for summarizers and gateway-owned
   scheduled/event work, remove the runtime-event subscription for the outer
   transaction, and recreate it for the final config before admission resumes.
   After committing the registry swap, close retired context/evolution owners,
   extract any retained provider, close every old agent tool registry and
   session store, then close borrowed old MCP/provider generation services.
9. On terminal shutdown, remember Stop even if `Run` has not registered yet,
   quiesce runtime producers, cancel and join the AgentLoop and its automation
   controller, and hold a permanent runtime pause until active leases reach
   zero. Only then stop channel/media dependencies and close provider, bus, and
   registry resources. A timeout leaves dependencies/resources open for
   process teardown.
10. In Agent management, keep the ordered grid as the entry surface and put the
   exact selected agent and allow-listed detail tab in the URL. A capability
   draft owns its loaded composite revision until save or explicit reload.
   Block tab, agent, route, and browser navigation while that draft is dirty;
   proceed only after the operator explicitly discards it. A revision conflict
   preserves the draft, disables another save, and requires an explicit reload
   before editing can resume.
11. Mount activity polling only on the selected Activity tab. Poll while the
    gateway and browser are online, the document is visible, and the operator
    has not paused; abort on unmount or agent change, and pause after a request
    error until explicit retry. Merge by sequence into at most 200 browser
    rows, apply severity switches only as a presentation filter, and surface
    recorder reset, truncation, and each drop counter without persisting the
    cursor.
12. For controller-only local repair, hold the exact runtime-generation lease,
    fail closed unless the loop contains its concrete production workspace
    manager, and have trusted orchestration persist the domain-separated digest
    of the exact fixed repair prompt before the provider call. Acquire and
    validate the exact pinned checkout before constructing any model-visible
    state, register only the confined read/list/edit/patch tools, and run
    provider calls serially with response-order tool execution. Check
    cancellation at every provider/tool boundary, bound and sanitize every
    projection, then always run one detached bounded pin-and-heartbeat
    postflight. Keep the reservation locked so the controller alone can
    reverify the PR and choose the next lifecycle action.
13. For controller-only local review, keep the caller's exact runtime-generation
    lease, resolve the immutable session agent, and reject stale or unusable
    configuration before any provider call. Build a fresh private side-question
    request from only the fixed review system prompt and bounded caller context;
    suppress default context, history, cache, affinity, hooks, MCP, workflows,
    and tools, detach every message/options graph, and use a fresh provider
    instance. Reject tool calls and strictly parse one bounded structured
    outcome/findings payload. Return only normalized values or fixed safe errors;
    never create a turn, session record, cache key, event identity, repository
    capability, or lifecycle action.
14. For development chat, compare the supplied conversation revision before AI
    work, then revalidate any supplied candidate against the latest browsable
    repair and private evidence. Ask performs one isolated no-tool answer and
    persists the paired user/assistant records atomically. Steer performs only
    scope classification: clarification records stop there, while compatible
    instructions remain queued until a later implementation repair assembles
    them in creation order under the confirmed charter. Persist an applied
    marker only with the implementation result that consumed queued steering.
15. For Seahorse context, validate distinct canonical per-agent databases,
    construct and bootstrap every engine under one context-aware private guard,
    and only then stage fresh retrieval wrappers/factories for one all-agent
    transaction. Mark the context manager committed immediately after the tool
    transaction, return it before runtime admission resumes, and let only that
    manager close engines. On any precommit error or cancellation, close the
    complete candidate and install legacy context without a `short_*` surface.
16. For recursion and install-skill, finish ordinary shared-tool registration,
    resolve one process lock per exact/resolved workspace identity, then snapshot
    sorted agents before recursion entries exist. Build root wrappers and frozen
    per-owner factories in the fixed `install_skill`, `spawn`, `subagent`,
    `spawn_status`, `delegate` order. Stage sorted distinct registries once and
    run one all-agent transaction. Emit unknown-declaration warnings only after
    commit; on precommit failure publish none, while a malformed postcommit
    admission projection is diagnostic only because the maps are already live.
17. For each SubTurn, retain the runtime generation and one short immediate-
    parent tool-source lease before asynchronous launch. In the child path,
    acquire one configured execution slot before consuming that source. Resolve
    central target authorization and meet the parent,
    target, explicit name selector, inherited profile, and child-eligibility
    sets before constructing any wrapper. Instantiate the sorted exact roots
    for one scoped turn owner, attach the child, then release the source lease;
    release the execution slot after `runTurn`. Provider definitions, prompts,
    hook rewrites, execution, and native search consume the resulting custom/
    off profile. At terminal claim, stop construction admission and boundedly
    drain prior leases. A successful drain applies descendant and Git policy,
    closes strict tools then the ephemeral session, commits status, emits the
    event, and removes the exact active pointer. A timeout commits error and
    leaves cleanup pending until the last retained construction release closes
    those resources synchronously.
18. For async spawn, finish validation and synchronous runtime/source
    preparation, snapshot the exact direct child runner and source origin, then
    ask the paired owner-local manager to insert `running` and allocate its ID.
    Launch only through that manager, with the direct SubTurn set to
    `Async:false` and `Critical:true`; the manager goroutine, not the SubTurn
    compatibility channel, supplies asynchronous execution. The runner commits
    one terminal status, passes that task ID/status to the sole callback outside
    the lock, and releases prepared ownership after the callback attempt. Status
    list and ID lookup project detached records through the same exact-origin
    predicate.
19. For tracked-spawn completion, filter and bound the callback result, combine
    the source parent turn and manager-local task ID, and atomically offer one
    envelope to the exact snapshotted named-session mailbox. Treat identical
    replay as already accepted and conflict or invalid admission as orphaned.
    Let an eligible active turn claim it at a result checkpoint; otherwise
    reserve exactly one continuation, acquire a fresh coherent runtime
    generation, and resolve only the captured named agent/session. Mark the
    claim terminal before provider work and publish at most one returned
    response. Never retry a claimed envelope or route it through raw callback
    output, synthetic system ingress, the default session, or the direct
    `Async:true` SubTurn channel.

20. Treat `update_plan` as a reserved native identity until the coding-task
    ledger exists: omit it from every adapted provider projection and callable
    prompt calculation, reject model-authored or hook-renamed calls through the
    ordinary skipped-tool path, and keep direct registry/workflow execution
    unchanged.

## Cross-Feature Behavior

Routing selects the target agent before this feature builds candidates. Session
memory supplies history and stores results. Tool execution, MCP, skills, hooks,
and security policies can alter the visible tool set or execution outcome.
Runtime events report each major step. Threads can contribute a policy prompt
that lets the main chat become or join a thread only after configured routing
thresholds are satisfied.
The optional Seahorse context strategy consumes Session Memory's strict startup
snapshots and owns the per-agent SQLite engine lifetime. Agent Conversations
owns atomic `short_grep`/`short_expand` factory publication, registry allowlist
admission, selected-owner wrapper isolation, and reload/shutdown ordering; Tool
Execution supplies the generic factory and multi-registry transaction rules.
Agent Conversations owns the exact recursion enablement matrix, frozen
source-agent policy, owner-local manager/spawner service, workspace lock
identity, and startup/reload behavior. Skills owns the borrowed-lock execution
contract for `install_skill`, while Tool Execution supplies the prototype
factory, strict owner service cache, compatibility leases, and atomic
multi-registry transaction. Agent Conversations also owns the activated child
authority meet and turn-resource lifecycle; Tool Execution owns the generic
selected-construction, close/quarantine, and async wrapper contracts it uses.
Workflow agent steps normally reuse this same turn execution path, including
provider prompt cache keys, tool iteration limits, and final message
persistence. The exact `history: read_only` profile instead consumes Session
Memory's immutable existing-session snapshot and the isolated decision path in
`FR-AGENT-020`. A compiler-private workflow captures that snapshot before its
run exists and owns persistence, retry/resume reuse, and every public
observation projection; the agent layer owns exact validation and the
pseudonymous provider identity used by all later calls. Session Memory owns
complete locator enumeration, frozen-reference rewriting, strict `FrozenSet`
validation, and materialization; Tool Execution owns the optional bounded live
`media://` snapshot reader used only during capture. The agent layer composes
those primitives but never retains or reacquires live media authority after the
private root exists. Managed workflow agent steps can additionally run hidden
no-history child turns with scoped prompts, per-child model and reasoning-effort
overrides, and tool disabling while preserving the same provider resolution.
Repository review composes this path through private call-admission and usage
observer callbacks. Agent execution checks admission before each concrete or
streaming provider attempt, attributes every non-nil response to the actual
selected model, returns a detached usage snapshot to the trusted workflow
caller, and stops further calls when admission or usage persistence fails; it
does not persist review budgets or campaign statistics itself.
The `session: ephemeral` workflow profile instead uses the stateless
side-question path in `FR-AGENT-021`: supplied workflow context is the entire
conversation context, account selection has no session-affinity key, and the
request-local identity cannot become session, cache, router, event, or output
state. Workflows own validation and fixed audit markers; agent execution
optimization owns preservation of this profile across managed and repair calls.
Git workspaces are allocated through the registered tool during a turn and are
released or reconciled by the shared turn-finalization path, while checkout
inventory and retention behavior are owned by the git workspaces feature.
The development-workspace implementation service is the sole caller of the
local-repair profile in `FR-AGENT-023`: Git Workspaces owns reservation
identity and postflight truth, Tool Execution owns the sequential suppressed
loop and confined primitives, and Security owns the authority boundary. The
agent layer resolves one exact configured agent under the caller-held runtime
generation, binds one concrete provider/model without fallback or affinity,
and returns no commit, validation, push, publication, merge, or lifecycle
capability. Review, scope audit, and completion audit use the separate
development-workspace isolated structured-AI adapter rather than the removed local
review worker.
The development-workspace service owns durable conversation ordering,
candidate evidence, scope classification, notification projection, and repair
consumption. Agent Conversations owns the isolated no-tool model profile and
the edit-confined repair loop; a chat message alone grants neither profile's
tools nor lifecycle authority.
Account routers plug into the account-selection step: the turn loop expands the
router to concrete candidates, can reselect after context compression, and
records fallback outcomes without changing provider prompt serialization.
Model routers independently choose only configured aliases. Pico chat supplies
`account_ref` plus alias-valued `model_name`; these turn-scoped selections do not
rewrite `model_list[]`, `model_aliases[]`, `account_routers[]`, or
`model_routers[]`.
[Chat channels](chat-channels.md) create the opaque turn-UX identity and own the
provider-specific typing, reaction, placeholder, and generation-pinned callback
implementations. This feature carries that identity through turn ownership,
steering, streaming, tools, and outbound delivery and requests only exact
transitions. [Durable external event automation](event-automation.md) owns
admission and event normalization; the agent preserves its process-local
metadata but does not persist or reinterpret those trust facts.

## Failure And Edge Cases

- Missing aliases fail with exactly `no model configured`; unknown aliases,
  disabled/missing accounts, and unsupported alias/account combinations fail
  before a provider request. Provider defaults and raw model fallbacks are not
  recovery paths.
- Missing GitHub Copilot credentials fail before provider execution, while
  local bridge Copilot entries continue to report local transport failures.
- Codex reset lookup or redemption failure preserves the original
  fallback-eligible usage-limit error, except that caller cancellation and
  deadline errors remain caller-visible. Generic rate limits, workspace spend
  controls, additional-model-only limits, and zero-credit accounts never spend
  a reset.
- Tool lookup misses produce a tool-skipped result instead of a panic.
- Iteration limits stop repeated tool-call loops.
- A SubTurn rejects nil/typed-nil, blank, panicking, unknown, ambiguous, or
  unavailable explicit selectors and unauthorized or missing targets before
  factory, event, or provider effects. Implicit inheritance silently drops
  legacy/root-only and depth-ineligible entries. Factory failure closes prior
  products and publishes no child; terminal close failure changes the child to
  error and quarantines failed leases without replacing an original panic.
- Native search is enabled only for frozen child `web_search` authority and an
  actually selected supporting provider. Unsupported fallbacks retain an
  authorized physical client tool. Without that physical root, only the exact
  statically proven configured provider set may authorize pseudo-only search;
  implicit unproven search is omitted, an explicit selector is unavailable,
  and a pure unsupported hook rewrite fails unless the hook narrows search off.
- Spawn preparation failure returns no ID and creates no record. After launch,
  error results, nil results, and panic become failed records; cancellation and
  deadline errors become canceled records. A scoped caller sees no originless
  or foreign record, and foreign exact-ID lookup is indistinguishable from a
  missing task. No-context programmatic inspection remains an explicit
  compatibility surface rather than model-facing authorization.
- A tracked completion with a missing task identity, invalid exact route, nil
  result, conflicting composite identity, or full mailbox is orphaned and is
  never rerouted through direct user output or synthetic system ingress. An
  identical callback replay is suppressed. Once a turn claims an envelope,
  cancellation, provider/persistence/finalization failure, or response
  publication failure does not requeue it; this preserves at-most-once
  visibility but does not promise provider execution or delivery exactly once.
  Process exit discards queued and deduplication state. A reload continuation
  must acquire a fresh coherent runtime generation and fails closed if the
  captured named agent/session no longer resolves; it never selects `main` as a
  fallback.
- An allowed recursion/install catalog collision or transaction failure exposes
  none of the candidate entries in any agent. A denied entry remains absent and
  leaves an unrelated incumbent untouched. Startup records the shared failure
  on every agent; reload keeps the current generation. A malformed admission
  projection after commit is logged and cannot roll back already published maps.
- Workspace lock aliasing is a process-local construction snapshot. It coalesces
  exact cleaned absolute identities, successfully resolved symlinks, and
  existing `os.SameFile` aliases; it does not promise an OS lock, cross-process
  serialization, or protection against a later adversarial namespace swap.
- Seahorse construction falls back to legacy context without `short_*` entries
  when database paths alias or any private engine/bootstrap/transaction step
  fails. A committed context manager is never closed by a malformed detached
  admission result, selected-owner cleanup, or postcommit panic. Reload
  cancellation closes only the incomplete B candidate and never resumes with a
  mixed A/B retrieval surface.
- Local repair fails before its first provider call when its request, concrete
  provider/model, limits, or exact workspace pin is invalid. A provider panic,
  nil response, conflicting tool-call encoding, malformed or oversized
  arguments, confinement failure, cancellation, or postflight mismatch ends
  the run without fallback or implicit release; edits already completed remain
  in the locked checkout for controller inspection.
- Trusted controller workspace access fails closed when the agent loop or its
  workspace manager is nil, when the concrete manager is typed nil, or when a
  test or extension supplied only another interface implementation. It never
  falls back to exposing that alternate implementation, and callers remain
  responsible for retaining the runtime-generation lease across use.
- Local review fails before or during its sole provider request for a stale or
  invalid agent generation, missing concrete model, blank/oversized context,
  provider construction/cancellation/failure, tool call, malformed JSON,
  unknown outcome/severity, invalid field bounds, passing findings, or a
  changes-required result without findings. It returns no partial result or raw
  provider/configuration/context diagnostic and never falls back to an ordinary
  turn, history, cache, tools, hooks, MCP, workflows, or another agent.
- Development Ask/Steer fails closed on a stale conversation count, stale or
  unavailable candidate fence, invalid mode/content/request ID, unavailable
  isolated runner, malformed schema output, or concurrent aggregate mutation.
  A failed classifier does not queue steering; a scope-changing request does
  not enter a repair prompt; and queued steering does not mutate the checkout
  until an explicitly started implementation run reaches its repair boundary.
- An isolated read-only decision fails before provider execution when its
  caller cannot supply an exact existing-session snapshot. A provider-authored
  tool call is an error even though no tool definitions were offered; it is not
  ignored or executed. Hooks and MCP are deliberately absent from this profile,
  and a concurrent interactive append cannot change the already-frozen prompt
  or be overwritten when the decision finishes.
- A compiler-private decision also fails before provider execution for a
  noncanonical or mismatched agent, a nonblank live session, tools other than
  `none`, a corrupt snapshot, a stale expected revision, an owner/revision mismatch, or a missing frozen
  value. Capture fails as one unit if any structured media locator cannot be
  frozen; decode or execution fails if the persisted `FrozenSet`, frozen
  reference closure, metadata, bound, or digest is invalid. Missing or changed
  runtime-only prompt/tool-call metadata is a revision mismatch rather than a
  lossy restart. Revision validation precedes materialization, and neither path
  falls back to live session or media lookup after capture. Raw session key,
  inbound delivery, session scope, frozen references/set, materialized data,
  and raw capture errors do not enter cache/account identity, provider options,
  shared diagnostics, or public output; cancellation and provider-tool-call
  rejection remain normal.
- An ordinary live turn targeting an existing structured `review` session
  fails before metadata, transcript, provider, or context-manager mutation;
  Seahorse also skips direct ingest of that scope as defense in depth.
- A stateless side-question fails closed if its caller supplies incompatible
  history, cache, or tool authority; it never falls back to the selected
  agent's default session or context. Provider-authored tool calls remain
  errors, and the request-local identity is discarded without entering logs,
  events, outputs, session state, prompt cache, or account-router affinity.
- Media too large for configured limits is rejected before provider execution.
- Direct `Async:true` child turns that cannot use their compatibility channel
  report orphan or failed status. Tracked spawn uses the separate
  composite-ID envelope and failure policy in `FR-AGENT-040`.
- Turn terminal state is immutable and exactly once. Hard abort and panic use
  the captured restore point as the only rollback source; external interrupt
  APIs do not mutate session history. Failed or panicked parents cancel even
  critical descendants, while only critical descendants may survive a
  completed parent.
- A reload waits for turns and retained asynchronous work from the old
  generation. Nested subturn work borrows the retained marker and cannot block
  behind the pause that is waiting for its parent generation to drain.
- Retired agent cleanup attempts both tool and session closure. A cleanup error
  is reported and never makes the old registry live again; successfully closed
  factory-backed sources release global pointer leases before borrowed retired
  services close.
- Background work created by a provisional or cached config waits at the same
  gate and fails closed if that exact config generation is not active when
  admission resumes.
- A message routed before its worker semaphore is available retains that route
  generation through placeholder replacement and turn completion. A stale
  summarizer cannot retarget the same agent/session key in a replacement
  workspace.
- Runtime events emitted by provisional replacement services have no workflow
  subscription and cannot be replayed against restored workflows after
  rollback. Workflow enable/disable reloads synchronously create or remove
  exactly one subscription.
- If buffered output is rejected, no-output cleanup removes the exact
  typing/reaction/placeholder generation. If it is accepted, only exact typing
  is stopped early because channel pre-send still owns the matching reaction
  and placeholder.
- A queued steering owner that panics or abandons setup is rescued with its
  detached inbound identity; a competing live owner wins without surfacing an
  error, and cross-chat steering cleans only the secondary chat's exact UX.
- A legacy channel manager cannot express exact ownership, so cleanup makes one
  bounded detached best-effort chat-scoped call. Missing optional rebind support
  is a no-op, and missing turn-scoped streaming support uses the original
  streamer lookup.
- Cloning an inbound snapshot must not share its maps, occurrence-time pointer,
  or attachment backing slice with the producer. Event and transport-trust
  fields remain process-local even when copied to an outbound context.
- A dirty capability draft is never silently replaced by navigation, refresh,
  a concurrent-write conflict, or a failed reload. Confirmed discard and
  explicit latest-version reload are the only destructive draft transitions.
- Hidden or offline browser tabs, a stopped gateway, an operator pause, and an
  activity request failure stop polling without clearing retained rows. A
  process-generation reset starts a new row window and is shown to the
  operator.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-AGENT-024` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/repository_model_evaluation_workflows_test.go](../../pkg/workflows/repository_model_evaluation_workflows_test.go) |
| `FR-AGENT-025` | [pkg/agent/controller_local_review_test.go](../../pkg/agent/controller_local_review_test.go), [pkg/agent/controller_local_review.go](../../pkg/agent/controller_local_review.go) |
| `FR-AGENT-001`, `FR-AGENT-002`, `FR-AGENT-006`, `FR-AGENT-008` | [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/agent/context_test.go](../../pkg/agent/context_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/pipeline_streaming_test.go](../../pkg/agent/pipeline_streaming_test.go), [pkg/agent/thinking_test.go](../../pkg/agent/thinking_test.go) |
| `FR-AGENT-003` | [pkg/agent/model_resolution_test.go](../../pkg/agent/model_resolution_test.go), [pkg/agent/account_router_test.go](../../pkg/agent/account_router_test.go), [pkg/config/voice_selection_test.go](../../pkg/config/voice_selection_test.go), [pkg/providers/factory_test.go](../../pkg/providers/factory_test.go), [pkg/providers/fallback_test.go](../../pkg/providers/fallback_test.go), [pkg/providers/oauth/codex_provider_test.go](../../pkg/providers/oauth/codex_provider_test.go) |
| `FR-AGENT-004`, `FR-AGENT-005` | [pkg/agent/pipeline_execute_test.go](../../pkg/agent/pipeline_execute_test.go), [pkg/agent/error_format_test.go](../../pkg/agent/error_format_test.go), [pkg/tools/registry_test.go](../../pkg/tools/registry_test.go) |
| `FR-AGENT-007` | [pkg/agent/subturn_test.go](../../pkg/agent/subturn_test.go), [pkg/tools/subagent_tool_test.go](../../pkg/tools/subagent_tool_test.go), [pkg/tools/spawn_status_test.go](../../pkg/tools/spawn_status_test.go) |
| `FR-AGENT-009` | [cmd/picoclaw/main_test.go](../../cmd/picoclaw/main_test.go), [cmd/picoclaw/internal/agent/command_test.go](../../cmd/picoclaw/internal/agent/command_test.go), [cmd/picoclaw/internal/model/command_test.go](../../cmd/picoclaw/internal/model/command_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-AGENT-010` | [pkg/agent/reasoning_effort_test.go](../../pkg/agent/reasoning_effort_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go), [pkg/providers/openai_compat/provider_test.go](../../pkg/providers/openai_compat/provider_test.go), [pkg/providers/azure/provider_test.go](../../pkg/providers/azure/provider_test.go), [pkg/providers/oauth/codex_provider_test.go](../../pkg/providers/oauth/codex_provider_test.go) |
| `FR-AGENT-011` | [pkg/providers/promptir/conversion_test.go](../../pkg/providers/promptir/conversion_test.go), [pkg/providers/common/common_test.go](../../pkg/providers/common/common_test.go), [pkg/providers/openai_responses_common/responses_common_test.go](../../pkg/providers/openai_responses_common/responses_common_test.go), [pkg/tokenizer/estimator_test.go](../../pkg/tokenizer/estimator_test.go) |
| `FR-AGENT-012` | [pkg/agent/pipeline_llm_adaptation_test.go](../../pkg/agent/pipeline_llm_adaptation_test.go), [pkg/agent/instance_test.go](../../pkg/agent/instance_test.go) |
| `FR-AGENT-013` | [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/agent/runtime_event_logger_test.go](../../pkg/agent/runtime_event_logger_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go) |
| `FR-AGENT-014` | [pkg/providers/oauth/codex_rate_limit_reset_test.go](../../pkg/providers/oauth/codex_rate_limit_reset_test.go) |
| `FR-AGENT-015` | [pkg/agent/agent_turn_ux_test.go](../../pkg/agent/agent_turn_ux_test.go), [pkg/agent/steering_test.go](../../pkg/agent/steering_test.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/channels/base_test.go](../../pkg/channels/base_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go) |
| `FR-AGENT-016` | [pkg/agent/channel_manager_compat_test.go](../../pkg/agent/channel_manager_compat_test.go), [pkg/agent/pipeline_streaming_test.go](../../pkg/agent/pipeline_streaming_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go) |
| `FR-AGENT-017` | [pkg/agent/turn_context_test.go](../../pkg/agent/turn_context_test.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/agent/steering_test.go](../../pkg/agent/steering_test.go), [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go) |
| `FR-AGENT-018` | [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts), [web/frontend/src/components/agent/agents/agents-page.test.tsx](../../web/frontend/src/components/agent/agents/agents-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-AGENT-019` | [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go), [web/backend/api/agent_capabilities_cas_test.go](../../web/backend/api/agent_capabilities_cas_test.go), [web/backend/api/agent_capabilities_replace_linux_test.go](../../web/backend/api/agent_capabilities_replace_linux_test.go), [pkg/agent/activity_test.go](../../pkg/agent/activity_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts), [web/frontend/src/components/agent/agents/agent-capabilities-panel.test.tsx](../../web/frontend/src/components/agent/agents/agent-capabilities-panel.test.tsx), [web/frontend/src/components/agent/agents/agent-activity-panel.test.tsx](../../web/frontend/src/components/agent/agents/agent-activity-panel.test.tsx), [web/frontend/src/routes/agent/-agents-route.test.tsx](../../web/frontend/src/routes/agent/-agents-route.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-AGENT-020` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/context_seahorse_test.go](../../pkg/agent/context_seahorse_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go) |
| `FR-AGENT-021` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go) |
| `FR-AGENT-022` | [pkg/agent/agent_message_review_test.go](../../pkg/agent/agent_message_review_test.go), [pkg/agent/agent_scope_admission_race_test.go](../../pkg/agent/agent_scope_admission_race_test.go), [pkg/agent/context_seahorse_test.go](../../pkg/agent/context_seahorse_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/agent_message.go](../../pkg/agent/agent_message.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go) |
| `FR-AGENT-023` | [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/agent/local_repair_prompt_test.go](../../pkg/agent/local_repair_prompt_test.go), [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go), [pkg/agent/local_repair_factory.go](../../pkg/agent/local_repair_factory.go), [pkg/agent/local_repair_factory_test.go](../../pkg/agent/local_repair_factory_test.go), [pkg/agent/git_workspace.go](../../pkg/agent/git_workspace.go), [pkg/agent/git_workspace_controller_test.go](../../pkg/agent/git_workspace_controller_test.go), [pkg/tools/toolloop_test.go](../../pkg/tools/toolloop_test.go), [pkg/tools/apply_patch_test.go](../../pkg/tools/apply_patch_test.go) |
| `FR-AGENT-026` | [pkg/prworkspace/conversation.go](../../pkg/prworkspace/conversation.go), [pkg/prworkspace/implementation.go](../../pkg/prworkspace/implementation.go), [web/frontend/src/api/development-workspaces.test.ts](../../web/frontend/src/api/development-workspaces.test.ts), [web/frontend/src/components/development-workspaces/development-chat.test.tsx](../../web/frontend/src/components/development-workspaces/development-chat.test.tsx) |
| `FR-AGENT-027` | [pkg/agent/context_manager_test.go](../../pkg/agent/context_manager_test.go), [pkg/agent/context_legacy.go](../../pkg/agent/context_legacy.go), [pkg/agent/steering.go](../../pkg/agent/steering.go) |
| `FR-AGENT-028` | [pkg/agent/turn_profile_policy_test.go](../../pkg/agent/turn_profile_policy_test.go), [pkg/agent/turn_profile_test.go](../../pkg/agent/turn_profile_test.go), [pkg/agent/turn_profile_policy.go](../../pkg/agent/turn_profile_policy.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go) |
| `FR-AGENT-029` | [pkg/agent/turn_supervisor_test.go](../../pkg/agent/turn_supervisor_test.go), [pkg/agent/subturn_test.go](../../pkg/agent/subturn_test.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go), [pkg/agent/turn_state.go](../../pkg/agent/turn_state.go), [pkg/agent/steering.go](../../pkg/agent/steering.go), [pkg/agent/subturn.go](../../pkg/agent/subturn.go) |
| `FR-AGENT-030` | [pkg/agent/workflow_authoring.go](../../pkg/agent/workflow_authoring.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/tools/registry.go](../../pkg/tools/registry.go) |
| `FR-AGENT-031` | [pkg/agent/instance.go](../../pkg/agent/instance.go), [pkg/agent/instance_test.go](../../pkg/agent/instance_test.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/agent_mcp_test.go](../../pkg/agent/agent_mcp_test.go), [pkg/tools/registry.go](../../pkg/tools/registry.go) |
| `FR-AGENT-032` | [pkg/agent/tool_factory_catalog.go](../../pkg/agent/tool_factory_catalog.go), [pkg/agent/tool_factory_catalog_test.go](../../pkg/agent/tool_factory_catalog_test.go), [pkg/agent/instance.go](../../pkg/agent/instance.go), [pkg/agent/instance_test.go](../../pkg/agent/instance_test.go), [pkg/agent/agent_init.go](../../pkg/agent/agent_init.go), [pkg/agent/agent_test.go](../../pkg/agent/agent_test.go), [pkg/agent/threads_tool_scope_test.go](../../pkg/agent/threads_tool_scope_test.go), [pkg/tools/codex_compat.go](../../pkg/tools/codex_compat.go), [pkg/tools/codex_compat_test.go](../../pkg/tools/codex_compat_test.go), [pkg/tools/hardware](../../pkg/tools/hardware), [pkg/audio/tts/openai_tts.go](../../pkg/audio/tts/openai_tts.go), [pkg/audio/tts/mimo_tts.go](../../pkg/audio/tts/mimo_tts.go), [pkg/audio/tts/tts_test.go](../../pkg/audio/tts/tts_test.go) |
| `FR-AGENT-033` | [pkg/agent/agent_mcp.go](../../pkg/agent/agent_mcp.go), [pkg/agent/agent_mcp_runtime_test.go](../../pkg/agent/agent_mcp_runtime_test.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/agent_inject.go](../../pkg/agent/agent_inject.go), [pkg/agent/reload_media_generation_test.go](../../pkg/agent/reload_media_generation_test.go), [pkg/agent/runtime_gate.go](../../pkg/agent/runtime_gate.go) |
| `FR-AGENT-035` | [pkg/agent/agent_mcp.go](../../pkg/agent/agent_mcp.go), [pkg/agent/agent_mcp_catalog.go](../../pkg/agent/agent_mcp_catalog.go), [pkg/agent/agent_mcp_catalog_test.go](../../pkg/agent/agent_mcp_catalog_test.go), [pkg/agent/agent_mcp_catalog_runtime_test.go](../../pkg/agent/agent_mcp_catalog_runtime_test.go), [pkg/agent/agent_mcp_runtime_test.go](../../pkg/agent/agent_mcp_runtime_test.go), [pkg/agent/prompt_contributors.go](../../pkg/agent/prompt_contributors.go), [pkg/agent/prompt_test.go](../../pkg/agent/prompt_test.go), [pkg/agent/instance.go](../../pkg/agent/instance.go), [pkg/tools/registry_transaction.go](../../pkg/tools/registry_transaction.go), [pkg/tools/mcp_factory.go](../../pkg/tools/mcp_factory.go), [pkg/tools/search_tool.go](../../pkg/tools/search_tool.go) |
| `FR-AGENT-036` | [pkg/agent/context_seahorse.go](../../pkg/agent/context_seahorse.go), [pkg/agent/context_seahorse_catalog.go](../../pkg/agent/context_seahorse_catalog.go), [pkg/agent/context_seahorse_catalog_test.go](../../pkg/agent/context_seahorse_catalog_test.go), [pkg/agent/context_seahorse_catalog_runtime_test.go](../../pkg/agent/context_seahorse_catalog_runtime_test.go), [pkg/agent/context_seahorse_test.go](../../pkg/agent/context_seahorse_test.go), [pkg/agent/context_manager_test.go](../../pkg/agent/context_manager_test.go), [pkg/agent/context_seahorse_unsupported.go](../../pkg/agent/context_seahorse_unsupported.go), [pkg/seahorse/tool_factory.go](../../pkg/seahorse/tool_factory.go), [pkg/seahorse/tool_factory_test.go](../../pkg/seahorse/tool_factory_test.go), [pkg/tools/registry_transaction.go](../../pkg/tools/registry_transaction.go), [pkg/tools/registry_factory.go](../../pkg/tools/registry_factory.go) |
| `FR-AGENT-037` | [pkg/agent/recursion_tool_factory_catalog.go](../../pkg/agent/recursion_tool_factory_catalog.go), [pkg/agent/recursion_tool_factory_catalog_test.go](../../pkg/agent/recursion_tool_factory_catalog_test.go), [pkg/agent/agent_init.go](../../pkg/agent/agent_init.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/tools/integration/skills_install_test.go](../../pkg/tools/integration/skills_install_test.go), [pkg/tools/registry_transaction.go](../../pkg/tools/registry_transaction.go), [pkg/tools/registry_transaction_test.go](../../pkg/tools/registry_transaction_test.go) |
| `FR-AGENT-038` | [pkg/agent/subturn_effective_tools.go](../../pkg/agent/subturn_effective_tools.go), [pkg/agent/subturn_effective_tools_test.go](../../pkg/agent/subturn_effective_tools_test.go), [pkg/agent/subturn.go](../../pkg/agent/subturn.go), [pkg/agent/subturn_test.go](../../pkg/agent/subturn_test.go), [pkg/agent/turn_state.go](../../pkg/agent/turn_state.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go), [pkg/agent/turn_supervisor_test.go](../../pkg/agent/turn_supervisor_test.go), [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/tools/spawn.go](../../pkg/tools/spawn.go) |
| `FR-AGENT-039` | [pkg/tools/subagent.go](../../pkg/tools/subagent.go), [pkg/tools/subagent_manager_test.go](../../pkg/tools/subagent_manager_test.go), [pkg/tools/spawn.go](../../pkg/tools/spawn.go), [pkg/tools/spawn_test.go](../../pkg/tools/spawn_test.go), [pkg/tools/spawn_status.go](../../pkg/tools/spawn_status.go), [pkg/tools/spawn_status_test.go](../../pkg/tools/spawn_status_test.go), [pkg/agent/recursion_tool_factory_catalog_test.go](../../pkg/agent/recursion_tool_factory_catalog_test.go), [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go) |
| `FR-AGENT-040` | [pkg/agent/subagent_result_mailbox.go](../../pkg/agent/subagent_result_mailbox.go), [pkg/agent/subagent_result_delivery_test.go](../../pkg/agent/subagent_result_delivery_test.go), [pkg/agent/subagent_result_pipeline_test.go](../../pkg/agent/subagent_result_pipeline_test.go), [pkg/agent/subagent_result_runtime_test.go](../../pkg/agent/subagent_result_runtime_test.go), [pkg/agent/subagent_result_handoff_test.go](../../pkg/agent/subagent_result_handoff_test.go), [pkg/agent/subagent_result_policy_test.go](../../pkg/agent/subagent_result_policy_test.go), [pkg/agent/pipeline_execute.go](../../pkg/agent/pipeline_execute.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go), [pkg/agent/turn_state.go](../../pkg/agent/turn_state.go), [pkg/agent/eventbus_test.go](../../pkg/agent/eventbus_test.go), [pkg/agent/recursion_tool_factory_catalog_test.go](../../pkg/agent/recursion_tool_factory_catalog_test.go), [pkg/tools/subagent_manager_test.go](../../pkg/tools/subagent_manager_test.go), [pkg/tools/spawn_test.go](../../pkg/tools/spawn_test.go) |

| `FR-AGENT-041` | [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go), [pkg/agent/pipeline_execute.go](../../pkg/agent/pipeline_execute.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/agent/pipeline_llm_adaptation_test.go](../../pkg/agent/pipeline_llm_adaptation_test.go), [pkg/agent/p008_codex_schema_parity_test.go](../../pkg/agent/p008_codex_schema_parity_test.go), [web/backend/api/agent_capabilities.go](../../web/backend/api/agent_capabilities.go), [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go) |

| `FR-AGENT-034` | [web/backend/api/collection_apis_test.go](../../web/backend/api/collection_apis_test.go), [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts), [web/frontend/src/routes/agent/-agents-route.test.tsx](../../web/frontend/src/routes/agent/-agents-route.test.tsx), [web/frontend/tests/collection-visual.spec.ts](../../web/frontend/tests/collection-visual.spec.ts) |

## Implementation Anchors

- [pkg/agent/pipeline.go](../../pkg/agent/pipeline.go)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/agent/agent.go](../../pkg/agent/agent.go)
- [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go)
- [pkg/agent/context_seahorse.go](../../pkg/agent/context_seahorse.go)
- [pkg/agent/context_seahorse_catalog.go](../../pkg/agent/context_seahorse_catalog.go)
- [pkg/agent/recursion_tool_factory_catalog.go](../../pkg/agent/recursion_tool_factory_catalog.go)
- [pkg/agent/subturn_effective_tools.go](../../pkg/agent/subturn_effective_tools.go)
- [pkg/seahorse/tool_grep.go](../../pkg/seahorse/tool_grep.go)
- [pkg/seahorse/tool_expand.go](../../pkg/seahorse/tool_expand.go)
- [pkg/seahorse/tool_factory.go](../../pkg/seahorse/tool_factory.go)
- [pkg/agent/channel_manager_compat.go](../../pkg/agent/channel_manager_compat.go)
- [pkg/agent/steering.go](../../pkg/agent/steering.go)
- [pkg/agent/turn_context.go](../../pkg/agent/turn_context.go)
- [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go)
- [pkg/agent/turn_state.go](../../pkg/agent/turn_state.go)
- [pkg/agent/subturn.go](../../pkg/agent/subturn.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
- [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go)
- [pkg/agent/git_workspace.go](../../pkg/agent/git_workspace.go)
- [pkg/prworkspace/conversation.go](../../pkg/prworkspace/conversation.go)
- [pkg/agent/runtime_gate.go](../../pkg/agent/runtime_gate.go)
- [pkg/providers/factory.go](../../pkg/providers/factory.go)
- [web/backend/api/agents.go](../../web/backend/api/agents.go)
- [web/backend/api/agent_capabilities.go](../../web/backend/api/agent_capabilities.go)
- [web/frontend/src/components/agent/agents](../../web/frontend/src/components/agent/agents)
