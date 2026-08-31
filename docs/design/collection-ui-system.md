# Standard Collection UI System

## Decision

PicoClaw administrative collections use one shared presentation and interaction
system. Standard surfaces include Accounts, Account Routers, Model Aliases,
Model Routers, MCP Servers, Agents, Model Evaluations, Skills, Tools, Event
Sources, Workflow Definitions, Git Workspaces, Development Workspaces,
Development Repository Assignments, and Development Workflow Configurations.
Repository Review Run Findings, Repository Findings, and Repository Review
Issue Previews are also standard collections even though they are nested below
an owning review or repository. New collection surfaces, and legacy collection
surfaces when materially changed, must adopt this contract.

This standard does not apply to intrinsically unique experiences such as chat,
logs, code viewers, diagrams, result reports, marketplace search, or a single
configuration form. Those surfaces are registered as `exempt`, with a reason,
in `web/frontend/collection-surfaces.json` when they otherwise resemble a list.

## Canonical Anatomy

```text
[Collection title · result count]                 [Refresh] [Add item]
[JQL-like filter with autocomplete]       [List] [Table] [Grid]

[When selected: N selected]                 [Delete] [Clear selection]

Item identity   concise metadata   status/time
Click selects · Shift-click extends · double-click opens · right-click shows actions
```

Refresh, Add, selection, and bulk deletion appear only when the collection
definition and feature controller provide those capabilities. Item actions are
likewise metadata-driven rather than hard-coded into the shared page.

A collection route owns one resource type and summary data only. Related counts
may link elsewhere. It must not embed an unrelated list, global settings, a
creation form, an editor, or an item's complete configuration payload.

## Shared Frontend Contract

The collection subsystem provides:

- `StandardCollectionPage`, the production controller that composes the shared
  shell, toolbar, results, optional selection and confirmation flow, paging,
  refresh, creation, and metadata-defined item actions;
- `CollectionShell`, `CollectionToolbar`, `CollectionQueryInput`,
  `CollectionResults`, `CollectionSelectionBar`, and `CollectionDetailShell`;
- typed `CollectionDefinition<T>` metadata for stable IDs, identity, columns,
  Grid facts, badges, supported actions, and supported views;
- `useCollectionRouteState` for canonical URL state and browser/in-memory state:
  query and view preference, recent queries, explicit selection, scroll
  restoration, and mutation reconciliation;
- shared row-selection, context-menu, table, view-switch, loading, empty, and error primitives built
  on the existing shadcn/Radix layer.

Feature controllers own React Query fetching and cursor paging. They flatten
loaded pages and pass items, totals, schema, canonical query, loading/error
state, next-page state, refresh, and applicable mutations to
`StandardCollectionPage`. Route components stay thin and provide search-state
adapters, resource metadata, navigation, and specialized detail/editor content;
they do not recreate collection infrastructure.

Nested collections use the same production controller. They may add the shared
context bar for Back navigation and parent identity, leading status/filter
content, and metadata-driven selection actions, but they do not replace the
shared shell, query toolbar, view switch, results, paging, or route-state
controller. Selection actions such as retry, validate, discuss, generate, or
publish use the same explicit selected-ID state as bulk deletion.

## Views And Responsive Layout

- Compact List is the default. Users may choose List, Table, or Grid when the
  surface supports them.
- Administrative collections support all three views. Operational collections
  rendered through the shared subsystem may be registered as reviewed
  exemptions and support List and Table only.
- URL parameter `view` overrides the browser-local preference. The preferred
  view is stored separately per collection.
- Rows are 56–72 px high. Table headers are sticky. Table collapses to compact
  rows on mobile; Grid becomes one column.
- Grid cards contain at most four summary facts. Collection screens never render
  full configuration payloads.
- Avoid nested decorative cards. Use semantic tokens only and prevent horizontal
  viewport overflow at 1280 x 900 and 390 x 844.

## Query State And Autocomplete

