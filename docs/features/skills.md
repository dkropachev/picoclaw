# Skills Loading And Installation

## Feature ID

`FR-SKILLS`

## Behavior Summary

PicoClaw loads skills from workspace, global, and builtin locations, includes
selected skill prompts in agent context, supports registry search and install,
and lets chat users force a skill for one request or the next message. Agent
install wrappers can additionally borrow one shared process-local workspace
lock so owner-local factory products do not race one another during installation.

## Reconstruction Notes

- Similarity target: recreate skill discovery/loading, registry search, install/import/remove, and chat command forced-skill behavior.
- Core types/functions: skill loader, registry manager, ClawHub/GitHub registries,
  installer, search cache, `InstallSkillTool`,
  `NewInstallSkillToolWithLock`, CLI handlers, launcher handlers, and command
  executor handlers.
- Runtime ordering: resolve skill roots, load valid `SKILL.md` files, search configured registries, install/import to workspace, refresh list/search detail, apply `/use` selection during command execution.
- Non-obvious constraints: workspace skills override lower-precedence roots,
  registry failures remain scoped, deletion must not remove builtin/global
  content, and a shared install mutex covers the complete destructive
  install/reinstall transaction rather than only its final write.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SKILLS-001` | MUST | Skill loading discovers valid `SKILL.md` files from workspace, global, and builtin roots in precedence order, resolves metadata-declared names to their discovered files, and deduplicates those identities case-insensitively before building one batch lookup index for context loading. | Users need predictable skill availability, and the UI catalog must identify the same effective skill content that runtime loading uses. |
| `FR-SKILLS-002` | MUST | Invalid, missing, or malformed skill files are skipped or reported without breaking unrelated skills. | One bad skill must not disable the agent. |
| `FR-SKILLS-003` | MUST | Search uses configured registries and cache settings, returning bounded results with registry identity. | Skill discovery must be reproducible and efficient. |
| `FR-SKILLS-004` | MUST | Install/import writes skill content into workspace skills and makes it listable/readable after success. | Installed skills are persistent capabilities. |
| `FR-SKILLS-005` | MUST | Remove deletes an installed workspace skill without deleting builtin or unrelated content. | Users need safe cleanup. |
| `FR-SKILLS-006` | MUST | `/use` and related commands force a selected skill for the requested message scope and can clear pending selection. | Chat workflows need direct skill control. |
| `FR-SKILLS-007` | SHOULD | Deprecated GitHub registry config remains accepted while canonical registry config is preferred. | Existing configs must keep working. |
| `FR-SKILLS-008` | MUST | Browser skill surfaces identify skill origin with accessible text and badge colors that retain sufficient contrast in supported themes. | Origin is operationally important and must remain perceivable without relying on low-contrast color alone. |
| `FR-SKILLS-009` | MUST | `NewInstallSkillToolWithLock` accepts one borrowed process-local mutex and every `Execute` holds it from before argument and registry validation through existing-install inspection, backup, download, installed-skill validation, origin-metadata persistence, rollback or backup cleanup, and result construction. Wrappers given the same mutex serialize that complete operation, while wrappers given distinct mutexes may overlap. A nil mutex creates a private compatibility lock, `NewInstallSkillTool` remains source-compatible with a fresh private lock, and a zero-value wrapper uses a safe fallback lock rather than panicking. The wrapper borrows its registry manager and supplied mutex and does not acquire lifecycle ownership of either. | Owner-local install wrappers must not race backup, replacement, validation, metadata, or rollback in one workspace, while unrelated workspaces must remain independently runnable. |
| `FR-SKILLS-010` | MUST | The installed-skill inventory accepts the shared bounded `query`, opaque `cursor`, and `limit` contract and returns summary-only rows with stable URL-safe backend-issued `id`, `name`, `source`, `origin`, `registry`, `version`, and `installed_at` fields plus `total`, `next_cursor`, `canonical_query`, and an allowlisted typed `query_schema`. Direct item lookup resolves `id` without a loaded list. `/agent/skills` renders the standard List, Table, and Grid views with compact List as the default; `/agent/skills/new` owns import/install choices and `/agent/skills/:id` owns detail. Only removable workspace skills participate in explicit selection and confirmed bulk deletion; partial `not_found` and `read_only_origin` failures retain selection. Canonical `q` and `view` state, paging, refresh, recent queries, and Back restoration use the shared collection controller. Marketplace results remain creation choices at `/agent/hub`, not installed collection items, and the former dialog-only import/detail flow is not compatibility-rendered. | Installed skills need stable deep links, server-authoritative discovery, and safe multi-item cleanup without confusing builtin, global, registry, or marketplace content with removable workspace state. |

## Data And State Model

Skill state includes workspace/global/builtin roots, parsed skill metadata and
content, registry definitions, cached search results, install target paths, and
per-chat pending forced-skill command state. An agent-created install wrapper may
also retain a borrowed generation-local process mutex selected by its caller;
the mutex carries no persistent or cross-process state. Installed collection
summaries are bounded projections keyed by backend-issued opaque IDs; detail
content is loaded only from the ID-addressed item endpoint.

## Surface Ownership

Owns: CODE cmd/picoclaw/internal/skills/**
Owns: CODE pkg/skills/**
Owns: CODE pkg/tools/integration/skills/**
Owns: CODE web/backend/api/skills.go
Owns: CODE web/backend/api/skills_collections.go
Owns: CODE web/frontend/src/api/skills.ts
Owns: CODE web/frontend/src/components/agent/hub/**
Owns: CODE web/frontend/src/components/agent/skills/**
Owns: CODE web/frontend/src/components/agent/skill-tool-collection-route-state.ts
Owns: CODE web/frontend/src/routes/agent/hub.tsx
Owns: CODE web/frontend/src/routes/agent/skills*.tsx
Owns: CODE web/frontend/src/routes/agent/-skills-tools-route.test.tsx
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
Owns: TEST web/backend/api/skills_tools_collections*
Owns: TOOL find_skills
Owns: TOOL install_skill

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| CLI | `picoclaw skills list/search/show/install/remove/list-builtin/install-builtin` | Workspace and registry skill management. | `FR-SKILLS-001` through `FR-SKILLS-005` |
| HTTP | `/api/skills*` | Shared-query installed inventory, ID-addressed detail, registry search, install/import, item deletion, and explicit-ID bulk deletion with stable per-item failures. | `FR-SKILLS-003`, `FR-SKILLS-004`, `FR-SKILLS-005`, `FR-SKILLS-010` |
| Tools | `find_skills`, `install_skill`, `NewInstallSkillToolWithLock` | Agent-callable registry search and install, with optional caller-coordinated serialization across wrappers. | `FR-SKILLS-003`, `FR-SKILLS-004`, `FR-SKILLS-009` |
| Config | `tools.skills.*` | Registries, cache, concurrency, and legacy GitHub fields. | `FR-SKILLS-003`, `FR-SKILLS-007` |
| Frontend | `/agent/skills`, `/agent/skills/new`, `/agent/skills/:id`, and `/agent/hub` | Installed skills use the standard routed collection/detail shells and shared query, view, paging, selection, and mutation reconciliation. Import/install and marketplace discovery remain creation surfaces; origin badges retain accessible text and contrast. | `FR-SKILLS-003`, `FR-SKILLS-004`, `FR-SKILLS-005`, `FR-SKILLS-008`, `FR-SKILLS-010` |

## Algorithms And Ordering

1. Resolve builtin, global, and workspace roots.
2. Load valid skill directories and apply precedence.
3. For search, query enabled registries with cache/concurrency controls.
4. For an agent-tool install, acquire the borrowed workspace mutex before
   validation and retain it through backup/download/validation/metadata and any
   cleanup or rollback. For every install/import surface, validate source
   content and write to the workspace.
5. During command execution, apply or clear forced-skill state before normal agent prompt construction.

## Cross-Feature Behavior

Agent conversations inject loaded skill content. Commands are executed through
the central command path. Self-evolution can draft or apply skills. Security
policies apply to registry tokens and generated content. Agent Conversations
owns the generation-local workspace-identity coordinator that supplies one lock
to root and owner-local `install_skill` wrappers; this feature owns the wrapper's
borrowed-lock execution semantics.

## Failure And Edge Cases

- Search offset beyond available results returns an empty page.
- Registry failures are reported with registry context.
- Skill names are normalized for workspace paths.
- Import rejects unsafe or structurally invalid archives.
- A nil borrowed lock falls back to a private lock. Sharing is process-local and
  caller-directed; this contract does not serialize CLI, HTTP, another process,
  or wrappers constructed with a different mutex.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SKILLS-001`, `FR-SKILLS-002` | [pkg/skills/loader_test.go](../../pkg/skills/loader_test.go), [cmd/picoclaw/internal/skills/list_test.go](../../cmd/picoclaw/internal/skills/list_test.go) |
