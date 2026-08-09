# Durable External Event Automation

## Feature ID

`FR-EVENT-AUTOMATION`

## Behavior Summary

Durable external event automation accepts normalized notifications from
GitHub, chat, email, and generic webhooks through a restart-safe source-neutral
inbox. It deduplicates provider deliveries, protects stored payloads through
size limits and recursive redaction, matches explicit `on.event` workflow
filters, persists one deterministic dispatch per event/workflow pair, and
reconciles workflow runs after process failure without repeating an interrupted
run.

The current stage opens the opt-in inbox, runs routing/dispatch workers with the
gateway lifecycle, accepts authenticated Standard Webhooks and native GitHub
deliveries on the existing shared gateway listener, and can opt existing Delta
Chat email channel instances into durable `message.received` admission. It also
offers an explicitly installed GitHub issue-triage workflow that keeps its AI
classifier separate from the declared GitHub comment action. Authenticated
operator API and CLI surfaces inspect events and dispatches and create explicit
additive replays through the exact live gateway store generation. An
authenticated responsive dashboard provides the same bounded inspection,
explicit payload reveal, and deliberate replay operations without opening
storage independently. Its global dispatch view binds payload-free
event/workflow/status filters and exact dispatch selection to the URL and links
each dispatch to its event, workflow, and run without exposing worker tokens.
Selected dispatch detail loads a payload-free invocation summary automatically.
Full persisted workflow inputs remain behind an explicit load action and render
only after the run, workflow, trusted origin, event, and dispatch bindings match
the selected dispatch.
Workflow runs now persist a separate trusted, payload-free origin for those
relationships: production dispatches carry exact event/dispatch/root-run
identity, event-parity draft tests carry event/root-run identity without a
dispatch, and reusable children or retries retain the original family root.
The workflow dashboard renders those typed links beside independent
cancellation/completion lifecycle fields and never infers provenance from
payload-bearing event or input snapshots.
A separate authenticated event-source manager exposes
the opt-in master switch, storage and redaction policy, secure Standard
Webhooks/GitHub connector lifecycle, and eligible Delta Chat email adapters
through the existing configuration and gateway lifecycle APIs. GitHub
connectors can additionally restrict durable admission to a case-insensitive
owner/repository allowlist and identify one target GitHub user. The manager can
import newline-delimited repository lists locally; authenticated deliveries
outside a non-empty scope are acknowledged without durable insertion, while
admitted deliveries expose bounded pull-request, issue, comment, review,
assignment, review-request, and mention metadata for deterministic workflow
filtering. A GitHub connector may instead poll the authenticated user's
notifications through exact read-only MCP tools, with the same repository scope
and durable event contract, so a private installation does not need public
webhook ingress.
For native GitHub webhooks, that target also identifies a submitted review from
another reviewer on a pull request authored by the configured user. The
normalizer projects explicit author/reviewer comparison facts plus bounded
review and fork-head identity, allowing an ordinary deterministic `on.event`
filter to select inbound review feedback without asking a model to interpret
the raw payload. This is routing metadata only: it creates no development case,
checkout, model call, edit, push, merge, or GitHub action.

After an operator explicitly installs `github-pr-development`, that ordinary
workflow can opt a successful matching run into a separate read-only capture
boundary with the reserved `picoclawDevelopmentCapture: v1` output. Before the
dispatch is acknowledged, the gateway independently re-reads the exact review
database ID and current pull request through the generation-fenced GitHub MCP
reader, then stores one immutable review-level development case with the
current base/head repository, ref, and commit facts. The review body remains
untrusted feedback, not instructions or authority. This capture stage exposes
no UI, PR chat, gate, checkout, model call, edit, commit, push, merge, or GitHub
write action.

The authenticated body supplies canonical numeric database IDs for the base
repository, pull request, and pull author. The provider read independently
returns and exactly matches the pull-author ID. Its current projection omits
repository and pull-request IDs, so capture instead cross-binds those two
signed IDs to the same current object through the canonical lowercase HTTPS
provider origin and exact repository, pull URL, pull number, and base facts.
The origin plus repository and pull IDs identify one private `pdt_` development
thread; the author ID is an immutable invariant rather than part of its key.
Every capture remains a separate `pdc_` case at one transactionally assigned
contiguous thread ordinal. This private grouping is only durable identity for
later orchestration: current chat, repair, list/detail routes, and browser state
stay strictly case-scoped and receive no sibling-case data or new action
authority.

A later own-PR development workbench projects those immutable cases
through exact protected runtime and launcher routes. Its newest-first inbox and
detail view expose only bounded browser-safe pull-request, review, captured
base/head, feedback, and capture-time facts. Every provider state, ref, and SHA
is labelled as the snapshot verified when the case was captured, never as live
GitHub state or action authority. Explicit event replays remain separate visible
cases rather than being collapsed by review identity. Reading, filtering,
selecting, paging, and navigating this view perform no model call, gate,
checkout, repository or provider operation, or durable mutation.

From one selected case, an authenticated user may explicitly append a message
to a separate local development conversation. PicoClaw records that human
message before asking its isolated, tool-free agent to discuss the captured
feedback, then records the bounded answer. The model receives only an explicit
bounded snapshot and recent transcript, has no runtime history or cache, and
cannot inspect a checkout or act on GitHub. Conversation versions and messages
are independent of the immutable capture, so chatting neither reorders the
inbox nor turns historical provider facts into current authority. Attention
gates, checkout, editing, validation, publication, and merge remain later
explicit stages.

The first local-development execution primitive remains internal. A trusted
controller can independently refresh one captured case through the same bounded
GitHub reader, require the pull request and exact review to remain actionable,
and obtain the current fork repository, branch, commit, canonical clone URL,
and an integrity digest of the unchanged review evidence. It may then lend one
already-resolved concrete model target to an edit-only runner. That runner owns
no raw checkout path or release capability: it acquires and postflight-verifies
the exact controller pin, exposes only bounded read/list/edit/apply-patch tools
over repository content, and serializes model-authored mutations. This
foundation creates no development-session, browser, gate, test, Git, commit,
push, merge, or provider-write surface; those remain later controller stages.

An explicitly installed PR-review workflow turns targeted authenticated review
requests into structured local drafts. Review cases, editable/droppable
findings, append-only chat/rephrase history, and an immutable submission outbox
share the event SQLite store. The authenticated `/reviews` workbench keeps the
human in control of every edit and either resolves the case by dropping all
findings or explicitly queues one active-only snapshot. A separate crash-safe
worker alone may publish that snapshot through GitHub's pending-review
protocol after confirming that the pull request still has the reviewed head.
Ambiguous external outcomes are terminal and never blindly retried; the
workbench lets a human reconcile them as visibly submitted or verified absent
without making another GitHub call.
When a review attention policy needs the PR discussion as AI working context,
the review service lazily projects the authoritative SQLite transcript into one
agent-owned, per-case session. That session is a derived internal view with a
stable key across optimistic review versions; it is not exposed by review DTOs
or browser session discovery and is excluded from Seahorse indexing.
A trusted internal launcher can now resolve one frozen global-plus-repository
attention policy for an exact case version and decision point, compile any
ordered mix of working-context AI, isolated AI, deterministic, and zero gates,
and admit at most one private workflow run. A SQLite decision-to-run link and
the workflow run's create-only store boundary are joined inside the same
admission callback, so concurrent or restarted callers converge on the already
linked run before any model, function, or human task can execute. Trusted
operator configuration now persists those policies globally with explicit
repository-local inherit, overlay, replace, or disable overrides. The active
gateway generation resolves an immutable case-insensitive repository catalog,
validates every referenced agent, and exposes a strict revision-fenced
management API. The authenticated review route also provides a structured
visual editor for that complete catalog, including ordered mixed gates and an
effective repository preview, without launching a decision. When this
workbench's outgoing review becomes durably submitted, the same SQLite
transaction records one `review.submitted` attention occurrence. A separate
generation-fenced worker pins that occurrence's first trusted effective policy
before launching it and then converges retries on the same private run or
effect-free no-op result. The submitted case now owns the only browser-safe
attention projection: it maps the validated occurrence, decision link, private
run, and bounded human tasks to lifecycle state and opaque response fences in
the existing PR conversation card. An exact fenced answer resumes only that
private task and then reprojects authoritative state, while generic workflow
surfaces treat the reserved attention workflow as absent.
Durable inputs remain separate from the process-local
[`pkg/events`](../../pkg/events) observability bus: runtime events are
best-effort in-process signals, while external automation events and dispatch
state survive restart.
The application default is doubly inert: event ingress is disabled and both
the concrete account list and model-alias list are empty. Enabling durable
ingress never manufactures a model for workflow agent steps; model-backed
automation must separately configure an explicit account and alias and
otherwise fails before a provider request.

## Reconstruction Notes

- Similarity target: recreate an opt-in `pkg/eventing` package with immutable
  normalized envelopes, an `Inbox` behavior interface, and one portable
  SQLite-backed `Store` that owns inbox, routing, and workflow-dispatch state,
  plus per-GitHub-connector repository admission and target-user projection
  without another listener or store.
- Core types/functions: `Envelope`, `Actor`, `Subject`, envelope normalization,
  validation, and cloning helpers, `Redactor`, `Inbox`, `Store`,
  `Open`/`OpenStore`, store options,
  routing/dispatch claim and completion records, replay records, retention
  results, `config.EventIngressConfig` with effective-default resolution,
  workflow `EventTrigger`/`EventEntityTrigger`, deterministic trigger matching,
  gateway-owned `EventWorkflowRouter`/`EventWorkflowDispatcher` workers,
  `channelmessage.Backend`/`Controller`, and the message-bus inbound admission
  seam. GitHub scope additionally uses
  `GenericWebhookConfig.Repositories`/`TargetUser`,
  `webhook.BackendConfig.ConnectorRepositories`/`ConnectorTargetUsers`, the
  connector runtime admission check, `decodeGitHubAdmissionRequest`,
  `githubResourceAttributes`, bounded GitHub resource/target projection, and
  the event-source repository-file importer. Review working context uses
  `reviews.Service.WithWorkingContext`, `reviews.WorkingContextRuntimeAcquire`,
  `reviews.WorkingContextRequest`, and the gateway's exact-generation agent
  session-store resolver. Review attention launch additionally uses
  `reviews.AttentionLauncher`, its lease-scoped `AttentionPolicySource`, and
  `eventing.ReviewDecisionRunStore`. Persisted attention policy additionally
  uses `config.ReviewsConfig`, `config.ReviewAttentionConfig`,
  `reviews.ConfigAttentionPolicySource`, and the authenticated
  `/api/reviews/attention-policies` configuration projection. Browser policy
  management uses the review route's policy view, its strict lossless JSON
  transport, and the review-attention policy draft/model helpers. Automatic
  outgoing-review attention uses `reviews.AttentionTriggerWorker`,
  `eventing.ReviewAttentionTriggerQueue`, and the launcher's trusted
  policy-pin/launch boundary. Browser attention handoff uses
  `reviews.AttentionBridge`, its case-owned GET/response methods, the protected
  review handler subroutes, and a strict lossless frontend client rendered in
  the existing review conversation card. Own-PR development capture uses
  `prdevelopment.CaptureSink`, `prdevelopment.GitHubVerifier`,
  `eventing.PRDevelopmentCaseStore`, and the workflow dispatcher's ordered
  `SucceededEventRunSinkFanout`. Its separate read-only workbench uses bounded
  `prdevelopment` list/detail projections, the existing schema-v6
  newest-first/repository indexes, exact generation-owned runtime handlers, a
  launcher authority-replacing proxy, and the canonical development view under
  `/reviews`.
- Runtime ordering: resolve disabled-safe config, normalize and validate an
  envelope, enforce the payload limit, redact configured fields, atomically
  insert or return the existing deduplicated event, lease and renew routing,
  create deterministic per-workflow dispatches through the current claim,
  reconcile the deterministic run ID, exclusively create a new run, link its
  dispatch before effects, execute with lease renewal, then retain or replay
  only through explicit store operations. For GitHub, authenticate the exact
  body, decode the signed object, reject secret-bearing delivery/event identity,
  compare signed `repository.full_name` against the immutable normalized
  allowlist, acknowledge a miss without insertion, derive own-PR submitted
  feedback only from the authenticated body plus configured target, and only
  then redact and insert an admitted projection. For an explicitly opted-in
  successful development-capture run, reconcile an existing immutable capture
  first; otherwise bind the exact event/dispatch/run/workflow provenance,
  re-read the pull request and bounded review pages through the exact read-only
  GitHub tool, verify the signed review occurrence against that provider view,
  persist the provider-current pull/fork/head snapshot and review-level
  feedback, and only then acknowledge the dispatch.
