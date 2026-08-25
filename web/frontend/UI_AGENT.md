# PicoClaw UI Specialist Contract

This contract applies when an agent is assigned frontend-only UI work under
`web/frontend/`. Read `AGENTS.md` first; this file adds the handoff and evidence
rules for product UI changes.

## Authority And Preconditions

- Own the requirement IDs named in the handoff. Do not invent a UI-only feature
  when behavior belongs to an existing `docs/features/*.md` specification.
- Start implementation only after the owning feature agent has supplied the
  completed backend contract: stable identities, query fields and schema, list
  and detail responses, mutation fences, bulk results, safe error codes, and
  deterministic fixtures.
- Do not edit Go code, backend APIs, feature specifications, or generated route
  files. Report a missing contract instead of compensating for it in the UI.
- Keep HTTP access in `src/api/**` and use the existing authenticated request
  helpers. A UI component must not call `fetch` directly.

If a required backend contract is absent, stop that part of the implementation
and return this report:

```text
Backend gap
- Owning requirement IDs:
- Blocked route and interaction:
- Missing or ambiguous endpoint:
- Required request fields and mutation fence:
- Required success response:
- Required safe errors and blocker codes:
- Fixture or test case needed:
- Frontend work that can continue independently:
```

## Collection Surfaces

- Render one primary resource collection per route. A list route contains only
  that resource's summary data and collection controls.
- Use the shared collection subsystem and `CollectionDefinition<T>` metadata.
  Do not create feature-specific collection shells, cards, tables, toolbars,
  selection stores, pagination controllers, filter parsers, or view switches.
- Filter paged data on the server. The backend's `query_schema` is authoritative
  for fields, operators, sorting, and bounded suggestions.
- Put creation, detail, and editing on dedicated stable routes. Tabs are allowed
  only for information belonging to the selected item.
- Keep global settings, unrelated lists, creation forms, and full configuration
  payloads off collection routes.
- Register the surface in `collection-surfaces.json`. A `standard` surface must
  declare its list/detail/editor routes, capabilities, supported views, owning
  specification, and implementation globs.

The master interaction and layout contract is
[`docs/design/collection-ui-system.md`](../../docs/design/collection-ui-system.md).

## Implementation Quality

- Reuse shadcn/Radix controls from `src/components/ui/**`, existing forms and
  comboboxes, i18n, TanStack Router, and TanStack Query.
- Use semantic design tokens. Keep compact layouts keyboard-complete and free of
  body-level horizontal overflow.
- Preserve the active query, view, explicit selection, and in-memory scroll
  position across detail/editor navigation as specified by the shared hook.
- Treat loading, empty, error, not-found, conflict, and partial-success states as
  first-class states, not as toast-only behavior.

## Handoff Evidence

Before handoff, provide:

- desktop (1280 x 900) and mobile (390 x 844) evidence;
- light- and dark-theme evidence for the affected canonical states;
- keyboard evidence for query autocomplete, view controls, menus, selection,
  confirmation, and Back navigation;
- no console/page errors and no body-level horizontal overflow;
- no serious or critical axe findings;
- updated intentional Playwright baselines with a concise explanation;
- the exact owning requirement IDs and commands run.

Run at minimum:

```bash
cd web/frontend
pnpm lint
pnpm format
pnpm test
pnpm test:ui:smoke
pnpm test:ui:visual
```

Use `pnpm test:ui:visual:update` only after human review of an intentional visual
change. CI never updates snapshots.
