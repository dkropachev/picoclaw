# Scheduling And Reminders

## Feature ID

`FR-SCHED`

## Behavior Summary

PicoClaw schedules reminders and recurring work through cron commands and the
agent-callable cron tool. Jobs persist in the workspace and can deliver prompts
through channels or run gated shell commands.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SCHED-001` | MUST | Cron jobs support one-shot times, durations, and cron expressions as documented schedule types. | Users need flexible reminders. |
| `FR-SCHED-002` | MUST | Jobs persist under the workspace and survive process restart. | Schedules are durable user state. |
| `FR-SCHED-003` | MUST | `deliver: true` jobs route results to the configured channel/chat, while non-delivery jobs only update runtime state/logs. | Scheduling must distinguish notification from background work. |
| `FR-SCHED-004` | MUST | Command jobs require cron command enablement and exec remote permission gates before shell execution. | Scheduled shell execution is high risk. |
| `FR-SCHED-005` | MUST | CLI cron add/list/enable/disable/remove reflects persisted job state. | Operators need direct schedule management. |
| `FR-SCHED-006` | SHOULD | Heartbeat prompts run on configured interval and share the normal agent execution path. | Periodic assistant behavior should stay consistent. |

## Auxiliary Interfaces

Owns: CLI cmd/picoclaw/internal/cron/*
Owns: CONFIG.tools.cron*
Owns: CONFIG.heartbeat*
Owns: TEST cmd/picoclaw/internal/cron/*
Owns: TEST pkg/cron/*
Owns: TEST pkg/heartbeat/*
Owns: TOOL cron

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw cron add/list/enable/disable/remove` | Persistent job management. | `FR-SCHED-005` |
| Tool | `cron` | Agent-callable scheduling actions. | `FR-SCHED-001` through `FR-SCHED-004` |
| Config | `tools.cron.*`, `heartbeat.*` | Command gates, timeout, allowed remotes, and heartbeat interval. | `FR-SCHED-004`, `FR-SCHED-006` |

## Cross-Feature Behavior

Scheduled delivery uses chat channels and gateway delivery. Command jobs use
tool execution and security gates. Agent conversations process scheduled prompts.

## Failure And Edge Cases

- Invalid schedules are rejected before persistence.
- Disabled jobs remain stored but do not execute.
- Command jobs fail closed when exec or cron command gates are disabled.
- Missing target channel/chat prevents delivery and reports failure.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SCHED-001`, `FR-SCHED-002`, `FR-SCHED-003`, `FR-SCHED-004` | [pkg/tools/cron_test.go](../../pkg/tools/cron_test.go), [docs/reference/cron.md](../reference/cron.md) |
| `FR-SCHED-005` | [cmd/picoclaw/internal/cron](../../cmd/picoclaw/internal/cron) |
| `FR-SCHED-006` | [pkg/heartbeat/service_test.go](../../pkg/heartbeat/service_test.go) |

## Implementation Anchors

- [pkg/tools/cron.go](../../pkg/tools/cron.go)
- [pkg/heartbeat/service.go](../../pkg/heartbeat/service.go)
- [cmd/picoclaw/internal/cron](../../cmd/picoclaw/internal/cron)
