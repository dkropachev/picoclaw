# Model Router

## Feature ID

`FR-MODEL-ROUTER`

## Behavior Summary

Model routers are named chat model aliases persisted under `model_routers[]`.
They materialize as virtual model rows and can be selected anywhere a default
chat model can be selected. At runtime, the router evaluates the current chat
input and selects a configured downstream model alias. Account selection remains
the independent `account_ref` axis.

## Reconstruction Notes

- Similarity target: recreate a small graph-based input router that materializes
  as a normal chat model while persisting outside `model_list[]`.
- Core types/functions: `ModelRouterConfig`, `ModelRouterBlock`,
  `ModelRouterRule`, `modelrouter.Router`, agent candidate selection, and
  launcher model handlers.
- Runtime ordering: load config, validate exact alias targets, select or expand
  `account_ref` to a concrete account, evaluate the current input to select an
  alias, then resolve that alias for the chosen account.
- Non-obvious constraints: terminal model blocks target only `model_aliases[]`;
  they cannot target account routers or other model routers.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-MODEL-ROUTER-001` | MUST | Model routers persist under top-level `model_routers[]`; `model_list[]` must not persist model-router rows. Loading materializes each enabled router as a virtual `provider: model-router` model row with the router alias as `model_name`. | Router aliases should behave like models without corrupting stored provider configuration. |
| `FR-MODEL-ROUTER-002` | MUST | A model router has a name, enabled flag, entry block, and typed blocks. Rule blocks evaluate ordered rules over the current input, and terminal model blocks point to exact configured model aliases only. Supported rules include contains, regex, code-block presence, and media presence. | Users need concrete input-based routing logic without mixing model and account routing. |
| `FR-MODEL-ROUTER-003` | MUST | Config validation rejects missing entry blocks, duplicate block IDs, invalid regex rules, fallback cycles, model-router-to-model-router targets, unknown targets, and account routers that try to reference model-router aliases as accounts. | Invalid graphs should fail before runtime. |
| `FR-MODEL-ROUTER-004` | MUST | Agent execution evaluates a selected model router to an exact alias before provider execution. It independently resolves `account_ref` (including account-router expansion), then resolves the selected alias base model or its concrete-account override. | Model routers and account routers must compose without either owning the other axis. |
| `FR-MODEL-ROUTER-005` | MUST | The launcher `/models/routers` surface can list, create, inspect, edit, delete, and select model routers as the agent-default alias reference without storing API secrets or account selection on router entries; account routers remain managed on the Accounts surface. | Router setup needs a browser UI while keeping model and account administration separate. |
| `FR-MODEL-ROUTER-006` | MUST | The chat model selector keeps account choices, account routers, and model routers in separate groups, and model-router virtual rows are selectable as default chat targets. | Users need to see what kind of routing target they are selecting. |

| `FR-MODEL-ROUTER-007` | MUST | Model Routers expose configured routers through name-addressed list/detail/create/update/delete endpoints and the shared bounded query contract with `total`, opaque `next_cursor`, `canonical_query`, and typed allowlisted `query_schema`. At-most-200 explicit-name bulk deletion requires the current config revision, computes selection-aware agent/default/router reference blockers, removes all valid routers in one fully validated fenced save, and returns `deleted_ids`, stable per-name failures/blockers, the new revision, and restart effects without changing unrelated config. The shared List/Table/Grid UI lives at `/models/routers`, with dedicated `/new`, `/{name}`, and `/{name}/edit` routes; detail links load independently, and legacy `/models` is not compatibility-rendered. | Router administration needs stable identities, server-authoritative paging, and safe partial deletion rather than mutable model-array indexes and an embedded editor. |

## Data And State Model

Model Router collection identity is normalized router `name`. Query fields are
sortable `name`, `enabled`, `blocks`, and `rules`; `enabled` suggests `true` and
`false`, and default ordering is name plus stable name. Bulk failure codes
include `invalid_id`, `duplicate_id`, `not_found`, and `referenced`; blockers
contain bounded safe labels only. The returned config revision fences every
name-addressed or bulk mutation.

Router config lives in `model_routers[]`:

```json
{
  "name": "task-router",
  "enabled": true,
  "entry": "entry",
  "blocks": [
    {
      "id": "entry",
      "type": "rules",
      "rules": [{ "match": "has_code", "target": "code" }],
      "fallback": "default"
    },
    { "id": "code", "type": "model", "model": "code-model" },
    { "id": "default", "type": "model", "model": "daily-driver" }
  ]
}
```

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `model_routers[]` | Persist and validate model-router graphs, then materialize virtual model rows. | `FR-MODEL-ROUTER-001` through `FR-MODEL-ROUTER-003` |
| Runtime | `pkg/modelrouter` | Evaluate ordered input rules and return the selected downstream target. | `FR-MODEL-ROUTER-002`, `FR-MODEL-ROUTER-004` |
| Agent | model resolution and turn setup | Resolve model-router aliases before provider/account-router execution. | `FR-MODEL-ROUTER-004` |
| HTTP/UI | `/api/accounts/models*`, `/models/routers`, Chat selector | Manage model routers and expose them as selectable chat models. | `FR-MODEL-ROUTER-005`, `FR-MODEL-ROUTER-006` |

| HTTP/UI | `/api/model-routers*`; `/models/routers*` | Name-addressed typed query/list/detail/CRUD, revision-fenced explicit-name bulk delete, and standard collection/detail/editor routes. | `FR-MODEL-ROUTER-005`, `FR-MODEL-ROUTER-007` |

## Algorithms And Ordering

1. Normalize incoming model-router API payloads to provider `model-router`, clear
   credential fields, and set the virtual model ID to the alias.
2. Persist router definitions in `model_routers[]`, then materialize virtual
   model rows during load and API writes.
3. Validate graph shape, rule syntax, fallback acyclicity, and exact downstream
   alias targets.
4. During turn setup, if the selected model is a model router, evaluate rules
   against the current user message and media presence.
5. Resolve the selected alias after the independent account/account-router
   selection, then execute the existing provider fallback path.

## Cross-Feature Behavior

Account routers independently provide account-level expansion for the selected
`account_ref`. Agent conversations own alias resolution, provider calls,
retries, and fallback recording. Launcher management owns the authenticated
browser/API model management surface. Security isolation owns secure-string
behavior and requires router entries to remain secret-free.

## Failure And Edge Cases

- Missing names, disabled routers, empty entries, duplicate block IDs, missing
  target blocks, invalid regex values, and fallback cycles are rejected.
- Model blocks referencing unknown aliases, account routers, or another model
  router are rejected.
- Account routers referencing model-router aliases as accounts are rejected.
- If runtime selection cannot produce an alias, the turn fails explicitly; it
  does not fall back to a provider or raw model.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-MODEL-ROUTER-001`, `FR-MODEL-ROUTER-003` | [pkg/config/model_router_test.go](../../pkg/config/model_router_test.go) |
