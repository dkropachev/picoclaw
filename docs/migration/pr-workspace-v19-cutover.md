# PR Workspace V19 Cutover

Eventing schema v19 replaces the separate PR review and PR-development stores
with one PR-workspace aggregate. This is intentionally destructive and has no
backward-compatibility mode.

## Before Upgrading

1. Stop every launcher and gateway process that can open the eventing database.
2. Copy the eventing database and its WAL/SHM companions if the old PR records
   must be retained for audit.
3. Record or export any legacy review findings or development results that are
   still needed. The new runtime has no importer for them.
4. Upgrade all processes together. Old and new binaries must not share the
   database.

## What The First V19 Open Does

For a database declaring schema v18, PicoClaw first validates the complete v18
schema. If validation succeeds, it drops every table whose name begins with
`pr_review_` or `pr_development_`, creates the unified v19 PR tables, validates
the retained generic event/workflow tables, and commits the version change in
one schema transaction.

The cutover does not translate `prc_`, `pdc_`, or private development-thread
identities into `prw_` workspaces. It also does not translate old review
attention policies into lifecycle gate profiles.

Databases declaring v1 through v17 are rejected. Upgrade an archival copy with
the older release chain if historical inspection is required; do not point the
new runtime at that copy until it is a valid v18 database and the destructive
cutover is acceptable.

## After Upgrading

- Open `/pull-requests`; `/reviews` is not a redirect.
- Create or resolve each active pull request as a new workspace.
- Recreate any needed charter, finding disposition, or deferred follow-up from
  the archived information.
- Review `pr_lifecycle` gate profiles, nudge bounds, repository assignments, and
  size thresholds.
- Verify review, branch-push, and issue provider capabilities independently.

## Rollback

There is no in-place downgrade. Stop all new processes and restore the complete
pre-upgrade database copy, including matching WAL/SHM state when applicable,
before starting an older binary. A v19 database must not be opened by an older
release.
