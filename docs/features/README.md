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

| Feature | Spec |
| --- | --- |
| Agent conversations and turn execution | [agent-conversations.md](agent-conversations.md) |
| Chat channels and gateway delivery | [chat-channels.md](chat-channels.md) |
| Session memory and history | [session-memory.md](session-memory.md) |
| Threads and handoffs | [threads.md](threads.md) |
| Tool execution | [tool-execution.md](tool-execution.md) |
| MCP integration and discovery | [mcp-integration.md](mcp-integration.md) |
| Skills loading and installation | [skills.md](skills.md) |
| Scheduling and reminders | [scheduling.md](scheduling.md) |
| Routing and multi-agent dispatch | [routing.md](routing.md) |
| Hooks and interception | [hooks.md](hooks.md) |
| Self-evolution | [self-evolution.md](self-evolution.md) |
| Launcher management UX | [launcher-management.md](launcher-management.md) |
| Security, credentials, and isolation | [security-isolation.md](security-isolation.md) |
| Runtime events and observability | [runtime-events.md](runtime-events.md) |
| Portability, updates, and packaging | [portability-updates.md](portability-updates.md) |
| Workflows and reusable automation | [workflows.md](workflows.md) |

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
- Feature-owned Go coverage and changed executable production lines cannot
  regress in PRs.
