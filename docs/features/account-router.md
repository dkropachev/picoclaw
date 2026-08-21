# Account Router

## Feature ID

`FR-ACCOUNT-ROUTER`

## Behavior Summary

Account routers select one or more accounts through a static block graph. They
do not own a model ID and are configured as first-class `account_routers[]`
entries, not as fake `model_list` rows. The launcher Accounts surface exposes
both a UI editor for connecting primitive blocks/accounts and a raw JSON graph
editor. Every caller selects the router as `account_ref` and independently
selects an exact model alias.

## Reconstruction Notes

- Similarity target: recreate a config-backed account router with workflow-like
  block IDs and fallback edges.
- Core types/functions: `AccountRouterConfig`, `AccountRouterBlock`,
  `accountrouter.Router`, persistent router state store, agent candidate
  selection, fallback result recording, and launcher model handlers.
- Runtime ordering: validate router graph and account references, select a
  concrete account, resolve the requested alias to its base model or that
  concrete account's override, execute normal provider fallback, record
  success/failure/usage, redact private-execution provider errors before state
  persistence, and persist updated route state atomically.
- Non-obvious constraints: routers cannot reference other routers, load balance
  does not reshuffle an active session unless context compression occurs or the
  chosen account becomes unavailable, and failed attempts must be attributed by
  stable account identity even when two accounts use the same provider and
  model ID.

## Requirements

