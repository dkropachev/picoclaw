# Pull Request Workspaces

PicoClaw handles review and implementation as phases of one pull-request
workspace. Open **Pull requests** in the launcher, or navigate directly to
`/pull-requests`.

There is no compatibility view for the former review cases or PR-development
cases. A workspace has one `prw_` identity, one verified provider snapshot, one
charter history, one set of findings and corrections, and one audit trail from
review through implementation and publication.

## Lifecycle

Every workspace follows the same durable lifecycle:

1. **Intake** resolves a GitHub pull request and verifies its provider,
   repository, pull-request, author, base, and head identities.
2. **Charter** records the PR type, goal, acceptance criteria, included areas,
   exclusions, and non-goals. The confirmed revision is the authority boundary
   for review and implementation.
3. **Review** evaluates the exact immutable provider diff against the confirmed
   charter. Findings include evidence, severity, a scope grade, a change-size
   grade, type compatibility, confidence, and the charter clauses they affect.
4. **Triage** lets the user keep a finding in scope, defer it, dismiss it, or
   correct it. Corrections can apply to review, implementation, or both.
5. **Implementation** repairs only confirmed in-scope findings in a pinned local
   workspace. It runs an isolated scope audit before accepting the candidate;
   hard candidate scope drift stops here, before validation or finalization.
6. **Validation** records local validation evidence for the exact candidate.
7. **Completion audit** checks every acceptance criterion and in-scope finding,
   then searches for missing work and out-of-scope changes.
8. **Publication** separately authorizes a GitHub review, an implementation
   branch push, and follow-up issues for deferred work. One publication never
   grants another publication or merge authority.

The provider head revision and workspace version are optimistic-concurrency
fences. Refresh after a conflict; do not copy a stale version or SHA into a new
request.

## PR Type And Scope

The charter requires exactly one primary PR type:

| Type | Allowed intent | Typical incompatible expansion |
| --- | --- | --- |
| `fix` | Correct a bounded defect and the tests needed to prove it | Feature work or broad cleanup |
| `refactor` | Change structure while preserving externally visible behavior | New behavior unrelated to the structural goal |
| `feature` | Add the behavior and supporting tests described by the charter | Opportunistic unrelated fixes or redesign |
| `documentation` | Change documentation and the minimum supporting metadata | Product-code behavior changes |
| `test` | Add or correct test coverage and fixtures | Unrequested production behavior changes |

Scope distance and implementation size are independent:

| Scope | Meaning | Default disposition |
| --- | --- | --- |
| `S0_exact` | Directly required by a confirmed charter clause | May proceed when type-compatible and otherwise gated |
| `S1_necessary_adjacent` | Necessary to make exact work correct or verifiable | Requires explicit justification and policy evaluation |
| `S2_related_followup` | Related and useful, but not necessary for this PR | Defer from implementation |
| `S3_unrelated` | Outside the chartered area | Block from implementation and defer or dismiss |

Change size is graded `XS`, `S`, `M`, or `L` from files, semantic lines, and
modules. Size does not make an out-of-scope change acceptable. The default
profile sends a large exact-scope implementation to a human scope gate and
blocks related, unrelated, or type-incompatible implementation work.

The UI also distinguishes `candidate_present` work from a `follow_up`. A
follow-up can be deferred because it is not in the candidate. Deferring a
candidate-present change does not remove that code, so it cannot satisfy the
scope audit or make the branch publishable. The candidate must instead be
repaired, reverted, or evaluated under a deliberately revised and reconfirmed
charter.

An isolated scope audit that classifies any candidate-present hunk as
`S2_related_followup`, `S3_unrelated`, or incompatible with the charter's PR
type stops implementation before validation and finalization. It opens a
built-in human hard-scope gate that a configurable profile cannot weaken. The
UI reports the scope and size grades and offers only these outcomes:

- **Remove code + defer follow-up** (`defer`) authorizes another repair to
  remove the candidate-present code and clones the finding as deferred
  follow-up work for grouping and issue tracking;
- **Revise and reconfirm charter** (`revise`) returns the workspace to charter
  revision, and implementation cannot continue until the new revision is
  explicitly confirmed;
- **Stop implementation** (`block`) leaves implementation blocked.