- Non-obvious constraints: deduplication is scoped by source and connector;
  duplicate input never replaces the first stored payload; lease ownership is
  fenced against stale workers; stale routing cannot authorize a dispatch;
  routing and dispatch state are distinct; file run creation is exclusive
  across processes; replay points back to its immutable source event; SQLite
  migrations are transactional; disabled ingress opens no database; and targets
  excluded from the repository's SQLite build matrix must compile through an
  unsupported stub instead of acquiring a new platform dependency. Matching is
  deterministic and AI decisions belong inside a matched workflow, not inside
  the router. Default event configuration does not seed a provider account or
  model alias, and event setup never substitutes a provider default for
  model-backed steps. Configured channel admission is synchronous with durable
  insert; Delta Chat advances its provider cursor only after that boundary. An empty
  GitHub repository scope accepts all repositories; matching ignores case and
  list order; ignored delivery IDs intentionally create no deduplication state;
  target facts are routing metadata rather than authorization; the GitHub event
  header remains an unsigned routing hint even though the action and projected
  resource identity come from the authenticated body; notification polling
  does not claim submitted-review parity; and repository and target-user
  strings remain subject to public-identity secret checks. GitHub MCP's current
  `get_reviews` projection does not expose the webhook review node ID, so that
  node ID remains authenticated trigger evidence rather than provider-verified
  identity. Its current review-comment projection does not expose a parent
  review database ID, so inline comment-to-review association is outside this
  review-level capture contract. A development workbench read never refreshes
  those captured facts: `current` review state and pull/head facts mean
  provider-current at capture time only. Browser projections omit
  event/dispatch/run/workflow/connector provenance, target-user and trigger-node
  evidence, and the capture hash. A replay keeps a distinct `pdc_` identity even
  when its provider review matches an earlier visible case. A
  review working-context session is a hidden derived view rather than another
  authority: its key excludes the mutable case version, its full scope and
  history are verified after every materialization, SQLite remains the only
  review transcript, and review-scoped sessions do not enter browser session
  discovery or Seahorse indexing. Review-attention policy authority comes only
  from the operator-owned PicoClaw configuration, never from the checked-out PR
  repository; repository identity is selected from the authoritative case and
  compared without case, while case-colliding configured keys are invalid.
  Browser attention never accepts or returns a private run, task, session,
  policy, workflow, input-hash, or stored-error identity. Its response token is
  a domain-separated digest over the exact server-loaded case-to-task chain,
  not caller-supplied linkage authority, and the review route contains only a
  canonical case ID plus the fixed `focus=chat` affordance.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-EVENT-AUTOMATION-001` | MUST | Configuration is omitted, explicitly disabled, or resolved for an enabled agent workspace. | Omitted ingress is disabled; effective config defaults its database to `<workspace>/eventing/events.db`, retention to 30 days, payload size to 1 MiB, and redaction to the mandatory sensitive-field set. New application defaults also leave `model_list` and `model_aliases` empty, so model-backed workflow steps require a separately configured explicit account and alias. | Resolution returns an independent effective config and does not open or create storage, add an account, or create a model alias. | Non-positive limits receive defaults; relative paths resolve under the workspace, absolute paths are preserved, and exactly `~`, `~/`, or `~\` home prefixes are expanded without treating `~name` as home. Enabling ingress does not select a provider default; a model-backed step without an explicit runnable selection fails through the shared `no model configured` boundary before a provider request. | Existing installations must remain unchanged until durable ingress and any model-backed execution are deliberately enabled. |
| `FR-EVENT-AUTOMATION-002` | MUST | A caller submits an external envelope with source, connector, deduplication key, event type, and JSON-object payload, optionally including actor, subject, occurrence time, attributes, and receipt time. | Normalization returns an immutable deep copy, assigns a stable opaque `ev_` ID and missing receipt time, and canonicalizes timestamps to UTC. | A successful store insert commits the normalized inbox data and pending routing state together. | Missing/oversized/non-UTF-8 identity, entity, attribute, or payload data, excessive/invalid attributes, non-object/trailing/invalid JSON, out-of-range timestamps, invalid caller-supplied event/replay IDs, or self-referential replay lineage fail before mutation; caller-owned payloads, maps, pointers, actors, and subjects cannot mutate stored or returned state. | All connectors need one safe, bounded, source-neutral contract. |
| `FR-EVENT-AUTOMATION-003` | MUST | An envelope is prepared for persistence with a payload byte limit, configured sensitive field names, and optional exact secret values. | Sensitive values and embedded secret strings are recursively replaced with `[REDACTED]` while unrelated JSON types and structure are preserved; actor, subject, and envelope attribute strings receive the same protection, and repeated redaction is idempotent. | Only the redacted deep copy is eligible for durable insertion. | Oversized or invalid JSON is rejected; key matching ignores case and punctuation/underscore/camel-case differences, recognizes sensitive suffixes and explicitly configured punctuation-only keys, and descends through nested objects/arrays. An exact configured secret in a JSON or attribute-map key fails closed rather than leaking or causing a redacted-key collision. The caller's input is never rewritten. | Durable automation must not turn provider secrets or credentials into an unbounded local archive. |
| `FR-EVENT-AUTOMATION-004` | MUST | One or more workers concurrently ingest deliveries with the same non-empty `(source, connector, dedupe_key)`. | Every caller receives the same original event and an observable duplicate indication after exactly one insert. | The first envelope remains authoritative; later duplicates add no inbox/routing state and never overwrite its payload or metadata. | Database contention may wait within the configured SQLite busy timeout, but must not surface as two durable events. | Provider retries and concurrent receivers must be safe. |
| `FR-EVENT-AUTOMATION-005` | MUST | A supported build opens an enabled ingress database for the first time or after restart. | The validated current schema is available with WAL journaling, foreign keys, and connection-local busy handling; an existing current database reopens without losing records. Schema v2 adds an exact workflow-revision binding for newly routed dispatches while preserving readable legacy dispatches with no binding. | Schema versions advance transactionally and the database file is restricted to owner access. | A newer unknown version or current-version database missing required tables, columns, or indexes fails closed; failed migration rolls back its version and partial objects; unsupported targets return `ErrUnsupportedPlatform` and do not create a database. | The inbox must be durable without reducing PicoClaw's existing portability or making pre-v2 intent unreadable. |
| `FR-EVENT-AUTOMATION-006` | MUST | A worker claims routable events with a non-empty worker label, bounded limit, and positive lease duration. | It receives only available pending or expired-lease events, each with a store-generated fresh opaque lease token and deadline. | Claims transition routing state atomically to `claimed`, store the generated token, and increment the attempt count once. | Concurrent claimers cannot own the same live lease; future `available_at` work is skipped; an empty label or invalid claim request mutates nothing. | At-least-once routing needs bounded, restart-recoverable, independently fenced work ownership. |
| `FR-EVENT-AUTOMATION-007` | MUST | A worker transitions routing using the event ID and fresh lease token returned by its claim. | A current token can acknowledge success, nack to pending at an explicit retry time with redacted/bounded error detail, or mark the event dead; stale or foreign tokens receive a typed conflict. | Routing status, availability, cleared lease, sanitized error detail, and update timestamp change atomically without changing the envelope. | A zero/past nack time retries immediately; transition after lease replacement/expiry and duplicate terminal completion cannot clobber newer state. | Per-claim lease fencing prevents slow workers from corrupting recovered work. |
| `FR-EVENT-AUTOMATION-008` | MUST | Routing selects an exact validated workflow byte snapshot and revision for a durable event and calls revision-bound dispatch creation through its current claim. | The store derives stable `dsp_` and `wr_` IDs from the event/workflow pair and returns exactly one pending dispatch, including its first non-empty opaque workflow revision, before execution. | The dispatch and its immutable first workflow-revision binding are committed atomically and remain independent of event routing state. | Repeating the same pair returns the existing dispatch and cannot replace its revision; selecting different workflows creates distinct dispatches; a blank/oversized revision, missing event, or stale routing claim fails; no workflow code is invoked. Legacy unbound dispatches remain readable for fail-closed recovery. | Retries must not create duplicate workflow runs, silently retarget durable intent to edited YAML, or couple routing completion to execution. |
| `FR-EVENT-AUTOMATION-009` | MUST | A worker claims available workflow dispatches, links the expected run ID, and finishes or nacks using the returned dispatch lease token. | Live work is exclusively claimed, expired claimed/running work becomes claimable after restart, a nack schedules pending retry, and the current token can persist `succeeded`, `failed`, or `dead` with redacted/bounded detail. | Dispatch attempts, availability, token/lease fields, `claimed`/`running`/pending/terminal status, sanitized error, link time, and finish time advance atomically. | Future-availability work is skipped; a mismatched run ID, stale token, expired lease, or non-terminal finish status is rejected without mutating newer state. | Workflow delivery needs scheduled recovery, deterministic run linkage, and per-claim fencing guarantees. |
| `FR-EVENT-AUTOMATION-010` | MUST | An operator-layer caller requests replay of an existing durable event. | A new pending event is returned with new `ev_` identity and deduplication identity and a `replay_of` link to the event that was replayed. | The replay adds new inbox/routing state; the source envelope and prior dispatch history remain unchanged. | A missing source fails without mutation; replay itself does not claim routing or launch a workflow; replaying a replay creates another additive record linked to its immediate source. | Operators need auditable reprocessing without rewriting history. |
| `FR-EVENT-AUTOMATION-011` | MUST | An enabled committed event-ingress generation starts, reloads, or reaches its six-hour maintenance interval with a positive retention policy. | Its context-bound retention worker acquires that exact live runtime generation, computes a UTC cutoff from effective retention days, and prunes immediately after activation and periodically thereafter. Each cycle reports removed records, continues after a telemetry-safe failure, and is bounded to 20 batches of 500 rows. | Each batch deletes up to its bounded limit of the oldest eligible `succeeded`/`dead` events and their terminal dispatches transactionally; reload/shutdown cancel and join the worker before closing its store. A retention span whose cutoff precedes the signed Unix-nanosecond storage domain is a safe no-op determined before calendar arithmetic. | Pending/claimed routing, events with pending/claimed/running dispatches, and source events referenced by retained replays are preserved. Provisional or rolled-back generations never prune, an oversized retention value cannot wrap into a future destructive cutoff, a failure never busy-loops or stops later maintenance, disabled ingress creates no worker/database, and a full cycle cannot exceed 10,000 rows. | Storage must remain bounded without deleting actionable work, breaking replay lineage, or letting a failed configuration candidate cause irreversible deletion. |
| `FR-EVENT-AUTOMATION-012` | MUST | The foundation package is constructed or imported while no connector/listener integration is configured. | Existing runtime-event publication, workflow triggers, gateway routes, CLI/API behavior, and UI behavior remain unchanged. | No listener, workflow run, API registration, UI state, or process-local event subscription is created by `pkg/eventing`. | A disabled config or unsupported platform is inert rather than silently falling back to volatile delivery. | PR 1 must introduce durable primitives without disturbing existing infrastructure or pretending later stages exist. |
| `FR-EVENT-AUTOMATION-013` | MUST | A validated workflow declares `on.event` with one or more source, connector, type, actor, subject, or attribute filters. | Scalar or list values parse into typed filters; alternatives within one list use OR, populated fields use AND, and anchored `*`/`?` globs select a workflow deterministically. | Parsing and matching do not mutate the workflow or envelope. | An absent trigger, unknown/typoed field (including one inherited through YAML merge), empty trigger, empty list/map, blank pattern, empty entity filter, or missing required entity/attribute does not match and fails parsing/validation where applicable. Source, connector, event type, and entity type compare case-insensitively; IDs and attribute values remain case-sensitive. | Operators need explicit reviewable routing policy before AI or side-effecting steps run. |
| `FR-EVENT-AUTOMATION-014` | MUST | The router claims one durable event while both ingress and workflows are enabled. | It renews the routing lease, loads each current local definition once as an exact parsed, validated, compatibility-approved byte snapshot, evaluates that snapshot's deterministic trigger, and creates every matching dispatch with its opaque content revision through the current live routing token. It skips malformed, invalid, or compatibility-blocked workflows consistently with existing automatic triggers and acknowledges only after all selected rows and revisions are durable; zero matches is successful routing. | Each new `(event_id, workflow_ref)` atomically creates one deterministic revision-bound dispatch while the authorizing claim remains live; routing then becomes `succeeded`. | A stale/expired/replaced claim cannot insert even when another worker has already completed routing. Fan-out or lease-renewal failure uses bounded exponential retry, safe duplicate encounter, and `dead` attempt exhaustion. | A crash, slow catalog scan, or definition edit between matching and insertion must neither lose or retarget selected work, duplicate a dispatch, nor let a stale routing claim authorize work. |
| `FR-EVENT-AUTOMATION-015` | MUST | The dispatcher claims a durable event/workflow dispatch with its deterministic run ID. | Before execution it loads the redacted persisted envelope and one exact current runnable workflow snapshot, rejects a non-empty stored revision that differs from that snapshot, re-evaluates the persisted event against that same snapshot, renews its claim, and passes that exact workflow object plus the dispatch run ID and a trusted payload-free production origin to the shared executor. A legacy unbound dispatch may proceed only after the current snapshot still matches. The executor exclusively creates the durable run and calls `OnRunPersisted`; that callback links the dispatch and renews again before any workflow side effect. | The dispatch moves through claimed/running to succeeded, failed, pending retry, or dead while the normal file run store records the workflow run and its event/dispatch/root-run origin. | Revision drift, a removed/nonmatching trigger, an invalid internal origin, and other pre-run failures retry with bounded exponential backoff and become dead after the configured attempt limit without executing changed intent. A link/callback or ordinary workflow failure leaves a terminal durable run and becomes dispatch `failed` rather than being executed again. | Durable intent must execute the bytes and trigger decision it selected, remain linked to the same auditable workflow run before effects, and fail closed when old bytes are no longer available. |
| `FR-EVENT-AUTOMATION-016` | MUST | A claimed dispatch is recovered after restart and its deterministic run may be absent, terminal, still marked running, or missing after it was linked. | A never-created run may start; an existing successful run completes the dispatch successfully; an existing failed/canceled/skipped run completes it failed; an orphan running/unknown run is canceled as interrupted and completes failed without repeating workflow side effects. A linked dispatch whose run record disappeared fails closed without replay. | Reconciliation updates only the owned dispatch and existing run record. The first file run create uses a cross-process exclusive filesystem boundary and returns only after syncing the run file, run directory, store root, and workspace directory; later terminal updates use atomic synced replacement. | Duplicate run creation and concurrency-limit failures are typed; a crash before exclusive durable run creation remains retryable, while a crash after creation cannot start that run again even if normal run retention later removes its record. | Exactly-once external effects are not generally possible, so the safe recovery boundary is the durable run record plus its pre-effect dispatch link. |
| `FR-EVENT-AUTOMATION-017` | MUST | A workflow starts from an external event. | `event` exposes detached `id`, `source`, `connector`, `type`, actor, subject, occurrence/receipt times, payload, attributes, and replay lineage; inputs additionally expose `event_id`, `dispatch_id`, `source`, `connector`, `type`, and the event object. JSON numbers retain their original decimal token for exact conditions and persisted event snapshots. The session is `workflow:<ref>:event:<event-id>` and delivery is empty. Separately, the trusted origin exposes only exact event, dispatch, and family-root run IDs. | The executor persists detached input/event snapshots and the payload-free origin in the normal run record without changing existing non-event workflow numeric value types. | Only the already-redacted durable envelope reaches workflow context. No arbitrary raw payload path participates in router policy or provenance; connectors promote routing facts into normalized fields/attributes, and the origin is never inferred from them. | Deterministic routing stays small while workflows retain full-fidelity context for deterministic or AI-driven decisions and a narrow relationship authority. |
| `FR-EVENT-AUTOMATION-018` | MUST | Routing or workflow execution may outlive its initial lease. | `RoutingLeaseRenewer` optionally extends catalog work; the durable dispatcher requires `DispatchLeaseRenewer`. The dispatcher renews immediately after claim, throughout event/run lookup, synchronously again before any terminal reconciliation or interrupted-run cancellation, before run creation, immediately after linking, and periodically during execution. | Renewal changes only lease/update timestamps and never increments attempts or changes run identity. A heartbeat that observes the worker's own completed/nacked transition is reconciled against durable state rather than reported as ownership loss. | Blank IDs/tokens, non-positive/overflowing durations, pending/terminal/missing work, expiry, or a foreign token fail without mutation. Renewal failures consume the normal bounded retry/dead budget where the claim remains live; a stale worker cannot cancel a replacement worker's running deterministic run; unsupported builds return `ErrUnsupportedPlatform`. | Healthy long matching/execution must not be reclaimed concurrently, and a worker that loses ownership must stop creating, canceling, or performing side effects promptly. |
| `FR-EVENT-AUTOMATION-019` | MUST | The gateway starts, reloads, or shuts down with event ingress enabled or disabled. | Disabled ingress returns before creating the database, directories, or workers. Enabled ingress opens and validates the inbox and initializes event workflow hooks/MCP synchronously before workers. Reload makes readiness false, drains old workers, swaps while retaining the previous provider/config, preflights the candidate event runtime before starting any candidate service, and restores the previous runtime/service on failure. Candidate router/dispatcher iterations require the exact config generation and therefore cannot route or execute while the outer transaction is paused. Shutdown quiesces event and other runtime producers before AgentLoop drain, stops channel/media dependencies afterward, and only then closes provider state. | The service owns exactly one store and router/dispatcher pair per active gateway configuration, and readiness reflects failed recovery. | Store-open/schema/platform/runtime-init failures fail enabled startup instead of falling back to memory. Drain or post-swap restart failure retries cleanup and rolls back when safe; a canceled or stale candidate worker exits without durable routing or workflow effects; if recovery itself fails, readiness remains false and providers/dependencies are not closed out from under active workers. | Existing installations remain inert by default and reload cannot expose a half-committed runtime or workers using stale/closed providers. |
| `FR-EVENT-AUTOMATION-020` | MUST | A newly created dispatch is published to the process-local runtime event bus. | A best-effort `workflow.triggered` event identifies trigger kind, event/dispatch IDs, normalized source/connector/type, workflow ref, and deterministic session without including the external payload or secrets. | Runtime telemetry has no effect on durable routing or dispatch completion and is emitted only for a newly inserted dispatch. | A closed, full, or absent runtime bus cannot fail durable routing; duplicate routing does not republish the same dispatch trigger. | Observability is useful but must not become a second delivery or secret-retention path. |
| `FR-EVENT-AUTOMATION-021` | MUST | A release adds `on.event` parsing/validation semantics that older binaries ignored. | Workflow engine/schema/fingerprint compatibility changes make earlier validation stamps stale and require explicit revalidation before automatic execution. | Revalidation writes the normal compatibility manifest with the current workflow hash and validation result. | Existing channel, command, schedule, runtime-event, manual, and workflow-call trigger parsing and execution remain unchanged. | Previously stamped YAML must not silently activate newly understood automation after upgrade. |
| `FR-EVENT-AUTOMATION-022` | MUST | Master ingress and at least one named `events.ingress.webhooks` connector are enabled with a valid Standard Webhooks signing secret. | The gateway collision-safely registers `POST /webhooks/events/{connector}` on its existing shared listener and verifies exactly one `Webhook-Id`, `Webhook-Timestamp`, and `Webhook-Signature` header against the connector's HMAC-SHA256 secret. | Route registration is additive and identity-owned; no second listener or transport runtime is created. Connector names containing a configured canonical signing secret are rejected opaquely before config persistence or backend construction, including inactive names that would otherwise expose the credential as a JSON map key. | Disabled or unknown connectors and non-canonical percent-encoded path aliases return `404`; missing, duplicated, stale, future, or invalid signature headers return the same `401`; wrong methods return `405` with `Allow: POST`; route collisions fail startup/reload rather than replacing an existing handler. | Generic producers need an interoperable authenticated ingress without disturbing existing gateway and channel routes. |
| `FR-EVENT-AUTOMATION-023` | MUST | An authenticated connector submits one bounded JSON object containing `type`, optional `occurred_at`, actor, subject, attributes, and object payload. | The adapter preserves the exact signed bytes until authentication, strictly rejects unknown/server-owned fields and trailing data, derives deduplication from `Webhook-Id`, assigns source `webhook` and the path connector, and acknowledges only after synchronous durable insertion. New events return `202` and duplicates return `200`, both with the original event ID and `inserted` flag. | The normal inbox insert atomically stores the redacted normalized envelope or returns its first durable duplicate; provider retries never replace content or create another routing record. Client-controlled identity fields containing any configured signing secret are rejected before insertion because routing and deduplication identities cannot be safely rewritten. | Unsupported media/content encodings return `415`, oversized input returns `413`, malformed or secret-bearing identity input returns `400`, and storage failure returns retryable `503`; responses never echo payload, signature, secret, connector identity, or raw storage errors. | A transport acknowledgement must mean durable ownership, not workflow completion or volatile acceptance. |
| `FR-EVENT-AUTOMATION-024` | MUST | Gateway startup, reload, rollback, or shutdown changes the active event store, connector set, or signing secret. | Admission remains inactive until startup commit, becomes `503` before reload drain, waits for admitted inserts before store close, activates only the committed generation immediately before readiness, and restores only a fully restarted old generation after rollback. Secrets persist through secure-string storage and participate in exact-value redaction. | A stable generation-fenced controller swaps connector/store backends and stale cleanup cannot deactivate or unregister a replacement. | A timed-out drain remains retryable with its store open; candidate backends never acknowledge requests; failed recovery stays unready; disabling ingress removes the event route after commit. Public exposure requires TLS termination at a trusted reverse proxy. | Reload must not strand acknowledged events in a candidate database or close state still used by an in-flight request. |
| `FR-EVENT-AUTOMATION-025` | MUST | Master ingress and one or more named `events.ingress.channels.<channel-instance>` entries are enabled. | Each entry resolves to an existing enabled Delta Chat channel instance with mandatory `email` source and `mirror` or `event_only` mode. Omitted source defaults to `email`, while omitted mode defaults to `mirror`. Delta email requires a verified sender contact plus an encrypted/signed message unless `allow_unverified_email` is explicitly true. | Effective adapter maps are detached copies and no transport, database, or admission hook is created by disabled master ingress. | Empty, untrimmed, oversized, case-colliding, secret-bearing, missing, disabled, non-Delta, unsupported-source, unsupported-mode, chat-source, or unverified-email-opt-in-on-chat entries fail enabled config load and management validation; identity/secret conflicts fail opaquely before persistence even while master ingress or an entry is disabled, and all other disabled bodies remain inert. | Channel eventing must be explicitly additive, identify configured instances, and never silently turn a best-effort transport, spoofable mail `From` address, or credential-bearing name into workflow authority. |
| `FR-EVENT-AUTOMATION-026` | MUST | A configured, already-authorized Delta Chat message reaches inbound admission with its retry-stable provider-local message identity. | The adapter emits source `email`, connector equal to the channel instance, type `message.received`, a length-prefixed SHA-256 deduplication key scoped by account/conversation/topic, safe actor/conversation entities, bounded text/subject/reply/message fields, safe attachment name/type/size metadata, and an optional UTC occurrence time. Email events also expose `email_trust` and separate boolean sender-verification/transport-authentication attributes. | The normal redacting store receives only the bounded normalized envelope; all resolved configuration secrets longer than three bytes participate in exact-value redaction. | Missing stable identity fails retryably instead of using a content fingerprint. Email that lacks either trust proof creates no durable event unless explicitly opted in; mirror still follows the prior chat path and event-only consumes it. RFC724 Message-ID is never used for deduplication and, when fetched solely to correlate a full-download replacement, remains process-local; provider blob path, remote URL, media reference, bytes, routing `Raw` metadata, and media scope never enter the envelope. Oversized work is bounded before encoding and falls back to a valid truncated object. | Email needs useful workflow context without turning the durable inbox into an archive of transport internals, credentials, or spoofable authority. |
| `FR-EVENT-AUTOMATION-027` | MUST | A configured channel message is synchronously admitted after allow-list and group-trigger filtering. | Durable insertion succeeds or deduplicates before the ordinary channel turn continues. `mirror` then preserves the existing queue and opaque process-local turn-UX identity; `event_only` consumes the message without queueing, typing, reaction, or placeholder UX and releases turn-scoped media. Unconfigured and process-internal messages retain their prior path. | A successful insert owns the durable event; an insert error releases turn media, queues nothing, and is returned to a transport capable of retrying. Mirror typing, reaction, and placeholder artifacts form one exact-turn generation. The manager detaches and gives the prior generation bounded cleanup before starting the next provider generation, and provider stop/undo callbacks are exact-generation pinned so an older timed-out callback cannot clear newer UX. Cancellation, bus closure, abandoned/no-output work, or a stale worker removes only its exact generation; a committed buffered normal/error/tool/stream output retains cleanup ownership. Steering atomically commits to a live session owner after slow preparation or claims a new worker, and continuation output retains the original same-chat UX identity; cross-chat or ownerless steering cleans its secondary generation immediately after the queue commit. | Rejected senders and group noise never reach event admission. Admission receives detached metadata, cannot mutate the queued message, and a closed bus never runs the hook. Turn UX identity is excluded from serialized routing context. | The transport must not acknowledge volatile work, leak unconsumed media, strand a steering message, leave user-facing artifacts for an unqueued message, or let a stale rollback corrupt a newer turn. |
| `FR-EVENT-AUTOMATION-028` | MUST | Delta Chat starts or emits `IncomingMsg`, message-specific `MsgsChanged`, pending-download `MsgsChanged`, or `EventChannelOverflow`. | Provider events are notification-only: startup, every `IncomingMsg`, every message-specific `MsgsChanged`, pending-download generic `MsgsChanged`, and overflow wake an ascending `get_next_msgs` drain; overflow is handled before account filtering because it reports `contextId=0`. Authorized complete content uses only the provider-local Delta message ID as deduplication input, prefers locally observed receipt time over sender-controlled mail `Date`, and runs `markseen_msgs` only after successful durable admission or ordinary forwarding. There is no listener acknowledged-ID ring. | Successful, deliberately filtered, own, device, empty, or undecipherable messages advance the provider cursor in strict ascending order. Retryable download/fetch/admission/ack failure at a lower accepted ID blocks all later IDs. An incomplete message's RFC724 Message-ID is retained only in process for replacement correlation, never deduplication or durable payload. | A full download is driven by provider notifications rather than bounded polling. If the original remains in the ordered queue it must complete; if absent, it retires only after candidates through the last RFC-correlated replacement are processed. An unrelated complete batch cannot retire it, and without a visible correlated replacement the pending original conservatively blocks the queue. Shutdown cancels retry loops. Raw account blob paths never cross admission or enter agent text; unavailable media yields only a safe filename annotation. | Email ingestion must not lose MIME parts, duplicate mirror turns from stale provider notifications, retire pending work on unrelated traffic, acknowledge before durable ownership, let a forged mail date steer time-based routing, or skip retryable work through high-water advancement. |
| `FR-EVENT-AUTOMATION-029` | MUST | Startup, reload, rollback, shutdown, or a timed-out insert drain changes channel adapter/store generations. | A process-stable admission controller fences the exact prepared connector set before channels publish and collision-safely, identity-owned registers on the existing bus seam. Gateway commit first stages both channel and webhook backends as validated non-accepting reservations, aborts earlier reservations if any later staging check fails, records their generation identities, and only then reaches an irreversible aggregate commit point and publishes both sequentially through no-fail commits under its serialized lifecycle. It waits for admitted inserts before store close, restores a freshly prepared old backend on rollback, and closes only after pending publishers and active inserts resolve. | Generation identities fence delayed cleanup; active, retiring, staged, prepared, pending, and closed connector state move atomically. Sequential publication may transiently make one transport observable before the other after the irreversible commit point; both backends share the committed store and readiness remains false until both are published. | Candidate-only configured messages wait while unrelated/internal messages pass. Mismatched preparation, a pre-existing admission owner, or either controller's staging invariant fails closed before either candidate accepts traffic; no aggregate activation that returns an error exposes or acknowledges either candidate. Stale abort/cleanup cannot affect a replacement, a drain timeout leaves the old store open for retry, and failed recovery remains unready. | Hot reload must not insert an acknowledged channel event into a provisional/closed database, leave a failed half-commit, let a newly configured connector bypass durable admission, or displace another bus admission owner. |
| `FR-EVENT-AUTOMATION-030` | MUST | An enabled `events.ingress.webhooks.<connector>` selects `format: github` and GitHub sends one bounded JSON-object delivery with exactly one `X-Hub-Signature-256`, `X-GitHub-Delivery`, and `X-GitHub-Event` header. | The adapter verifies the `sha256=` HMAC over the exact raw body before decoding and, for admitted input, passes the complete authenticated object to the ordinary payload redaction and normalization path, assigns source `github`, the path connector, type `<event>` or `<event>.<action>`, and the delivery ID as deduplication key, and promotes only bounded sender, repository, event-resource, reviewer, team, and assignee metadata, with connector-local repository admission and target derivation defined by `FR-EVENT-AUTOMATION-039`. Envelope attributes explicitly record `body_authenticated=true`, `headers_authenticated=false`, and `signature_algorithm=hmac-sha256`. | For an admitted delivery, the ordinary redacting inbox atomically owns a new event before `202`, or returns the retained first event with `200` and `inserted: false`; it uses the same generation-fenced shared route and lifecycle as Standard Webhooks. A repository-scope miss follows `FR-EVENT-AUTOMATION-039` without invoking the inbox. | Missing, duplicated, malformed, or oversized authentication headers fail uniformly with `401`; malformed JSON, an invalid signed action, or a secret-bearing identity fails before scope admission or mutation with `400`. GitHub signs the body but not its event/delivery headers and supplies no signed timestamp, so public ingress requires trusted TLS termination. The local default body limit is 1 MiB even though GitHub permits payloads up to 25 MiB. Deduplication protects an admitted delivery only while its durable event remains retained; a redelivery after eligible pruning is a new event. | Native mapping makes GitHub automation useful without a second listener or a parallel durability, redaction, workflow, or reload system, while preserving the provider protocol's real trust boundary. |
| `FR-EVENT-AUTOMATION-031` | MUST | An operator explicitly installs `github-issue-triage` while native GitHub ingress, workflows, and a non-deferred GitHub MCP server are configured. | The installed workflow deterministically matches source `github`, type `issues.opened`, and `body_authenticated=true`; a no-tool classifier receives a narrow repository/issue projection from the signed body and returns only enum category/priority plus a boolean comment decision. A separate conditional `mcp/github/add_issue_comment` step uses signed-body owner/repository/issue identity and posts fixed bounded text containing the enums and event marker. | Installation writes one local workflow definition and revalidates the local catalog without changing gateway, ingress, model, MCP, or credential configuration. A matched run uses the existing durable dispatch/run state and records classifier/action steps normally. | GitHub's event header remains transport-authenticated only by trusted TLS. Issue text remains untrusted despite the body signature. Invalid model output, disabled/no-tool policy failure, absent MCP capability, or GitHub action failure produces no hidden fallback action. Explicit workflow retry, event replay, or provider redelivery after retention pruning can duplicate the comment because the marker is not a provider idempotency key. | AI classification becomes useful without model-held action authority, a new GitHub client, or changes to existing installations. |

| `FR-EVENT-AUTOMATION-032` | MUST | An authenticated launcher user requests the live event list, one event, its payload, the dispatch list, or one exact dispatch while ingress is enabled; a local CLI operator requests the existing list/event/payload/dispatch-list subset. | The launcher proxies through the gateway's PID bearer credential and the CLI calls that protected runtime endpoint directly. Lists support exact source/connector/type/status filters and filter-bound, versioned newest-first keyset cursors with a default of 50 and maximum of 100. Dedicated event and dispatch projections and their metadata store queries omit every owner/lease token; exact dispatch lookup through the runtime and launcher APIs selects the same token-free metadata by strict opaque ID without materializing a lease credential, while the CLI `dispatches` command remains list-only. Event metadata additionally omits deduplication and payload blobs and derives only `length(payload_json)`. Ordinary event responses omit payload, while the explicit payload endpoint returns the already-redacted JSON bytes exactly and all responses prohibit caching. | Read operations mutate no event, routing, dispatch, or workflow state and remain admitted to one live operator-controller generation until the store call and response projection complete. | Missing/invalid filters, IDs, cursors, or limits fail with `400`; missing events or dispatches return `404`; absent, starting, reloading, stale, or stopped gateway state returns retryable `503`. Disabled ingress registers no operator route and opens no store. Reload rejects new operations and drains admitted calls before closing the old store; delayed cleanup cannot deactivate a replacement. | Operators need inspectable durable state without opening SQLite beside a reloading gateway, materializing a page of payload blobs, exposing worker fencing credentials, or losing exact JSON numbers in browser parsing. |
| `FR-EVENT-AUTOMATION-033` | MUST | An authenticated operator explicitly requests replay of one existing event through the live gateway, and CLI callers additionally pass `--yes`. | Exactly one accepted `POST` with an empty JSON object creates a fresh pending event linked by `replay_of`, returns `201` and its new location, and leaves the source and prior dispatches unchanged. The launcher enforces same-origin browser metadata before proxying; neither client automatically retries a replay. | Replay uses the active generation's ordinary redacting store insertion and therefore creates new routing state that current deterministic workflow definitions process normally. | Missing events return `404`; malformed media type/body/query/ID or cross-site launcher requests fail without mutation. After replay dispatch, storage, cancellation, timeout, or transport failure reports a fixed unknown outcome without `Retry-After`; the operator must inspect replay lineage before deciding whether to issue another explicit request. Every replay can repeat workflows and external effects. | Replay must be deliberate, auditable, and additive rather than a hidden dispatch reset or an unsafe retry abstraction. |
| `FR-EVENT-AUTOMATION-034` | MUST | An authenticated dashboard user opens the Events route, changes exact event filters, selects an event, requests more results, explicitly reveals its payload, or opens replay confirmation. | The responsive master/detail surface keeps normalized filters and selection in the URL, keeps opaque cursors only in filter-bound query state, lists newest events, shows token-free event and dispatch projections, and loads exact payload text only after an explicit action. Replay presents an unmistakable duplicate-effects warning and sends one non-retried empty-object request only after confirmation. | Inspection and filter/selection changes mutate no durable state. Payload text is discarded when selection changes. A successful replay creates the additive event defined by `FR-EVENT-AUTOMATION-033`, invalidates affected reads, and selects the returned event. | Loading, empty, unavailable, malformed-response, not-found, and replay-failure states remain operable on desktop and narrow mobile widths. Payload never enters route state, browser persistence, logs, toast text, clipboard state, or HTML interpretation; cancel sends no request, failure keeps confirmation available, and ambiguous replay failure is never retried automatically. | Operators need a safe, accessible control plane that preserves the API's least-exposure and replay boundaries instead of turning inspection into hidden data retention or side effects. |
| `FR-EVENT-AUTOMATION-035` | MUST | An authenticated dashboard user opens `/event-sources`, edits the ingress master switch or storage/redaction policy, adds, edits, disables, or removes a Standard Webhooks/GitHub connector, chooses a connector secret action, configures a GitHub target login or watched repositories, imports a local newline-delimited owner/repo file, or opts an available Delta Chat instance into `mirror` or `event_only` email admission. | The responsive accessible editor loads only the safe configuration projection, shows existing webhook credentials as presence metadata, derives each token-free `/webhooks/events/{connector}` endpoint, warns that public GitHub delivery requires HTTPS, and presents available Delta Chat instances plus retained missing/disabled adapter references with explicit dependency state. For GitHub it explains empty-scope accept-all behavior, accepts one target login and one owner/repo per line, and can replace only the repository draft from a locally read text file. Management reads remain available to repair unresolved active webhook references and unavailable adapter dependencies, but validate every serialized webhook name, format, repository, target user, and channel map key against all configured sensitive values and fail opaquely rather than project a credential-bearing public identity. Optional retention and payload limits accept blank defaults or positive safe integers; connector names match `^[A-Za-z][A-Za-z0-9_-]{0,63}$` and are locale-independently case-insensitively unique; persisted connector names remain stable so their credential identity cannot move implicitly; enabled connectors use a format-valid configured, entered, or cryptographically generated secret; changing a persisted connector format requires a compatible replacement for any configured secret; and, while master ingress is active, an enabled adapter must reference an existing enabled Delta Chat channel. After save, the page reloads the safe projection, refreshes gateway state, and surfaces the shared restart-required notice when the effective active event-ingress runtime signature changed. | No draft or imported file mutates configuration before explicit save. The editor sends one scoped RFC 7396 `PATCH /api/config`: null policy values restore effective defaults, omitted or preserve-mode secret fields retain the secure value, a concrete secret rotates it, an explicit empty secret clears only a disabled connector, GitHub repository/target fields carry their normalized values, switching away from GitHub clears those fields, and null map tombstones remove deleted webhook/adapter entries without replacing unrelated configuration. Erasing a replacement field reverts to preservation; renaming is an explicit add-new/remove-old operation. A generated or entered secret remains input-only until save. With master ingress disabled, source edits remain runtime-inert. While it is active, the restart signature covers effective policy, enabled webhooks/adapters including canonical case- and order-insensitive GitHub repository scope and target login, workflow-dispatch executor settings, and digests of the complete exact-secret redactor input; inactive non-secret routing metadata and semantically equivalent repository reordering or case changes do not create a false restart requirement, but rotating any configured credential may require restart so the running store learns the new redaction value. | Invalid policy, duplicate/invalid names, invalid GitHub repository or target-user scope, a public identity containing any configured sensitive value, invalid Standard `whsec_` canonical-base64 material, invalid GitHub UTF-8/trim/32–256-byte material, a preserved configured secret after format change, an enabled connector without a secret while master ingress is active, or an unavailable/disabled Delta Chat dependency while master ingress is active blocks save with field-level or opaque boundary guidance as appropriate. Load, validation, and save failures remain actionable without losing an unsaved draft; background gateway polling continues to retry a temporarily unavailable status read. A clear action requires the connector to be disabled. Existing, generated, and replacement secret bytes never enter route state, endpoint text, browser persistence, logs, toast text, restart signatures, or read responses; the disabled default remains inert and no new listener is created. | Event ingestion must be configurable without raw JSON editing while preserving secure-string, opt-in, validation, and existing shared-listener/restart boundaries. |
| `FR-EVENT-AUTOMATION-036` | MUST | An authenticated dashboard user edits a workflow draft's external-event filters, selects a recent durable event, asks whether it matches, or launches an event-parity draft test. | The workflow UI projects `on.event` through the server parser into source, connector, type, actor, subject, and attribute controls while retaining raw YAML; explains OR-within/AND-across, anchored `*`/`?`, and case rules; and revision-fences a YAML-node replacement that preserves unrelated triggers, jobs, and comments. Alias/merge shapes and projected patterns or attribute names containing line breaks stay raw-only so the builder cannot flatten or split them. The match preview sends only draft YAML and event ID, loads payload-free metadata from the protected live gateway, and returns deterministic field checks from the exact runtime evaluator. Event-parity testing sends only the event ID, server-loads the complete already-redacted envelope through one admitted live-generation operation, requires the draft match, and constructs the same event snapshot, fixed inputs, target-ref event session, and empty delivery as automatic dispatch plus a trusted event/root-only draft-test origin before creating an ordinary `draft:<target>` run. The authoring agent has no tools or history, learns that AI is a post-match workflow step, and repair context contains structural event metadata rather than payload values. | Inspect, render, and preview are stateless and create no event, replay, dispatch, routing transition, compatibility stamp, or workflow run. A draft test records its selected event ID in the singleton test snapshot and its normal run record contains a trusted `external_event_draft_test` origin but creates no dispatch; automatic production dispatch behavior remains unchanged. Browser event selection uses metadata only, and exact payload remains behind the explicit ephemeral event inspector. | Malformed or oversized requests, stale revisions, invalid triggers, unsupported alias/merge or multiline-scalar edits, missing/unavailable events, a non-match, or any event-mode manual input, secret, session, delivery, or origin override fails before run creation. Delayed inspect/render responses cannot overwrite newer YAML. Captured payload bytes or values never enter routes, query keys, storage, toast/error text, match responses, origin, or AI author/repair prompts; switching selection purges an explicitly revealed payload. | Deterministic and AI-driven automation should be buildable and production-faithfully testable from the dashboard without a second matcher, browser payload copying, hidden routing side effects, or model-held action authority. |
| `FR-EVENT-AUTOMATION-037` | MUST | An authenticated dashboard user switches `/events` to the global dispatch view, changes exact event/workflow/status filters, pages results, selects one dispatch, or follows an event/dispatch/workflow/run relationship. | The responsive master/detail surface normalizes `view`, `dispatch_event`, `workflow`, `dispatch_status`, and exact `dispatch` selection in route search state while keeping opaque cursors only inside matching query state. It lists token-free dispatch projections across all events, loads selected metadata through the exact dispatch endpoint, shows its immutable workflow revision and independent created/available/lease/link/finish lifecycle fields, and constructs ID/ref-only links to the selected event and the workflow console's exact workflow/run state. Event detail links back to the exact dispatch view. | Filters, selection, pagination, and navigation mutate no event, dispatch, routing, or workflow state. Switching between event and dispatch views preserves the inactive view's normalized search state; browser back, forward, and refresh reconstruct the visible selection without persisting a cursor. | Invalid route values normalize away; malformed, unavailable, empty, and not-found responses remain operable on desktop and narrow widths. Payloads, delivery data, errors, cursors, deduplication identities, and owner/lease tokens never enter URLs or relationship labels. A pruned related record is an ordinary not-found state rather than evidence that another lifecycle stage completed. | Operators need one durable, shareable path through event, dispatch, workflow, and run state without copying opaque IDs, leaking protected fields, or losing context on refresh. |
| `FR-EVENT-AUTOMATION-038` | MUST | A production external-event dispatch or event-parity draft test creates a root workflow run, either run creates reusable children, a production run is retried, or an authenticated operator inspects any member of the run family. | The trusted internal `RunOrigin` is payload-free and contains only `kind`, exact event ID, optional exact dispatch ID, and exact family-root run ID. Production dispatch constructs `external_event` with `ev_` plus 32 lowercase hexadecimal characters, `dsp_` plus 32 lowercase hexadecimal characters, and the deterministic initial `wr_` run as root. Event-parity draft testing constructs `external_event_draft_test` with the exact event and initial root but forbids a dispatch. Root IDs are at most 1,024 bytes and match `wr_[A-Za-z0-9_-]+`. Reusable children and supported retries retain the complete unchanged origin. Run projection and Retry validate the selected record's intrinsic kind/IDs/context plus every ancestor still available through parent and retry links. A not-found pruned ancestor is an independent-retention boundary; any available ancestor with mismatched origin/context, an invalid link, a read failure, or a cycle makes provenance untrusted. Retry preserves origin across legitimate pruning and drops it only when this validation rejects the captured source. Workflow run detail renders ID/ref-only links to the event, production dispatch, and family root beside independent run cancellation/completion fields. | The origin is constructed only after trusted server-side event/dispatch resolution, persists in the ordinary file run record, and changes no envelope, routing, dispatch, or replay state. Retrying a production event run uses one captured authoritative source, creates no dispatch, and does not alter the original dispatch; retry lineage remains separate from the unchanged origin root. A missing ancestor neither repairs nor invalidates the retained record, while any conflicting retained lineage suppresses the projection and retry propagation. Event-backed draft runs remain retryable only by launching a new current draft test against an explicitly selected event. Legacy runs without origin remain readable and display no external relationship. | Browser/manual Run, Retry, and draft-test inputs cannot set origin. Invalid internal kind/ID combinations or context mismatches fail before run creation. Intrinsically malformed persisted origin, retained ancestor mismatch, invalid ancestry, lineage read failure, or cycle is omitted from browser projection and not copied by Retry. Event/input maps, payload, session, delivery, errors, route state, or coincidentally shaped manual values are never provenance authority. Event, dispatch, ancestor-run, and current-run retention are independent: a pruned relationship is ordinary not found and does not imply success, failure, cancellation, dispatch completion, replay, or provenance forgery. Payloads, delivery data, cancel reasons, errors, deduplication data, cursors, and lease tokens never enter relationship URLs or labels. | Typed provenance closes the navigation loop without granting payload-bearing workflow data authority, conflating production dispatch with a draft test, treating retention as tampering, or accepting conflicting retained lineage. |

| `FR-EVENT-AUTOMATION-039` | MUST | An enabled GitHub webhook connector declares optional `repositories` and `target_user`, an authenticated operator edits or imports a newline-delimited owner/repo list, or an authenticated GitHub delivery reaches that connector. | The editor round-trips the scope without exposing credentials, replaces its repository draft from a local text file, and on explicit save removes blank lines, trims entries, and retains the first of case-insensitive duplicates. After exact-body HMAC verification, JSON-object decoding, and secret-bearing delivery/event identity rejection, an empty repository list admits every repository; a non-empty list compares the body-authenticated `repository.full_name` case-insensitively. Admitted deliveries promote bounded pull-request number/URL/author/head/base/draft, issue number/URL/author, comment URL/author, review URL/author/state, requested reviewer/team, and assignee metadata. When configured, `target_user`, `targets_user`, and an optional deduplicated comma-separated `target_reason` identify an active matching requested reviewer, assignee, or case-insensitive `@mention`; review-request removal and unassignment actions do not produce the corresponding active target reason. A scope miss returns `202` with `ignored: true` and `inserted: false` while omitting `event_id`. | An explicit save writes only connector configuration. An admitted delivery follows the ordinary redacted inbox insertion, routing, and dispatch lifecycle. An ignored delivery creates no inbox, routing, dispatch, replay, or deduplication record. Changing an enabled connector's effective repository scope or target user changes the launcher event-runtime signature and therefore surfaces restart-required feedback; disabled connector metadata remains runtime-inert. | For an enabled connector, repository and target-user scope is valid only with GitHub format; other disabled connector bodies remain inert except for public-identity secret checks. An enabled configuration accepts at most 4,096 repositories; every entry is a trimmed UTF-8 `owner/repo` value matching `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`, is at most 256 bytes, and is unique ignoring case. A target login is at most 128 bytes and contains only letters, digits, and internal hyphens. A missing repository projection under a non-empty allowlist is ignored. Invalid authentication, malformed bodies/actions, and secret-bearing public or delivery/event identities remain errors before the scope decision. Target metadata is a deterministic routing hint, not authentication or GitHub action authority. A repeated scoped-out provider delivery is authenticated and ignored again because the first miss created no durable deduplication state. | Operators need to suppress irrelevant organization traffic and route review requests, assignments, and mentions without storing every delivery or asking workflow models to rediscover targeting facts from raw payloads. |
| `FR-EVENT-AUTOMATION-040` | MUST | An enabled GitHub connector uses `format: github` and opts into `poll_notifications`, with the exact non-deferred `github/list_notifications` and `github/pull_request_read` MCP tools available. | The gateway performs one bounded read-only provider scan at startup and waits 60 seconds after each completed scan before the next, requesting at most five provider-maximum 50-item pages including already-read notifications without marking, dismissing, or acknowledging them. A slow scan cannot accumulate timer ticks into immediate catch-up scans. It applies the connector's case-insensitive repository allowlist, maps review requests, mentions, assignments, issues, pull requests, and other notification reasons to stable event types, and enriches pull-request notifications with trusted number, URL, author, base/head revision, and clone metadata. Provider-derived envelopes carry `provider_authenticated` and `source_authenticated`, connector target metadata, and a deduplication key over notification ID plus provider update time. Exact JSON returned through the MCP wrapper may be inline or in one confined, bounded text artifact; the poller consumes and removes a valid artifact rather than parsing its model-facing notice. | Each admitted projection enters the ordinary redacted inbox once per connector/update. Re-polling unchanged provider state adds no event; an updated notification may add a new event. Poll-only connectors require no webhook secret and own no public webhook endpoint, while a connector with a signing secret may use polling and authenticated webhooks together. | Missing exact MCP tools leave polling unavailable rather than substituting an untrusted tool. Malformed, oversized, trailing, or incomplete MCP output; missing, multiple, malformed, non-regular, symlinked, out-of-root, invalid, or oversized exact-text artifacts; invalid repository/resource identity; or incomplete pull-request enrichment fails that scan before inserting the affected event. Empty allowlists intentionally accept every notification visible to the authenticated GitHub identity. Restart signatures include effective enabled polling state. | Private development machines need useful GitHub targeting events without requiring a publicly reachable webhook, while provider reads must never mutate the notification inbox or weaken durable event authentication. |
| `FR-EVENT-AUTOMATION-041` | MUST | An operator explicitly installs the built-in `github-pr-review` workflow and an authenticated GitHub `pull_request.review_requested` event targets the configured user. | The workflow acquires a workspace at the exact pull-request head, fetches the exact provider-authenticated base-repository revision, verifies both object IDs and the checked-out head, derives the merge base, and builds deterministic context-rich unified diffs only for changed production files through native `git.diff`. Each selected diff is at most 128 KiB and the aggregate is at most 512 KiB; any missing, malformed, empty, non-UTF-8, or oversized evidence fails closed before model execution. The isolated no-tools structured reviewer receives only this bounded path-relative evidence and cannot inspect the checkout or post to GitHub. A successful run may expose the reserved versioned `picoclawReviewDraft`; before acknowledging its dispatch, the event dispatcher validates and idempotently captures its summary, tests, residual risks, and zero to 200 bounded findings into the event SQLite store, bound to the exact event, dispatch, run, workflow revision, repository, pull request, and reviewed head. | One capture creates one durable review case and ordered findings. A zero-finding draft resolves immediately as `all_dropped`; otherwise the case is `open`. Reconciliation with the same immutable identity/content returns the existing case, while different content for an already captured dispatch/run conflicts. Workflows without the reserved output retain ordinary dispatch behavior. | Capture accepts only a GitHub review-request envelope whose authenticated source is either a verified webhook body or the trusted provider poller, and whose dispatch/run identity is present in the same store. Invalid schemas, revisions, paths, lines, severities, URLs, object IDs, sizes, repository identities, Git ancestry, evidence bounds, or identity mismatches fail capture or preparation and keep the dispatch retryable. Repository text and code remain untrusted model data; the review agent has no tools or GitHub write authority. | Findings must survive process failure as a human-owned draft instead of disappearing in workflow output or being posted automatically by a model. |
| `FR-EVENT-AUTOMATION-042` | MUST | An authenticated user opens `/reviews`, filters or pages cases, selects one case, edits a finding, drops/restores it, chats about the case or a finding, or requests a rephrase. | The responsive master/detail workbench loads strict token-free projections through the launcher's same-origin `/api/reviews*` proxy. Every complete case read holds one SQLite read transaction across its case, ordered findings, ordered messages, and latest submission, so one response cannot mix rows from different optimistic versions even when another process commits between component queries. Every mutation carries the current optimistic case version. Editing replaces bounded finding fields; dropping the last active finding moves the case to `all_dropped`; restoring reopens it. Chat durably records the human message before an isolated no-tools/no-history/no-cache AI call and then records its answer. Rephrase durably records the instruction and structured suggestion but changes no finding until the user applies it through the ordinary edit path. | Findings, case versions, state, and append-only messages persist in SQLite. One case accepts at most 256 messages, each at most 64 KiB and together at most 4 MiB of content. Filters and opaque cursors mutate no durable state. A stale version returns `409` with the latest safe detail so the browser can reload without discarding the user's local text. Submitting, submitted, unknown, and stale cases are read-only; an all-dropped case remains explicitly restorable. | Browser responses and errors cannot represent raw submission requests, idempotency markers, lease tokens/owners, internal diagnostics, deduplication keys, or event payloads. Noncanonical paths/queries, repeated or unknown JSON fields, duplicate keys at any nesting depth, oversized bodies, cross-site mutations, malformed upstream JSON, redirects, and unauthorized gateway responses fail closed. AI concurrency is bounded; chat/rephrase outputs obey the same message limit, while rephrased titles are at most 8 KiB. Repository and transcript content is quoted as untrusted data. The launcher bounds review responses to 32 MiB. A read, commit, cancellation, or corruption error returns no partial aggregate. | Human review, discussion, and wording changes need one auditable surface that preserves authorship and prevents stale tabs or prompt injection from silently changing the final review. |
| `FR-EVENT-AUTOMATION-043` | MUST | A human confirms submission of an open case with at least one active finding and the exact current version, or explicitly reconciles a terminal unknown case after checking GitHub. | The HTTP submission path atomically freezes only the active findings, summary, repository, pull number, reviewed head SHA, and a unique non-public marker into an immutable pending outbox record, moves the case to `submitting`, returns `202`, and performs no GitHub call. A leased worker first reads the pull request through the exact GitHub tool and requires its current head to equal the reviewed head; only then does it create one pending review at that commit, add each file/line finding through `add_comment_to_pending_review`, and submit the pending review once as `COMMENT` with the summary, body-only findings, and marker through `pull_request_review_write`. The workbench polls until the durable case reaches `submitted`, returns to editable `open` after a definite typed pre-write failure, or displays terminal `submission_unknown`/`stale`. | A successful worker atomically stores the external review identity/URL and resolves the case. A changed head makes the case stale before any GitHub write. Only a typed definite failure known to precede every external call records a public code and reopens the case; an ambiguous/untyped MCP result, lost/expired worker lease, or crash after claim becomes terminal unknown and is never reclaimed automatically. From that exact version, a human may record that the review was found, making the case submitted, or that it was verified absent, recording `reconciled_absent` and reopening the case. Reconciliation itself makes no GitHub call. All-dropped cases create no outbox row and make no GitHub call. | Submission validation rejects stale versions, inactive/empty drafts, malformed repository/revision/path/line/body data, unavailable exact GitHub read/write tools, marker/request mismatches, malformed or mismatched current-head reads, and a changed pull-request head before the write protocol begins. The protocol performs no retries and never deletes a pending review. Once any external call may have reached GitHub, transport/cancellation ambiguity is unknown rather than failed; restart recovery terminalizes expired claims before considering pending work. Reconciliation rejects non-unknown cases, stale versions, unknown resolutions, and attempts whose latest outbox row is not unknown. | Human confirmation must be the sole authority for external publication, and crash recovery must prefer a visible uncertain state plus explicit human reconciliation over duplicate GitHub reviews or comments. |
| `FR-EVENT-AUTOMATION-044` | MUST | A trusted review attention-policy consumer prepares one or more `ai_working_context` gates for an exact review case and one exact canonical agent. | Each review service pins the exact live agent-runtime generation before serializing its projections by case, reloads one complete atomic case aggregate from SQLite, validates the projected case/findings/messages, atomically reserves its protected review scope, and compare-and-swaps its ordered user/assistant messages plus an empty summary into one opaque agent-owned session. The reservation uses the same process-local directory/canonical-session locks as ordinary live admission and snapshot replacement. The key is derived only from canonical owner, `review` namespace, and case ID, while exact agent-qualified identity and version aliases prevent cross-agent or cross-case rebinding. A synchronous callback receives the coherent SQLite case version, internal canonical key, exact non-empty post-CAS revision, and a bounded detached JSON-native case/finding/message-metadata subject with transcript content and the latest submission record/internal fields unrepresentable. The case lock and runtime lease remain held so the consumer can compile inside the callback and attach `ReadOnlySessionRef{AgentID, Session, ExpectedRevision}` before ordinary private workflow admission. If a competing projection through another service using the same local runtime store advances the session before downstream exact snapshot capture, capture fails closed; an advance after capture cannot change the already frozen evidence. | SQLite remains the sole authoritative review transcript. Projection replaces only the derived local session view; it never imports session writes into review messages, changes a case/finding/submission/workflow run, or adds an eventing table. A later case version may refresh the same key and advance its alias/revision. Session revision fences only the derived view and does not prove that SQLite remained unchanged after the aggregate read; a production gate launcher that requires latest-case admission must bind and revalidate the supplied case version at its durable decision/linkage boundary. The internal key, revision, aliases, and subject are absent from browser-safe review DTOs and browser session discovery. Whichever of ordinary live or protected review admission wins first owns the key; after ownership checks that may read scope metadata, the loser fails before using protected transcript content for commands, history, a provider, a context manager, a public read-only workflow, or thread linkage, and before mutating the session. Seahorse excludes review scope from startup bootstrap and live ingest. A pre-existing ordinary-session collision therefore makes projection fail closed. | Invalid projected case/finding/message data or noncanonical agent identity, an unavailable/stale runtime generation or exact agent store, a nil/unsupported snapshot or atomic-admission capability, alias/key/owner/identity conflict, admission conflict, stale CAS, any replacement error, missing or inexact post-CAS readback, non-advancing/empty revision, or non-JSON/oversized gate subject fails closed before the callback. Cancellation propagates and every case/runtime lease is released. A failed first replacement may leave only a hidden empty review reservation, which an exact retry validates and completes. Cross-process sharing of one JSONL session directory is outside this process-local bridge contract. This bridge changes no eventing or review-draft schema and composes the current workflow engine v11, run schema v6, and validator v7 contracts. | Working-context AI gates need the same durable PR discussion the user sees without making a model-owned session authoritative, exposing an internal transcript through chat discovery, contaminating long-term retrieval, racing reload, or silently consulting the wrong case or agent. |
| `FR-EVENT-AUTOMATION-045` | MUST | A trusted in-process consumer requests the attention decision for one exact review case version and bounded decision point while a trusted policy source holds one stable repository-selected global-plus-local policy snapshot. | The launcher resolves the policy through the ordinary workflow resolver and compiles every effective working-context AI, isolated-context AI, deterministic, and zero gate in configured collect-all order. A true all-zero result returns without projecting a session, inserting a decision link, creating a run, or invoking any gate. An active working-context composition projects the authoritative review transcript under `FR-EVENT-AUTOMATION-044` and attaches its exact agent, opaque session key, and expected revision only to the private root; a composition without a working-context gate reads the same bounded aggregate subject directly and attaches no session capability. A canonical digest over the trusted source revision and detached effective policy becomes the durable policy revision. The exact case ID/version, decision point, and derived policy revision deterministically identify one private run. During the executor's pre-effect durable-create admission, one immediate SQLite transaction returns an existing link or verifies the case version, inserts the decision-to-run link, and invokes the create-only workflow-run callback before committing. Concurrent and restarted exact requests therefore return the one matching linked private run and execute no duplicate model, function, or human task. | Schema-v4 event storage adds only the immutable `(case_id, case_version, decision_point, policy_revision) -> run_id` link; the file run store remains workflow status and human-task authority. Launching does not mutate review cases, findings, messages, submissions, events, dispatches, provider state, policy configuration, or GitHub. Waiting human gates remain durable through the ordinary workflow resume path. Policy persistence and generic HTTP/CLI/manual launch remain outside this internal stage; automatic submitted-review triggering composes it through `FR-EVENT-AUTOMATION-048`, while its case-owned browser projection, response, and attention navigation compose through `FR-EVENT-AUTOMATION-049`. SQLite and the file run store are not a distributed transaction: process failure after durable run creation but before link commit may leave one unlinked private run that has executed no step; an exact retry detects the deterministic run record and fails unavailable rather than executing or replacing it. | Noncanonical identities, invalid or changing policy snapshots, invalid effective gates, stale/corrupt review aggregates, stale case or session revisions, wrong executor candidate shape, a missing/corrupt/mismatched linked run, storage uncertainty, runtime reload, or cancellation fails closed through fixed private-safe conflict/unavailable/cancellation boundaries. The trusted policy lease, review lock, and runtime lease are always released. Public requests cannot supply policy, repository, subject, run identity, session identity, or revision. Browser-safe run results, serialized runs, events, logs, and errors omit the policy body, review subject/transcript, session key/revision, and private diagnostics. | Durable review attention must reuse the configurable workflow gate engine without allowing duplicate delivery, mutable policy or review data, or private PR discussion to escape into generic workflow surfaces. |
| `FR-EVENT-AUTOMATION-046` | MUST | An authenticated operator reads or replaces the complete review-attention catalog, or an enabled event/workflow gateway generation starts or reloads with that catalog. | Config schema v6 stores bounded `reviews.attention.global` decision-point gate lists and `reviews.attention.repositories[owner/repo]` decision-point overrides. The complete catalog contains at most 8,192 policies and at most 8,192 gates; each repository contains at most 128 decision points. Each override explicitly selects `inherit`, `overlay`, `replace`, or `disable`; effective policies use the ordinary workflow resolver and may contain any validated ordered mix of the four gate kinds. One immutable trusted source case-folds repository lookup, rejects case-colliding configured keys, deep-detaches every returned snapshot, publishes a canonical SHA-256 whole-catalog revision, and derives each selected revision from only the canonical repository, decision point, and selected global/local layers so unrelated policy edits do not retarget an admitted decision. Every configured AI gate names an exact configured agent, and every working-context agent has a session store, before the active gateway generation opens event storage. Exact authenticated `GET` and same-origin `PUT /api/reviews/attention-policies` expose only the policy catalog, its catalog revision, the opaque complete-config revision, and gateway effect status. `PUT` is a strict full replacement fenced by `expected_config_revision`; after update-safe validation it compare-and-swaps the combined public/security revision and raw-patches only `reviews.attention` in the persisted public JSON. | A successful replacement mutates only operator-owned PicoClaw configuration; it does not write into any checked-out repository, review case, transcript, decision link, workflow run, event, provider, or GitHub resource. Config migration from v5 is additive and preserves an already present preview catalog. Policy changes participate in the gateway restart signature only while event ingress and workflows are both active; inactive policy edits remain runtime-inert. The source and management read are immutable current-schema projections that never migrate, back up, or save. A policy save preserves every unrelated public value and numeric token from the exact loaded revision, never serializes environment overrides or materialized defaults, and leaves the security sidecar byte-identical. | Null or malformed collections, invalid/case-colliding repositories, noncanonical decision/gate/agent IDs, unknown modes or gate kinds, duplicate gates, invalid effective overlays, incompatible working-context agents, non-JSON questions, missing configured agents, excessive count/depth/size, unsupported content type/encoding/charset, invalid UTF-8, duplicate/unknown/trailing JSON, oversized bodies, missing/stale revisions, cross-site mutation, load/save uncertainty, legacy schema requiring migration, or runtime generation drift fails closed through fixed safe errors. A stale public or security-sidecar generation returns conflict and never retries or writes. Repository policy authority can never be loaded from the PR head being evaluated. The browser policy editor is owned by `FR-EVENT-AUTOMATION-047`, automatic submitted-review triggering by `FR-EVENT-AUTOMATION-048`, and the case-owned browser handoff by `FR-EVENT-AUTOMATION-049`; generic/manual launch remains outside this stage. | Global defaults and repository exceptions need durable operator control without allowing reviewed code, stale browser state, an unrelated policy edit, or a partially initialized runtime to change who can stop or steer development. |
| `FR-EVENT-AUTOMATION-047` | MUST | An authenticated operator selects the canonical policy view of `/reviews`, creates, renames, or removes global or repository decision policies, creates, reorders, or removes gates, changes an override mode, edits one gate, saves the complete catalog, reloads authoritative state, or navigates away with unsaved changes. | The responsive accessible editor reads only `GET /api/reviews/attention-policies` plus the bounded agent catalog, keeps one memory-only draft, and exposes structured controls for global decisions, repository identity, `inherit`/`overlay`/`replace`/`disable`, ordered repeated working-context AI, isolated-context AI, deterministic, and zero gates. AI controls select an exact configured agent and edit criteria, title, and optional JSON question guidance; deterministic controls edit the existing expression, title, and required JSON questions; zero exposes no behavior fields. Repository policies show the effective ordered composition computed with the ordinary replacement-by-gate-ID overlay rule. Strict local projection and validation enforce the server's identity, collection, gate, text, effective-composition, JSON depth/node/byte, and complete-catalog bounds before save. The lossless JSON parser/stringifier retains every accepted number token—including integers beyond `Number.MAX_SAFE_INTEGER`—plus case-distinct and special object keys across GET, untouched draft state, question editing, and PUT. | The default inbox URL omits `view`; the policy view uses only `view=policies` and never places a policy, question, repository, decision, revision, error, or agent catalog in route state, browser storage, logs, or toast text. Background refetch never overwrites a dirty draft. Save sends one non-retried full replacement with the exact captured config revision; success rehydrates from the returned authoritative catalog/revisions and reports applied versus restart-required effect. A `409` or newly observed revision keeps the draft and exposes explicit reload/discard; navigation and before-unload are blocked until the operator discards, reloads, or saves. Editing, preview, refresh, and discard create no review case, chat message, decision link, workflow run, model/tool call, repository write, event, provider action, or GitHub mutation. The editor accurately explains that only an outgoing PicoClaw workbench review reaching `submitted` triggers its `review.submitted` policy; policy editing itself never launches a decision. | Malformed, unknown, duplicate, trailing, over-bound, unsafe-Unicode, or numerically lossy responses fail as unavailable instead of partially populating the form. Duplicate/case-colliding repositories, duplicate decisions or gate IDs, invalid effective overlays, incompatible working-context agents, missing agents, malformed or null required questions, and unsupported expression/text/size state produce actionable local errors and disable save. A failed or stale save retains all local text and never rebases or retries against a newer revision; an explicit reload is the only destructive conflict action and requires confirmation while dirty. Delayed reads and saves cannot replace a newer hydrated generation. Empty/error/loading states, long identities, many policies, keyboard navigation, and narrow widths remain operable. | Operators need one safe visual place to configure every approved gate type and mixture without raw config editing, numeric corruption, stale-tab overwrite, accidental execution, or leaking policy authority into reviewed repositories or browser persistence. |
| `FR-EVENT-AUTOMATION-048` | MUST | The outgoing PicoClaw review workbench atomically transitions a currently claimed pending submission to `submitted`, or a human reconciles that submission's terminal unknown outcome as `submitted`, in an open current-schema event store; an attention-capable workflow runtime may become active then or later. | The same SQLite transaction that commits the post-transition case version inserts exactly one durable `review.submitted` attention occurrence for that immutable submission and case decision, regardless of whether workflows are currently enabled. Once an attention runtime is active, its generation-fenced worker claims the occurrence under a fresh expiring lease. Before any session projection, model, function, human task, or run effect, the first successful policy capture resolves the trusted current repository-selected policy, canonically encodes its source revision, complete detached resolution, and decision digest, and pins those exact bytes to the live claim. The worker then strictly decodes and re-hashes only that pin and launches the exact case/version/decision through the ordinary private attention launcher. | Schema v5 adds a submission-bound trigger row with pending/claimed/delivered/noop state, availability, attempts, a fresh opaque lease identity and deadline, one immutable pinned policy and digest, bounded sanitized retry detail, and an optional validated private run ID. A successful launch records `delivered` even when its durable run is already terminal; an all-zero policy records `noop` without a decision link or run. Both terminal outcomes clear lease state and are never reclaimed. The trigger path changes no review content, submitted outcome, event, dispatch, provider notification, policy configuration, checked-out repository, or GitHub resource. | Non-submitted completion, failed/unknown submission, and absent reconciliation create no occurrence. Schema migration does not synthesize triggers for submissions already recorded as submitted under v4. Duplicate completion/reconciliation cannot add a second row. Capture, strict pin validation, pre-admission launch, storage, cancellation, or runtime-generation failure releases a still-live claim to bounded backoff without discarding its immutable pin; stale, expired, or foreign lease tokens cannot renew, pin, release, or complete it. Before a successful pin, a retry may select the then-current trusted policy; afterward no retry or reload consults live policy. A crash after launch but before completion reuses the same decision key and run ID, while a no-op retry reevaluates the same effect-free pin, so both converge without duplicated model or human work. This occurrence is only PicoClaw's outgoing workbench submission; inbound third-party `pull_request_review.submitted` events and own-PR development cases are outside this contract. This trigger stage itself adds no HTTP, CLI, attention-navigation, or generic workflow-task surface; `FR-EVENT-AUTOMATION-049` separately composes its browser handoff without exposing a generic workflow surface. | A durable submission must not lose its attention decision in a finish-to-launch crash window, fork that decision after policy drift, or confuse reviewing another contributor's PR with reacting to feedback on the user's own PR. |
| `FR-EVENT-AUTOMATION-049` | MUST | An authenticated user reads or polls `GET /api/reviews/{case}/attention`, follows the canonical `/reviews?case={case}&focus=chat` handoff, or submits `POST /api/reviews/{case}/attention/respond` with exactly the projected case version, opaque response token, and one normalized response; a generic workflow observation or mutation surface or production workflow-retention pass encounters a run with the exact reserved `inline/review-attention-gates/v1` reference. | The gateway returns `none` immediately for a valid non-submitted case without reading an attention occurrence or workflow run. For every submitted case it validates the authoritative latest submission and trigger first. A historical submitted case with no v5 trigger projects `none`. Pending or claimed state accepts only a coherent absent pin pair or a strictly decoded canonical pin whose recomputed decision revision equals `policy_revision`, then projects `queued` or `processing` without reading a decision link or run. No-op requires a canonical pinned all-zero effective policy, no run, and terminal completion, then projects `not_required` without reading a link or run. Only delivered state requires a canonical pinned active policy, terminal completion, the deterministic canonical run ID, exact decision link, and a stable bounded run/task snapshot. Every projected task's exact title, questions, and response schema are canonically re-hashed and must equal its stored input hash before any prompt or fence is exposed. The public DTO contains only `case_version`, aggregate `none`/`queued`/`processing`/`waiting`/`continuing`/`recovery_required`/`completed`/`not_required`/`failed` status, `can_respond`, and bounded turns with public `answered`/`waiting`/`continuing`/`recovery_required`/`canceled` status, configured title/questions, and the durably accepted response for answered, continuing, or recovery state; a canceled turn is non-actionable and contributes to aggregate `failed`. Exactly one current actionable waiting or recovery turn may set `can_respond` and receive an opaque lowercase `sha256:` response fence; the token is absent whenever `can_respond` is false and is never issued for continuing, answered, or canceled turns. The fence is a domain-separated, length-prefixed digest over the exact server-loaded case/version, submission, decision, policy revision, run, task, original waiting revision, and input hash. Response handling resolves every private identity server-side, derives a separate response ID from the fence plus the bounded normalized response, resumes the exact task, and returns the authoritative projection. The existing review conversation card renders status/history/questions and the in-memory response editor; `focus=chat` scrolls and focuses only after the selected conversation is rendered and the URL carries no token or private state. Every exact reserved attention run, including malformed impostors, is omitted or not found on generic workflow list/detail/events/SSE/graph/task/resume/cancel/retry routes. Visible ordinary runs have direct hidden `parent_run_id` plus `caller_job_id`, `retry_of_run_id`, matching `child_run_ids`, and an origin whose root is hidden scrubbed; graphs omit hidden nodes and incident edges. Cancel, retry, task resume, and task cancel return not found for every ordinary run transitively connected to a hidden run through normalized parent/child/retry relationships, preventing cascade mutation while leaving scrubbed ordinary reads available. Production launcher and CLI workflow retention preserve every exact reserved-reference run regardless of terminal age because case projection and exact replay depend on it after restart; related ordinary runs retain normal retention. With event ingress active but workflows disabled, the bridge uses the file run store with no executor even if one was injected: existing lifecycle remains readable, waiting/recovery has `can_respond=false` and no fence, no new answer is consumed, exact already-persisted replay stays projection-only and idempotent, and generic exact-reserved task resume returns not found before the workflow-disabled/runtime branch. | GET, polling, navigation, rendering, and failed validation mutate nothing. A valid response changes only the private human task and its run continuation; it never changes the review case/version, finding, message, submission, event, trigger, decision, policy, repository, provider, or GitHub. Exact persisted replay or lost-response recovery is idempotent and reprojects the current state, while an altered response or stale, old, cross-case, or cross-task fence conflicts. No schema is added because the view is reconstructed from existing owner-local state. | A noncanonical route/query/body, malformed or status-inconsistent trigger/pin/task/projection, stale case version, oversized response, invalid fence, altered replay, or continuation failure returns fixed bounded conflict/unavailable errors without partial authority. Missing/corrupt decision linkage or run/task state is an error only for a delivered occurrence; historical absence and valid pre-delivery/no-op branches retain the projections above. If the exact response persisted before continuation or transport failed, retry recovers by exact response ID and reprojects without consuming a second answer. Private IDs and revisions, session, policy body, task/run/workflow identity, input hashes, trigger lease/retry state, and raw stored/runtime errors are never projected or accepted from the browser. The launcher requires an explicit nonzero port and numeric loopback or literal current local-interface PID host; hostname, wildcard/unspecified, multicast or remote numeric address, incomplete authority, redirects, and proxy use fail before the process bearer is put on a request. This outgoing-review handoff makes no inbound own-PR feedback/development-case claim. | The user must be brought back to the exact PR discussion when a gate needs judgment, be able to steer that one private continuation safely, and never gain a side channel into generic workflow authority or internal review automation state. |
| `FR-EVENT-AUTOMATION-050` | MUST | A native GitHub webhook connector has a non-empty configured `target_user`, its exact request body passes HMAC-SHA256 verification, and the unsigned `X-GitHub-Event` routing hint plus the body action select `pull_request_review.submitted`. | The normalized envelope projects bounded pull-request base/head repository identity; canonical positive-decimal repository, pull-request, and pull-author database IDs from the authenticated body; existing bounded review author/URL/state metadata; canonical review database ID, node ID, commit SHA, and UTC submitted time; plus explicit lowercase-string booleans `pull_request_author_is_target` and `review_author_is_target`. When the configured target case-insensitively matches the body-authenticated pull-request author, the body-authenticated reviewer is a different canonical human or GitHub App `[bot]` login, every required numeric object ID and both review IDs are canonical, state is `approved`, `changes_requested`, or `commented`, commit identity is lowercase 40- or 64-hex, and submitted time parses and canonicalizes to UTC, the ordinary deduplicated target projection adds `review_feedback` and sets `targets_user` to `true`. Existing requested-reviewer, assignee, and mention reasons remain ordered, deduplicated, and independently selectable. An ordinary `on.event` workflow may match these explicit attributes without inspecting payload text or invoking a model; because `target_reason` is a comma-separated set, membership selectors use a pattern such as `*review_feedback*` and continue to match when `mention` coexists. | Only the ordinary already-redacted event envelope and its existing routing lifecycle are inserted. The object IDs are body-authenticated routing evidence only; they are not provider-verified until `FR-EVENT-AUTOMATION-051`. This projection adds no schema, development case or thread, workflow definition or run, checkout, session, model/tool call, attention decision, edit, commit, push, merge, provider acknowledgement, or GitHub mutation. The event-source target-user hint describes authored-PR feedback and labels webhook targeting as routing metadata only. | Missing/malformed pull-request, reviewer, repository/pull/author database ID, review ID/node/state/commit/submitted-time identity; another author's PR; a self-review; another action or event hint; absent target configuration; and notification-poller input do not add `review_feedback`. They may retain other independently valid target reasons. The event header is not covered by GitHub's body HMAC and therefore remains a routing hint. `FR-EVENT-AUTOMATION-051` owns the first provider-verified development-case capture boundary; every later checkout or action boundary must independently re-read the exact review, pull request, fork/head, and current provider state rather than treating these attributes or that capture as GitHub write authority. Repository scoping, payload limits, redaction, secret-identity rejection, and connector-scoped delivery deduplication still apply before or during ordinary admission. | Submitted feedback on the user's own PR must become deterministically routable through the existing workflow engine without hard-coded usernames, model inference, self-review loops, mutable-name identity, or premature repository/GitHub authority. |
| `FR-EVENT-AUTOMATION-051` | MUST | The exact explicitly installed `workflows/github-pr-development.yml` run succeeds for a body-authenticated GitHub `pull_request_review.submitted` envelope targeted as another reviewer's feedback on the configured user's own PR, and exposes the exact reserved string output `picoclawDevelopmentCapture: v1`. | Before dispatch acknowledgement, the successful-run sink validates the immutable event, dispatch, run, workflow ref/revision, connector, own-PR target facts, authenticated repository/pull/author database IDs, review database ID/node ID/author/state/commit/submitted-time/URL, and exact successful-run relationship. It then calls only the generation-fenced read-only GitHub `pull_request_read` tool. `get` must return and exactly match the canonical positive-decimal pull-author database ID and verifies the current pull-request number, one canonical lowercase HTTPS provider origin and exact pull URL, author/target, open-or-closed/draft/merged state, base repository/ref/SHA, and current head repository/ref/SHA including a fork. The current MCP projection omits repository and pull-request database IDs, so capture cross-binds those HMAC-authenticated IDs to this same provider object by requiring exact canonical origin, base-repository full name, pull URL, and pull number. Bounded `get_reviews` pages select the exact canonical review database ID and verify its author, event state or provider-current `dismissed` state, commit SHA, submitted instant, HTTPS URL on that origin, and at most 64-KiB valid-UTF-8 body. The webhook review node ID is retained as authenticated trigger evidence only because `get_reviews` does not expose it. | One immutable `pdc_` development case records exact capture provenance, provider-current pull/base/head facts, the submitted and current review states, review-level feedback, and timestamps in the event store. The same capture transaction binds it to the provider-verified private thread owned by `FR-EVENT-AUTOMATION-057`. Lookup by immutable event/dispatch/run/workflow identity makes reconciliation of that exact capture idempotent before another provider read. Dispatch ID and run ID are independently unique: an explicit replay admitted as a new dispatch/run creates a distinct independently verified case even when connector and review ID match, while a collision in which an existing dispatch or run carries changed identity or content conflicts. The workflow and sink create no review-workbench case, thread conversation, attention decision, gate, session, checkout, model/tool-authored action, edit, commit, push, merge, provider acknowledgement, or GitHub mutation. | A missing marker leaves ordinary successful-dispatch behavior unchanged. A wrong marker/ref, unauthenticated or non-own-PR event, absent/noncanonical object ID or provider origin, direct author-ID mismatch or failed repository/pull cross-binding, missing exact MCP read tool, malformed/duplicate/trailing/deep/oversized provider JSON, non-regular or escaping exact-result artifact, absent or duplicate exact review, provider mismatch, or more than five 100-review pages fails before case/thread mutation and keeps dispatch reconciliation retryable. Provider result bytes are bounded to 32 MiB in aggregate. The current MCP comment projection does not expose a parent review database ID, so inline review-comment association is outside this review-level contract; later development actions must re-read their own current authority. | The first own-PR feedback milestone needs durable, reconstructible, provider-bound local intake without pretending that a signed trigger, mutable repository/login/URL text, untrusted review text, or successful read-only workflow already authorizes development or publication. |
| `FR-EVENT-AUTOMATION-052` | MUST | An authenticated user opens the own-PR development view, filters its inbox by one exact `owner/repository` and optionally one positive pull number, pages it with an opaque cursor, selects one canonical `pdc_` case, or follows its HTTPS pull-request or review link. | The generation-owned gateway exposes exact read-only `GET /runtime/eventing/pr-development` and `GET /runtime/eventing/pr-development/{pdc_...}` routes. The list accepts only optional repository, canonical positive decimal `pull_number`, canonical decimal limit, and opaque cursor bound to both filters, defaults to 50 and caps at 100, and returns immutable cases newest first by `(updated_at, id)` with a matching next cursor. The launcher-safe summary exposes exactly case ID, repository, pull number/URL/author/state/draft/merged, head repository/ref/SHA, review author, submitted/current review states, review submitted time/URL, and `captured_at`. Exact detail adds the base repository/ref/SHA, review commit SHA, and at-most-64-KiB valid-UTF-8 feedback; `FR-EVENT-AUTOMATION-053` separately adds a bounded conversation projection without widening the list. The responsive `/reviews?view=development` inbox keeps optional exact repository, optional positive pull number, and selected case in canonical route state, labels every provider state/ref/SHA as captured snapshot data, opens external links deliberately, and renders feedback only as pre-wrapped plain text. | Reads, filters, pagination, selection, canonicalization, rendering, and external-link navigation mutate no event, dispatch, workflow run, capture, thread, conversation, repository, provider, or browser-persisted state. Immutable `pr_development_cases` rows and schema-v6 indexes remain the sole source of public capture and inbox-ordering facts; private schema-v9 thread membership does not collapse or reorder them. Explicit replays and multiple reviews retain separate visible, separately selectable `pdc_` cases even when they share one private thread. Chat, drafts, optimistic versions, and repair controls remain bound to the selected case only. | Noncanonical paths, methods, nonempty or streaming GET bodies, IDs, queries, repeated or unknown parameters, invalid repositories/pull numbers/limits/cursors, unavailable generations, malformed stored rows, oversized or non-JSON upstream responses, redirects, and unauthorized gateway responses fail closed without a partial result. Browser DTOs cannot represent event/dispatch/run/workflow/connector provenance, target-user or trigger-node evidence, private `pdt_` identity or case ordinal, provider origin or repository/pull/author database IDs, review database ID, capture hash, event payload, credentials, leases, or internal errors. `current_review_state`, pull state, and head/base facts describe only the provider snapshot verified at capture. The read surface performs no refresh and starts no model, gate, checkout, filesystem, Git, CI, commit, push, merge, acknowledgement, or provider action. | Provider-verified feedback must become locally inspectable and discoverable without exposing private stable identity, conflating sibling captures or the incoming development aggregate with outgoing review drafts, or turning a historical capture into live repository or GitHub authority. |
| `FR-EVENT-AUTOMATION-053` | MUST | An authenticated user selects one exact own-PR development case, submits one Go-`TrimSpace`-normalized nonempty at-most-32-KiB UTF-8 message with an integer expected version from zero through 256, or reloads after failure or another writer. The launcher accepts only exact `POST /api/pr-development/{pdc_...}/chat` with one unambiguous same-origin browser provenance; the protected runtime accepts only exact `POST /runtime/eventing/pr-development/{pdc_...}/chat` and rejects any `Origin`, `Referer`, or `Sec-Fetch-Site` header before store or model access. Both routes reject a raw query or bare `?`, noncanonical escaped paths, aliases, extra segments, unsupported methods, streaming or missing-length bodies, and anything except one at-most-one-MiB `application/json` value with optional UTF-8 charset, identity encoding, valid Unicode scalars, bounded depth, and exactly the case-sensitive keys `expected_version` and `content` with no exact or case-colliding duplicate, unknown, or trailing member. | Schema v7 stores one `pr_development_conversations` high-water row per immutable `pdc_` case plus append-only `pr_development_messages`. New capture creates empty state atomically and v6 migration backfills it. Version equals contiguous message count; state also records total content bytes and a domain-separated length-prefixed rolling SHA-256 digest over every canonical message field. Every read and append verifies the complete transcript against all high-water values before use. After basic input and agent-configuration preflight, one process-wide same-case lock and one per-`Service` AI slot cover the turn. The service then reads and binds the immutable case and current conversation, validates the complete transcript, rejects a stale version, and reserves two remaining rows plus the normalized human bytes and the maximum 64-KiB assistant answer before appending anything. It appends the human under an immediate expected-version transaction, invokes the configured agent, validates its answer, and appends the assistant under the next version. The detached at-most-512-KiB user context contains only bounded captured-snapshot facts and the newest suffix of at most 50 ordered transcript messages. The private ephemeral request uses `tools=none`, `history=none`, `cache=none`, managed execution off, and one exact isolated advisory system prompt; it suppresses the configured agent default, workspace/bootstrap, identity, memory, skills, prompt contributors, tool rules, summary, time, and dynamic runtime context. The isolated prompt marks repository, feedback, refs, transcript, and latest message as untrusted historical data and forbids inspection or action claims. The protected response uses the safe GET-detail shape, including version and ordered public message ID, ordinal, role, content, and creation time. | A successful turn appends two message rows and advances the high-water row twice; each case remains bounded to 256 messages, 64 KiB per message, and 4 MiB total. Ordinals are zero-based, unique, and contiguous. Any failure after the human append—including model invocation, model-output validation, assistant append, or post-append validation—never rolls back the human row or fabricates a missing assistant row; any successfully stored assistant remains authoritative. The immutable capture, capture timestamps, inbox ordering, events, dispatches, workflow runs, repository, browser persistence, and provider state do not change. One process-wide case lock prevents service instances or runtime generations in the same process from interleaving turns; SQLite immediate transactions, complete-transcript validation, and expected-version/high-water compare-and-swap fence other writers, including another process. The case-keyed browser component keeps detail, transcript, draft, mutation, and ambiguity state in memory only, uses the same Go whitespace normalization, refuses equal-or-older delayed detail, and adopts only a strictly case/version/message-bound response. An ambiguous reload at expected version plus one with the exact human row records a committed-response failure and suppresses blind retry; expected version plus two with that row and the following assistant clears the failure as a completed turn. The conversation is an accessible live log, refresh failures retain the displayed detail and draft, and narrow-screen selection moves focus to Back then restores it to the selected case on return. | Invalid route, transport, provenance, JSON, identity, text, transcript, capacity, integrity, agent, output, cancellation, version, or storage state fails closed. Conflicts return `409`. A chat error may include detail only after the protected handler independently reloads and validates it within two seconds; a failed reload and every list/detail error omit detail, and raw model/store diagnostics never cross the boundary. The launcher's 120-second AI request budget is covered by the shared protected HTTP server's 135-second write timeout. AI concurrency is bounded per service instance, not globally; only the same-case lock is process-wide. Neither GET nor chat receives provider credentials, checkout/filesystem/Git/CI tools, default agent context, private runtime/session identity, capture provenance, internal errors, or authority to launch a gate, acknowledge feedback, edit code, commit, push, merge, refresh GitHub, or mutate another resource. | A user needs an auditable conversation beside each submitted review before development starts, while the model remains an advisory interpreter of local historical evidence rather than an actor or source of current repository/provider truth. |
| `FR-EVENT-AUTOMATION-054` | MUST | A trusted in-process development controller supplies one store-validated immutable `pdc_` case with one valid schema-v9 thread membership to `GitHubVerifier.VerifyCase`, then—while owning the concrete provider/model generation—may call `agent.LocalRepairRunner` with an exact controller pin derived from the returned current head plus a bounded user instruction and optional explicitly untrusted context. | `VerifyCase` validates the selected case's membership, rebuilds routing evidence from the durable case, and reuses the generation-fenced bounded `pull_request_read` pull/review scan. For a provider-verified membership, the thread's canonical provider origin is authoritative; a nonempty strict optional `GitHubVerifier.WebOrigin` must canonicalize to that same origin, the provider-returned pull-author database ID must exactly equal the stored invariant, and the signed repository/pull IDs are cross-bound through the exact origin, base-repository full name, pull URL, and pull number. For an isolated legacy membership, `VerifyCase` preserves the pre-v9 case-scoped evidence and configured-origin checks without deriving, attaching, or joining stable provider-object identity. Pull and review URLs must have exact origin-bound repository/pull/review shapes, and the clone URL must equal that origin plus the current head repository and `.git`. `VerifyCase` requires the current pull request to be open and unmerged, the exact review to remain non-dismissed with unchanged ID, author, state, commit, submitted instant, URL, and body, and the base repository/ref to remain unchanged; base SHA and fork/head repository/ref/SHA may advance. It returns only current head identity, the credential-free canonical clone URL, current review state, and a domain-separated length-prefixed SHA-256 review digest. The repair runner accepts no raw workspace path or release capability, process-serializes the exact pin and serializes calls through its borrowed provider, acquires the pin before model access, builds one fresh registry containing exactly `read_file`, `list_dir`, `edit_file`, and `apply_patch`, and runs a fixed isolated prompt with no history/cache/default context. Model tool batches are bounded, uniquely identified, allowlisted before execution, argument values are suppressed from logs, and mutation calls execute sequentially in response order. Every read/write path is canonical checkout-relative, bounded, workspace-confined, and denied when it lexically aliases or resolves through a symlink into `.git`; apply-patch validates every source and move destination before operation one. The runner always reacquires and identity-compares the pin after execution, including cancellation. | Provider refresh mutates no capture, thread membership, sibling case, conversation, event, dispatch, workflow run, provider, or repository. A repair remains scoped to the selected case and may mutate only ordinary files inside its pinned local checkout; exact pinned-checkout acquisition may create or update the manager-owned repository, workspace, lock, heartbeat, and history state. No sibling-case transcript or repair state, session, account affinity, prompt cache, hook, runtime event, MCP, web, shell, process, workflow, message, Git command, CI command, commit, push, merge, acknowledgement, release, or provider write is available. The result contains only bounded final text, iteration count, and opaque workspace ID. | Invalid/tampered thread or case membership, malformed legacy isolation, changed provider origin, author-ID mismatch or failed repository/pull cross-binding for a verified thread, edited feedback, closed/merged/dismissed state, target retargeting, invalid/noncanonical web origin or pull/review URL, malformed/credentialed/cross-host/encoded clone URL, provider bounds, unavailable exact pin, changed pin identity, outside/aliased/Git-control/symlink path, oversized file/patch/tool batch, unknown or duplicate tool call, provider panic/nil response, cancellation, iteration exhaustion, empty/invalid answer, or failed postflight fails closed. Provider failure after an allowed edit and ordinary multi-operation patch failure may leave partial repository-content edits; the exact pin remains locked and postflight-checked for explicit inspection or recovery, never reset, released, published, or silently retried by this primitive. | Local development needs an independently current provider-object fence for new verified captures and the smallest possible editing capability before later thread conversation, review, gates, validation, or publication orchestration can safely exist, while migrated cases retain their existing isolated case-scoped repair path. |
| `FR-EVENT-AUTOMATION-055` | MUST | An authenticated user explicitly confirms one local repair for an exact `pdc_` case and submits a Go-`TrimSpace`-normalized nonempty at-most-4-KiB instruction with exact conversation and repair revisions plus one random `prq_` idempotency key. | Schema v8 stores at most one `pds_` repair session per case and at most 64 contiguous `pdr_` attempts. Schema-v9 thread membership does not merge, retarget, or share those sessions. The generation wires repair execution whenever exact GitHub read capability is ready. Projection and admission then side-effect-free-check the selected case's valid thread membership and the current configured default agent for a new session, or the existing session's immutable canonical agent after one has been created; a default-agent configuration change never retargets that session. Admission atomically verifies the complete selected-case/conversation/session aggregate, both optimistic fences, absence of active work, instruction and selected canonical agent, then creates or advances only that case's session and queues one attempt; an exact idempotency replay returns the same aggregate while changed intent conflicts. A generation-owned worker claims one attempt as `preparing`, renews its lease, loads only the exact selected-case conversation prefix named at admission, independently calls `VerifyCase` with the private provider-object fence when present or the isolated pre-v9 case fence for a legacy membership, durably installs an initially empty pin or requires an exact existing pin, resolves one concrete no-fallback repair runner, changes the attempt to `running` immediately before invoking it, and finishes through a bounded cancellation-detached lease fence. The browser-safe detail independently versions conversation and repair state and exposes only availability, fixed unavailability reason, opaque session/attempt IDs, canonical agent ID, pinned head repository/ref/SHA, instruction, public status/timestamps, bounded sanitized summary, and fixed error code. | Admission returns `202` without waiting for provider or filesystem work and does not append or reinterpret advisory chat or consult a sibling case. Attempts move only through `queued`, reclaimable read-only `preparing`, ambiguous-effect `running`, and one terminal `completed`, `failed`, or `recovery_required` state. A successful attempt means only preserved local content edits: no local review, CI, commit, push, merge, provider acknowledgement, or pin release has occurred. The stable case-owned session reservation, immutable selected agent, and first verified head/review pin remain held across every attempt, including failure and default-agent reload. Later case chat may receive only bounded detached public summaries from that same repair session as explicitly untrusted data. | Every runtime and launcher mutation route rejects aliases, queries, browser authority at the protected hop, cross-site launcher input, malformed/duplicate/unknown JSON, stale fences, changed idempotent intent, missing/corrupt thread membership, and cross-case session or attempt binding. Verified provider-object drift, legacy-case evidence drift, non-actionable state, unavailable exact session agent/model/workspace, or another safe pre-run failure becomes terminal `failed` without editing. If the stored agent is removed or becomes unusable, existing history remains visible with repair unavailable and no fallback to the new default. An expired `preparing` lease may be reclaimed because it performed only repeatable verification/pinning; an expired/lost `running` lease, crash, completion-write ambiguity, or any runner error after invocation becomes `recovery_required` and is never automatically rerun. A moved ref during first checkout, later provider/pin mismatch, or existing dirty session never causes repinning, reset, cleanup, release, or silent loss. Public DTOs cannot represent private thread ID/ordinal, provider origin/object IDs, clone URL, reservation, lease, workspace path, provider/model/account identity, prompt/tool arguments, review digest, or raw diagnostics. | A browser-visible case development session must survive restart and ambiguous failures, preserve partial local work, and let the user steer each explicit attempt without treating a private grouping, sibling capture, advisory conversation, an old capture, a retry, or successful editing as permission to validate or publish. |
| `FR-EVENT-AUTOMATION-056` | MUST | A trusted local-development controller has durably stored one exact pinned-workspace candidate produced after validation and invokes the controller-only commit boundary with that parent, tree, candidate digest, opaque workspace, immutable `pdcmt_` intent, canonical message, and UTC whole-second authored time. | Git Workspaces recomputes the same all-worktree candidate, deterministically creates and verifies one fixed-identity one-parent commit object whose message binds a domain-separated digest of the intent, compare-and-swaps detached local `HEAD`, reconciles an interrupted real-index update after a crash between completed Git subprocesses, and returns only opaque workspace plus parent/tree/digest/commit/count evidence. Reservation-scoped kernel locking serializes the edit-only repair interval, candidate snapshot, commit, and pinned release across processes. Repeating an exact intent after an ambiguous return proves and returns the same commit with `already_applied`; an unexpected `HEAD`, candidate drift before application, or ordinary-file drift after proven application is a conflict or explicit recovery outcome rather than a second commit. | This primitive mutates only the manager-owned local checkout's content-addressed objects, detached `HEAD`, its bounded exclusive local reflog, and index. It creates no eventing row and is not yet called by the schema-v8 repair worker, whose `completed` meaning remains local edits only until a later durable validation/commit orchestration requirement replaces that lifecycle. No branch, provider, review, conversation, workflow run, cache, remote, or browser state changes. | Empty or malformed evidence, stale workspace/pin/parent/tree/digest, attached or concurrently changed `HEAD`, dirty staging state, in-progress Git operation, changed gitlink, unsafe ref-storage/symlink-ref configuration, nonexclusive appendable reflogs, path/origin/control-plane drift, excessive output, cancellation, failed cleanliness proof, or a stale lock left inside a terminated Git subprocess fails closed. Stale Git locks require explicit operator recovery and are never deleted automatically. The primitive cannot run repository CI or hooks, push, merge, release, reset ordinary files, acknowledge a review, or obtain provider/network authority. It is absent from generic tools, workflow primitives, HTTP, and frontend APIs; callers must later bind exact validation and write-ahead intent records before product use. | A completed repair will need a durable local commit anchor, but the low-level Git effect must be independently deterministic and reconcilable at completed-subprocess boundaries before a worker can safely claim that validation or development completed. |
| `FR-EVENT-AUTOMATION-057` | MUST | A provider-verified own-PR capture is committed under `FR-EVENT-AUTOMATION-051`, an exact retry reconciles that capture, or an eventing database at schema v8 is opened by schema-v9 code. | Schema v9 adds private `pdt_` development threads and immutable case memberships. `FR-EVENT-AUTOMATION-051` establishes a verified identity by retaining the HMAC-authenticated repository and pull-request IDs only after exact current-object cross-binding and by exactly matching the provider-returned pull-author ID. A verified thread is uniquely keyed by the canonical lowercase HTTPS provider origin plus canonical positive-decimal base-repository and pull-request database IDs; its canonical positive-decimal pull-author database ID is an immutable equality invariant, not part of the key. In the same immediate transaction that creates a new `pdc_` case and empty case conversation, capture resolves exactly one matching verified thread and assigns the case its next zero-based ordinal. Membership ordinals are unique and contiguous from zero through the thread's exact case count; complete reads validate every membership, reverse uniqueness, count, and identity before use. Exact capture retry returns the existing case with the same thread and ordinal. Another review, connector, or explicit replay may append a distinct case only when the provider origin, repository ID, pull ID, and author invariant match exactly; connector remains per-case authenticated provenance. Repository/login spelling, URL, pull number, ref, review identity, timestamps, and connector never establish or merge stable thread identity by themselves. | Migration creates one isolated private legacy thread containing ordinal-zero membership for each pre-v9 case, without parsing payloads, contacting a provider, or grouping any two cases. A legacy thread carries no verified provider-object identity and never matches, joins, or upgrades into a later verified capture automatically. Existing case-scoped `VerifyCase` and repair behavior continues through the pre-v9 case evidence for compatibility, but the legacy thread cannot aggregate sibling cases or enter future thread-wide ledger/orchestration until an explicit provider-verified baseline or adoption contract is added. New verified threads and memberships mutate only local eventing metadata; all captures remain immutable, separately listed `pdc_` cases. Conversation, repair session, locks, optimistic revisions, AI context, browser cache/draft/mutation state, and routes remain case-scoped until a later ledger requirement explicitly replaces them. No `pdt_`, ordinal, provider origin, identity hash, exact count, cases digest, or legacy marker is representable in current list/detail/chat/repair DTOs, routes, model context, logs, generic event/workflow surfaces, or browser storage. Raw numeric IDs remain ordinary HMAC-authenticated webhook payload/attribute fields already observable through existing event/workflow surfaces; those untrusted fields confer no verified invariant, stable grouping, membership, or action authority. No model, gate, checkout, Git/CI, commit, push, merge, provider acknowledgement, or provider write is started. | Missing/noncanonical provider identity or origin, direct author-ID mismatch, failed repository/pull cross-binding, changed author invariant, duplicate/reordered/gapped membership, a case linked twice, key collision with unequal identity, mixed verified/legacy state, counter disagreement, malformed legacy isolation, cancellation, or storage ambiguity fails closed without guessing from connector, repository/login text, URL, pull number, review identity, or time. A failed new-capture transaction leaves no case, conversation, thread, or membership fragment; failed migration rolls back the schema version and every generated legacy row. | Multiple reviews and replays for one real provider PR need one durable future orchestration identity without conflating same-looking resources, exposing trusted grouping state, retroactively inventing legacy evidence, or prematurely changing today's case-owned UI and development lifecycle. |
| `FR-EVENT-AUTOMATION-058` | MUST | Schema-v10 code opens a schema-v9 eventing database, or a trusted future PR-development controller calls `eventing.PRDevelopmentControllerStore` for the latest queued or completed repair attempt in one provider-verified thread with an existing pinned retained-workspace baseline. | Schema v10 adds at most one private `pctl_` controller and stable `pdln_` retained-line identity per verified `pdt_` thread, immutably bound to one owner repair session, canonical agent, and exact existing workspace/clone/ref/commit pin. First creation atomically transfers that session out of the legacy claim queue and, after store-wide collision and unique-owner checks, inherits the exact `pdrk_` reservation already locking its pinned workspace; every later mutation receives a globally fresh `pdck_` reservation. Expected revision and reserved headroom fence every material transition; exact live token, deadline, monotonic epoch, and non-regressing time fence every state-changing lease write. First Adopt or later Resume must bind the complete exact line result, and Resume durably advances mutation epoch before Park evidence is accepted. Only the completed latest owner attempt may append its next exact park/version/epoch/intent/base/tip/tree/no-change/review-digest fence. That transaction globally retires the reservation digest, hash-chains the parked and authenticated retired-mutation proof, removes usable mutation authority, and enters `review_pending`. A distinct reservation-free review lease may then claim only that fence; Finish folds authenticated review-completion proof into the final tail hash and enters `ready`, Release returns the unreviewed fence to `review_pending`, and only an expired review lease may rotate safely. Complete reads validate owner/pin and initial-reservation equality, phase/lease shape, exact reachable revision/epoch relations, source/line state, completed fence ownership and causal order, two-stage contiguous hashes/versions, no-change tree preservation, and store-wide active/retired reservation non-reuse. | Migration creates only empty tables and validated indexes. The APIs perform no filesystem, Git, model, AI-review, CI, workflow, commit, push, merge, HTTP/UI, or provider effect. Creation changes private queue eligibility by suppressing legacy claims for the owner; this is durable storage ownership transfer, not worker execution. This slice has no controller-aware transition that completes a newly admitted queued attempt, so it does not claim that legacy dirty work can be adopted or that a new queued attempt can complete; the later worker must adopt a clean pinned line before mutation. Reader snapshots validate the complete private aggregate but redact both the lease token and raw mutation reservation. The parked Git-workspace line remains retained after mutation authority is retired, but this store neither creates nor inspects it. | Legacy/malformed identity, duplicate reservation ownership, a sibling or unpinned owner, disallowed/non-latest/cross-session attempt, stale revision, insufficient exit headroom, foreign/expired token, skipped or changed binding, unfinished attempt, duplicate/gapped/changed fence, reservation reuse, impossible no-change tree, noncausal or unreachable proof, or corrupt high-water/hash state fails closed without another lease or partial evidence. An exact current lease or state-changing mutation operation that encounters expiration durably enters `recovery_required` and preserves its bearer in private storage; stale callers and read-only Get neither transition state nor receive that bearer. An expired review lease alone may be reclaimed for the same immutable fence. RecordFence and FinishReview authenticate exact retries with hash-bound retired token proofs; an exact committed Bind retry is no-write and remains replayable without another clock-dependent transition, while Acquire and ReviewRelease do not replay retired authority. No storage phase, including `ready`, proves an AI review, deterministic validation, CI, commit, or publication ran. | A later attempt ledger needs one crash-fenced owner and immutable handoff from exclusive mutation to separate review without holding edit authority across model context, silently rerunning ambiguous filesystem work, or prematurely wiring any worker or product surface. |

For `FR-EVENT-AUTOMATION-051`, absent provider IDs fail creation of a new
capture. The sole compatibility exception is an exact pre-identity retry whose
three IDs are all absent: it is reconciled by immutable provenance before
current origin parsing, and only an existing migrated singleton legacy
membership can match. It cannot create a case or claim provider identity.

For `FR-EVENT-AUTOMATION-057`, “without parsing payloads” means the migration
does not read or parse the retained raw event-envelope payload. It does
integrity-load each normalized pre-v9 development-case row, including its
capture hash and timestamp, solely to bind the legacy membership and fail
closed on a corrupt case.

For `FR-EVENT-AUTOMATION-055`, the public repair revision is bounded at 1024
and admission reserves every remaining claim, first-pin, begin, and terminal
transition before it appends an attempt. Reclaiming an expired `preparing`
attempt rotates only private lease state: it changes neither public timestamps
nor the public revision. Each claim samples its scan clock only after acquiring
the SQLite immediate transaction, examines at most 32 eligible aggregates, and
fully validates each before mutation. Immediately before ownership is written,
it samples a fresh claim clock and uses that instant for the complete lease,
queued public timestamps, session bump, and preparing-reclaim fence. A
semantically invalid stored aggregate is durably suppressed through private
state without advancing public revision or timestamps; later polls therefore
progress past a full invalid batch, while operational store errors still fail
the claim. The maintenance worker runs even when new repair admission is
unavailable, so it can fail queued or preparing work safely and convert expired
`running` ownership to `recovery_required` in deterministic batches of at most
32; later polls make bounded progress. Cancellation of the owning generation
before `running` instead leaves preparation reclaimable, and intentional
heartbeat cancellation does not manufacture lease loss. The
`preparing -> running` transaction atomically refreshes the execution lease
immediately before runner invocation. Every renewal samples its clock inside
an acquired immediate transaction and extends monotonically, so it cannot
shorten a fresher execution lease written by `BeginPRDevelopmentRepair`.
GitHub-read readiness controls whether the generation can wire the verifier and
worker; per-case model/workspace readiness controls only projection and new
admission. Those checks use the configured default before session creation and
the stored immutable session agent afterward, so reload cannot silently switch
an established checkout/model identity or strand it merely because the default
changed. Durable and public repair instructions and summaries are each bounded
to 4 KiB. Together with the existing case, 256-message/4-MiB transcript, and
64-attempt bounds, even worst-case JSON HTML escaping keeps both the maximum
legal detail and its launcher error wrapper within the 32-MiB proxy response
ceiling.
Only `preparing -> failed` and
`running -> completed|recovery_required` are valid terminal transitions, and a
completed result must persist its stable private workspace identity. Public
projection additionally requires a complete pin for `running`, `completed`, or
`recovery_required`, unique attempt IDs, and nondecreasing attempt conversation
versions.

For `FR-EVENT-AUTOMATION-046`, configured-agent discovery is the exact
authenticated `GET /api/reviews/attention-agents` companion resource. It
requires one strong `If-Match` containing the policy response's opaque complete
config revision and returns at most 256 identities in canonical agent-ID order.
Only normalized `id`/`name`, the effective default ID, the same config revision,
and an optional canonical decimal next-page offset are representable. The
cursor and required revision header form one generation fence: a public or
security-sidecar change makes every older page request conflict. An empty
configured list projects only implicit `main`; the route never projects
workspace, account, model, skills, subagents, runtime effects, paths, security
bytes, or raw errors, and it never migrates or writes configuration.

## Data And State Model

The normalized `Envelope` is an immutable value. Its identity fields distinguish
the source family, configured connector, provider delivery/deduplication key,
and source event type. It carries a required JSON-object payload, optional
`Actor` and `Subject`, string attributes, an optional occurrence timestamp, a
receipt timestamp, and optional replay lineage. Normalization assigns an
`ev_`-prefixed durable ID and missing receipt time, then converts timestamps to
UTC. A replay is another event with its own identity and routing lifecycle,
plus a reference to its original; the original record never changes.

For a configured native GitHub target, own-PR review facts remain ordinary
envelope attributes. `pull_request_author_is_target` and
`review_author_is_target` are explicit string booleans derived from the
authenticated body and local target setting. `review_feedback` is one
deduplicated `target_reason` value, not a durable development-case or thread
identity. Canonical numeric repository, pull-request, and pull-author database
IDs plus review ID, node ID, commit, submitted time, and base/head repository
projections remain bounded routing evidence inside the already-redacted event;
they add no table, lease, provider credential, checkout reference, or write
capability. Only the later provider read may promote them: it directly matches
the author ID and cross-binds the signed repository and pull IDs through exact
current origin, repository, pull URL/number, and base facts.

Only the explicit FR-051 successful-run capture boundary may promote that
routing evidence into a `PRDevelopmentCase`. The case retains the original
event/dispatch/run/workflow provenance, provider-current pull and base/head
facts, submitted and current review states, the review-level feedback body,
the trigger node ID with its routing-only classification, and capture times.
It is deliberately separate from outgoing `pr_review_cases` and has no finding,
message, submission, gate, workspace, branch-action, or provider-action state.

Schema v7 gives every development case one `pr_development_conversations`
high-water row and zero or more append-only `pr_development_messages` rows.
The high-water row stores the exact message-count version, total content bytes,
and a domain-separated length-prefixed rolling SHA-256 digest over every
canonical message field. New capture inserts its empty state in the capture
transaction; migration from v6 backfills empty state for every existing case.
Every conversation read and append recomputes the complete contiguous
transcript and requires all three high-water values to match, detecting a
missing state row, deleted tail, changed same-length message, count drift, and
byte-total drift before projecting or appending. The digest is a local
consistency fence, not authentication against an attacker who can rewrite the
whole database.

Envelope text is valid UTF-8 and byte-bounded: source is at most 128 bytes;
connector and event type are each at most 256; the deduplication key is at most
1,024; actor/subject scalar fields are each at most 2,048. Envelope, actor, and
subject attribute maps each hold at most 128 entries, with keys at most 256
bytes, values at most 8,192 bytes, and no blank key. Event IDs and replay links
are exactly `ev_` plus 32 lowercase hexadecimal characters, and an event cannot
name itself as its replay parent. Persisted event, lease, retry, cursor, and
retention times must round-trip through a signed Unix-nanosecond value (roughly
September 1677 through April 2262) instead of silently wrapping.

Workflow references are valid UTF-8 and at most 1,024 bytes. Routing and
dispatch error details are passed through exact-secret redaction and truncated
on a UTF-8 boundary to at most 16 KiB before persistence.

The SQLite database lives at `events.ingress.database_path`, whose effective
default is `<workspace>/eventing/events.db`. Its logical state contains:

- a `PRAGMA user_version` schema marker advanced with each transactional
  migration;
- immutable inbox data, including the normalized envelope, redacted payload,
  optional self-referential `replay_of` foreign-key linkage, and exactly one
  routing lifecycle with
  `pending`, `claimed`,
  `succeeded`, or `dead` state, availability time, attempt count, fresh lease
  token, lease deadline, error, and lifecycle timestamps;
- at most one dispatch row per `(event_id, workflow_ref)`, with a
  deterministic run ID, an immutable opaque workflow-content revision for
  dispatches selected by schema-v2 routing, and its own `pending`, `claimed`, `running`,
  `succeeded`, `failed`, or `dead` lifecycle, availability time, fresh lease
  token, and lifecycle timestamps; and
- unique indexes for `(source, connector, dedupe_key)`, event/workflow dispatch,
  and workflow run identity.

The workflow file store, not the eventing SQLite schema, persists optional
`RunOrigin` with an event-backed run. It contains no envelope or payload fields.
`external_event` requires an exact event ID, dispatch ID, and root run ID;
`external_event_draft_test` requires the event and root but forbids a dispatch.
The first run is its own origin root, and reusable descendants and retries copy
that origin unchanged. A retry therefore retains the original event/dispatch
relationship without creating or updating a dispatch, provided the captured
source remains trusted. Trust requires valid intrinsic fields/context and a
matching origin/context on every retained parent/retry ancestor. An ancestor
missing because of independent pruning terminates that validation branch
without invalidating its descendant; a retained mismatch, invalid link, read
failure, or ancestry cycle suppresses origin projection and retry propagation.
Origin absence is the valid representation for legacy, manual, channel,
command, schedule, and runtime-event runs; browser code cannot promote their
event/input snapshots into provenance.

The routing lifecycle may be columns on the inbox record rather than a separate
physical table; callers depend on behavior, not table layout. Payload byte
limits and redaction happen before the insert transaction.
Redaction traverses JSON objects and arrays, normalizes sensitive key spelling,
recognizes sensitive suffixes, replaces exact configured secret substrings,
and also scrubs actor, subject, and envelope attributes. Existing
`[REDACTED]` markers remain stable when stored events pass through redaction
again during replay. Configured punctuation-only field names retain exact-key
matching; an exact configured secret found in a JSON or attribute-map key is
rejected because changing structural keys could collide or alter event
semantics. Store construction adds resolved secure configuration values longer
than three bytes to exact-value redaction without persisting that trusted list.
Worker labels seed a
diagnostic token prefix only; each claim adds fresh cryptographic randomness
and transitions compare the complete opaque lease token. Store clocks are
injectable and
operation deadlines/cutoffs are explicit where needed so recovery and retention
are deterministic in tests. Store methods return detached values so caller
mutation cannot change durable or concurrently returned state.

The operator surface projects stored rows into separate public values that have
no deduplication-key or lease-token field. Event lists and ordinary detail omit
payload bytes; `GET .../payload` returns only the already-redacted stored JSON
bytes so browser clients can keep large and exponent-form numbers as exact
text. Opaque operator cursors encode a version, resource kind, timestamp/ID
keyset position, and digest of the active filters using bounded canonical
base64url JSON. They are traversal positions rather than durable server state.

`events.ingress.enabled` defaults to `false`.
`events.ingress.database_path` may override the workspace-relative default.
`events.ingress.retention_days` declares the cutoff policy used by the
generation-fenced gateway retention worker. An enabled committed ingress
generation prunes immediately after activation and every six hours, in bounded
batches, while disabled or provisional generations cannot touch storage.
`events.ingress.max_payload_bytes` rejects oversized inputs, and
`events.ingress.redact_fields` adds recursively scrubbed JSON field names to the
mandatory defaults; it cannot remove the built-in sensitive-field set.
That set is `authorization`, `proxy_authorization`, `cookie`, `set_cookie`,
`password`, `passwd`, `secret`, `token`, `access_token`, `refresh_token`,
`api_key`, `client_secret`, `private_key`, `webhook_secret`, and `signature`.
GitHub's `x_hub_signature` and `x_hub_signature_256` headers are also mandatory.
Configuration owns policy; `pkg/eventing` owns normalized data and persistence.

`events.ingress.webhooks` maps conservative, case-distinct connector names to
an `enabled` flag, an optional `format`, and a signing secret. Omitted or
`standard` format preserves the existing Standard Webhooks contract and
requires a canonical `whsec_` secret containing at least 32 decoded bytes.
`github` selects native GitHub delivery authentication and requires an exactly
trimmed 32–256 byte UTF-8 webhook secret. The format is explicit and closed;
unknown values fail enabled configuration rather than falling back to another
verifier.

GitHub connectors may additionally declare `repositories` and `target_user`.
An absent or empty repository list accepts every repository configured to send
to that endpoint. A non-empty list contains at most 4,096 exact trimmed
`owner/repo` values of at most 256 bytes, unique ignoring case; effective
configuration deep-copies the list and the immutable webhook runtime compiles
it into a case-folded membership set. The optional target user is a trimmed
GitHub login of at most 128 bytes. Repository and target-user fields are invalid
on an enabled non-GitHub connector and do not turn a disabled connector into
runtime state. All repository and target-user strings are public configuration
identity and therefore cannot contain a configured sensitive value.

An admitted GitHub envelope may carry bounded `pull_request_*`, `issue_*`,
`comment_*`, `review_*`, `requested_reviewer`, `requested_team`, and `assignee`
attributes projected from the authenticated body. When a target is configured,
`target_user` records the normalized local setting, `targets_user` is always
`true` or `false`, and `target_reason` is present only for a deduplicated
ordered set of active `requested_reviewer`, `assignee`, and `mention` reasons;
a native webhook may additionally include the FR-050 `review_feedback` reason.
Review-request removal and unassignment do not turn the removed top-level user
into an active reason. These attributes are durable routing context, not
provider authorization.

`poll_notifications` is GitHub-only and defaults to `false`. When enabled, an
empty connector secret means poll-only: the shared webhook controller does not
publish a route for that connector. Polling uses the configured `github` MCP
server's exact `list_notifications` and `pull_request_read` tool identities,
never a model-selected tool. The normalized event differentiates this trusted
provider channel from a signed webhook with `provider_authenticated=true`,
`source_authenticated=true`, and `body_authenticated=false`. Both channels may
therefore authorize capture through `source_authenticated`; only a signed
webhook may claim that its exact body was authenticated. Provider notification
IDs and update timestamps form connector-scoped deduplication identities.

Schema v3 adds four review tables to the same event database:

- `pr_review_cases` binds a unique dispatch and run to immutable event,
  workflow, repository, pull-request, base/head, and initial draft identity,
  plus an optimistic version, active/total counts, and the `open`,
  `all_dropped`, `submitting`, `submission_unknown`, `submitted`, or `stale`
  lifecycle;
- `pr_review_findings` stores ordered editable content and revision plus
  active/dropped state and optional drop reason;
- `pr_review_messages` stores an ordered append-only, optionally
  finding-scoped chat/rephrase transcript, bounded atomically to 256 messages,
  64 KiB per message, and 4 MiB of aggregate content per case; and
- `pr_review_submissions` stores one immutable request per case draft version,
  a unique hidden marker, pending/claimed/submitted/unknown/failed state,
  fresh fenced lease ownership, bounded diagnostics, and optional external
  review identity.

Case/finding/message/submission IDs are respectively `prc_`, `prf_`, `prm_`,
and `prs_` plus 32 lowercase hexadecimal characters. Browser projections are
concrete safe types which make marker, request JSON, lease data, and internal
errors unrepresentable. A case version advances with every edit, state
transition, message append, submission creation, and submission outcome.
Submitting freezes the selected draft version; later browser input cannot
rewrite its outbox request.
Complete aggregate reads hold one SQLite snapshot from the case row through
findings, messages, and the latest submission. A concurrent writer may commit
in WAL mode, but that commit becomes visible only to the next aggregate read.

Schema v4 adds `pr_review_decision_runs`. Its composite primary key contains
one canonical case ID, positive exact case version, bounded decision point, and
derived SHA-256 policy revision; its run ID is independently unique. Rows are
immutable historical idempotency links. Admission of a new row requires the
case still to have that exact version, while an existing exact row remains
readable after the case advances. The table stores no policy body, repository,
gate subject, transcript, session capability, model output, or workflow status.

Schema v5 adds `pr_review_attention_triggers`. Its submission ID is both the
primary key and a foreign key to one immutable submission; a separate unique
constraint covers case ID, positive post-submission case version, and the fixed
`review.submitted` decision point. The row carries pending/claimed/delivered/noop
lifecycle, scheduled availability, attempts, a fresh expiring lease, bounded
sanitized retry detail, and an optional private run ID. Its policy pin is a
non-empty value of at most 3 MiB: a
canonical versioned JSON envelope containing only the trusted source revision,
complete detached gate-policy resolution, and matching decision digest. Pinning
is compare-only after the first write: an identical retry is accepted and any
change conflicts. The table contains no review transcript, gate subject,
session capability, model output, provider credential, or GitHub authority and
has no browser projection. The additive v4-to-v5 migration creates no row for a
submission whose submitted transition already happened under the old schema.

Schema v6 adds `pr_development_cases`. Each `pdc_` row owns one immutable
provider-verified capture and its exact accepted event, dispatch, run, workflow
revision, and connector provenance. Dispatch ID and run ID are independently
unique. A replay admitted under a new dispatch and run creates a distinct case,
including when it refers to the same connector and provider review. The row stores current
pull open/closed, draft/merged, base and fork/head repository/ref/SHA facts;
review database ID, routing-only trigger node ID, author, submitted and current
state, commit, time, HTTPS URL and bounded feedback; a capture hash; and
created/updated times. The hash covers the normalized provenance and provider
domain content. An exact dispatch/run provenance lookup returns the committed
case before another mutable provider read; repeating that exact capture
converges only when the full hash is equal, and any mixed identity or changed
content conflicts.
Newest-first and repository/pull indexes support future owner-local consumers,
and ordinary inbox pruning retains an event referenced by one of these cases,
without changing the immutable capture schema. The development workbench's read
boundary uses those indexes for a filter-bound `(updated_at, id)` keyset list
and exact-ID detail lookup. Its public summary/detail projections are constructed
types rather than serialized capture rows: provenance, routing-only evidence,
provider database identity, and the capture hash are unrepresentable. Schema v7
adds only a separate bounded append-only conversation table; it adds no capture
status, attention, checkout, CI, repository, or provider-action field.

Schema v8 separately keeps one bounded repair session and its attempts per
case. Schema v9 then adds `pr_development_threads` and immutable
`pr_development_thread_cases` membership without changing either case-owned
relation. A verified thread stores one canonical provider origin, repository
database ID, pull-request database ID, immutable pull-author database ID, and
exact case count. Its memberships assign every case one zero-based ordinal,
with unique reverse case binding and no gap through that count. Capture appends
membership under the same immediate transaction as case and empty-conversation
creation. Migration gives each pre-v9 case a different identity-less legacy
thread at ordinal zero; it never groups or upgrades old cases from connector,
repository/login spelling, URL, pull number, review identity, or timestamps.
Existing legacy `VerifyCase` and repair continue from the case's pre-v9
evidence, but those one-case threads cannot join siblings or participate in a
future thread-wide ledger without an explicit provider-verified baseline.
Exact retry lookup validates case provenance and the selected membership in one
snapshot before any provider read. A verified membership requires the complete
provider identity derived from the authenticated pull URL and stable object
IDs; an isolated legacy membership may reconcile an older routing record whose
three object IDs are all absent, but that record cannot create a new capture.
Complete thread membership reads scan only ordered link metadata and fixed-size
capture hashes; a case payload is fully validated when that case is consumed,
without loading every sibling payload merely to inspect thread membership.
These tables and stable identifiers are controller-private. Current workbench
list/detail, conversation, repair, AI context, browser selection, drafts, and
optimistic fences continue to address one `pdc_` case only.

Schema v10 adds controller-private `pr_development_thread_controllers` and
`pr_development_attempt_review_fences`. The first table has at most one stable
controller and retained-line identity per provider-verified thread and one
unique immutable owner repair session. It records a bounded material revision;
`idle`, `mutation`, `review_pending`, `review`, `ready`, or
`recovery_required` phase; an optional all-or-none exact source/workspace/line
binding; current attempt; fresh lease epoch/token/deadline; and a raw mutation
reservation only while mutation authority is live or requires recovery. First
mutation inherits the exact still-live owner-session reservation already
locking the pinned workspace; every later mutation receives a fresh controller
reservation. The
second table records at most one immutable parked-line fence per attempt, with
contiguous ordinal and line version, exact park epoch/intent, base/tip/tree,
no-change fact, local-review digest, a non-authorizing digest of the retired
mutation reservation, retired mutation lease epoch/token digest/revision replay
proof, append-once review-completion lease proof, and a domain-separated
previous/fence-hash chain. RecordFence hashes the parked/mutation core; Finish
atomically folds the review proof into the final tail hash before later fences
can chain from it. Active raw
reservations and retired reservation digests are unique store-wide so a bearer
cannot be issued again on another controller. Controller fence count, revision,
lease epoch, and digest are validated against the complete ordered chain on
every complete read. Mutation-to-review handoff
appends that fence and removes both the mutation lease and usable reservation
before a separate reservation-free review lease exists; it retains the parked
line but does not run its reviewer. Schema migration creates no controller or
fence and performs no worker, UI, Git, model, review, CI, commit, or provider
effect. First creation also atomically suppresses the immutable owner session
from the legacy schema-v8 claim queue; that is a durable ownership transfer,
not worker execution.

The case-owned attention view adds no storage or review-case field. It is
reconstructed from the submitted aggregate, trigger and decision linkage, and
private workflow task journal on every read or response. Its opaque response
fence and separate response ID are derived capabilities rather than persisted
browser authority; only the ordinary private human-task journal records an
accepted answer and continuation state.

Config schema v6 adds the non-secret operator-owned `reviews.attention`
catalog. `global` maps at most 128 canonical decision points to ordered gate
lists. `repositories` maps at most 1,024 case-insensitively unique, trimmed
`owner/repo` identities of at most 256 bytes to decision-point overrides. The
complete catalog contains at most 8,192 gate entries and at most 1 MiB of
canonical JSON. A global list is an array rather than null. A repository entry
and its decision map are objects rather than null. Override mode is explicit:
`inherit` and `disable` carry no gates, while `overlay` and `replace` carry a
nonempty validated gate list. Every gate uses the same shared workflow bounds
and JSON-compatible `questions` contract. Persistence validates every stored
layer; the trusted source additionally validates every effective
global-plus-repository composition before API save or active runtime use.
The broad `/api/config` projection omits `reviews`; its `PUT` accepts only an
empty backward-compatible placeholder, its `PATCH` rejects that field, and
both preserve the exact existing catalog across unrelated updates. The
dedicated replacement request permits that complete 1 MiB catalog plus a
fixed 64 KiB envelope allowance for its config revision and JSON field names;
the allowance cannot increase the catalog accepted by configuration or the
runtime source.

The browser policy editor holds a separate transient array-shaped draft so a
decision point or repository can be renamed and duplicate/case-collision errors
can be shown before constructing the map-shaped transport. Gate question JSON
uses a lossless token representation rather than JavaScript `number`; formatting
or round-tripping therefore cannot change a valid integer, decimal, or exponent
token. Only a validated save constructs the complete replacement envelope.
The draft's hydration revision remains the save fence even if a background read
observes a newer catalog; that newer state is retained only as an explicit
reload target until the operator discards local changes.

The immutable runtime source owns two revisions. `CatalogRevision` hashes a
domain-tagged canonical ordering of the entire detached catalog with repository
case normalized. A selected snapshot hashes a different domain-tagged value
containing only the normalized authoritative case repository, exact decision
point, selected global list, and selected local override. Thus case/order-only
catalog changes are revision-stable, an unrelated repository or decision edit
changes the catalog generation without changing an already selected decision,
and a selected-layer change deterministically changes future decision identity.
An absent decision is a valid no-op selection rather than an unavailable
policy source.

The review working-context session is not another review record and adds no
SQLite schema. It is a lazily rebuilt projection in the selected canonical
agent's ordinary local session store. Its v1 scope has channel `review` and the
single key dimension `review=<case-id>`, so the opaque key is stable for that
case and owner across case versions. Ordered internal aliases carry the exact
immutable case binding and current review version. An agent-qualified
`review:agent:<agent-id>:case:<case-id>` base alias plus exact binding-digest
and version aliases prevent the same case from
silently moving to another owner, identity, or session while allowing distinct
agents to own separate projections for that case. The projected history
contains only the authoritative ordered SQLite review messages and its summary
is always empty. Browser-safe review DTOs and browser session discovery omit
the key, revision, aliases, and gate subject; Seahorse does not bootstrap or
index review-scoped sessions. This projection exists only so an exact workflow
read-only snapshot can consume it.

JSON serialization emits only `[NOT_HERE]` for either secret format; the normal
`.security.yml`, `enc://`, and `file://` secure-string paths own the actual
value. Security-only entries cannot create or resurrect connectors removed
from JSON. Secure references are resolved only after the supported master
environment override is applied. Management edits first overlay secrets from
connectors present in the persisted JSON, then apply explicit replacements and
quietly resolve the final active candidate; this permits repair of a broken old
reference without probing it first. The master ingress switch is authoritative:
inactive references remain unresolved and load/save-stable. No configured
connector map key may contain a validated signing secret, even while inactive.

