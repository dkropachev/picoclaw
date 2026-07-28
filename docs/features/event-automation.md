# Durable External Event Automation

## Feature ID

`FR-EVENT-AUTOMATION`

## Behavior Summary

Durable external event automation provides the restart-safe foundation for
accepting normalized notifications from GitHub, chat, email, and generic
webhooks. It stores a source-neutral event envelope in an embedded inbox,
deduplicates provider deliveries, tracks event-routing and per-workflow
dispatch work independently, protects stored payloads through size limits and
recursive redaction, and supports bounded retention and auditable replay.

This foundation does not listen on HTTP or channel transports, match workflow
definitions, launch workflows, expose operator APIs, or render UI. Those
surfaces are added by later feature stages. It is also separate from the
process-local [`pkg/events`](../../pkg/events) observability bus: runtime events
are best-effort in-process signals, while external automation events are
durable inputs that survive restart.

## Reconstruction Notes

- Similarity target: recreate an opt-in `pkg/eventing` package with immutable
  normalized envelopes, an `Inbox` behavior interface, and one portable
  SQLite-backed `Store` that owns inbox, routing, and workflow-dispatch state.
- Core types/functions: `Envelope`, `Actor`, `Subject`, envelope normalization,
  validation, and cloning helpers, `Redactor`, `Inbox`, `Store`,
  `Open`/`OpenStore`, store options,
  routing/dispatch claim and completion records, replay records, retention
  results, and `config.EventIngressConfig` with effective-default resolution.
- Runtime ordering: resolve disabled-safe config, normalize and validate an
  envelope, enforce the payload limit, redact configured fields, atomically
  insert or return the existing deduplicated event, lease routing, create
  deterministic per-workflow dispatches, lease/complete each dispatch, then
  retain or replay only through explicit store operations.
