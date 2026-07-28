# Account Router

## Feature ID

`FR-ACCOUNT-ROUTER`

## Behavior Summary

Account routers select one or more accounts through a static block graph. They
do not own a model ID and are configured as first-class `account_routers[]`
entries, not as fake `model_list` rows. The launcher Accounts surface exposes
both a UI editor for connecting primitive blocks/accounts and a raw JSON graph
editor. Pico Chat discovers selectable model IDs after the user chooses a router
and sends the selected ID only for that turn.

## Reconstruction Notes

- Similarity target: recreate a config-backed account router with workflow-like
  block IDs and fallback edges.
- Core types/functions: `AccountRouterConfig`, `AccountRouterBlock`,
  `accountrouter.Router`, persistent router state store, agent candidate
  selection, fallback result recording, and launcher model handlers.
- Runtime ordering: validate router graph and account references, build account
  candidates, apply a turn-scoped requested model or each account's
  configured/provider default, select an entry block for the session, evaluate
  any branch conditions, execute normal provider fallback, record
  success/failure/usage, and persist updated route state atomically.
- Non-obvious constraints: routers cannot reference other routers, load balance
  does not reshuffle an active session unless context compression occurs or the
  chosen account becomes unavailable, and failed attempts must be attributed by
  stable account identity even when two accounts use the same provider and
  model ID.

## Requirements

