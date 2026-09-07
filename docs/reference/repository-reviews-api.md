# Repository Reviews API

This is the normative launcher HTTP contract for Repository Reviews. The
feature semantics, durable state machines, and model-output schemas are defined
in [Repository Reviews](../features/repository-reviews.md).

## Protocol And Authentication

- The base prefix is `/api/repository-reviews` and responses are JSON except
  successful profile and automation-configuration deletes, which return
  `204 No Content`. Issue-preview and issue-link deletes return JSON envelopes.
- These routes use the launcher's authenticated, trusted single-operator
  boundary. Repository Reviews adds no tenant or role model. Mutations also
  enforce the launcher's same-origin/replay-header checks.
- Mutation requests use `Content-Type: application/json`, reject unknown fields
  and trailing JSON, and accept no query parameters. The general decoded body
  limit is 8 MiB; issue-link proxy bodies are limited to 32 KiB.
- Standard collections accept exactly one each of `query`, `cursor`, and
  `limit`. `limit` is 1–200; omitted uses the collection default. The response
  contains the item key named below, `total`, optional `next_cursor`,
  `canonical_query`, and `query_schema`. A cursor is opaque and bound to its
  resource, normalized query, page size, repository/campaign/generation
  context, and ordering.
- IDs are opaque. Canonical prefixes are `rrpf_*` profile, `rra_*` automation,
  `rrw_*` raw finding, `rdf_*` deduplicated finding, `rrf_*` repository
  finding, and `rid_*` issue preview. Clients must not derive authority from
  a prefix.
- Repository finding ledgers use schema 6 and contain only the canonical
  `rrw_*` → `rdf_*` → `rrf_*` chain. Older ledger schemas and retired finding
  identities are rejected; reads never synthesize, replay, or translate them.
  Opening a pre-schema-6 SQLite store retires each incompatible repository
  ledger and the automation assigned to it. Profiles and automations that do
  not reference a retired ledger remain available.

### Registered Route Manifest

This manifest is exhaustive and uses the exact Go `ServeMux` patterns. Route
parity tests compare registration against these entries.

```text
DELETE /api/repository-reviews/automations/{automation_id}
DELETE /api/repository-reviews/automations/{automation_id}/findings/{finding_id}/issue-link
DELETE /api/repository-reviews/automations/{automation_id}/issues/{draft_id}
DELETE /api/repository-reviews/profiles/{profile_id}
GET /api/repository-reviews/automation-options
GET /api/repository-reviews/automations
GET /api/repository-reviews/automations/{automation_id}
GET /api/repository-reviews/automations/{automation_id}/commit-options
GET /api/repository-reviews/automations/{automation_id}/file-attributions
GET /api/repository-reviews/automations/{automation_id}/finding-health
GET /api/repository-reviews/automations/{automation_id}/findings
GET /api/repository-reviews/automations/{automation_id}/findings-processing
GET /api/repository-reviews/automations/{automation_id}/findings-processing/sources/{source_id}
GET /api/repository-reviews/automations/{automation_id}/findings/{finding_id}
GET /api/repository-reviews/automations/{automation_id}/findings/{finding_id}/sources
GET /api/repository-reviews/automations/{automation_id}/findings/{finding_id}/sources/{source_id}
GET /api/repository-reviews/automations/{automation_id}/issues
GET /api/repository-reviews/automations/{automation_id}/issues/{draft_id}
GET /api/repository-reviews/automations/{automation_id}/raw-findings
GET /api/repository-reviews/automations/{automation_id}/raw-findings/{source_id}
GET /api/repository-reviews/automations/{automation_id}/repository-findings
GET /api/repository-reviews/automations/{automation_id}/repository-findings/{finding_id}
GET /api/repository-reviews/profiles
GET /api/repository-reviews/profiles/{profile_id}
PATCH /api/repository-reviews/automations/{automation_id}
PATCH /api/repository-reviews/automations/{automation_id}/issues/{draft_id}
PATCH /api/repository-reviews/automations/{automation_id}/repository-findings/{repository_finding_id}
PATCH /api/repository-reviews/profiles/{profile_id}
POST /api/repository-reviews/automations
POST /api/repository-reviews/automations/{automation_id}/findings-processing/retry
POST /api/repository-reviews/automations/{automation_id}/findings-processing/sources/{source_id}/retry
POST /api/repository-reviews/automations/{automation_id}/findings/status
POST /api/repository-reviews/automations/{automation_id}/findings/{finding_id}/issue-link
POST /api/repository-reviews/automations/{automation_id}/findings/{finding_id}/issue-link/candidates
POST /api/repository-reviews/automations/{automation_id}/findings/{finding_id}/post
POST /api/repository-reviews/automations/{automation_id}/issues/generations
POST /api/repository-reviews/automations/{automation_id}/issues/publish
POST /api/repository-reviews/automations/{automation_id}/issues/{draft_id}/publish
POST /api/repository-reviews/automations/{automation_id}/issues/{draft_id}/regenerate
POST /api/repository-reviews/automations/{automation_id}/pause
POST /api/repository-reviews/automations/{automation_id}/purge-history
POST /api/repository-reviews/automations/{automation_id}/raw-findings/{source_id}/retry
POST /api/repository-reviews/automations/{automation_id}/repository-findings/validations
POST /api/repository-reviews/automations/{automation_id}/repository-findings/{repository_finding_id}/duplicates
POST /api/repository-reviews/automations/{automation_id}/repository-findings/{repository_finding_id}/sync
POST /api/repository-reviews/automations/{automation_id}/restart
POST /api/repository-reviews/automations/{automation_id}/resume
POST /api/repository-reviews/automations/{automation_id}/start
POST /api/repository-reviews/profiles
```

