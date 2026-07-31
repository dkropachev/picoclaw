# Routing And Multi-Agent Dispatch

## Feature ID

`FR-ROUTE`

## Behavior Summary

PicoClaw routes inbound messages to agents, aligns session dimensions with the
matched route, and selects light or primary model-alias candidates according to
message complexity. Provider execution receives a candidate resolved from an
explicit account reference plus that alias, never an implicit provider model.

## Reconstruction Notes

- Similarity target: recreate route resolution, default agent fallback, session policy handoff, identity-link sender canonicalization, and light/heavy model routing.
- Core types/functions: route resolver, dispatch view, selector matching, session policy, feature extractor, rule classifier, and router.
- Runtime ordering: normalize inbound fields, build dispatch view, scan rules in
  order, validate agent target, hand session policy to allocator, select a
  concrete account, score complexity, select an exact alias, and resolve that
  account-and-alias candidate.
- Non-obvious constraints: empty rules are skipped, first match wins, invalid
  dimensions are dropped, attachments force high complexity, explicit session
  keys can override route-derived keys later, and model routing never fills a
  blank selection from provider defaults.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-ROUTE-001` | MUST | Dispatch rules match normalized channel, account, space, chat, topic, sender, and mentioned fields with first-match-wins ordering. | Routing must be deterministic. |
| `FR-ROUTE-002` | MUST | Runtime-targetable agent IDs use the exact canonical grammar `[a-z0-9][a-z0-9_-]{0,63}` through one shared validator. Invalid dispatch targets fall back to default agent selection, while authoring catalogs omit invalid configured IDs instead of emitting targets that routing cannot resolve. | Bad config should not drop messages or produce unusable authoring targets. |
| `FR-ROUTE-003` | MUST | Default agent selection uses explicit default, then first configured agent, then implicit `main`. | Empty/simple configs need stable behavior. |
| `FR-ROUTE-004` | MUST | Matched dispatch rules can override session dimensions before session allocation. | Routing and history isolation must stay aligned. |
| `FR-ROUTE-005` | MUST | Identity links canonicalize sender matching and session identity consistently. | Same user identities should route and persist together. |
| `FR-ROUTE-006` | MUST | Model routing computes structural complexity and selects the configured exact light model alias below threshold when enabled and available; otherwise it retains the exact primary alias. | Cost-saving model selection must be predictable without exposing raw provider model IDs as policy. |
| `FR-ROUTE-007` | SHOULD | Code blocks, attachments, long prompts, tool-call-heavy history, and deep conversations increase complexity. | Complex turns should avoid weak models. |
| `FR-ROUTE-008` | MUST | Agent management preserves configured order, accepts only unique canonical IDs, resolves the default as explicit default then first configured agent then implicit `main`, and validates explicit delegation targets against the post-mutation agent set. Wildcard delegation means all other agents and never permits self-targeting. Deletion is blocked only by direct dispatch targets or another agent's explicit delegation allowlist; wildcard delegation is not a blocker. | Browser-managed policy must remain targetable and deterministic without treating dynamic workflow references or wildcard delegation as permanent ownership or allowing recursive self-spawn. |
| `FR-ROUTE-009` | MUST | Agent detail deep links accept one exact canonical agent ID and one allow-listed tab value. Invalid or repeated search values are removed without trimming into another identity, a valid missing agent remains an explicit not-found selection, tab/history navigation is reversible, and runtime activity cursors or payload state never enter the URL. | A shareable management URL must not normalize to the wrong agent, silently retarget after deletion, or make ephemeral runtime authority browser-persistent. |
| `FR-ROUTE-010` | MUST | Runtime model candidates are identified by the pair of an explicit account reference and exact model alias. A direct account resolves the alias base mapping or its concrete-account override; an account router first selects a reachable concrete account and only then applies that account's alias override; a model router selects another configured alias rather than a raw model. Primary, light, fallback, side-question, workflow, hook-rewrite, and subagent paths preserve this contract. Missing account/alias state or an unrunnable pairing fails locally, including `no model configured` for an absent alias, and never asks a provider for its default model. | Account failover and model choice are independent policies; combining them only at resolution prevents an account router or blank model from silently choosing an unintended upstream model. |

## Data And State Model

Routing state includes configured agent list/defaults, dispatch rules, selector
fields, identity link maps, session dimensions, structural feature vectors,
classifier score, selected account reference, selected exact model alias, and
runtime candidates whose stable identity binds the concrete account and alias.

## Surface Ownership

Owns: CODE pkg/routing/**
Owns: CONFIG.routing*
Owns: TEST pkg/routing/*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `agents.dispatch.*`, `session.identity_links`, `agents.defaults.routing.*`, `routing.*` | Dispatch, session handoff, and model routing policy. | `FR-ROUTE-001` through `FR-ROUTE-007` |
| Runtime | Route resolver, complexity router, account router, and candidate resolver | Agent dispatch, exact primary/light alias choice, concrete account choice, per-account alias resolution, and account-plus-alias candidate identity. | `FR-ROUTE-001`, `FR-ROUTE-006`, `FR-ROUTE-010` |
| HTTP/UI | Agent management API and UI | Preserve canonical target IDs, configured order, default priority, delegation references, direct-reference deletion blockers, and exact selected-agent tab deep links. | `FR-ROUTE-003`, `FR-ROUTE-008`, `FR-ROUTE-009` |

## Algorithms And Ordering

1. Normalize inbound channel/account/scope/sender fields.
2. Build a dispatch view and scan rules top to bottom.
3. Return the first rule whose non-empty selectors all match exactly.
4. Resolve default agent when no rule matches or the target is invalid.
5. Resolve the explicit account reference to a direct account or let the
   account router choose one reachable concrete account.
6. Extract complexity features, compare the classifier score with the
   threshold, and select the exact primary or light alias.
7. Resolve that alias's concrete-account override or base mapping, validate it
   against the selected provider, and construct a candidate whose stable
   identity contains both the account and alias.

## Cross-Feature Behavior

Chat channels provide normalized inbound context. Session memory receives the
selected session policy. Agent conversations use the selected agent and model
candidates. Account routing chooses credential/provider identity independently;
model routing chooses only aliases, and the agent runtime composes the two
selections before provider execution.

## Failure And Edge Cases

- Rules with no constraints are skipped.
- Unknown dimensions are dropped.
- The light alias is ignored when routing is disabled or candidate resolution
  lacks that configured alias.
- A missing alias returns `no model configured`; an account router with no
  runnable account-and-alias candidate fails without invoking a bootstrap or
  provider-default model.
- Attachments force primary model at default scoring.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-ROUTE-001`, `FR-ROUTE-002`, `FR-ROUTE-003`, `FR-ROUTE-004`, `FR-ROUTE-005` | [pkg/routing/route_test.go](../../pkg/routing/route_test.go), [pkg/routing/agent_id_test.go](../../pkg/routing/agent_id_test.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [docs/architecture/routing-system.md](../architecture/routing-system.md) |
| `FR-ROUTE-006`, `FR-ROUTE-007` | [pkg/routing/router_test.go](../../pkg/routing/router_test.go), [pkg/routing/features.go](../../pkg/routing/features.go) |
| `FR-ROUTE-008` | [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [pkg/agent/registry_test.go](../../pkg/agent/registry_test.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/tools/spawn_test.go](../../pkg/tools/spawn_test.go) |
| `FR-ROUTE-009` | [web/frontend/src/components/agent/agents/agent-route-search.test.ts](../../web/frontend/src/components/agent/agents/agent-route-search.test.ts), [web/frontend/src/routes/agent/-agents-route.test.tsx](../../web/frontend/src/routes/agent/-agents-route.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-ROUTE-010` | [pkg/agent/account_router_test.go](../../pkg/agent/account_router_test.go), [pkg/agent/alias_runtime_test.go](../../pkg/agent/alias_runtime_test.go), [pkg/agent/model_resolution_test.go](../../pkg/agent/model_resolution_test.go), [pkg/config/model_selection_test.go](../../pkg/config/model_selection_test.go), [docs/architecture/routing-system.md](../architecture/routing-system.md) |

## Implementation Anchors

- [pkg/routing/route.go](../../pkg/routing/route.go)
- [pkg/routing/agent_id.go](../../pkg/routing/agent_id.go)
- [pkg/routing/router.go](../../pkg/routing/router.go)
- [pkg/agent/account_alias_resolution.go](../../pkg/agent/account_alias_resolution.go)
- [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go)
