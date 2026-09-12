# Feature Requirements

This directory is the canonical source of truth for PicoClaw product behavior.
Feature requirements describe capabilities. Config, HTTP APIs, CLI commands,
provider adapters, and tests are auxiliary surfaces that prove or expose those
capabilities.

Future behavior changes must update the relevant feature requirement before or
with the code change. The `make lint-features` gate verifies that feature specs
own discovered repository surfaces and that each requirement has acceptance
evidence.

The canonical spec format is the Reconstruction Contract Matrix, defined in
[Feature Requirements Format](../design/feature-requirements-format.md).

## Canonical Specs

| Feature                                | Spec                                                               |
| -------------------------------------- | ------------------------------------------------------------------ |
| Agent conversations and turn execution | [agent-conversations.md](agent-conversations.md)                   |
| Chat channels and gateway delivery     | [chat-channels.md](chat-channels.md)                               |
| Session memory and history             | [session-memory.md](session-memory.md)                             |
| Threads and handoffs                   | [threads.md](threads.md)                                           |
| Tool execution                         | [tool-execution.md](tool-execution.md)                             |
| MCP integration and discovery          | [mcp-integration.md](mcp-integration.md)                           |
| Skills loading and installation        | [skills.md](skills.md)                                             |
| Scheduling and reminders               | [scheduling.md](scheduling.md)                                     |
| Routing and multi-agent dispatch       | [routing.md](routing.md)                                           |
| Account router                         | [account-router.md](account-router.md)                             |
| Model router                           | [model-router.md](model-router.md)                                 |
| GitHub Copilot subscription accounts   | [github-copilot-subscription.md](github-copilot-subscription.md)   |
| Hooks and interception                 | [hooks.md](hooks.md)                                               |
| Self-evolution                         | [self-evolution.md](self-evolution.md)                             |
| Launcher management UX                 | [launcher-management.md](launcher-management.md)                   |
| Security, credentials, and isolation   | [security-isolation.md](security-isolation.md)                     |
| Runtime events and observability       | [runtime-events.md](runtime-events.md)                             |
| Durable external event automation      | [event-automation.md](event-automation.md)                         |
| Portability, updates, and packaging    | [portability-updates.md](portability-updates.md)                   |
| Database protocol foundation           | [database-layer.md](database-layer.md)                             |
| Database owner-only local IPC          | [database-local-ipc.md](database-local-ipc.md)                     |
| Database supervisor control plane      | [database-supervisor-control.md](database-supervisor-control.md)   |
| Database provider catalog foundation   | [database-provider-catalog.md](database-provider-catalog.md)       |
| Database storage contracts             | [database-storage-contracts.md](database-storage-contracts.md)     |
| Database physical claims               | [database-physical-claims.md](database-physical-claims.md)         |
| Database provider lease                | [database-provider-lease.md](database-provider-lease.md)           |
| Database SQLite control foundation     | [database-sqlite-control.md](database-sqlite-control.md)           |
| Database SQLite transaction boundary   | [database-sqlite-transaction-boundary.md](database-sqlite-transaction-boundary.md) |
| Database SQLite provider core          | [database-sqlite-provider.md](database-sqlite-provider.md)         |
| Database SQLite inspection             | [database-sqlite-inspection.md](database-sqlite-inspection.md)     |
| Database SQLite offline operations     | [database-sqlite-offline.md](database-sqlite-offline.md)           |
| Database migration stage verification  | [database-stage-verification.md](database-stage-verification.md)   |
| Database migration stage retirement    | [database-stage-retirement.md](database-stage-retirement.md)       |
| Database readiness foundation          | [database-readiness.md](database-readiness.md)                     |
| Database backup model                  | [database-backup-model.md](database-backup-model.md)               |
| Database backup foundation             | [database-backup-foundation.md](database-backup-foundation.md)     |
| Database backup archive                | [database-backup-archive.md](database-backup-archive.md)           |
| Database backup consumption            | [database-backup-consumption.md](database-backup-consumption.md)   |
| Database offline migration             | [database-offline-migration.md](database-offline-migration.md)     |
| SQLite runtime storage                 | [sqlite-storage.md](sqlite-storage.md)                             |
| Workflows and reusable automation      | [workflows.md](workflows.md)                                       |
| Git workspaces and checkout retention  | [git-workspaces.md](git-workspaces.md)                             |
| Agent execution optimization           | [agent-execution-optimization.md](agent-execution-optimization.md) |
| Repository pre-review and findings     | [repository-reviews.md](repository-reviews.md)                     |
| Repository model evaluations           | [repository-model-evaluations.md](repository-model-evaluations.md) |

## Workflow

1. Run `make feature-inventory` to inspect currently discovered surfaces.
2. Update the relevant feature spec when changing behavior.
3. Link unit or integration tests in `Acceptance Evidence`.
4. Run `make lint-features`, `make feature-delta`, `make coverage-delta`,
   `make test`, and affected integration suites.

## Requirement Rules

- Requirement IDs are unique and stable: `FR-<FEATURE>-NNN`.
- Requirement text uses observable behavior: inputs, state, output, errors,
  persistence, ordering, and defaults where applicable.
- Reconstruction notes, data/state models, and algorithms must be detailed
  enough for a coding agent to recreate similar code from the spec.
- Auxiliary interfaces are implementation contracts, not standalone features.
- An `Owns:` line maps discovered repo surfaces to a feature spec.
- `Owns: CODE` maps production files to the feature spec that must change with
  those files.
- `MUST` requirements require unit or integration evidence.
- New feature-owned Go code must reach 95% coverage, and distinct changed
  executable blocks must reach 90%. Existing impacted features and scoped global
  coverage pass when uncovered-statement debt does not increase or the exact
  coverage ratio does not regress. Deleting covered legacy code may lower the
  percentage or covered-statement count when debt does not grow.