## Request Bodies

All fields shown are required unless marked optional. Version values are
positive integers except `expected_repository_version`, which is zero for an
assignment with no ledger and otherwise positive. The complete profile field
defaults and bounds are in the feature contract.

| Name | JSON object |
| --- | --- |
| `ProfileCreate` | Writable profile fields: `name`, `review_focus`, `scope_policy`, `reviewer_model`, optional `deduplication_model`, optional `deduplication_similarity_threshold`, optional `deduplication_candidate_limit`, optional `issue_writer_model`, optional `issue_prompt`, optional `account_ref`, `force`, optional `auto_continue`, `max_files_per_run`, `max_content_bytes`, `max_parallel_children`, optional `assignment_timeout_seconds`, and `budget` |
| `ProfileUpdate` | `ProfileCreate` plus `expected_version`; response metadata is forbidden |
| `ProfileDelete` | `{ "expected_version": 7 }` |
| `AutomationCreate` | `{ "repository": "owner/repository", "profile_id": "rrpf_...", "branch": "main" }`; `branch` is optional/blank for the advertised default; display name is materialized from repository/profile |
| `AutomationUpdate` | `AutomationCreate` fields plus `expected_version`; repository remains uniquely assigned |
| `AutomationAction` | `{ "expected_version": 7, "commit_sha": "<optional full SHA>", "run_id": "<optional active run ID>" }`; only action-relevant optional fields are accepted |
| `PurgeOrRemove` | `{ "expected_version": 7, "expected_repository_version": 12, "expected_ledger_fence": "rplf_...", "confirm_repository": "owner/repository" }`; confirmation must equal the displayed normalized identity and the opaque fence must equal the detail capability exactly |
| `RunFindingRetry` | `{ "finding_ids": ["..."] }` with explicit unique occurrence IDs |
| `ProcessingRetry` | `{ "source_ids": ["rrw_..."] }` with 1–200 explicit unique failed raw-source IDs |
| `EmptyMutation` | `{}` for single-source retry or issue synchronization |
| `RepositoryLifecycle` | `{ "lifecycle": "open|dismissed", "expected_version": 3 }` |
| `DuplicateDecision` | `{ "candidate_id": "rrf_...", "decision": "distinct|merge", "expected_provisional_version": 3, "expected_candidate_version": 4 }`; candidate version is required for merge |
| `ResolutionChecks` | `{ "repository_finding_ids": ["rrf_..."] }` with 1–50 explicit unique IDs |
| `IssueGeneration` | `{ "generation_id": "rrig_...", "finding_ids": ["<eligible occurrence anchor>"], "instructions_mode": "default|custom", "instructions": "<optional>" }`; 1–200 unique eligible canonical actions, custom instructions at most 16 KiB |
| `IssueEdit` | `{ "title": "...", "body": "...", "labels": ["bug"], "expected_version": 3 }`; title ≤256 bytes, body ≤60 KiB, labels are bounded |
| `IssueRegenerate` | `{ "expected_version": 3 }` |
| `IssueDelete` | `{ "expected_version": 3, "confirmed": true }` |
| `IssuePublish` | `{ "expected_version": 3, "confirmed": true }` |
| `IssueBatchPublish` | `{ "issues": [{ "id": "rid_...", "expected_version": 3 }], "confirmed": true }` with 1–200 explicit unique previews |
| `DirectPost` | `{ "expected_version": 3, "instructions": "<optional>" }`; instructions at most 16 KiB |
| `IssueCandidates` | `{ "expected_version": 3 }` |
| `IssueLink` | `{ "issue_url": "https://github.com/owner/repository/issues/1", "expected_version": 3, "confirmed": true, "replace": false }` |
| `IssueUnlink` | `{ "expected_version": 3, "confirmed": true }` |

