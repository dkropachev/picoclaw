# Standard Collection UI System

## Decision

PicoClaw administrative collections use one shared presentation and interaction
system. The first standard surfaces are Model Aliases, Model Routers, MCP
Servers, Agents, and Model Evaluations. New collection surfaces, and legacy
collection surfaces when materially changed, must adopt this contract.

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

A collection route owns one resource type and summary data only. Related counts
may link elsewhere. It must not embed an unrelated list, global settings, a
creation form, an editor, or an item's complete configuration payload.

## Shared Frontend Contract

The collection subsystem provides:

- `CollectionShell`, `CollectionToolbar`, `CollectionQueryInput`,
  `CollectionResults`, `CollectionSelectionBar`, and `CollectionDetailShell`;
- typed `CollectionDefinition<T>` metadata for stable IDs, identity, columns,
  Grid facts, badges, supported actions, and supported views;
- `useCollectionRouteState` for canonical URL/local state, cursor loading,
  explicit selection, scroll restoration, and mutation reconciliation;
- shared row-selection, context-menu, table, view-switch, loading, empty, and error primitives built
  on the existing shadcn/Radix layer.

Feature route components stay thin. They provide API functions, resource
metadata, and specialized detail/editor content; they do not recreate collection
infrastructure.

## Views And Responsive Layout

- Compact List is the default. Users may choose List, Table, or Grid when the
  surface supports them.
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

| Collection        | List                 | New                      | Detail                     | Edit / related                                              |
| ----------------- | -------------------- | ------------------------ | -------------------------- | ----------------------------------------------------------- |
| Model Aliases     | `/models/aliases`    | `/models/aliases/new`    | `/models/aliases/:name`    | `/:name/edit`                                               |
| Model Routers     | `/models/routers`    | `/models/routers/new`    | `/models/routers/:name`    | `/:name/edit`                                               |
| MCP Servers       | `/agent/mcp/servers` | `/agent/mcp/servers/new` | `/agent/mcp/servers/:name` | `/:name/edit`; settings live at `/agent/mcp/settings`       |
| Agents            | `/agent/agents`      | `/agent/agents/new`      | `/agent/agents/:id`        | `/:id/edit`, `/:id/capabilities`, `/:id/activity`           |
| Model Evaluations | `/model-evaluations` | `/model-evaluations/new` | `/model-evaluations/:id`   | `/:id/edit`, `/:id/languages`, `/:id/corpus`, `/:id/report` |

Legacy `/models`, `/agent/mcp`, `?agent=`, and `?probe=` UI forms are not
redirected or compatibility-rendered. The existing evaluation report route
remains canonical.

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
suggested values. Each pilot provides a stable name/ID detail endpoint. Bulk
delete returns:

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

## Governance And Evidence

`web/frontend/collection-surfaces.json` is the auditable inventory. `standard`
entries must use the shared shell and register required routes. A base/head delta
guard rejects new `legacy` entries and requires a modified legacy implementation
to migrate to `standard`. `exempt` entries require a specific rationale and are
not an escape hatch for ordinary administrative collections.

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
