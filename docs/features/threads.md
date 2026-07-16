# Threads And Handoffs

## Feature ID

`FR-THREADS`

## Behavior Summary

PicoClaw threads are discoverable, full-window work contexts backed by session
metadata. They let a normal chat become a named thread, attach to an existing
thread, switch the UI to thread work, return to the origin session, and hide
dropped threads from discovery while preserving durable records.

## Reconstruction Notes

- Similarity target: recreate thread records as session-backed metadata with a
  `threads` tool, launcher APIs, and UI cards/routes that can search, open,
  attach, switch, drop, and return from thread handoffs.
- Core types/functions: thread store, thread registry, `ThreadsTool`, thread
  HTTP handlers, thread card payload parsers, thread sidebar/search/open views,
  and thread policy prompt contributor.
- Runtime ordering: normalize thread type/context, search existing records,
  create or register only when allowed, persist metadata, emit UI card payloads,
  switch or attach sessions, and preserve origin handoff links for return.
- Non-obvious constraints: lookup/search requests must not create duplicate
  threads, dropped threads stay directly addressable but non-discoverable, and
  automatic policy-driven routing is gated by per-rule thresholds.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-THREADS-001` | MUST | The `threads` tool can search, create, switch, register the current session, attach the current session, return to origin, drop threads, and expose or update routing policy. | The model needs a single structured surface for thread lifecycle and navigation. |
| `FR-THREADS-002` | MUST | Launcher thread APIs list/search, fetch, create, update, attach, return, and drop thread records without deleting underlying session data. | The UI needs durable management without losing conversation history. |
| `FR-THREADS-003` | MUST | Thread policy rules define type, description, optional mode/attach strategy, confidence behavior, and per-rule `min_messages`, `min_text_chars`, and `threshold_logic` gates before chat may become or join a thread. | Normal chat should not create threads just because a rule matches; long or substantial chats can become threads predictably. |
| `FR-THREADS-004` | SHOULD | Thread UI provides a search workspace and an open-thread chat view, thread cards route directly to `/threads/open/{thread-id}` while search lives under `/threads/search`, and all thread UI surfaces expose thread-native creation/drop actions instead of normal chat history actions. | Search, active thread work, and thread lifecycle actions are distinct user workflows. |
| `FR-THREADS-005` | MUST | When a model/tool-created auto-switch card opens a newly created empty thread from a user request, the UI seeds that thread exactly once with the concrete requested task, using the card query or thread source query; blank UI-created threads store non-empty metadata but must not fabricate a generic first chat message. | A user asking to start a thread expects the requested work to begin there, while a blank New Thread action should not pollute the thread with generic filler. |
| `FR-THREADS-006` | MUST | The `threads` tool and thread policy prompt are available by default only to the root/default user-facing agent, never inherited by subturn/spawn child agents, and available to non-default configured agents only when explicitly listed in that agent's `AGENT.md` tools allowlist. | Thread lifecycle changes are UI/session control-plane actions and should not be exposed to background agents accidentally. |

## Data And State Model

Thread state is stored under workspace thread/session metadata. Records contain
thread ID, type, title, discoverability, searchable context, primary UI/session
linkage, source query, agent ID, registration source, timestamps, and optional
handoff records that connect origin sessions to target threads. Thread policy
state lives under `tools.threads.policy` and is normalized before prompting,
tool output, or API response.

## Surface Ownership

Owns: CODE pkg/threads/**
Owns: CODE pkg/tools/threads.go
Owns: CODE web/backend/api/thread.go
Owns: CODE web/frontend/src/api/threads.ts
Owns: CODE web/frontend/src/store/threads.ts
Owns: CODE web/frontend/src/components/threads/**
Owns: CODE web/frontend/src/components/agent/tools/thread-policy-tab.tsx
Owns: CODE web/frontend/src/components/chat/chat-page.tsx *
Owns: CODE web/frontend/src/components/app-sidebar.tsx *
Owns: CODE web/frontend/src/features/chat/thread-*
Owns: CODE web/frontend/src/routes/threads*
Owns: CONFIG.tools.threads*
Owns: HTTP * /api/threads*
Owns: HTTP * /api/tools/thread-policy
Owns: TEST pkg/threads/*
Owns: TEST pkg/tools/threads*
Owns: CODE pkg/agent/agent_init.go *
Owns: CODE pkg/agent/instance.go *
Owns: CODE pkg/agent/prompt_turn.go *
Owns: CODE pkg/agent/subturn.go *
Owns: TEST pkg/agent/threads_tool_scope_test.go *
Owns: TEST web/backend/api/thread*
Owns: TEST web/backend/api/tools*
Owns: TEST web/frontend/src/components/threads/thread-card-message.test.tsx *
Owns: TOOL threads

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Tool | `threads` | Actions include search/find/propose, create, register current, attach current, switch, return to origin, detach, drop, get policy, and set policy. | `FR-THREADS-001`, `FR-THREADS-003` |
| Tool registration | Agent tool registry and prompts | Registers `threads` and its policy prompt by default only on the root/default user-facing agent; non-default agents must explicitly opt in through `AGENT.md`; subturn child registries and prompts remove inherited `threads`. | `FR-THREADS-006` |
| Config | `tools.threads.*` | Enables the tool and defines routing policy, rule thresholds, attach behavior, and per-agent overrides. | `FR-THREADS-003` |
| HTTP | `/api/threads*`, `/api/tools/thread-policy` | Launcher management and policy endpoints return JSON records and persist normalized config. | `FR-THREADS-002`, `FR-THREADS-003` |
| UI | `/threads/search`, `/threads/open`, `/threads/open/{thread-id}` | Search renders thread tiles; empty open renders the Thread workspace empty state; open by ID renders the chat window bound to a selected thread session, hides normal chat history, labels primary creation as New Thread, and exposes trash/drop for active or listed threads. | `FR-THREADS-004` |
| UI | Thread cards in chat messages | Switch cards may move the active chat session and, for empty newly created threads, seed the original task into the opened thread; rendered thread tiles expose the same trash/drop affordance as the search UI. | `FR-THREADS-004`, `FR-THREADS-005` |

Thread-specific frontend paths are mapped in
[frontend-ownership.json](frontend-ownership.json), so thread UI changes must
update this feature spec rather than a broad UI owner.

## Web UI Route Contract

The left pane exposes a single non-collapsible `Threads` navigation item.
Clicking `Threads` always links to `/threads/search` and renders the thread
search workspace with a search box, type filter, and discoverable thread tiles.
The sidebar never links directly to the last opened thread and never exposes
nested `Search` or `Thread` submenu items.

Thread routes have distinct meanings:

| Route | Required behavior |
| --- | --- |
| `/threads/search` | Render the search workspace only. The left-pane `Threads` item opens this route. Search result tiles and model-produced search cards route selected threads to `/threads/open/{target-session-id}` and do not create new threads. |
| `/threads/open` | Render the full-window Thread workspace empty state with the `Thread` header, `New Thread` action, and `No threads yet` content. This route has no search box, no thread tiles, no chat composer, and no normal chat history menu. |
| `/threads/open/{id}` | Treat `id` as a thread ID, UI session ID, primary session key, or registered session key. If the looked-up thread exists and is discoverable, switch the chat controller to `thread.ui_session_id || thread.id` and render the normal chat window as the Thread workspace with no search widgets or thread tiles. If the lookup fails or the thread is dropped, clear the remembered open thread and replace-navigate to `/threads/open`. |

Every navigation that creates, switches to, or opens a thread must use
`/threads/open/{target-session-id}` where `target-session-id` is
`target_session_id || thread.ui_session_id || thread.id`. A generated thread
with zero messages must receive the seeded task message before or while the UI
switches when the card query or thread source query contains a real user task.
That seed is idempotent for the target session and task text: repeated card
renders, React remounts, reconnects, or already-loaded session history must not
send the same first user message again.
The seed message is derived from the user request by removing thread-control
phrasing, such as `can you start a thread about`, and preferring the embedded
work directive, such as `I want you to go over ...`. For example,
`can you start a thread about planning to relocate to japan, i want you to go
over all the possible options how we as family of 3 can go there` seeds the
thread with `go over all the possible options how we as a family of 3 can
relocate to japan`, not a generic starter prompt. If no concrete task text is
available, the UI only switches/opens the thread and sends no fabricated chat
message. A concrete open URL must never render the `/threads/open` empty state
unless the target lookup fails or the thread is undiscoverable.

Thread-native actions replace normal chat history controls on Thread surfaces:
`New Thread` creates a thread, stores its open UI session, switches chat to that
session, and navigates to `/threads/open/{target-session-id}`. When New Thread
is triggered from non-empty search/task text, the UI sends that cleaned task as
the first chat message; when triggered with no task text, it stores source query
metadata as `New thread` and sends no first chat message. The trash/drop action
marks the thread non-discoverable without deleting the session; dropping the
active thread clears the remembered open session and navigates to the empty
`/threads/open` workspace, while dropping another thread only removes its tile
from the current result list.

## Algorithms And Ordering

1. Treat user requests to find, search, show, list, open, switch to, or continue
   an existing thread as navigation and never as a new thread creation request.
2. For policy-driven routing, start in normal chat, match a rule, verify the
   rule thresholds from visible user/assistant chat content, then search for an
   existing thread before registering, attaching, or switching.
3. For create/register/attach/switch actions, normalize type, title, query,
   context tags, active session key, and handoff metadata before writing records.
   Tool-created new threads must provide `query` as the concrete user task;
   `title` is display metadata only and cannot be the sole seed source.
4. For dropped threads, set discoverability false and exclude them from normal
   list/search while preserving direct lookup and session metadata.
5. For UI card payloads, render search results as non-switching tiles and render
   switch/handoff payloads as direct links to the open-thread route.
6. When an auto-switch payload targets an empty thread, switch to that thread
   session and send a first message only when the payload query, thread source
   query, preview, or title contains a concrete task. Derive the first message
   from that task and remove thread-control scaffolding; do not send a generic
   default prompt for `New thread` or other blank placeholders.
7. In thread UI routes, replace normal chat creation/history controls with
   thread-specific controls: New Thread creates and opens a new thread, and
   trash/drop hides the thread from discovery without deleting session data.
8. Route `/threads/open/{id}` through the concrete open-thread child route so
   session-backed thread URLs render the chat workspace instead of the empty
   `/threads/open` parent route.
9. Register `threads` and the thread policy prompt only for the default
   user-facing agent unless a non-default agent explicitly lists `threads` in
   `AGENT.md`; when cloning tools for a subturn/spawn child, remove `threads`
   from the child registry and filter prompt contributors against the child's
   actual registered tools.

## Cross-Feature Behavior

Agent conversations include the thread policy prompt and execute the `threads`
tool. Session memory provides durable histories and aliases. Launcher management
auth protects HTTP/UI access. Tool execution owns the generic registry and
schema export mechanics, while this feature owns thread-specific tool behavior.
Chat controller switching and send APIs are used by thread cards to move the
active UI session and seed work after creating an empty thread.
Subturn execution may clone agent registries and context builders for background
work, but thread tool and policy prompt exposure remains scoped to user-facing
root turns.

## Failure And Edge Cases

- Empty or invalid policy modes normalize to tool mode unless an explicit
  valid mode is supplied.
- Negative rule thresholds normalize to zero; absent thresholds make a rule
  eligible immediately only when that is explicitly configured.
- Multiple plausible existing threads require confirmation when the matching
  rule requests it.
- Lookup/navigation text cannot create, register, or attach a new thread unless
  the user explicitly asks to create one.
- Model/tool-created empty threads from a concrete user request must not remain
  blank after they open; the persisted source query is non-empty and the first
  sent prompt is the cleaned requested task. The same target session and seed
  text must not be sent twice after duplicate switch-card renders or session
  history reloads. Blank UI-created threads keep a non-empty source query
  placeholder but send no generic first prompt.
- Tool `create` and `switch`/`attach_current` with `create_if_missing` fail
  clearly when creating a new thread without `query`; the model must retry with
  the concrete user request instead of relying on `title`.
- Thread routes must not show the normal chat history menu in place of
  thread-specific New Thread and Drop Thread controls.
- A concrete `/threads/open/{id}` URL returned by a model switch card or thread
  tile must render the open-thread chat view, not the empty `No threads yet`
  parent route.
- Dropped or undiscoverable threads must not remain reachable through a stale
  `/threads/open/{thread-id}` URL; those entry points fall back to the empty
  Thread workspace, and the left-pane `Threads` shortcut remains clickable and
  returns to search.
- Background subturns, spawned agents, or non-default agents without explicit
  opt-in must not receive the `threads` tool or thread policy prompt from
  shared registration.
- Attach and return operations fail clearly when current session or handoff
  metadata is unavailable.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-THREADS-001` | [pkg/tools/threads_test.go](../../pkg/tools/threads_test.go), [pkg/threads/threads_test.go](../../pkg/threads/threads_test.go) |