| `FR-MODEL-ROUTER-002` | [pkg/modelrouter/router_test.go](../../pkg/modelrouter/router_test.go) |
| `FR-MODEL-ROUTER-004` | [pkg/agent/account_router_test.go](../../pkg/agent/account_router_test.go) |
| `FR-MODEL-ROUTER-005`, `FR-MODEL-ROUTER-006` | [web/frontend/src/hooks/use-chat-models.test.ts](../../web/frontend/src/hooks/use-chat-models.test.ts), [web/frontend/src/components/models/model-card.test.tsx](../../web/frontend/src/components/models/model-card.test.tsx) |

| `FR-MODEL-ROUTER-007` | [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [pkg/config/model_router_test.go](../../pkg/config/model_router_test.go), [web/frontend/src/api/models.test.ts](../../web/frontend/src/api/models.test.ts), [web/frontend/tests/collection-visual.spec.ts](../../web/frontend/tests/collection-visual.spec.ts) |

## Implementation Anchors

- [pkg/config/model_router.go](../../pkg/config/model_router.go)
- [pkg/modelrouter/router.go](../../pkg/modelrouter/router.go)
- [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go)
- [web/backend/api/models.go](../../web/backend/api/models.go)
- [web/frontend/src/components/models/model-router-sheet.tsx](../../web/frontend/src/components/models/model-router-sheet.tsx)

## Surface Ownership

Owns: CODE pkg/modelrouter/**
Owns: CODE pkg/config/model_router.go
Owns: CODE web/backend/api/model_collections.go
Owns: CODE web/frontend/src/components/models/model-router-sheet.tsx
Owns: CODE web/frontend/src/components/models/models-page.tsx
Owns: CODE web/frontend/src/components/collections/pilots/model-router*
Owns: CODE web/frontend/src/routes/models_*router*.tsx
Owns: HTTP * /api/model-routers*
Owns: UI /models/routers*
Owns: CONFIG.model_routers
Owns: CONFIG.model_routers.*.blocks
Owns: CONFIG.model_routers.*.blocks.*.fallback
Owns: CONFIG.model_routers.*.blocks.*.id
Owns: CONFIG.model_routers.*.blocks.*.model
Owns: CONFIG.model_routers.*.blocks.*.rules
Owns: CONFIG.model_routers.*.blocks.*.rules.*.match
Owns: CONFIG.model_routers.*.blocks.*.rules.*.target
Owns: CONFIG.model_routers.*.blocks.*.rules.*.value
Owns: CONFIG.model_routers.*.blocks.*.type
Owns: CONFIG.model_routers.*.enabled
Owns: CONFIG.model_routers.*.entry
Owns: CONFIG.model_routers.*.name
Owns: TEST pkg/modelrouter/**
Owns: TEST pkg/config/model_router_test.go