| ID                      | Level | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Rationale                                                                                                                                                                       |
| ----------------------- | ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-ACCOUNT-ROUTER-001` | MUST  | An account router has a name, enabled flag, entry block, refresh interval, and typed account/load-balance blocks, but no model setting. Account routers are persisted under `account_routers[]`; `model_list` remains limited to runnable provider model configs and must not persist router rows. Runtime router resolution must accept `credential:<provider>:<id>` account references directly, without requiring duplicate runnable account aliases in `model_list`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Users should manage account routing independently from per-turn model selection and account/provider defaults without overloading model storage.                                |
| `FR-ACCOUNT-ROUTER-002` | MUST  | Account blocks reference exactly one credential account ref such as `credential:openai:work`; account and load-balance blocks may fall back to any other block, including chains such as load-balancer -> account -> load-balancer. Fallback traversal rejects malformed account refs, cycles, router refs, and unknown accounts.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Static routing must fail at config time instead of during a turn.                                                                                                               |
| `FR-ACCOUNT-ROUTER-003` | MUST  | Load-balance blocks choose among account refs by `tokens_spent`, `closest_limit`, or `blind`; blind non-session choice refreshes every configured interval, defaulting to 60 seconds.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Operators need simple deterministic distribution before automatic job routing exists.                                                                                           |
| `FR-ACCOUNT-ROUTER-008` | MUST  | Branch blocks evaluate numeric conditions over account metrics and route to `then` or `else` block IDs. Conditions support greater-than, greater-than-or-equal, less-than, less-than-or-equal, equal, and not-equal comparisons. Expressions support numeric constants, account metrics (`rpm`, request counts, token counts, and limit pressure), and basic math operations (`add`, `subtract`, `multiply`, `divide`, and `modulo`).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Operators need to route based on account limits and derived thresholds.                                                                                                         |
| `FR-ACCOUNT-ROUTER-004` | MUST  | A session keeps its selected load-balance account until context compression or until that account is unavailable due to auth, billing, rate-limit, network, timeout, overload, or other classified provider failure.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Long conversations should not drift across accounts unless continuity or availability requires it.                                                                              |
| `FR-ACCOUNT-ROUTER-005` | MUST  | Router state persists per workspace with config hash, account health, token/request usage, block cursors, and session affinities; writes are atomic, stale sessions are pruned, removed accounts are pruned, cooldowns are reason-aware, and corrupt state files are preserved with a `.corrupt.<timestamp>` suffix before recovery.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Account health must survive restarts without pinning bad or stale state forever.                                                                                                |
| `FR-ACCOUNT-ROUTER-006` | MUST  | Agent execution treats router aliases as account-selection targets: initial selection supplies provider candidates, context compression can reselect, fallback results update router state, `/use` can switch to a router, and all account candidates are registered for rate limiting. A non-empty turn-scoped model ID is applied to every concrete candidate selected through the router; when it is absent, runnable account aliases retain their configured model and credential account refs use the provider default model.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Router behavior must compose with existing turns, fallbacks, model switching, and provider defaults without persisting a model on the router.                                   |
| `FR-ACCOUNT-ROUTER-007` | MUST  | Launcher Accounts management can add, edit, list, delete, and set an account router as default without storing API secrets or a model setting on the router entry; invalid router account references return validation errors. The Accounts page lists routers in an explicit Account Routers section, marks them with a route icon, shows a concise list of referenced accounts with current credential status, and exposes a visible Decision Graph edit action. The fullscreen create UI starts with no router block selected, prompts the user to add an account, load-balancer, or branch block, and never auto-creates the first account block. The fullscreen UI editor can create account/load-balancer/branch blocks, opens account/load-balancer/branch block editing in an adaptive modal that shrinks to simple block content and caps near fullscreen for larger block content, connect fallback and branch decision edges between blocks, show the full diagram as a draggable canvas, automatically stack the entry-to-fallback chain from top to bottom, pan the canvas, zoom by Shift+scroll or scale controls, and avoid exposing a separate Entry Block or model selector. Branch blocks expose a simple condition text editor with staged autocomplete and hints for account metrics, comparison operators, fixed numbers, and math functions; the editor completes account metric tokens as `accounts:<provider>:<account-name>.<metric>`, offers comparison operators after a complete left expression, and persists router account refs in the runtime `credential:<provider>:<id>` form. The raw JSON editor exposes the same account-only graph. Pico Chat lists routers separately, discovers selectable model IDs for the chosen router in its model control, reports per-account discovery failures without hiding IDs returned by responding accounts, and supplies the user's choice as a turn-scoped model ID without saving it on the router. | Browser setup must expose routers safely as account-only graphs without silently wiring a new router to an unintended block, coupling the graph to a model, or hiding usable Chat choices when one account is down. |

## Data And State Model

Router config shape:

```json
{
  "name": "router-main",
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
```

Runtime state persists under the agent workspace as
`account_router_state.json`. Each router stores a config hash, account status and
usage, session block affinities, block cursors, and timestamps.

## Auxiliary Interfaces

| Type    | Surface                                       | Contract                                                                                                                              | Requirement IDs                                         |
| ------- | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| Config  | `account_routers[]`                           | Router name, static account-only block graph, fallback refs, strategy, and refresh interval; no model field.                                      | `FR-ACCOUNT-ROUTER-001` through `FR-ACCOUNT-ROUTER-003` |
| Runtime | `pkg/accountrouter`                           | Select candidates, enforce session affinity, track health/usage, persist state, and recover corrupt state.                                       | `FR-ACCOUNT-ROUTER-003` through `FR-ACCOUNT-ROUTER-005` |
| Agent   | Agent model resolution and fallback execution | Build router account candidates, apply a turn-scoped model or account/provider default, select/reselect, and record results by account identity. | `FR-ACCOUNT-ROUTER-004`, `FR-ACCOUNT-ROUTER-006`        |
| HTTP/UI | `/api/accounts/models*`, `/accounts`          | Manage account-only router graphs and let Chat discover selectable model IDs without persisting a model on a router.                              | `FR-ACCOUNT-ROUTER-007`                                 |

## Algorithms And Ordering

1. Normalize incoming account-router payloads into `account_routers[]`, copying
   the alias into `name`, ignoring any legacy router `model` value, and clearing
   provider credential fields.
2. Validate block IDs, entry references, block types, load-balance strategy,
   duplicate load-balance account refs, fallback/branch refs, condition
   expressions, graph cycles, and account references against credential refs and
   runnable `model_list` account aliases.
3. When Pico Chat selects an account router, resolve its reachable accounts,
   discover selectable model IDs, report discovery failures per account, and
   send the chosen ID with the turn without persisting it on the router.
4. Build router accounts by resolving each `credential:` account ref into a
   runtime provider config using that credential. Apply a non-empty turn-scoped
   model ID to every candidate; otherwise credential refs use the provider
   default and runnable model-name refs retain their configured model. Runnable
   model-name refs resolve through existing model candidate resolution; router
   refs are rejected as upstream accounts.
5. For account blocks, use the account if operational; otherwise use fallback
   candidates when a fallback exists.
6. For load-balance blocks, filter to operational accounts, reuse session
   affinity unless compression or unavailability allows reselection, then choose
   by tokens spent, RPM pressure, or blind session hash / interval cursor.
7. For branch blocks, evaluate the configured account metric/math comparison and
   expand either the `then` or `else` block before its fallback path.
8. Execute provider fallback normally and record every classified failed attempt
   against the stable account identity, then mark the successful account
   operational and increment usage.
9. Persist state after selection or result recording, pruning stale sessions and
   removed accounts and resetting incompatible config hashes.

## Cross-Feature Behavior

Agent conversations own the normal turn loop and provider prompt behavior.
Account routers only provide the candidate selection layer. Launcher management
owns the authenticated browser/API surface that creates and edits router entries
on the Accounts page. Security isolation continues to own secure string
semantics; router entries intentionally do not store API keys.

## Failure And Edge Cases

- Missing `router`, disabled router config, empty entry, missing blocks, unknown
  block types, unsupported strategies, invalid branch expressions, duplicate
  load-balance account refs, and graph cycles are rejected.
- Unknown model-name refs, router refs, malformed credential refs, or ambiguous
  duplicate account references are rejected after the full config is known.
- Account status cooldowns differ by failure class: auth/billing failures stay
  unavailable longer than rate-limit or transient network failures.
- Accounts using the same provider and turn-scoped or default model remain
  distinguishable by stable identity during selection and failure attribution,
  so each request uses the chosen account's credentials.
- Corrupt state is renamed aside and a fresh state file is written.

## Acceptance Evidence

| Requirement IDs                                                                                    | Evidence                                                                                                                                                                                                                                                                         |
| -------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-ACCOUNT-ROUTER-001`, `FR-ACCOUNT-ROUTER-002`, `FR-ACCOUNT-ROUTER-003`, `FR-ACCOUNT-ROUTER-008` | [pkg/config/account_router_test.go](../../pkg/config/account_router_test.go)                                                                                                                                                                                                     |
| `FR-ACCOUNT-ROUTER-003`, `FR-ACCOUNT-ROUTER-008`, `FR-ACCOUNT-ROUTER-004`, `FR-ACCOUNT-ROUTER-005` | [pkg/accountrouter/router_test.go](../../pkg/accountrouter/router_test.go)                                                                                                                                                                                                       |
| `FR-ACCOUNT-ROUTER-006`                                                                            | [pkg/agent/account_router_test.go](../../pkg/agent/account_router_test.go), [pkg/providers/fallback_test.go](../../pkg/providers/fallback_test.go)                                                                                                                               |
| `FR-ACCOUNT-ROUTER-007`                                                                            | [web/backend/api/models_test.go](../../web/backend/api/models_test.go), [web/frontend/src/components/models/model-card.test.tsx](../../web/frontend/src/components/models/model-card.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |

## Implementation Anchors

- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/accountrouter](../../pkg/accountrouter)
- [pkg/agent/instance.go](../../pkg/agent/instance.go)
- [pkg/agent/pipeline_setup.go](../../pkg/agent/pipeline_setup.go)
- [pkg/agent/pipeline_llm.go](../../pkg/agent/pipeline_llm.go)
- [pkg/providers/fallback.go](../../pkg/providers/fallback.go)
- [web/backend/api/models.go](../../web/backend/api/models.go)
- [web/frontend/src/components/credentials](../../web/frontend/src/components/credentials)

## Surface Ownership

```text
Owns: CODE pkg/accountrouter/**
Owns: CODE pkg/agent/fallback_result.go
Owns: CONFIG.account_routers*
Owns: TEST pkg/accountrouter/**
Owns: TEST pkg/config/account_router_test.go*
```