| `FR-SKILLS-003`, `FR-SKILLS-007` | [pkg/skills/search_cache_test.go](../../pkg/skills/search_cache_test.go), [pkg/skills/clawhub_registry_test.go](../../pkg/skills/clawhub_registry_test.go), [pkg/skills/github_registry_test.go](../../pkg/skills/github_registry_test.go) |
| `FR-SKILLS-004`, `FR-SKILLS-005` | [pkg/skills/installer_test.go](../../pkg/skills/installer_test.go), [web/backend/api/skills_test.go](../../web/backend/api/skills_test.go) |
| `FR-SKILLS-006` | [pkg/commands/show_list_handlers_test.go](../../pkg/commands/show_list_handlers_test.go), [docs/guides/configuration.md](../guides/configuration.md) |
| `FR-SKILLS-008` | [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/src/components/agent/skills/origin-utils.ts](../../web/frontend/src/components/agent/skills/origin-utils.ts) |
| `FR-SKILLS-009` | [pkg/tools/integration/skills_install.go](../../pkg/tools/integration/skills_install.go), [pkg/tools/integration/skills_install_test.go](../../pkg/tools/integration/skills_install_test.go), [pkg/tools/integration_facade.go](../../pkg/tools/integration_facade.go), [pkg/tools/facade_compat_test.go](../../pkg/tools/facade_compat_test.go) |
| `FR-SKILLS-010` | [web/backend/api/skills_tools_collections_test.go](../../web/backend/api/skills_tools_collections_test.go), [web/frontend/src/api/skills.test.ts](../../web/frontend/src/api/skills.test.ts), [web/frontend/src/components/agent/skills/skill-collections.test.tsx](../../web/frontend/src/components/agent/skills/skill-collections.test.tsx), [web/frontend/src/routes/agent/-skills-tools-route.test.tsx](../../web/frontend/src/routes/agent/-skills-tools-route.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts), [web/frontend/tests/collection-visual.spec.ts](../../web/frontend/tests/collection-visual.spec.ts) |

## Implementation Anchors

- [pkg/skills](../../pkg/skills)
- [pkg/tools/integration/skills_install.go](../../pkg/tools/integration/skills_install.go)
- [web/backend/api/skills.go](../../web/backend/api/skills.go)
- [web/backend/api/skills_collections.go](../../web/backend/api/skills_collections.go)
- [web/frontend/src/components/agent/skills/skill-collections.tsx](../../web/frontend/src/components/agent/skills/skill-collections.tsx)
- [cmd/picoclaw/internal/skills](../../cmd/picoclaw/internal/skills)
