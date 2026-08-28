# Development Workspaces

PicoClaw turns a GitHub issue, a feature brief, or an existing pull request into
one durable development workspace. Open **Development** in the launcher or go
to `/development`.

This is a breaking replacement for Pull Request Workspaces. Workspace IDs use
`devw_`; the launcher and API expose only `/development` and
`/api/development-workspaces`. Old `prw_` IDs and `/pull-requests` URLs are not
translated or redirected.

## Start Work

Open **Development → New work** and choose exactly one workflow:

- **Implement feature** creates a new implementation and, after authorization,
  a new draft pull request. Start from either one GitHub issue URL or one written
  brief. Briefs also require one verified configured repository.
- **Pick up PR** continues one existing GitHub pull request. It accepts only the
  pull-request URL and never asks for an issue or brief.

Only the selected form is mounted. Switching workflows clears fields from the
previous workflow, and the API strictly rejects mixed issue, brief, and PR
payloads.

### Configured repositories

Brief intake lists only repositories verified for implementation. Manage them
at `/development/repositories`. Repository identity is provider-backed; a
display name is not authority to clone, push, or create a PR.

The repository page is a server-paged List/Table/Grid collection. Use its query
field to filter `repository`, `configuration`, or `default_branch`; compact List
is the default. Add an assignment at `/development/repositories/new`, open its
backend-issued direct link at `/:id`, and edit it at `/:id/edit`. Explicitly
selected assignments may be deleted together after confirmation. Removing an
assignment preserves its repository descriptor and only restores the default
Workflow configuration for future work. An assignment that disappears during a
bulk request remains selected with a safe `not_found` result, while a stale
config revision rejects the whole request so you can refresh before retrying.

## Lifecycle

Initial work advances automatically until it needs a decision or reaches a
failure boundary:

1. **Intake** resolves the one selected source and freezes provider and
   repository evidence.
2. **Charter** drafts the goal, one primary work type, acceptance criteria,
   included areas, exclusions, and non-goals.
3. **Planning** classifies feature work, or **Review** examines an existing PR.
4. **Triage** applies repository scope policy to each item.
5. **Implementation** edits a private retained Git line.
6. **Validation** runs configured local checks against the exact candidate.
7. **Completion audit** checks required work and searches for scope drift.
8. **Publication** waits for its authorization and performs a create-only push.
9. **Complete** records the published result.

Issue-based draft PRs include an issue-closing reference. Brief-based draft PRs
include the bounded brief summary. Feature implementation creates a draft PR;
it does not merge, close an issue directly, or mark a PR ready for review.

The workspace version, provider head, retained-line version, candidate SHA, and
publication payload are fences. A conflict means the underlying work changed;
reload instead of replaying a stale mutation.

## Scope And Deferral

Each repository selects one Workflow configuration. Its scope-disposition
policy has a strict default plus optional overrides for `fix`, `feature`,
`refactor`, `documentation`, and `test`.

```json
{
  "scope-disposition": {
    "default": { "mode": "strict", "prompt": "" },
    "by-type": {
      "fix": {
        "mode": "relaxed",
        "prompt": "Keep changes inside request retry handling."
      }
    }
  }
}
```

- **Strict** includes small, type-compatible exact work, gates exact work that
  exceeds the size bound, and defers adjacent or follow-up work.
- **Relaxed** may include a relevant `XS` or `S` necessary-adjacent or related
  item at confidence `0.80` or higher.
- Type-incompatible and unrelated work never becomes safe because it is small.
- CI/CD, deployment, release, dependency upgrades, migrations, generated code,
  and broad cleanup default to deferred unless the charter explicitly targets
  that domain.
- A custom prompt may tighten or clarify relevance. It cannot bypass hard
  scope, type, size, or confidence boundaries.

Scope policy is configured under `/development/workflow-configurations` and is
separate from follow-up issue publication policy.

Workflow configurations are also a server-paged List/Table/Grid collection.
They default to `ORDER BY name ASC` and summarize default status, binding count,
and deferred-issue mode. Create one at
`/development/workflow-configurations/new`, inspect `/:id`, and use `/:id/edit`
for gate bindings and scope/deferred policy. The lifecycle diagram opens a gate
on that routed editor with optional `flow` and `gate` context. The former
`?config=<id>` URL is a hard cutover and is not redirected; use the item route
instead. The built-in configuration, the current default, and configurations
referenced by repository assignments cannot be deleted; referenced failures
name only bounded repository labels. Shared nudging and size thresholds remain
lifecycle settings, not collection rows.

## Required Actions And Notifications

PicoClaw records required attention as durable notifications rather than
opening unsolicited dialogs. Notifications accumulate at `/notifications` and
deep-link to the exact workspace panel and entity.

