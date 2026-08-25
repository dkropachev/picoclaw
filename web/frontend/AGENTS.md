# Frontend Agent Instructions

These rules apply to files under `web/frontend/`.

## Scope

- Treat UI code as an auxiliary surface for the product feature it exposes.
- When a UI behavior changes, update the owning `docs/features/*.md` spec for that product capability. Do not use a broad UI-only spec to satisfy feature ownership.
- Use `docs/design/frontend-guidelines.md` for rationale, examples, and enforcement details.
- For collection work, follow `docs/design/collection-ui-system.md` and register the surface in `collection-surfaces.json`.
- A frontend-only UI specialist must also follow `UI_AGENT.md`, including its backend-contract preconditions and evidence handoff.

## Implementation Rules

- Use the existing React, TanStack Router, Jotai, shadcn/Radix, Tailwind, and i18n patterns already present in `src/`.
- Put HTTP calls in `src/api/**`. Components, hooks, routes, stores, and feature controllers must call API helpers instead of calling `fetch` directly.
- Use `launcherFetch` for launcher-authenticated same-origin requests unless an auth flow explicitly needs plain `fetch` to avoid redirects.
- Prefer shared controls from `src/components/ui/**` and shared form helpers from `src/components/shared-form.tsx`.
- Use design tokens such as `bg-background`, `text-foreground`, `border-border`, `text-muted-foreground`, `bg-card`, `text-primary`, and semantic variants before raw palette values.
- Hardcoded colors are allowed only for data rendering where the exact palette is part of the content, such as ANSI log colors or code-block themes.
- Inline `style` is allowed only for runtime-measured geometry, dynamic dimensions, or values that cannot be represented as stable Tailwind classes. Add a nearby `ui-rule-allow dynamic-style` comment explaining why.
- Keep page layouts dense and operational. Avoid marketing-style hero sections, decorative nested cards, and UI text that explains how to use obvious controls.
- Use icon buttons for clear icon actions and provide `aria-label` or `title` where the visible label is absent.
- Keep responsive layout stable: avoid horizontal overflow, overlapping controls, and text that can escape buttons, cards, panels, sidebars, or dialogs.
- Administrative collections must use the shared collection subsystem. Do not add feature-local collection shells, filters, view switches, selection stores, pagination controllers, cards, or tables.
- Keep one primary resource collection on a route and put creation, detail, and editing on dedicated stable routes.

## Validation

Run these before handing off frontend UI changes:

```bash
cd web/frontend
pnpm lint
pnpm format
pnpm test:collection-governance
pnpm test:ui
```

From the repository root, `make lint-frontend` installs locked dependencies and
runs the lint and formatting checks. Use `make test-frontend-ui` for the mocked
browser smoke suite.
Use `make collection-delta BASE_REF=<base> HEAD_REF=<head>` for the collection
debt guard. Update visual baselines locally only with
`make update-frontend-visuals`; CI never rewrites them.
