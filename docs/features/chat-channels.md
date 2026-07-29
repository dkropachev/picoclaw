# Chat Channels And Gateway Delivery

## Feature ID

`FR-CHANNEL`

## Behavior Summary

PicoClaw exposes the agent through chat channels and the gateway. Channels
normalize inbound messages, enforce allow/trigger rules, forward work to the
agent bus, and deliver outbound text/media responses through platform-specific
transports.

## Reconstruction Notes

- Similarity target: recreate channel adapters with a common base, manager startup, webhook/socket registration, inbound normalization, outbound workers, and gateway lifecycle.
- Core types/functions: channel factory registry, `BaseChannel`, `ChannelManager`, message bus, gateway bootstrap/reload/shutdown, Pico websocket/media handlers.
- Runtime ordering: load channel config, instantiate enabled adapters, register webhooks, start workers, publish inbound context, queue outbound response, send platform message, emit events.
- Non-obvious constraints: platform-specific allow lists, group trigger logic, placeholder/typing UX, reply IDs, media references, rate limiting, and closed-bus behavior.

## Requirements

| ID               | Level  | Requirement                                                                                                                                                               | Rationale                                                                          |
| ---------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `FR-CHANNEL-001` | MUST   | Enabled channels start from `channel_list`, register any required webhook or socket transport, and report lifecycle events.                                               | Gateway startup must reflect configured delivery paths.                            |
| `FR-CHANNEL-002` | MUST   | Inbound channel messages normalize channel, account, space, chat, topic, sender, message ID, mention state, text, and media before entering the bus.                      | Routing and session allocation need common context.                                |
| `FR-CHANNEL-003` | MUST   | Allow lists and group triggers can reject messages before agent execution.                                                                                                | Users need channel-level access and noise control.                                 |
| `FR-CHANNEL-004` | MUST   | Outbound messages preserve reply context and media references where the platform supports them.                                                                           | Replies must land in the expected chat/thread.                                     |
| `FR-CHANNEL-005` | SHOULD | Channels with placeholders or typing indicators emit intermediate UX feedback without changing final response content.                                                    | Long-running turns need visible progress.                                          |
| `FR-CHANNEL-006` | MUST   | Gateway HTTP and websocket routes expose only configured channel, Pico, health, and explicitly registered feature behavior. Additive shared-mux registration detects collisions, returns an identity-owned release function, and never holds the route-map lock while a downstream handler runs. | Launcher, native Pico clients, and opt-in gateway features share one listener without silently replacing or blocking one another. |
| `FR-CHANNEL-007` | MUST   | Channel-specific command UX forwards generic commands to the central command executor except documented platform-local discovery behavior.                                | Slash command behavior must stay consistent across channels.                       |
| `FR-CHANNEL-008` | MUST   | Send failures, rate limits, and closed buses produce structured errors/events instead of silently dropping messages.                                                      | Operators need diagnoseable delivery failure.                                      |
| `FR-CHANNEL-009` | SHOULD | Browser chat UI preserves readable message layout, accessible composer labeling, responsive controls, and non-overlapping code/message surfaces in light and dark themes. | Chat delivery remains user-facing even when the gateway is stopped or unavailable. |
| `FR-CHANNEL-010` | MUST | Pico browser chat sends the selected account alias and selected upstream model ID on user messages when those controls are set, and the channel copies them into normalized inbound metadata for the agent turn. | Chat-window account/model selection must affect the next turn without relying on a hidden config rewrite. |
| `FR-CHANNEL-011` | MUST | Gateway config reload marks readiness false, preflights replacement storage and event runtime dependencies, pauses new agent runtime admission, drains active turns and reload-owned services, retains the prior provider/config through replacement startup, and resumes turns only after commit or successful rollback. An inbound message holds one generation across workflow-trigger evaluation, route/session reservation, worker-queue wait, and turn completion. Replacement event/heartbeat workers and scheduled jobs are fenced to their exact config generation, the runtime-event workflow subscription is absent throughout the outer transaction, and cron starts only after other fallible replacement initialization. Terminal shutdown remembers Stop before a delayed AgentLoop start, quiesces producers, drains the permanent runtime boundary, then stops outbound channel/media dependencies and closes providers. Cleanup, rollback, or shutdown-drain failure leaves readiness/resources fail-safe and never closes a provider or dependency still reachable by active work. | Channels remain live during reload, so readiness alone cannot prevent a turn, queued lifecycle event, or background service from observing provisional or closed runtime state. |
| `FR-CHANNEL-012` | MUST | Channel reload detaches and retires an old adapter identity before replacement construction, drains its admitted sends, joins its workers, and stops it before starting a same-name candidate. The candidate remains absent from lookup, synchronous send, outbound dispatch, and HTTP routing until `Start` succeeds. Failed or timed-out retirement remains manager-owned and blocks duplicate construction until cleanup succeeds. Shutdown joins dispatchers, retires/cancels workers to release full-queue senders, drains admitted direct sends, joins workers, then stops each adapter; errors are returned and successful stops are not repeated on retry. | A replacement must never overlap the old adapter or receive traffic before it is ready, and shutdown must neither panic on raced queue access nor deadlock behind a full retired queue. |

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
Owns: EVENT channel.*
Owns: EVENT gateway.*
Owns: EVENT bus.*

## Auxiliary Interfaces