`events.ingress.channels` maps existing Delta Chat channel instance names to
`enabled`, `source`, and `mode`. `source` must be `email` and defaults to email
when omitted. `mode` is `mirror` or `event_only` and defaults to mirror.
Mirror inserts the event before retaining the existing agent turn; event-only
inserts without queueing a turn. Disabled master ingress and disabled entries
are inert. Delta Chat email is secure-by-default: both the sender contact's
bidirectional verification and the message's encrypted/signed padlock must be
present before durable ingestion. `allow_unverified_email: true` explicitly
opts an email adapter into ordinary unauthenticated mail; those events carry
`email_trust: unverified` plus separate sender-verification and
transport-authentication actor attributes. A skipped unverified mirror message
continues through the existing chat path, while event-only consumes it without
creating an actionable event. The opt-in is invalid for a chat source.

An enabled transport must supply a retry-stable message identity. Delta Chat
uses only its stable provider-local message ID for deduplication. It fetches
RFC724 Message-ID only within an incomplete-message replacement lifecycle and
retains it process-locally solely to correlate the original and candidate IDs.
An enabled adapter rejects identity-free messages instead of inventing a
collision-prone content fingerprint.

PR4 intentionally enables durable channel admission only where the transport
can defer or fail its provider acknowledgement:

| Channel type | Event admission | Provider boundary |
| --- | --- | --- |
| Delta Chat | Supported | `markseen_msgs` follows successful admission; failures remain in the ordered retry loop. |
| MQTT | Not yet supported | Existing Paho auto-ack behavior, clean sessions, and optional process-random client IDs do not provide a crash-durable ownership boundary. |
| DingTalk | Not yet supported | Chatbot delivery is fire-and-forget; its ACK is diagnostic and cannot request redelivery. |
| MaixCam | Not yet supported | The protocol has no positive application acknowledgement or documented resend-on-disconnect contract. |
| Other inbound channels | Not yet supported | Their handlers currently acknowledge, advance, or swallow delivery before a failed durable insert can be retried. |

