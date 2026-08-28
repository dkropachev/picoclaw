# Frontend Guidelines

## Decision

PicoClaw UI code remains part of the product feature it exposes. The launcher
frontend also has cross-cutting implementation rules so agents and contributors
build consistent, secure, and maintainable UI surfaces.

The scoped guidance lives in `web/frontend/AGENTS.md`. Automated checks live in
`web/frontend/scripts/lint-ui-rules.mjs`, `web/frontend/eslint.config.js`, and
`web/frontend/tests/ui-smoke.spec.ts`. Frontend feature ownership lives in
`docs/features/frontend-ownership.json`. These gates run through `pnpm lint`,
`pnpm format`, `pnpm test:ui`, `make lint-frontend`,
`make test-frontend-ui`, `make lint-features`, and `make feature-delta`.

Administrative collections additionally follow the
[Standard Collection UI System](collection-ui-system.md). The audited inventory
lives in `web/frontend/collection-surfaces.json`; UI-specialist handoffs follow
`web/frontend/UI_AGENT.md`.

## Product Ownership

Frontend code is not a generic standalone feature. Feature ownership follows
behavior:

| UI Surface                                                                                    | Owning Feature Spec                    |
| --------------------------------------------------------------------------------------------- | -------------------------------------- |
| Chat page, Pico websocket UI, channel pages                                                   | `docs/features/chat-channels.md`       |
| Tool configuration and tool library pages                                                     | `docs/features/tool-execution.md`      |
| Persistent agent policy, default selection, model fallback, skills, and delegation management | `docs/features/agent-conversations.md` |
| Session logs and transcript history pages                                                     | `docs/features/session-memory.md`      |
| Model, config, OAuth, auth, startup, update, and app shell management                         | `docs/features/launcher-management.md` |
| Durable event inspection, payload reveal, dispatch history, and replay                        | `docs/features/event-automation.md`    |
| Skill and hub pages                                                                           | `docs/features/skills.md`              |
| Thread search, cards, policy, and open-thread pages                                           | `docs/features/threads.md`             |

When a UI behavior changes, update the owning feature spec before or with the
code change. `docs/features/frontend-ownership.json` is the enforced
path-to-spec map for launcher frontend source. A cross-cutting UI spec should
be reserved for shared app shell, navigation, design-system, theme, loading,
and error behavior, and it must not claim ownership of all frontend source.

## Agent-Facing Rules

Agents should read `web/frontend/AGENTS.md` before editing launcher frontend
files. The short version is:

- use established React, TanStack Router, Jotai, Tailwind, shadcn/Radix, and
  i18n patterns;
- keep API access in `src/api/**`;
- use `launcherFetch` for authenticated launcher requests;
- prefer shared UI components and shared form helpers;
- prefer semantic design tokens over raw colors;
- justify inline styles with `ui-rule-allow dynamic-style`;
- keep operational pages dense, responsive, accessible, and free of layout
  overlap.
- use shared collection primitives for administrative collections and dedicated
  list, new, detail, and edit routes.

## Static Enforcement

`web/frontend/scripts/lint-ui-rules.mjs` uses the TypeScript parser and
`web/frontend/ui-rules.config.json` to enforce deterministic rules that are
cheap enough for every pull request:

| Rule                                                                                              | Reason                                                                         |
| ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| No direct `fetch(...)` outside `src/api/**`.                                                      | Keeps auth redirects, credential handling, and error semantics centralized.    |
| No inline `style=` without nearby `ui-rule-allow dynamic-style`.                                  | Prevents layout drift while allowing measured geometry and dynamic dimensions. |
| No raw hex colors outside approved rendering exceptions.                                          | Keeps UI color choices tied to tokens and avoids one-off palettes.             |
| Every `standard` manifest surface registers required routes and reaches `StandardCollectionPage`. | Prevents feature-local collection infrastructure and route regressions.        |
| Standard list import closures do not import configured legacy cards.                              | Keeps migrated surfaces on the shared presentation path.                       |

`web/frontend/scripts/check-collection-delta.mjs` provides the base/head gate.
It rejects newly registered legacy collections, prevents a standard surface
from regressing, and requires a touched legacy implementation to migrate to
`standard`. The first PR that introduces the manifest bootstraps the inventory;
all later comparisons are strict.

Approved hardcoded-color exceptions live in `web/frontend/ui-rules.config.json`
and are intentionally narrow:

- `src/lib/ansi-log.ts` maps ANSI terminal color indexes.
- `src/components/chat/message-code-block.tsx` mirrors a code-rendering color
  theme where exact colors are content presentation.

## Visual Enforcement

Static checks do not prove layout quality. `web/frontend/tests/ui-smoke.spec.ts`
runs mocked API route and interaction checks at desktop and mobile widths for
the main operational pages. Those tests verify:

- the route renders without console errors;
- primary controls are visible;
- there is no body-level horizontal page overflow at mobile and desktop widths;
- serious and critical axe accessibility violations are absent;
- key dialogs, expandable settings, and the mobile sidebar fit in the viewport.

`web/frontend/tests/collection-visual.spec.ts` adds deterministic collection
baselines for both viewports and themes. Visible time is fixed, animations and
carets are disabled, APIs are mocked, and fonts are ready before capture. CI
uploads the HTML report, traces, failure screenshots, and snapshot diffs when a
browser check fails; it never updates baselines.

For larger user-facing UI changes, reviewers should still inspect desktop and
mobile widths directly, especially dialogs, sheets, popovers, and dense tables.

## Local Commands

```bash
cd web/frontend
pnpm lint
pnpm format
pnpm test:collection-governance
pnpm test:ui:smoke
pnpm test:ui:visual
pnpm test:ui
```

After reviewing an intentional visual change:

```bash
pnpm test:ui:visual:update
```

Repository-wide frontend lint:

```bash
make lint-frontend
make test-frontend-ui
make collection-delta BASE_REF=origin/main HEAD_REF=HEAD
```