Notifications cover charter ambiguity, scope exceptions, scope-changing
steering, exhausted implementation or validation blockers, unknown provider
outcomes, and publication approval. Resolving the underlying gate or provider
state resolves its notification automatically.
For a charter ambiguity, the deep link opens the exact question and an editable
charter. Save a clarified revision or explicitly accept the draft as-is; the
automation then resumes without a separate late source-attachment step.

Inbox actions include:

- mark read or unread;
- snooze and clear snooze;
- archive a resolved notification;
- move to previous or next result without losing the current query;
- create, pin, rename, duplicate, select, and delete saved views.

Built-in views include **Needs action**, **Unread**, **Snoozed**, **Resolved**,
and **All**.

### Advanced notification queries

Simple filters produce the same query language used by the advanced editor:

```text
status = open
AND priority IN (critical, high)
AND repository ~ "owner/"
ORDER BY priority DESC, updated DESC
```

Allowlisted fields are `status`, `read`, `snoozed`, `priority`, `reason`,
`repository`, `workspace`, `intent`, `source`, `phase`, `created`, `updated`,
and `text`. Queries support `=`, `!=`, `IN`, `NOT IN`, `~`, `!~`, time
comparisons, `AND`, `OR`, `NOT`, parentheses, quoted strings, ISO timestamps,
relative dates such as `-7d`, and up to three `ORDER BY` fields.

The server parses and type-checks the query; it never interpolates query text
into SQL. Limits are 4 KiB, nesting depth 16, 50 predicates, and 100 values per
`IN`. Pagination cursors are bound to normalized filter and sort semantics.

## Mobile Notifications

The launcher is an installable PWA. Open notification settings and explicitly
choose **Enable mobile notifications** to request browser permission and
register that device. Permission is never requested on page load.

OS push is reserved for newly opened, unsnoozed high- or critical-priority
actions. Default lock-screen content is privacy-minimal and contains only the
reason category plus a notification ID. Repository names appear only when the
operator enables that setting. Opening a push authenticates the launcher before
fetching detail.

Web Push requires HTTPS and browser/PWA support. Unsupported browsers retain
the complete in-app inbox. Devices can be renamed, disabled, or revoked.

## Ask And Steer

Every workspace has a revisioned development conversation:

- **Ask** returns a read-only answer. When a candidate exists, the request is
  fenced to that exact candidate and includes bounded candidate evidence.
- **Steer** queues an instruction for the next safe implementation boundary.
  It does not interrupt an active file operation. Applying steering invalidates
  stale validation or completion evidence as needed.

Steering does not itself grant scope, Git, provider, or publication authority.
A scope-expanding instruction becomes a required action instead of silently
changing the candidate.

On mobile, chat stays behind an explicit bottom-sheet button so it does not
cover lifecycle evidence until requested.

## Inspect Changes

Workspace views are **Overview**, **Changes**, **Files**, and **Activity**.

**Changes** lists changed paths. **Files** browses the retained candidate tree.
Selecting a path lazy-loads Monaco's read-only diff editor:

- base and candidate appear side-by-side on wide screens and inline on narrow
  screens;
- the launcher theme controls the editor theme;
- route state records the selected path and exact candidate revision;
- models are disposed when path or revision changes;
- **Accessible text view** exposes a read-only text-area fallback, also used if
  Monaco or its worker cannot load.

The browser cannot edit files. Server reads are limited to exact retained Git
objects and reject stale fences, traversal, absolute paths, symlinks,
submodules, binary data, invalid UTF-8, and oversized blobs.

## Publication And Recovery

Feature implementation pushes a deterministic create-only branch and creates a
draft PR. Existing-PR pickup pushes only the verified writable PR head. Neither
flow authorizes merge.

Push and PR creation are separate frozen effects. If a provider response is
ambiguous, PicoClaw records an unknown outcome and raises a notification.
Choose **Reconcile outcome** to inspect exact remote state. Do not retry the
external action blindly; reconciliation uses the exact ref/tip or PR marker.

## Troubleshooting

- **No repositories in brief mode:** verify one under
  `/development/repositories` and confirm implementation capability.
- **Needs action but no modal:** open `/notifications`; attention is deliberately
  inbox-driven.
- **Code unavailable:** the candidate may not exist yet, the route may name a
  stale revision, or the retained Git fence changed. Return to the current
  workspace revision.
- **Message conflict:** another conversation write advanced its revision. Reload
  the conversation and resend against the new state.
- **Provider outcome unknown:** reconcile from the required-action panel; never
  create another branch or PR to guess the result.
- **No OS push:** confirm HTTPS, browser support, permission, enabled device,
  priority, and snooze state. The in-app inbox remains authoritative.