DingTalk exposes native `msgId` and occurrence time, while MQTT and MaixCam
accept optional explicit `message_id` values. These normalized metadata fields
are groundwork for future adapters only; ordinary acknowledgement and error
behavior remains unchanged until each transport has a durable retry contract.

Channel messages use this normalized payload shape:

```json
{
  "text": "please triage this",
  "subject": "Production alert",
  "message_id": "provider-local-42",
  "reply_to_message_id": "provider-local-41",
  "attachments": [
    {
      "kind": "file",
      "filename": "report.pdf",
      "content_type": "application/pdf",
      "size_bytes": 1234
    }
  ]
}
```

The exposed adapter's envelope source is `email`, connector is the configured
instance, type is `message.received`, and its deduplication key is a SHA-256
digest rather than the raw stable provider identity. Any RFC724 Message-ID
fetched to correlate an incomplete message's full-download replacement remains
process-local; transport paths, URLs, media references, bytes, and arbitrary
routing metadata are excluded.

The signed request body has this transport-owned schema:

```json
{
  "type": "deploy.completed",
  "occurred_at": "2026-07-28T12:00:00Z",
  "actor": {"id": "ci", "type": "service"},
  "subject": {"id": "release-42", "type": "deployment"},
  "attributes": {"environment": "production"},
  "payload": {"build": 9007199254740993}
}
```

