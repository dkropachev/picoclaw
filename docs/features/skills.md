# Skills Loading And Installation

## Feature ID

`FR-SKILLS`

## Behavior Summary

PicoClaw loads skills from workspace, global, and builtin locations, includes
selected skill prompts in agent context, supports registry search and install,
and lets chat users force a skill for one request or the next message.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SKILLS-001` | MUST | Skill loading discovers valid `SKILL.md` files from workspace, global, and builtin roots in precedence order. | Users need predictable skill availability. |
| `FR-SKILLS-002` | MUST | Invalid, missing, or malformed skill files are skipped or reported without breaking unrelated skills. | One bad skill must not disable the agent. |
| `FR-SKILLS-003` | MUST | Search uses configured registries and cache settings, returning bounded results with registry identity. | Skill discovery must be reproducible and efficient. |
| `FR-SKILLS-004` | MUST | Install/import writes skill content into workspace skills and makes it listable/readable after success. | Installed skills are persistent capabilities. |
| `FR-SKILLS-005` | MUST | Remove deletes an installed workspace skill without deleting builtin or unrelated content. | Users need safe cleanup. |
| `FR-SKILLS-006` | MUST | `/use` and related commands force a selected skill for the requested message scope and can clear pending selection. | Chat workflows need direct skill control. |
| `FR-SKILLS-007` | SHOULD | Deprecated GitHub registry config remains accepted while canonical registry config is preferred. | Existing configs must keep working. |

## Auxiliary Interfaces

Owns: CLI cmd/picoclaw/internal/skills/*
Owns: CONFIG.tools.skills*
Owns: CONFIG.tools.find_skills*
Owns: CONFIG.tools.install_skill*
Owns: HTTP * /api/skills*
Owns: TEST cmd/picoclaw/internal/skills/*
Owns: TEST pkg/commands/*
Owns: TEST pkg/skills/*
Owns: TEST pkg/tools/integration/skills*
Owns: TEST web/backend/api/skills*
Owns: TOOL find_skills
Owns: TOOL install_skill

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw skills list/search/show/install/remove/list-builtin/install-builtin` | Workspace and registry skill management. | `FR-SKILLS-001` through `FR-SKILLS-005` |
| HTTP | `/api/skills*` | Launcher list, detail, search, install, import, and delete. | `FR-SKILLS-003`, `FR-SKILLS-004`, `FR-SKILLS-005` |
| Tools | `find_skills`, `install_skill` | Agent-callable registry search and install. | `FR-SKILLS-003`, `FR-SKILLS-004` |
| Config | `tools.skills.*` | Registries, cache, concurrency, and legacy GitHub fields. | `FR-SKILLS-003`, `FR-SKILLS-007` |

## Cross-Feature Behavior

Agent conversations inject loaded skill content. Commands are executed through
the central command path. Self-evolution can draft or apply skills. Security
policies apply to registry tokens and generated content.

## Failure And Edge Cases

- Search offset beyond available results returns an empty page.
- Registry failures are reported with registry context.
- Skill names are normalized for workspace paths.
- Import rejects unsafe or structurally invalid archives.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SKILLS-001`, `FR-SKILLS-002` | [pkg/skills/loader_test.go](../../pkg/skills/loader_test.go), [cmd/picoclaw/internal/skills/list_test.go](../../cmd/picoclaw/internal/skills/list_test.go) |
| `FR-SKILLS-003`, `FR-SKILLS-007` | [pkg/skills/search_cache_test.go](../../pkg/skills/search_cache_test.go), [pkg/skills/clawhub_registry_test.go](../../pkg/skills/clawhub_registry_test.go), [pkg/skills/github_registry_test.go](../../pkg/skills/github_registry_test.go) |
| `FR-SKILLS-004`, `FR-SKILLS-005` | [pkg/skills/installer_test.go](../../pkg/skills/installer_test.go), [web/backend/api/skills_test.go](../../web/backend/api/skills_test.go) |
| `FR-SKILLS-006` | [pkg/commands/show_list_handlers_test.go](../../pkg/commands/show_list_handlers_test.go), [docs/guides/configuration.md](../guides/configuration.md) |

## Implementation Anchors

- [pkg/skills](../../pkg/skills)
- [web/backend/api/skills.go](../../web/backend/api/skills.go)
- [cmd/picoclaw/internal/skills](../../cmd/picoclaw/internal/skills)
