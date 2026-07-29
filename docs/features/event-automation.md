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
storage independently. A separate authenticated event-source manager exposes
the opt-in master switch, storage and redaction policy, secure Standard
Webhooks/GitHub connector lifecycle, and eligible Delta Chat email adapters
through the existing configuration and gateway lifecycle APIs. Durable inputs
remain separate from the process-local
[`pkg/events`](../../pkg/events) observability bus: runtime events are
best-effort in-process signals, while external automation events and dispatch
state survive restart.

## Reconstruction Notes

- Similarity target: recreate an opt-in `pkg/eventing` package with immutable
  normalized envelopes, an `Inbox` behavior interface, and one portable
  SQLite-backed `Store` that owns inbox, routing, and workflow-dispatch state.
- Core types/functions: `Envelope`, `Actor`, `Subject`, envelope normalization,
  validation, and cloning helpers, `Redactor`, `Inbox`, `Store`,
  `Open`/`OpenStore`, store options,
  routing/dispatch claim and completion records, replay records, retention
  results, `config.EventIngressConfig` with effective-default resolution,
  workflow `EventTrigger`/`EventEntityTrigger`, deterministic trigger matching,
  gateway-owned `EventWorkflowRouter`/`EventWorkflowDispatcher` workers,
  `channelmessage.Backend`/`Controller`, and the message-bus inbound admission
  seam.
- Runtime ordering: resolve disabled-safe config, normalize and validate an
  envelope, enforce the payload limit, redact configured fields, atomically
  insert or return the existing deduplicated event, lease and renew routing,
  create deterministic per-workflow dispatches through the current claim,
  reconcile the deterministic run ID, exclusively create a new run, link its
  dispatch before effects, execute with lease renewal, then retain or replay
  only through explicit store operations.