`Webhook-Id` becomes the connector-scoped deduplication key. The server owns
event ID, source, connector, receipt time, and replay lineage. Authentication
uses the exact bounded request bytes, so JSON numbers—including integers
outside JavaScript's exact range and exponent spellings—reach the durable
payload unchanged before store redaction and normalization.

With `format: github`, the same route accepts GitHub's native delivery shape
rather than the transport-owned schema above:

```http
POST /webhooks/events/primary
Content-Type: application/json
X-GitHub-Delivery: 4f30a410-...
X-GitHub-Event: pull_request
X-Hub-Signature-256: sha256=<hex HMAC>
```

The adapter verifies the exact body using HMAC-SHA256 and then submits the
complete JSON object to the ordinary payload redaction and normalization path.
`X-GitHub-Event: pull_request` plus a signed
top-level `"action": "opened"` maps to `pull_request.opened`; without a
top-level action field it maps to `pull_request`. `X-GitHub-Delivery` is the
connector-scoped deduplication key. Safe bounded `sender` metadata may become
the actor and `repository` metadata may become the subject. The authenticated
object remains the semantic source, while its durable copy is subject to the
same recursive redaction and canonical normalization as every inbox payload.

GitHub's signature covers only the body—not `X-GitHub-Event`,
`X-GitHub-Delivery`, or other headers—and includes no signed timestamp. The
adapter therefore persists `body_authenticated: "true"`,
`headers_authenticated: "false"`, and
`signature_algorithm: "hmac-sha256"` attributes. A trusted TLS boundary must
protect the route and preserve one exact value for each required header.
Workflows making security-sensitive decisions should inspect signed payload
fields in addition to routing metadata.

PicoClaw's default `events.ingress.max_payload_bytes` remains 1 MiB. GitHub
allows webhook payloads up to 25 MiB, so operators who need larger provider
events must deliberately raise the local bound and account for durable storage
and workflow-context cost. A delivery ID deduplicates while its first event is
retained; after terminal retention pruning removes that event, a later GitHub
redelivery with the same ID is admitted as a new event.

GitHub issue triage is an optional local workflow, not ingress configuration.
The operator installs it explicitly with
`picoclaw workflow install github-issue-triage` after configuring native GitHub
ingress, workflows, and an enabled non-deferred MCP server named `github` that
exposes `add_issue_comment`. The install writes
`workflows/github-issue-triage.yml` and revalidates the local workflow catalog;
it does not enable or modify any of those dependencies. The webhook signing
secret authenticates ingress only and is independent from the MCP server's
GitHub write credential. The installed trigger matches every configured native
GitHub connector by default. In a multi-connector deployment, add the intended
connector name under `on.event.connectors` and revalidate the edited workflow.

GitHub and Standard Webhooks share the same transport status policy:

| Status | Meaning |
| --- | --- |
| `202 Accepted` | A new event is durably inserted; routing continues asynchronously. |
| `200 OK` | The retained first event already owns this connector-scoped delivery ID. |
| `400 Bad Request` | Authenticated content or a durable identity is malformed or unsafe. |
| `401 Unauthorized` | Required authentication headers are missing, duplicated, malformed, or do not verify. |
| `404 Not Found` | The connector/path is unknown, disabled, or non-canonical. |
| `405 Method Not Allowed` | The route was called with a method other than `POST`. |
| `413 Content Too Large` | The request or normalized payload exceeds configured bounds. |
| `415 Unsupported Media Type` | Content type or encoding is unsupported. |
| `503 Service Unavailable` | Admission is inactive/draining or the durable insert failed retryably. |

Workflow event filters are declared under `on.event`. `sources`, `connectors`,
and `types` accept a string or string list. Optional `actor` and `subject`
filters accept `ids`, `types`, and string-list `attributes`; top-level
`attributes` filter envelope attributes. Each pattern is a fully anchored glob
where `*` spans zero or more Unicode code points and `?` spans exactly one.
Alternatives inside a list are ORed; every populated field, entity, and
attribute is ANDed. Missing entities or attributes never satisfy a wildcard.

```yaml
on:
  event:
    sources: github
    connectors: primary
    types:
      - pull_request.opened
      - pull_request.synchronize
    actor:
      types: bot
    subject:
      types: repository
      attributes:
        repository: acme/*
    attributes:
      installation: production
```

The deterministic dispatch run ID is also the file-run-store identity. The
executor creates that run exclusively, then invokes a persistence callback that
links the dispatch before any workflow step. A linked dispatch whose run file
later disappears fails closed. Connector and action stages that require
stronger guarantees must use dispatch/run identity as an idempotency key with
the external system.

## Surface Ownership

