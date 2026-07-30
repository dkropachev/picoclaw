# Routing And Multi-Agent Dispatch

## Feature ID

`FR-ROUTE`

## Behavior Summary

PicoClaw routes inbound messages to agents, aligns session dimensions with the
matched route, and selects light or primary model candidates according to
message complexity.

## Reconstruction Notes

- Similarity target: recreate route resolution, default agent fallback, session policy handoff, identity-link sender canonicalization, and light/heavy model routing.
- Core types/functions: route resolver, dispatch view, selector matching, session policy, feature extractor, rule classifier, and router.
- Runtime ordering: normalize inbound fields, build dispatch view, scan rules in order, validate agent target, hand session policy to allocator, score complexity, select model candidate.
- Non-obvious constraints: empty rules are skipped, first match wins, invalid dimensions are dropped, attachments force high complexity, and explicit session keys can override route-derived keys later.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-ROUTE-001` | MUST | Dispatch rules match normalized channel, account, space, chat, topic, sender, and mentioned fields with first-match-wins ordering. | Routing must be deterministic. |
| `FR-ROUTE-002` | MUST | Runtime-targetable agent IDs use the exact canonical grammar `[a-z0-9][a-z0-9_-]{0,63}` through one shared validator. Invalid dispatch targets fall back to default agent selection, while authoring catalogs omit invalid configured IDs instead of emitting targets that routing cannot resolve. | Bad config should not drop messages or produce unusable authoring targets. |
| `FR-ROUTE-003` | MUST | Default agent selection uses explicit default, then first configured agent, then implicit `main`. | Empty/simple configs need stable behavior. |
| `FR-ROUTE-004` | MUST | Matched dispatch rules can override session dimensions before session allocation. | Routing and history isolation must stay aligned. |
| `FR-ROUTE-005` | MUST | Identity links canonicalize sender matching and session identity consistently. | Same user identities should route and persist together. |
| `FR-ROUTE-006` | MUST | Model routing computes structural complexity and selects light model below threshold when enabled and available. | Cost-saving model selection must be predictable. |
| `FR-ROUTE-007` | SHOULD | Code blocks, attachments, long prompts, tool-call-heavy history, and deep conversations increase complexity. | Complex turns should avoid weak models. |
| `FR-ROUTE-008` | MUST | Agent management preserves configured order, accepts only unique canonical IDs, resolves the default as explicit default then first configured agent then implicit `main`, and validates explicit delegation targets against the post-mutation agent set. Wildcard delegation means all other agents and never permits self-targeting. Deletion is blocked only by direct dispatch targets or another agent's explicit delegation allowlist; wildcard delegation is not a blocker. | Browser-managed policy must remain targetable and deterministic without treating dynamic workflow references or wildcard delegation as permanent ownership or allowing recursive self-spawn. |

## Data And State Model

Routing state includes configured agent list/defaults, dispatch rules, selector
fields, identity link maps, session dimensions, structural feature vectors,
classifier score, and selected model name.

## Surface Ownership

Owns: CODE pkg/routing/**
Owns: CONFIG.routing*
Owns: TEST pkg/routing/*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `agents.dispatch.*`, `session.identity_links`, `agents.defaults.routing.*`, `routing.*` | Dispatch, session handoff, and model routing policy. | `FR-ROUTE-001` through `FR-ROUTE-007` |
| Runtime | Route resolver and router | Agent dispatch and model candidate selection. | `FR-ROUTE-001`, `FR-ROUTE-006` |
| HTTP/UI | Agent management API and UI | Preserve canonical target IDs, configured order, default priority, delegation references, and direct-reference deletion blockers. | `FR-ROUTE-003`, `FR-ROUTE-008` |

## Algorithms And Ordering

1. Normalize inbound channel/account/scope/sender fields.
2. Build a dispatch view and scan rules top to bottom.
3. Return the first rule whose non-empty selectors all match exactly.
4. Resolve default agent when no rule matches or the target is invalid.
5. Extract complexity features and compare classifier score with threshold.

## Cross-Feature Behavior

Chat channels provide normalized inbound context. Session memory receives the
selected session policy. Agent conversations use the selected agent and model
candidates.

## Failure And Edge Cases

- Rules with no constraints are skipped.
- Unknown dimensions are dropped.
- Light model is ignored when routing is disabled or candidate resolution lacks the model.
- Attachments force primary model at default scoring.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-ROUTE-001`, `FR-ROUTE-002`, `FR-ROUTE-003`, `FR-ROUTE-004`, `FR-ROUTE-005` | [pkg/routing/route_test.go](../../pkg/routing/route_test.go), [pkg/routing/agent_id_test.go](../../pkg/routing/agent_id_test.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [docs/architecture/routing-system.md](../architecture/routing-system.md) |
| `FR-ROUTE-006`, `FR-ROUTE-007` | [pkg/routing/router_test.go](../../pkg/routing/router_test.go), [pkg/routing/features.go](../../pkg/routing/features.go) |
| `FR-ROUTE-008` | [web/backend/api/agents_test.go](../../web/backend/api/agents_test.go), [pkg/agent/registry_test.go](../../pkg/agent/registry_test.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/tools/spawn_test.go](../../pkg/tools/spawn_test.go) |

## Implementation Anchors

- [pkg/routing/route.go](../../pkg/routing/route.go)
- [pkg/routing/agent_id.go](../../pkg/routing/agent_id.go)
- [pkg/routing/router.go](../../pkg/routing/router.go)
- [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go)
