# Scheduling And Reminders

## Feature ID

`FR-SCHED`

## Behavior Summary

PicoClaw schedules reminders and recurring work through cron commands and the
agent-callable cron tool. Jobs persist in the workspace and can deliver prompts
through channels or run gated shell commands.

## Reconstruction Notes

- Similarity target: recreate cron tool actions, persistent job storage, CLI job management, execution delivery modes, command gates, and heartbeat scheduling.
- Core types/functions: cron tool, job parser/store, Cobra cron subcommands, heartbeat service, gateway delivery handler, and exec gate checks.
- Runtime ordering: parse schedule, validate command/delivery gates, persist job, list/enable/disable/remove, run due jobs, route delivered prompts or execute command jobs.
- Non-obvious constraints: command jobs require both cron and exec permissions, disabled jobs stay stored, heartbeat uses ordinary agent execution, and a concrete agent loop publishes a scheduled direct response before releasing tracked-result output ownership.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SCHED-001` | MUST | Cron jobs support one-shot times, durations, and cron expressions as documented schedule types. | Users need flexible reminders. |
| `FR-SCHED-002` | MUST | Jobs persist as typed, ordered, versioned rows in the private WAL-backed `<workspace>/cron/jobs.db` and survive process restart. Every mutation loads and commits one authoritative snapshot inside `BEGIN IMMEDIATE`, so concurrent gateway and CLI writers preserve unrelated jobs. On first open, bounded valid `jobs.json` records import deterministically, selected invalid/duplicate jobs leave payload-free issue codes and digests, and the exact source is retained without overwrite under `cron/legacy-json/cron-jobs-v1/`; SQLite is immediately authoritative and no JSON dual write exists. Agent filesystem mutation tools protect the database, WAL/SHM companions, active legacy source, and retained archive namespace for both root and owner-local registries. | Schedules are durable user state shared by concurrent operators and gateway execution. |
| `FR-SCHED-003` | MUST | `deliver: true` jobs route results to the configured channel/chat, while non-delivery jobs only update runtime state/logs. | Scheduling must distinguish notification from background work. |
| `FR-SCHED-004` | MUST | Command jobs require cron command enablement and exec remote permission gates before shell execution. | Scheduled shell execution is high risk. |
| `FR-SCHED-005` | MUST | CLI cron add/list/enable/disable/remove reflects persisted job state. | Operators need direct schedule management. |
| `FR-SCHED-006` | SHOULD | Heartbeat prompts run on configured interval and share the normal agent execution path. | Periodic assistant behavior should stay consistent. |
| `FR-SCHED-007` | MUST | Gateway-owned cron commands, cron agent turns, heartbeat prompts, and workflow schedules acquire the exact `(config, registry, execution policy, diagnostic policy)` generation that created them before effects. A fresh or detached generation-fenced reacquisition without an owner-issued diagnostic origin is safe-only; retained synchronous work keeps its revocable origin, and a stale origin can only meet the current generation cap. First-party scheduled roots install zero, so preview propagation remains dormant. A cron service obtains that admission before clearing and persisting a due occurrence and holds it through callback bookkeeping; reload starts replacement cron only after other fallible initialization. `Stop` and `Close` serialize against `Start`, cancel and synchronously join the exact scheduler loop before returning or closing SQLite. When its executor supports the concrete direct-publishing boundary, an agent cron turn publishes or suppresses the root response inside that boundary before tracked child-result pumping may resume; compatibility executors retain the separate response callback. Already-due durable occurrences remain due across restart, while stale candidate or cached schedule work is rejected without executing commands, creating workflow runs, or publishing trigger telemetry. | Background schedules must not escape the reload transaction, lose a one-shot occurrence, reorder a tracked child result ahead of its root response, execute against another workspace, leave database work alive after shutdown, or observe provisional provider/config state. |

## Data And State Model

Schedule state includes persisted cron job records, enabled flags, schedule
expressions/times, delivery target metadata, command payloads, execution timeout,
and heartbeat interval/prompt state.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/cron/**
Owns: CODE pkg/cron/**
Owns: CODE pkg/heartbeat/**
Owns: CODE pkg/tools/cron.go
Owns: CODE pkg/agent/workflow_automations.go
Owns: CLI cmd/picoclaw/internal/cron/*
Owns: CONFIG.tools.cron*
Owns: CONFIG.heartbeat*
Owns: TEST cmd/picoclaw/internal/cron/*
Owns: TEST pkg/cron/*
Owns: TEST pkg/heartbeat/*
Owns: TOOL cron

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw cron add/list/enable/disable/remove` | Persistent job management. | `FR-SCHED-005` |
| Tool | `cron` | Agent-callable scheduling actions. | `FR-SCHED-001` through `FR-SCHED-004` |
| Config | `tools.cron.*`, `heartbeat.*` | Command gates, timeout, allowed remotes, and heartbeat interval. | `FR-SCHED-004`, `FR-SCHED-006` |
| Storage | `<workspace>/cron/jobs.db`; `cron/jobs.json`; `cron/legacy-json/cron-jobs-v1/jobs.json` | Typed job definitions, ordering and execution state plus the protected active and immutable verified legacy sources. | `FR-SCHED-002`, `FR-SCHED-005` |
| Runtime | `AgentLoop.AcquireRuntimeGeneration`, gateway reload lifecycle | Fence due work to its originating config/provider generation and activate replacement cron only after fallible service setup. | `FR-SCHED-007` |
| Runtime | optional direct-publishing job executor | Keep a scheduled root response inside the agent turn's tracked-result output boundary, with compatibility fallback for alternate executors. | `FR-SCHED-007` |