- The active query is URL parameter `q`. Clear restores the collection default.
- The input is one single-line, server-authoritative JQL-like field. Enter
  applies. Escape restores the active query without applying draft text.
- Store at most eight successful recent queries per collection in browser-local
  state. Provide Clear history. Durable saved views are not part of this version.
- Caret-aware autocomplete uses the existing accessible listbox behavior and the
  response `query_schema`. It suggests fields, valid operators, enum/dynamic
  values, logical keywords, and `ORDER BY` clauses appropriate to the caret.
- Invalid-query responses contain a safe code, bounded message, and zero-based
  UTF-8 byte position. The UI converts the byte position before caret/error
  highlighting. It does not implement a competing parser.

The reusable backend grammar supports typed string/enum, boolean, number, and
timestamp fields; `=`, `!=`, `~`, `!~`, comparisons, `IN`, `NOT IN`, `AND`,
`OR`, `NOT`, parentheses, and at most three `ORDER BY` fields. Bounds are 4 KiB
input, depth 16, 50 predicates, 100 `IN` values, default page size 50, and
maximum page size 200.

## Paging, Selection, And Deletion

- Stores evaluate a typed AST through resource-specific allowlisted resolvers.
  Generic query code never interpolates SQL or bypasses feature validation.
- Opaque cursors bind the canonical query, ordering, final sort values, and
  stable item ID. A cursor from another canonical query is rejected.
- Every summary exposes a stable URL-safe `id`. Clients treat it as opaque. If
  a natural identity is unsafe for a path segment, the backend generates a
  fixed-length deterministic unpadded base64url digest over the resource
  namespace and canonical identity, then resolves it against bounded canonical
  candidates; clients never reconstruct it.
- Selection contains only explicitly selected stable IDs. A plain row click
  replaces selection, Shift-click extends a contiguous range, Control/Command
  toggles individual rows, double-click or Enter opens detail, and right-click
  exposes item actions without a permanent action trigger. Selection persists across
  loaded pages and clears when the canonical query changes.
- Bulk delete accepts at most 200 IDs and the applicable config revision or
  per-item versions. It always requires confirmation and never offers
  query-wide deletion.
- Reconcile partial success in place: remove `deleted_ids`; retain and select
  failed rows; show stable safe failure codes and blockers. Apply a returned
  revision before another mutation.

## Routes And Navigation

- List, Add, Detail, and Edit are dedicated stable routes. Mutable array indexes
  are not UI identities.
- Browser Back returns to the same query, view, explicit selection, and in-memory
  scroll position.
- Direct detail links load the item endpoint and never require a prior list
  response.
- Detail pages use the shared header and status/action area, bounded content
  width, and consistent loading, error, and not-found states.
- Related detail sections use nested routes. Tabs are reserved for information
  that belongs to the selected item.

Canonical pilot routes:

| Collection              | List                                            | New                                        | Detail                                                     | Edit / related                                                       |
| ----------------------- | ----------------------------------------------- | ------------------------------------------ | ---------------------------------------------------------- | -------------------------------------------------------------------- |
| Model Aliases           | `/models/aliases`                               | `/models/aliases/new`                      | `/models/aliases/:name`                                    | `/:name/edit`                                                        |
| Model Routers           | `/models/routers`                               | `/models/routers/new`                      | `/models/routers/:name`                                    | `/:name/edit`                                                        |
| MCP Servers             | `/agent/mcp/servers`                            | `/agent/mcp/servers/new`                   | `/agent/mcp/servers/:name`                                 | `/:name/edit`; settings live at `/agent/mcp/settings`                |
| Agents                  | `/agent/agents`                                 | `/agent/agents/new`                        | `/agent/agents/:id`                                        | `/:id/edit`, `/:id/capabilities`, `/:id/activity`                    |
| Model Evaluations       | `/model-evaluations`                            | `/model-evaluations/new`                   | `/model-evaluations/:id`                                   | `/:id/edit`, `/:id/languages`, `/:id/corpus`, `/:id/report`          |
| Event Sources           | `/event-sources`                                | `/event-sources/new`                       | `/event-sources/:id`                                       | `/:id/edit`; ingress and storage policy at `/event-sources/settings` |
| Skills                  | `/agent/skills`                                 | `/agent/skills/new`                        | `/agent/skills/:id`                                        | None; marketplace choices remain at `/agent/hub`                     |
| Tools                   | `/agent/tools`                                  | None                                       | `/agent/tools/:id`                                         | `/:id/edit`; global adaptation at `/agent/tools/settings/adaptation` |
| Workflow Definitions    | `/agent/workflows`                              | `/agent/workflows/new`                     | `/agent/workflows/:id`                                     | `/:id/edit`; global workflow policy at `/agent/workflows/settings`   |
| Workflow Runs           | `/agent/workflows/runs`                         | None                                       | `/agent/workflows/runs/:id`                                | Operational exemption; shared List/Table only                        |
| Git Workspaces          | `/agent/git-workspaces`                         | None                                       | `/agent/git-workspaces/:id`                                | History at `/history`; global limits at `/settings`                  |
| Git Workspace History   | `/agent/git-workspaces/history`                 | None                                       | None                                                       | Operational exemption; shared List/Table only                        |
| Development Workspaces  | `/development`                                  | `/development/new`                         | `/development/:id`                                         | Workspace-owned Overview, Changes, Files, and Activity views         |
| Repository Assignments  | `/development/repositories`                     | `/development/repositories/new`            | `/development/repositories/:id`                            | `/:id/edit`                                                          |
| Workflow Configurations | `/development/workflow-configurations`          | `/development/workflow-configurations/new` | `/development/workflow-configurations/:id`                 | `/:id/edit`                                                          |
| Review Findings         | `/repository-reviews/:id/findings`              | None                                       | `/repository-reviews/:id/findings/:findingId`              | Raw-source and repository-finding navigation                           |
| Review Raw Findings     | `/repository-reviews/:id/raw-findings`          | None                                       | `/repository-reviews/:id/raw-findings/:sourceId`           | Per-source retry and parent deduplicated-finding navigation            |
| Repository Findings     | `/repository-reviews/repositories/:id/findings` | None                                       | `/repository-reviews/repositories/:id/findings/:findingId` | Issue linking at `/:findingId/link-issue`                            |
| Review Issue Previews   | `/repository-reviews/:id/issues`                | None                                       | `/repository-reviews/:id/issues/:draftId`                  | `/:draftId/edit`                                                     |

Legacy `/models`, `/agent/mcp`, `?agent=`, `?probe=`, and Tools `?tab=` UI forms
are not redirected or compatibility-rendered. Workflow `mode`, `workflow`, and
`run` search URLs are likewise a hard cutover: definitions, authoring, runs,
run detail, and settings use their dedicated routes. The existing evaluation
report route remains canonical. Skill import and item detail are routed pages
rather than dialog-only navigation. Development Workflow configuration
`?config=` URLs are also removed: configuration identity is a path segment on
the dedicated detail and edit routes, while optional `flow` and `gate` editor
context remains query state on `/:id/edit`.

## Backend List And Mutation Contract

Existing resource-specific array fields remain additive-compatible. Standard
list responses also contain:

```json
{
  "total": 0,
  "next_cursor": "",
  "canonical_query": "",
  "query_schema": {
    "fields": []
  }
}
```

Each schema field declares name, type, operators, sortability, and bounded
suggested values. Every summary and direct-detail response uses the same stable,
URL-safe ID. Each collection provides an ID-addressed detail endpoint; the
backend is solely responsible for encoding and resolving opaque IDs. Bulk delete
returns:

```json
{
  "deleted_ids": ["stable-id"],
  "failures": [
    { "id": "blocked-id", "code": "referenced", "blockers": ["safe label"] }
  ]
}
```

Configuration-backed mutations retain same-origin guards, candidate config
validation, credential cleanup, selection-aware reference blockers, one fenced
save, new revision, and restart-effect reporting. Evaluation deletion holds the
catalog lock and deletes only version-matching drafts.

