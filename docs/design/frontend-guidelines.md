# Frontend Guidelines

## Decision

PicoClaw UI code remains part of the product feature it exposes. The launcher
frontend also has cross-cutting implementation rules so agents and contributors
build consistent, secure, and maintainable UI surfaces.

The scoped guidance lives in `web/frontend/AGENTS.md`. Automated checks live in
`web/frontend/scripts/lint-ui-rules.mjs` and run through `pnpm lint` and
`make lint-frontend`.

## Product Ownership

Frontend code is not a generic standalone feature. Feature ownership follows
behavior:

| UI Surface | Owning Feature Spec |
| --- | --- |
| Chat page, Pico websocket UI, channel pages | `docs/features/chat-channels.md` |
| Tool configuration and tool library pages | `docs/features/tool-execution.md` |
| Session logs and transcript history pages | `docs/features/session-memory.md` |
| Model, config, OAuth, auth, startup, update, and app shell management | `docs/features/launcher-management.md` |
| Skill and hub pages | `docs/features/skills.md` |

When a UI behavior changes, update the owning feature spec before or with the
code change. A cross-cutting UI spec should be reserved for shared app shell,
navigation, design-system, theme, loading, and error behavior, and it must not
claim ownership of all frontend source.

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

## Static Enforcement

`web/frontend/scripts/lint-ui-rules.mjs` enforces deterministic rules that are
cheap enough for every pull request:

| Rule | Reason |
| --- | --- |
| No direct `fetch(...)` outside `src/api/**`. | Keeps auth redirects, credential handling, and error semantics centralized. |
| No inline `style=` without nearby `ui-rule-allow dynamic-style`. | Prevents layout drift while allowing measured geometry and dynamic dimensions. |
| No raw hex colors outside approved rendering exceptions. | Keeps UI color choices tied to tokens and avoids one-off palettes. |

Approved hardcoded-color exceptions are intentionally narrow:

- `src/lib/ansi-log.ts` maps ANSI terminal color indexes.
- `src/components/chat/message-code-block.tsx` mirrors a code-rendering color
  theme where exact colors are content presentation.

## Visual Enforcement

Static checks do not prove layout quality. For user-facing UI changes, reviewers
should still inspect desktop and mobile widths. Browser smoke tests should be
added when a route has stable mocked API fixtures. Those tests should verify:

- the route renders without console errors;
- primary controls are visible and clickable;
- there is no horizontal page overflow at mobile and desktop widths;
- dialogs, sheets, and popovers fit in the viewport;
- screenshots are stable enough to catch obvious regressions.

## Local Commands

```bash
cd web/frontend
pnpm lint
```

Repository-wide frontend lint:

```bash
make lint-frontend
```

`pnpm format` remains available as a whole-tree formatting audit. It is not part
of the pull-request gate until the existing frontend formatting baseline is
clean.
