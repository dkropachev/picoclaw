# Account Router

## Feature ID

`FR-MODEL-ROUTER`

## Behavior Summary

Account routers are `model_list` entries that behave like normal chat model
aliases while selecting one or more credential accounts through a static block
graph. A router defines the shared model for the joint account; every connected
account sends that same model through its own credentials. The launcher Accounts
surface exposes both a UI editor for connecting primitive blocks/accounts and a
raw JSON graph editor.

## Reconstruction Notes

- Similarity target: recreate a config-backed account router whose graph is
  encoded in `model_list[].router` with workflow-like block IDs and fallback
  edges.
- Core types/functions: `ModelRouterConfig`, `ModelRouterBlock`,
  `modelrouter.Router`, persistent router state store, agent candidate
  selection, fallback result recording, and launcher model handlers.
- Runtime ordering: validate router graph and account references, build account
  candidates from credential account refs, select an entry block for the
  session, execute normal provider fallback, record success/failure/usage, and
  persist updated route state atomically.
- Non-obvious constraints: legacy model-name account refs remain supported for
  old configs, routers cannot reference router models, duplicate legacy account
  names are ambiguous, load balance does not reshuffle an active session unless
  context compression occurs or the chosen account becomes unavailable, and
  failed attempts must be attributed by stable account identity even when two
  accounts use the same provider/shared-model pair.

## Requirements

| ID                    | Level | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Rationale                                                                                                            |
| --------------------- | ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `FR-MODEL-ROUTER-001` | MUST  | An account router is a `model_list` entry with provider `router`, a normal `model_name` alias, a shared `model`, and an enabled `router` graph containing an `entry` block and typed blocks.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Users should select a router anywhere they select a model while managing it as an account capability.                |
| `FR-MODEL-ROUTER-002` | MUST  | Account blocks reference exactly one credential account ref such as `credential:openai:work`; account and load-balance blocks may fall back to any other block, including chains such as load-balancer -> account -> load-balancer. Fallback traversal rejects unknown legacy refs, cycles, router refs, and ambiguous duplicate legacy account names.                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Static routing must fail at config time instead of during a turn.                                                    |
| `FR-MODEL-ROUTER-003` | MUST  | Load-balance blocks choose among account refs by `tokens_spent`, `closest_limit`, or `blind`; blind non-session choice refreshes every configured interval, defaulting to 60 seconds.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Operators need simple deterministic distribution before automatic job routing exists.                                |
| `FR-MODEL-ROUTER-004` | MUST  | A session keeps its selected load-balance account until context compression or until that account is unavailable due to auth, billing, rate-limit, network, timeout, overload, or other classified provider failure.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Long conversations should not drift across accounts unless continuity or availability requires it.                   |
| `FR-MODEL-ROUTER-005` | MUST  | Router state persists per workspace with config hash, account health, token/request usage, block cursors, and session affinities; writes are atomic, stale sessions are pruned, removed accounts are pruned, cooldowns are reason-aware, and corrupt state files are preserved with a `.corrupt.<timestamp>` suffix before recovery.                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Account health must survive restarts without pinning bad or stale state forever.                                     |
| `FR-MODEL-ROUTER-006` | MUST  | Agent execution treats router aliases as regular model candidates: initial selection supplies provider candidates, context compression can reselect, fallback results update router state, `/use` can switch to a router, and all account candidates are registered for rate limiting.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Router behavior must compose with existing turns, fallbacks, and model switching.                                    |
| `FR-MODEL-ROUTER-007` | MUST  | Launcher Accounts management can add, edit, list, delete, and set an account router as default without storing API secrets on the router entry; invalid router account references return validation errors. The fullscreen create UI starts with no router block selected, prompts the user to add an account or load-balancer block, and never auto-creates the first account block. The fullscreen UI editor can create account/load-balancer blocks, connect fallback edges between blocks, show the full diagram as a draggable canvas, automatically stack the entry-to-fallback chain from top to bottom, pan the canvas, zoom by Shift+scroll or scale controls, and only offer shared models that are available in every connected account. The raw JSON editor exposes the same router graph. | Browser setup must expose routers safely as joint accounts without silently wiring a new router to an unintended block. |

## Data And State Model

Router config lives in `model_list[]`:

```json
{
  "model_name": "router-main",
  "provider": "router",
  "model": "gpt-5.4",
  "router": {
    "enabled": true,
    "entry": "pool",
    "refresh_interval_seconds": 60,
    "blocks": [
      {
        "id": "pool",
        "type": "load_balance",
        "accounts": ["credential:openai", "credential:openai:backup"],
        "strategy": "tokens_spent",
        "fallback": "backup"
      },
      { "id": "backup", "type": "account", "account": "credential:anthropic" }
    ]
  }
}
```