| ID                      | Level | Requirement                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Rationale                                                                                                                                                                       |
| ----------------------- | ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FR-ACCOUNT-ROUTER-001` | MUST  | An account router has a name, enabled flag, entry block, refresh interval, and typed account/load-balance blocks, but no model setting. Account routers are persisted under `account_routers[]`; `model_list` remains limited to concrete provider account configs and must not persist router rows. Runtime router resolution must accept `credential:<provider>:<id>` account references directly, without requiring duplicate account rows in `model_list`. | Users should manage account routing independently from per-turn model selection without overloading account storage. |
| `FR-ACCOUNT-ROUTER-002` | MUST  | Account blocks reference exactly one credential account ref such as `credential:openai:work`; account and load-balance blocks may fall back to any other block, including chains such as load-balancer -> account -> load-balancer. Fallback traversal rejects malformed account refs, cycles, router refs, and unknown accounts.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Static routing must fail at config time instead of during a turn.                                                                                                               |
| `FR-ACCOUNT-ROUTER-003` | MUST  | Load-balance blocks choose among account refs by `tokens_spent`, `closest_limit`, or `blind`; blind non-session choice refreshes every configured interval, defaulting to 60 seconds.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Operators need simple deterministic distribution before automatic job routing exists.                                                                                           |
| `FR-ACCOUNT-ROUTER-008` | MUST  | Branch blocks evaluate numeric conditions over account metrics and route to `then` or `else` block IDs. Conditions support greater-than, greater-than-or-equal, less-than, less-than-or-equal, equal, and not-equal comparisons. Expressions support numeric constants, account metrics (`rpm`, request counts, token counts, and limit pressure), and basic math operations (`add`, `subtract`, `multiply`, `divide`, and `modulo`).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Operators need to route based on account limits and derived thresholds.                                                                                                         |
| `FR-ACCOUNT-ROUTER-004` | MUST  | A session keeps its selected load-balance account until context compression or until that account is unavailable due to auth, billing, rate-limit, network, timeout, overload, or other classified provider failure. An explicit provider safety-filter/refusal result is request-local: fallback may try another configured candidate, but router health and cooldown state do not mark that account unavailable.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Long conversations should not drift across accounts because one request triggered a provider policy decision.                                                                    |
| `FR-ACCOUNT-ROUTER-005` | MUST  | Router state persists per workspace with config hash, account health, token/request usage, block cursors, and session affinities; writes are atomic, stale sessions are pruned, removed accounts are pruned, cooldowns are reason-aware, and corrupt state files are preserved with a `.corrupt.<timestamp>` suffix before recovery.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Account health must survive restarts without pinning bad or stale state forever.                                                                                                |
| `FR-ACCOUNT-ROUTER-006` | MUST  | Agent execution treats router names only as account-selection targets. For each selected concrete account, it resolves the independently selected exact model alias, applying a per-account override only when that override key names the concrete account. Empty aliases fail with `no model configured`; raw model IDs and provider defaults are never substituted. Initial selection, context-compression reselection, fallback accounting, `/use`, and rate limiting retain the concrete account identity. | Router behavior must compose with turns and fallbacks without owning, guessing, or defaulting a model. |
| `FR-ACCOUNT-ROUTER-007` | MUST  | Launcher Accounts management can add, edit, list, delete, and select an account router as `account_ref` without storing API secrets or a model setting on it; invalid account references return validation errors. The Accounts page and graph editor keep account/load-balancer/branch blocks account-only. Pico Chat lists routers separately from concrete accounts and pairs either choice with an exact configured model alias from the independent model selector. | Browser setup must expose routers safely without coupling an account graph to a model or raw upstream model discovery. |
| `FR-ACCOUNT-ROUTER-009` | MUST  | Private agent execution records provider fallback outcomes through the router's private-result path. That path preserves stable account attribution, classified failure reason, health/cooldown transitions, request/token accounting, and success recovery, but replaces every persisted upstream or wrapper error with the fixed `provider request failed` text. Ordinary non-private result recording retains its existing diagnostics. | A provider error can echo compiler-private workflow context or frozen media; router health must remain accurate without turning shared durable state into an exfiltration surface. |

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
| Runtime | `pkg/accountrouter`                           | Select candidates, enforce session affinity, track health/usage, redact private provider errors, persist state, and recover corrupt state.                                       | `FR-ACCOUNT-ROUTER-003` through `FR-ACCOUNT-ROUTER-005`, `FR-ACCOUNT-ROUTER-009` |
| Agent   | Agent model resolution and fallback execution | Select/reselect concrete accounts, resolve the exact alias for each selected account, and record results by account identity. | `FR-ACCOUNT-ROUTER-004`, `FR-ACCOUNT-ROUTER-006`        |
| HTTP/UI | `/api/accounts/models*`, `/accounts`          | Manage account-only router graphs and select an independent model alias without persisting one on a router. | `FR-ACCOUNT-ROUTER-007`                                 |

## Algorithms And Ordering

1. Normalize incoming account-router payloads into `account_routers[]`, copying
   the alias into `name`, ignoring any legacy router `model` value, and clearing
   provider credential fields.
2. Validate block IDs, entry references, block types, load-balance strategy,
   duplicate load-balance account refs, fallback/branch refs, condition
   expressions, graph cycles, and account references against credential refs and
   runnable `model_list` account aliases.
3. Receive the router name as `account_ref` and an independently configured
   exact alias as `model_name`; reject a missing alias before provider setup.
4. Resolve each `credential:` or `model_list` account ref to a concrete account.
   After the graph selects that account, resolve the alias base model or its
   override for that exact concrete account. Never look for an override keyed by
   the router name and never derive a provider model.
5. For account blocks, use the account if operational; otherwise use fallback
   candidates when a fallback exists.
6. For load-balance blocks, filter to operational accounts, reuse session
   affinity unless compression or unavailability allows reselection, then choose
   by tokens spent, RPM pressure, or blind session hash / interval cursor.
7. For branch blocks, evaluate the configured account metric/math comparison and
   expand either the `then` or `else` block before its fallback path.
8. Execute provider fallback normally and record every classified failed attempt
   against the stable account identity, then mark the successful account
   operational and increment usage. When the caller marks the execution
   private, keep those classifications and counters but replace persisted
   provider error detail with one fixed message before writing shared state.
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
- Unknown account refs, router refs, malformed credential refs, or ambiguous
  duplicate account references are rejected after the full config is known.
- Account status cooldowns differ by failure class: auth/billing failures stay
  unavailable longer than rate-limit or transient network failures.
- Private fallback accounting preserves those cooldown classes while persisting
  only the fixed private failure text; raw provider errors never enter router
  state.
- Accounts using the same provider and resolved alias model remain
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
| `FR-ACCOUNT-ROUTER-009`                                                                            | [pkg/accountrouter/router_test.go](../../pkg/accountrouter/router_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go)                                                                                                                        |

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