## Algorithms And Ordering

1. Parse requested schedule and reject invalid time/cron forms.
2. Validate command gates and delivery target before persistence.
3. Persist job state and expose CLI list/status operations from the same store.
4. On due execution, either enqueue an agent prompt for delivery or run the gated command.
5. Heartbeat periodically submits configured prompts through the same agent path.
6. A gateway cron service acquires the originating config generation before it
   clears a due `nextRunAtMs`, persists that claim, invokes the callback, and
   saves terminal/next-run state. If service cancellation wins admission, the
   durable occurrence remains untouched. Startup preserves already-due
   occurrences, including overdue one-shots, for immediate execution.
7. Workflow schedule refresh detects config pointer changes immediately,
   discards the old cache, and admits asynchronous runs before goroutine
   launch. Reload initializes replacement services with cron stopped, starts
   cron last, and resumes the generation gate only after commit or rollback.
8. A concrete AgentLoop executor publishes the scheduled agent response inside
   its direct output-owner boundary; alternate JobExecutor implementations use
   the historical separate `PublishResponseIfNeeded` callback.

## Cross-Feature Behavior

Scheduled delivery uses chat channels and gateway delivery. Command jobs use
tool execution and security gates. Agent conversations process scheduled prompts.

## Failure And Edge Cases

- Invalid schedules are rejected before persistence.
- Disabled jobs remain stored but do not execute.
- Command jobs fail closed when exec or cron command gates are disabled.
- Missing target channel/chat prevents delivery and reports failure.
- A due candidate command waiting during rollback is rejected when the old
  generation resumes. A cached schedule from another generation cannot run a
  same-named workflow in the replacement workspace and emits no triggered
  event.
- Stopping an old cron service before runtime admission neither clears nor
  deletes its due job. The replacement/restarted service preserves the overdue
  timestamp and executes it once admitted.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SCHED-001`, `FR-SCHED-002`, `FR-SCHED-003`, `FR-SCHED-004` | [pkg/cron/sqlite_test.go](../../pkg/cron/sqlite_test.go), [pkg/tools/cron_test.go](../../pkg/tools/cron_test.go), [docs/reference/cron.md](../reference/cron.md) |
| `FR-SCHED-005` | [cmd/picoclaw/internal/cron/add_test.go](../../cmd/picoclaw/internal/cron/add_test.go), [cmd/picoclaw/internal/cron/list_test.go](../../cmd/picoclaw/internal/cron/list_test.go), [cmd/picoclaw/internal/cron/enable_test.go](../../cmd/picoclaw/internal/cron/enable_test.go), [cmd/picoclaw/internal/cron/disable_test.go](../../cmd/picoclaw/internal/cron/disable_test.go), [cmd/picoclaw/internal/cron/remove_test.go](../../cmd/picoclaw/internal/cron/remove_test.go) |
| `FR-SCHED-006` | [pkg/heartbeat/service_test.go](../../pkg/heartbeat/service_test.go) |
| `FR-SCHED-007` | [pkg/tools/cron_test.go](../../pkg/tools/cron_test.go), [pkg/agent/workflow_automations_test.go](../../pkg/agent/workflow_automations_test.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go), [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/agent/runtime_policy_late_work_test.go](../../pkg/agent/runtime_policy_late_work_test.go) |

## Implementation Anchors

- [pkg/tools/cron.go](../../pkg/tools/cron.go)
- [pkg/cron/sqlite.go](../../pkg/cron/sqlite.go)
- [pkg/agent/workflow_automations.go](../../pkg/agent/workflow_automations.go)
- [pkg/gateway/gateway.go](../../pkg/gateway/gateway.go)
- [pkg/heartbeat/service.go](../../pkg/heartbeat/service.go)
- [cmd/picoclaw/internal/cron](../../cmd/picoclaw/internal/cron)
