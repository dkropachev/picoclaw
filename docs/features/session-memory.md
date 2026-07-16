# Session Memory And History

## Feature ID

`FR-SESSION`

## Behavior Summary

PicoClaw persists conversation state by routed session scope. Session behavior
defines how chat/user/topic dimensions become keys, how JSONL history is stored,
how legacy aliases are promoted, and how launcher history views expose records.

## Reconstruction Notes

- Similarity target: recreate scoped session allocation, canonical key generation, JSONL history backend, legacy alias promotion, and launcher history endpoints.
- Core types/functions: `SessionScope`, route session allocator, canonical key helpers, JSONL backend, memory store, and session API handlers.
- Runtime ordering: normalize route policy, derive dimensions, canonicalize identity, create metadata, promote aliases only when safe, append/read messages, expose list/detail/delete.
- Non-obvious constraints: invalid dimensions are dropped, corrupt JSONL lines are skipped, existing canonical history is never overwritten by alias promotion.

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

## Data And State Model

Persistent state is session JSONL message files plus metadata containing scoped
identity and aliases. Runtime state includes allocated scope fields, canonical
keys, legacy aliases, summaries, and per-session append locks.

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
| Frontend | Logs and session history UI under `web/frontend/src/components/logs/**`, `web/frontend/src/hooks/use-session-history.ts`, and `web/frontend/src/routes/logs.tsx` | Browser history and log surfaces expose session records and follow shared frontend API, token, and dynamic-style lint rules. | `FR-SESSION-006` |

## Algorithms And Ordering

1. Convert inbound context and route policy into normalized scope dimensions.
2. Build canonical key from agent and selected dimensions.
3. Create metadata and promote legacy alias history only when canonical history is empty.
4. Append messages in JSONL order under per-session synchronization.
5. Read history by skipping corrupt lines and applying summary/compaction policy.

## Cross-Feature Behavior

Routing supplies the session policy. Agent conversations read and write session
history. Chat channels provide normalized scope values. Launcher management
exposes the history surface. Threads store discoverable thread records and
handoff links on top of session metadata without deleting the underlying
conversation history.

## Failure And Edge Cases

- Invalid or duplicate configured dimensions are ignored.
- Missing metadata does not prevent reading a valid session body.
- Delete handles JSONL and legacy JSON sessions.
- Large sessions are summarized or compacted rather than loaded without bound.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SESSION-001`, `FR-SESSION-002`, `FR-SESSION-003`, `FR-SESSION-007` | [pkg/session/allocator_test.go](../../pkg/session/allocator_test.go), [pkg/session/key_test.go](../../pkg/session/key_test.go), [docs/architecture/session-system.md](../architecture/session-system.md) |
| `FR-SESSION-004`, `FR-SESSION-005` | [pkg/session/jsonl_backend_test.go](../../pkg/session/jsonl_backend_test.go), [pkg/agent/context_budget_test.go](../../pkg/agent/context_budget_test.go), [pkg/agent/context_cache_test.go](../../pkg/agent/context_cache_test.go) |
| `FR-SESSION-006` | [web/backend/api/session_test.go](../../web/backend/api/session_test.go) |

## Implementation Anchors

- [pkg/session/allocator.go](../../pkg/session/allocator.go)
- [pkg/session/jsonl_backend.go](../../pkg/session/jsonl_backend.go)
- [web/backend/api/session.go](../../web/backend/api/session.go)
