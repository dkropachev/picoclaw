# Chat Channels And Gateway Delivery

## Feature ID

`FR-CHANNEL`

## Behavior Summary

PicoClaw exposes the agent through chat channels and the gateway. Channels
normalize inbound messages, enforce allow/trigger rules, forward work to the
agent bus, optionally pass opted-in instances through synchronous durable-event
admission, and deliver outbound text/media responses through platform-specific
transports. The authenticated channel catalog and editor include the built-in
Delta Chat email channel with presence-only legacy password handling, required
email validation, sidebar discovery, and ordinary gateway restart feedback.

## Reconstruction Notes

- Similarity target: recreate channel adapters with a common base, manager startup, webhook/socket registration, inbound normalization, outbound workers, and gateway lifecycle.
- Core types/functions: channel factory registry, `BaseChannel`, `ChannelManager`, message bus, gateway bootstrap/reload/shutdown, Pico websocket/media handlers, and the secret-safe Delta Chat catalog/config projection.
- Runtime ordering: load channel config, instantiate enabled adapters, register webhooks, start workers, publish inbound context, queue outbound response, send platform message, emit events.
- Non-obvious constraints: platform-specific allow lists, group trigger logic,
  placeholder/typing UX, reply IDs, media references, rate limiting, closed-bus
  behavior, and provider acknowledgement only after synchronous admission when
  the transport supports retry.

## Requirements