## Profiles, Configuration, And Lifecycle Routes

`{aid}` means an automation ID and `{pid}` a profile ID.

| Method and path | Request/query | Success response |
| --- | --- | --- |
| `GET /profiles` | Standard collection | `200 {profiles,total,next_cursor?,canonical_query,query_schema}` |
| `POST /profiles` | `ProfileCreate` | `201 {profile}` |
| `GET /profiles/{pid}` | None | `200 {profile}` |
| `PATCH /profiles/{pid}` | `ProfileUpdate` | `200 {profile}` |
| `DELETE /profiles/{pid}` | `ProfileDelete` | `204` |
| `GET /automations` | Standard collection | `200 {automations,total,next_cursor?,canonical_query,query_schema}`; entries are compact public projections |
| `POST /automations` | `AutomationCreate` | `201 {automation}` |
| `GET /automations/{aid}` | None | `200 {automation,repository?,capabilities}` |
| `PATCH /automations/{aid}` | `AutomationUpdate` | `200 {automation}` |
| `DELETE /automations/{aid}` | `PurgeOrRemove` | `204`; configuration and its canonical repository-review ledger are removed |
| `POST /automations/{aid}/purge-history` | `PurgeOrRemove` | `200 {automation,outcome:"history_purged"}`; automation is fresh and idle and its canonical repository-review ledger is removed |
| `POST /automations/{aid}/start` | `AutomationAction` | `202 {automation,outcome:"started"}` |
| `POST /automations/{aid}/pause` | `AutomationAction` | `202 {automation}` |
| `POST /automations/{aid}/resume` | `AutomationAction` | `202 {automation,outcome:"started"}` |
| `POST /automations/{aid}/restart` | `AutomationAction` | `202 {automation,outcome:"started"}` |
| `GET /automations/{aid}/commit-options` | None | `200 {expected_version,remembered:{sha,short_sha,url?},latest:{sha,short_sha,url?},newer_commit_available}` |
| `GET /automation-options` | None | `200 {models,accounts,limits_error?}`; never returns credentials |

`capabilities` contains existing issue-action fields plus
`can_purge_history`, `can_remove_repository`, ordered `purge_blockers`, and
`purge_summary`. Each blocker is exactly `{code,count,message}`. Summary is
`{repository_version,ledger_fence,raw_findings,deduplicated_findings,repository_findings,issue_previews,external_issue_associations}`.
For a configured assignment without a ledger, the summary contains version and
counts of zero plus a deterministic empty-inventory `ledger_fence`,
`can_purge_history` is `false`, `can_remove_repository` is `true` when otherwise
quiescent, and `purge_blockers` is an empty array.
Capability booleans and blockers are present even when false/empty.
`ledger_fence` binds the canonical repository identity and ledger version, so
an identity or version change invalidates stale confirmation.
`retention_unavailable` is the fail-closed blocker when inventory cannot be read.
In that state `purge_summary` is omitted; unavailable counts are never returned
as zero.
`external_issue_associations` counts unique stored external issue URLs, including
conflict URLs; deletion never dereferences or mutates them.
Capabilities are advisory: mutations re-evaluate them while holding the store
lock.
Purge/removal never calls GitHub and never deletes profiles, discussion
threads, or generic workflow-run records; those resource owners keep their own
retention policies. It deletes only the migration-audited, digest-matching
imported automation archive represented by the configuration; skipped,
uncounted, drifted, and profile archives remain.

## Collection Query Schemas

Every field is sortable. Enum suggestions are returned in `query_schema`; raw
enum values remain stable beneath UI labels.