`pass` is unavailable for this gate. It cannot be supplied through the API to
bypass hard scope. Branch-publication eligibility independently rechecks the
current findings, repair assessment, and the gate's immutable hard-scope flag,
so editing a finding or retaining a stale successful gate cannot make that
candidate publishable. The exact hard-resolution finding IDs are pinned in the
gate; their scope classification stays frozen while the removal occurrence is
active, while title/message corrections remain available.

Scope evidence is bound to the candidate diff rather than accepted as a model
summary. The audit must contain exactly one record for every real diff hunk,
with the exact candidate path, full `@@` hunk header, and semantic added-plus-
removed line count. Missing, extra, duplicated, fabricated, or mismatched hunk
evidence rejects the audit.

Completion and completion-nudge findings marked `candidate_present` use the
same evidence boundary: each must cite an exact audited path, module, full `@@`
header, and semantic-line count. It may conservatively strengthen the scope or
type warning but cannot understate the persisted scope audit. A `follow_up`
finding must not claim a candidate hunk.

## Findings, Corrections, And Shared Context

Review, repair, scope audit, and completion audit start from one canonical facts
bundle. It contains the verified provider snapshot, active confirmed charter,
current findings, applicable corrections and advisory messages, active
repository lessons, current deferred groups, prior stage evidence and coverage,
and bounded nudge-learning history. The review and implementation projections
select the facts applicable to that audience; their specialized prompts supply
different instructions and authority, not separate memories of user feedback.

Current facts are fenced before they reach a model. Findings and stage evidence
must belong to the active charter and exact provider head. A correction recorded
after confirmation binds both that charter and head; guidance recorded before
the first charter has an empty charter ID and is deliberately workspace-scoped
at that exact head. It is therefore available while drafting the charter and to
later review and implementation prompts. Superseded corrections are omitted.
Messages follow the same head fence and, when they name a charter, must name the
active charter. Stale facts remain in the audit history but are not silently
reused after a head or charter change. Prompt inputs are bounded independently
from the durable history, and a finding-targeted correction is rejected unless
that finding exists in the aggregate.

A correction records:

- what claim or behavior was wrong;
- the corrected rule and supporting evidence;
- whether it applies to review, implementation, or both;
- the charter and head revision against which it was made.

Conversation messages are advisory guidance for `review`, `implementation`, or
`both`; adding one grants no model, gate, Git, or provider authority. A
correction is a typed durable replacement for an incorrect fact or decision.
Promoting a correction to a repository lesson is a separate gated action. An
active lesson can inform other workspaces only for the same provider repository,
matching PR type, and applicable audience. Promotion does not rewrite older
workspaces, and neither a message nor a lesson overrides the current confirmed
charter.

## Adaptive Nudging

Review and completion audit can run additional search rounds after the first
answer, including when the first answer reports no findings. The service chooses
the wording from bounded variants such as searching for missed issues or
checking whether all scoped work is complete; callers do not hard-code a single
nudge phrase.

Each round stores its prompt digest, selected variant, new findings or missing
work, and later reward. Variant selection uses durable success history, so the
system can explore alternatives and increasingly favor variants that find
validated additional work. Review and completion policies have independent
minimum and maximum round counts.

A manual **Find more** or **Check again** action challenges the persisted stage
evidence: already reviewed and still-unreviewed path/hunk coverage, prior
findings, current corrections and messages, and earlier nudge results. It does
not rerun the initial review or completion prompt as though the first pass had
never happened. Novel and duplicate results are recorded separately, and later
user dispositions, corrections, or published deferred work update the variant's
durable reward.

The completion audit asks two different questions:

- Is everything required by the confirmed charter complete?
- Did the implementation change anything outside that charter or PR type?

A successful first pass does not skip the configured minimum additional search
rounds. A bounded maximum prevents open-ended model loops.

## Gates

Gate profiles are configured from **Pull requests → Gate profiles**, the
canonical `/pull-requests/profiles` page. Global nudge, scope-grade, and
deferred-issue settings live under `/pull-requests/settings`; each profile has
its own `/pull-requests/profiles/:profileID?flow=review|implementation` editor.
Profiles may be assigned to specific canonical repositories; all others use
the configured default.

A decision point is an ordered all-of workflow. Supported stage formats are:

- `zero`: immediate explicit pass;
- `deterministic`: evaluate a bounded expression over the frozen subject;
- `ai_working_context`: evaluate with an agent-owned, protected, revision-fenced
  session derived from the bounded current workspace context;
- `ai_isolated_context`: evaluate with an ephemeral, no-tool context;
- `human`: suspend until the user chooses `pass`, `revise`, `defer`, or `block`.

Later stages run only after every earlier stage passes. Gate specifications,
subjects, and results are digest-pinned and stored privately. A working-context
snapshot is frozen before the workflow run; an intervening session mutation
fails closed instead of changing the gate's evidence. Retrying a suspended gate
resumes the same gate; it does not repeat a completed review, repair, validation,
or provider effect.

The built-in profile defines 14 stable decision points: charter confirmation
and reconfirmation; review start and completion; ambiguous-finding
classification; non-owned implementation eligibility; implementation start,
large-or-adjacent scope, and completion; review and branch publication;
deferred-issue publication; correction promotion; and ambiguous-publication
reconciliation. Candidate-present `S2`/`S3` or PR-type-incompatible work uses
the separate non-configurable built-in human resolution gate described above;
it is not the configurable large-exact-scope classification gate.

## Deferred Work

Findings that cannot be fixed within the confirmed PR scope are deferred, not
quietly implemented. Draft deferred findings can be regrouped, edited, split,
merged, or linked to an existing issue. Once a group has a live publication or
an existing issue URL, it is externally bound and immutable; edit or reorganize
it before publication. Every update, split, and merge uses the workspace CAS
version and rejects duplicate group or finding membership rather than silently
deduplicating it. Group membership is durable, so removing a finding from a
group does not reappear after reload.

`pr_lifecycle.deferred_issues.mode` controls follow-up issue handling:

- `off` refuses new issue publications and cancels an already queued one before
  any provider call;
- `ask` waits for the configured `pr.deferred.publish` gate;
- `automatic` groups eligible deferred findings and queues them under a durable
  built-in explicit-pass policy, without pretending that user consent was
  supplied by another gate.

Creating a GitHub issue remains a separate publication. If its authorization
gate returns `revise`, `defer`, or `block`, the publication is blocked and the
group records durable `publication_suppressed` state with the reason. Automatic
policy skips that group on later mutations. An explicit **Publish/Retry** action
clears suppression and starts a new authorization attempt. Every attempt uses a
stable marker so an ambiguous provider response can be reconciled without
blindly creating a duplicate issue.

Automatic grouping or queueing is a post-mutation policy step. If it fails, the
request returns an error instead of silently discarding the failure, includes
the current authoritative aggregate, and retains any primary finding, run, or
gate mutation that already committed. The client retries `POST
/deferred-groups/automatic-sync` from that returned version; stable derived
request identities make the retry idempotent and prevent duplicate groups or
publications. The unified UI exposes the same retry explicitly.

## Publications And Unknown Outcomes

The workspace exposes three distinct provider effects:

- publish the selected review findings;
- push the validated implementation branch;
- create a follow-up issue for one deferred group.

Queueing freezes the complete provider request privately and records its digest
before dispatch: selected review findings and summary, the exact validated
branch fence, or the deferred issue title/body/labels/finding IDs. Dispatch and
recovery decode that frozen payload; later workspace edits cannot change an
already authorized provider request. Stable request identities make an exact
queue replay idempotent, while a different head or selected finding set
conflicts. A queued review or branch payload whose head becomes stale terminates
without a provider call. Branch queueing also rechecks that no unresolved hard
candidate scope blocker or hard-scope repair attempt is current.

Each effect durably reports states such as `queued`, `waiting_gate`, `running`,
`succeeded`, `failed`, `stale`, or `unknown`. An interrupted or ambiguous
external request is `unknown`, not proof of failure. The UI first opens the
separate `pr.publication.reconcile` gate with exactly two human actions:
**Check provider again** (`pass`) authorizes a bounded read-only marker or
exact-remote-head check, while **Assume failed** (`block`) explicitly records
that choice and releases the publication/group lock for a new deliberate
request. `revise` and `defer` are not valid reconciliation responses because a
terminal non-decision would strand the publication; persisted results from an
older build are staled and replaced with a fresh two-action gate. Merely failing
to find an issue marker does not prove absence and does not auto-create another
issue. Unsafe or cross-repository result URLs are never accepted as success.

