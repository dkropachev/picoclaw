# Model Router

## Feature ID

`FR-MODEL-ROUTER`

## Behavior Summary

Model routers are `model_list` entries that behave like normal chat model
aliases while selecting one or more existing account entries through a static
block graph. The first implementation supports account and load-balance blocks,
fallback edges, session-stable routing, context-compression reselection, and
restart-safe account health persistence.

## Reconstruction Notes

- Similarity target: recreate a config-backed model router whose graph is
  encoded in `model_list[].router` with workflow-like block IDs and fallback
  edges.
- Core types/functions: `ModelRouterConfig`, `ModelRouterBlock`,
  `modelrouter.Router`, persistent router state store, agent candidate
  selection, fallback result recording, and launcher model handlers.
- Runtime ordering: validate router graph and account references, build account
  candidates from existing model entries, select an entry block for the session,
  execute normal provider fallback, record success/failure/usage, and persist
  updated route state atomically.
- Non-obvious constraints: routers cannot reference router models, duplicate
  account names are ambiguous, load balance does not reshuffle an active
  session unless context compression occurs or the chosen account becomes
  unavailable, and failed attempts must be attributed by stable account identity
  even when two accounts use the same provider/model pair.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-MODEL-ROUTER-001` | MUST | A router model is a `model_list` entry with provider `router`, a normal `model_name` alias, and an enabled `router` graph containing an `entry` block and typed blocks. | Users should select a router anywhere they select a model. |
| `FR-MODEL-ROUTER-002` | MUST | Account blocks reference exactly one existing non-router account and may define a fallback block; fallback traversal rejects unknown refs, cycles, router refs, and ambiguous duplicate account names. | Static routing must fail at config time instead of during a turn. |
| `FR-MODEL-ROUTER-003` | MUST | Load-balance blocks choose among account refs by `tokens_spent`, `closest_limit`, or `blind`; blind non-session choice refreshes every configured interval, defaulting to 60 seconds. | Operators need simple deterministic distribution before automatic job routing exists. |
| `FR-MODEL-ROUTER-004` | MUST | A session keeps its selected load-balance account until context compression or until that account is unavailable due to auth, billing, rate-limit, network, timeout, overload, or other classified provider failure. | Long conversations should not drift across accounts unless continuity or availability requires it. |
| `FR-MODEL-ROUTER-005` | MUST | Router state persists per workspace with config hash, account health, token/request usage, block cursors, and session affinities; writes are atomic, stale sessions are pruned, removed accounts are pruned, cooldowns are reason-aware, and corrupt state files are preserved with a `.corrupt.<timestamp>` suffix before recovery. | Account health must survive restarts without pinning bad or stale state forever. |
| `FR-MODEL-ROUTER-006` | MUST | Agent execution treats router aliases as regular model candidates: initial selection supplies provider candidates, context compression can reselect, fallback results update router state, `/use` can switch to a router, and all account candidates are registered for rate limiting. | Router behavior must compose with existing turns, fallbacks, and model switching. |
| `FR-MODEL-ROUTER-007` | MUST | Launcher model management can add, edit, list, delete, and set a router as default without storing API secrets on the router entry; invalid router account references return validation errors. | Browser setup must expose routers safely as normal model entries. |

## Data And State Model

Router config lives in `model_list[]`:

```json
{
  "model_name": "router-main",
  "provider": "router",
  "model": "router-main",
  "router": {
    "enabled": true,
    "entry": "pool",
    "refresh_interval_seconds": 60,
    "blocks": [
      {
        "id": "pool",
        "type": "load_balance",
        "accounts": ["account-a", "account-b"],
        "strategy": "tokens_spent",
        "fallback": "backup"
      },
      { "id": "backup", "type": "account", "account": "account-c" }
    ]
  }
}
```

Runtime state persists under the agent workspace as
`model_router_state.json`. Each router stores a config hash, account status and
usage, session block affinities, block cursors, and timestamps.

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `model_list[].router` | Static block graph with account and load-balance blocks, fallback refs, strategy, and refresh interval. | `FR-MODEL-ROUTER-001` through `FR-MODEL-ROUTER-003` |
| Runtime | `pkg/modelrouter` | Select candidates, enforce session affinity, track health/usage, persist state, and recover corrupt state. | `FR-MODEL-ROUTER-003` through `FR-MODEL-ROUTER-005` |
| Agent | Agent model resolution and fallback execution | Build router account candidates, select/reselect per turn, and record fallback results by account identity. | `FR-MODEL-ROUTER-004`, `FR-MODEL-ROUTER-006` |
| HTTP/UI | `/api/models*`, `/models` | Manage router entries through launcher model management as regular model aliases without router secrets. | `FR-MODEL-ROUTER-007` |

## Algorithms And Ordering

1. Normalize incoming router model entries to provider `router`, default model
   ID to the alias, and clear provider credential fields.
2. Validate block IDs, entry references, block types, load-balance strategy,
   duplicate load-balance account refs, fallback refs, fallback cycles, and
   account references across the final model list.
3. Build router accounts by resolving each referenced account through existing
   model candidate resolution; router entries are skipped as upstream accounts.
4. For account blocks, use the account if operational; otherwise use fallback
   candidates when a fallback exists.
5. For load-balance blocks, filter to operational accounts, reuse session
   affinity unless compression or unavailability allows reselection, then choose
   by tokens spent, RPM pressure, or blind session hash / interval cursor.
6. Execute provider fallback normally and record every classified failed attempt
   against the stable account identity, then mark the successful account
   operational and increment usage.
7. Persist state after selection or result recording, pruning stale sessions and
   removed accounts and resetting incompatible config hashes.

## Cross-Feature Behavior

Agent conversations own the normal turn loop and provider prompt behavior.
Model routers only provide the candidate selection layer. Launcher management
owns the authenticated browser/API surface that creates and edits router model
entries. Security isolation continues to own secure string semantics; router
entries intentionally do not store API keys.

## Failure And Edge Cases

- Missing `router`, disabled router config, empty entry, missing blocks, unknown
  block types, unsupported strategies, duplicate load-balance account refs, and
  fallback cycles are rejected.
- Unknown, router, or ambiguous duplicate account references are rejected after
  the full model list is known.
- Account status cooldowns differ by failure class: auth/billing failures stay
  unavailable longer than rate-limit or transient network failures.
- Same provider/model accounts remain distinguishable by stable identity during
  failure attribution.
- Corrupt state is renamed aside and a fresh state file is written.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-MODEL-ROUTER-001`, `FR-MODEL-ROUTER-002`, `FR-MODEL-ROUTER-003` | [pkg/config/model_router_test.go](../../pkg/config/model_router_test.go) |
| `FR-MODEL-ROUTER-003`, `FR-MODEL-ROUTER-004`, `FR-MODEL-ROUTER-005` | [pkg/modelrouter/router_test.go](../../pkg/modelrouter/router_test.go) |
| `FR-MODEL-ROUTER-006` | [pkg/agent/model_router_test.go](../../pkg/agent/model_router_test.go), [pkg/providers/fallback_test.go](../../pkg/providers/fallback_test.go) |
| `FR-MODEL-ROUTER-007` | [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/modelrouter](../../pkg/modelrouter)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/agent/pipeline_setup.go](../../pkg/agent/pipeline_setup.go)
- [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go)
- [pkg/providers/fallback.go](../../pkg/providers/fallback.go)
- [web/backend/api/models.go](../../web/backend/api/models.go)
- [web/frontend/src/components/models](../../web/frontend/src/components/models)

## Surface Ownership

Owns: CODE pkg/modelrouter/**
Owns: CODE pkg/agent/model_router_record.go
Owns: TEST pkg/modelrouter/**
Owns: TEST pkg/config/model_router_test.go
