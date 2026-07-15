# Runtime Events And Observability

## Feature ID

`FR-EVENTS`

## Behavior Summary

Runtime events provide observable envelopes for agent, channel, gateway, bus,
and MCP behavior. Event logging filters decide which published events are printed
without changing event publication.

## Reconstruction Notes

- Similarity target: recreate the event bus, stable event kind constants,
  subscriber filtering, logging filters, and payload redaction conventions.
- Core types/functions: Event, Bus, subscription filters, event kind registry,
  logging config, runtime event logger, and safe payload builders.
- Runtime ordering: construct event envelope, publish to bus, match subscribers,
  enqueue or close channels according to bus state, then independently apply
  logging filters for printed diagnostics.
- Non-obvious constraints: logging configuration never suppresses publication,
  closed buses close subscriber channels, and payloads prefer counts/lengths over
  full sensitive args.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-EVENTS-001` | MUST | Published events carry kind, timestamp, severity, scope, and payload fields where available. | Consumers need stable event envelopes. |
| `FR-EVENTS-002` | MUST | Subscribers can filter by kind prefix and receive buffered event channels with close semantics. | Hooks and diagnostics need reliable subscriptions. |
| `FR-EVENTS-003` | MUST | Logging include/exclude/min-severity filters affect printed logs only, not event bus publication. | Observability should not mutate runtime behavior. |
| `FR-EVENTS-004` | MUST | Known event names cover agent, channel, bus, gateway, and MCP domains. | Feature telemetry must be discoverable. |
| `FR-EVENTS-005` | SHOULD | Event payloads avoid full sensitive args by default and include lengths/counts when safer. | Logs should be useful without leaking secrets. |

## Data And State Model

Event state includes immutable event envelopes with kind, timestamp, severity,
scope, and payload fields; subscriber registrations with prefix filters and
buffered channels; bus closed state; event logging include/exclude/min-severity
config; and known kind constants for each runtime domain.

## Surface Ownership

Owns: CODE pkg/events/**
Owns: CODE pkg/agent/event*
Owns: CODE pkg/agent/events*
Owns: CODE pkg/agent/runtime_event*
Owns: CONFIG.events*
Owns: TEST pkg/events/*
Owns: TEST pkg/config/events*
Owns: EVENT *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `events.logging.*` | Event logging enablement, filters, payload inclusion, and severity threshold. | `FR-EVENTS-003`, `FR-EVENTS-005` |
| Events | `agent.*`, `channel.*`, `bus.*`, `gateway.*`, `mcp.*` | Published runtime event kinds. | `FR-EVENTS-001`, `FR-EVENTS-004` |

## Algorithms And Ordering

1. Create events with a stable kind, current timestamp, severity, optional
   scope, and a payload already reduced for sensitive fields when necessary.
2. When subscribing, register a prefix filter and buffered channel under bus
   synchronization; immediately fail or close if the bus is already closed.
3. On publish, copy current subscribers, match each subscriber by kind prefix,
   and deliver through bounded channel behavior without letting slow consumers
   mutate the event.
4. On bus close, mark closed state once and close all subscriber channels.
5. For runtime logging, evaluate include, exclude, and severity filters against
   published events and print only matching entries without affecting delivery.

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
