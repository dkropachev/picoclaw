# Session Memory And History

## Feature ID

`FR-SESSION`

## Behavior Summary

PicoClaw persists conversation state by routed session scope. Session behavior
defines how chat/user/topic dimensions become keys, how JSONL history is stored,
how legacy aliases are promoted, and how launcher history views expose records.

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

## Auxiliary Interfaces

Owns: CONFIG.session*
Owns: HTTP * /api/sessions*
Owns: TEST pkg/session/*
Owns: TEST pkg/memory/*
Owns: TEST pkg/identity/*
Owns: TEST pkg/state/*
Owns: TEST web/backend/api/session*

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | `session.dimensions`, `session.identity_links`, legacy `dm_scope` | Session isolation policy and compatibility input. | `FR-SESSION-001`, `FR-SESSION-003` |
| HTTP | `GET /api/sessions`, `GET /api/sessions/{id}`, `DELETE /api/sessions/{id}` | Launcher history list/detail/delete behavior. | `FR-SESSION-006` |
| Storage | Workspace session JSONL files and metadata | Durable conversation messages, summaries, and aliases. | `FR-SESSION-004`, `FR-SESSION-005` |

## Cross-Feature Behavior

Routing supplies the session policy. Agent conversations read and write session
history. Chat channels provide normalized scope values. Launcher management
exposes the history surface.

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
