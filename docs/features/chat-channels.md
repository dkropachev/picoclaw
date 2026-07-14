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

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-CHANNEL-001` | MUST | Enabled channels start from `channel_list`, register any required webhook or socket transport, and report lifecycle events. | Gateway startup must reflect configured delivery paths. |
| `FR-CHANNEL-002` | MUST | Inbound channel messages normalize channel, account, space, chat, topic, sender, message ID, mention state, text, and media before entering the bus. | Routing and session allocation need common context. |
| `FR-CHANNEL-003` | MUST | Allow lists and group triggers can reject messages before agent execution. | Users need channel-level access and noise control. |
| `FR-CHANNEL-004` | MUST | Outbound messages preserve reply context and media references where the platform supports them. | Replies must land in the expected chat/thread. |
| `FR-CHANNEL-005` | SHOULD | Channels with placeholders or typing indicators emit intermediate UX feedback without changing final response content. | Long-running turns need visible progress. |
| `FR-CHANNEL-006` | MUST | Gateway HTTP and websocket routes expose only the configured channel management and Pico protocol behavior. | Launcher and native Pico clients use the shared gateway surface. |
| `FR-CHANNEL-007` | MUST | Channel-specific command UX forwards generic commands to the central command executor except documented platform-local discovery behavior. | Slash command behavior must stay consistent across channels. |
| `FR-CHANNEL-008` | MUST | Send failures, rate limits, and closed buses produce structured errors/events instead of silently dropping messages. | Operators need diagnoseable delivery failure. |

## Data And State Model

Channel state includes enabled config entries, platform credentials/settings,
running flags, outbound queues, webhook registration keys, rate-limit state,
message context, media references, and gateway log/status process state.

## Surface Ownership

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

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Channels | Telegram, Discord, WhatsApp, Matrix, QQ, DingTalk, LINE, WeCom, Weixin, Feishu, Slack, IRC, OneBot, MQTT, MaixCam, Pico | Platform adapters normalize inbound messages and deliver outbound responses. | `FR-CHANNEL-001`, `FR-CHANNEL-002`, `FR-CHANNEL-004` |
| HTTP | `/api/gateway/*`, `/api/channels/*`, `/api/pico/*`, `/pico/*` | Gateway lifecycle, channel catalog/config, Pico token/info/setup, websocket and media proxy. | `FR-CHANNEL-006` |
| Config | `channel_list.*`, `gateway.*` | Channel enablement, settings, trigger, placeholder, typing, gateway host/port/log/hot reload. | `FR-CHANNEL-001`, `FR-CHANNEL-003`, `FR-CHANNEL-005` |
| Events | `channel.*`, `gateway.*`, `bus.*` | Lifecycle, webhook, outbound, rate limit, gateway, and bus failure telemetry. | `FR-CHANNEL-001`, `FR-CHANNEL-008` |

## Algorithms And Ordering

1. Gateway loads config and creates a shared message bus and agent loop.
2. Channel manager registers factories and initializes each enabled channel.
3. HTTP callback channels register routes before gateway reports ready; socket/polling channels start their own workers.
4. Inbound messages are normalized, filtered by access/trigger rules, and published to the bus.
5. Outbound messages are queued per channel, rate-limited, sent, and reported through runtime events.

## Cross-Feature Behavior

Routing and sessions consume normalized inbound context. Agent conversations
produce outbound responses. Security rules control dashboard and channel
credentials. Runtime events expose delivery status.

## Failure And Edge Cases

- Disabled channels do not start or register routes.
- Unknown channel config returns an explicit error through launcher API.
- Webhook registration conflicts fail gateway startup or reload.
- A closed bus rejects publish operations and emits close events.
- Platform media delivery falls back to text when supported by the channel.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-CHANNEL-001`, `FR-CHANNEL-006`, `FR-CHANNEL-008` | [pkg/gateway/gateway_test.go](../../pkg/gateway/gateway_test.go), [web/backend/api/gateway_test.go](../../web/backend/api/gateway_test.go), [pkg/bus/bus_test.go](../../pkg/bus/bus_test.go) |
| `FR-CHANNEL-002`, `FR-CHANNEL-003`, `FR-CHANNEL-004`, `FR-CHANNEL-005`, `FR-CHANNEL-007` | [pkg/channels](../../pkg/channels), [pkg/channels/telegram/telegram_dispatch_test.go](../../pkg/channels/telegram/telegram_dispatch_test.go), [pkg/channels/tool_feedback_animator_test.go](../../pkg/channels/tool_feedback_animator_test.go) |
| `FR-CHANNEL-006` | [web/backend/api/pico_test.go](../../web/backend/api/pico_test.go), [web/backend/api/channels_test.go](../../web/backend/api/channels_test.go) |

## Implementation Anchors

- [pkg/gateway/gateway.go](../../pkg/gateway/gateway.go)
- [pkg/channels/manager.go](../../pkg/channels/manager.go)
- [web/backend/api/pico.go](../../web/backend/api/pico.go)