Runtime state persists under the agent workspace as
`model_router_state.json`. Each router stores a config hash, account status and
usage, session block affinities, block cursors, and timestamps.

## Auxiliary Interfaces

| Type    | Surface                                       | Contract                                                                                                            | Requirement IDs                                     |
| ------- | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| Config  | `model_list[].router`                         | Static block graph with account and load-balance blocks, fallback refs, strategy, and refresh interval.             | `FR-MODEL-ROUTER-001` through `FR-MODEL-ROUTER-003` |
| Runtime | `pkg/modelrouter`                             | Select candidates, enforce session affinity, track health/usage, persist state, and recover corrupt state.          | `FR-MODEL-ROUTER-003` through `FR-MODEL-ROUTER-005` |
| Agent   | Agent model resolution and fallback execution | Build router account candidates, select/reselect per turn, and record fallback results by account identity.         | `FR-MODEL-ROUTER-004`, `FR-MODEL-ROUTER-006`        |
| HTTP/UI | `/api/models*`, `/accounts`                   | Manage account router entries through launcher Accounts management as regular model aliases without router secrets. | `FR-MODEL-ROUTER-007`                               |

## Algorithms And Ordering

1. Normalize incoming router model entries to provider `router`, default blank
   model ID to the alias, preserve an explicit shared model ID, and clear
   provider credential fields.
2. Validate block IDs, entry references, block types, load-balance strategy,
   duplicate load-balance account refs, fallback refs, fallback cycles, and
   account references across the final model list.
3. Build router accounts by resolving each `credential:` account ref into a
   runtime provider config using that credential and the router's shared model.
   Legacy model-name refs still resolve through existing model candidate
   resolution; router entries are skipped as upstream accounts.
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
Account routers only provide the candidate selection layer. Launcher management
owns the authenticated browser/API surface that creates and edits router entries
on the Accounts page. Security isolation continues to own secure string
semantics; router entries intentionally do not store API keys.

## Failure And Edge Cases

- Missing `router`, disabled router config, empty entry, missing blocks, unknown
  block types, unsupported strategies, duplicate load-balance account refs, and
  fallback cycles are rejected.
- Unknown legacy model-name refs, router refs, malformed credential refs, or
  ambiguous duplicate legacy account references are rejected after the full
  model list is known.
- Account status cooldowns differ by failure class: auth/billing failures stay
  unavailable longer than rate-limit or transient network failures.
- Same provider/model accounts remain distinguishable by stable identity during
  failure attribution.
- Same provider/shared-model accounts remain distinguishable by stable identity
  during provider selection so each request uses the chosen account's
  credentials.
- Corrupt state is renamed aside and a fresh state file is written.

## Acceptance Evidence

| Requirement IDs                                                     | Evidence                                                                                                                                                                                              |
| ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-MODEL-ROUTER-001`, `FR-MODEL-ROUTER-002`, `FR-MODEL-ROUTER-003` | [pkg/config/model_router_test.go](../../pkg/config/model_router_test.go)                                                                                                                              |
| `FR-MODEL-ROUTER-003`, `FR-MODEL-ROUTER-004`, `FR-MODEL-ROUTER-005` | [pkg/modelrouter/router_test.go](../../pkg/modelrouter/router_test.go)                                                                                                                                |
| `FR-MODEL-ROUTER-006`                                               | [pkg/agent/model_router_test.go](../../pkg/agent/model_router_test.go), [pkg/providers/fallback_test.go](../../pkg/providers/fallback_test.go)                                                        |
| `FR-MODEL-ROUTER-007`                                               | [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/frontend/src/components/models/model-card.test.tsx](../../web/frontend/src/components/models/model-card.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/modelrouter](../../pkg/modelrouter)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/agent/pipeline_setup.go](../../pkg/agent/pipeline_setup.go)
- [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go)
- [pkg/providers/fallback.go](../../pkg/providers/fallback.go)
- [web/backend/api/models.go](../../web/backend/api/models.go)
- [web/frontend/src/components/credentials](../../web/frontend/src/components/credentials)

## Surface Ownership

Owns: CODE pkg/modelrouter/**
Owns: CODE pkg/agent/model_router_record.go
Owns: TEST pkg/modelrouter/**
Owns: TEST pkg/config/model_router_test.go
