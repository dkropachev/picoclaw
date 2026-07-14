# Feature Requirements

This directory is the canonical source of truth for PicoClaw product behavior.
Feature requirements describe capabilities. Config, HTTP APIs, CLI commands,
provider adapters, and tests are auxiliary surfaces that prove or expose those
capabilities.

Future behavior changes must update the relevant feature requirement before or
with the code change. The `make lint-features` gate verifies that feature specs
own discovered repository surfaces and that each requirement has acceptance
evidence.

## Canonical Specs

| Feature | Spec |
| --- | --- |
| Agent conversations and turn execution | [agent-conversations.md](agent-conversations.md) |
| Chat channels and gateway delivery | [chat-channels.md](chat-channels.md) |
| Session memory and history | [session-memory.md](session-memory.md) |
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

## Workflow

1. Run `make feature-inventory` to inspect currently discovered surfaces.
2. Update the relevant feature spec when changing behavior.
3. Link unit or integration tests in `Acceptance Evidence`.
4. Run `make lint-features`, `make test`, and affected integration suites.

## Requirement Rules

- Requirement IDs are unique and stable: `FR-<FEATURE>-NNN`.
- Requirement text uses observable behavior: inputs, state, output, errors,
  persistence, ordering, and defaults where applicable.
- Auxiliary interfaces are implementation contracts, not standalone features.
- An `Owns:` line maps discovered repo surfaces to a feature spec.