Review publication puts its stable marker and the complete frozen finding set
in the initial pending review, before adding inline comments. If any create,
comment, or submit response is ambiguous, reconciliation distinguishes a
`PENDING` review from a submitted `COMMENTED` review. It can submit that exact
pending review with the complete recovery body and then must re-observe the
marker in `COMMENTED` state; it never creates or comments a second review during
recovery. Duplicate markers, a foreign commit, review ID, URL, pull request, or
unexpected state fail closed.

Branch publication adds one further fence: after the queued publication is
durably claimed as running, unrelated workspace mutations are rejected until
the exact branch attempt succeeds, fails, becomes unknown, or is safely
requeued. This prevents guidance, correction, charter, or evidence changes from
racing an external push after completion authorization was checked.

Repair evidence also separates two prompt identities. `prompt_digest` binds the
exact edit-only system prompt, user instruction, and canonical implementation
context. `scope_prompt_digest` binds the later isolated scope-audit prompt. One
cannot be substituted for the other when auditing which model decision produced
code versus classified it.

Pushing a branch is not merging a pull request. Publishing a review is not
approving implementation. Creating a deferred issue does not authorize either
of the other effects.

## Configuration

The current top-level configuration is `pr_lifecycle`:

```json
{
  "pr_lifecycle": {
    "gate_profiles": {
      "default": {
        "name": "Default",
        "workflows": {}
      }
    },
    "default_gate_profile_id": "default",
    "repository_assignments": {},
    "nudge": {
      "review_minimum_additional": 2,
      "review_maximum_additional": 5,
      "completion_minimum_additional": 2,
      "completion_maximum_additional": 5
    },
    "scope": {
      "xs": { "files": 1, "semantic_lines": 20, "modules": 1 },
      "s": { "files": 3, "semantic_lines": 100, "modules": 1 },
      "m": { "files": 10, "semantic_lines": 500, "modules": 3 }
    },
    "deferred_issues": {
      "mode": "ask"
    }
  }
}
```

Use `GET` and revision-fenced `PUT /api/pr-lifecycle/gate-profiles` for the
scoped settings surface. Saving validates the same V2 stage semantics used by
runtime compilation and also rejects AI agents absent from the full config. A
successful update reports `restart_required` because the gateway freezes the
catalog and policies for one runtime generation. Subsequent GETs keep that
effect until a PID or health-confirmed gateway start loads the exact saved
catalog; a failed start or a start using an older catalog cannot clear it.

The removed `reviews` configuration is not accepted as a compatibility
placeholder and is not migrated into `pr_lifecycle`.

## HTTP Surfaces

The authenticated launcher proxies one bounded API tree:

- `/api/pr-workspaces`
- `/api/pr-workspaces/{prw_...}` and its charter, run, finding, correction,
  message, deferred-group, gate, and publication subresources
- `/api/pr-lifecycle/gate-profiles`

The managed gateway owns the matching protected runtime tree at
`/runtime/eventing/pr-workspaces`. Browser credentials are replaced with the
managed process bearer; redirects, environment proxies, non-local authority,
cross-site mutations, noncanonical paths, oversized bodies, and malformed JSON
fail closed.

The former `/reviews`, `/api/reviews`, `/api/pr-development`, and corresponding
runtime routes do not redirect and have no compatibility handler.

## Storage Cutover

Eventing schema v19 is a destructive replacement for PR state:

- a valid v18 database is validated, then every `pr_review_*` and
  `pr_development_*` table and its data are dropped;
- generic event inbox and workflow-dispatch tables are retained;
- the unified `pr_*` workspace, provider snapshot, charter, stage, finding,
  correction, lesson, nudge, deferred, repair, validation, gate, publication,
  operation-intent, activity, request, and history tables are created;
- v1 through v17 databases are rejected rather than incrementally migrated into
  the new PR model.

Back up the database before upgrading if old PR records are needed for audit.
They cannot be opened or imported by the new workspace UI.

See [PR workspace v19 cutover](../migration/pr-workspace-v19-cutover.md) for the
operator checklist.
