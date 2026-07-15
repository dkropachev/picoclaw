# Skills Loading And Installation

## Feature ID

`FR-SKILLS`

## Behavior Summary

PicoClaw loads skills from workspace, global, and builtin locations, includes
selected skill prompts in agent context, supports registry search and install,
and lets chat users force a skill for one request or the next message.

## Reconstruction Notes

- Similarity target: recreate skill discovery/loading, registry search, install/import/remove, and chat command forced-skill behavior.
- Core types/functions: skill loader, registry manager, ClawHub/GitHub registries, installer, search cache, CLI handlers, launcher handlers, and command executor handlers.
- Runtime ordering: resolve skill roots, load valid `SKILL.md` files, search configured registries, install/import to workspace, refresh list/search detail, apply `/use` selection during command execution.
- Non-obvious constraints: workspace skills override lower-precedence roots, registry failures remain scoped, and deletion must not remove builtin/global content.

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

## Data And State Model

Skill state includes workspace/global/builtin roots, parsed skill metadata and
content, registry definitions, cached search results, install target paths, and
per-chat pending forced-skill command state.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/skills/**
Owns: CODE pkg/skills/**
Owns: CODE pkg/tools/integration/skills/**
Owns: CODE web/backend/api/skills.go
Owns: CODE web/frontend/src/api/skills.ts
Owns: CODE web/frontend/src/components/agent/hub/**
Owns: CODE web/frontend/src/components/agent/skills/**
Owns: CODE web/frontend/src/routes/agent/hub.tsx
Owns: CODE web/frontend/src/routes/agent/skills.tsx
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

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw skills list/search/show/install/remove/list-builtin/install-builtin` | Workspace and registry skill management. | `FR-SKILLS-001` through `FR-SKILLS-005` |
| HTTP | `/api/skills*` | Launcher list, detail, search, install, import, and delete. | `FR-SKILLS-003`, `FR-SKILLS-004`, `FR-SKILLS-005` |
| Tools | `find_skills`, `install_skill` | Agent-callable registry search and install. | `FR-SKILLS-003`, `FR-SKILLS-004` |
| Config | `tools.skills.*` | Registries, cache, concurrency, and legacy GitHub fields. | `FR-SKILLS-003`, `FR-SKILLS-007` |

## Algorithms And Ordering

1. Resolve builtin, global, and workspace roots.
2. Load valid skill directories and apply precedence.
3. For search, query enabled registries with cache/concurrency controls.
4. For install/import, validate source content and write to workspace.
5. During command execution, apply or clear forced-skill state before normal agent prompt construction.

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