| Collection | Fields | Default order |
| --- | --- | --- |
| Profiles | `id,name,account,reviewer,deduplicator,deduplication_threshold,deduplication_candidates,issue_writer,force,auto_continue,files,parallel,version,updated` | `name ASC` |
| Automations | `id,name,repository,branch,status,progress,reviewed,raw_findings,findings,updated` | `updated DESC` |
| File attributions | `path,commit,blob,focus,agent,reviewer,account,model,source,attempts,runs,latest` | `path ASC, focus ASC, reviewer ASC` |
| Findings (`rdf_*`) | `id,repository,title,path,symbol,severity,status,run_status,association,contributors,sources,mapped,created,updated` | `severity DESC, updated DESC` |
| Raw findings (`rrw_*`) | `id,path,severity,title,symbol,model,reviewer,deduplication_state,disposition,finding,created,updated` | `created DESC` |
| Findings processing | `id,campaign,title,path,symbol,severity,model,reviewer,state,disposition,created,updated` | `updated DESC` |
| Repository findings (`rrf_*`) | `id,repository,title,path,symbol,severity,match,lifecycle,issue,validation,occurrences,commits,created,updated` | `severity DESC, updated DESC` |
| Issue previews | `id,repository,title,generation,state,origin,publishable,findings,created,updated` | `updated DESC` |

## Evidence And Processing Routes

| Method and path | Request/query | Success response |
| --- | --- | --- |
| `GET /automations/{aid}/file-attributions` | Standard collection | `200 {file_attributions,total,next_cursor?,canonical_query,query_schema}` |
| `GET /automations/{aid}/finding-health` | None | `200 {run_findings,repository_findings,findings_processing,updated_at}` |
| `GET /automations/{aid}/findings` | Standard collection | `200 {automation,repository?,findings,total,next_cursor?,canonical_query,query_schema,capabilities,findings_processing}` with completed current-campaign `rdf_*` only |
| `GET /automations/{aid}/findings/{fid}` | None | `200 {automation,repository,finding,raw_source_total,contexts,repository_finding?,capabilities}` |
| `GET /automations/{aid}/findings/{fid}/sources` | `offset`, `limit` paging | `200 {automation,repository,finding_id,sources,offset,total,next_offset?}` |
| `GET /automations/{aid}/findings/{fid}/sources/{sid}` | None | Canonical raw-source detail envelope |
| `GET /automations/{aid}/raw-findings` | Standard collection | `200 {automation,repository?,raw_findings,total,next_cursor?,canonical_query,query_schema,findings_processing}` |
| `GET /automations/{aid}/raw-findings/{sid}` | None | `200 {automation,repository?,source,context?,finding?}` |
| `POST /automations/{aid}/raw-findings/{sid}/retry` | `EmptyMutation` | `202 {automation,repository,source,findings_processing}` |
| `GET /automations/{aid}/findings-processing` | Standard collection | `200 {automation,repository?,raw_findings,total,next_cursor?,canonical_query,query_schema,capabilities,findings_processing}` |
| `POST /automations/{aid}/findings-processing/retry` | `ProcessingRetry` | `202 {retried_ids,failures,findings_processing,health}` |
| `GET /automations/{aid}/findings-processing/sources/{sid}` | None | Canonical repository-wide raw-source detail envelope |
| `POST /automations/{aid}/findings-processing/sources/{sid}/retry` | `EmptyMutation` | Single-source form of the processing retry response |
| `POST /automations/{aid}/findings/status` | `RunFindingRetry`; returns `202 {automation,repository,findings}` |

## Repository Findings And Issue Routes