| Type     | Surface                                                                                                                                                                     | Contract                                                                                                                                                      | Requirement IDs                                      |
| -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| Channels | Telegram, Discord, WhatsApp, Matrix, QQ, DingTalk, LINE, WeCom, Weixin, Feishu, Slack, IRC, OneBot, MQTT, MaixCam, Pico                                                     | Platform adapters normalize inbound messages and deliver outbound responses.                                                                                  | `FR-CHANNEL-001`, `FR-CHANNEL-002`, `FR-CHANNEL-004` |
| HTTP     | `/api/gateway/*`, `/api/channels/*`, `/api/pico/*`, `/pico/*`                                                                                                               | Gateway lifecycle, channel catalog/config, Pico token/info/setup, websocket and media proxy.                                                                  | `FR-CHANNEL-006`                                     |
| Config   | `channel_list.*`, `gateway.*`                                                                                                                                               | Channel enablement, settings, trigger, placeholder, typing, gateway host/port/log/hot reload.                                                                 | `FR-CHANNEL-001`, `FR-CHANNEL-003`, `FR-CHANNEL-005` |
| Events   | `channel.*`, `gateway.*`, `bus.*`                                                                                                                                           | Lifecycle, webhook, outbound, rate limit, gateway, and bus failure telemetry.                                                                                 | `FR-CHANNEL-001`, `FR-CHANNEL-008`                   |
| Frontend | Chat, Pico, channel, message, code-block, account/model selection, and context-usage UI under `web/frontend/src/components/chat/**`, `web/frontend/src/features/chat/**`, and related channel routes | Browser chat surfaces expose channel delivery behavior and follow shared frontend API, token, responsive layout, accessibility, and dynamic-style lint rules. | `FR-CHANNEL-005`, `FR-CHANNEL-006`, `FR-CHANNEL-009`, `FR-CHANNEL-010` |
| Runtime  | Gateway reload service transaction and `AgentLoop.PauseRuntimeForReload` | Hold channel turns outside the provisional provider/config window and resume them only after replacement services commit or old services recover. | `FR-CHANNEL-011` |
| Go API | `Manager.RegisterHTTPRoute` | Collision-safe additive registration on the existing dynamic gateway mux with stale-release protection. | `FR-CHANNEL-006` |

## Algorithms And Ordering

1. Gateway loads config and creates a shared message bus and agent loop.
2. Channel manager registers factories and initializes each enabled channel.
3. HTTP callback channels and opt-in gateway features register routes before
   gateway reports ready; additive feature routes fail on collision and own an
   identity-safe release closure. Socket/polling channels start their own
   workers.
4. Inbound messages are normalized, filtered by access/trigger rules, and published to the bus.
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

## Cross-Feature Behavior

Routing and sessions consume normalized inbound context. Agent conversations
produce outbound responses. Security rules control dashboard and channel
credentials. Runtime events expose delivery status. Thread card payloads can
render inside chat messages and route users into thread search or open-thread
views without changing the channel delivery contract.

## Failure And Edge Cases

- Disabled channels do not start or register routes.
- Unknown channel config returns an explicit error through launcher API.
- Webhook registration conflicts fail gateway startup or reload.
- A closed bus rejects publish operations and emits close events.
- Platform media delivery falls back to text when supported by the channel.
- Disabled or disconnected chat composer states expose actionable text without
  relying on placeholder-only or title-only labels.
- The chat model selector exposes only account-backed models and account-router
  aliases in the account control, grouped as Accounts and Account Routers, and
  exposes fetched upstream model IDs in a separate model control.
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

## Acceptance Evidence

| Requirement IDs                                                                          | Evidence                                                                                                                                                                                                                                       |
| ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-CHANNEL-001`, `FR-CHANNEL-006`, `FR-CHANNEL-008`                                     | [pkg/gateway/gateway_test.go](../../pkg/gateway/gateway_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go)                                                   |
| `FR-CHANNEL-002`, `FR-CHANNEL-003`, `FR-CHANNEL-004`, `FR-CHANNEL-005`, `FR-CHANNEL-007` | [pkg/channels](../../pkg/channels), [pkg/channels/telegram/telegram_dispatch_test.go](../../pkg/channels/telegram/telegram_dispatch_test.go), [pkg/channels/tool_feedback_animator_test.go](../../pkg/channels/tool_feedback_animator_test.go) |
| `FR-CHANNEL-006`                                                                         | [pkg/channels/dynamic_mux_test.go](../../pkg/channels/dynamic_mux_test.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go), [web/backend/api/pico_test.go](../../web/backend/api/pico_test.go), [web/backend/api/channels_test.go](../../web/backend/api/channels_test.go) |
| `FR-CHANNEL-009`                                                                         | [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/scripts/lint-ui-rules.mjs](../../web/frontend/scripts/lint-ui-rules.mjs)                                                                       |
| `FR-CHANNEL-010`                                                                         | [pkg/channels/pico/pico_test.go](../../pkg/channels/pico/pico_test.go), [web/frontend/src/hooks/use-chat-models.test.ts](../../web/frontend/src/hooks/use-chat-models.test.ts)                                                                 |
| `FR-CHANNEL-011`                                                                         | [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go), [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/health/server_test.go](../../pkg/health/server_test.go)                         |
| `FR-CHANNEL-012`                                                                         | [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go)                                                                                                                                                                               |

## Implementation Anchors

- [pkg/gateway/gateway.go](../../pkg/gateway/gateway.go)
- [pkg/channels/manager.go](../../pkg/channels/manager.go)
- [web/backend/api/pico.go](../../web/backend/api/pico.go)
