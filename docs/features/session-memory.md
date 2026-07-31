# Session Memory And History

## Feature ID

`FR-SESSION`

## Behavior Summary

PicoClaw persists conversation state by routed session scope. Session behavior
defines how chat/user/topic dimensions become keys, how JSONL history is stored,
how legacy session aliases are promoted, how alias-valued model metadata is
stored and exposed, and how launcher history views accept optional runtime
account-selection metadata.

## Reconstruction Notes

- Similarity target: recreate scoped session allocation, canonical key generation, JSONL history backend, legacy alias promotion, and launcher history endpoints.
- Core types/functions: `SessionScope`, route session allocator, canonical key helpers, JSONL backend, memory store, and session API handlers.
- Runtime ordering: normalize route policy, derive dimensions, canonicalize identity, create metadata, promote aliases only when safe, append/read messages, expose list/detail/delete.
- Non-obvious constraints: invalid dimensions are dropped, corrupt JSONL lines
  are skipped, existing canonical history is never overwritten by alias
  promotion, and `model_name` denotes a configured alias or model router rather
  than a raw upstream model ID.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SESSION-001` | MUST | Session scope is allocated from route policy and inbound context using supported dimensions: space, chat, topic, and sender. | Conversation isolation must be predictable. |
| `FR-SESSION-002` | MUST | Canonical session keys include routed agent identity and normalized dimension values. | Multi-agent and multi-channel history must not collide. |
| `FR-SESSION-003` | MUST | Legacy aliases remain readable and can promote history into an empty canonical session without overwriting existing canonical history. | Upgrades must preserve user history. |
| `FR-SESSION-004` | MUST | JSONL storage appends messages atomically per session and skips corrupt lines while reading remaining history. | Durable history should survive partial writes. |
| `FR-SESSION-005` | MUST | Session summaries and compaction preserve enough context for future turns while respecting configured thresholds. | Long sessions need bounded context. |
| `FR-SESSION-006` | MUST | Launcher session APIs list, fetch, and delete session history without exposing unrelated workspace files. | History management is a user-facing launcher capability. |
| `FR-SESSION-007` | SHOULD | Explicit session keys supplied by trusted callers are preserved when compatible with canonical or legacy formats. | Tests, direct calls, and compatibility flows need determinism. |
| `FR-SESSION-008` | MUST | An inbound Pico chat selection is the pair `account_ref` plus alias-valued `model_name`: the account reference may identify a concrete account or account router, and the model name may identify an exact configured alias or model router. Pico input normalizes and forwards both fields into turn resolution. Stored provider messages and current backend live/history projections preserve only alias-valued `model_name`; they do not persist or emit `account_ref`. Frontend live/history parsers accept an optional `account_ref` and include it in message reconciliation whenever a server supplies one, while remaining compatible with its absence. No selection field carries the resolved upstream provider model ID. | Runtime account selection and durable alias identity have different lifetimes; the wire contract must not imply persistence that the backend does not provide or leak concrete model resolution. |

## Data And State Model

Persistent state is session JSONL message files plus metadata containing scoped
identity and aliases. Runtime state includes allocated scope fields, canonical
keys, legacy aliases, summaries, structured message text/media parts, and
per-session append locks. Optional message selection metadata stores
alias-valued `model_name`; inbound `account_ref` remains transient turn metadata
and is not part of the stored provider message. The concrete upstream model
used by the provider is not session selection state.

## Surface Ownership

Owns: CODE pkg/agent/memory/**
Owns: CODE pkg/agent/sessions/**
Owns: CODE pkg/agent/state/**
Owns: CODE pkg/identity/**
Owns: CODE pkg/memory/**
Owns: CODE pkg/seahorse/**
Owns: CODE pkg/session/**
Owns: CODE pkg/state/**
Owns: CODE web/backend/api/session.go
Owns: CODE web/frontend/src/api/sessions.ts
Owns: CODE web/frontend/src/components/logs/**
Owns: CODE web/frontend/src/hooks/use-session-history.ts
Owns: CODE web/frontend/src/routes/logs.tsx
Owns: CONFIG.session*
Owns: HTTP * /api/sessions*
Owns: TEST pkg/session/*
Owns: TEST pkg/memory/*
Owns: TEST pkg/identity/*
Owns: TEST pkg/state/*
Owns: TEST web/backend/api/session*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `session.dimensions`, `session.identity_links`, legacy `dm_scope` | Session isolation policy and compatibility input. | `FR-SESSION-001`, `FR-SESSION-003` |
| HTTP | `GET /api/sessions`, `GET /api/sessions/{id}`, `DELETE /api/sessions/{id}` | Launcher history list/detail/delete behavior. | `FR-SESSION-006` |
| Storage | Workspace session JSONL files and metadata | Durable conversation messages, summaries, and aliases. | `FR-SESSION-004`, `FR-SESSION-005` |
| Chat/history wire | Inbound Pico payloads and frontend `SessionDetail.messages[]` | Inbound requests carry optional `account_ref` plus alias-valued `model_name`; stored/backend-projected messages preserve `model_name`, and frontend projections tolerate optional `account_ref` without requiring it. | `FR-SESSION-008` |
| Frontend | Logs and session history UI under `web/frontend/src/components/logs/**`, `web/frontend/src/hooks/use-session-history.ts`, and `web/frontend/src/routes/logs.tsx` | Browser history and log surfaces expose session records and follow shared frontend API, token, and dynamic-style lint rules. | `FR-SESSION-006` |

## Algorithms And Ordering

1. Convert inbound context and route policy into normalized scope dimensions.
2. Build canonical key from agent and selected dimensions.
3. Create metadata and promote legacy alias history only when canonical history is empty.
4. Append messages in JSONL order under per-session synchronization.
5. Normalize the inbound account reference and selected alias for turn
   resolution, then persist/project the alias as `model_name`; keep
   `account_ref` transient and the resolved concrete provider model internal to
   execution.
6. Read history by skipping corrupt lines and applying summary/compaction policy,
   then reconcile browser messages using `model_name` and any optional
   `account_ref` a response actually supplies.

## Cross-Feature Behavior

Routing supplies the session policy. Agent conversations read and write session
history, including structured prompt parts when provider history supplies them.
Chat channels provide normalized scope values. Launcher management exposes the
history surface. Threads store discoverable thread records and handoff links on
top of session metadata without deleting the underlying conversation history.
Account and model routing consume the inbound selection pair. History preserves
the alias name, not the account reference or concrete provider-resolution
result.

## Failure And Edge Cases

- Invalid or duplicate configured dimensions are ignored.
- Missing metadata does not prevent reading a valid session body.
- Delete handles JSONL and legacy JSON sessions.
- Missing optional selection metadata remains readable for legacy messages.
  On inbound chat, a raw `model` field is ignored as selection authority; only
  `account_ref` and alias-valued `model_name` participate in turn selection.
  Browser message identity uses either optional selection field only when that
  field is present in the received payload.
- Large sessions are summarized or compacted rather than loaded without bound.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SESSION-001`, `FR-SESSION-002`, `FR-SESSION-003`, `FR-SESSION-007` | [pkg/session/allocator_test.go](../../pkg/session/allocator_test.go), [pkg/session/key_test.go](../../pkg/session/key_test.go), [docs/architecture/session-system.md](../architecture/session-system.md) |
| `FR-SESSION-004`, `FR-SESSION-005` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/agent/context_budget_test.go](../../pkg/agent/context_budget_test.go), [pkg/agent/context_cache_test.go](../../pkg/agent/context_cache_test.go) |
| `FR-SESSION-006` | [web/backend/api/session_test.go](../../web/backend/api/session_test.go) |
| `FR-SESSION-008` | [pkg/channels/pico/pico_test.go](../../pkg/channels/pico/pico_test.go), [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [web/frontend/src/features/chat/protocol.test.ts](../../web/frontend/src/features/chat/protocol.test.ts), [web/frontend/src/features/chat/history.ts](../../web/frontend/src/features/chat/history.ts), [web/frontend/src/api/sessions.ts](../../web/frontend/src/api/sessions.ts) |

## Implementation Anchors

- [pkg/session/allocator.go](../../pkg/session/allocator.go)
- [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go)
- [web/backend/api/session.go](../../web/backend/api/session.go)