| ID               | Level  | Requirement                                                                                                                                                               | Rationale                                                                          |
| ---------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `FR-CHANNEL-001` | MUST   | Enabled channels start from `channel_list`, use the configured map key as their runtime identity even when it aliases a shared channel type, register any required webhook or socket transport, and report lifecycle events. | Gateway startup and routing must reflect configured delivery paths and distinguish multiple instances of one type. |
| `FR-CHANNEL-002` | MUST   | Inbound channel messages normalize channel, account, space, chat, topic, sender, message ID, mention state, text, and media before entering the bus.                      | Routing and session allocation need common context.                                |
| `FR-CHANNEL-003` | MUST   | Allow lists and group triggers can reject messages before agent execution.                                                                                                | Users need channel-level access and noise control.                                 |
| `FR-CHANNEL-004` | MUST   | Outbound messages preserve reply context and media references where the platform supports them.                                                                           | Replies must land in the expected chat/thread.                                     |
| `FR-CHANNEL-005` | SHOULD | Channels with placeholders or typing indicators emit intermediate UX feedback without changing final response content.                                                    | Long-running turns need visible progress.                                          |
| `FR-CHANNEL-006` | MUST   | Gateway HTTP and websocket routes expose only configured channel, Pico, health, and explicitly registered feature behavior. Additive shared-mux registration detects collisions, returns an identity-owned release function, and never holds the route-map lock while a downstream handler runs. The health server can wrap an additive runtime handler in the same constant-time bearer check used by built-in protected endpoints, verify the configured credential without exposing it, and fail the wrapper closed when no credential exists. Read-only workflow-authoring capability and agent-activity routes use that existing protected listener, are registered and released with their exact runtime generation, and create no additional listener or authorization path. A hostname or adaptive wildcard bind records a reachable numeric PID probe derived from the listener family that actually opened. | Launcher, native Pico clients, and opt-in gateway features share one listener without silently replacing or blocking one another, duplicating process-local authorization logic, or sending bearer authority to an occupied address in another IP family. |
| `FR-CHANNEL-007` | MUST   | Channel-specific command UX forwards generic commands to the central command executor except documented platform-local discovery behavior.                                | Slash command behavior must stay consistent across channels.                       |
| `FR-CHANNEL-008` | MUST   | Send failures, rate limits, and closed buses produce structured errors/events instead of silently dropping messages.                                                      | Operators need diagnoseable delivery failure.                                      |
| `FR-CHANNEL-009` | SHOULD | Browser chat UI preserves readable message layout, accessible composer labeling, responsive controls, and non-overlapping code/message surfaces in light and dark themes. | Chat delivery remains user-facing even when the gateway is stopped or unavailable. |
| `FR-CHANNEL-010` | MUST | Pico browser chat discovers model IDs after selecting an account or account router by sending its `{account_ref}`, then sends the selected reference as `model_name` and the selected upstream ID as `model`; the channel copies both into normalized inbound metadata for that turn. | Chat-window account/model selection must affect only the next turn without relying on a router-owned model or hidden config rewrite. |
| `FR-CHANNEL-011` | MUST | Gateway config reload marks readiness false, preflights replacement storage and event runtime dependencies, pauses new agent runtime admission, drains active turns and reload-owned services, retains the prior provider/config through replacement startup, and resumes turns only after commit or successful rollback. An inbound message holds one generation across workflow-trigger evaluation, route/session reservation, worker-queue wait, and turn completion. Replacement event/heartbeat workers and scheduled jobs are fenced to their exact config generation, the runtime-event workflow subscription is absent throughout the outer transaction, and cron starts only after other fallible replacement initialization. Terminal shutdown remembers Stop before a delayed AgentLoop start, quiesces producers, drains the permanent runtime boundary, then stops outbound channel/media dependencies and closes providers. Cleanup, rollback, or shutdown-drain failure leaves readiness/resources fail-safe and never closes a provider or dependency still reachable by active work. | Channels remain live during reload, so readiness alone cannot prevent a turn, queued lifecycle event, or background service from observing provisional or closed runtime state. |
| `FR-CHANNEL-012` | MUST | Channel reload detaches and retires an old adapter identity before replacement construction, drains its admitted sends, joins its workers, and stops it before starting a same-name candidate. The candidate remains absent from lookup, synchronous send, outbound dispatch, and HTTP routing until `Start` succeeds. Failed or timed-out retirement remains manager-owned and blocks duplicate construction until cleanup succeeds. Shutdown joins dispatchers, retires/cancels workers to release full-queue senders, drains admitted direct sends, joins workers, then stops each adapter; errors are returned and successful stops are not repeated on retry. | A replacement must never overlap the old adapter or receive traffic before it is ready, and shutdown must neither panic on raced queue access nor deadlock behind a full retired queue. |
| `FR-CHANNEL-013` | MUST | After channel allow-list and group-trigger checks, BaseChannel marks accepted transport messages as channel-origin and publishes through the optional synchronous inbound-admission seam. Admission-consumed or rejected messages release turn-scoped media and return without entering the agent queue or creating typing, reaction, or placeholder UX; forwarded and unconfigured messages preserve the existing pre-queue UX and chat path; process-internal bus messages bypass admission. Every forwarded turn receives an opaque process-local UX identity excluded from serialized context. Typing, reaction, and placeholder artifacts are registered as one same-chat generation; the manager serializes each provider transition, detaches and gives the prior generation bounded cleanup before starting the next, and mutates all three artifacts atomically under a separate short-lived map lock. Provider stop/undo callbacks are exact-generation pinned, so a timed-out older callback cannot clear newer UX. Steering preparation rechecks session ownership after slow work, then atomically queues and rebinds same-chat UX to a pinned owner or claims a fresh worker; a committed cross-chat or ownerless steering message immediately removes its secondary chat generation because the active turn cannot own that key. Cancellation, bus closure, abandoned reservations, no-output turns, and stale workers remove only their exact artifacts, while buffered normal/error/tool/stream output retains cleanup ownership. | Durable event-only routing must be additive and cannot leak copied media, strand a steering message, leave an unanswered “Thinking…” artifact, corrupt a concurrent turn, or change non-channel producers. |
| `FR-CHANNEL-014` | MUST | Delta Chat treats provider events only as notifications: startup, every `IncomingMsg`, and every message-specific `MsgsChanged` wake an ascending `get_next_msgs` drain; pending downloads also wake on generic `MsgsChanged`, and `EventChannelOverflow` wakes before account filtering because overflow reports `contextId=0`. The provider queue is authoritative and no acknowledged-event ring is retained. Incomplete download/fetch, publish, and acknowledgement retry with capped context-aware backoff; acknowledgement is strictly ascending after successful durable admission or ordinary forwarding, so a retryable lower message blocks every later ID. Deliberately filtered, own, device, empty, and undecipherable messages advance the cursor. An incomplete original's RFC724 Message-ID is retained process-locally only to correlate replacement IDs, never for deduplication or a durable payload: an original still present must complete, while an absent original retires only after candidates through the last RFC-correlated replacement complete. An unrelated complete batch cannot retire it, and no visible correlated replacement leaves the queue conservatively blocked. Only safe filename/type/size and stable-message metadata cross channel admission; private blob paths never enter agent text or admission, and a missing media store yields only a safe filename annotation. | Delta Chat email must not expose partially downloaded MIME content, duplicate a mirror turn from stale provider notifications, retire pending work on unrelated traffic, skip a failed lower message, or acknowledge before durable event/agent ownership. |
| `FR-CHANNEL-015` | MUST | The authenticated channel catalog exposes `{name: "deltachat", display_name: "Delta Chat", config_key: "deltachat"}` and the ordinary `/channels/deltachat` editor reads its safe config through `GET /api/channels/deltachat/config`. Email is the only required/configured-readiness field; all non-secret Delta Chat settings and shared channel controls remain editable. Optional IMAP/SMTP ports use numeric controls and accept only `0` for automatic selection or an integer from `1` through `65535`. A configured legacy password is omitted from the config body and represented only by `configured_secrets: ["password"]`; a blank password edit preserves it, while a non-empty `_password` explicitly rotates it through the existing scoped `PATCH /api/config`. Save validation blocks a missing email or invalid port before persistence. Once PATCH succeeds, the submitted password draft is cleared immediately; success/restart feedback requires a successful masked reload followed by gateway-state refresh, while reload failure leaves an actionable error without retaining or rendering the submitted credential or claiming success. Sidebar discovery shows Delta Chat with an email affordance and derives enablement from `channel_list.deltachat`, with legacy `channels` as a read-only compatibility fallback. WhatsApp mode discovery likewise prefers modern `channel_list.whatsapp.settings.use_native` and retains only a flat legacy read fallback. Channels remain disabled by default, an existing password is never rendered or copied into browser state, and visual setup creates no second listener or bypass around the normal channel lifecycle. | Delta Chat must be set up without raw config editing or credential disclosure while preserving existing channel, secret-persistence, disabled-default, and restart semantics. |