Owns: CODE pkg/eventing/**
Owns: CODE pkg/eventing/webhook/**
Owns: CODE pkg/eventing/channelmessage/**
Owns: CODE pkg/reviews/**
Owns: CODE pkg/prdevelopment/**
Owns: CODE pkg/config/config.go
Owns: CODE pkg/config/events.go
Owns: CODE pkg/config/reviews.go
Owns: CODE pkg/config/defaults.go
Owns: CODE pkg/workflows/event_trigger.go
Owns: CODE pkg/workflows/event_dispatcher.go
Owns: CODE pkg/agent/workflow_eventing.go
Owns: CODE pkg/gateway/event_automation.go
Owns: CODE pkg/gateway/review_attention_policy.go
Owns: CODE pkg/gateway/review_working_context.go
Owns: CODE pkg/gateway/event_webhook*
Owns: CODE pkg/gateway/event_channel*
Owns: CODE pkg/gateway/event_operator*
Owns: CODE cmd/picoclaw/internal/events/**
Owns: CODE web/backend/api/config.go
Owns: CODE web/backend/api/events.go
Owns: CODE web/backend/api/reviews.go
Owns: CODE web/backend/api/pr_development.go
Owns: CODE web/backend/api/review_attention_policies.go
Owns: CODE web/backend/api/review_attention_agents.go
Owns: CODE web/frontend/src/api/event-sources.ts
Owns: CODE web/frontend/src/api/events.ts
Owns: CODE web/frontend/src/api/reviews.ts
Owns: CODE web/frontend/src/api/pr-development.ts
Owns: CODE web/frontend/src/api/review-attention.ts
Owns: CODE web/frontend/src/api/review-attention-*.ts
Owns: CODE web/frontend/src/components/events/**
Owns: CODE web/frontend/src/components/reviews/**
Owns: CODE web/frontend/src/routes/event-sources.tsx
Owns: CODE web/frontend/src/routes/events.tsx
Owns: CODE web/frontend/src/routes/reviews.tsx
Owns: CONFIG.events
Owns: CONFIG.events.ingress*
Owns: CONFIG.events.ingress.webhooks*
Owns: CONFIG.events.ingress.channels*
Owns: CONFIG.reviews
Owns: CONFIG.reviews.attention*
Owns: HTTP POST /webhooks/events/*
Owns: HTTP GET /runtime/eventing/*
Owns: HTTP POST /runtime/eventing/events/*/replay
Owns: HTTP /runtime/eventing/reviews*
Owns: HTTP GET /runtime/eventing/pr-development*
Owns: HTTP POST /runtime/eventing/pr-development/*/chat
Owns: HTTP POST /runtime/eventing/pr-development/*/repair
Owns: HTTP /api/events*
Owns: HTTP /api/reviews*
Owns: HTTP GET /api/pr-development*
Owns: HTTP POST /api/pr-development/*/chat
Owns: HTTP POST /api/pr-development/*/repair
Owns: HTTP /api/reviews/attention-policies
Owns: CLI cmd/picoclaw/internal/events/*
Owns: TEST pkg/eventing/*
Owns: TEST pkg/eventing/webhook/*
Owns: TEST pkg/eventing/channelmessage/*
Owns: TEST pkg/reviews/*
Owns: TEST pkg/prdevelopment/*
Owns: TEST pkg/config/events*
Owns: TEST pkg/config/reviews*
Owns: TEST pkg/workflows/event_trigger_test.go
Owns: TEST pkg/workflows/event_dispatcher_test.go
Owns: TEST pkg/gateway/event_automation_test.go
Owns: TEST pkg/gateway/event_review_readiness_test.go
Owns: TEST pkg/gateway/pr_development_repair_runtime_test.go
Owns: TEST pkg/gateway/pr_development_capture_test.go
Owns: TEST pkg/gateway/review_working_context_test.go
Owns: TEST pkg/gateway/event_webhook_test.go
Owns: TEST pkg/gateway/event_channel_test.go
Owns: TEST pkg/gateway/event_operator_test.go
Owns: TEST cmd/picoclaw/internal/events/*
Owns: TEST web/backend/api/config_test.go
Owns: TEST web/backend/api/config_event_channel_test.go
Owns: TEST web/backend/api/events_test.go
Owns: TEST web/backend/api/reviews_test.go
Owns: TEST web/backend/api/pr_development_test.go
Owns: TEST web/backend/api/review_attention_policies_test.go
Owns: TEST web/backend/api/review_attention_agents_test.go
Owns: TEST web/frontend/src/api/event-sources.test.ts
Owns: TEST web/frontend/src/api/events.test.ts
Owns: TEST web/frontend/src/components/events/event-sources-page.test.tsx
Owns: TEST web/frontend/src/components/events/*
Owns: TEST web/frontend/src/api/reviews.test.ts
Owns: TEST web/frontend/src/api/pr-development.test.ts
Owns: TEST web/frontend/src/api/review-attention.test.ts
Owns: TEST web/frontend/src/api/review-attention-*.test.ts
Owns: TEST web/frontend/src/components/reviews/*
Owns: TEST web/frontend/src/routes/-reviews*
Owns: TEST web/frontend/tests/ui-smoke.spec.ts

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `events.ingress.enabled` | Opt-in master switch; omitted and explicit `false` preserve the pre-feature runtime and create no database. | `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-012`, `FR-EVENT-AUTOMATION-035` |
| Config | `events.ingress.database_path`, `retention_days`, `max_payload_bytes`, `redact_fields` | Resolve a safe workspace database default while preserving explicit policy values used by store construction and ingest/retention calls. | `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-003`, `FR-EVENT-AUTOMATION-011`, `FR-EVENT-AUTOMATION-035` |
| Config | `events.ingress.webhooks.<connector>` | Opt-in connector name, enablement, `standard`/`github` format, securely persisted format-specific secret, and GitHub-only repository allowlist, target user, and notification-polling switch; omitted format remains Standard Webhooks and an empty GitHub repository list accepts all. Enabled values are validated before storage or route construction and passed to durable exact-secret redaction. A GitHub poll-only connector may omit its signing secret and therefore receives no webhook route. The target user also selects body-authenticated submitted feedback from a different reviewer on pull requests authored by that user, as routing metadata only. | `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-024`, `FR-EVENT-AUTOMATION-030`, `FR-EVENT-AUTOMATION-035`, `FR-EVENT-AUTOMATION-039`, `FR-EVENT-AUTOMATION-040`, `FR-EVENT-AUTOMATION-050` |
| Config | `events.ingress.channels.<channel-instance>` | Opt-in source and mirror/event-only mode for one existing enabled channel instance, with Delta Chat email defaults and enabled-load/API validation. | `FR-EVENT-AUTOMATION-025`, `FR-EVENT-AUTOMATION-035` |
| Config | `reviews.attention.global`, `reviews.attention.repositories.<owner/repo>` | Persist bounded operator-owned decision-point gate lists plus explicit repository inherit/overlay/replace/disable overrides; reviewed repositories cannot supply or weaken this policy. | `FR-EVENT-AUTOMATION-045`, `FR-EVENT-AUTOMATION-046` |
| HTTP | `GET /api/config`, `PUT /api/config`, `PATCH /api/config` | The authenticated update-safe read projection masks configured webhook secrets, round-trips public GitHub repository/target scope, permits repair of unresolved references/dependencies, and opaquely refuses any public event identity containing a configured sensitive value. It omits the dedicated `reviews` policy subresource; broad PUT accepts only an empty compatibility placeholder, broad PATCH rejects that field, and both preserve the exact catalog during unrelated updates. Omitted or `[NOT_HERE]` webhook secrets preserve the current secure value; an explicit valid secret rotates it, an explicit empty value can clear only a disabled connector, GitHub scope fields use ordinary merge-patch replace/null-clear semantics, and a null map value removes the connector and its security overlay. | `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-024`, `FR-EVENT-AUTOMATION-035`, `FR-EVENT-AUTOMATION-039`, `FR-EVENT-AUTOMATION-046` |
| HTTP | `POST /webhooks/events/{connector}` | Strict, bounded format-selected Standard Webhooks or native GitHub authentication and normalization with durable admitted `202`/duplicate `200` acknowledgement, authenticated GitHub repository-scope `202` ignore without an event ID, retry-safe error statuses, and deterministic own-PR submitted-review targeting from the signed body plus local target. | `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-023`, `FR-EVENT-AUTOMATION-030`, `FR-EVENT-AUTOMATION-039`, `FR-EVENT-AUTOMATION-050` |
| HTTP | `/runtime/eventing/*`, launcher `/api/events*` proxy | PID-bearer-protected, generation-fenced event/dispatch list and exact token-free metadata inspection, exact opt-in payload text, explicit additive replay, and an internal atomic redacted workflow-context read; the launcher substitutes its authenticated session boundary without forwarding browser credentials and exposes workflow context only through bounded match/test operations. | `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-033`, `FR-EVENT-AUTOMATION-036`, `FR-EVENT-AUTOMATION-037` |
| HTTP | `/runtime/eventing/reviews*`, launcher `/api/reviews*` proxy | Protected safe case list/detail, optimistic finding edit/drop/restore, bounded durable chat/rephrase, explicit submission, human-only unknown-outcome reconciliation, and case-owned attention projection/response routes. The launcher peeks PID metadata without lifecycle side effects and requires a nonzero port plus a numeric loopback or literal current local-interface host; hostname, wildcard/unspecified, multicast or remote numeric, and incomplete authority fail before bearer request construction. It validates canonical routes/query/body/origin, injects only the process bearer, blocks proxies and redirects, bounds ordinary/AI timeouts and review responses to 32 MiB, and preserves safe upstream status/JSON without forwarding browser authorization. | `FR-EVENT-AUTOMATION-042`, `FR-EVENT-AUTOMATION-043`, `FR-EVENT-AUTOMATION-049` |
| HTTP | `GET /runtime/eventing/pr-development`, `GET /runtime/eventing/pr-development/{pdc_...}`; launcher `/api/pr-development` equivalents | Generation-fenced read-only newest-first development-case summaries and exact safe detail. The launcher substitutes the managed process bearer, rejects every noncanonical path/query/method, disables proxies and redirects, bounds the response, and returns only strict JSON without forwarding browser authority. | `FR-EVENT-AUTOMATION-052` |
| HTTP | `POST /runtime/eventing/pr-development/{pdc_...}/chat`; launcher `POST /api/pr-development/{pdc_...}/chat` equivalent | The launcher admits one exact same-origin, strict two-key JSON mutation before PID access, replaces browser authority, and waits at most 120 seconds. The protected runtime rejects every browser-provenance header before store/model access, repeats the exact path, transport, Unicode, and JSON checks, and runs on the shared server whose 135-second write timeout exceeds that application budget. It returns success or fixed error JSON; error detail exists only after a separate fresh bounded authoritative reload. Neither boundary grants repository or provider action authority. | `FR-EVENT-AUTOMATION-053` |
| HTTP | `GET /runtime/eventing/reviews/{case}/attention`, `POST /runtime/eventing/reviews/{case}/attention/respond`; launcher `/api` equivalents | Project the validated case-owned attention lifecycle and at most one actionable opaque SHA-256 response fence; resolve all private linkage server-side, resume only the exact waiting/recovery task, recover an exact persisted answer idempotently, and return the authoritative projection without mutating review state. | `FR-EVENT-AUTOMATION-049` |
| HTTP | `GET`, `PUT /api/reviews/attention-policies` | Authenticated bounded policy-only projection and same-origin strict full replacement, fenced by the opaque public-plus-security revision, persisted by an atomic raw public-JSON patch that leaves unrelated values and the security sidecar unchanged, and returning canonical catalog revision plus current gateway effect. Browser clients parse and serialize the complete transport losslessly rather than rounding arbitrary question numbers. | `FR-EVENT-AUTOMATION-046`, `FR-EVENT-AUTOMATION-047` |
| HTTP | `GET /api/reviews/attention-agents` | Authenticated fixed-256-page identity-only projection in canonical ID order. One strong policy-generation `If-Match` plus an optional canonical decimal offset fences every page to the same public-plus-security config revision; stale generations conflict and reads never migrate or write. | `FR-EVENT-AUTOMATION-046` |
| HTTP | `/api/workflows/development/triggers/*`, `/api/workflows/development/event-trigger/*`, `/api/workflows/development/test`, `/api/workflows/definitions/inspect`, `/api/workflows/templates/{name}/inspect`, `/api/workflows/runs*` | Stateless server-parsed generic trigger projection/revision with event-specific compatibility and match routes, deterministic metadata-only event preview, event-ID-only draft-test context resolved through the protected live gateway, path-free inspection of declared event filters/actions/possible effects without source or captured payload values, and safe workflow run projection whose origin is validated against every retained parent/retry ancestor while respecting independent pruning boundaries. Browser request DTOs cannot supply origin. Exact reserved review-attention runs are omitted/not found; direct hidden relationships are scrubbed from visible ordinary runs and graphs; and cancel/retry/task mutation is denied for the normalized transitive parent/child/retry component. Production web and CLI retention preserve exact reserved-reference runs while related ordinary runs retain configured retention. | `FR-EVENT-AUTOMATION-036`, `FR-EVENT-AUTOMATION-038`, `FR-EVENT-AUTOMATION-049` |
| CLI | `picoclaw events list|get|payload|dispatches|replay` | Call the live protected gateway using the local PID credential, print bounded projected JSON, emit an explicitly requested payload's validated object bytes exactly, and require `--yes` before a non-retried replay. | `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-033` |
| Frontend | authenticated `/events` dashboard route | Responsive URL-bound event and global dispatch master/detail inspection, exact ID/ref-only event/dispatch/workflow/run relationship links, selected-event dispatch history, explicit exact-text payload reveal, and warned non-retried replay through launcher-owned authenticated endpoints. Related workflow run pages return links only from validated origin. | `FR-EVENT-AUTOMATION-034`, `FR-EVENT-AUTOMATION-037`, `FR-EVENT-AUTOMATION-038` |
| Frontend | authenticated `/event-sources` route and Events-page link | Responsive visual management of the ingress master/policy, secure Standard Webhooks/GitHub connector CRUD, GitHub target user and newline-delimited watched-repository editing/local text import, eligible Delta Chat email adapters, dependency and endpoint warnings, scoped save, and restart-required feedback. The target-user hint covers authored-PR submitted feedback and states that webhook targeting is routing metadata only. | `FR-EVENT-AUTOMATION-035`, `FR-EVENT-AUTOMATION-039`, `FR-EVENT-AUTOMATION-050` |
| Frontend | authenticated `/reviews` route | Responsive case master/detail workbench with status/repository filters, pagination, finding edit/drop/restore, case/finding chat, rephrase preview and explicit apply, stale-version reload that preserves local text, all-dropped resolution, explicit submission confirmation, durable status polling, terminal-state locks, and warned human reconciliation of unknown submissions as found or verified absent. Its canonical policy view provides memory-only structured global/repository gate editing, lossless question JSON, effective previews, config-CAS conflict handling, unsaved-navigation protection, restart feedback, and accurate outgoing-submission trigger scope without launching a decision during policy work. The canonical `/reviews?case={case}&focus=chat` handoff selects the case and focuses its existing conversation card after render, where bounded attention history, status, questions, and the current in-memory response editor appear without placing response authority in the URL or browser storage. | `FR-EVENT-AUTOMATION-042`, `FR-EVENT-AUTOMATION-043`, `FR-EVENT-AUTOMATION-047`, `FR-EVENT-AUTOMATION-048`, `FR-EVENT-AUTOMATION-049` |
| Frontend | authenticated `/reviews?view=development` route | Responsive own-PR feedback master/detail view with optional exact repository and positive pull-number filters, cursor pagination, canonical `pdc_` selection, captured-snapshot labels, deliberate HTTPS PR/review links, plain-text feedback/messages, and an explicit advisory conversation. Per-case detail, draft, mutation, and ambiguity state remain in memory; Go-compatible trimming, strict response binding, monotonic detail adoption, committed-human/completed-turn reload recovery, live-log/error announcements, retained detail on refresh failure, and mobile focus transfer prevent stale or ambiguous responses from losing or duplicating work. | `FR-EVENT-AUTOMATION-052`, `FR-EVENT-AUTOMATION-053` |
| Frontend | authenticated `/agent/workflows` trigger builder, definition/template inspector, captured-event test context, and run detail | Server-projected deterministic event-filter controls within the all-family builder, raw-YAML fallback for authoring, path-free read-only inspection of declared event filters/actions/possible effects, payload-free event selection and field checks, explicit ephemeral payload reveal, event-ID-only production-parity draft testing, and validated payload-free event/dispatch/family-root links beside independent cancellation/completion lifecycle fields. | `FR-EVENT-AUTOMATION-036`, `FR-EVENT-AUTOMATION-038` |
| File | `workspace/workflow_runs/<run_id>/run.json` | Normal workflow run persistence includes optional trusted payload-free `RunOrigin`; it remains separate from envelope/dispatch storage and retry metadata. | `FR-EVENT-AUTOMATION-015`, `FR-EVENT-AUTOMATION-017`, `FR-EVENT-AUTOMATION-038` |
| Go API | `bus.InboundAdmission`, `MessageBus.PublishInboundWithPreparation` | Synchronous detached channel-origin admission before queue and conditional turn UX; internal messages and unconfigured channels preserve direct queueing. | `FR-EVENT-AUTOMATION-027` |
| Go API | `pkg/eventing/channelmessage.Backend`, `Controller` | Bounded safe message normalization, hashed deduplication, synchronous store insertion, mirror/event-only decision, and exact prepared-generation activation/drain. | `FR-EVENT-AUTOMATION-026`, `FR-EVENT-AUTOMATION-027`, `FR-EVENT-AUTOMATION-029` |
| Runtime | Delta Chat ordered provider queue, notification events, and acknowledgement loop | Drain `get_next_msgs` on startup and provider wake events, correlate full-download replacement IDs process-locally, retry strictly in order before cursor advancement, and expose only safe event metadata. | `FR-EVENT-AUTOMATION-028` |
| Go API | `pkg/eventing.Envelope` | Source-neutral immutable external-event input and stored representation with connector-scoped deduplication and optional replay lineage. | `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004`, `FR-EVENT-AUTOMATION-010` |
| Go API | `pkg/eventing.Inbox` / `Store` | `Inbox` defines `Insert`, `Get`, newest-first filtered keyset list, routing claim/ack/nack/dead transitions, dispatch create/get/claim/link/nack/finish/keyset-list, `Replay`, bounded `Prune`, and `Close`; `Store` is its SQLite implementation. The contract provides atomic deduplication and fresh-token fenced leases. | `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004`, `FR-EVENT-AUTOMATION-006` through `FR-EVENT-AUTOMATION-011` |
| Go API / storage | `eventing.ReviewStore`, `reviews.CaptureSink`, `reviews.Service` | Idempotent trusted workflow-draft capture, aggregate inspection, optimistic finding/message mutation, immutable submission creation, safe browser projection, fenced durable submission delivery over the review tables introduced through schema v3, and transactional outgoing-submission attention occurrence creation. | `FR-EVENT-AUTOMATION-041`, `FR-EVENT-AUTOMATION-042`, `FR-EVENT-AUTOMATION-043`, `FR-EVENT-AUTOMATION-048` |
| Internal Go API / storage | `prdevelopment.CaptureSink`, `prdevelopment.GitHubVerifier`, `eventing.PRDevelopmentCaseStore` | Recognize only the exact installed own-PR workflow's versioned opt-in output, reconcile immutable capture provenance, directly match the provider-returned author ID, cross-bind the signed numeric repository/pull IDs and exact review occurrence to one canonical-origin bounded current GitHub PR/review snapshot, and idempotently persist a review-level local development case before dispatch acknowledgement. | `FR-EVENT-AUTOMATION-051` |
| Internal Go API / storage | `eventing.PRDevelopmentThreadReader`, `eventing.PRDevelopmentCaseStore`, private schema-v9 thread and membership tables | Resolve only the canonical origin plus authenticated and provider-cross-bound repository/pull identity, enforce the exactly matched author invariant, append one distinct case at the next contiguous ordinal in the capture transaction, and isolate each legacy case without inventing provider evidence. | `FR-EVENT-AUTOMATION-057` |
| Internal Go API / storage | `eventing.PRDevelopmentControllerReader`, `eventing.PRDevelopmentControllerStore`, private schema-v10 controller and attempt-review-fence tables | Own one stable verified-thread controller and retained-line identity, inherit the first pinned-workspace reservation then issue fresh reservations for later resumes, fence mutation and reservation-free review with distinct leases, redact bearer authority from Reader snapshots, bind exact caller-supplied line evidence, atomically retire mutation authority before publishing one chained review fence, finalize its review-proof hash, safely reclaim review only, and mark an expired mutation owner for recovery without invoking any worker or local/provider effect. | `FR-EVENT-AUTOMATION-058` |
| Go API / storage | `eventing.PRDevelopmentCaseStore`, `prdevelopment` read service and handler | List immutable development cases by exact repository/pull filters with a newest-first `(updated_at, id)` keyset cursor, load one exact case, and project bounded public summary/detail DTOs without capture provenance or action authority. | `FR-EVENT-AUTOMATION-052` |
| Go API / storage | `eventing.PRDevelopmentConversationStore`, `prdevelopment` chat service and handler | Atomically create/backfill a two-table conversation, validate its contiguous count, byte high-water, and rolling canonical digest on every read/append, and leave capture ordering untouched. After store reads, a process-wide same-case lock and per-service AI admission reject stale state or insufficient complete-turn capacity before appending the human; every later failure preserves that row, and only a fresh handler reload may declassify partial authoritative detail. The model request uses one isolated advisory prompt over explicit bounded captured evidence and transcript. | `FR-EVENT-AUTOMATION-053` |
| Internal Go API | `prdevelopment.GitHubVerifier.VerifyCase`, `agent.LocalRepairRunner`, `gitworkspace.Manager.AcquirePinned` | Independently refresh one immutable case into actionable current provider/head authority, then lend an already-resolved concrete model only four guarded repository-content tools over the exact controller pin with serialized mutations and unconditional pin postflight. | `FR-EVENT-AUTOMATION-054` |
| HTTP / Go API / storage | `POST /runtime/eventing/pr-development/{pdc_...}/repair`, `eventing.PRDevelopmentRepairAdmitter`, `eventing.PRDevelopmentRepairQueue`, `prdevelopment.RepairWorker` | Atomically admit one explicit revision-fenced idempotent repair attempt, project its independently monotonic public lifecycle, claim and renew preparation/execution with at-most-once ambiguity handling, reverify and durably pin current GitHub facts, and invoke one exact controller repair runtime while preserving the locked checkout. | `FR-EVENT-AUTOMATION-055` |
| Controller Go API | `gitworkspace.Manager.WithPinnedOperation`, `SnapshotPinnedCandidate`, `CommitPinned` | Serialize trusted local filesystem work through a callback-scoped derived context and turn exact prevalidated candidate evidence into one deterministic local commit that reconciles completed-subprocess crash boundaries without publication or browser/model authority. | `FR-EVENT-AUTOMATION-056` |
| Internal Go API | `reviews.Service.WithWorkingContext`, `reviews.WorkingContextRuntimeAcquire` | Under one per-case projection lock and exact runtime-generation lease, load one authoritative atomic review aggregate, compare-and-swap and strictly verify its hidden stable agent-owned session, then synchronously hand its case version, internal key, exact session revision, and detached JSON-native gate subject to a trusted consumer. | `FR-EVENT-AUTOMATION-044` |
| Internal Go API / storage | `reviews.AttentionLauncher`, `reviews.AttentionPolicySource`, `eventing.ReviewDecisionRunStore` | Hold one trusted policy generation while resolving a repository overlay, bind its digest to an exact review decision, compile ordinary workflow gates, and atomically fence the exact case version plus immutable decision-to-private-run link through durable run creation. | `FR-EVENT-AUTOMATION-045` |
| Internal Go API / storage | `eventing.ReviewAttentionTriggerQueue`, `reviews.AttentionTriggerWorker` | Transactionally record the outgoing submitted-review occurrence, claim/renew it with fresh lease fencing, pin one canonical trusted effective policy before effects, retry pre-admission failure with bounded detail, and finish as one exact private run delivery or effect-free no-op. | `FR-EVENT-AUTOMATION-048` |
| Internal Go API | `reviews.AttentionBridge.Project`, `reviews.AttentionBridge.Respond`, `reviews.IsAttentionWorkflowRun`, `reviews.PruneTerminalWorkflowRunsExceptAttention` | Validate the status-specific submitted-case authority chain and task payload hash, derive the bounded public lifecycle and response fence, idempotently resume the exact current task with a separate response ID, identify the exact reserved workflow reference for generic-surface suppression, and preserve its restart/replay authority from ordinary retention. | `FR-EVENT-AUTOMATION-049` |
| Internal Go API | `reviews.ConfigAttentionPolicySource` | Validate and detach the operator catalog, case-fold trusted repository selection, expose stable full-catalog and selection-scoped revisions, and enumerate configured AI/working-context agents for runtime preflight. | `FR-EVENT-AUTOMATION-046` |
| Runtime | `eventing/githubpoll.Poller`, gateway notification worker | Exact-tool, bounded, read-only GitHub notification scans that enrich and admit trusted provider events through the ordinary generation-owned event store without changing provider notification state. | `FR-EVENT-AUTOMATION-040` |
| Runtime | `reviews.SubmissionWorker`, `reviews.GitHubSubmitter` | Claim exactly one immutable submission, heartbeat its lease, verify the current PR head through the exact read tool, execute the create-pending/add-comment/submit-pending MCP protocol once only on a match, and persist stale/submitted/typed-pre-write-failed/unknown without automatic external retries. Successful submitted persistence atomically records the separate local attention occurrence. | `FR-EVENT-AUTOMATION-043`, `FR-EVENT-AUTOMATION-048` |
| Storage | `pkg/eventing.Open` / `OpenStore`, `WithMaxPayloadBytes`, `WithClock`, `WithBusyTimeout`, `WithRedaction` | Open the embedded store with transactional `PRAGMA user_version` migration, WAL, foreign keys, busy handling, one authoritative SQLite connection, restrictive permissions, restart persistence, a one-MiB default payload limit, mandatory/custom redaction, optional exact-secret replacement, and deterministic test clocks on supported targets. | `FR-EVENT-AUTOMATION-003`, `FR-EVENT-AUTOMATION-005` through `FR-EVENT-AUTOMATION-011` |
| Go API | `pkg/eventing.RoutingDispatchCreator`, `RevisionRoutingDispatchCreator`, `RoutingLeaseRenewer`, `DispatchLeaseRenewer` | Additive capabilities that create a dispatch only through the current routing claim, atomically bind new routed work to its exact workflow revision, and renew current live leases without expanding the compatibility-critical `Inbox` interface. Routing renewal remains optional; durable workflow routing requires revision-bound creation and `EventDispatchInbox` requires dispatch renewal because interrupted-run cancellation cannot otherwise be fenced across stores. | `FR-EVENT-AUTOMATION-008`, `FR-EVENT-AUTOMATION-014`, `FR-EVENT-AUTOMATION-018` |
| Workflow YAML | `on.event` | Typed source/connector/type/entity/attribute filters with scalar/list syntax, explicit non-empty validation, anchored globs, and deterministic case rules. | `FR-EVENT-AUTOMATION-013`, `FR-EVENT-AUTOMATION-021` |
| CLI / Workflow YAML | `picoclaw workflow install github-issue-triage` / `workflows/github-issue-triage.yml` | Explicitly install the deterministic native GitHub issue trigger, isolated structured classifier, and declared conditional GitHub comment action without changing existing configuration. | `FR-EVENT-AUTOMATION-031` |
| CLI / Workflow YAML | `picoclaw workflow install github-pr-review` / `workflows/github-pr-review.yml`, native `git.diff` | Explicitly install the authenticated targeted review-request trigger, exact head/base-repository and merge-base verification, bounded path-relative unified-diff construction, a no-tools structured review step, and reserved durable draft output. | `FR-EVENT-AUTOMATION-041` |
| CLI / Workflow YAML | `picoclaw workflow install github-pr-development` / `workflows/github-pr-development.yml` | Explicitly install the authenticated own-PR submitted-review trigger and read-only current-PR step whose versioned successful-run marker opts into provider-verified durable capture. | `FR-EVENT-AUTOMATION-051` |
| Go API | `EvaluateEventTrigger`, `EventWorkflowRouter`, `EventWorkflowDispatcher`, `RunOrigin`, `RunRequest.Origin`, `RunRequest.OnRunPersisted`, `Executor.RetryCaptured`, `ProjectWorkflowRunForBrowserWithStore`, `ProjectEventBackedDraftRunsForBrowserWithStore`, `LoadRunnableLocalSnapshotWithRevision`, `EventContextFromEnvelope` | Produce deterministic field-level match diagnostics through the same evaluator used by routing, claim one item, compatibility-check and revision-bind the exact loaded workflow bytes, durably fan out deterministic dispatches, reject content drift or a no-longer-matching persisted event, pass the same accepted snapshot and trusted payload-free origin into execution, reconcile deterministic runs, link a newly persisted run before effects, renew long leases, retry from one authoritative source, validate every available provenance ancestor without treating pruning as forgery, safely project exact and batch trusted run provenance, and build the detached redacted workflow context shared by dispatch and event-parity draft testing. | `FR-EVENT-AUTOMATION-014` through `FR-EVENT-AUTOMATION-018`, `FR-EVENT-AUTOMATION-020`, `FR-EVENT-AUTOMATION-021`, `FR-EVENT-AUTOMATION-036`, `FR-EVENT-AUTOMATION-038` |
| Runtime | gateway event automation service, webhook/operator/review/development controllers, review working-context bridge, attention launcher and browser bridge, channel admission controller, provider poller, submission and attention-trigger workers, own-PR capture sink, and launcher restart signature | Open enabled storage only after attention-policy/agent preflight, initialize workflow/MCP/runtime-session dependencies before workers, generation-fence HTTP and advisory development chat, review-session projection, trusted attention policy, outgoing-submission attention launch and response, provider-verified own-PR capture and safe projection, and channel admission while transactionally draining, replacing, rolling back, and closing services/providers, and report restart-required only when the active effective event runtime changes, including semantic changes to enabled GitHub repository/target/polling/policy scope but excluding case/order-only scope differences. | `FR-EVENT-AUTOMATION-019`, `FR-EVENT-AUTOMATION-024`, `FR-EVENT-AUTOMATION-029`, `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-035`, `FR-EVENT-AUTOMATION-039` through `FR-EVENT-AUTOMATION-053` |
| Build | `pkg/eventing` unsupported-platform implementation | Preserves the same construction and development-case capture/read/conversation store surface and returns `ErrUnsupportedPlatform` without pulling SQLite into excluded targets. | `FR-EVENT-AUTOMATION-005`, `FR-EVENT-AUTOMATION-012`, `FR-EVENT-AUTOMATION-051` through `FR-EVENT-AUTOMATION-053` |

## Algorithms And Ordering

1. Resolve `EventIngressConfig` without side effects. If disabled, stop before
   validating inert webhook/channel entries, creating directories, opening
   SQLite, registering routes/hooks, or starting goroutines. Otherwise validate
   connector names, case collisions, channel instance/source/mode references,
   enabled signing secrets, GitHub-only repository/target scope, and shared-route
   ownership before opening the store.
2. For enabled configuration, resolve an explicit database path or derive
   `<workspace>/eventing/events.db`, validate positive resource policy, create
   positive defaults for non-positive limits, append configured redaction fields
   to the mandatory set, create only the narrow parent directory, and open the
   store with owner-only file permissions.
3. On supported targets, configure every SQLite connection for foreign keys and
   a five-second default busy timeout, enable WAL with normal synchronization,
   constrain the single-node store to one authoritative connection, read the
   schema version, reject newer schemas, and apply each missing migration
   transactionally. Before completing `Open`, validate that the current version
   has every required table, column, and index; roll back the version and all
   objects created by a failed migration.
4. Before ingestion, copy and validate identity fields and timestamps, verify
   UTF-8 payload JSON and byte size, recursively redact configured object
   fields, reject exact secrets in structural keys, and canonicalize the
   detached payload without changing an existing redaction marker.
5. Atomically insert one inbox row containing the immutable envelope and its
   initial pending routing columns. Resolve a uniqueness conflict by reading and
   returning the original row with a duplicate indication; never update it from
   the retried envelope.
6. Claim routing work in deterministic order. Atomically select only pending
   work whose `available_at` is due or expired claimed work, generate a new
   opaque lease token per record whose diagnostic prefix is derived from the
   caller's worker label, write the token and deadline, increment the attempt,
   and return detached event/claim values.
7. Accept routing transitions only when event ID, fresh lease token, current
   claimed state, and unexpired lease still match. Ack success, dead-letter
   terminal failure, or nack to pending with an explicit future availability
   time without touching envelope data.
8. Insert selected workflow dispatches idempotently by event/workflow identity
   only while the authorizing routing claim is current, derive stable `dsp_` and
   `wr_` IDs from that pair, and atomically retain the first exact non-empty
   workflow-content revision with the dispatch before any future executor is
   allowed to launch a workflow. A claimed dispatcher can link only that
   expected run ID after its run record exists and before effects.
9. Claim and transition dispatches with the same fresh-token lease fencing and
   availability rules as routing. Link only the deterministic expected run ID;
   nack retry to pending or finish with a terminal state. Routing completion
   does not imply dispatch completion and vice versa.
10. Replay by reading the immutable source event, deriving a fresh `ev_` ID and
    a `replay/<new-event-id>` deduplication identity, linking `replay_of` to that
    immediate source ID, and inserting through the normal validated ingest
    path.
11. Retain by selecting only records older than the cutoff whose routing and all
    dispatch work is terminal and which are not referenced by a retained replay,
    outgoing review case, or own-PR development case, ordering oldest first,
    capping work at the requested maximum, and cascading deletion to terminal
    dispatch rows.
12. List events and dispatches newest first with stable timestamp-plus-ID keyset
    cursors. Use 50 rows when a list limit is omitted and cap list, claim, and
    prune batches at 500 rows.
13. When gateway ingress is enabled, open and validate the store synchronously,
    include every enabled webhook secret in exact-value redaction, and build an
    inactive candidate webhook backend with each connector's effective format
    and an immutable case-folded GitHub repository set and target user. If
    workflows are disabled, keep the store open for connector/operations
    ownership but start no routing or dispatch goroutine.
14. A router claims one event, renews its routing lease while reading the current
    local catalog, loads each candidate as one exact compatibility-approved,
    parsed, validated byte snapshot, evaluates that snapshot's deterministic
    `on.event` filters, and atomically fences every revision-bound idempotent
    dispatch insert through the live routing token. Acknowledge only after the
    complete fan-out is durable; nack live-claim failures with capped
    exponential backoff and dead-letter exhausted routing.
15. A dispatcher claims one delivery, renews immediately, and holds a heartbeat
    through event/run lookup. It renews synchronously again after run lookup and
    before reconciling its deterministic run ID. Terminal runs finish the
    dispatch consistently; an orphan running or unknown run is canceled as
    interrupted only while that renewed token is still current and is never
    executed again. A dispatch already linked to a now-missing run fails closed.
    A new run receives a trusted production origin containing this exact event,
    dispatch, and deterministic root-run identity.
16. If no run exists, load one exact current runnable workflow snapshot, reject
    drift from a non-empty stored revision, and re-evaluate the persisted
    envelope's trigger against that same snapshot. A legacy unbound dispatch
    proceeds only if it still matches. Build detached redacted event/input
    context and payload-free origin, renew the dispatch lease, and call the
    shared executor with that exact workflow object, run ID, origin, and an
    `OnRunPersisted` callback. The executor validates the origin, exclusively
    creates the run, invokes the callback to link and renew the dispatch, and
    only then starts lifecycle callbacks or workflow steps.
    Creation returns only after the run file and every directory entry needed
    to find it are synced; later state updates atomically replace the prior valid
    JSON record. A callback or ordinary workflow failure leaves a terminal run
    and a failed dispatch; only a failure before durable run creation can retry.
17. During execution, renew a capable store's dispatch lease every one-third of
    its duration. Cancel the execution context immediately on renewal failure;
    never let a stale worker finish or nack work owned by a replacement token.
18. On reload, prepare the exact candidate channel connector set, mark readiness
    false, and deactivate webhook admission before pausing long-lived runtime
    users. Deactivate channel admission and wait for every webhook request and
    channel insert admitted by the old generations before closing their store,
    then swap runtime state while retaining the prior provider/config. Build but
    do not activate candidate backends; fence each replacement
    router/dispatcher iteration to that exact config generation and start cron
    only after all other fallible replacement initialization. After the
    irreversible aggregate commit point, publish channel and webhook candidates
    sequentially through no-fail commits immediately before readiness; one
    transport may be observable first during that bounded scheduling interval.
    Only then close the retained provider after active work drains. Otherwise
    cancel/drain partial workers, prepare and fully restart the old runtime,
    activate its matching backends, and reject candidate scheduled work before
    resuming. Keep webhook routes retryable with `503` and readiness false if
    recovery cannot complete. Shutdown follows the same admission-drain
    boundary before worker/store close, then drains AgentLoop generation users,
    channel/media dependencies, and provider state.
19. For HTTP admission, require the escaped path to equal the literal decoded
    ASCII route before selecting its named connector. Reject percent-encoded
    aliases, unsupported method/media/content encoding, and cap the raw body.
    For `standard`, require exactly one Standard Webhooks ID, timestamp, and
    signature header, then verify timestamp tolerance and HMAC over the exact
    raw body. For `github`, require exactly one delivery ID, event type, and
    `sha256=` signature header and verify HMAC-SHA256 over the exact raw body.
    Authentication always uses only that connector's prevalidated secret and
    precedes JSON decoding.
20. For `standard`, decode exactly one strict transport object, reject
    transport/server-owned or unknown fields, require an object payload, and
    map `Webhook-Id` to `dedupe_key`, source to `webhook`, and connector to the
    route name. For `github`, require the complete body to be one object, pass
    it as payload through ordinary redaction and normalization, derive source
    `github`, deduplication from `X-GitHub-Delivery`, and type from
    `X-GitHub-Event` plus a non-empty signed top-level action. Promote only
    bounded sender, repository, pull-request, issue, comment, review, reviewer,
    team, and assignee projections and persist that the body is authenticated
    while headers are not. For a configured target, derive case-insensitive
    active requested-reviewer, assignee, and mention reasons. For the exact
    submitted-review event hint and signed action, also project canonical review
    and fork-head routing facts, compare the signed PR/review authors to that
    target, and add `review_feedback` only for a complete non-self review on the
    target's own PR; do not infer a requested-reviewer reason from
    review-request removal or an assignee reason from unassignment. Reject
    configured signing-secret substrings in
    connector, type, action, or deduplication identities before repository
    scope selection instead of persisting or rewriting them. An empty GitHub
    scope admits all; otherwise case-fold the authenticated
    `repository.full_name` and require membership. A missing/nonmatching
    repository returns `202` with `ignored: true`, `inserted: false`, and no
    `event_id` without calling the store. Insert admitted input synchronously;
    return `202` for a new row or `200` with the original ID for a duplicate.
    Workflow routing and execution remain asynchronous after a durable admitted
    acknowledgement.
21. After channel authorization and group-trigger filtering, normalize only
    allow-listed message, subject, actor, conversation, reply, occurrence, and
    attachment metadata. Derive a fixed-length deduplication key from length-prefixed
    account/conversation/topic/stable-message identity, bound input work by the
    payload policy, and insert synchronously through the ordinary redacting
    store.
22. If insertion succeeds, consume `event_only` without agent-turn UX and
    release its media scope or, for `mirror`, create the existing
    typing/reaction/placeholder UX immediately before queueing the unchanged
    normalized message. The manager serializes this per-chat provider
    transition: it detaches and gives the previous exact generation bounded
    cleanup before starting the next, whose callbacks remain generation-pinned
    if an older cleanup outlives its deadline. On insertion failure, release
    turn media and return the error without queueing or acknowledging;
    unconfigured/internal traffic bypasses the event adapter.
23. Delta Chat treats provider events as queue-wake notifications. Drain
    `get_next_msgs` in ascending order at startup and after every `IncomingMsg`
    or message-specific `MsgsChanged`; while a full download is pending, generic
    `MsgsChanged` also wakes the drain, and `EventChannelOverflow` wakes it
    before account filtering despite its zero `contextId`. Fetch complete
    content, use only the stable provider-local
    Delta message ID as hash input, publish through synchronous admission, and
    retry before `markseen_msgs`; a lower retryable failure blocks later IDs,
    while intentionally filtered messages advance. For an incomplete message,
    retain RFC724 Message-ID only in process. Complete an original that remains
    visible; if it disappears, process through the last RFC-correlated
    replacement before retiring it. An unrelated complete batch cannot retire
    the pending original, and no visible correlation conservatively blocks the
    ordered queue.
24. Installing GitHub issue triage validates and writes one local workflow,
    then revalidates the local catalog without enabling ingress, workflows,
    models, MCP, or credentials. A matched body-authenticated
    `github`/`issues.opened` dispatch passes only signed body repository/issue
    fields to the no-tool structured classifier. After its enum/enum/boolean
    result validates, evaluate the declared conditional
    `mcp/github/add_issue_comment` step with signed body identity and fixed
    bounded comment text; no model prose or issue text reaches the action.
25. When ingress is enabled, register the protected operator subtree on the
    existing gateway listener before opening candidate storage. Stage its
    immutable store backend alongside channel and webhook admission and publish
    all three only after their fallible checks succeed. For list/get/payload
    calls, acquire the active controller generation, validate bounded exact
    filters and filter-bound cursor, call event/dispatch metadata store queries
    that do not select payload blobs or worker owner/lease tokens, project into
    token-free values, and release only after the response data is detached.
    Deactivation first rejects new operations, drains admitted calls, and only
    then permits worker shutdown and store close.
26. The launcher validates its authenticated event route, allow-listed query,
    and live PID data, replaces any inbound credentials with the PID bearer,
    bypasses environment proxies, and bounds the upstream request/response.
    The CLI reads the same local PID authority and calls the gateway directly
    with the same no-proxy rule. Ordinary detail omits payload; the explicit
    launcher route and `picoclaw events payload` command validate a bounded JSON
    object and pass its exact redacted bytes as text without trimming,
    re-encoding, or adding a newline. Replay additionally requires an empty JSON
    object, launcher same-origin metadata or CLI `--yes`, and one non-retried
    live-store call that returns the new additive event and location. Once the
    replay is dispatched, a storage, cancellation, timeout, or transport error
    returns a fixed unknown-outcome response without retry guidance.
27. The dashboard binds normalized list filters and the selected event ID to
    route search state, while keeping opaque pagination cursors inside matching
    query keys. Selecting an event loads only its payload-free detail and
    dispatch pages. Mount the no-retry, zero-retention payload query only after
    explicit reveal and render the returned bytes as escaped text. Replay opens
    a duplicate-effects warning, sends exactly one empty-object mutation after
    confirmation, and on success refreshes affected projections and selects the
    returned event.
28. The event-source editor projects the authenticated masked configuration
    into independent policy, webhook, and Delta Chat adapter drafts. Validate
    positive optional limits, case-insensitively unique public connector names,
    format-specific replacement secrets, GitHub repository/target scope, and
    enabled channel dependencies before constructing a scoped merge patch. A
    selected local text file replaces only the repository draft; explicit save
    trims entries, removes blanks and case-insensitive duplicates, and sends the
    normalized list and target user only for GitHub while a format change away
    from GitHub clears both. Keep preserved secret fields absent, send concrete
    rotations or explicit disabled clears only on save, and use null tombstones
    for deleted map entries. After persistence, discard secret input state,
    reload the safe projection, recompute launcher gateway status from an active
    event-runtime signature that case-folds and sorts GitHub repositories and
    case-folds the target login, and present the ordinary restart-required
    control only for a semantic runtime difference.
29. Each committed enabled event-ingress generation starts one context-bound
    retention worker even when workflows are disabled. Acquire the exact live
    runtime generation before every cycle, compute `UTC now - retention_days`,
    and call `Prune` in at most 20 batches of 500 rows. Run once after
    activation and every six hours, warn with error-only telemetry and retry on
    the next interval, and cancel/join the worker before reload rollback,
    replacement, shutdown, or store close. A provisional generation waiting
    for commit, or a candidate later rolled back, cannot delete rows.
30. The workflow editor sends draft YAML to a stateless server projector,
    retains its revision, and applies only a typed `on.event` replacement
    through the YAML node tree. Event selection lists payload-free metadata.
    Preview loads that metadata through the live PID authority and calls the
    shared evaluator; a field mismatch is a successful read-only result. For an
    event-parity draft test, submit only the selected ID, atomically read the
    complete redacted envelope from the admitted operator generation, recheck
    the draft trigger, build the same detached event context/fixed inputs and
    isolated target-ref session as the dispatcher, and launch an ordinary
    draft run with empty delivery and a trusted event/root-only draft-test
    origin with no dispatch ID. Persist the selected event ID beside the
    draft-key-fenced test snapshot. AI authoring uses the event schema as
    instructions but runs with no tools/history and receives only bounded
    structural failure context, never captured payload values.
31. The dashboard normalizes its event/dispatch view and each view's independent
    filters and selection into route search, but keeps list cursors in
    filter-bound query state. Global dispatch selection first lists token-free
    rows and then fetches the exact selected dispatch by strict opaque ID.
    Construct related event, workflow, and run URLs from returned IDs/refs only;
    preserve inactive-view search state when toggling, and replace invalid
    values without copying payload, error, delivery, lease, or cursor data.
32. When the executor receives trusted event provenance, validate the origin
    kind, exact event/dispatch IDs, and event/input correspondence before
    creating a run, assign the initial run ID as its family root, and copy the
    complete origin unchanged to reusable children. For Retry, capture the
    source record once and copy origin only after store-aware trust validation.
    Validate intrinsic origin/context on that record and iteratively walk every
    available parent/retry ancestor; accept a not-found ancestor as an
    independent-retention boundary, but reject an available mismatch, invalid
    link, non-not-found read failure, or cycle. Apply the same validation to
    browser projection. Reject origin on browser/manual request DTOs and never
    derive it from payload-bearing event/input maps. The workflow dashboard
    builds event, production-dispatch, and family-root links from trusted IDs
    alone, while absent or untrusted origin produces no link. Treat every
    linked record and its routing, dispatch, run, cancel-request, completion,
    and retention lifecycle independently.
33. For each enabled polling connector, call the exact GitHub notification
    tool with `include_read_notifications`, the provider maximum of 50 items,
    and monotonically
    increasing pages up to five. Validate the complete bounded JSON result,
    apply the immutable repository scope, and enrich pull-request subjects
    through the exact read-only PR tool. Normalize each matched connector copy
    with provider/source authentication and notification-ID/update-time
    deduplication, then insert through the ordinary store without modifying
    provider notification state.
34. For a targeted authenticated review request, acquire the exact head
    workspace and require its checked-out commit to equal the event head.
    Fetch the exact base object from the authenticated base-repository URL,
    verify both object IDs, derive their merge base, and generate deterministic
    context-rich unified diffs from that merge base to the head. Remove
    workspace paths from the model projection and stable hash; fail before the
    no-tools agent call if any selected diff exceeds 128 KiB or their aggregate
    exceeds 512 KiB. After a successful event run, inspect only the reserved
    review-draft output. Validate the typed schema and trusted GitHub
    review-request source, bind it to the dispatch/run/workflow/repository/
    revision identity already held by the store, and idempotently insert the
    case and ordered findings before acknowledging the dispatch. Ignore runs
    without that output. Invoke successful-run capture sinks in declared order
    before dispatch acknowledgement. For the exact installed own-PR development
    workflow's `v1` marker, first reconcile an existing immutable capture; then
    validate the complete signed routing identity including canonical numeric
    repository, pull-request, and pull-author database IDs; read the current PR;
    require the provider-returned author ID to match exactly, and cross-bind the
    signed repository and pull IDs through the canonical lowercase HTTPS origin
    plus exact provider-current repository, pull URL, and pull number; scan up
    to five 100-item review pages; and when all five are full issue one one-item
    next-page overflow probe through the exact GitHub read tool. Select and bind
    the review database ID, resolve only the exact provider-object thread
    identity, and atomically create the provider-verified review-level
    development case, empty case conversation, and next contiguous private
    thread membership. A sink failure keeps dispatch reconciliation retryable;
    each sink must therefore converge on the same case/thread/ordinal when a
    later sink or acknowledgement fails.
35. Apply human finding edits, drops/restores, and message appends in immediate
    SQLite transactions that first compare the exact case version and allowed
    lifecycle. Record a chat/rephrase user message before running the isolated
    agent; append its answer or structured suggestion at the resulting version.
    Rephrase never writes finding content itself.
36. On explicit submission, read the current open aggregate, verify the exact
    version and non-empty active set, construct a marker over case/version,
    serialize only active findings and reviewed revision, and atomically insert
    the immutable pending request while advancing the case to `submitting`.
    Return before any external call.
37. A submission worker claims one pending row with a fresh lease, renews at
    one-third of the lease, validates the stored request/marker, and reads the
    pull request's current head through the exact GitHub tool. A mismatch
    terminalizes the case as stale without a write; a match permits
    create-pending, ordered inline comments, and submit-pending exactly once.
    Atomically persist definite success or only a typed failure known to precede
    all writes. Treat any untyped or possibly delivered MCP failure, renewal
    loss, expired claim, or crash as terminal unknown; terminalize expired
    claims before selecting new pending work and never automatically reclaim
    them.
38. Reconcile terminal unknown only from its exact optimistic version after a
    human checks GitHub. Recording `submitted` resolves the case; recording
    verified `absent` marks the attempt failed with `reconciled_absent` and
    reopens the case. Neither resolution invokes a provider tool.
39. Read one complete review case inside one read-only SQLite transaction.
    Establish the snapshot before composing case, ordered findings, ordered
    messages, and latest submission; commit only after every query succeeds.
    Roll back on query, cancellation, or commit failure and return no partial
    aggregate. A concurrent process may commit in WAL mode, but its whole
    mutation appears only in a later aggregate read.
40. Before preparing a working-context gate, acquire and hold the exact
    configured runtime generation and canonical agent store, then acquire the
    case's in-process projection lock. Reload the complete review aggregate
    from SQLite only after both boundaries are held. Strictly validate the
    case, findings, message ownership/order/roles/kinds/timestamps, transcript
    limits, and immutable repository/PR/revision identity. Derive the opaque
    session key from only agent, `review` channel, and case ID; use an
    agent-qualified internal case alias and reject any owner, namespace, key, or
    immutable identity mismatch. Before replacement, atomically reserve the
    protected review scope under the same process-local JSONL locks used by
    ordinary live admission and snapshot replacement; a live scope that wins
    first makes projection fail closed, while a review reservation that wins
    first prevents live commands, history access, thread linkage, and public
    read-only workflows from using the key. Compare-and-swap the complete
    derived history, empty summary, scope, and aliases from the exact reserved
    or prior snapshot revision, then read it back through the alias. Invoke the consumer
    synchronously only after the canonical key, non-empty revision, complete
    scope, empty summary, and every message field compare exactly. Supply that
    exact revision and a detached JSON-native case/finding/message-metadata
    subject while retaining the lock and runtime lease, so another projection
    through that service and runtime reload cannot race capture. A different
    Service using the same local runtime store that advances the session is
    rejected if it wins before the downstream exact snapshot read; an advance
    after that read cannot change the already frozen evidence. The session revision
    does not fence a later SQLite-only mutation; a launcher that requires the latest
    authoritative case must revalidate the supplied case version at durable
    decision admission. SQLite remains
    authoritative: later case growth refreshes this same key, and no session
    content is ever copied back.
41. To launch one review-attention decision, first load the exact case version
    and use only its authoritative repository to select a trusted policy
    snapshot under the policy source's generation lease. Resolve and detach the
    global/repository layers, hash the source revision plus canonical effective
    policy, and return immediately for a compiler-confirmed no-op. For active
    policies, derive the deterministic run ID from the exact case/version,
    decision point, and derived policy revision. Build the bounded subject from
    a fresh exact aggregate; only when the effective mix contains a
    working-context gate, do so inside `WithWorkingContext` and attach its exact
    read-only session revision. Submit no public inputs, event, origin, delivery,
    or session. The executor admission hook validates the private candidate and
    enters `ReviewDecisionRunStore.AdmitReviewDecisionRun`: return an existing
    exact link without invoking create, otherwise verify the current case
    version, stage the immutable link, invoke the create-only run callback while
    the SQLite write transaction remains held, and commit before workflow
    effects become reachable. Reconcile only an exact linked private run. A run
    found without its link is an unknown cross-store outcome and is never
    executed or replaced automatically.
42. To materialize one trusted policy generation, validate and JSON-detach the
    complete operator catalog, normalize configured repository keys to lower
    case while rejecting collisions, validate every local layer against its
    selected global layer, and preflight all AI agents plus working-context
    session stores before opening event storage. Canonically sort decision and
    repository maps to derive the whole-catalog revision. During selection,
    hash only the domain tag, lower-cased authoritative case repository, exact
    decision point, and detached selected layers, then invoke the consumer
    synchronously with another detached snapshot. The management `GET` loads
    one stable update snapshot under the shared mutation lock. Its companion
    agent-identity `GET` first validates one strong policy-revision `If-Match`,
    then compares that revision before validating and canonically sorting the
    current agent identities, and finally projects only one 256-item page. A
    canonical decimal cursor is accepted only at a prior page boundary, and
    remains meaningful only with the same required revision header. `PUT` strictly
    decodes one bounded full replacement, requires its exact config revision,
    validates policy and configured-agent semantics, rechecks and saves through
    the shared cross-process compare-and-swap, and returns the newly loaded
    catalog/revisions without exposing unrelated configuration. Include the
    catalog revision in the launcher runtime signature only when both ingress
    and workflows are active.
43. In the authenticated review policy view, strictly and losslessly parse the
    policy projection before creating one transient array-shaped draft. Fence
    asynchronous policy and agent reads to the selected hydration generation;
    never replace dirty state from a background read. Apply every edit and
    effective-overlay preview locally, validate the complete projected catalog
    plus exact configured-agent identities, and issue one full PUT with the
    hydration config revision only after explicit save. On success, rebuild the
    draft from the authoritative response and report its gateway effect. On
    conflict or a newly observed revision, retain all draft text and require an
    explicit confirmed reload/discard; never rebase, retry, or launch a gate.
44. In the same transaction that moves an outgoing workbench submission to
    `submitted`, either from its live worker claim or explicit found
    reconciliation, insert its unique post-transition `review.submitted`
    attention occurrence. A generation-fenced attention worker claims one due
    row and renews its fresh lease. If no policy is pinned, capture the trusted
    current repository-selected policy, resolve it, encode the canonical v1
    source-revision/resolution/digest envelope, and persist it through the
    current token before any session, model, function, human-task, or run work.
    On every launch, strictly decode and recompute that pin instead of reading
    live configuration. Complete a validated run result as delivered or an
    all-zero result as noop. Release a still-current pre-admission failure with
    bounded sanitized detail and backoff while retaining any pin. After a
    crash, the deterministic decision link converges active work on the same
    run; effect-free no-op re-evaluation converges on the same terminal row.
    Cancel and join this worker with the gateway generation before closing its
    store.
45. For a case-owned attention read, reload the authoritative case aggregate.
    Return `none` immediately for a valid non-submitted case without reading an
    occurrence or run. For a submitted case, validate the latest immutable
    submission and trigger first. Treat historical trigger absence as `none`;
    validate pending/claimed pin absence-or-canonicality without reading a run;
    require a canonical all-zero pin and no run for terminal no-op; and only for
    delivered require a canonical active pin, deterministic run/link, and stable
    bounded task snapshot. Recompute every task payload hash from its exact title,
    questions, and response schema before projection. Map that validated state to
    the fixed public aggregate and turn statuses. Issue one domain-separated,
    length-prefixed SHA-256 response fence only when the runtime can resume the
    sole current waiting or recovery task; continuing, answered, canceled, and
    disabled-runtime turns carry no response authority. For a response, repeat
    the same chain validation using the submitted case version, compare the
    opaque fence, normalize and bound the answer, derive a separate response ID,
    and resume with the server-loaded task ID, original waiting revision, and
    input hash. Reproject after the persistence attempt without inheriting
    request cancellation so an exact retry can recover an answer committed
    before continuation or transport failure; a changed answer never shares
    that recovery identity. In the authenticated workbench, canonicalize only
    `case` plus `focus=chat`, wait for the selected conversation card to render,
    and then scroll/focus its existing chat affordance. Before every generic
    workflow list, detail, events, SSE, graph, task, resume, cancel, or retry
    operation, suppress any run with the exact reserved attention reference.
    Scrub direct hidden parent/caller, retry, child, and origin-root references
    from visible ordinary runs and remove hidden graph nodes/incident edges.
    Deny cancel/retry/task mutation for every ordinary run in the normalized
    transitive parent/child/retry component so cascade behavior cannot reach a
    hidden run. Exempt exact reserved-reference runs from production web and CLI
    terminal retention. When workflows are disabled, construct a read-only bridge
    with no executor, issue no fence, consume no new answer, preserve exact replay,
    and classify exact-reserved task resume as not found before disabled state.
46. For an own-PR development list/detail read, first admit the request to one
    live event-store generation and validate the exact read-only route. Decode
    only optional repository, canonical positive pull number, canonical limit,
    and an opaque cursor bound to both filters; reject a nonempty or streaming
    GET body before store access;
    query immutable cases newest first through the schema-v6 indexes; and build
    a detached public DTO rather than serializing the capture row. Revalidate
    every stored field before projection, omit all event/workflow/routing and
    trigger-only provenance plus private thread identity, ordinal, origin, and
    provider object IDs, and label provider review/pull/base/head values as
    capture-time snapshots. Return explicit replay and sibling-review captures
    as separate cases without grouping, reordering, or sharing their chat or
    repair state.
    Release the generation after the complete bounded JSON response has been
    constructed without invoking a provider, model, gate, repository, or
    mutation path.
47. For an own-PR development chat, make the launcher require exact same-origin
    browser provenance, then strip it while replacing browser authority. Make
    the protected runtime reject any browser-provenance header. At both layers,
    reject a noncanonical path, raw query or bare `?`, non-POST method, invalid
    length/media/encoding/Unicode/JSON shape, or anything except the exact
    `expected_version` and `content` keys before PID/store/model access. Validate
    the case ID, version range, Go-trimmed human text, and configured agent; then
    hold the process-wide case turn lock and a per-service AI slot. Load and bind
    the immutable capture and complete integrity-checked transcript, reject a
    stale expected version, and reserve two rows plus the worst-case assistant
    bytes before atomically appending the human. Invoke one isolated agent with
    an exact replacement system prompt and no default context, tools, runtime
    history, cache, managed execution, checkout, or provider authority. Validate
    and append the assistant at the next exact version. On any later failure,
    preserve the human row and return fixed text; include detail only if an
    independent two-second reload succeeds. Release the AI slot and case lock.
48. For a controller-owned local repair, first load the selected immutable case
    and its valid private thread membership, rebuild routing evidence, and
    independently re-read the current pull and bounded exact-review pages. For
    a provider-verified membership, require the provider-returned author ID to
    equal the stored invariant and cross-bind the signed repository/pull IDs
    through the stored canonical origin plus exact repository, pull URL, and
    pull number. For an isolated legacy membership, retain the pre-v9
    case-scoped verification path without deriving or joining stable thread
    identity. Reject a corrupt membership, closed or merged pull, dismissed or
    changed review, retargeted base repository/ref, or noncanonical clone
    endpoint; otherwise bind the refreshed fork/ref/SHA and review digest. While
    the trusted caller holds one already-resolved concrete provider generation,
    serialize the exact pin, acquire it, verify its locked workspace identity,
    and construct a fresh four-tool repository-content registry. Validate every
    complete model tool-call batch before operation one, serialize writes, deny
    `.git`, escaping and symlink-resolved control paths, and suppress argument
    values from logs. On every exit after acquisition, reacquire and compare the
    pin, using a bounded cancellation-detached postflight when necessary. Leave
    any allowed partial edits locked for explicit inspection; do not release,
    reset, run Git/CI, publish, consult a sibling case, or mutate provider,
    thread, or durable case state.
49. For a user-started durable repair, first validate the exact protected or
    launcher mutation boundary. Under the same selected-case admission lock,
    atomically read only that case's workbench and valid thread membership,
    and select the stored immutable session agent when one exists, otherwise
    the current configured default; side-effect-free-check that exact agent
    without retargeting an existing session. Then atomically
    fence the current conversation and repair revisions and insert one
    idempotent queued attempt. Return the
    complete safe detail with `202`. The generation-owned worker claims the
    attempt into `preparing`, heartbeats its lease, reloads the atomic workbench,
    takes only the admitted conversation prefix, and re-verifies current GitHub
    state. Install the singleton session pin only when empty; otherwise compare
    every provider/head/review field exactly. Resolve the stored exact agent to
    one concrete model, transition to `running` while atomically refreshing its
    execution lease, and invoke the edit-only runner
    with the stable reservation. Stop and cancel on lease loss. Finish success,
    safe pre-run failure, or ambiguous recovery through a detached live-token
    transaction; reclaim only expired preparation and terminalize expired
    execution as `recovery_required`. Poll only while active, merge conversation
    and repair cache dimensions independently, and never infer shared
    conversation/repair state, validation, or publication from thread membership
    or a completed local edit.
50. When opening schema v8, create the schema-v9 thread and membership tables in
    the same migration transaction, allocate one distinct legacy `pdt_` for each
    existing `pdc_`, and link only that case at ordinal zero. Validate the exact
    one-to-one result before advancing `user_version`. Do not read or parse
    retained raw event-envelope payloads, call GitHub, or infer/merge identity
    from connector, repository or login text, URL, pull number, review identity,
    refs, or timestamps. Roll back
    every generated thread and membership if any case cannot be isolated or the
    resulting schema/count/ordinal invariants do not validate. Preserve existing
    case-scoped verification and repair behavior, but never admit a legacy
    thread to sibling aggregation or future thread-wide ledger execution without
    an explicit provider-verified baseline.
51. When opening schema v9, create and structurally validate the empty schema-v10
    controller and immutable review-fence tables before advancing
    `user_version`; do not choose a thread owner, adopt a retained line, or
    synthesize a fence. For a later trusted controller call, load and validate
    the selected case's complete verified thread membership, exact latest
    queued-or-completed attempt, immutable pinned retained-workspace
    session/agent, controller revision, phase, lease shape, and full fence chain
    in one immediate transaction. Create at most one stable controller/line
    identity on the first mutation claim, atomically remove that owner from the
    legacy claim queue, collision-check and inherit its exact current workspace
    reservation for Adopt, or compare-and-swap a fresh controller reservation
    for a later Resume. Issue a fresh token/epoch for the exact allowed mutation
    or review phase. Never
    reclaim an expired mutation lease: atomically preserve its reservation and
    enter `recovery_required`. A review lease is separate and may be reclaimed
    only because its exact fence is immutable and the mutation reservation has
    already been retired. Under a live mutation lease, bind only the exact
    all-or-none owner-pin-equal retained-line result; a later mutation cannot
    park until an exact resume advances its mutation epoch. After the caller
    separately completes the latest attempt, parks, and snapshots that line,
    atomically append and hash one exact attempt fence with a globally unique
    non-authorizing digest of the retired mutation bearer plus authenticated
    retired-lease replay proof, clear the
    mutation lease and usable reservation, and enter `review_pending`; only a
    later call may acquire reservation-free review.
    Finish review by marking that exact fence and entering `ready`, or release it
    unchanged to `review_pending`. Exact RecordFence and FinishReview retries
    must compare their complete tuple and retired token proof; live Bind may
    repeat only its exact committed assertion without another clock-dependent
    write, while Acquire and ReviewRelease do not
    replay retired authority. Execute no worker, UI, model, filesystem, Git, local-review, CI,
    commit, workflow, provider, or publication behavior in this algorithm.

## Cross-Feature Behavior

[Runtime events and observability](runtime-events.md) receives a best-effort
`workflow.triggered` notification only when routing creates a dispatch. Its
process-local bus is not the durable inbox, a delivery source, or a recovery
mechanism, and event logging filters never change durable ingestion.

[Workflows](workflows.md) owns `on.event` parsing, deterministic matching,
execution, compatibility checks, and persisted workflow runs. Durable eventing
owns the input, routing work, dispatch intent, deterministic run identity, and
lease/recovery state consumed by those workers. Creating a dispatch is still a
separate durable step before execution. The explicitly installed issue-triage
template owns only composition: deterministic routing and structured
classification precede a separately declared GitHub MCP action.
The same workflow feature owns gate policy resolution, compilation, private
evidence capture, isolated AI, deterministic checks, and human-task resume;
event automation owns review repository selection, policy-generation and case
fences, decision/run idempotency, and the protected PR-chat projection used by
working-context gates. The persisted catalog remains PicoClaw operator
configuration outside every reviewed checkout; repository-local means a
trusted `owner/repo` selector in that catalog, not a file controlled by the PR.
Event automation also owns the structured review policy draft, all four gate
controls and their ordered mixtures, effective repository preview, validation,
and conflict-preserving editor behavior. Launcher management owns authenticated
route registration and canonical discoverability, while security isolation
owns lossless transport, memory-only retention, and the non-execution boundary.
The editor intentionally stops at configuration, while a separate durable
worker applies `review.submitted` only after PicoClaw's outgoing workbench
submission is final. The case-owned browser bridge may then navigate to the
canonical focused PR chat, project only the validated attention lifecycle, and
resume one exact fenced human task; it does not provide a generic manual launch
surface. `FR-EVENT-AUTOMATION-051` owns the distinct inbound third-party
review-feedback-to-development-case contract; it is never inferred from this
outgoing submission occurrence, review workbench, or browser handoff.
GitHub `targets_user` and `target_reason` attributes may participate in the same
deterministic `on.event` filters, but they grant no GitHub identity, review,
comment, tool, or MCP authority; any action remains an explicitly declared
workflow step under the existing policy.
The native-webhook-only `review_feedback` reason likewise establishes only that
the authenticated body describes a complete submitted review from another user
on a PR whose author matches local `target_user`. The unsigned GitHub event
header still supplies part of the event-type routing hint. The explicitly
installed `github-pr-development` workflow and its successful-run sink re-read
and fence the exact review database ID plus current pull request, fork/head, and
provider state before creating one local review-level case. Because the current
MCP review projection omits the webhook node ID, that value remains trigger
evidence; because its comment projection omits a parent review database ID,
inline comment association is not claimed. Neither the event nor captured case
is checkout, push, merge, or GitHub-write authority. Notification polling does
not synthesize this reason.
`FR-EVENT-AUTOMATION-052` composes only a later local read boundary over that
immutable capture. The launcher owns authenticated proxying and canonical
development-view navigation; security isolation owns the narrow public DTO and
plain-text rendering boundary. This read model does not extend the installed
workflow, rerun provider verification, collapse explicit replays, or establish
gate, checkout, Git, CI, provider-refresh, or publication authority.
`FR-EVENT-AUTOMATION-053` composes an explicit local conversation beside that
read model. Its transcript has an independent optimistic version and its agent
receives only detached captured evidence under an exact isolated replacement
system prompt with no tools, history, cache, workspace/bootstrap, identity,
memory, skills, contributors, tool rules, summary, time, or runtime context. It
neither refreshes the case nor authorizes a later development or action feature;
those stages must independently bind current repository and provider state.
`FR-EVENT-AUTOMATION-054` establishes only that next internal refresh-and-edit
primitive. [Git workspaces](git-workspaces.md) owns exact pin acquisition,
heartbeat, control-plane verification, and later release; the repair runner can
call only acquisition and never release. Security isolation owns the four-tool
filesystem boundary and untrusted-context prompt contract.
`FR-EVENT-AUTOMATION-055` adds the user-visible durable orchestration without
widening that primitive: one explicit instruction creates one leased attempt,
the controller re-verifies and pins before editing, and ambiguous execution is
terminal until an explicit future recovery action. Local review/CI and
publication must still independently fence the resulting checkout and current
provider state.
`FR-EVENT-AUTOMATION-056` supplies the later controller with a deterministic
local commit effect, but deliberately leaves it unwired until durable
validation evidence and a write-ahead commit intent exist. Git Workspaces owns
the candidate, reservation lock, commit-object verification, compare-and-swap,
and index reconciliation; Event Automation will own the later attempt ledger
and recovery state that decide when this primitive may be called.
`FR-EVENT-AUTOMATION-058` adds only the first private storage/controller seam
for that later ledger. Event Automation owns stable verified-thread/session
binding, lease and revision fencing, the exact retained-line projection, and
the immutable attempt-review-fence chain. [Git workspaces](git-workspaces.md)
still owns adoption, resume, park, reservation release, and exact-object
snapshot semantics; a future worker must supply their proven results. The
schema-v10 store retires its mutation lease and raw bearer before exposing
`review_pending`, so a later separate AI reviewer can receive only a distinct
reservation-free review lease while the parked line remains retained. No such
worker or AI call is wired here, and no `ready` row is CI, commit, push,
acknowledgement, or publication evidence. Existing case UI, advisory chat,
workflow gates, Git implementation, model execution, and CI behavior remain
unchanged. Controller creation deliberately and durably removes its owner
session from the schema-v8 repair queue, and this storage slice does not yet
provide the controller-aware worker/completion transition needed to advance a
newly admitted queued attempt. Its store contract can be exercised against an
already completed record, but ordinary legacy repair work may be dirty while
line adoption requires a clean commit; the later worker must transfer and adopt
the clean pinned line before mutation rather than infer that missing seam.
The explicitly installed PR-review template is different: its agent has
read-only review authority and emits a local structured draft only. Durable
eventing owns capture and the human workbench. The separate submission worker,
not the review model or event workflow, receives the narrow GitHub write
authority after explicit human confirmation.
Durable eventing also owns the PR-chat-to-session bridge required by workflow
`ai_working_context` gates. It derives and verifies one hidden stable session
from the SQLite-authoritative review transcript, then passes its internal key
to the workflow root only while the case projection lock and exact agent-runtime
generation remain held. Workflows own compilation, exact read-only snapshotting,
AI evaluation, and any durable human-task suspension; they never make the
derived session authoritative or copy it back into review storage. Browser
review/session discovery and Seahorse indexing omit this internal projection.
Because the bridge composes existing contracts rather than changing workflow
or persisted review syntax, all current eventing, review-draft, workflow-engine,
workflow-schema, and validator versions remain unchanged.
The event dispatcher and server-resolved draft-test path are the only producers
of trusted event `RunOrigin`; workflow storage validates, persists, safely
projects, and propagates it through child/retry runs. Production provenance
contains event/dispatch/root identity, while draft-test provenance deliberately
has no dispatch. The event and workflow dashboards navigate using only those
validated IDs/refs. They do not reconstruct provenance from the redacted
envelope, inputs, session, delivery, errors, or URL state, and they present
event routing, dispatch, run, cancellation-request, and completion lifecycles as
independent retained records.

[Webhook ingress](../../pkg/eventing/webhook) normalizes Standard Webhooks or
admitted native GitHub deliveries directly into this envelope on the channel
manager's shared HTTP mux; an authenticated GitHub repository-scope miss is an
acknowledgement-only decision and produces no envelope.
Aggregate shared-route teardown releases independently owned workflow-authoring
and agent-activity routes before releasing event operator and ingress routes;
that lifecycle coordination does not transfer their behavior or state into
durable eventing.
[Chat channels](chat-channels.md) own provider authorization, group filtering,
and transport UX; opted-in instances pass safe normalized metadata through the
durable channel-message adapter. Delta Chat additionally owns notification-
driven ordered fetch, process-local full-download replacement correlation, and
retry-before-seen email acknowledgement. The operator API/CLI consumes the live
gateway-owned store without redefining its deduplication, leasing, redaction,
replay, or retention semantics; the dashboard consumes only the
launcher-authenticated projection.

[Security and isolation](security-isolation.md) owns secure persistence of each
webhook signing secret and workflow side-effect policy. Signing adds no new
tool authority: deterministic or AI-driven decisions still execute through
existing workflow agent/tool policy.

[MCP integration](mcp-integration.md) owns lifecycle and exact tool
registration for the configured `github` server. Event polling requires its
read-only notification/PR tools; review publication requires its pending-review
write tools. Neither path uses model discovery to choose or rename those
capabilities.

## Failure And Edge Cases

- Empty source, connector, deduplication key, or event type is rejected before
  a transaction starts.
- Timestamps outside the signed Unix-nanosecond storage range and
  self-referential replay lineage are rejected instead of wrapping or creating
  an undeletable replay cycle.
- Invalid or oversized JSON is never partially stored. Redaction cannot be
  disabled by malformed nesting or case variation in a configured field name.
- A provider retry with different payload data still returns the first durable
  event; it does not become an update operation.
- Concurrent insert and claim operations obey SQLite busy handling and unique
  constraints without producing duplicate durable ownership.
- Process exit abandons no in-memory-only work: expired routing and dispatch
  leases are reclaimable after reopening the same database.
- A worker whose lease expired cannot finish work after another worker claims
  it, even when both claims use the same human-readable worker label, because
  their store-generated lease tokens differ.
- A stale routing worker cannot create another dispatch after its lease expires,
  another worker reclaims it, or routing is acknowledged, even if the
  event/workflow pair was not inserted previously.
- A long workflow renews its existing token without incrementing attempts. A
  renewal failure cancels its execution and leaves reconciliation to a later
  owner instead of writing through a stale lease.
- Routing failure does not silently delete dispatch history. Dispatch failure
  does not mutate the source event or report routing success.
- Invalid, malformed, or compatibility-blocked workflow definitions are skipped
  consistently with existing automatic triggers. A transient fan-out/store
  failure retries the whole event, relying on dispatch uniqueness for completed
  matches.
- A definition edit after matching cannot change a new dispatch's first bound
  revision. Dispatch execution rejects changed bytes, and legacy unbound work
  rejects a persisted event that no longer matches, before creating a run.
- A missing actor, subject, or attribute cannot satisfy `*`; explicit empty
  filters fail validation rather than becoming an accidental catch-all.
- AI classification is expressed as an agent step inside a broadly but
  deterministically matched workflow. The router never invokes a model or
  treats model output as durable delivery identity.
- Enabling event ingress with no configured account and model alias never
  selects an upstream provider default. Deterministic ingestion remains
  independent, while a model-backed workflow path fails at the shared
  `no model configured` boundary before a provider request.
- Event payload numbers remain lossless through expression comparison,
  file-run reads, listing, cancellation updates, and values propagated into
  dynamic run or lifecycle outputs, including integers beyond float64's exact
  range, exponent overflow, and very small exponent values. Representable
  ordinary dynamic numbers keep their legacy float64 shape.
- If execution crashes after the run record is created, recovery cancels the
  orphan record and marks the dispatch failed; it does not repeat workflow
  steps. External actions that need exactly-once behavior still require
  provider-supported idempotency keyed by dispatch/run ID.
- A dispatch-link callback failure marks the already-created run failed before
  workflow steps. If a linked run file is later pruned or removed, recovery
  fails the dispatch without reconstructing or replaying that run.
- A production origin whose kind, event ID, dispatch ID, root run ID, event
  context, or top-level fixed inputs disagree is rejected before the run record
  is created. A draft-test origin additionally rejects any dispatch ID.
  Browser/manual request fields and lookalike payload/input values cannot
  supply or repair origin.
- Reusable children retain the original family root and event relationship.
  Retry uses one captured authoritative source and preserves its origin only
  when intrinsic and retained-lineage validation trusts it; legitimate pruning
  of an ancestor alone does not discard provenance. Retrying a production
  event run does not create, relink, reset, or finish a dispatch; the new run's
  `retry_of_run_id` remains separate from its unchanged trusted origin.
- Runs created before typed origin, intrinsically malformed records, and runs
  with a mismatched/invalid/cyclic available ancestor remain readable. Browser
  projection omits untrusted external relationships instead of guessing from
  the larger redacted event snapshot. A not-found ancestor is an ordinary
  independent-retention boundary, not evidence that the descendant origin was
  forged; non-not-found lineage read failure remains fail-closed. A trusted
  retained kind remains authoritative across that boundary: production stays
  unmasked and retryable, draft-test work stays masked and non-retryable, and
  only untrusted or legacy ancestry uses the fail-closed draft-family fallback.
- Replay is additive. It cannot replace its source, inherit a live lease, or
  erase earlier dispatch outcomes. Its self-referential foreign key preserves
  the source while a retained replay points to it.
- Retention preserves pending/claimed routing, too-new events, replay parents,
  and events with pending/claimed/running dispatches.
- A database created by a newer schema version fails closed rather than being
  downgraded.
- A database declaring the current version still fails closed when required
  tables, columns, constraints, foreign keys, or indexes do not match; failed
  migration and validation roll back their version and partial objects.
- On unsupported build targets, opening durable ingress returns
  `ErrUnsupportedPlatform`. With ingress disabled, normal PicoClaw behavior
  remains available.
- Enabled invalid storage fails gateway startup before readiness. Disabled
  ingress creates no database or worker; enabled ingress with workflows
  disabled opens storage but leaves routing pending.
- Reload stays unready while services are drained or replacement state is
  provisional. Storage, runtime initialization, service restart, or rollback
  failure cannot close a provider still owned by active work or report a
  half-restored gateway as ready.
- Invalid connector names, case collisions, missing/weak signing secrets, and
  route collisions fail before webhook admission or storage startup. Disabled
  master ingress treats retained connector entries as inert, but cannot persist
  a canonical signing secret in a connector map key.
- Enabled repository scope on a non-GitHub connector, more than 4,096
  repositories, malformed/oversized/untrimmed owner/repo values,
  case-insensitive duplicates, or an invalid target login fails configuration
  before admission. Repository and target values containing a configured
  sensitive value fail opaquely even when their connector is inactive.
- Authentication failures disclose no distinction between a missing, duplicate,
  stale, future, or mismatched signing header. Unsupported methods, media types,
  encodings, non-canonical encoded route aliases, malformed bodies,
  secret-bearing identity fields, and body limits mutate no durable state.
- GitHub authentication validates the raw body only. Its event and delivery
  headers remain explicitly unauthenticated routing metadata, so TLS must
  protect the client-to-gateway path (or every hop to a trusted terminating
  proxy). The absence of a signed timestamp means retained delivery-ID
  deduplication, not signature freshness, is the replay boundary.
- The one-MiB local default intentionally rejects larger GitHub deliveries with
  `413` even though the provider permits payloads up to 25 MiB. Raising the
  configured bound is explicit resource policy, not an automatic provider
  exception.
- Every successful admitted response names a durable event. Concurrent retries
  with the same `Webhook-Id` or admitted `X-GitHub-Delivery` return the retained
  original ID and first payload; workflow failure occurs after acknowledgement
  and is visible through durable dispatch state instead of changing the HTTP
  result. The sole non-owning success is an authenticated GitHub repository
  scope miss: it returns `202` with `ignored: true`, `inserted: false`, and no
  `event_id`, creates no durable deduplication state, and authenticates and
  ignores a later repeat again.
- A configured target is not inferred from unauthenticated headers. Matching
  uses only the authenticated body plus local target configuration; a
  review-request removal or unassignment does not label the removed user as an
  active requested reviewer or assignee.
- Own-PR submitted feedback requires complete canonical PR-author, distinct
  reviewer, review database/node identity, supported state, commit, and
  submitted-time fields. Missing or malformed identity, another author's PR,
  self-review, non-submitted actions, and poll-derived notifications omit only
  `review_feedback`; another independently valid requested-reviewer, assignee,
  or mention reason remains available.
- `pull_request_author_is_target`, `review_author_is_target`, and
  `review_feedback` are workflow-routing facts, not proof that a branch is
  current or that PicoClaw may read, edit, push, comment, or merge it. The
  normalizer creates no development case or execution as a side effect.
- Own-PR development capture accepts only the exact installed workflow ref and
  exact `v1` marker after a successful run. Marker absence is an ordinary
  successful run; an invalid marker, provenance mismatch, unavailable exact
  read tool, provider mismatch, bounded-scan exhaustion, malformed result, or
  unsafe exact-result artifact creates no case and leaves dispatch
  reconciliation retryable. An already captured immutable identity returns
  before another mutable provider read.
- Review feedback is bounded untrusted data even after provider verification.
  The review database ID, author, state, commit, time, and URL can be bound at
  review level, but the current MCP response cannot bind inline comments to
  that review and cannot independently verify its webhook node ID. Capture
  therefore grants no model, checkout, repository, or provider-write authority.
- Development-workbench list/detail reads never call GitHub to refresh a case.
  A pull request, review, ref, or SHA that changed after capture remains visibly
  labelled as an older captured snapshot; it is not silently rewritten or
  presented as live. A malformed stored row fails the complete read rather than
  falling back to raw capture serialization.
- Development chat never changes `pr_development_cases.updated_at`, so a new
  message cannot reorder the captured-feedback inbox. Stale conversation
  versions and capacity for fewer than two remaining rows or fewer than the
  normalized human plus maximum assistant bytes conflict before append without
  eviction. Any later model, response-validation, assistant-append, or
  post-append-validation failure leaves the human message recoverable without
  fabricating a missing answer or retrying the model implicitly.
- Explicit event replays may produce visually similar development cases. The
  workbench preserves each `pdc_` identity and capture time, while keeping their
  internal event/dispatch/run provenance unrepresentable. Filtering, paging,
  selection, and external-link navigation create no deduplication or mutation.
- Reload and shutdown reject new webhook requests with retryable `503` before
  draining admitted inserts. A drain timeout leaves the store open for a later
  retry; a provisional candidate never acknowledges into its database.
- Configured channel messages wait behind startup/reload preparation until a
  backend with the exact connector set commits. An insert failure queues no
  turn; `event_only` creates no turn UX; a drain timeout keeps the owning store
  open; unrelated and internal traffic remain unchanged.
- Provider retries resolve to the first channel event through the hashed stable
  provider-local identity. RFC724 Message-ID is fetched only within an
  incomplete-message replacement lifecycle, retained process-locally solely to
  correlate the original and candidate IDs, and never used for deduplication or
  durable payload.
  Blob path, media reference, and routing metadata are not durable payload
  fields, and raw blob paths never cross channel admission or enter agent text.
- Mirror mode cannot atomically couple the durable SQLite insert, in-memory
  agent queue, and provider acknowledgement. A crash in that interval may cause
  a provider retry to repeat the legacy chat turn, while the durable event and
  its workflow dispatch remain deduplicated.
- Delta Chat provider events only wake its authoritative ordered queue. It
  retries incomplete fetch, durable admission, and provider acknowledgement in
  strict order and does not mark an accepted message seen before durable
  ownership. An unrelated complete batch cannot retire a pending download; no
  visible RFC-correlated replacement conservatively blocks later queue work,
  and shutdown cancels the retry loop.
- The compatibility version bump makes pre-`on.event` stamps stale, so newly
  understood event YAML cannot activate until explicitly revalidated.
- The GitHub issue-triage template is inert until explicit installation and
  separate dependency configuration. Its event marker aids audit/search but is
  not a GitHub idempotency key, so an explicit workflow retry, event replay, or
  provider redelivery after retention pruning can duplicate a comment. Other
  GitHub actions remain separate later stages.
- Operator list/detail projections cannot serialize deduplication or lease
  tokens because those fields are absent from their DTOs. Payload is fetched
  only through its no-store exact-text route. An inactive or draining
  generation returns retryable `503`; it never falls back to opening SQLite.
- Replay is not idempotent and is never retried automatically. A lost response
  or post-dispatch error may have created a new pending event, so it carries no
  `Retry-After` hint and the operator must inspect replay lineage before deciding
  whether to issue another explicit request.
- Dashboard list/detail selection never fetches payload. Exact payload text is
  held only by the explicitly mounted query and is discarded on deselection;
  it is never parsed into JavaScript numbers or included in route state.
- The policy view fails unavailable rather than partially hydrating on malformed,
  duplicate, trailing, unsafe-Unicode, over-bound, or numerically lossy JSON.
  Accepted question-number tokens, including unsafe integers and exponent forms,
  remain exact across GET, formatting, untouched draft state, and PUT.
- Background policy or agent reads cannot overwrite a dirty draft. A save
  conflict keeps all local text, never rebases or retries, and permits data loss
  only through an explicit confirmed reload/discard. Route and before-unload
  navigation remain blocked while that draft is dirty.
- Opening, editing, validating, previewing, refreshing, or discarding the policy
  view creates no case, chat message, decision link, workflow run, model/tool
  call, repository write, event, provider action, or GitHub mutation. The view
  explicitly reports that an outgoing workbench review queues its
  `review.submitted` policy only after reaching submitted, while policy editing
  itself never triggers it.
- Global dispatch navigation remains read-only and treats a missing selected
  dispatch, event, workflow, or run as an independent retained-state gap. It
  never infers one lifecycle outcome from another or falls back to a different
  selection after refresh.
- Workflow run relationship navigation applies the same rule: event routing,
  dispatch finish, run completion, and cancellation request/reason are
  independent fields with independent retention. A pruned ancestor terminates
  only that validation branch, while conflicting retained ancestry suppresses
  provenance. Links and labels use only validated origin IDs/refs; payloads,
  delivery data, cancel reasons, errors, deduplication identities, cursors, and
  lease credentials never enter route state.
- Event-source drafts do not persist on navigation or validation failure.
  Existing webhook secrets are presence-only; preserving one emits no secret
  field, rotating one emits only the newly entered value in the authenticated
  save request, clearing one requires the connector to be disabled, and
  deleting one uses a merge-patch tombstone so its security entry cannot
  survive as an active source. Endpoint previews and GitHub HTTPS warnings
  never contain a credential.
- A GitHub repository file is read locally and replaces only the unsaved
  repository draft. Blank lines, surrounding whitespace, and duplicate case
  variants normalize on save; invalid remaining owner/repo values or target
  login block the patch. Empty scope deliberately accepts all repositories.
  Enabled semantic scope changes require restart, while repository ordering,
  repository case, target-login case, and inactive connector scope do not
  create a false restart requirement.
- While master ingress is active, an enabled Delta Chat event adapter cannot be
  saved when the named channel is absent or disabled. Master-disabled drafts
  retain invalid dependency references so the same management screen can
  disable or remove them before activation. Explicit unverified-email admission
  remains visibly separate from the mode choice and does not weaken the default
  verified and transport-authenticated email requirement.
- A failed source save or gateway-status refresh leaves an actionable page and
  never reports the draft as active. Saving only disabled connector bodies while
  master ingress remains disabled does not create storage, routes, workers, or
  a spurious runtime restart requirement.
- Event-trigger builder projection and replacement never mutate the persisted
  development session by themselves. A stale revision, unsupported alias or
  merge shape, or projected scalar containing a line break stays in raw-YAML
  mode instead of flattening, splitting, or overwriting advanced content.
- Captured-event match preview reads metadata only and uses the routing
  evaluator without claiming the event. A legitimate mismatch returns
  deterministic failed checks; malformed/invalid drafts and unavailable event
  state remain distinct errors.
- Event-parity draft testing never accepts a browser-supplied envelope. It
  rejects manual context overrides, a missing or non-matching event, and a
  generation-safe context read failure before creating a run. The synthetic
  draft run has no durable dispatch ID and cannot be mistaken for routed work.
- Selected payload values do not enter trigger inspection, definition/template
  inspection, match diagnostics, route state, browser persistence, toasts, or
  workflow-author prompts. Definition/template inspection shows only declared
  filters, action targets, and conservative possible-effect codes. Failed test
  repair omits payload values and lifecycle event message/payload text, while
  the author model itself has no tool authority.
- A poll-only GitHub connector cannot accidentally expose an unauthenticated
  webhook route. Poll scans may repeat or fail, but they never mark provider
  notifications read and unchanged notification/update pairs remain one
  durable event.
- A review workflow run with malformed output remains a failed-to-capture
  dispatch rather than a partially inserted case. Reconciliation cannot
  replace a previously captured draft after a human edits it.
- A missing exact base object, unrelated base repository, head mismatch,
  unavailable merge base, malformed diff, or selected evidence over either
  diff bound fails the workflow before the no-tools reviewer sees any content.
- Simultaneous browser tabs resolve through optimistic versions. A model answer
  arriving after another mutation conflicts instead of overwriting newer state;
  the already recorded human message remains visible for a deliberate retry.
- Concurrent readers never combine a pre-mutation case version with
  post-mutation findings, messages, or submission state. Snapshot acquisition,
  query, or commit failure returns an error instead of a partial case.
- Dropping every finding is a valid no-publication resolution. Restoring one
  reopens the case; neither transition creates a submission or calls GitHub.
- Definite typed request validation failure before an external call may reopen
  a case. A changed current pull-request head becomes stale before any write.
  Any untyped failure, transport failure during create/comment/submit, lost
  renewal, or worker crash after claim is not retried: it becomes visibly
  unknown so an operator can inspect GitHub, then explicitly mark the review
  found or verified absent without a provider call.
- A review working-context launch never trusts a caller-supplied transcript or
  browser-supplied session key. An absent/corrupt/unsupported session backend,
  stale runtime generation, malformed SQLite aggregate, different owner or
  immutable case binding, stale compare-and-swap, any replacement error,
  missing/empty/non-advancing revision, inexact readback, or invalid bounded
  JSON-native subject prevents the gate callback. A legitimate newer review
  version atomically rewrites the complete derived view at the same key; direct
  writes to that internal session are discarded on the next authoritative
  projection and never become review chat.
- An attention launch never trusts caller-supplied repository, gate policy,
  subject, session, or run identity. Exact duplicate decisions return only an
  already linked matching private run and never re-enter workflow execution. A
  stale first decision fails before durable run creation; an invalid or missing
  link never causes reconstruction. Because SQLite and the file run store cannot
  commit atomically across a process failure, a durable run without its link is
  treated as an unknown, non-executable private orphan and blocks automatic
  replacement. Policy, case, session, or candidate drift maps to fixed safe
  admission errors without exposing the review transcript or policy body.
- A submitted-review attention occurrence is committed with the submitted case
  transition, so a process exit cannot leave a durable submitted review without
  its local trigger. A crash before policy pinning may intentionally select the
  trusted policy of the successful retry; a crash afterward cannot retarget the
  occurrence because every worker validates and reuses the same canonical pin.
  Launch-before-completion recovery finds the exact linked private run, while a
  zero-only policy repeats no effect before recording noop. A stale lease cannot
  change the pin, retry schedule, run result, or terminal state.
- An attention projection validates submitted authority by trigger status:
  historical absence is `none`; pending/claimed do not require a run; no-op
  requires a canonical all-zero pin and no run; delivered requires a canonical
  active pin, exact link/run, stable task chain, and matching task payload hash.
  Any malformed or inconsistent required state fails without raw fallback.
  Waiting or recovery state exposes no response fence when the read-only runtime
  cannot resume it; continuing, answered, and canceled turns never expose one.
- A stale, old, cross-case, cross-task, or altered-response fence cannot consume
  a human task. If the exact normalized response was already durably accepted,
  the separate response ID makes retry recover and reproject that answer even
  when continuation or the prior HTTP response failed; it never appends review
  chat or advances the review case version.
- Generic workflow list, detail, event, SSE, graph, task, resume, cancel, and
  retry routes treat the exact reserved attention workflow reference as absent,
  including malformed runs that use that reference. Visible ordinary runs and
  graphs scrub direct hidden relationships; mutations are denied for the whole
  normalized transitive parent/child/retry component. Production web and CLI
  retention preserve exact reserved-reference runs regardless of terminal age,
  while related ordinary runs retain normal retention. Exact-reserved task resume
  returns not found before disabled-runtime disclosure. The case-owned projection
  never returns private run/task/workflow/session/policy identity, input hashes,
  trigger fencing state, or raw internal errors.
- Attention-policy configuration never trusts a repository checkout or request
  path as policy authority. Invalid effective overlays, case-colliding
  repository keys, unavailable configured agents, missing working-context
  session stores, or an over-bound catalog fail before active event storage is
  opened. The dedicated API returns only fixed policy/config conflict errors;
  a stale or cross-site replacement writes neither public JSON nor the security
  sidecar, and an unrelated catalog edit cannot change the selected revision of
  an already linked decision.
- The browser never receives the hidden submission marker, immutable request,
  lease identity, or internal error, including in conflict and failure
  responses. The launcher never forwards its authenticated user's cookie or
  authorization value to the gateway.

GitHub protocol references: [validating webhook
deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries),
[webhook best
practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks),
and [event/payload
schemas](https://docs.github.com/en/webhooks/webhook-events-and-payloads).

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-012` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004` | [pkg/eventing/envelope_test.go](../../pkg/eventing/envelope_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-003` | [pkg/eventing/redaction_test.go](../../pkg/eventing/redaction_test.go), [pkg/eventing/store_replay_redaction_test.go](../../pkg/eventing/store_replay_redaction_test.go) |
| `FR-EVENT-AUTOMATION-005` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go) |
| `FR-EVENT-AUTOMATION-006`, `FR-EVENT-AUTOMATION-007` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-008`, `FR-EVENT-AUTOMATION-009` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go) |
| `FR-EVENT-AUTOMATION-010`, `FR-EVENT-AUTOMATION-011` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go) |
| `FR-EVENT-AUTOMATION-013`, `FR-EVENT-AUTOMATION-021` | [pkg/workflows/event_trigger_test.go](../../pkg/workflows/event_trigger_test.go), [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/development_compatibility_test.go](../../pkg/workflows/development_compatibility_test.go) |
| `FR-EVENT-AUTOMATION-014`, `FR-EVENT-AUTOMATION-015`, `FR-EVENT-AUTOMATION-016`, `FR-EVENT-AUTOMATION-017`, `FR-EVENT-AUTOMATION-020` | [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/workflows/store_test.go](../../pkg/workflows/store_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go) |
| `FR-EVENT-AUTOMATION-018` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go) |
| `FR-EVENT-AUTOMATION-019` | [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go), [pkg/gateway/gateway_test.go](../../pkg/gateway/gateway_test.go) |
| `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-023` | [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [pkg/channels/dynamic_mux_test.go](../../pkg/channels/dynamic_mux_test.go), [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_webhook_deferred_test.go](../../web/backend/api/config_event_webhook_deferred_test.go) |
| `FR-EVENT-AUTOMATION-024` | [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [pkg/config/events_test.go](../../pkg/config/events_test.go) |
| `FR-EVENT-AUTOMATION-025` | [pkg/config/events_channels_test.go](../../pkg/config/events_channels_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [web/backend/api/config_event_channel_test.go](../../web/backend/api/config_event_channel_test.go) |
| `FR-EVENT-AUTOMATION-026`, `FR-EVENT-AUTOMATION-027` | [pkg/eventing/channelmessage/backend_test.go](../../pkg/eventing/channelmessage/backend_test.go), [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go), [pkg/channels/base_test.go](../../pkg/channels/base_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-EVENT-AUTOMATION-028` | [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go) |
| `FR-EVENT-AUTOMATION-029` | [pkg/eventing/channelmessage/controller_test.go](../../pkg/eventing/channelmessage/controller_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-EVENT-AUTOMATION-030` | [pkg/config/events_webhook_format_test.go](../../pkg/config/events_webhook_format_test.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go) |
| `FR-EVENT-AUTOMATION-031` | [pkg/workflows/templates.go](../../pkg/workflows/templates.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go) |
| `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-033` | [pkg/eventing/operator](../../pkg/eventing/operator), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/gateway/event_operator_test.go](../../pkg/gateway/event_operator_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-EVENT-AUTOMATION-034` | [web/frontend/src/api/events.test.ts](../../web/frontend/src/api/events.test.ts), [web/frontend/src/components/events](../../web/frontend/src/components/events), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-035` | [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [web/frontend/src/api/event-sources.test.ts](../../web/frontend/src/api/event-sources.test.ts), [web/frontend/src/components/events/event-sources-page.test.tsx](../../web/frontend/src/components/events/event-sources-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_channel_test.go](../../web/backend/api/config_event_channel_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go) |
| `FR-EVENT-AUTOMATION-036` | [pkg/workflows/editor_test.go](../../pkg/workflows/editor_test.go), [pkg/workflows/event_trigger_test.go](../../pkg/workflows/event_trigger_test.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/eventing/operator/backend_test.go](../../pkg/eventing/operator/backend_test.go), [pkg/eventing/operator/handler_test.go](../../pkg/eventing/operator/handler_test.go), [web/backend/api/workflow_editor_test.go](../../web/backend/api/workflow_editor_test.go), [web/backend/api/workflow_ai_test.go](../../web/backend/api/workflow_ai_test.go), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows](../../web/frontend/src/components/workflows), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-037` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/eventing/operator](../../pkg/eventing/operator), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [web/frontend/src/api/events.test.ts](../../web/frontend/src/api/events.test.ts), [web/frontend/src/routes/events.tsx](../../web/frontend/src/routes/events.tsx), [web/frontend/src/components/events](../../web/frontend/src/components/events), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-038` | [pkg/workflows/origin.go](../../pkg/workflows/origin.go), [pkg/workflows/origin_test.go](../../pkg/workflows/origin_test.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/store_test.go](../../pkg/workflows/store_test.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_cancel_test.go](../../web/backend/api/workflow_cancel_test.go), [web/backend/api/workflow_event_context_test.go](../../web/backend/api/workflow_event_context_test.go), [web/frontend/src/api/events.ts](../../web/frontend/src/api/events.ts), [web/frontend/src/api/events.test.ts](../../web/frontend/src/api/events.test.ts), [web/frontend/src/api/workflows.ts](../../web/frontend/src/api/workflows.ts), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows/workflow-run-origin.test.ts](../../web/frontend/src/components/workflows/workflow-run-origin.test.ts), [web/frontend/src/components/workflows/workflows-page.tsx](../../web/frontend/src/components/workflows/workflows-page.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-039` | [pkg/config/events_github_scope_test.go](../../pkg/config/events_github_scope_test.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/frontend/src/api/event-sources.test.ts](../../web/frontend/src/api/event-sources.test.ts), [web/frontend/src/components/events/event-sources-page.test.tsx](../../web/frontend/src/components/events/event-sources-page.test.tsx) |
| `FR-EVENT-AUTOMATION-040` | [pkg/config/events_github_scope_test.go](../../pkg/config/events_github_scope_test.go), [pkg/eventing/githubpoll](../../pkg/eventing/githubpoll), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [web/frontend/src/api/event-sources.test.ts](../../web/frontend/src/api/event-sources.test.ts), [web/frontend/src/components/events/event-sources-page.test.tsx](../../web/frontend/src/components/events/event-sources-page.test.tsx) |
| `FR-EVENT-AUTOMATION-041` | [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/workflows/native_functions_test.go](../../pkg/workflows/native_functions_test.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/reviews/capture_test.go](../../pkg/reviews/capture_test.go), [pkg/eventing/review_store_sqlite_test.go](../../pkg/eventing/review_store_sqlite_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go) |
| `FR-EVENT-AUTOMATION-042` | [pkg/reviews/service_handler_test.go](../../pkg/reviews/service_handler_test.go), [pkg/eventing/review_store_sqlite_test.go](../../pkg/eventing/review_store_sqlite_test.go), [pkg/eventing/operator/reviews_delegation_test.go](../../pkg/eventing/operator/reviews_delegation_test.go), [web/backend/api/reviews_test.go](../../web/backend/api/reviews_test.go), [web/frontend/src/api/reviews.test.ts](../../web/frontend/src/api/reviews.test.ts), [web/frontend/src/components/reviews](../../web/frontend/src/components/reviews), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx) |
| `FR-EVENT-AUTOMATION-043` | [pkg/reviews/submitter_test.go](../../pkg/reviews/submitter_test.go), [pkg/reviews/worker_test.go](../../pkg/reviews/worker_test.go), [pkg/reviews/worker_sqlite_test.go](../../pkg/reviews/worker_sqlite_test.go), [pkg/reviews/service_handler_test.go](../../pkg/reviews/service_handler_test.go), [pkg/eventing/review_store_sqlite_test.go](../../pkg/eventing/review_store_sqlite_test.go), [web/frontend/src/components/reviews](../../web/frontend/src/components/reviews) |
| `FR-EVENT-AUTOMATION-044` | [pkg/reviews/session_bridge_test.go](../../pkg/reviews/session_bridge_test.go), [pkg/reviews/session_bridge_sqlite_test.go](../../pkg/reviews/session_bridge_sqlite_test.go), [pkg/reviews/session_bridge_integration_test.go](../../pkg/reviews/session_bridge_integration_test.go), [pkg/gateway/review_working_context_test.go](../../pkg/gateway/review_working_context_test.go), [pkg/reviews/service_handler_test.go](../../pkg/reviews/service_handler_test.go), [pkg/agent/context_seahorse_test.go](../../pkg/agent/context_seahorse_test.go), [web/backend/api/session_test.go](../../web/backend/api/session_test.go) |
| `FR-EVENT-AUTOMATION-045` | [pkg/eventing/review_decision_run_sqlite_test.go](../../pkg/eventing/review_decision_run_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/reviews/attention_test.go](../../pkg/reviews/attention_test.go), [pkg/reviews/attention_sqlite_test.go](../../pkg/reviews/attention_sqlite_test.go), [pkg/reviews/session_bridge_integration_test.go](../../pkg/reviews/session_bridge_integration_test.go), [pkg/gateway/review_attention_test.go](../../pkg/gateway/review_attention_test.go) |
| `FR-EVENT-AUTOMATION-046` | [pkg/config/reviews_test.go](../../pkg/config/reviews_test.go), [pkg/config/migration_test.go](../../pkg/config/migration_test.go), [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/reviews/attention_config_test.go](../../pkg/reviews/attention_config_test.go), [pkg/gateway/review_attention_test.go](../../pkg/gateway/review_attention_test.go), [web/backend/api/review_attention_policies_test.go](../../web/backend/api/review_attention_policies_test.go), [web/backend/api/review_attention_agents_test.go](../../web/backend/api/review_attention_agents_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go) |
| `FR-EVENT-AUTOMATION-047` | [web/frontend/src/api/review-attention-agents.test.ts](../../web/frontend/src/api/review-attention-agents.test.ts), [web/frontend/src/api/review-attention-json.test.ts](../../web/frontend/src/api/review-attention-json.test.ts), [web/frontend/src/api/review-attention-policies.test.ts](../../web/frontend/src/api/review-attention-policies.test.ts), [web/frontend/src/components/reviews/review-attention-policy-model.test.ts](../../web/frontend/src/components/reviews/review-attention-policy-model.test.ts), [web/frontend/src/components/reviews/review-attention-policies-page.test.tsx](../../web/frontend/src/components/reviews/review-attention-policies-page.test.tsx), [web/frontend/src/components/reviews/reviews-page.test.tsx](../../web/frontend/src/components/reviews/reviews-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-048` | [pkg/eventing/review_attention_trigger_sqlite_test.go](../../pkg/eventing/review_attention_trigger_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/reviews/attention_test.go](../../pkg/reviews/attention_test.go), [pkg/reviews/attention_trigger_worker_test.go](../../pkg/reviews/attention_trigger_worker_test.go), [pkg/reviews/attention_trigger_worker_sqlite_test.go](../../pkg/reviews/attention_trigger_worker_sqlite_test.go), [pkg/gateway/review_attention_trigger_test.go](../../pkg/gateway/review_attention_trigger_test.go), [web/frontend/src/components/reviews/review-attention-policies-page.test.tsx](../../web/frontend/src/components/reviews/review-attention-policies-page.test.tsx) |
| `FR-EVENT-AUTOMATION-049` | [pkg/reviews/attention_bridge.go](../../pkg/reviews/attention_bridge.go), [pkg/reviews/attention_bridge_test.go](../../pkg/reviews/attention_bridge_test.go), [pkg/reviews/attention_bridge_sqlite_test.go](../../pkg/reviews/attention_bridge_sqlite_test.go), [pkg/reviews/workflow_retention.go](../../pkg/reviews/workflow_retention.go), [pkg/gateway/review_attention_bridge_test.go](../../pkg/gateway/review_attention_bridge_test.go), [web/backend/api/reviews_test.go](../../web/backend/api/reviews_test.go), [web/backend/api/workflow_attention_privacy.go](../../web/backend/api/workflow_attention_privacy.go), [web/backend/api/review_attention_workflow_suppression_test.go](../../web/backend/api/review_attention_workflow_suppression_test.go), [cmd/picoclaw/internal/workflow/helpers.go](../../cmd/picoclaw/internal/workflow/helpers.go), [cmd/picoclaw/internal/workflow/retention_test.go](../../cmd/picoclaw/internal/workflow/retention_test.go), [web/frontend/src/api/review-attention.test.ts](../../web/frontend/src/api/review-attention.test.ts), [web/frontend/src/components/reviews/reviews-page.test.tsx](../../web/frontend/src/components/reviews/reviews-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-050` | [pkg/eventing/webhook/github.go](../../pkg/eventing/webhook/github.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [pkg/workflows/event_trigger_test.go](../../pkg/workflows/event_trigger_test.go), [web/frontend/src/components/events/event-sources-page.tsx](../../web/frontend/src/components/events/event-sources-page.tsx), [web/frontend/src/components/events/event-sources-page.test.tsx](../../web/frontend/src/components/events/event-sources-page.test.tsx) |
| `FR-EVENT-AUTOMATION-051` | [pkg/prdevelopment/capture.go](../../pkg/prdevelopment/capture.go), [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/capture_test.go](../../pkg/prdevelopment/capture_test.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_schema_sqlite.go](../../pkg/eventing/pr_development_schema_sqlite.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/workflows/event_dispatcher_test.go](../../pkg/workflows/event_dispatcher_test.go), [pkg/gateway/pr_development_capture_test.go](../../pkg/gateway/pr_development_capture_test.go), [cmd/picoclaw/internal/workflow/command_test.go](../../cmd/picoclaw/internal/workflow/command_test.go) |
| `FR-EVENT-AUTOMATION-052` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/prdevelopment](../../pkg/prdevelopment), [pkg/gateway](../../pkg/gateway), [web/backend/api](../../web/backend/api), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-053` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_conversation_schema_sqlite.go](../../pkg/eventing/pr_development_conversation_schema_sqlite.go), [pkg/eventing/pr_development_conversation_store_sqlite.go](../../pkg/eventing/pr_development_conversation_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/review_store_sqlite.go](../../pkg/eventing/review_store_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/pr_development_conversation_store_sqlite_test.go](../../pkg/eventing/pr_development_conversation_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/prdevelopment/chat_test.go](../../pkg/prdevelopment/chat_test.go), [pkg/workflows/context.go](../../pkg/workflows/context.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/channels/manager.go](../../pkg/channels/manager.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [web/backend/api/reviews.go](../../web/backend/api/reviews.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/i18n/locales/en.json](../../web/frontend/src/i18n/locales/en.json), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-054` | [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/github_case_test.go](../../pkg/prdevelopment/github_case_test.go), [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go), [pkg/tools/toolloop_test.go](../../pkg/tools/toolloop_test.go), [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go), [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-EVENT-AUTOMATION-055` | [pkg/eventing/pr_development_repair_schema_sqlite.go](../../pkg/eventing/pr_development_repair_schema_sqlite.go), [pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go), [pkg/eventing/pr_development_repair_store_sqlite_test.go](../../pkg/eventing/pr_development_repair_store_sqlite_test.go), [pkg/prdevelopment/repair_worker.go](../../pkg/prdevelopment/repair_worker.go), [pkg/prdevelopment/repair_worker_test.go](../../pkg/prdevelopment/repair_worker_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/agent/local_repair_factory.go](../../pkg/agent/local_repair_factory.go), [pkg/agent/local_repair_factory_test.go](../../pkg/agent/local_repair_factory_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [pkg/gateway/pr_development_repair_runtime_test.go](../../pkg/gateway/pr_development_repair_runtime_test.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-EVENT-AUTOMATION-056` | [pkg/gitworkspace/pinned_commit.go](../../pkg/gitworkspace/pinned_commit.go), [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go) |
| `FR-EVENT-AUTOMATION-057` | [pkg/eventing/webhook/github.go](../../pkg/eventing/webhook/github.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_thread_schema_sqlite.go](../../pkg/eventing/pr_development_thread_schema_sqlite.go), [pkg/eventing/pr_development_thread_store_sqlite.go](../../pkg/eventing/pr_development_thread_store_sqlite.go), [pkg/eventing/pr_development_thread_store_sqlite_test.go](../../pkg/eventing/pr_development_thread_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/prdevelopment/capture.go](../../pkg/prdevelopment/capture.go), [pkg/prdevelopment/capture_test.go](../../pkg/prdevelopment/capture_test.go), [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/github_case_test.go](../../pkg/prdevelopment/github_case_test.go), [pkg/gateway/pr_development_capture_test.go](../../pkg/gateway/pr_development_capture_test.go) |
| `FR-EVENT-AUTOMATION-058` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_controller_schema_sqlite.go](../../pkg/eventing/pr_development_controller_schema_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |

Additional `FR-EVENT-AUTOMATION-057` acceptance anchors are
[pkg/eventing/store_types.go](../../pkg/eventing/store_types.go),
[pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go),
[pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go),
and [pkg/prdevelopment/repair_worker.go](../../pkg/prdevelopment/repair_worker.go).

Additional `FR-EVENT-AUTOMATION-058` implementation and acceptance anchors are
[pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go),
[pkg/eventing/pr_development_controller_store_sqlite_test.go](../../pkg/eventing/pr_development_controller_store_sqlite_test.go),
[pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go),
and [pkg/eventing/pr_development_repair_store_sqlite_test.go](../../pkg/eventing/pr_development_repair_store_sqlite_test.go).

## Implementation Anchors

- [pkg/eventing/envelope.go](../../pkg/eventing/envelope.go)
- [pkg/eventing/redaction.go](../../pkg/eventing/redaction.go)
- [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go)
- [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go)
- [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go)
- [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go)
- [pkg/eventing/pr_development_schema_sqlite.go](../../pkg/eventing/pr_development_schema_sqlite.go)
- [pkg/eventing/pr_development_conversation_schema_sqlite.go](../../pkg/eventing/pr_development_conversation_schema_sqlite.go)
- [pkg/eventing/pr_development_controller_schema_sqlite.go](../../pkg/eventing/pr_development_controller_schema_sqlite.go)
- [pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go)
- [pkg/eventing/pr_development_conversation_store_sqlite.go](../../pkg/eventing/pr_development_conversation_store_sqlite.go)
- [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go)
- [pkg/eventing/webhook/github.go](../../pkg/eventing/webhook/github.go)
- [pkg/prdevelopment/capture.go](../../pkg/prdevelopment/capture.go)
- [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go)
- [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go)
- [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go)
- [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go)
- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/prdevelopment](../../pkg/prdevelopment)
- [pkg/config/events.go](../../pkg/config/events.go)
- [pkg/config/reviews.go](../../pkg/config/reviews.go)
- [pkg/workflows/event_trigger.go](../../pkg/workflows/event_trigger.go)
- [pkg/workflows/event_dispatcher.go](../../pkg/workflows/event_dispatcher.go)
- [pkg/workflows/origin.go](../../pkg/workflows/origin.go)
- [pkg/workflows/editor.go](../../pkg/workflows/editor.go)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/workflows/context.go](../../pkg/workflows/context.go)
- [pkg/agent/agent.go](../../pkg/agent/agent.go)
- [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
- [pkg/agent/workflow_eventing.go](../../pkg/agent/workflow_eventing.go)
- [pkg/channels/manager.go](../../pkg/channels/manager.go)
- [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go)
- [pkg/gateway/review_working_context.go](../../pkg/gateway/review_working_context.go)
- [pkg/gateway/review_attention_policy.go](../../pkg/gateway/review_attention_policy.go)
- [pkg/gateway/event_webhook.go](../../pkg/gateway/event_webhook.go)
- [pkg/gateway/event_channel.go](../../pkg/gateway/event_channel.go)
- [pkg/gateway/event_operator.go](../../pkg/gateway/event_operator.go)
- [web/backend/api](../../web/backend/api)
- [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts)
- [web/frontend/src/components/reviews](../../web/frontend/src/components/reviews)
- [web/frontend/src/routes/reviews.tsx](../../web/frontend/src/routes/reviews.tsx)
- [pkg/eventing/webhook](../../pkg/eventing/webhook)
- [pkg/eventing/channelmessage](../../pkg/eventing/channelmessage)
- [pkg/eventing/operator](../../pkg/eventing/operator)
- [pkg/eventing/githubpoll](../../pkg/eventing/githubpoll)
- [pkg/eventing/review_types.go](../../pkg/eventing/review_types.go)
- [pkg/eventing/review_store_sqlite.go](../../pkg/eventing/review_store_sqlite.go)
- [pkg/eventing/review_decision_run_sqlite.go](../../pkg/eventing/review_decision_run_sqlite.go)
- [pkg/eventing/review_attention_trigger_sqlite.go](../../pkg/eventing/review_attention_trigger_sqlite.go)
- [pkg/reviews](../../pkg/reviews)
- [pkg/reviews/attention_trigger_worker.go](../../pkg/reviews/attention_trigger_worker.go)
- [pkg/reviews/session_bridge.go](../../pkg/reviews/session_bridge.go)
- [pkg/reviews/attention.go](../../pkg/reviews/attention.go)
- [pkg/reviews/attention_bridge.go](../../pkg/reviews/attention_bridge.go)
- [pkg/reviews/attention_config.go](../../pkg/reviews/attention_config.go)
- [web/frontend/src/api/event-sources.ts](../../web/frontend/src/api/event-sources.ts)
- [web/frontend/src/api/events.ts](../../web/frontend/src/api/events.ts)
- [web/frontend/src/api/reviews.ts](../../web/frontend/src/api/reviews.ts)
- [web/frontend/src/api/review-attention-json.ts](../../web/frontend/src/api/review-attention-json.ts)
- [web/frontend/src/api/review-attention.ts](../../web/frontend/src/api/review-attention.ts)
- [web/frontend/src/components/events](../../web/frontend/src/components/events)
- [web/frontend/src/components/reviews](../../web/frontend/src/components/reviews)
- [web/frontend/src/routes/event-sources.tsx](../../web/frontend/src/routes/event-sources.tsx)
- [web/frontend/src/routes/events.tsx](../../web/frontend/src/routes/events.tsx)
- [web/frontend/src/routes/reviews.tsx](../../web/frontend/src/routes/reviews.tsx)
- [pkg/bus/bus.go](../../pkg/bus/bus.go)
- [pkg/channels/deltachat/handler.go](../../pkg/channels/deltachat/handler.go)
- [web/backend/api/config.go](../../web/backend/api/config.go)
- [web/backend/api/events.go](../../web/backend/api/events.go)
- [web/backend/api/reviews.go](../../web/backend/api/reviews.go)
- [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go)
- [web/backend/api/review_attention_policies.go](../../web/backend/api/review_attention_policies.go)
- [web/backend/api/workflows.go](../../web/backend/api/workflows.go)
- [web/backend/api/workflow_human_tasks.go](../../web/backend/api/workflow_human_tasks.go)
- [web/backend/api/review_attention_agents.go](../../web/backend/api/review_attention_agents.go)
- [web/backend/api/workflow_editor.go](../../web/backend/api/workflow_editor.go)
- [web/backend/api/workflow_event_context.go](../../web/backend/api/workflow_event_context.go)
