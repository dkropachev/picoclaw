# Runtime Events And Observability

## Feature ID

`FR-EVENTS`

## Behavior Summary

Runtime events provide observable envelopes for agent, channel, gateway, bus,
and MCP behavior. Event logging filters decide which published events are printed
without changing event publication.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-EVENTS-001` | MUST | Published events carry kind, timestamp, severity, scope, and payload fields where available. | Consumers need stable event envelopes. |
| `FR-EVENTS-002` | MUST | Subscribers can filter by kind prefix and receive buffered event channels with close semantics. | Hooks and diagnostics need reliable subscriptions. |
| `FR-EVENTS-003` | MUST | Logging include/exclude/min-severity filters affect printed logs only, not event bus publication. | Observability should not mutate runtime behavior. |
| `FR-EVENTS-004` | MUST | Known event names cover agent, channel, bus, gateway, and MCP domains. | Feature telemetry must be discoverable. |
| `FR-EVENTS-005` | SHOULD | Event payloads avoid full sensitive args by default and include lengths/counts when safer. | Logs should be useful without leaking secrets. |

## Auxiliary Interfaces

Owns: CONFIG.events*
Owns: TEST pkg/events/*
Owns: TEST pkg/config/events*
Owns: EVENT *

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `events.logging.*` | Event logging enablement, filters, payload inclusion, and severity threshold. | `FR-EVENTS-003`, `FR-EVENTS-005` |
| Events | `agent.*`, `channel.*`, `bus.*`, `gateway.*`, `mcp.*` | Published runtime event kinds. | `FR-EVENTS-001`, `FR-EVENTS-004` |

## Cross-Feature Behavior

All runtime features can publish events. Hooks may observe events. Gateway logs
render filtered events for operators.

## Failure And Edge Cases

- Closed buses stop delivery and close subscriber channels.
- Slow subscribers are bounded by buffer behavior.
- Invalid filter patterns match no events rather than all events.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-EVENTS-001`, `FR-EVENTS-002`, `FR-EVENTS-004` | [pkg/events/events_test.go](../../pkg/events/events_test.go), [pkg/events/subscription_test.go](../../pkg/events/subscription_test.go), [pkg/events/kind.go](../../pkg/events/kind.go) |
| `FR-EVENTS-003`, `FR-EVENTS-005` | [pkg/events/filter_test.go](../../pkg/events/filter_test.go), [pkg/config/events_test.go](../../pkg/config/events_test.go), [docs/architecture/runtime-events.md](../architecture/runtime-events.md) |

## Implementation Anchors

- [pkg/events](../../pkg/events)
- [pkg/agent/runtime_event_logger.go](../../pkg/agent/runtime_event_logger.go)
- [docs/architecture/runtime-events.md](../architecture/runtime-events.md)