## Data And State Model

Channel state includes enabled config entries, platform credentials/settings,
running flags, outbound queues, webhook registration keys, rate-limit state,
message context, media references, and gateway log/status process state.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/gateway/**
Owns: CODE pkg/bus/**
Owns: CODE pkg/channels/**
Owns: CODE pkg/gateway/**
Owns: CODE pkg/health/**
Owns: CODE web/backend/api/channels.go
Owns: CODE web/backend/api/gateway*
Owns: CODE web/backend/api/pico.go
Owns: CODE web/frontend/src/api/channels.ts
Owns: CODE web/frontend/src/api/gateway.ts
Owns: CODE web/frontend/src/api/pico.ts
Owns: CODE web/frontend/src/components/channels/**
Owns: CODE web/frontend/src/components/chat/**
Owns: CODE web/frontend/src/components/chat/chat-empty-state.tsx
Owns: CODE web/frontend/src/features/chat/**
Owns: CODE web/frontend/src/hooks/use-gateway*
Owns: CODE web/frontend/src/hooks/use-pico-chat.ts
Owns: CODE web/frontend/src/hooks/use-sidebar-channels.ts
Owns: CODE web/frontend/src/routes/channels/**
Owns: CODE web/frontend/src/routes/index.tsx
Owns: CHANNEL *
Owns: CLI cmd/picoclaw/internal/gateway/*
Owns: CONFIG.channel_list*
Owns: CONFIG.gateway*
Owns: HTTP * /api/gateway*
Owns: HTTP GET /api/channels*
Owns: HTTP GET /api/pico*
Owns: HTTP POST /api/pico*
Owns: HTTP GET /pico/*
Owns: HTTP HEAD /pico/*
Owns: TEST pkg/channels/*
Owns: TEST pkg/gateway/*
Owns: TEST pkg/bus/*
Owns: TEST pkg/health/*
Owns: TEST cmd/picoclaw/internal/gateway/*
Owns: TEST web/backend/api/gateway*
Owns: TEST web/backend/api/channels*
Owns: TEST web/backend/api/pico*
Owns: TEST web/frontend/src/components/channels/channel-config-fields.test.ts
Owns: TEST web/frontend/src/components/channels/channel-config-page.test.tsx
Owns: TEST web/frontend/src/hooks/use-sidebar-channels.test.ts
Owns: EVENT channel.*
Owns: EVENT gateway.*
Owns: EVENT bus.*

## Auxiliary Interfaces

| Type     | Surface                                                                                                                                                                     | Contract                                                                                                                                                      | Requirement IDs                                      |
| -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| Channels | Telegram, Discord, WhatsApp, Matrix, QQ, DingTalk, LINE, WeCom, Weixin, Feishu, Slack, IRC, OneBot, MQTT, MaixCam, Delta Chat, Pico                                          | Platform adapters normalize inbound messages and deliver outbound responses.                                                                                  | `FR-CHANNEL-001`, `FR-CHANNEL-002`, `FR-CHANNEL-004`, `FR-CHANNEL-015` |
| HTTP     | `/api/gateway/*`, `/api/channels/*`, `/api/pico/*`, `/pico/*`                                                                                                               | Gateway lifecycle, secret-safe channel catalog/config, Pico token/info/setup, websocket and media proxy.                                                       | `FR-CHANNEL-006`, `FR-CHANNEL-015`                   |
| Config   | `channel_list.*`, `gateway.*`                                                                                                                                               | Channel enablement, settings, trigger, placeholder, typing, gateway host/port/log/hot reload.                                                                 | `FR-CHANNEL-001`, `FR-CHANNEL-003`, `FR-CHANNEL-005` |
| Events   | `channel.*`, `gateway.*`, `bus.*`                                                                                                                                           | Lifecycle, webhook, outbound, rate limit, gateway, and bus failure telemetry.                                                                                 | `FR-CHANNEL-001`, `FR-CHANNEL-008`                   |
| Frontend | Chat, Pico, channel, message, code-block, account/model selection, context-usage UI, and `/channels/deltachat` setup under `web/frontend/src/components/chat/**`, `web/frontend/src/features/chat/**`, and related channel routes | Browser chat and channel-setup surfaces expose delivery/configuration behavior and follow shared frontend API, secret, token, responsive layout, accessibility, and dynamic-style lint rules. | `FR-CHANNEL-005`, `FR-CHANNEL-006`, `FR-CHANNEL-009`, `FR-CHANNEL-010`, `FR-CHANNEL-015` |
| Runtime  | Gateway reload service transaction and `AgentLoop.PauseRuntimeForReload` | Hold channel turns outside the provisional provider/config window and resume them only after replacement services commit or old services recover. | `FR-CHANNEL-011` |
| Go API | `Manager.RegisterHTTPRoute` | Collision-safe additive registration on the existing dynamic gateway mux with stale-release protection. | `FR-CHANNEL-006` |
| Go API | `health.Server.Protect`, `UsesBearerToken` | Reuse and verify the gateway's fail-closed PID-bearer authorization boundary for independently registered runtime subtrees without exposing the token. | `FR-CHANNEL-006` |
| Go API | `bus.InboundAdmission`, `MessageBus.RegisterInboundAdmission`, `MessageBus.PublishInboundWithPreparation` | Collision-safely register one identity-owned admission hook, admit detached channel-origin metadata synchronously before queueing, and invoke turn UX only for messages that will continue to the agent. | `FR-CHANNEL-013` |
| Channel | Delta Chat inbound listener | Provider events wake the authoritative ordered message queue; the listener retries before seen acknowledgement, exposes only safe metadata, and correlates full-download replacement IDs with process-local RFC724 identity. | `FR-CHANNEL-014` |
| Frontend / HTTP | Delta Chat channel catalog, sidebar item, and `/channels/deltachat` editor backed by `GET /api/channels/deltachat/config` and scoped `PATCH /api/config` | Discover, validate, and save the built-in email channel while exposing password presence but never its value and reporting the normal restart requirement after persistence. | `FR-CHANNEL-015` |

## Algorithms And Ordering

1. Gateway loads config and creates a shared message bus and agent loop.
2. Channel manager registers factories and initializes each enabled channel.
3. HTTP callback channels and opt-in gateway features register routes before
   gateway reports ready; additive feature routes fail on collision and own an
   identity-safe release closure. Socket/polling channels start their own
   workers.
4. Inbound messages are normalized and filtered by access/trigger rules. An
   optional admission hook runs synchronously for channel-origin traffic; only
   a forwarded message creates turn UX and enters the agent queue.
5. Outbound messages are queued per channel, rate-limited, sent, and reported through runtime events.
6. On reload, mark unready, preflight replacement state, pause and drain agent
   runtime users, stop reload-owned services, swap provider/config while
   retaining the prior generation, and start replacements. Commit or roll back
   services before resuming channel turns; remain unready if recovery fails.
7. On shutdown, stop cron/heartbeat/device/voice/event producers, cancel and
   join the AgentLoop, and drain its terminal generation boundary before
   stopping channel outbound workers or media cleanup and closing the provider.
8. For a channel replacement, synchronously remove the old lookup/HTTP/worker
   identity, mark its never-closed queues retired, drain admitted sends, join
   both workers, and stop the adapter. Keep a candidate private through
   `Start`; publish its adapter, worker, config hash, and HTTP routes together
   only after success. Shutdown joins dispatchers, retires workers before
   draining send leases so full queues wake, then joins workers and stops
   adapters. Retain failed cleanup for a serialized retry.
9. Delta Chat drains sorted IDs from `get_next_msgs` at startup and after every
   `IncomingMsg` or message-specific `MsgsChanged`; pending downloads also wake
   on generic `MsgsChanged`, and `EventChannelOverflow` wakes that queue drain,
   with overflow handled before account filtering because its `contextId` is
   zero. Event IDs are notification-only, so there is no
   listener acknowledgement ring. Process and acknowledge queue IDs strictly in
   ascending order after authorization, group policy, complete fetch, and
   successful publish; a retryable lower ID blocks all later IDs. If a full
   download replaces its original ID, use only the incomplete original's
   process-local RFC724 Message-ID to find replacement candidates. Complete the
   original when it remains visible, or process through the last correlated
   replacement before retiring an absent original; unrelated complete batches
   cannot retire it, and no visible correlation conservatively blocks the queue.
10. Channel discovery adds the built-in Delta Chat catalog entry, reads its
    secret-safe config projection, and constructs a blank password edit field
    from `configured_secrets` rather than from credential bytes. The editor
    requires email, omits a blank password so secure persistence preserves the
    current value, includes a non-empty replacement only in the authenticated
    scoped patch, clears the submitted password draft immediately after PATCH,
    and claims success only after reloading the masked config and gateway
    status. Numeric mail ports accept only zero or an integer through 65535.
    Sidebar enablement follows `channel_list` and uses the legacy `channels` key
    only while reading older launcher projections; WhatsApp mode reads nested
    modern settings before its flat legacy fallback.

## Cross-Feature Behavior

Routing and sessions consume normalized inbound context. Agent conversations
produce outbound responses. Security rules control dashboard and channel
credentials. Runtime events expose delivery status. Thread card payloads can
render inside chat messages and route users into thread search or open-thread
views without changing the channel delivery contract.

## Failure And Edge Cases

- Disabled channels do not start or register routes.
- Unknown channel config returns an explicit error through launcher API.
- Delta Chat setup cannot enable/save a missing email. A configured legacy
  password is represented only by its field name in `configured_secrets`; its
  value never appears in the GET body, form state, placeholder, sidebar, or
  toast. Leaving the replacement blank preserves it, while a failed rotation
  leaves the prior secure value and the editable page available. Port values
  outside zero or the integer range 1 through 65535 fail before PATCH.
- Loading or saving Delta Chat config and refreshing gateway state expose
  actionable error or restart-required feedback. A post-PATCH reload failure
  clears the submitted secret draft and cannot emit a success notice. Sidebar
  catalog/config failure does not fabricate an enabled channel, and visual
  configuration does not start the adapter before the normal gateway lifecycle
  applies the saved enabled state.
- Webhook registration conflicts fail gateway startup or reload.
- A closed bus rejects publish operations and emits close events.
- Platform media delivery falls back to text when supported by the channel.
- Disabled or disconnected chat composer states expose actionable text without
  relying on placeholder-only or title-only labels.
- The chat model selector exposes only account-backed models and account-router
  aliases in the account control, grouped as Accounts and Account Routers, and
  exposes fetched upstream model IDs in a separate model control. The model
  control and composer are disabled while discovery is loading; partial account
  failures warn, fatal discovery errors expose an explicit retry, and sending
  remains blocked when no discovered or configured fallback model is available.
- Chat messages, code blocks, model selectors, history buttons, and context
  controls do not create horizontal overflow on narrow mobile screens.
- Inbound channel work admitted before reload retains the same generation while
  it evaluates workflow triggers, resolves a route, owns a session placeholder,
  waits for a worker slot, and completes. Later inbound work waits at the
  runtime boundary; neither can capture the provisional provider between swap
  and service commit or rollback.
- A turn that reaches admission after the provisional swap remains blocked
  through rollback and resolves only against the restored generation.
- Runtime lifecycle events emitted by provisional services are dropped while
  the generation-owned workflow subscription is suspended; they are not
  evaluated against restored workflows.
- A blocked or failed replacement cannot receive direct, queued, streaming, or
  HTTP traffic. A cleanup failure keeps that exact adapter identity pending, so
  retry cannot construct a second same-name client or overlap provider
  sessions.
- Configured Delta Chat event-only admission returns without agent queueing or
  typing, reaction, and placeholder state. A durable insertion error is returned
  and queues nothing; unsupported and unconfigured channels retain the previous
  behavior.
- Delta Chat incomplete messages remain retryable and accepted messages are not
  marked seen before durable admission or ordinary forwarding owns them. RFC724
  Message-ID is fetched only within an incomplete-message replacement lifecycle
  and retained process-locally solely to correlate the original and candidate
  IDs; it is never a deduplication input or durable payload field. An unrelated
  complete batch cannot retire the pending original, and absence of a visible
  correlation conservatively blocks later queue work. Private blob paths never
  enter agent text, admission metadata, or durable payload; unavailable media
  falls back to a safe filename annotation only.

## Acceptance Evidence

| Requirement IDs                                                                          | Evidence                                                                                                                                                                                                                                       |
| ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-CHANNEL-001`, `FR-CHANNEL-006`, `FR-CHANNEL-008`                                     | [pkg/gateway/gateway_test.go](../../pkg/gateway/gateway_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go)                                                   |
| `FR-CHANNEL-002`, `FR-CHANNEL-003`, `FR-CHANNEL-004`, `FR-CHANNEL-005`, `FR-CHANNEL-007` | [pkg/channels](../../pkg/channels), [pkg/channels/telegram/telegram_dispatch_test.go](../../pkg/channels/telegram/telegram_dispatch_test.go), [pkg/channels/tool_feedback_animator_test.go](../../pkg/channels/tool_feedback_animator_test.go) |
| `FR-CHANNEL-006`                                                                         | [pkg/channels/dynamic_mux_test.go](../../pkg/channels/dynamic_mux_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go), [pkg/health/server_test.go](../../pkg/health/server_test.go), [pkg/gateway/workflow_authoring_test.go](../../pkg/gateway/workflow_authoring_test.go), [pkg/gateway/agent_activity_test.go](../../pkg/gateway/agent_activity_test.go), [pkg/gateway/listen_test.go](../../pkg/gateway/listen_test.go), [web/backend/api/pico_test.go](../../web/backend/api/pico_test.go), [web/backend/api/channels_test.go](../../web/backend/api/channels_test.go) |
| `FR-CHANNEL-009`                                                                         | [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/scripts/lint-ui-rules.mjs](../../web/frontend/scripts/lint-ui-rules.mjs)                                                                       |
| `FR-CHANNEL-010`                                                                         | [pkg/channels/pico/pico_test.go](../../pkg/channels/pico/pico_test.go), [web/frontend/src/hooks/use-chat-models.test.ts](../../web/frontend/src/hooks/use-chat-models.test.ts)                                                                 |
| `FR-CHANNEL-011`                                                                         | [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go), [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/health/server_test.go](../../pkg/health/server_test.go)                         |
| `FR-CHANNEL-012`                                                                         | [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go)                                                                                                                                                                               |
| `FR-CHANNEL-013`                                                                         | [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go), [pkg/channels/base_test.go](../../pkg/channels/base_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go), [pkg/agent/steering_test.go](../../pkg/agent/steering_test.go), [pkg/agent/agent_turn_ux_test.go](../../pkg/agent/agent_turn_ux_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-CHANNEL-014`                                                                         | [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go)                                                                                                                                    |
| `FR-CHANNEL-015`                                                                         | [web/backend/api/channels_test.go](../../web/backend/api/channels_test.go), [web/frontend/src/components/channels/channel-config-fields.test.ts](../../web/frontend/src/components/channels/channel-config-fields.test.ts), [web/frontend/src/components/channels/channel-config-page.test.tsx](../../web/frontend/src/components/channels/channel-config-page.test.tsx), [web/frontend/src/hooks/use-sidebar-channels.test.ts](../../web/frontend/src/hooks/use-sidebar-channels.test.ts)                                                                                                                                    |

## Implementation Anchors

- [pkg/gateway/gateway.go](../../pkg/gateway/gateway.go)
- [pkg/bus/bus.go](../../pkg/bus/bus.go)
- [pkg/channels/manager.go](../../pkg/channels/manager.go)
- [pkg/channels/deltachat/handler.go](../../pkg/channels/deltachat/handler.go)
- [web/backend/api/channels.go](../../web/backend/api/channels.go)
- [web/frontend/src/components/channels/channel-config-page.tsx](../../web/frontend/src/components/channels/channel-config-page.tsx)
- [web/frontend/src/hooks/use-sidebar-channels.ts](../../web/frontend/src/hooks/use-sidebar-channels.ts)
- [web/backend/api/pico.go](../../web/backend/api/pico.go)