- Non-obvious constraints: deduplication is scoped by source and connector;
  duplicate input never replaces the first stored payload; lease ownership is
  fenced against stale workers; stale routing cannot authorize a dispatch;
  routing and dispatch state are distinct; file run creation is exclusive
  across processes; replay points back to its immutable source event; SQLite
  migrations are transactional; disabled ingress opens no database; and targets
  excluded from the repository's SQLite build matrix must compile through an
  unsupported stub instead of acquiring a new platform dependency. Matching is
  deterministic and AI decisions belong inside a matched workflow, not inside
  the router. Configured channel admission is synchronous with durable insert;
  Delta Chat advances its provider cursor only after that boundary.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-EVENT-AUTOMATION-001` | MUST | Configuration is omitted, explicitly disabled, or resolved for an enabled agent workspace. | Omitted ingress is disabled; effective config defaults its database to `<workspace>/eventing/events.db`, retention to 30 days, payload size to 1 MiB, and redaction to the mandatory sensitive-field set. | Resolution returns an independent effective config and does not open or create storage. | Non-positive limits receive defaults; relative paths resolve under the workspace, absolute paths are preserved, and exactly `~`, `~/`, or `~\` home prefixes are expanded without treating `~name` as home. | Existing installations must remain unchanged until durable ingress is deliberately enabled. |
| `FR-EVENT-AUTOMATION-002` | MUST | A caller submits an external envelope with source, connector, deduplication key, event type, and JSON-object payload, optionally including actor, subject, occurrence time, attributes, and receipt time. | Normalization returns an immutable deep copy, assigns a stable opaque `ev_` ID and missing receipt time, and canonicalizes timestamps to UTC. | A successful store insert commits the normalized inbox data and pending routing state together. | Missing/oversized/non-UTF-8 identity, entity, attribute, or payload data, excessive/invalid attributes, non-object/trailing/invalid JSON, out-of-range timestamps, invalid caller-supplied event/replay IDs, or self-referential replay lineage fail before mutation; caller-owned payloads, maps, pointers, actors, and subjects cannot mutate stored or returned state. | All connectors need one safe, bounded, source-neutral contract. |
| `FR-EVENT-AUTOMATION-003` | MUST | An envelope is prepared for persistence with a payload byte limit, configured sensitive field names, and optional exact secret values. | Sensitive values and embedded secret strings are recursively replaced with `[REDACTED]` while unrelated JSON types and structure are preserved; actor, subject, and envelope attribute strings receive the same protection, and repeated redaction is idempotent. | Only the redacted deep copy is eligible for durable insertion. | Oversized or invalid JSON is rejected; key matching ignores case and punctuation/underscore/camel-case differences, recognizes sensitive suffixes and explicitly configured punctuation-only keys, and descends through nested objects/arrays. An exact configured secret in a JSON or attribute-map key fails closed rather than leaking or causing a redacted-key collision. The caller's input is never rewritten. | Durable automation must not turn provider secrets or credentials into an unbounded local archive. |
| `FR-EVENT-AUTOMATION-004` | MUST | One or more workers concurrently ingest deliveries with the same non-empty `(source, connector, dedupe_key)`. | Every caller receives the same original event and an observable duplicate indication after exactly one insert. | The first envelope remains authoritative; later duplicates add no inbox/routing state and never overwrite its payload or metadata. | Database contention may wait within the configured SQLite busy timeout, but must not surface as two durable events. | Provider retries and concurrent receivers must be safe. |
| `FR-EVENT-AUTOMATION-005` | MUST | A supported build opens an enabled ingress database for the first time or after restart. | The validated current schema is available with WAL journaling, foreign keys, and connection-local busy handling; an existing current database reopens without losing records. | Schema versions advance transactionally and the database file is restricted to owner access. | A newer unknown version or current-version database missing required tables, columns, or indexes fails closed; failed migration rolls back its version and partial objects; unsupported targets return `ErrUnsupportedPlatform` and do not create a database. | The inbox must be durable without reducing PicoClaw's existing portability. |
| `FR-EVENT-AUTOMATION-006` | MUST | A worker claims routable events with a non-empty worker label, bounded limit, and positive lease duration. | It receives only available pending or expired-lease events, each with a store-generated fresh opaque lease token and deadline. | Claims transition routing state atomically to `claimed`, store the generated token, and increment the attempt count once. | Concurrent claimers cannot own the same live lease; future `available_at` work is skipped; an empty label or invalid claim request mutates nothing. | At-least-once routing needs bounded, restart-recoverable, independently fenced work ownership. |
| `FR-EVENT-AUTOMATION-007` | MUST | A worker transitions routing using the event ID and fresh lease token returned by its claim. | A current token can acknowledge success, nack to pending at an explicit retry time with redacted/bounded error detail, or mark the event dead; stale or foreign tokens receive a typed conflict. | Routing status, availability, cleared lease, sanitized error detail, and update timestamp change atomically without changing the envelope. | A zero/past nack time retries immediately; transition after lease replacement/expiry and duplicate terminal completion cannot clobber newer state. | Per-claim lease fencing prevents slow workers from corrupting recovered work. |
| `FR-EVENT-AUTOMATION-008` | MUST | Routing selects a workflow reference for a durable event and calls `CreateDispatch`. | The store derives stable `dsp_` and `wr_` IDs from the event/workflow pair and returns exactly one pending dispatch before execution. | A pending dispatch is persisted independently of event routing state. | Repeating the same pair returns the existing dispatch; selecting different workflows creates distinct dispatches; a missing event fails; no workflow code is invoked. | Retries must not create duplicate workflow runs or couple routing completion to execution. |
| `FR-EVENT-AUTOMATION-009` | MUST | A worker claims available workflow dispatches, links the expected run ID, and finishes or nacks using the returned dispatch lease token. | Live work is exclusively claimed, expired claimed/running work becomes claimable after restart, a nack schedules pending retry, and the current token can persist `succeeded`, `failed`, or `dead` with redacted/bounded detail. | Dispatch attempts, availability, token/lease fields, `claimed`/`running`/pending/terminal status, sanitized error, link time, and finish time advance atomically. | Future-availability work is skipped; a mismatched run ID, stale token, expired lease, or non-terminal finish status is rejected without mutating newer state. | Workflow delivery needs scheduled recovery, deterministic run linkage, and per-claim fencing guarantees. |
| `FR-EVENT-AUTOMATION-010` | MUST | An operator-layer caller requests replay of an existing durable event. | A new pending event is returned with new `ev_` identity and deduplication identity and a `replay_of` link to the event that was replayed. | The replay adds new inbox/routing state; the source envelope and prior dispatch history remain unchanged. | A missing source fails without mutation; replay itself does not claim routing or launch a workflow; replaying a replay creates another additive record linked to its immediate source. | Operators need auditable reprocessing without rewriting history. |
| `FR-EVENT-AUTOMATION-011` | MUST | An enabled committed event-ingress generation starts, reloads, or reaches its six-hour maintenance interval with a positive retention policy. | Its context-bound retention worker acquires that exact live runtime generation, computes a UTC cutoff from effective retention days, and prunes immediately after activation and periodically thereafter. Each cycle reports removed records, continues after a telemetry-safe failure, and is bounded to 20 batches of 500 rows. | Each batch deletes up to its bounded limit of the oldest eligible `succeeded`/`dead` events and their terminal dispatches transactionally; reload/shutdown cancel and join the worker before closing its store. A retention span whose cutoff precedes the signed Unix-nanosecond storage domain is a safe no-op determined before calendar arithmetic. | Pending/claimed routing, events with pending/claimed/running dispatches, and source events referenced by retained replays are preserved. Provisional or rolled-back generations never prune, an oversized retention value cannot wrap into a future destructive cutoff, a failure never busy-loops or stops later maintenance, disabled ingress creates no worker/database, and a full cycle cannot exceed 10,000 rows. | Storage must remain bounded without deleting actionable work, breaking replay lineage, or letting a failed configuration candidate cause irreversible deletion. |
| `FR-EVENT-AUTOMATION-012` | MUST | The foundation package is constructed or imported while no connector/listener integration is configured. | Existing runtime-event publication, workflow triggers, gateway routes, CLI/API behavior, and UI behavior remain unchanged. | No listener, workflow run, API registration, UI state, or process-local event subscription is created by `pkg/eventing`. | A disabled config or unsupported platform is inert rather than silently falling back to volatile delivery. | PR 1 must introduce durable primitives without disturbing existing infrastructure or pretending later stages exist. |
| `FR-EVENT-AUTOMATION-013` | MUST | A validated workflow declares `on.event` with one or more source, connector, type, actor, subject, or attribute filters. | Scalar or list values parse into typed filters; alternatives within one list use OR, populated fields use AND, and anchored `*`/`?` globs select a workflow deterministically. | Parsing and matching do not mutate the workflow or envelope. | An absent trigger, unknown/typoed field (including one inherited through YAML merge), empty trigger, empty list/map, blank pattern, empty entity filter, or missing required entity/attribute does not match and fails parsing/validation where applicable. Source, connector, event type, and entity type compare case-insensitively; IDs and attribute values remain case-sensitive. | Operators need explicit reviewable routing policy before AI or side-effecting steps run. |
| `FR-EVENT-AUTOMATION-014` | MUST | The router claims one durable event while both ingress and workflows are enabled. | It renews the routing lease, loads current local definitions, skips malformed, invalid, or compatibility-blocked workflows consistently with existing automatic triggers, and creates every matching dispatch idempotently through the current live routing token. It acknowledges only after all selected rows are durable; zero matches is successful routing. | Each new `(event_id, workflow_ref)` creates one deterministic dispatch while the authorizing claim remains live; routing then becomes `succeeded`. | A stale/expired/replaced claim cannot insert even when another worker has already completed routing. Fan-out or lease-renewal failure uses bounded exponential retry, safe duplicate encounter, and `dead` attempt exhaustion. | A crash or slow catalog scan must neither lose selected work, duplicate a dispatch, nor let a stale routing claim authorize work. |
| `FR-EVENT-AUTOMATION-015` | MUST | The dispatcher claims a durable event/workflow dispatch with its deterministic run ID. | Before execution it loads the redacted envelope, rechecks current workflow validation/compatibility, renews its claim, and invokes the shared executor with exactly the dispatch run ID. The executor exclusively creates the durable run and calls `OnRunPersisted`; that callback links the dispatch and renews again before any workflow side effect. | The dispatch moves through claimed/running to succeeded, failed, pending retry, or dead while the normal file run store records the workflow run. | Pre-run failures retry with bounded exponential backoff and become dead after the configured attempt limit. A link/callback or ordinary workflow failure leaves a terminal durable run and becomes dispatch `failed` rather than being executed again. | Durable intent must be linked to the same auditable workflow run before effects on every recovery path. |
| `FR-EVENT-AUTOMATION-016` | MUST | A claimed dispatch is recovered after restart and its deterministic run may be absent, terminal, still marked running, or missing after it was linked. | A never-created run may start; an existing successful run completes the dispatch successfully; an existing failed/canceled/skipped run completes it failed; an orphan running/unknown run is canceled as interrupted and completes failed without repeating workflow side effects. A linked dispatch whose run record disappeared fails closed without replay. | Reconciliation updates only the owned dispatch and existing run record. The first file run create uses a cross-process exclusive filesystem boundary and returns only after syncing the run file, run directory, store root, and workspace directory; later terminal updates use atomic synced replacement. | Duplicate run creation and concurrency-limit failures are typed; a crash before exclusive durable run creation remains retryable, while a crash after creation cannot start that run again even if normal run retention later removes its record. | Exactly-once external effects are not generally possible, so the safe recovery boundary is the durable run record plus its pre-effect dispatch link. |
| `FR-EVENT-AUTOMATION-017` | MUST | A workflow starts from an external event. | `event` exposes detached `id`, `source`, `connector`, `type`, actor, subject, occurrence/receipt times, payload, attributes, and replay lineage; inputs additionally expose `event_id`, `dispatch_id`, `source`, `connector`, `type`, and the event object. JSON numbers retain their original decimal token for exact conditions and persisted event snapshots. The session is `workflow:<ref>:event:<event-id>` and delivery is empty. | The executor persists detached input/event snapshots in the normal run record without changing existing non-event workflow numeric value types. | Only the already-redacted durable envelope reaches workflow context. No arbitrary raw payload path participates in router policy; connectors promote routing facts into normalized fields/attributes. | Deterministic routing stays small while workflows retain full-fidelity context for deterministic or AI-driven decisions. |
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
| `FR-EVENT-AUTOMATION-030` | MUST | An enabled `events.ingress.webhooks.<connector>` selects `format: github` and GitHub sends one bounded JSON-object delivery with exactly one `X-Hub-Signature-256`, `X-GitHub-Delivery`, and `X-GitHub-Event` header. | The adapter verifies the `sha256=` HMAC over the exact raw body before decoding, passes the complete authenticated object to the ordinary payload redaction and normalization path, assigns source `github`, the path connector, type `<event>` or `<event>.<action>`, and the delivery ID as deduplication key, and promotes only bounded sender/repository metadata. Envelope attributes explicitly record `body_authenticated=true`, `headers_authenticated=false`, and `signature_algorithm=hmac-sha256`. | The ordinary redacting inbox atomically owns a new delivery before `202`, or returns the retained first event with `200` and `inserted: false`; it uses the same generation-fenced shared route and lifecycle as Standard Webhooks. | Missing, duplicated, malformed, or oversized authentication headers fail uniformly with `401`; malformed JSON, an invalid signed action, or a secret-bearing identity fails before mutation with `400`. GitHub signs the body but not its event/delivery headers and supplies no signed timestamp, so public ingress requires trusted TLS termination. The local default body limit is 1 MiB even though GitHub permits payloads up to 25 MiB. Deduplication protects a delivery only while its durable event remains retained; a redelivery after eligible pruning is a new event. | Native mapping makes GitHub automation useful without a second listener or a parallel durability, redaction, workflow, or reload system, while preserving the provider protocol's real trust boundary. |
| `FR-EVENT-AUTOMATION-031` | MUST | An operator explicitly installs `github-issue-triage` while native GitHub ingress, workflows, and a non-deferred GitHub MCP server are configured. | The installed workflow deterministically matches source `github`, type `issues.opened`, and `body_authenticated=true`; a no-tool classifier receives a narrow repository/issue projection from the signed body and returns only enum category/priority plus a boolean comment decision. A separate conditional `mcp/github/add_issue_comment` step uses signed-body owner/repository/issue identity and posts fixed bounded text containing the enums and event marker. | Installation writes one local workflow definition and revalidates the local catalog without changing gateway, ingress, model, MCP, or credential configuration. A matched run uses the existing durable dispatch/run state and records classifier/action steps normally. | GitHub's event header remains transport-authenticated only by trusted TLS. Issue text remains untrusted despite the body signature. Invalid model output, disabled/no-tool policy failure, absent MCP capability, or GitHub action failure produces no hidden fallback action. Explicit workflow retry, event replay, or provider redelivery after retention pruning can duplicate the comment because the marker is not a provider idempotency key. | AI classification becomes useful without model-held action authority, a new GitHub client, or changes to existing installations. |

| `FR-EVENT-AUTOMATION-032` | MUST | An authenticated launcher user or local CLI operator requests the live event list, one event, its payload, or the dispatch list while ingress is enabled. | The launcher proxies through the gateway's PID bearer credential and the CLI calls that protected runtime endpoint directly. Lists support exact source/connector/type/status filters and filter-bound, versioned newest-first keyset cursors with a default of 50 and maximum of 100. Dedicated event and dispatch projections and their metadata store queries omit every owner/lease token; event metadata additionally omits deduplication and payload blobs and derives only `length(payload_json)`. Ordinary event responses omit payload, while the explicit payload endpoint returns the already-redacted JSON bytes exactly and all responses prohibit caching. | Read operations mutate no event, routing, dispatch, or workflow state and remain admitted to one live operator-controller generation until the store call and response projection complete. | Missing/invalid filters, IDs, cursors, or limits fail with `400`; missing events return `404`; absent, starting, reloading, stale, or stopped gateway state returns retryable `503`. Disabled ingress registers no operator route and opens no store. Reload rejects new operations and drains admitted calls before closing the old store; delayed cleanup cannot deactivate a replacement. | Operators need inspectable durable state without opening SQLite beside a reloading gateway, materializing a page of payload blobs, exposing worker fencing credentials, or losing exact JSON numbers in browser parsing. |
| `FR-EVENT-AUTOMATION-033` | MUST | An authenticated operator explicitly requests replay of one existing event through the live gateway, and CLI callers additionally pass `--yes`. | Exactly one accepted `POST` with an empty JSON object creates a fresh pending event linked by `replay_of`, returns `201` and its new location, and leaves the source and prior dispatches unchanged. The launcher enforces same-origin browser metadata before proxying; neither client automatically retries a replay. | Replay uses the active generation's ordinary redacting store insertion and therefore creates new routing state that current deterministic workflow definitions process normally. | Missing events return `404`; malformed media type/body/query/ID or cross-site launcher requests fail without mutation. After replay dispatch, storage, cancellation, timeout, or transport failure reports a fixed unknown outcome without `Retry-After`; the operator must inspect replay lineage before deciding whether to issue another explicit request. Every replay can repeat workflows and external effects. | Replay must be deliberate, auditable, and additive rather than a hidden dispatch reset or an unsafe retry abstraction. |
| `FR-EVENT-AUTOMATION-034` | MUST | An authenticated dashboard user opens the Events route, changes exact event filters, selects an event, requests more results, explicitly reveals its payload, or opens replay confirmation. | The responsive master/detail surface keeps normalized filters and selection in the URL, keeps opaque cursors only in filter-bound query state, lists newest events, shows token-free event and dispatch projections, and loads exact payload text only after an explicit action. Replay presents an unmistakable duplicate-effects warning and sends one non-retried empty-object request only after confirmation. | Inspection and filter/selection changes mutate no durable state. Payload text is discarded when selection changes. A successful replay creates the additive event defined by `FR-EVENT-AUTOMATION-033`, invalidates affected reads, and selects the returned event. | Loading, empty, unavailable, malformed-response, not-found, and replay-failure states remain operable on desktop and narrow mobile widths. Payload never enters route state, browser persistence, logs, toast text, clipboard state, or HTML interpretation; cancel sends no request, failure keeps confirmation available, and ambiguous replay failure is never retried automatically. | Operators need a safe, accessible control plane that preserves the API's least-exposure and replay boundaries instead of turning inspection into hidden data retention or side effects. |
| `FR-EVENT-AUTOMATION-035` | MUST | An authenticated dashboard user opens `/event-sources`, edits the ingress master switch or storage/redaction policy, adds, edits, disables, or removes a Standard Webhooks/GitHub connector, chooses a connector secret action, or opts an available Delta Chat instance into `mirror` or `event_only` email admission. | The responsive accessible editor loads only the safe configuration projection, shows existing webhook credentials as presence metadata, derives each token-free `/webhooks/events/{connector}` endpoint, warns that public GitHub delivery requires HTTPS, and presents available Delta Chat instances plus retained missing/disabled adapter references with explicit dependency state. Management reads remain available to repair unresolved active webhook references and unavailable adapter dependencies, but validate every serialized webhook name/format and channel map key against all configured sensitive values and fail opaquely rather than project a credential-bearing public identity. Optional retention and payload limits accept blank defaults or positive safe integers; connector names match `^[A-Za-z][A-Za-z0-9_-]{0,63}$` and are locale-independently case-insensitively unique; persisted connector names remain stable so their credential identity cannot move implicitly; enabled connectors use a format-valid configured, entered, or cryptographically generated secret; changing a persisted connector format requires a compatible replacement for any configured secret; and, while master ingress is active, an enabled adapter must reference an existing enabled Delta Chat channel. After save, the page reloads the safe projection, refreshes gateway state, and surfaces the shared restart-required notice when the effective active event-ingress runtime signature changed. | No draft mutates configuration before explicit save. The editor sends one scoped RFC 7396 `PATCH /api/config`: null policy values restore effective defaults, omitted or preserve-mode secret fields retain the secure value, a concrete secret rotates it, an explicit empty secret clears only a disabled connector, and null map tombstones remove deleted webhook/adapter entries without replacing unrelated configuration. Erasing a replacement field reverts to preservation; renaming is an explicit add-new/remove-old operation. A generated or entered secret remains input-only until save. With master ingress disabled, source edits remain runtime-inert. While it is active, the restart signature covers effective policy, enabled webhooks/adapters, workflow-dispatch executor settings, and digests of the complete exact-secret redactor input; inactive non-secret routing metadata does not create a false restart requirement, but rotating any configured credential may require restart so the running store learns the new redaction value. | Invalid policy, duplicate/invalid names, a public identity containing any configured sensitive value, invalid Standard `whsec_` canonical-base64 material, invalid GitHub UTF-8/trim/32–256-byte material, a preserved configured secret after format change, an enabled connector without a secret while master ingress is active, or an unavailable/disabled Delta Chat dependency while master ingress is active blocks save with field-level or opaque boundary guidance as appropriate. Load, validation, and save failures remain actionable without losing an unsaved draft; background gateway polling continues to retry a temporarily unavailable status read. A clear action requires the connector to be disabled. Existing, generated, and replacement secret bytes never enter route state, endpoint text, browser persistence, logs, toast text, restart signatures, or read responses; the disabled default remains inert and no new listener is created. | Event ingestion must be configurable without raw JSON editing while preserving secure-string, opt-in, validation, and existing shared-listener/restart boundaries. |

## Data And State Model

The normalized `Envelope` is an immutable value. Its identity fields distinguish
the source family, configured connector, provider delivery/deduplication key,
and source event type. It carries a required JSON-object payload, optional
`Actor` and `Subject`, string attributes, an optional occurrence timestamp, a
receipt timestamp, and optional replay lineage. Normalization assigns an
`ev_`-prefixed durable ID and missing receipt time, then converts timestamps to
UTC. A replay is another event with its own identity and routing lifecycle,
plus a reference to its original; the original record never changes.

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
  deterministic run ID and its own `pending`, `claimed`, `running`,
  `succeeded`, `failed`, or `dead` lifecycle, availability time, fresh lease
  token, and lifecycle timestamps; and
- unique indexes for `(source, connector, dedupe_key)`, event/workflow dispatch,
  and workflow run identity.

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
Owns: CODE pkg/config/config.go
Owns: CODE pkg/config/events.go
Owns: CODE pkg/config/defaults.go
Owns: CODE pkg/workflows/event_trigger.go
Owns: CODE pkg/workflows/event_dispatcher.go
Owns: CODE pkg/agent/workflow_eventing.go
Owns: CODE pkg/gateway/event_automation.go
Owns: CODE pkg/gateway/event_webhook*
Owns: CODE pkg/gateway/event_channel*
Owns: CODE pkg/gateway/event_operator*
Owns: CODE cmd/picoclaw/internal/events/**
Owns: CODE web/backend/api/config.go
Owns: CODE web/backend/api/events.go
Owns: CODE web/frontend/src/api/event-sources.ts
Owns: CODE web/frontend/src/api/events.ts
Owns: CODE web/frontend/src/components/events/**
Owns: CODE web/frontend/src/routes/event-sources.tsx
Owns: CODE web/frontend/src/routes/events.tsx
Owns: CONFIG.events
Owns: CONFIG.events.ingress*
Owns: CONFIG.events.ingress.webhooks*
Owns: CONFIG.events.ingress.channels*
Owns: HTTP POST /webhooks/events/*
Owns: HTTP GET /runtime/eventing/*
Owns: HTTP POST /runtime/eventing/events/*/replay
Owns: HTTP /api/events*
Owns: CLI cmd/picoclaw/internal/events/*
Owns: TEST pkg/eventing/*
Owns: TEST pkg/eventing/webhook/*
Owns: TEST pkg/eventing/channelmessage/*
Owns: TEST pkg/config/events*
Owns: TEST pkg/workflows/event_trigger_test.go
Owns: TEST pkg/workflows/event_dispatcher_test.go
Owns: TEST pkg/gateway/event_automation_test.go
Owns: TEST pkg/gateway/event_webhook_test.go
Owns: TEST pkg/gateway/event_channel_test.go
Owns: TEST pkg/gateway/event_operator_test.go
Owns: TEST cmd/picoclaw/internal/events/*
Owns: TEST web/backend/api/config_test.go
Owns: TEST web/backend/api/config_event_channel_test.go
Owns: TEST web/backend/api/events_test.go
Owns: TEST web/frontend/src/api/event-sources.test.ts
Owns: TEST web/frontend/src/api/events.test.ts
Owns: TEST web/frontend/src/components/events/event-sources-page.test.tsx
Owns: TEST web/frontend/src/components/events/*
Owns: TEST web/frontend/tests/ui-smoke.spec.ts

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `events.ingress.enabled` | Opt-in master switch; omitted and explicit `false` preserve the pre-feature runtime and create no database. | `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-012`, `FR-EVENT-AUTOMATION-035` |
| Config | `events.ingress.database_path`, `retention_days`, `max_payload_bytes`, `redact_fields` | Resolve a safe workspace database default while preserving explicit policy values used by store construction and ingest/retention calls. | `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-003`, `FR-EVENT-AUTOMATION-011`, `FR-EVENT-AUTOMATION-035` |
| Config | `events.ingress.webhooks.<connector>` | Opt-in connector name, enablement, `standard`/`github` format, and securely persisted format-specific secret; omitted format remains Standard Webhooks. Enabled values are validated before storage or route construction and passed to durable exact-secret redaction. | `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-024`, `FR-EVENT-AUTOMATION-030`, `FR-EVENT-AUTOMATION-035` |
| Config | `events.ingress.channels.<channel-instance>` | Opt-in source and mirror/event-only mode for one existing enabled channel instance, with Delta Chat email defaults and enabled-load/API validation. | `FR-EVENT-AUTOMATION-025`, `FR-EVENT-AUTOMATION-035` |
| HTTP | `GET /api/config`, `PUT /api/config`, `PATCH /api/config` | The authenticated update-safe read projection masks configured webhook secrets, permits repair of unresolved references/dependencies, and opaquely refuses any public event identity containing a configured sensitive value. Omitted or `[NOT_HERE]` webhook secrets preserve the current secure value; an explicit valid secret rotates it, an explicit empty value can clear only a disabled connector, and a merge-patch null map value removes the connector and its security overlay. | `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-024`, `FR-EVENT-AUTOMATION-035` |
| HTTP | `POST /webhooks/events/{connector}` | Strict, bounded format-selected Standard Webhooks or native GitHub authentication and normalization with durable `202`/duplicate `200` acknowledgement and retry-safe error statuses. | `FR-EVENT-AUTOMATION-022`, `FR-EVENT-AUTOMATION-023`, `FR-EVENT-AUTOMATION-030` |
| HTTP | `/runtime/eventing/*`, launcher `/api/events*` proxy | PID-bearer-protected, generation-fenced event/dispatch inspection, exact opt-in payload text, and explicit additive replay; the launcher substitutes its authenticated session boundary without forwarding browser credentials. | `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-033` |
| CLI | `picoclaw events list|get|payload|dispatches|replay` | Call the live protected gateway using the local PID credential, print bounded projected JSON, emit an explicitly requested payload's validated object bytes exactly, and require `--yes` before a non-retried replay. | `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-033` |
| Frontend | authenticated `/events` dashboard route | Responsive filter-bound event inspection, selected-event dispatch history, explicit exact-text payload reveal, and warned non-retried replay through launcher-owned authenticated endpoints. | `FR-EVENT-AUTOMATION-034` |
| Frontend | authenticated `/event-sources` route and Events-page link | Responsive visual management of the ingress master/policy, secure Standard Webhooks/GitHub connector CRUD, eligible Delta Chat email adapters, dependency and endpoint warnings, scoped save, and restart-required feedback. | `FR-EVENT-AUTOMATION-035` |
| Go API | `bus.InboundAdmission`, `MessageBus.PublishInboundWithPreparation` | Synchronous detached channel-origin admission before queue and conditional turn UX; internal messages and unconfigured channels preserve direct queueing. | `FR-EVENT-AUTOMATION-027` |
| Go API | `pkg/eventing/channelmessage.Backend`, `Controller` | Bounded safe message normalization, hashed deduplication, synchronous store insertion, mirror/event-only decision, and exact prepared-generation activation/drain. | `FR-EVENT-AUTOMATION-026`, `FR-EVENT-AUTOMATION-027`, `FR-EVENT-AUTOMATION-029` |
| Runtime | Delta Chat ordered provider queue, notification events, and acknowledgement loop | Drain `get_next_msgs` on startup and provider wake events, correlate full-download replacement IDs process-locally, retry strictly in order before cursor advancement, and expose only safe event metadata. | `FR-EVENT-AUTOMATION-028` |
| Go API | `pkg/eventing.Envelope` | Source-neutral immutable external-event input and stored representation with connector-scoped deduplication and optional replay lineage. | `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004`, `FR-EVENT-AUTOMATION-010` |
| Go API | `pkg/eventing.Inbox` / `Store` | `Inbox` defines `Insert`, `Get`, newest-first filtered keyset list, routing claim/ack/nack/dead transitions, dispatch create/get/claim/link/nack/finish/keyset-list, `Replay`, bounded `Prune`, and `Close`; `Store` is its SQLite implementation. The contract provides atomic deduplication and fresh-token fenced leases. | `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004`, `FR-EVENT-AUTOMATION-006` through `FR-EVENT-AUTOMATION-011` |
| Storage | `pkg/eventing.Open` / `OpenStore`, `WithMaxPayloadBytes`, `WithClock`, `WithBusyTimeout`, `WithRedaction` | Open the embedded store with transactional `PRAGMA user_version` migration, WAL, foreign keys, busy handling, one authoritative SQLite connection, restrictive permissions, restart persistence, a one-MiB default payload limit, mandatory/custom redaction, optional exact-secret replacement, and deterministic test clocks on supported targets. | `FR-EVENT-AUTOMATION-003`, `FR-EVENT-AUTOMATION-005` through `FR-EVENT-AUTOMATION-011` |
| Go API | `pkg/eventing.RoutingDispatchCreator`, `RoutingLeaseRenewer`, `DispatchLeaseRenewer` | Additive capabilities that create a dispatch only through the current routing claim and renew current live leases without expanding the compatibility-critical `Inbox` interface. Routing renewal remains optional; `EventDispatchInbox` requires dispatch renewal because interrupted-run cancellation cannot otherwise be fenced across stores. | `FR-EVENT-AUTOMATION-014`, `FR-EVENT-AUTOMATION-018` |
| Workflow YAML | `on.event` | Typed source/connector/type/entity/attribute filters with scalar/list syntax, explicit non-empty validation, anchored globs, and deterministic case rules. | `FR-EVENT-AUTOMATION-013`, `FR-EVENT-AUTOMATION-021` |
| CLI / Workflow YAML | `picoclaw workflow install github-issue-triage` / `workflows/github-issue-triage.yml` | Explicitly install the deterministic native GitHub issue trigger, isolated structured classifier, and declared conditional GitHub comment action without changing existing configuration. | `FR-EVENT-AUTOMATION-031` |
| Go API | `EventWorkflowRouter`, `EventWorkflowDispatcher`, `RunRequest.OnRunPersisted`, `LoadRunnableLocalSnapshot`, `EventContextFromEnvelope` | Claim one item, compatibility-check the exact loaded workflow bytes, durably fan out deterministic dispatches, reconcile deterministic runs, link a newly persisted run before effects, renew long leases, and build the detached redacted workflow context. | `FR-EVENT-AUTOMATION-014` through `FR-EVENT-AUTOMATION-018`, `FR-EVENT-AUTOMATION-020`, `FR-EVENT-AUTOMATION-021` |
| Runtime | gateway event automation service, webhook/operator controllers, channel admission controller, and launcher restart signature | Open enabled storage before readiness, initialize workflow runtime dependencies before workers, generation-fence HTTP and channel admission while transactionally draining, replacing, rolling back, and closing services/providers, and report restart-required only when the active effective event runtime changes. | `FR-EVENT-AUTOMATION-019`, `FR-EVENT-AUTOMATION-024`, `FR-EVENT-AUTOMATION-029`, `FR-EVENT-AUTOMATION-032`, `FR-EVENT-AUTOMATION-035` |
| Build | `pkg/eventing` unsupported-platform implementation | Preserves the same construction surface and returns `ErrUnsupportedPlatform` without pulling SQLite into excluded targets. | `FR-EVENT-AUTOMATION-005`, `FR-EVENT-AUTOMATION-012` |

## Algorithms And Ordering

1. Resolve `EventIngressConfig` without side effects. If disabled, stop before
   validating inert webhook/channel entries, creating directories, opening
   SQLite, registering routes/hooks, or starting goroutines. Otherwise validate
   connector names, case collisions, channel instance/source/mode references,
   enabled signing secrets, and shared-route ownership before opening the store.
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
   `wr_` IDs from that pair, and commit them before any future executor is
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
    ordering oldest first, capping work at the requested maximum, and cascading
    deletion to terminal dispatch rows.
12. List events and dispatches newest first with stable timestamp-plus-ID keyset
    cursors. Use 50 rows when a list limit is omitted and cap list, claim, and
    prune batches at 500 rows.
13. When gateway ingress is enabled, open and validate the store synchronously,
    include every enabled webhook secret in exact-value redaction, and build an
    inactive candidate webhook backend with each connector's effective format.
    If workflows are disabled, keep the store open for connector/operations
    ownership but start no routing or dispatch goroutine.
14. A router claims one event, renews its routing lease while reading the current
    local catalog, requires current compatibility, parses and validates each
    candidate, evaluates deterministic `on.event` filters, and atomically fences
    every idempotent dispatch insert through the live routing token. Acknowledge
    only after the complete fan-out is durable; nack live-claim failures with
    capped exponential backoff and dead-letter exhausted routing.
15. A dispatcher claims one delivery, renews immediately, and holds a heartbeat
    through event/run lookup. It renews synchronously again after run lookup and
    before reconciling its deterministic run ID. Terminal runs finish the
    dispatch consistently; an orphan running or unknown run is canceled as
    interrupted only while that renewed token is still current and is never
    executed again. A dispatch already linked to a now-missing run fails closed.
16. If no run exists, require a current runnable workflow, build detached
    redacted event/input context, renew the dispatch lease, and call the shared
    executor with that exact run ID and an `OnRunPersisted` callback. The
    executor exclusively creates the run, invokes the callback to link and
    renew the dispatch, and only then starts lifecycle callbacks or workflow
    steps. Creation returns only after the run file and every directory entry
    needed to find it are synced; later state updates atomically replace the
    prior valid JSON record. A callback or ordinary workflow failure leaves a
    terminal run and a failed dispatch; only a failure before durable run
    creation can retry.
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
    `github`, deduplication from
    `X-GitHub-Delivery`, and type from `X-GitHub-Event` plus a non-empty signed
    top-level action. Promote only bounded sender/repository projections and
    persist that the body is authenticated while headers are not. Reject
    configured signing-secret substrings in connector, type, action, or
    deduplication identities instead of persisting or rewriting them. Insert
    synchronously; return `202` for a new row or `200` with the original ID for
    a duplicate. Workflow routing and execution remain asynchronous after that
    durable transport acknowledgement.
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
    format-specific replacement secrets, and enabled channel dependencies
    before constructing a scoped merge patch. Keep preserved secret fields
    absent, send concrete rotations or explicit disabled clears only on save,
    and use null tombstones for deleted map entries. After persistence, discard
    secret input state, reload the safe projection, recompute launcher gateway
    status from the active effective event-runtime signature, and present the
    ordinary restart-required control when that signature differs.
29. Each committed enabled event-ingress generation starts one context-bound
    retention worker even when workflows are disabled. Acquire the exact live
    runtime generation before every cycle, compute `UTC now - retention_days`,
    and call `Prune` in at most 20 batches of 500 rows. Run once after
    activation and every six hours, warn with error-only telemetry and retry on
    the next interval, and cancel/join the worker before reload rollback,
    replacement, shutdown, or store close. A provisional generation waiting
    for commit, or a candidate later rolled back, cannot delete rows.

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

[Webhook ingress](../../pkg/eventing/webhook) normalizes Standard Webhooks or
native GitHub deliveries directly into this envelope on the channel manager's
shared HTTP mux.
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
- A missing actor, subject, or attribute cannot satisfy `*`; explicit empty
  filters fail validation rather than becoming an accidental catch-all.
- AI classification is expressed as an agent step inside a broadly but
  deterministically matched workflow. The router never invokes a model or
  treats model output as durable delivery identity.
- Event payload numbers remain lossless through expression comparison,
  file-run reads, listing, and cancellation updates, including integers beyond
  float64's exact range and very small exponent values.
- If execution crashes after the run record is created, recovery cancels the
  orphan record and marks the dispatch failed; it does not repeat workflow
  steps. External actions that need exactly-once behavior still require
  provider-supported idempotency keyed by dispatch/run ID.
- A dispatch-link callback failure marks the already-created run failed before
  workflow steps. If a linked run file is later pruned or removed, recovery
  fails the dispatch without reconstructing or replaying that run.
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
- A successful response always names a durable event. Concurrent retries with
  the same `Webhook-Id` or `X-GitHub-Delivery` return the retained original ID
  and first payload; workflow failure occurs after acknowledgement and is
  visible through durable dispatch state instead of changing the HTTP result.
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
- Event-source drafts do not persist on navigation or validation failure.
  Existing webhook secrets are presence-only; preserving one emits no secret
  field, rotating one emits only the newly entered value in the authenticated
  save request, clearing one requires the connector to be disabled, and
  deleting one uses a merge-patch tombstone so its security entry cannot
  survive as an active source. Endpoint previews and GitHub HTTPS warnings
  never contain a credential.
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

GitHub protocol references: [validating webhook
deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries),
[webhook best
practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks),
and [event/payload
schemas](https://docs.github.com/en/webhooks/webhook-events-and-payloads).

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-012` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004` | [pkg/eventing/envelope_test.go](../../pkg/eventing/envelope_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-003` | [pkg/eventing/redaction_test.go](../../pkg/eventing/redaction_test.go), [pkg/eventing/store_replay_redaction_test.go](../../pkg/eventing/store_replay_redaction_test.go) |
| `FR-EVENT-AUTOMATION-005` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go) |
| `FR-EVENT-AUTOMATION-006`, `FR-EVENT-AUTOMATION-007` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-008`, `FR-EVENT-AUTOMATION-009` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
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

## Implementation Anchors

- [pkg/eventing/envelope.go](../../pkg/eventing/envelope.go)
- [pkg/eventing/redaction.go](../../pkg/eventing/redaction.go)
- [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go)
- [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go)
- [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go)
- [pkg/config/events.go](../../pkg/config/events.go)
- [pkg/workflows/event_trigger.go](../../pkg/workflows/event_trigger.go)
- [pkg/workflows/event_dispatcher.go](../../pkg/workflows/event_dispatcher.go)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/agent/workflow_eventing.go](../../pkg/agent/workflow_eventing.go)
- [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go)
- [pkg/gateway/event_webhook.go](../../pkg/gateway/event_webhook.go)
- [pkg/gateway/event_channel.go](../../pkg/gateway/event_channel.go)
- [pkg/gateway/event_operator.go](../../pkg/gateway/event_operator.go)
- [pkg/eventing/webhook](../../pkg/eventing/webhook)
- [pkg/eventing/channelmessage](../../pkg/eventing/channelmessage)
- [pkg/eventing/operator](../../pkg/eventing/operator)
- [web/frontend/src/api/event-sources.ts](../../web/frontend/src/api/event-sources.ts)
- [web/frontend/src/api/events.ts](../../web/frontend/src/api/events.ts)
- [web/frontend/src/components/events](../../web/frontend/src/components/events)
- [web/frontend/src/routes/event-sources.tsx](../../web/frontend/src/routes/event-sources.tsx)
- [web/frontend/src/routes/events.tsx](../../web/frontend/src/routes/events.tsx)
- [pkg/bus/bus.go](../../pkg/bus/bus.go)
- [pkg/channels/deltachat/handler.go](../../pkg/channels/deltachat/handler.go)
- [web/backend/api/config.go](../../web/backend/api/config.go)
- [web/backend/api/events.go](../../web/backend/api/events.go)
