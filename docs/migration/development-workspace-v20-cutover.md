# Development Workspace V20 Cutover

Eventing schema v20 replaces v19 pull-request workspaces with development
workspaces and durable notification state. This change is intentionally
destructive and has no compatibility mode.

Development workspaces use `devw_` identities and the canonical
`/development`, `/api/development-workspaces`, and
`/runtime/eventing/development-workspaces` surfaces. Old `prw_` identities,
`/pull-requests`, `/api/pr-workspaces`, and
`/runtime/eventing/pr-workspaces` are neither imported nor redirected.

## Before Upgrading

1. Stop every launcher and gateway process that can open the eventing database.
2. Copy the database and its WAL/SHM companions if old PR-workspace records are
   needed for audit.
3. Export any v18 review/development or v19 PR-workspace result that must remain
   human-readable. V20 has no workspace-row importer.
4. Upgrade every process together. Mixed v19/v20 access is unsupported.

## What The First V20 Open Does

The cutover runs inside one immediate SQLite transaction and retains generic
event inbox, routing, dispatch, and workflow-revision rows.

- A new database creates the retained generic event/workflow core plus the v20
  development-workspace and notification schema.
- A valid v18 database is fully validated first. PicoClaw drops the legacy
  `pr_review_*` and `pr_development_*` tables and creates the v20 schema.
- A v19 database drops every table in the old `pr_*` workspace namespace, then
  creates clean v20 development-workspace, notification, saved-view, and push
  subscription state.
- A v20 database is validated without attempting repair.

The transaction records `PRAGMA user_version = 20` only after schema creation
and validation succeed. A validation, drop, creation, or commit failure rolls
back the complete cutover.

Versions 1 through 17 are rejected. A version newer than 20 is rejected as too
new. Corrupt v18/v19/current state fails closed rather than being blessed by a
destructive migration.

## What Is Not Migrated

The cutover does not translate:

- v18 review/development cases or private development threads;
- v19 `prw_` aggregates, charters, findings, gates, publications, or activity;
- old attention records into development notifications;
- issue/PR links into `devw_` identities;
- waiting Gate V2 tasks or gate-decision records into generic V3
  `field-values`;
- old routes, browser history, API payloads, or request IDs.

Configuration remains subject to the current strict config schema. Recreate or
verify repository descriptors, repository assignments, Workflow
configurations, scope-disposition rules, follow-up issue policy, and push
notification devices against the new runtime.

## After Upgrading

- Open `/development` and create new work through exactly one intake mode:
  implement an issue or brief, or pick up one existing PR.
- Recreate any bookmarked client-side portfolio filter as the canonical `q`
  collection query. Unknown legacy filter parameters are removed; they are not
  redirected or interpreted.
- Verify repositories at `/development/repositories` before using brief intake.
- Review strict/relaxed per-type scope policy at
  `/development/workflow-configurations`. Old `?config=` editor links are not
  redirected; open the configuration's `/:id/edit` route.
- Open `/notifications`, recreate any desired saved views, and explicitly
  enable each mobile Web Push device.
- Verify issue read, PR read, repository verification, branch push, and draft-PR
  creation capabilities independently.
- Keep the archived pre-upgrade database offline; do not point v20 at it merely
  for historical inspection.

## Rollback

There is no in-place downgrade. Stop every v20 process and restore the complete
pre-upgrade database copy, including matching WAL/SHM files, before starting an
older binary. A v20 database must not be opened by a v19-or-earlier release.