Workflow definitions use `/api/workflows/definitions` for the paged collection
and ID-addressed detail reads. Canonical workflow refs never become path
segments: the backend emits and resolves their deterministic URL-safe IDs.
Workflow runs use the same query and cursor envelope at `/api/workflows/runs`
and keep their existing ID-addressed operational actions and detail resources.

Git workspaces use `/api/git-workspaces` for the paged inventory and
ID-addressed detail reads. `/api/git-workspaces/history` pages the generic
operational history independently, and `/api/git-workspaces/settings` owns
configured/effective generic storage limits through one same-origin,
revision-fenced save. Cleanup and drop remain confirmed item actions; locked
workspaces and every controller-private workspace stay ineligible and
structurally absent from all three browser projections.

Development repository assignments use
`/api/development/repository-assignments` for typed query/cursor list reads,
opaque-ID detail and revision-fenced CRUD. Their summaries expose repository,
selected configuration, and default branch; bulk deletion uses one exact
config revision and preserves failed selections. Development Workflow
configurations use `/api/development/workflow-configurations/items` for the
paged collection and ID-addressed item reads and writes while the existing
aggregate endpoint remains the authority for shared lifecycle settings. Their
summaries expose stable ID, name, default state, binding count, and deferred
issue mode. Both collections default to compact List and support List, Table,
and Grid.

Development workspaces use `/api/development-workspaces` for the typed paged
inventory and the existing `/api/development-workspaces/:id` aggregate for
direct detail. Summaries expose ID, intent, source, repository, title, phase,
execution state, created time, and updated time and default to
`ORDER BY updated DESC`. The list supports List, Table, and Grid without
selection or deletion; intake remains the dedicated New route, and the routed
workspace retains its specialized lifecycle, chat, code, and activity views.

Repository review run findings, canonical repository findings, and issue
previews expose automation-scoped typed query/cursor envelopes. Run findings
remain immutable occurrence evidence; repository findings retain lifecycle and
issue-generation actions; issue previews retain version-fenced publication
state. Their list responses contain only compact summaries plus parent context,
capabilities, `total`, `next_cursor`, `canonical_query`, and `query_schema`.
Finding messages, commit/blob payloads, model observations, occurrence and
resolution histories, preview bodies, labels, external issue details, and
generation instructions remain on ID-addressed detail endpoints. Generation ID
is a caller-visible, server-enforced issue-preview collection scope and is bound
into its cursors.

## Governance And Evidence

`web/frontend/collection-surfaces.json` is the auditable inventory. `standard`
entries must use `StandardCollectionPage` and declare complete route,
capability, view, owning-spec, and implementation-glob metadata. A base/head
delta guard rejects new `legacy` entries and requires a modified legacy
implementation to migrate to `standard`. `exempt` entries require a specific
rationale and are not an escape hatch for ordinary administrative collections.
Workflow runs and Git workspace history are operational exemptions: they are
not administrative inventories, but still render with the shared page and
collection primitives and support List and Table. Their exemption covers only
the view set and operational ownership; query, paging, route state, direct
detail loading, and shared loading/empty/error behavior remain mandatory.

PR validation runs formatting, ESLint/UI rules, the collection delta guard,
Vitest, the production build, mocked Playwright smoke tests, and deterministic
visual tests. Visual coverage includes canonical views and states, both themes,
both viewports, and every pilot. Tests freeze visible time, disable animations,
wait for fonts, use mocked APIs, and retain traces and screenshot diffs on
failure. Bundled-font (`inter`) and GitHub-runner system-font (`system`)
baselines are kept separately because Chromium font rasterization is
platform-dependent. CI never rewrites either baseline family.

Local baseline update:

```bash
cd web/frontend
COLLECTION_VISUAL_BASELINE=inter pnpm test:ui:visual:update
```

Before accepting updated images, inspect every diff and record why the visual
change is intentional in the pull request.