| Method and path | Request/query | Success response |
| --- | --- | --- |
| `GET /automations/{aid}/repository-findings` | Standard collection | `200 {automation,repository?,repository_findings,total,next_cursor?,canonical_query,query_schema,capabilities}` |
| `GET /automations/{aid}/repository-findings/{rfid}` | None | `200 {automation,repository,finding,action_finding,repository_finding,occurrences,possible_duplicate_findings,contexts,issue?,capabilities}` |
| `PATCH /automations/{aid}/repository-findings/{rfid}` | `RepositoryLifecycle` | `200 {automation,repository,repository_finding}` |
| `POST /automations/{aid}/repository-findings/{rfid}/duplicates` | `DuplicateDecision` | `200 {automation,repository,repository_finding}`; a merge also returns the retained identity |
| `POST /automations/{aid}/repository-findings/validations` | `ResolutionChecks` | `202 {automation,repository,validation_jobs}` |
| `POST /automations/{aid}/repository-findings/{rfid}/sync` | `EmptyMutation` | `200 {automation,repository,repository_finding}` |
| `GET /automations/{aid}/issues` | Standard collection plus optional `generation_id` | `200 {automation,repository?,issues,total,next_cursor?,canonical_query,query_schema,capabilities}` |
| `POST /automations/{aid}/issues/generations` | `IssueGeneration` | `200 {automation,repository,generation_id,issues,results}`; per-finding failures do not erase successes |
| `GET /automations/{aid}/issues/{did}` | None | `200 {automation,repository,issue,finding?,findings,capabilities}` |
| `PATCH /automations/{aid}/issues/{did}` | `IssueEdit` | `200 {automation,repository,issue}` |
| `DELETE /automations/{aid}/issues/{did}` | `IssueDelete` | `200 {automation,repository,outcome:"deleted"}` |
| `POST /automations/{aid}/issues/{did}/regenerate` | `IssueRegenerate` | `200 {automation,repository,issue,result}`; failed regeneration retains last good content |
| `POST /automations/{aid}/issues/{did}/publish` | `IssuePublish` | `200` posted/reconciled or `202` unknown publication envelope |
| `POST /automations/{aid}/issues/publish` | `IssueBatchPublish` | `200 {automation,repository,results}` with one result per requested preview |
| `POST /automations/{aid}/findings/{fid}/issue-link/candidates` | `IssueCandidates` | `200 {automation,finding,candidates,generator_model,generator_account,repository?,discovered_issue?}` |
| `POST /automations/{aid}/findings/{fid}/issue-link` | `IssueLink` | `200 {automation,repository,finding,issue}` |
| `DELETE /automations/{aid}/findings/{fid}/issue-link` | `IssueUnlink` | `200 {automation,repository,finding}` |
| `POST /automations/{aid}/findings/{fid}/post` | `DirectPost` | Posted/reconciled result over the exact saved generated preview |

Candidate ranking may auto-link only the first result at score ≥95 with at
least four matching anchors and no conflicting anchors, followed by exact
same-repository re-fetch. Every other result is read-only until explicit link.
For the finding-anchored paths, `{fid}` is an eligible immutable `rdf_*`
occurrence used to anchor the canonical repository-finding action; it is not an
alternative repository-finding identity.

## Errors

Errors are bounded JSON: `{ "code": "stable_code", "message": "safe message" }`.
Collection query errors may also include a byte `position` and query
suggestions. Publication and purge errors may add their safe blocker arrays.
Raw provider errors, prompts, source, credentials, internal paths, and
checkpoint identities never appear.

| Status | Stable codes and meaning |
| --- | --- |
| `400` | `invalid_request`, `invalid_repository_review_profile`, `invalid_repository_review_automation`, `invalid_collection_request`, `invalid_query`, `invalid_cursor`, `invalid_page_limit`, `invalid_generation_id`, `invalid_issue_url`, or another route-specific invalid-input code; validation fails before mutation/effect |
| `404` | `not_found`, `repository_review_profile_not_found`, or `repository_review_history_not_found` |
| `409` | `stale_repository_review`, `stale_repository_review_profile`, `stale_repository_review_automation`, `repository_review_repository_assigned`, `repository_review_profile_assigned`, `repository_review_profile_active`, `repository_review_commit_selection_required`, `repository_review_purge_blocked`, or `repository_review_purge_in_progress` |
| `502` | `invalid_gateway_response` or a safe external operation failure where the external response was invalid |
| `503` | `repository_review_unavailable`, `repository_review_profile_unavailable`, `repository_review_automation_unavailable`, `issue_search_unavailable`, `issue_ranking_unavailable`, `issue_link_unavailable`, `issue_sync_unavailable`, `publication_unavailable`, or another safe dependency-unavailable code |

For `repository_review_purge_blocked`, the response is
`{code,message,purge_blockers}` and the ordered blockers use the same
`{code,count,message}` shape as capabilities. `repository_review_history_not_found`
means purge was requested for a configured assignment without an authoritative
history ledger. Confirmation mismatch and stale automation or repository
versions return `409 stale_repository_review_automation` without mutation.
While a durable primary intent or repository-identity fence exists,
`repository_review_purge_in_progress` rejects affected automation/ledger reads
and mutations until startup recovery completes; clients never receive a
partially purged projection.