| `FR-THREADS-002` | [web/backend/api/thread_test.go](../../web/backend/api/thread_test.go), [pkg/threads/threads_test.go](../../pkg/threads/threads_test.go) |
| `FR-THREADS-003` | [pkg/agent/prompt_test.go](../../pkg/agent/prompt_test.go), [pkg/tools/threads_test.go](../../pkg/tools/threads_test.go), [web/backend/api/tools_test.go](../../web/backend/api/tools_test.go) |
| `FR-THREADS-004` | [web/frontend/src/components/threads](../../web/frontend/src/components/threads), [web/frontend/src/components/threads/threads-page.test.tsx](../../web/frontend/src/components/threads/threads-page.test.tsx), [web/frontend/src/components/threads/thread-sidebar.test.tsx](../../web/frontend/src/components/threads/thread-sidebar.test.tsx), [web/frontend/src/components/app-sidebar.test.tsx](../../web/frontend/src/components/app-sidebar.test.tsx), [web/frontend/src/routes/-threads-open-route.test.tsx](../../web/frontend/src/routes/-threads-open-route.test.tsx) |
| `FR-THREADS-005` | [web/frontend/src/components/threads/thread-card-message.test.tsx](../../web/frontend/src/components/threads/thread-card-message.test.tsx) |
| `FR-THREADS-006` | [pkg/agent/threads_tool_scope_test.go](../../pkg/agent/threads_tool_scope_test.go) |

## Implementation Anchors

- [pkg/threads/threads.go](../../pkg/threads/threads.go)
- [pkg/tools/threads.go](../../pkg/tools/threads.go)
- [web/backend/api/thread.go](../../web/backend/api/thread.go)
- [web/frontend/src/components/threads](../../web/frontend/src/components/threads)