- Non-obvious constraints: deduplication is scoped by source and connector;
  duplicate input never replaces the first stored payload; lease ownership is
  fenced against stale workers; routing and dispatch state are distinct;
  replay points back to its immutable source event; SQLite migrations are
  transactional; disabled ingress opens no database; and targets excluded from
  the repository's SQLite build matrix must compile through an unsupported
  stub instead of acquiring a new platform dependency.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-EVENT-AUTOMATION-001` | MUST | Configuration is omitted, explicitly disabled, or resolved for an enabled agent workspace. | Omitted ingress is disabled; effective config defaults its database to `<workspace>/eventing/events.db`, retention to 30 days, payload size to 1 MiB, and redaction to the mandatory sensitive-field set. | Resolution returns an independent effective config and does not open or create storage. | Non-positive limits receive defaults; relative paths resolve under the workspace, absolute paths are preserved, and `~` is expanded. | Existing installations must remain unchanged until durable ingress is deliberately enabled. |
| `FR-EVENT-AUTOMATION-002` | MUST | A caller submits an external envelope with source, connector, deduplication key, event type, and JSON-object payload, optionally including actor, subject, occurrence time, attributes, and receipt time. | Normalization returns an immutable deep copy, assigns a stable opaque `ev_` ID and missing receipt time, and canonicalizes timestamps to UTC. | A successful store insert commits the normalized inbox data and pending routing state together. | Missing/oversized/non-UTF-8 identity or entity fields, excessive/invalid attributes, non-object/trailing/invalid JSON, or an invalid caller-supplied event/replay ID fail before mutation; caller-owned payloads, maps, pointers, actors, and subjects cannot mutate stored or returned state. | All connectors need one safe, bounded, source-neutral contract. |
| `FR-EVENT-AUTOMATION-003` | MUST | An envelope is prepared for persistence with a payload byte limit, configured sensitive field names, and optional exact secret values. | Sensitive keys and embedded secret strings are recursively replaced with `[REDACTED]` while unrelated JSON types and structure are preserved; actor, subject, and envelope attribute strings receive the same protection. | Only the redacted deep copy is eligible for durable insertion. | Oversized or invalid JSON is rejected; key matching ignores case and punctuation/underscore/camel-case differences, recognizes sensitive suffixes, descends through nested objects/arrays, and never rewrites the caller's input. | Durable automation must not turn provider secrets or credentials into an unbounded local archive. |
| `FR-EVENT-AUTOMATION-004` | MUST | One or more workers concurrently ingest deliveries with the same non-empty `(source, connector, dedupe_key)`. | Every caller receives the same original event and an observable duplicate indication after exactly one insert. | The first envelope remains authoritative; later duplicates add no inbox/routing state and never overwrite its payload or metadata. | Database contention may wait within the configured SQLite busy timeout, but must not surface as two durable events. | Provider retries and concurrent receivers must be safe. |
| `FR-EVENT-AUTOMATION-005` | MUST | A supported build opens an enabled ingress database for the first time or after restart. | The current schema is available with WAL journaling, foreign keys, and connection-local busy handling; an existing current database reopens without losing records. | Schema versions advance transactionally and the database file is restricted to owner access. | A newer unknown schema fails closed; failed migration rolls back; unsupported targets return `ErrUnsupportedPlatform` and do not create a database. | The inbox must be durable without reducing PicoClaw's existing portability. |
| `FR-EVENT-AUTOMATION-006` | MUST | A worker claims routable events with a non-empty worker label, bounded limit, and positive lease duration. | It receives only available pending or expired-lease events, each with a store-generated fresh opaque lease token and deadline. | Claims transition routing state atomically to `claimed`, store the generated token, and increment the attempt count once. | Concurrent claimers cannot own the same live lease; future `available_at` work is skipped; an empty label or invalid claim request mutates nothing. | At-least-once routing needs bounded, restart-recoverable, independently fenced work ownership. |
| `FR-EVENT-AUTOMATION-007` | MUST | A worker transitions routing using the event ID and fresh lease token returned by its claim. | A current token can acknowledge success, nack to pending at an explicit retry time with redacted/bounded error detail, or mark the event dead; stale or foreign tokens receive a typed conflict. | Routing status, availability, cleared lease, sanitized error detail, and update timestamp change atomically without changing the envelope. | A zero/past nack time retries immediately; transition after lease replacement/expiry and duplicate terminal completion cannot clobber newer state. | Per-claim lease fencing prevents slow workers from corrupting recovered work. |
| `FR-EVENT-AUTOMATION-008` | MUST | Routing selects a workflow reference for a durable event and calls `CreateDispatch`. | The store derives stable `dsp_` and `wr_` IDs from the event/workflow pair and returns exactly one pending dispatch before execution. | A pending dispatch is persisted independently of event routing state. | Repeating the same pair returns the existing dispatch; selecting different workflows creates distinct dispatches; a missing event fails; no workflow code is invoked. | Retries must not create duplicate workflow runs or couple routing completion to execution. |
| `FR-EVENT-AUTOMATION-009` | MUST | A worker claims available workflow dispatches, links the expected run ID, and finishes or nacks using the returned dispatch lease token. | Live work is exclusively claimed, expired claimed/running work becomes claimable after restart, a nack schedules pending retry, and the current token can persist `succeeded`, `failed`, or `dead` with redacted/bounded detail. | Dispatch attempts, availability, token/lease fields, `claimed`/`running`/pending/terminal status, sanitized error, link time, and finish time advance atomically. | Future-availability work is skipped; a mismatched run ID, stale token, expired lease, or non-terminal finish status is rejected without mutating newer state. | Workflow delivery needs scheduled recovery, deterministic run linkage, and per-claim fencing guarantees. |
| `FR-EVENT-AUTOMATION-010` | MUST | An operator-layer caller requests replay of an existing durable event. | A new pending event is returned with new `ev_` identity and deduplication identity and a `replay_of` link to the event that was replayed. | The replay adds new inbox/routing state; the source envelope and prior dispatch history remain unchanged. | A missing source fails without mutation; replay itself does not claim routing or launch a workflow; replaying a replay creates another additive record linked to its immediate source. | Operators need auditable reprocessing without rewriting history. |
| `FR-EVENT-AUTOMATION-011` | MUST | Retention runs with a non-zero cutoff and positive bounded limit after routing and dispatch processing. | It reports the number of removed records and leaves too-new, non-terminal, or replay-lineage-required work available. | Up to the bounded limit of oldest eligible `succeeded`/`dead` events and their terminal dispatches are deleted transactionally. | Pending/claimed routing, events with pending/claimed/running dispatches, and source events referenced by retained replays are preserved. | Storage must remain bounded without deleting actionable work or breaking replay lineage. |
| `FR-EVENT-AUTOMATION-012` | MUST | The foundation package is constructed or imported while no connector/listener integration is configured. | Existing runtime-event publication, workflow triggers, gateway routes, CLI/API behavior, and UI behavior remain unchanged. | No listener, workflow run, API registration, UI state, or process-local event subscription is created by `pkg/eventing`. | A disabled config or unsupported platform is inert rather than silently falling back to volatile delivery. | PR 1 must introduce durable primitives without disturbing existing infrastructure or pretending later stages exist. |

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
are exactly `ev_` plus 32 lowercase hexadecimal characters.

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
and also scrubs actor, subject, and envelope attributes. Worker labels seed a
diagnostic token prefix only; each claim adds fresh cryptographic randomness
and transitions compare the complete opaque lease token. Store clocks are
injectable and
operation deadlines/cutoffs are explicit where needed so recovery and retention
are deterministic in tests. Store methods return detached values so caller
mutation cannot change durable or concurrently returned state.

`events.ingress.enabled` defaults to `false`.
`events.ingress.database_path` may override the workspace-relative default.
`events.ingress.retention_days` bounds terminal history,
`events.ingress.max_payload_bytes` rejects oversized inputs, and
`events.ingress.redact_fields` adds recursively scrubbed JSON field names to the
mandatory defaults; it cannot remove the built-in sensitive-field set.
That set is `authorization`, `proxy_authorization`, `cookie`, `set_cookie`,
`password`, `passwd`, `secret`, `token`, `access_token`, `refresh_token`,
`api_key`, `client_secret`, `private_key`, `webhook_secret`, and `signature`.
Configuration owns policy; `pkg/eventing` owns normalized data and persistence.

## Surface Ownership

Owns: CODE pkg/eventing/**
Owns: CODE pkg/config/events.go
Owns: CODE pkg/config/defaults.go
Owns: CONFIG.events
Owns: CONFIG.events.ingress*
Owns: TEST pkg/eventing/*
Owns: TEST pkg/config/events*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `events.ingress.enabled` | Opt-in master switch; omitted and explicit `false` preserve the pre-feature runtime and create no database. | `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-012` |
| Config | `events.ingress.database_path`, `retention_days`, `max_payload_bytes`, `redact_fields` | Resolve a safe workspace database default while preserving explicit policy values used by store construction and ingest/retention calls. | `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-003`, `FR-EVENT-AUTOMATION-011` |
| Go API | `pkg/eventing.Envelope` | Source-neutral immutable external-event input and stored representation with connector-scoped deduplication and optional replay lineage. | `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004`, `FR-EVENT-AUTOMATION-010` |
| Go API | `pkg/eventing.Inbox` / `Store` | `Inbox` defines `Insert`, `Get`, newest-first filtered keyset list, routing claim/ack/nack/dead transitions, dispatch create/get/claim/link/nack/finish/keyset-list, `Replay`, bounded `Prune`, and `Close`; `Store` is its SQLite implementation. The contract provides atomic deduplication and fresh-token fenced leases. | `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004`, `FR-EVENT-AUTOMATION-006` through `FR-EVENT-AUTOMATION-011` |
| Storage | `pkg/eventing.Open` / `OpenStore`, `WithMaxPayloadBytes`, `WithClock`, `WithBusyTimeout`, `WithRedaction` | Open the embedded store with transactional `PRAGMA user_version` migration, WAL, foreign keys, busy handling, one authoritative SQLite connection, restrictive permissions, restart persistence, a one-MiB default payload limit, mandatory/custom redaction, optional exact-secret replacement, and deterministic test clocks on supported targets. | `FR-EVENT-AUTOMATION-003`, `FR-EVENT-AUTOMATION-005` through `FR-EVENT-AUTOMATION-011` |
| Build | `pkg/eventing` unsupported-platform implementation | Preserves the same construction surface and returns `ErrUnsupportedPlatform` without pulling SQLite into excluded targets. | `FR-EVENT-AUTOMATION-005`, `FR-EVENT-AUTOMATION-012` |

## Algorithms And Ordering

1. Resolve `EventIngressConfig` without side effects. If disabled, stop before
   creating directories, opening SQLite, registering listeners, or starting
   goroutines.
2. For enabled configuration, resolve an explicit database path or derive
   `<workspace>/eventing/events.db`, validate positive resource policy, create
   positive defaults for non-positive limits, append configured redaction fields
   to the mandatory set, create only the narrow parent directory, and open the
   store with owner-only file permissions.
3. On supported targets, configure every SQLite connection for foreign keys and
   a five-second default busy timeout, enable WAL with normal synchronization,
   constrain the single-node store to one authoritative connection, read the
   schema version, reject newer schemas, and apply each missing migration
   transactionally.
4. Before ingestion, copy and validate identity fields and timestamps, verify
   payload JSON and byte size, recursively redact configured object fields, and
   canonicalize the detached payload.
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
8. Insert selected workflow dispatches idempotently by event/workflow identity,
   derive stable `dsp_` and `wr_` IDs from that pair, and commit them before any
   future executor is allowed to launch a workflow. A claimed dispatcher links
   only that expected run ID before moving to `running`.
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

## Cross-Feature Behavior

[Runtime events and observability](runtime-events.md) may later report
`eventing.*` lifecycle telemetry, but its process-local bus is not the durable
inbox, a delivery source, or a recovery mechanism. Event logging filters never
change durable ingestion.

[Workflows](workflows.md) will later own `on.event` parsing, deterministic
matching, execution, and persisted workflow runs. This feature owns only the
input, routing work, dispatch intent, deterministic run identity, and recovery
state that the workflow dispatcher consumes. Creating a dispatch does not
execute a workflow.

[Chat channels](chat-channels.md) and later GitHub, Delta Chat email, and generic
webhook connectors normalize their provider-specific deliveries into this
envelope. They own authentication, signature verification, acknowledgements,
and transport lifecycles. Later operator API/CLI and dashboard stages consume
the store but do not redefine its deduplication, leasing, redaction, replay, or
retention semantics.

[Security and isolation](security-isolation.md) continues to own connector
credentials and workflow side-effect policy. The durable foundation stores no
listener credentials and grants no mutation authority.

## Failure And Edge Cases

- Empty source, connector, deduplication key, or event type is rejected before
  a transaction starts.
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
- Routing failure does not silently delete dispatch history. Dispatch failure
  does not mutate the source event or report routing success.
- Replay is additive. It cannot replace its source, inherit a live lease, or
  erase earlier dispatch outcomes. Its self-referential foreign key preserves
  the source while a retained replay points to it.
- Retention preserves pending/claimed routing, too-new events, replay parents,
  and events with pending/claimed/running dispatches.
- A database created by a newer schema version fails closed rather than being
  downgraded.
- On unsupported build targets, opening durable ingress returns
  `ErrUnsupportedPlatform`. With ingress disabled, normal PicoClaw behavior
  remains available.
- This stage intentionally has no inbound HTTP path, background listener,
  workflow trigger, agent action, CLI/API endpoint, or frontend route.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EVENT-AUTOMATION-001`, `FR-EVENT-AUTOMATION-012` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-002`, `FR-EVENT-AUTOMATION-004` | [pkg/eventing/envelope_test.go](../../pkg/eventing/envelope_test.go), [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-003` | [pkg/eventing/redaction_test.go](../../pkg/eventing/redaction_test.go) |
| `FR-EVENT-AUTOMATION-005` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go) |
| `FR-EVENT-AUTOMATION-006`, `FR-EVENT-AUTOMATION-007` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-008`, `FR-EVENT-AUTOMATION-009` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |
| `FR-EVENT-AUTOMATION-010`, `FR-EVENT-AUTOMATION-011` | [pkg/eventing/store_sqlite_test.go](../../pkg/eventing/store_sqlite_test.go) |

## Implementation Anchors

- [pkg/eventing/envelope.go](../../pkg/eventing/envelope.go)
- [pkg/eventing/redaction.go](../../pkg/eventing/redaction.go)
- [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go)
- [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go)
- [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go)
- [pkg/config/events.go](../../pkg/config/events.go)
