# Security, Credentials, And Isolation

## Feature ID

`FR-SEC`

## Behavior Summary

PicoClaw protects credentials, dashboard access, local files, network requests,
tool execution, and optional isolated subprocesses. These requirements define
security behavior that other feature specs rely on. Event-derived AI
classification can explicitly remove model tool authority while leaving any
approved side effect as a separately declared workflow action. Workflow
inspection exposes only bounded structural metadata and conservative
possible-effect classifications, never the underlying source or sensitive
values. Workflow-authoring capability discovery similarly exposes exact
addressable identities and typed parameter shapes from one leased live runtime,
never descriptions, raw schemas, configuration, paths, or internal errors.
Structured job/action authoring treats draft YAML and mutation operations as
bounded untrusted input, remains a stateless AST transformation, and requires
an exact-identity conservative effect review before the separate execution
path. Current configuration also separates non-secret model aliases from
provider accounts, validates every reachable account-and-alias combination,
and revision-fences whole-config mutations so neither a missing model nor a
stale writer can silently delegate behavior to a provider default.
Compiler-generated attention workflows keep gate subjects and a pre-captured
PR-chat snapshot in an owner-local private root. Capture replaces every
structured media locator with an immutable reference and persists the strict
self-contained `FrozenSet` before revision binding; generic run, event, tool,
UI, and result surfaces expose only fixed lifecycle state and explicitly
generated human-task questions, never that context, media, or session
capability.
Review-attention policy is likewise trusted operator configuration rather than
repository content. Its authenticated policy-only read and same-origin strict
replacement share the complete public-plus-security config revision, preserve
unrelated configuration through compare-and-swap, and expose neither security
sidecar bytes nor runtime/session authority. The browser policy editor keeps
that projection only in one memory-resident lossless draft, never delegates
arbitrary question numbers to JavaScript numeric coercion, and cannot launch a
gate, workflow, model, tool, review action, or repository mutation.
An automatic outgoing-review attention occurrence keeps its effective policy
pin and worker fencing state owner-local. The worker establishes that pin from
trusted configuration before private effects, and every retry strictly
validates the same bytes rather than granting a database row or reviewed
checkout fresh policy authority. Only the gate engine's deliberately generated
bounded human task may declassify configured questions.
The corresponding own-PR development occurrence is separately keyed to an
immutable local-review entry and exact conversation prefix. Its scheduling
lease is never a branch or mutation reservation; browser response authority is
an opaque one-turn fence derived only after the complete private chain has been
validated server-side.
The case-owned browser attention bridge extends that deliberate
declassification only with fixed lifecycle state, answered text, and at most
one opaque actionable response fence. The launcher replaces browser authority
before reaching the protected gateway; every private identifier and diagnostic
remains server-side, and exact reserved attention runs are absent from generic
workflow observation and mutation surfaces.
Incoming feedback on the configured user's own PR has a separate read-only
capture boundary. Exact-body webhook authentication and a bounded provider
re-read can bind one review database ID, author, state, commit, time, URL, and
current PR/fork/head snapshot, but they do not make the review body trusted
instructions or grant model, checkout, repository, or GitHub-write authority.
The current GitHub MCP review result omits the webhook node ID and its comment
result omits a parent review database ID, so capture preserves the former only
as trigger evidence and claims no inline comment-to-review association.
The authenticated body supplies canonical numeric repository, pull-request,
and pull-author database IDs. The bounded provider read directly returns and
exactly matches only the pull-author ID; its current projection omits repository
and pull IDs, so those signed IDs gain stable meaning only after exact current
origin, repository, pull URL/number, and base facts cross-bind them to the same
provider object. That identity creates or reuses one owner-local private `pdt_`
thread and appends the new case at a contiguous ordinal. Connector and mutable
names, URLs, numbers, refs, review facts, or times never substitute for
provider-object identity. Schema migration isolates every legacy case in a
different identity-less one-case thread rather than guessing a relationship;
its existing case-scoped verification and repair remain compatible, but it
cannot join siblings or future thread-wide ledger automation without an
explicit provider-verified baseline.
A later own-PR development viewer declassifies only an explicit safe projection
of those immutable captures through protected
`/runtime/eventing/pr-development` and authenticated
`/api/pr-development` GET routes. It omits capture provenance and internal
identities, labels provider/ref/SHA values as capture-time facts, renders
feedback as plain text, and grants no provider refresh, gate, workflow,
checkout, filesystem, Git, CI, or mutation authority. The list projection
alone may additionally expose one coarse `attention_required` boolean as the
safe union of the authoritative current local-review occurrence and one exact
current before-push publication decision; attention delivery adds no case-detail
field or source discriminator. The open workbench polls the list every five
seconds and labels that union only as `Needs input` for in-app discovery and
canonical chat focus, never as an out-of-band notification, provenance oracle,
or action capability. The case-owned bridge selects at most one source from the
same atomic private read, uses separate response-fence domains, and can resume
only the exact waiting private human task; its unchanged DTO cannot reveal
publication pins, claims, run identity, or provider evidence. Separately, the
selected-case detail may contain one
optional browser-safe `local_development` object derived from the same atomic
workbench read. Its strict whitelist reports only the latest public attempt,
candidate commit/no-change, terminal CI status plus opaque fingerprints,
reservation-free local-review status/outcome/summary/count, and derived local
readiness. It exposes no private evidence or lifecycle capability, and local
readiness never authorizes repair or a provider/publication action. A separate
exact chat POST may append a bounded local transcript
and consult an isolated advisory
model over explicit historical data; the model receives no tools, history,
cache, default/workspace/runtime prompt context, checkout, provider credential,
or action capability. That transcript is separate two-table local event state:
one per-case high-water row binds count/version, total bytes, and a rolling
canonical digest to append-only messages, and every read/append validates the
complete relation without changing capture ordering or conferring identity.
The private thread link does not yet broaden that case transcript, repair
session, route, browser state, or model context to a sibling case.
A later controller-only repair primitive still does not inherit authority from
that capture, DTO, transcript, or answer. It independently refreshes the exact
review and current open PR/head, then passes only an exact pin and one
already-resolved concrete provider target into a runner that cannot receive a
raw checkout path or release capability. The runner constructs a fresh registry
with four guarded repository-content tools, validates complete tool-call and
patch path sets before mutation, serializes writes, denies Git control paths and
symlink aliases, suppresses argument values from logs, and postflight-verifies
the still-owned pin on every exit. It has no ambient agent, session, account,
workflow, process, network, Git, CI, or publication surface.
A later trusted controller may retain that exact checkout as one private
development line, but neither the line nor its mutation lease becomes a model
or generic workspace capability. Parking binds one exact clean direct-child
commit and releases the edit reservation before review. The reservation-free
review surface revalidates the parked version, base/tip/tree, internal ref, and
manager-owned checkout, then returns only bounded canonical changed paths and
an exact-object unified diff. It exposes no checkout path, internal branch,
reservation, Git lifecycle operation, provider access, or generic tool, HTTP,
workflow, or browser surface.
Every retained-line Git effect is now protected by a controller-private
schema-v13 write-ahead operation. The row—not a model, workflow, or stale
worker—owns recovery after expiry. Adopt and Resume recover through composite
old-to-fresh reservation transitions that never release the stale-bearer lock
between line convergence and revocation; Commit rotates before exact replay;
Park replays and retires only old. Park then removes edit authority while
retaining the private branch for the separate reservation-free review, whose
generation-owned worker can claim only the exact completed orchestration and
immutable fence. Operation,
request/result, claim, bearer, checkout, ref, commit, fence, and repair-session
evidence remain structurally absent from public, browser, model, generic
workspace, workflow, tool, log, provider, and stats surfaces.
When recovery or another idle handoff has no active repair owner, the separate
inventory-v4 suspension boundary snapshots the exact ordinary candidate and
retires its edit reservation while preserving the checkout and private ref.
Prepared-Commit suspension records the deterministic child's exact prepared
tree and whether that child already became detached `HEAD` without treating it
as the retained/ref tip. An unapplied prepared request still requires that
exact candidate; only an applied child may retain later post-prepare edits in
the independently captured current candidate tree. Exact later
resume normalizes only that known child and the real index back to the retained
parent, preserves every candidate file, and installs a globally fresh bearer at
the unchanged attempt epoch. Suspended state is neither parked nor reviewable
and is absent from all generic and user-facing surfaces.
Schema-v17 recovery composition makes that boundary operational without
granting recovery a model-capable mutation interval. One generation-owned,
provider-independent worker may claim only eligible bound durable recovery,
checkpoint its exact kind-specific Git result before suspension, and erase all
raw recovery authority when the controller becomes `suspended`. Commit remains
the exact prepared Rotate-plus-`CommitPinned` effect before commit-recovery
suspension; Park directly retires old through its atomic review handoff. A later
repair persists and replays one fresh exact resume before any model request.
Legacy unbound v12 rows are deliberately untouched until an idempotent
retirement protocol is separately reviewed.
Local-CI validation is a separate mandatory security domain over exact
controller-owned candidate evidence. Git Workspaces materializes the immutable
pre-attempt parent and current candidate as bounded `.git`-free disposable
roots and a complete SHA-256 manifest; discovery reads both but executes
nothing. An explicit local plan is authoritative; otherwise a bounded
repository-native quick profile precedes supported GitHub workflow fallback,
and a multi-executable GitHub job is rejected because accepted steps run in
independent fresh sandboxes. Required steps run only in the Linux Bubblewrap
backend after a user-systemd cgroup-v2 supervisor handshake, with a clean
allowlisted environment, explicit controller-provided read-only offline
dependencies, no network or ambient credentials, bounded filesystems and
process/output/time resources, and complete cleanup. Missing support or an
offline dependency fails closed; there is no host or generic-isolation
fallback. Exact-manifest discovery records and execution evidence persist, but
production success reuse is disabled until mutable host toolchains and
dependency mounts have complete immutable manifests. None of that evidence
grants controller, Git, workflow, model, provider, UI, or publication authority.
The exact parked-line push is a separate controller-only outbound capability.
It revalidates one reservation-free retained line and permits only an
expected-OID compare-and-swap of its stored source branch at its literal stored
repository. The request carries no credential, helper, arbitrary refspec, push
option, or transport command; the fixed controller SSH command and ambient
controller/operator transport remain trusted under the existing threat model.
The primitive decides no readiness, provider refresh, acknowledgement, merge,
or durable publication policy and persists no publication fact.
Schema-v18 eventing now adds the separate private durability boundary that the
primitive intentionally lacks, but still grants no outbound capability. One
passed-review transaction records immutable local evidence; later store calls
may create-once pin operator policy, a private gate subject, caller-supplied
provider evidence, decision-run identity, and the exact push request/result.
Scheduling claims never become Git reservations or model/provider credentials.
Only `push_started` briefly excludes controller mutation; expiry there removes
all automatic retry authority and records terminal uncertainty. Every journal
field is non-JSON owner-local state, and this slice composes no provider, gate,
model, Git, gateway, worker, acknowledgement, or merge effect.
An additional narrow schema-v18 read capability now authenticates only an exact
live publication claim originating from `pending` and atomically returns its
complete integrity-checked local gate evidence. The publication is
claim-redacted, the owner session has its retired reservation and attempt
scheduling fields cleared after validation, every nested value remains private, and the canonical
conversation type and rolling digest are reused rather than copied into a new
raw schema. A first subject pin must compare that version/digest fence in its
write transaction; exact already-pinned replay remains valid after later chat.
Neither boundary acquires a provider, workflow, model, Git, filesystem, push,
gateway, HTTP/UI, acknowledgement, or merge capability.
Safe pre-effect publication requeue is another narrow schema-v18 mutation: it
restores one authenticated live claim to its exact scheduling origin at an
availability that is non-past at the live transition while preserving all pins,
decisions, counters, local evidence, and recorded parked-line evidence without
reading current branch or reservation state. Its shared retry-delay helper is
pure and bounded; worker classification and invocation remain outside both
capabilities.
Active publication gates now compose those private seams through the ordinary
mixed-gate compiler and private runner. The durable subject is a canonical
owner-local replay envelope, but models receive only its bounded untrusted
evidence projection; raw conversation is available solely as an exact anchored
prefix frozen into the protected working-context session. Exact pin ordering,
create-once run admission, owner-agent equality, and phase-specific status
mapping prevent restart from changing intent or duplicating execution. Human
wait keeps neither a mutation reservation nor a scheduling/runtime lease. An
internal dispatcher routes only caller-owned claims and requires a separate
handler for every reclaimable phase. The push-ready phase now has a fenced
least-authority handler: exact live ready-claim authentication and a repeated
provider pin precede one journaled parked-line request, only a newly committed
start grants one Git call, and renewal moves from the queue lease to the push
journal without ever lending a mutation reservation. Production queue wiring
remains disabled until the separate generation-owned runtime is supplied.
Schema v5 removes aliases mechanically derived from account names or concrete
model IDs, clears their references rather than guessing replacements, and
preserves legacy web-search mappings as explicit custom aliases instead of
silently assigning them to a predefined semantic role. Explicit custom aliases
and deliberately configured predefined roles are preserved. Optional frozen
session media treats every locator, decoded byte, copied metadata field, and
serialized frozen record as bounded untrusted input; capture and
materialization fail closed without exposing local paths or payload detail.

## Reconstruction Notes

- Similarity target: recreate secret-preserving config behavior, credential
  store CRUD, dashboard auth controls, HTTP guard checks, optional process
  isolation with fail-closed setup, and the mandatory fail-closed local-CI
  sandbox and exact-success evidence cache.
- Core types/functions: secure string config helpers, credential store,
  dashboard auth middleware, CSRF/logout handlers, HTTP guard, isolation runtime,
  token, OAuth response parsing, PKCE helpers, strict bounded request decoders,
  raw-only AST classification for structured workflow authoring,
  `media.SnapshotReader`, `media.FreezeInputs`, `media.FrozenSet` validation,
  and controller-only `gitworkspace.Manager.AdoptPinnedLine`,
  `ResumePinnedLine`, `ParkPinnedLine`, `SnapshotPinnedLineReview`,
  `RecoverPinnedLineAdoptReservation`, and
  `RecoverPinnedLineResumeReservation`, plus `SuspendPinnedLine`,
  `SuspendPinnedLineCommitRecovery`, `ResumeSuspendedPinnedLine`, and
  `PushPinnedLine` with `PinnedLinePushRequest` and `PinnedLinePushResult`;
  schema-v17 composition additionally uses the narrow tagged recovery-work,
  suspension-checkpoint/finalize, and suspended-resume prepare/finalize
  capabilities plus the generation-owned recovery worker; local validation additionally uses
  `pkg/prdevelopment/localci` and
  `gitworkspace.Manager.WithPinnedCandidateValidationRoots` with
  `PinnedCandidateValidationRequest`, `PinnedCandidateValidationRoots`, and
  `PinnedTreeManifest`. Schema-v18 publication durability additionally uses the
  all-private `eventing.PRDevelopmentPublicationReader`,
  `PRDevelopmentPublicationGateContextSnapshotReader`,
  `PRDevelopmentPublicationQueue`, `PRDevelopmentPublicationPushJournal`,
  `PRDevelopmentPublicationOutcomeReconciler`, and
  `PRDevelopmentPublicationDecisionRunStore` interfaces plus exact passed-review
  admission; the narrow `prdevelopment.PublicationGateProcessor` may advance
  only an already claimed zero-gate occurrence through existing least-authority
  policy, context, provider-observation, and queue capabilities, and includes no
  runtime publisher capability. The same queue's private
  `PRDevelopmentPublicationRequeue` releases only exact pre-effect scheduling
  ownership, while pure `prdevelopment.PublicationRetryDelay` supplies no
  scheduler, store, or effect authority.
- Security boundaries also include compiler-only private workflow admission,
  integrity-bound local context and frozen-media persistence, pseudonymous
  provider affinity, mandatory observation projection, and the case-owned
  attention response fence plus generic-workflow suppression boundary. The
  own-PR intake boundary additionally includes exact signed-routing validation,
  a generation-fenced read-only GitHub provider snapshot, strict bounded JSON
  or confined regular-file artifact consumption, immutable local capture, and
  a separate whitelist-only read projection whose runtime and launcher routes
  replace authority without serializing the durable case record. The retained
  development-line boundary additionally separates the private mutation
  reservation from an exact-SHA bounded review projection and keeps both the
  manager path and internal reachability ref outside every generic surface.
  The operation-recovery boundary additionally keeps schema-v13 request/result
  evidence, live claims, and staged replacement authority inside the existing
  controller store, while the Git composites hold old and fresh reservation
  locks continuously across convergence and revocation. The suspension boundary
  keeps its exact candidate, prepared-commit outcome, retired-bearer hash, and
  replay anchors private; releases both workspace and line ownership; and
  permits only an exact fresh resume without making suspension a parked review
  or readiness fact. The parked-line push boundary accepts only equality fences
  for stored identity and state, derives its sole destination from the stored
  source ref, and performs no provider-object operation or policy decision. Its
  result is a transient sanitized remote-effect receipt, never a credential,
  retry token, or durable publication record. The separate publication-journal
  boundary stores only canonical bounded local evidence and later trusted pins;
  hides all rows, claims, hashes, decision linkage, request/result bytes, and
  diagnostics from browser/model/generic surfaces; and makes `push_started` the
  sole short-lived exclusion against controller mutation. It cannot itself
  obtain provider evidence, execute a gate, or call the Git primitive. The local-CI boundary
  keeps exact parent/candidate roots, their complete manifest, repository-owned
  definitions, exact-manifest discovery index, Bubblewrap processes,
  user-systemd/cgroup-v2 supervision, output, environment identity, and evidence
  material outside retained checkouts and every model, workflow, browser,
  provider, eventing, and generic workspace surface.
- Runtime ordering: load security config, normalize protected values, validate
  access or target, execute guarded storage/network/process operation, redact
  sensitive output, and emit clear errors; for frozen media, preflight the
  complete locator graph, capture only bounded no-follow regular files, then
  validate the complete self-contained set again before materialization. For a
  retained line, validate exact private identity and clean commit state under
  the mutation reservation, advance and park its private ref before releasing
  that reservation, then independently revalidate the parked exact-SHA fence
  under inventory serialization before reading any bounded review bytes. For
  controller effects, commit the exact private operation before Git and
  finalize only its exact result; after expiry, claim that operation, perform
  only its kind-specific reconciliation while renewing the claim, and atomically
  install fresh authority or retire old before any later review access. For
  automated non-Park recovery, checkpoint the exact reconciliation result and
  enter `suspension_pending` before Git suspension, then exact-snapshot the
  current candidate under the fresh reservation and atomically retire that
  bearer into `suspended`. Park instead retires old directly. On a later queued
  attempt, persist one fresh resume bearer and intent before Git, normalize only
  an exact prepared child/parent transition without touching ordinary files,
  and install the sole mutation owner before any model. For exact parked-line
  push, acquire the manager mutex and kernel inventory lock, revalidate the
  complete parked reservation-free fence and local Git control plane, observe
  only the derived stored branch at the literal stored repository, permit one
  explicit expected-to-tip lease update, then perform cancellation-detached
  remote readback and local postflight before releasing serialization. For local
  publication durability, atomically create the occurrence with the exact green
  passed-review completion; lease only pre-effect scheduling; pin immutable
  policy, subject, provider, and decision evidence; release scheduling ownership
  for human wait; then atomically serialize a fully revalidated `push_started`
  write-ahead record against any controller mutation. Finalize only exact proven
  results, and turn expired or unproved started work into non-reclaimable
  `outcome_unknown`; any later reconciliation is a distinct minimal head-only
  observation supplied by another trusted component, never a second effect or
  a recheck/overwrite of the original provider review pin. For local
  CI, materialize and hash both exact snapshots under the reservation
  operation lock, discover both without execution using authoritative explicit,
  native-quick-profile, then supported GitHub fallback precedence, and reject
  definition drift or stateful multi-executable GitHub jobs. Reuse discovery
  only under the exact two manifests and implementation versions, resolve and
  bind the trusted environment, require controller-provided offline dependency
  mounts, complete the Bubblewrap plus user-systemd/cgroup-v2 handshake, run
  every required step in a fresh dedicated sandbox, revalidate the candidate,
  clean up, and only then return canonical persistent evidence. Do not reuse a
  passing production result while any toolchain or dependency mount lacks a
  complete immutable manifest.
- Non-obvious constraints: masked secure values preserve existing secrets,
  private network denial is the default, unsupported isolation does not fall back
  to unisolated execution, generated auth tokens must remain revocable, and a
  workflow-authoring projection or acknowledgement never grants runtime
  authority. A frozen-media digest is an internal consistency binding, not
  authentication against an attacker who can replace the complete containing
  record. A review-attention editor draft is memory-only capability-bearing
  configuration: it preserves accepted JSON number tokens exactly, never
  automatically rebases or retries a stale save, and gains no execution
  authority from previewing or validating an effective policy. A durable
  outgoing-review trigger pin is separate owner-local capability state: it is
  never browser-projected or logged, and corruption fails before private
  session, model, function, human-task, or run admission.
  A provider-verified own-PR case is still data, not authority: review text is
  never interpreted during capture, the workflow marker is only explicit local
  opt-in, and every future checkout or provider action must establish its own
  current authority rather than inheriting trust from either record.
  Likewise, parked-line push is an effect primitive rather than publication
  admission: the caller must already possess every exact local and remote fence,
  and success cannot be inferred from local readiness, a gate result, review
  text, or provider capture. It accepts no caller- or repository-supplied
  transport command. Existing fixed trusted SSH and ambient operator transport
  remain part of the controller deployment boundary.
  A publication record is not authority merely because its local evidence is
  valid: every provider observation and gate decision must be supplied through
  its own trusted boundary, every transition is fenced by exact private claim
  identity, and none can reveal a credential or invoke `PushPinnedLine`.
  Pre-effect wait/readiness never holds a reservation or blocks repair; only
  `push_started` excludes mutation, and its expiry is outcome uncertainty rather
  than retry. Desired-tip readback can prove current publication but cannot
  reconstruct whether a prior effect occurred across remote ABA. Publication
  also conveys no acknowledgement or merge authority; those semantics remain
  intentionally undefined.
  The development workbench does not change that rule: “current” review and PR
  values are names of capture-time fields, replay is a distinct public case,
  feedback is plain text, and event/dispatch/run/workflow/connector provenance,
  target user, provider node/review IDs, capture hashes, payloads, credentials,
  lease state, and raw errors remain unrepresentable.
  A retained development line is likewise controller-private: its line ID,
  workspace/path, internal branch, reservation hashes, mutation agent/epoch,
  and history never enter generic stats, tools, workflows, HTTP, UI, model
  context, or logs. The review result is untrusted repository data rather than
  lifecycle authority and is tied only to the exact returned base/tip/tree.
  Its advisory conversation is separate append-only local data: the model sees
  only a bounded explicit projection under an exact replacement system prompt;
  complete-transcript count, byte, and digest validation fences corruption;
  conversation versions never confer capture or action authority; and
  plain-text answers are not evidence that a repository or provider was
  inspected. Draft, transcript, mutation, and ambiguous-response recovery may
  live in the keyed UI component and query cache but never in browser
  persistence. Recovery scheduling is likewise not mutation authority:
  `suspension_pending` may retain one raw fresh bearer only for its exact live
  claim, `suspended` retains none, and both remain non-ready and invisible.
  Legacy unbound v12 recovery is excluded from automatic processing because a
  lost successful release response lacks an approved exact replay proof.
  An attention response token is not a task identifier: it is a scoped digest
  over the exact server-loaded case-to-waiting-task chain, is issued only while
  that one task is actionable, remains memory-only in the browser, and grants no
  generic workflow or review-case mutation authority.
  An unfinished schema-v13 operation is likewise the sole recovery authority
  for its effect. It cannot coexist with a pending schema-v12 recovery intent,
  cannot authorize a different Git operation, and cannot declassify its raw
  bearers through ordinary controller reads. Mandatory linked v12 evidence for a
  recovered Adopt, Resume, or Commit is already finalized audit material; Park
  creates no replacement bearer or mutation capability for review.
  Local-CI isolation is mandatory and non-substitutable: an unavailable backend
  is a failed validation, never permission to run on the host. Repository
  content cannot supply inherited environment, credentials, network, writable
  dependency/evidence state, or a retained checkout. Persistent discovery
  matches the exact parent/candidate manifests and implementation versions.
  Future passing-result reuse must additionally match the complete candidate/
  manifest/plan/environment/toolchain/sandbox/platform identity and an immutable
  toolchain/dependency manifest; the mutable-host production backend disables
  it. Definition drift is always incomplete, while dependency-only drift changes
  environment identity.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-SEC-001` | MUST | Secure string config fields avoid plaintext exposure in launcher read paths and preserve secret values on partial updates; router model entries must not persist provider API keys, and router graph account refs are limited to non-secret credential account identifiers such as `credential:openai:work`. | Credentials must not leak through management surfaces. |
| `FR-SEC-002` | MUST | Credential store operations save, load, list, delete, and transactionally update credentials with provider/auth-method identity; provider aliases such as `copilot` are canonicalized before credential lookup or persistence; provider construction and model discovery reject stored provider-identity mismatches, and named token refresh and persistence remain bound to the normalized credential ID; provider-specific token validators reject unsupported token forms before storage. Every mutation acquires a process-local lock and, on supported Unix and Windows hosts, an OS file lock, reloads the latest store while locked, and writes through a same-directory, collision-resistant temporary file before overwrite-capable atomic replacement, including write-through replacement on Windows, so concurrent processes cannot lose unrelated credentials, fail on temp-name reuse or an existing target, or overwrite a replacement during refresh. | Auth-backed providers and MCP servers require durable credentials without crossing account identity or losing updates during concurrent launcher and gateway activity. |
| `FR-SEC-003` | MUST | Sensitive-data filtering redacts configured secrets from model-visible tool output when enabled. Durable external-event store construction also receives detached resolved secure-config values longer than three bytes for exact-value redaction, without logging or serializing that trusted list. | Tool results and channel-origin event text can contain credentials. |
| `FR-SEC-004` | MUST | Dashboard auth rejects unauthenticated access, uses CSRF-safe logout, and rate-limits login attempts. | Web management is sensitive. |
| `FR-SEC-005` | MUST | HTTP guard blocks private/internal targets unless explicitly allowed or proxy first-hop rules apply. Configured MCP URLs reject embedded credentials; credential-bearing remote servers require HTTPS except for intentional loopback development, and remote MCP redirects remain same-origin. MCP OAuth discovery, token exchange, authenticated probing, and refresh clients additionally reject cross-origin or downgrade redirects, disable environment proxies, resolve and pin an approved address into the actual dial, block private and special-use destinations from public-looking hosts, and restrict intentionally local discovery to the configured local address. | Web tools and browser-managed authentication must not become SSRF or credential-exfiltration primitives. |
| `FR-SEC-006` | MUST | Isolation runtime starts supported commands with configured exposed paths and fails closed on unsupported/invalid setup. | Optional isolation must not silently weaken execution. |
| `FR-SEC-007` | SHOULD | Key generation and token helpers produce unique, parseable, and revocable values for auth flows. | Auth flows need reliable primitives. |
| `FR-SEC-008` | MUST | Model-list and tool-adaptation config validation rejects unsupported provider-control values such as invalid `reasoning_effort`, invalid account-router account references, invalid model-router target references, and invalid tool-adaptation policy values before those values are persisted or used; profile-specific tool-adaptation overrides normalize provider/model identity and replace earlier duplicate identities as whole entries. Config schema v5 makes exact, unique `model_aliases[]` the only user-facing model selectors: each alias has a non-empty concrete default mapping, optional `account_overrides` and `disabled_accounts` may name only enabled concrete model-list or supported credential accounts and never an account or model router, and the same account cannot appear in both. Every account reachable through a router must be provider-compatible with every alias it can receive, while explicitly disabled alias/account pairs are excluded from that router's runnable candidates. Account routers and model routers are stored in top-level router lists rather than as secret-bearing `model_list[]` entries; agent and subagent model persistence preserves inherited fallbacks separately from an explicit empty fallback list; strict diagnostics tolerate deprecated `account_routers[].model` input, but runtime and output ignore it so routers remain model-agnostic. Default configuration seeds neither a runnable account nor a model alias, and an empty effective selection fails locally with `no model configured` before any provider request. Migration from schemas v1-v4 separates `account_ref` from alias-valued `model_name`, promotes only unambiguous explicitly configured legacy models, and never invents a provider or model default. | Invalid config should fail early instead of producing unsafe or broken provider requests, and persistence must not silently broaden an explicit model policy or delegate selection to an upstream default. |
| `FR-SEC-009` | MUST | OAuth token response parsing extracts non-secret account email claims from ID-token or access-token JWT payloads when present, preserves the email across refreshes, and leaves the email empty without failing when claims are absent or malformed. | Launcher account naming and credential metadata need stable non-secret account identity without weakening token validation or persistence. |
| `FR-SEC-010` | MUST | Event webhook secrets use the existing secure-string persistence path: JSON exposes only `[NOT_HERE]`, `.security.yml` stores plaintext/encrypted/file references, masked updates preserve a current value, and security-only connector entries cannot create or resurrect JSON configuration. Reference resolution follows final master enablement without touching inactive credential files, and an explicit management replacement can repair an active broken reference without resolving the old value first. Connector map keys, the JSON-owned format discriminator, and client-controlled durable identity fields cannot contain a configured signing secret; those conflicts fail opaquely before persistence. Enabled secrets are validated for their selected Standard Webhooks or GitHub format and used for exact-value durable content redaction as well as HMAC verification. | A listener credential must neither leak through config, error, identity, or event-storage surfaces nor survive as stale active configuration after its connector is removed. |
| `FR-SEC-011` | MUST | Delta Chat email automation requires both a verified sender contact and a correctly encrypted/signed message unless the connector explicitly enables unverified email. Durable metadata excludes local blob paths, remote references, and bytes; declared and streamed attachment sizes are capped before materialization; receipt time is preferred over sender-controlled mail `Date`. | Email addresses, message dates, filenames, and attachment declarations are attacker-controlled and must not silently become workflow authority or expose private account storage. |
| `FR-SEC-012` | MUST | Native GitHub webhook admission verifies `X-Hub-Signature-256` against the exact bounded raw body with the connector's secret before JSON parsing, but never represents `X-GitHub-Event` or `X-GitHub-Delivery` as signature-authenticated. The normalized envelope records the body/header distinction, public deployment requires trusted TLS termination, and no unsigned timestamp or retained delivery ID is presented as cryptographic replay prevention. | GitHub's HMAC protects payload integrity but not transport headers or freshness; workflows and operators need the actual trust boundary rather than implied authority. |
| `FR-SEC-013` | MUST | An event-derived classifier may declare `tools: none`, which removes tool definitions and model-authored tool execution from its initial request, structured-output repair, managed fallbacks, and child work. The GitHub issue-triage workflow treats signed issue/repository content as untrusted, permits model influence only through validated category and priority enums plus a comment boolean, and performs any approved effect only through a separately declared `mcp/github/add_issue_comment` step whose repository/issue identity comes from the signed body and whose text is fixed. Installation and GitHub MCP enablement are explicit, the MCP write credential remains separate from the webhook signing secret, and no classifier failure gains fallback action authority. | Payload integrity does not make user-authored issue text safe instructions, and a classifier does not need authority-bearing tools to produce a bounded decision. |

| `FR-SEC-014` | MUST | Event operator runtime routes require the gateway's process-local PID bearer using constant-time comparison. The authenticated launcher injects that credential server-side without forwarding browser cookies or authorization, maps internal authorization/stale-process failures to unavailable rather than a new login challenge, and applies same-origin checks to replay. The local CLI obtains the credential only from the owner-readable live PID file. Public event/dispatch projections have no deduplication or lease-token fields, ordinary detail omits payload, the explicit exact-payload response is non-cacheable, and all clients bound response size and error text; CLI payload output validates an object but emits its original bytes without normalization. | Durable operator data contains worker fencing credentials and potentially sensitive redacted-at-rest content; management access must not expose internal authority, become a CSRF primitive, or silently weaken during reload. |
| `FR-SEC-015` | MUST | Shared durable file primitives reject empty paths, create each missing parent directory before its child reaches a durable boundary, write a synced same-directory temporary file before atomic replacement, and durably remove a file or empty directory while preserving ordinary missing/non-empty errors. POSIX implementations sync the containing directory after creation, replacement, and removal. Windows implementations use write-through moves for directory creation and replacement, and make logical deletion durable by moving the original to a collision-resistant same-parent tombstone before best-effort tombstone cleanup. | Workflow journals and other local state must not report a committed first-directory creation, replacement, or deletion that can disappear or revert after power loss, and Windows must not depend on replacing an open file or syncing a directory handle. |
| `FR-SEC-016` | MUST | Authenticated workflow definition and built-in-template inspection reads one bounded exact source; a published read opens the configured definition nonblocking through workspace- and definitions-root-confined handles and verifies the opened handle is regular, so neither a symlink race nor a swap to a FIFO can escape or indefinitely hold mutation boundaries. It releases the cross-process file lock before parsing and the handler-local config mutation lock before encoding or response writing. The response contains only path-free whitelisted trigger fields, declaration names and required/default-presence metadata, declared action targets, fixed validation/limit codes, and conservative possible-effect classifications. Whole-family trigger preflight, aggregate entry limits, bounded topology fields, rejection of control and Unicode format characters, and a fixed encoded-response ceiling prevent YAML aliases or invalid definitions from amplifying or visually spoofing a bounded review response. The response cannot represent raw YAML, prompts, arbitrary `with` or `if` values, session or delivery values, input default values, secret values or mappings, output expressions, filesystem paths, captured event payloads, or raw parser/filesystem/provider errors. Every truncation or field omission is explicit, dependency/effect aggregation is independent from topology truncation, and a known effect class survives target omission; unknown or reusable actions cannot be presented as read-only. | A convenient browser review surface must not become a definition-file reader, sensitive-value oracle, or false assurance that an automation is side-effect-free. |
| `FR-SEC-017` | MUST | Workflow-authoring capability discovery runs only against the existing PID-bearer-protected gateway loop while a runtime-use lease pins its generation; the authenticated launcher reads process authority without cleaning a stale PID file or loading, migrating, backing up, or saving configuration, substitutes the credential on one exact bounded GET, and never forwards browser credentials or upstream error detail. Projection requires agent IDs in the runtime's exact canonical `[a-z0-9][a-z0-9_-]{0,63}` form, sorts and validates every other exact UTF-8 identity, rejects control and Unicode format characters, uses the default agent's effective core registration keys, excludes the recursive workflow tool, and derives MCP targets only from exact separator-safe server/tool identity rather than a lossy provider-facing name. Tool and MCP parameter maps are untrusted: calls are panic-contained and transactionally projected into an ordered typed whitelist containing only fixed type, property/required membership, items, bounded scalar enum, and additional-properties shape. Non-whitelisted schema metadata is outside that DTO rather than a partially projected constraint. Bounded source selection plus shared collection, depth, property, enum, numeric-text, work, and encoded-response limits prevent cycles, aliases, panic loops, huge registry copies, oversized numeric parsing, or many discarded shapes from amplifying a response; every removed identity, omitted whole shape, and collection truncation has a fixed code, and an unsafe structural declaration omits its shape whole. Responses cannot contain agent/tool/MCP descriptions, prompts, raw schema maps, defaults, examples, constants, patterns, formats, references or compositions, provider/model/MCP configuration, URLs, commands, headers, environment, credentials, source paths, durable state, or raw runtime/proxy/panic errors. Discovery never initializes or connects MCP, emits MCP lifecycle events, or refreshes OAuth credentials: disabled MCP is a complete empty category; enabled MCP includes only a live collision-free manager's ready tools; a live identity-colliding manager produces an empty partial MCP category with a fixed unsafe-omission code; and no live manager produces a fixed unavailable partial state. It cannot construct another agent loop, edit configuration or YAML, create sessions/runs, execute a capability, or perform a durable mutation. | Production-equivalent discovery must not become a prompt, configuration, filesystem, credential, or denial-of-service oracle, and a read-only UI must not silently change persistent infrastructure. |
| `FR-SEC-018` | MUST | Authenticated structured job/action inspect and render routes strictly decode one bounded JSON object containing caller-supplied exact YAML, and render additionally requires its nonblank opaque revision plus exactly one allow-listed typed operation; unknown or trailing JSON, duplicate object members, null required members, invalid UTF-8, unpaired JSON surrogate escapes, unsafe/browser-inexact numbers, excessive nesting, collections, strings, YAML, operation work, validation detail, or encoded output fail closed through fixed public codes. The server hashes and parses only those supplied bytes, never reads a configured definition, active development session, configuration, PID authority, capability registry, credential, provider, filesystem path, event, or run. Ordered projection and rendering operate on YAML nodes rather than flattened maps: version/tag directives that the AST cannot retain, anchors/aliases, merge keys, unsafe tags, duplicate or non-string structural keys, malformed containers, and normalization-prone values never enter typed mutation state. Step IDs and every `needs` job-identity reference use the fixed 256-byte single-line identity bound. Global source directives, anchors/aliases, container ambiguity, complexity exhaustion, `jobs_truncated`, or `steps_truncated` blocks all operations; aggregate `validation_truncated` only marks diagnostics incomplete and does not block an otherwise structurally safe operation. Outside a global block, `unsafe_fields_omitted` can mark local raw-only state, a locally raw-only job rejects patch/delete, a locally raw-only step rejects patch/delete/move, and an editable step cannot move across a raw-only step in its inclusive source-to-destination span; other safe sibling edits or structurally safe insertions remain available. One successful render fences the exact revision before typed operation decoding, mutates only a transient AST copy, preserves unrelated unknown nodes/comments/order/style, returns original bytes for a semantic no-op, and emits only data derived from the caller's source—plus candidate YAML after render—through a bounded typed projection, fixed limits, and sanitized validation; raw parser/runtime/filesystem/provider errors are never returned. A set-only job-ID rename rejects collisions and replaces only the mapping key scalar, never performs a broad textual rewrite, and never implicitly retargets `needs` or authority-bearing expressions. Inspect, render, tab selection, capability selection, and effect review cannot save a draft or config, initialize or invoke a capability, create a session/run, or mutate runtime or durable state. Ready catalog targets are suggestions only and exact manual targets remain untrusted. The draft-test review conservatively treats every tool, MCP, agent, native function, local reusable workflow, unrecognized/manual target, raw-only action, and incomplete or empty projection as potentially effectful; its acknowledgement is component-local and keyed to the exact YAML/scenario/review identity, is cleared on any identity change, and is rechecked at final confirmation before the separate existing test endpoint may execute. | Draft editing handles attacker- and model-authored YAML; it must not become an alias-expansion denial of service, a secret/configuration oracle, a stale-consent execution path, or a new way to exercise runtime authority. |

| `FR-SEC-019` | MUST | Authenticated trigger simulation and confirmed draft execution strictly decode one bounded tagged JSON object and reject unknown, duplicate, trailing, null-required, invalid UTF-8, unpaired-surrogate, browser-inexact-number, over-depth, over-collection, over-string, over-YAML, or oversized input with fixed public errors. Simulation parses only the supplied exact draft plus an ID-selected protected event when applicable, shares production trigger/context helpers, and returns only fixed match/suppression codes, bounded scenario metadata, the provided secret count without names, conservative action effects, completeness, validation, and an opaque review token; it never reflects secret names or values, protected event or runtime payloads, raw YAML, delivery internals, filesystem/configuration/provider errors, credentials, or runtime authority. Both routes obtain one current-schema config and exact public-plus-security revision through a read-only snapshot; a legacy schema fails closed without migration, backup, or save, while protected-event authority comes from a bounded PID-file peek or captured process record and never performs stale-PID cleanup. The token is an HMAC under a process-local random key over length-delimited exact session and draft fences, prompt, target, YAML bytes, trigger/index, normalized typed scenario including secret values, server-derived review, exact public-plus-security config revision, and, for a protected event, a digest of the exact server-loaded redacted envelope; restart invalidates it. Payload, MAC, and nonce encodings must be canonical, MAC comparison is constant time, and the consumed identity hashes the length-delimited decoded payload and MAC so textual base64 aliases cannot bypass one-use admission. Confirmed execution rejects caller-origin, caller-effect, and sync/async controls, repeats and re-simulates the exact reviewed request, and reloads protected event state. A preflight-invalid token, request, initial session/draft fence, trigger, protected event digest, config revision, match, or review fails before runtime construction. Execution constructs only an unpruned lazy runtime; final token expiry, config, session, candidate-validation, and running-test fences repeat under the workflow/config mutation locks immediately before durable run creation or development mutation, so a concurrent final-fence failure closes the unused runtime without creating a run, writing development state, or pruning retained history. A token admits at most one durable run even when its development claim or HTTP response projection fails, and every post-create response is a bounded `202` with the run ID plus a fixed omission or degraded-reconciliation marker when the full session cannot be returned. Browser consent remains component-local, token- and exact-identity-bound, clears synchronously on any identity change, and is disabled after one confirmation. | Draft scenarios contain attacker-controlled messages, runtime metadata, event IDs, secrets, and authority-bearing actions; preview and confirmation must not become a secret/payload oracle, side-effecting matcher, stale-consent path, or forged run-origin channel. |
| `FR-SEC-020` | MUST | Agent capability reads and mutations resolve the runtime-equivalent workspace, open bounded regular `AGENT.md` and legacy `AGENTS.md` state without following symlinks or blocking on special files, and fence exact active bytes plus the public-and-security config generation. Existing-file commits on a supported host bind the intended candidate identity and bytes, use an atomic entry exchange, validate the exact displaced file, and establish the platform durability boundary before best-effort displaced-file cleanup. Conflict recovery repeats the exchange when a newer edit races restoration so that edit returns to the canonical path; any entry that cannot safely be recovered is retained at a logged operator-visible path. Create and legacy-upgrade commits atomically require an absent current file, recheck the alternate legacy source after creation, and conditionally quarantine only the generated file on conflict. A platform without a handle-safe primitive—including Windows while `ReplaceFileW` cannot provide no-follow target binding—projects a fixed read-only issue instead of accepting a save that cannot be safe. Structured YAML-node mutation rejects normalization-unsafe, malformed, and unterminated documents, whose runtime tools, MCP, and structured tasks fail closed, and preserves unrelated nodes, comments, order, style, exact permissions, and body; legacy upgrade requires an explicit acknowledgement and leaves the legacy file untouched. Capability catalogs expose only fixed bounded fields and never paths, URLs, commands, arguments, environment, headers, auth state, credentials, or raw parser/filesystem/provider errors. Per-agent activity similarly filters a fixed typed event allowlist before its bounded queue; the gateway writes a concrete numeric address selected from the listener that actually opened, including a single-stack localhost fallback, and the launcher proxy peeks rather than repairs PID authority, rejects hostname and wildcard PID authority, sends the bearer only to loopback or a literal local-interface address through a no-proxy/no-redirect bounded client, forwards no browser credentials or headers, strictly revalidates the upstream DTO, and returns fixed local errors. | Workspace editing and live telemetry combine attacker-controlled files, runtime payloads, and process authority; management convenience must not create a symlink/FIFO escape, lost update, secret/configuration oracle, credential-forwarding path, or bearer exfiltration primitive. |
| `FR-SEC-021` | MUST | A whole-config read-modify-write operation captures the parsed update-safe configuration and one opaque revision of the exact public JSON plus security sidecar under the shared process/advisory lock, validates a complete candidate, and saves only when that revision is still current. The revision hashes security bytes without returning them. A stale save returns `config revision mismatch`, writes neither file, and preserves the winning writer's aliases, credentials, and unrelated configuration; callers surface a reload/retry conflict instead of blindly retrying an operation whose intent may no longer be valid. Current-schema read-only snapshots never migrate or save, while update snapshots may complete the explicit legacy migration lifecycle before returning a coherent current generation. A scoped review-attention replacement uses the same combined revision but raw-patches only `reviews.attention` in the public JSON: it never serializes environment/default-derived state, rewrites the security sidecar, or migrates a legacy read. The authenticated review-attention UI holds one memory-only lossless draft from one captured revision, rejects malformed or numerically lossy projections before hydration, sends at most one full replacement for an explicit save, and never automatically retries, rebases, persists the draft in the browser, or turns validation/preview into execution authority. | CLI, launcher, runtime, and browser config writers must not lose concurrent public or secret state, persist ambient runtime state through an unrelated scoped save, corrupt arbitrary JSON numbers, retain trusted policy in a less protected store, or turn a read/edit path into implicit mutation or execution. |
| `FR-SEC-022` | MUST | Frozen-session media capture and materialization treat the complete locator batch and serialized `FrozenSet` as untrusted. They admit at most 32 locator occurrences, 16 distinct nonempty assets, 2 MiB decoded bytes per asset, 3 MiB decoded bytes counted per occurrence, and 5 MiB for both materialized encoding and frozen-set JSON. Occurrence counting happens before snapshot cloning; complete locator and supplied-metadata shape validation happens before any live read, and decoded/read/materialized bounds never trust declared lengths. At most four `FreezeInputs` calls hold capture admission concurrently; an excess caller waits only until a slot is available or its context is cancelled. A live `media://` snapshot retains store lifecycle synchronization while it safely opens and validates a regular handle and executes one bounded read: Unix uses no-follow/nonblocking open plus a status-change token, Windows rejects every handle carrying the reparse-point attribute and compares handle change time, and other platforms fail closed. Registration and final deletion use cleaned absolute exact lexical lifecycle keys without an approximate case fold. One live key coalesces only the same captured entry identity. A `SameFile` identity found under a distinct key is not coalesced because it may be a hard link; instead, all such live lifecycles become non-deleting. Re-registration permanently cancels older pending deletion through either its exact key or captured `SameFile` identity, and deletion rechecks `Lstat`/`SameFile` so an already replaced entry is preserved. These operations share store synchronization, so an old cleanup cannot delete a newly registered or read-pinned path. Only canonical frozen identities, canonical `media://` UUID capabilities, and `data:` locators with canonical parameter-free MIME plus canonical padded base64 are accepted. Raw filenames are bounded at 4 KiB before basename sanitization to valid UTF-8 without controls and at most 255 bytes; supplied MIME is at most 127 bytes and captured MIME is bounded at 1 KiB before canonicalization to at most 127 bytes. Frozen records have one supported explicit version, deterministic unique identities/order, bounded canonical metadata, exact decoded sizes, and content digests; strict decode also rejects invalid UTF-8 and unpaired JSON surrogates, and decode/use reject unknown, duplicate, missing, unused, reordered/noncanonical, trailing, digest/size-inconsistent, reference-inconsistent, or authoritative occurrence-metadata-inconsistent state before returning rewritten history. Snapshot/freeze/materialize failures use fixed bounded classification and omit locator text, local paths, filenames/content types, decoded or encoded payload bytes, and raw filesystem/JSON/base64 errors. Cancellation or any failure returns no partial snapshot, reference list, set, or materialized output and leaves caller-owned inputs unchanged. | Media locators can name private temporary files and carry attacker-sized inline content; restart-safe capture must not become a symlink/special-file read, resource-amplification path, tampered-context downgrade, or diagnostic data leak. |
| `FR-SEC-023` | MUST | A compiler-private workflow context is local capability state admitted only for the exact trusted in-memory gate workflow and normalized value snapshot. Admission rejects custom JSON/text marshalers without invoking them, encodes the caller-owned workflow once, verifies the unexported compiler hash against those exact bytes, and executes only the detached workflow decoded from that same capture; serialization or post-compile workflow/value mutation cannot retain authority. An initial context cannot claim retry provenance. It is persisted before effects with an integrity revision plus a domain-separated binding over run ID, workflow reference, retry source, private-root revision, and persisted-workflow revision, and cannot mix with public inputs, event, secrets, origin, session, delivery, parent/reusable context, or arbitrary workflow targets. A separate durable owner-local marker preserves private classification if JSON visibility/root fields are removed; store reads also require the decoded ID to match both the directory key and exact requested ID. Its exact read-only session is captured before durable run creation. During that one capture, every structured media locator is replaced by an immutable frozen reference and the complete canonical versioned `FrozenSet` is strictly validated and persisted with the snapshot before the private-root revision is computed. Resume, retry, restart, managed children, repairs, and provider fallback reuse only that frozen snapshot and set: they validate the frozen revision before materializing integrity-checked embedded bytes and never reread a live session or media store. The explicit private encoding also preserves runtime-only message/system-block prompt provenance and tool-call name/arguments/thought signature exactly across wait/restart. Provider cache and account affinity use only a domain-separated agent-plus-history-revision pseudonym, never the raw key, scope, inbound delivery, locator, or materialized payload. Every private agent step carries an explicit execution marker: account-router health keeps only failure classification plus fixed error text, and side-question vision fallback suppresses raw-error runtime events. `Run.MarshalJSON` recognizes private visibility and redacts invocation context, ancestry, delivery, outputs, raw errors, job/step diagnostics, frozen references, and the embedded media set by default. Only the trusted owner-local file-store encoder may bypass that default to write the exact raw checkpoint, execution, task, and private continuation state; its strict private-root decoder rejects unknown fields and invalid frozen-media state. The unexported root remains absent from every ordinary `Run` JSON, and every HTTP, SSE, browser/development, workflow-tool, runtime-event, stored event, cancellation result, and direct-result boundary removes private state and event messages/payloads before traversing caller-controlled graphs instead of falling back to the raw record. Event append/read first classifies a readable owning run under the store lock; an orphaned or unclassifiable record fails with the fixed private-context error. Human-task resume clones secrets at entry and rejects additions again in the authoritative locked claim and returned-run boundary before consuming or continuing a private response. Wrapped public sentinel errors are canonicalized before return, including fixed `ErrRunAdmissionConflict` and `ErrRunAdmissionUnavailable` boundaries that the HTTP owner maps respectively to the existing dependency-revision-mismatch `409` and dependency-check-unavailable `503` responses without exposing internal text. Only the compiler-generated bounded human task may deliberately declassify its title/questions. A missing, corrupt, hash/revision/binding/media-mismatched, mixed-visibility, or unprojectable private record fails closed with a fixed error. | Local gate evidence and exact provider prompt construction must survive process restarts and human delays without turning generic JSON, workflow observability, provider routing, media persistence, or error handling into a PR-chat and code/findings exfiltration path. |
| `FR-SEC-024` | MUST | The authenticated browser may access review attention only through exact case-owned projection and response routes after the launcher replaces all browser authority with the managed gateway's process bearer on one same-origin, no-proxy, no-redirect, strictly bounded JSON request. The launcher peeks PID metadata without cleanup, attachment, migration, process inspection, or health probing and requires a valid nonzero port plus a numeric loopback or literal current local-interface address; hostname, wildcard/unspecified, multicast or remote numeric, and incomplete authority fail with fixed unavailability before the bearer is put on a request. A valid non-submitted case short-circuits to `none` without reading an occurrence or run. Submitted projection validates the latest submission and trigger first: historical trigger absence is `none`; pending/claimed accept only coherent absent pin state or a strictly decoded canonical pin with matching recomputed revision and read no run; no-op requires a canonical all-zero pin, no run, and terminal completion; only delivered requires a canonical active pin, terminal completion, deterministic run ID, exact decision link, and stable bounded run/task snapshot. Before declassification, every task's exact title, questions, and response schema must re-hash to its stored input hash. The DTO exposes only the positive case version, fixed aggregate status, `can_respond`, configured title/questions, public turn status including non-actionable `canceled`, and a durably accepted response for answered/continuing/recovery state. At most one current actionable waiting or recovery turn receives an opaque lowercase SHA-256 fence, absent whenever `can_respond` is false or the turn is continuing, answered, or canceled. The fence is a domain-separated length-prefixed digest over the exact server-loaded case/version, submission, decision, policy revision, run, task, original waiting revision, and input hash; the server accepts no private linkage and derives a separate response ID from the fence plus normalized bounded answer. Exact persisted replay or lost-response recovery is idempotent, while stale, old, cross-case, cross-task, or altered answers conflict. GET, navigation, polling, and failure mutate nothing; POST may change only the exact private task/run continuation and never review, event, policy, repository, provider, or GitHub state. Private IDs/revisions, session, policy body, task/run/workflow identity, input hashes, trigger lease/retry state, process bearer, and raw stored/upstream errors are unrepresentable in the DTO, route, browser storage, and fixed errors. The canonical URL contains only `case` and `focus=chat`. Generic workflow surfaces suppress exact reserved-reference runs; visible ordinary runs scrub direct hidden parent/caller, retry, child, and origin-root references; graphs omit hidden nodes and incident edges; and cancel/retry/task mutations return not found for the entire normalized transitive parent/child/retry component. Production web and CLI retention preserve every exact reserved-reference run regardless of terminal age while ordinary related runs keep normal retention. With workflows disabled, the bridge is deliberately read-only even if given an executor: waiting/recovery has no fence, no new answer is consumed, exact persisted replay remains projection-only, and exact-reserved task resume returns not found before disabled-runtime disclosure. This outgoing-review declassification grants no inbound own-PR feedback or generic workflow authority. | A necessary human handoff must resume one provably current private gate without turning the browser, URL, launcher proxy, generic workflow APIs, replay, retention, or error handling into a capability-confusion or private-context exfiltration path. |
| `FR-SEC-025` | MUST | The own-PR development workbench read is a declassification over exact protected `GET /runtime/eventing/pr-development` and `GET /runtime/eventing/pr-development/{pdc_...}` routes mirrored only as authenticated launcher GETs. List input is limited to one validated repository, canonical positive pull number, canonical limit, and opaque filter-bound cursor; detail accepts no query. Both layers reject aliases, malformed encoding, unknown or repeated input, bodies, and non-GET methods before store access, and the launcher replaces all browser authority with one process bearer sent only to numeric loopback or a literal current local-interface address through a bounded no-proxy/no-redirect client. The runtime constructs dedicated DTOs instead of serializing durable cases. List summaries may contain only public case ID, repository, pull number/URL/author/state/draft/merged, head repository/ref/SHA, review author, submitted/current review state, review submitted time/URL, and capture time; detail may additionally contain base repository/ref/SHA, review commit SHA, bounded valid-UTF-8 feedback, and the bounded public conversation fields owned by `FR-SEC-026`. Event, dispatch, run, workflow, connector, target-user, provider node/review database IDs, capture hash, raw payload, credential, lease/retry state, internal errors, and every mutation capability are unrepresentable. Provider state, refs, and SHAs are explicitly captured-snapshot facts even when a field is named current; a replay is a distinct case rather than hidden replacement. The canonical browser view contains only fixed `view=development`, optional repository, optional canonical positive pull number, and selected public case ID, keeps cursors and DTOs memory-only, renders feedback and messages as plain text, isolates deliberate external links, and performs no provider refresh, model call, gate, workflow, checkout, filesystem write, Git/CI command, commit, push, merge, acknowledgement, provider call, or mutation merely by reading or navigating. | Viewing untrusted review feedback must not turn the browser, proxy, durable case, misleading “current” label, replay, route, or renderer into a provenance oracle, stored-script path, ambient authority bridge, or implicit local/provider action. |
| `FR-SEC-026` | MUST | Own-PR development conversation is available only through exact authenticated `POST /api/pr-development/{pdc_...}/chat` and protected `POST /runtime/eventing/pr-development/{pdc_...}/chat`. Before PID access the launcher rejects absent, ambiguous, or cross-site browser provenance; after it strips browser authority, the protected runtime rejects any `Origin`, `Referer`, or `Sec-Fetch-Site` header before store/model access. Both layers reject noncanonical or escaped aliases, extra segments, wrong methods, every raw query or bare `?`, unsupported or ambiguous content type/charset/encoding, missing/streaming/over-one-MiB bodies, invalid UTF-8 or Unicode scalars, excessive JSON depth, exact or case-colliding duplicates, unknown keys, trailing values, and anything except the case-sensitive keys `expected_version` and `content`. Canonical `pdc_` identity, integer version range zero through 256, configured agent, and Go-`TrimSpace`-normalized nonempty NUL-free at-most-32-KiB human text are preflighted before store/model use; stale version and complete-turn capacity necessarily wait for the current case and integrity-checked transcript read. The launcher replaces every browser credential and ambient header with the process bearer on one numeric-local no-proxy/no-redirect request, bounds it to 120 seconds and its JSON response to 32 MiB, and the shared protected server keeps a 135-second write timeout. One process-wide same-case lock prevents interleaving across service generations, while each `Service` independently admits one configurable bounded set of AI calls. Under both fences the service binds the captured case and conversation, requires the exact current version, and reserves two remaining rows plus the normalized human bytes and maximum 64-KiB assistant bytes before committing the human. It then sends a detached at-most-512-KiB context containing only safe captured-snapshot fields and at most 50 recent public messages to one canonical configured agent. The request is private, ephemeral, single-run, `tools=none`, `history=none`, `cache=none`, and managed-off, and its exact isolated system prompt replaces the configured default, workspace/bootstrap, identity, memory, skills, contributors, tool rules, summary, time, and dynamic runtime context. Repository, refs, feedback, transcript, and latest message are explicitly untrusted historical data. The validated assistant text is appended separately. Schema v7 uses one conversation high-water row and append-only message rows; every read/append validates contiguous count/version, total bytes, and a domain-separated length-prefixed rolling SHA-256 digest over all canonical message fields. The transcript is bounded to 256 rows, 64 KiB each, and 4 MiB total, and chat never updates capture ordering. Any model, response-validation, assistant-append, or later validation failure after human append preserves that committed row. An error may declassify detail only after an independent two-second safe reload; failed reload and list/detail errors omit detail, and raw provider/model/store text is never returned. Public DTOs omit agent/session/account/model identity, prompts, tool state, tokens, cache keys, capture provenance, provider credentials, filesystem paths, run/gate identity, and all action capability. The case-keyed UI retains detail, transcript, draft, mutation, and ambiguity state only in memory, applies Go-compatible trim semantics, renders plain text in an accessible live log, rejects loose case/version/message binding, independently preserves the newest conversation and repair dimensions, and takes unversioned repair capability from authoritative GETs while mutation detail may only downgrade it. A matching expected-version-plus-one human row is recovered as a committed response failure without blind retry; the same row followed by an assistant at expected version plus two is recovered as completed. Refetch failure retains and announces visible detail/draft, and mobile list/detail focus is restored. | Prompt injection in review text, stale tabs, cross-site requests, malformed gateway state, ambiguous outcomes, model/storage failures, or a plausible answer must not become an ambient model proxy, script path, transcript corruption, current-state claim, repository inspection claim, or authority to gate, acknowledge, edit, validate, commit, push, merge, refresh, or mutate GitHub. |
| `FR-SEC-027` | MUST | Controller-owned PR repair is admitted only after `GitHubVerifier.VerifyCase` reconstructs one valid immutable case and independently confirms an open unmerged pull, the unchanged exact non-dismissed review evidence and base target, and a credential-free canonical current-head clone endpoint. `VerifyCase` alone binds canonical pull/review/clone shapes to one strict HTTPS `WebOrigin`, defaulting to `https://github.com` and explicitly configurable for GitHub Enterprise; capture-time `Verify` retains its existing provider contract. While a trusted caller pins one concrete provider/model generation, `LocalRepairRunner` accepts only a bounded exact `PinnedAcquireRequest`, user instruction, and explicitly untrusted context; it never accepts a raw path, generic agent/instance/loop, session, workflow runner, account router, fallback chain, tool registry, or release capability. Exact pin identity is process-serialized and acquired before provider access. One construction-local registry allowlists exactly line-bounded `read_file`, bounded/filtering `list_dir`, size-bounded `edit_file`, and whole-path-preflighted bounded `apply_patch`, all restricted to the acquired checkout with no allow-path escape. Paths must be canonical repository-relative names; lexical case/trailing-dot-or-space/alternate-stream aliases of `.git`, traversal, absolute paths, outside symlinks, resolved Git-control ancestors, mutable symlinks/non-regular files, and oversized targets fail before delegation. Apply-patch parses first and validates every add/delete/update/move path before operation one. The fixed prompt distinguishes the user task from untrusted context and accurately denies shell, process, network, web, MCP, hook, workflow, message, runtime-event, Git, CI, commit, push, merge, and provider-write capability. Provider calls receive only the four exact definitions plus `max_tokens` and `temperature`, with no cache/affinity metadata; messages are detached, response/tool/argument counts and bytes are bounded, names and IDs are nonempty/unique/allowlisted before any tool executes, nil/panic/malformed responses fail, thought signatures survive follow-up, mutations execute sequentially in response order, and argument values never enter tool-loop logs. After any exit following acquisition—including cancellation through a bounded detached cleanup context—the runner reacquires and compares the same workspace/repository/ref/path/lock identity. It never releases, cleans, resets, tests, commits, or publishes the pin. Only ordinary repository-content files plus manager-owned repository/workspace creation and exact pin lock, heartbeat, and history state may change; a later failure can intentionally leave allowed partial edits locked for explicit inspection. | Untrusted review/code content and a tool-calling model must not turn an internal repair step into Git-control tampering, path escape, ambient-agent reuse, unintended provider selection/affinity, concurrent write races, hidden execution/publication, or silent loss of partially completed local work. |
| `FR-SEC-028` | MUST | Browser-started PR repair crosses only exact authenticated launcher and bearer-protected runtime routes with strict same-origin replacement, bounded exact JSON, two optimistic revisions, and a random idempotency identity. The public aggregate is a dedicated declassification: it may expose availability, opaque case/session/attempt identities, canonical selected agent ID, pinned public head repository/ref/SHA, at-most-4-KiB instruction, lifecycle/timestamps, at-most-4-KiB sanitized summary, and a fixed error code, but cannot represent clone URL, reservation, workspace identity or checkout path, lease/worker identity, provider/model/account selection, prompt, tool call or arguments, review digest, capture provenance, credentials, or raw diagnostics. The durable controller holds the exact runtime generation for a worker iteration, uses the stored agent only to construct one concrete no-fallback runner, feeds captured review/conversation/repair material only as bounded explicitly untrusted context, and performs no implicit repair from advisory chat. Preparation is reclaimable only before runner invocation; after the durable `running` fence, every lost lease, crash, runner error, or uncertain finish preserves the pin and becomes `recovery_required` without automatic replay. Existing session/history remains visible when new admission is unavailable. The browser keeps draft, retry identity, and baseline next-attempt ordinal memory-only; it consumes ambiguous intent only when that exact ordinal matches the submitted conversation version and canonical instruction, renders every instruction/summary as plain text, independently preserves the newest conversation and repair dimensions, treats authoritative GET detail as the only capability-upgrade source while mutation detail may downgrade it, polls only active status, and labels completion as local edits without review, test, commit, push, merge, reset, release, or provider acknowledgement. | A durable asynchronous controller and its UI must not turn retry, polling, config reload, untrusted transcript/code, error projection, or an ambiguous crash into leaked local/provider authority, duplicated edits, silently discarded work, or a false claim that local mutation passed a later gate. |
| `FR-SEC-029` | MUST | A trusted controller snapshots or commits ordinary content in one exact pinned checkout through `Manager.SnapshotPinnedCandidate` or `Manager.CommitPinned`; the edit-only local repair runner and pinned release participate in the same reservation-derived cross-process operation lock. | The controller APIs revalidate exact repository/ref/base pin/reservation/agent/workspace identity, manager-owned nonsymlink path, raw origin, base ancestry, detached `HEAD`, real Git directory/index, and the existing strict control-plane exclusions before effects. Snapshot uses a manager-private temporary index seeded from `HEAD`, stages all tracked and nonignored untracked worktree content without changing the real index, rejects empty candidates and changed gitlinks, and emits only parent/tree/domain-separated raw-diff digest/bounded count evidence. Commit requires exact stored evidence plus a canonical `pdcmt_` intent, single-line at-most-512-byte message, and UTC whole-second time; it recreates one deterministic commit whose message binds a domain-separated digest of the intent and whose PicoClaw author/committer are fixed, verifies the raw object, compare-and-swaps detached `HEAD`, updates only the real index, and proves the worktree clean. Exact retries after failure between completed Git subprocesses reconcile the intended object and index; a proven commit followed by content drift returns commit evidence with a distinct recovery error and never rewrites ordinary files. | Every Git subprocess is direct argv execution with bounded separately drained output, message on stdin, empty hooks, disabled signing/editor/pager/prompt, stripped ambient Git/askpass/locale/editor state, no system/global config, and the existing replacement/lazy-fetch/config/control-file exclusions. Snapshot may write unreachable Git objects; commit may write the intended object, detached `HEAD`, its bounded exclusive local reflog, and the index only. No model/tool/workflow/browser can call these APIs, and no hook, shell, validation command, network operation, remote/branch update, push, merge, reset of ordinary files, release, or provider action occurs. | Invalid Unicode/control/size/hex/time input, stale identity/evidence, dirty or conflicted real index, attached/unexpected/concurrently changed `HEAD`, merge/rebase/sequencer state, changed gitlink, unsafe ref-storage/symlink-ref configuration, nonexclusive appendable reflogs, output overflow, poisoned ambient config, cancellation, compare-and-swap ambiguity, postcommit drift, or failed postflight fails closed. A lock left by termination inside a Git subprocess requires explicit operator recovery and is never deleted automatically; the exact applied commit remains automatically recoverable only when the preceding Git subprocess completed. Ambiguity is never converted into a second commit or a clean-success claim. | Local attempt commits must not turn repository configuration, hooks, process environment, concurrent repair/release, crash timing, or model-visible APIs into code execution, data loss, duplicate commits, or remote publication. |
| `FR-SEC-030` | MUST | One authenticated own-PR review capture supplies canonical positive-decimal repository, pull-request, and pull-author database IDs and is cross-checked against a generation-fenced read-only current-pull response before a verified schema-v9 membership is created, or schema-v9 code opens a schema-v8 database. | The capture boundary derives one canonical lowercase HTTPS provider origin with no credentials, path, query, fragment, noncanonical default port, or textual alias. It exactly matches the provider-returned pull-author database ID. Because the current MCP projection omits repository and pull-request database IDs, it cross-binds those two HMAC-authenticated IDs to the same provider object through exact canonical origin, base-repository full name, pull URL, and pull number. A verified private `pdt_` thread is keyed only by the length-delimited origin, base-repository ID, and pull-request ID; pull-author ID is stored as a mandatory immutable equality invariant. The immediate capture transaction creates or resolves exactly one such thread and appends the distinct immutable case at the next zero-based ordinal. The thread's exact count, unique `(thread, ordinal)`, unique reverse case binding, and complete no-gap membership are validated on every complete controller read. Connector is retained only as case provenance. Repository/login spelling, URL, pull number, ref, review identity, timestamps, and connector cannot create, merge, or repair stable identity by themselves. | Schema-v8 migration performs no provider or payload read and instead creates one separate identity-less legacy thread with one ordinal-zero membership per old case. A legacy thread never auto-joins a verified capture; existing case-scoped `VerifyCase` and repair continue using the pre-v9 case evidence, but the thread cannot aggregate siblings or enter future thread-wide ledger/orchestration without an explicit provider-verified baseline or adoption contract. Verified or legacy thread ID, ordinal, provider origin, identity hash, exact count, cases digest, and legacy marker remain owner-local and structurally absent from browser DTOs/routes/browser storage, model prompts/context, generic workflow/event observation, logs, and public errors. Raw numeric IDs remain ordinary HMAC-authenticated webhook payload/attribute fields already observable through existing event/workflow surfaces; those untrusted fields project no verified invariant, stable grouping, membership, or action authority. Current case list/detail, conversation, repair session, locks, versions, drafts, and UI selection remain case-owned; membership grants no sibling-case read, action, checkout, model, gate, commit, push, merge, acknowledgement, or provider-write authority. | Missing/noncanonical IDs or origin, direct author-ID mismatch, failed repository/pull cross-binding, changed author invariant, key collision with unequal identity, duplicate/reordered/gapped membership, count drift, one case linked twice, mixed legacy/verified state, malformed legacy isolation, cancellation, migration failure, or transaction ambiguity fails closed without a partial case/conversation/thread/link and without fallback to mutable names or provenance. Exact capture retry must prove the same case, thread, ordinal, and full identity; it never appends a second membership. | A future PR-wide ledger needs stable provider-object grouping, but neither attacker-controlled display identity nor migration convenience may conflate repositories or PRs, expose trusted grouping state, or silently widen today's per-review authority. |
| `FR-SEC-031` | MUST | Only a trusted controller may call `Manager.AdoptPinnedLine`, `ResumePinnedLine`, `ParkPinnedLine`, or `SnapshotPinnedLineReview`. Mutation calls accept exact canonical line/workspace/source/version/epoch/tip/tree/agent fences under a reservation-derived cross-process operation lock; the private line record retains a domain-separated live reservation hash, complete bounded never-reusable retired hashes, and exact write-ahead/replay evidence while the raw bearer exists only in the live workspace lock, and returned lease/park evidence contains no checkout path, internal branch, raw reservation, or mutation-agent identity. Park is rejected inside a still-live inherited mutation operation, proves one clean detached exact tree at either the unchanged no-change tip or one direct-child commit, durably records the exact pending tuple, compare-and-swaps without dereferencing and reference-fsyncs one exclusive canonical loose private branch ref without creating a reflog, and clears the mutation reservation only after final line evidence is durable; exact replay can reconcile only that tuple. A review is allowed only while the line is parked and owns no mutation reservation. Under manager/inventory serialization it revalidates the private inventory owner, unlocked retained checkout, original repository/source pin, canonical origin, safe Git control plane, clean detached `HEAD`, stable internal ref, exact version, prior parked tip, current tip, and tree before and after reading objects. It returns the exact version and park epoch/intent, at most 1,000 canonical repository-relative valid-UTF-8/control-free paths, each at most 4 KiB and at most 256 KiB in aggregate, an at-most-512-KiB valid-UTF-8 NUL-free LF-canonical unified diff generated from the exact base and tip with Git's fail-closed attribute-source option and environment pinned to that tip and with local `diff.*` and output-changing configuration, external diff, text conversion, renames, color, hooks, prompts, replacement objects, lazy fetch, system/global configuration, and ambient Git/process authority disabled, plus a domain-separated digest of that complete projection. Fresh controller-pinned workspaces use an identity namespace disjoint from generic numeric workspace IDs, and line adoption rejects legacy shared-namespace IDs. Paths, refs, line records, reservation hashes, independently capped controller history, retained workspaces and repository-only rollups, and all related IDs, counts, bytes, and activity-derived shared-repository timestamps are structurally absent from generic stats, quota, logs, tools, workflows, HTTP, frontend, and model-call surfaces; guessed private IDs receive the ordinary not-found response, a later controller-owned lifecycle must account for retained storage separately, and generic acquire, release, cleanup, drop, and reconciliation cannot adopt, expose, unlock, or delete a line. String-tagged inventory version 2 rejects numeric-version rollback before rewrite. Missing, stale, changed, cross-line, reused-reservation, dirty, locked, mutating, malformed, symbolic, symlinked, reflog-bearing, packed-only/ref-moved, unsafe fsync/output configuration, over-limit, invalid-encoding, bare-CR, or cancellation state fails closed without a partial review, live-worktree fallback, reset, second line, fresh clone, reservation release, provider/network call, push, merge, or publication. | Untrusted code review must observe one immutable local commit without retaining edit authority or turning a manager-owned checkout, private reachability ref, reservation, Git process, generic workspace surface, or malformed repository data into an exfiltration, race, deletion, or publication capability. |
| `FR-SEC-032` | MUST | Schema-v10 PR-development controller state is a trusted local storage capability, not an execution or declassification surface. At most one stable controller/retained line belongs to one provider-verified thread and immutable pinned retained-workspace session/agent owner. First creation accepts only the latest queued or completed attempt, rejects active/other terminal ownership and sibling sessions, atomically suppresses that owner from legacy claims, proves exactly one repair session owns its `pdrk_`, collision-checks it against all active and retired controller reservations, and inherits that exact still-live bearer because it already locks the pinned workspace. Every later mutation gets a globally fresh `pdck_` bearer. This durable transfer invokes no worker; the present slice cannot complete a newly queued attempt or adopt ordinary dirty legacy work, so the later worker must adopt the clean pinned line before mutation. Expected revision plus exit headroom fence material transitions; exact attempt, non-regressing time, live deadline/token, and monotonic lease epoch fence state-changing lease writes. Exact Adopt or Resume equals the immutable owner pin; Resume advances mutation epoch before Park evidence. Only a completed latest owner attempt can append one contiguous exact park/version/epoch/intent/base/tip/tree/no-change/review-digest fence. The same transaction stores authenticated retired mutation lease/revision/token-digest proof, globally retires the reservation digest, clears usable mutation authority, and enters `review_pending`. A distinct reservation-free review lease may then claim only that fence; Finish folds authenticated review-completion proof into the final tail hash and enters `ready`, Release returns it unreviewed, and an expired review lease alone may rotate. An exact current operation encountering an eventing-recoverable mutation expiration—unbound in eventing, or bound at its active mutation epoch—preserves the bearer privately and enters `recovery_required`; read-only Get and stale-revision callers neither mutate state nor receive the raw bearer, and mutation is never automatically reclaimed. Complete reads validate verified identity, unique owner/pin/source/initial-reservation equality, completed fence ownership and causal order, phase/lease/bearer shape, exact reachable revision and lease-epoch relations, line state, two-stage contiguous hashes/versions, no-change tree preservation, and store-wide active/retired reservation non-reuse. RecordFence and FinishReview replay only with exact hash-bound retired token proof; exact committed Bind repeats without a write only after monotonic-time and live-deadline validation, and never returns authority past expiry. All controller, line, source, reservation, lease, fence, commit/tree, and digest fields are `json:"-"` and absent from browser, HTTP, workflow, tool, model, log, stats, and generic workspace surfaces; the Reader additionally redacts live lease and mutation bearers from its private result. Migration creates no owner/evidence. The APIs run no filesystem/Git, model/AI-review, CI, workflow/gate, commit, push, merge, HTTP/UI, provider acknowledgement, or publication effect, and `ready` proves none; their only legacy-worker change is explicit private claim suppression at ownership transfer. | Separating edit authority from immutable review ownership must survive crashes without leaking or reusing the mutation bearer, allowing concurrent mutation/review, stranding an acquired lease without exit headroom, accepting impossible local evidence, automatically retrying ambiguous effects, or turning a private row into model, browser, Git, CI, or provider authority. |
| `FR-SEC-033` | MUST | Schema-v11 PR-development ledger storage is a private evidence and review-terminalization capability. It accepts an attempt account only for an exact validated unreviewed controller fence and derives its owner case ordinal, commit/tree/no-change tuple, and mutation-stage fence hash rather than trusting caller copies. It accepts the paired structured review only immediately after that attempt and under the exact live reservation-free review lease; one transaction finalizes the authenticated fence/controller proof and appends the exact outcome/findings, so an independently finished fence without its ledger row cannot be backfilled. Absolute attempt/review ordinals preserve controller fence order, while the first post-upgrade row may authenticate an unreviewed parked fence without migration backfill and older reviewed fences may be skipped. Every free-form value is bounded UTF-8, every Git/digest value is canonical, review finding count and aggregate bytes are capped, timestamps are nonregressing, rows and findings are immutable, exact authenticated retries are no-write, and changed or out-of-order replay conflicts. Complete reads hold one snapshot while revalidating provider thread membership, owner session/case, controller/fence chain, attempt/review alternation and pairing, stage-specific fence hashes, timestamps, structured findings, and domain-separated entry/checkpoint hash chains. Logical compaction can only append a summary over an exact later fully reviewed prefix digest; it never deletes or updates raw entries, findings, or old checkpoints. The private context snapshot atomically binds the selected ordinal, complete thread high-water, and ledger. Its pure model projection excludes every local ID, provenance value, controller/lease/reservation/workspace/source field, internal diagnostic, hash, and CI digest; it labels included review/code/history text untrusted, orders only by authenticated ordinals, never substrings feedback or drops a mandatory ledger suffix record, and deterministically reports optional omissions. Oversized mandatory history requires compaction instead of prompt/provider caching or silent truncation. Migration creates empty tables only. No ledger API or projector executes Git/filesystem, CI, a model/reviewer/compactor, a workflow/gate, HTTP/UI, provider access, publication, push, merge, or acknowledgement. The generation-owned review worker is now the sole runtime consumer of this review-terminalization seam: under a separate exact review lease and runtime-generation boundary it verifies the completed orchestration, persisted CI evidence, ordered context, and parked snapshot before invoking one isolated no-tools/no-history/no-cache exact-agent reviewer. The worker and row grant no mutation, GitHub/provider-object, workflow/gate, browser, publication, push, merge, or acknowledgement authority. | Durable attempt memory and compaction must not let caller-supplied commit claims, prompt-cache eviction, review text, migration guesses, silent truncation, hash-chain corruption, or an unvalidated/unpaired storage row become execution authority, erase audit history, leak private controller state, or falsely prove that CI, review, or publication occurred. |
| `FR-SEC-034` | MUST | Pinned mutation recovery is a two-capability protocol between schema-v12 controller storage and the controller-only Git-workspace reservation rotator. An eventing-recoverable expiration atomically quarantines the old raw bearer and binds one globally fresh replacement, exact source/workspace and optional bound-line fence, expired lease-token digest, and hash-chained intent before any external effect. Only a separately token/deadline/epoch-fenced recovery claimant may receive that tuple; its lease is reclaimable solely because the permitted effect is the exact idempotent old-to-fresh rotation. The Git manager takes both reservation operation locks in canonical hash order, requires the old lock and fresh global nonuse, durably revokes the old hash, swaps the workspace and bound-line ownership atomically, and changes no code, Git ref, index, worktree, branch, object, or remote. Store finalization requires the exact still-live claim and matching rotation fence/proof, then installs the fresh controller bearer under a newly issued mutation lease, records non-authorizing hash-bound final proof including the issued deadline, and erases both staged raw-key copies. All new Go fields are structurally JSON-private; ordinary controller reads, generic workspace stats/tools/maintenance, models, workflows, HTTP, UI, logs, and providers receive neither bearer, claim, intent, checkout, ref, nor rotation history. Legacy recovery without v12 proof, pending park, stale/changed replay, reused reservation, corrupt chain, or re-expired final state fails closed. Neither side reconciles commit/park ambiguity or grants CI, model, gate, provider-write, publication, push, or merge authority. | An expired worker must lose usable filesystem authority without sacrificing unknown local work, and a recovery claimant or storage migration must not turn staged bearer material, guessed history, idempotent transfer, or a database row into broader code or publication authority. |
| `FR-SEC-035` | MUST | Only a trusted PR-development controller may use the schema-v13 operation boundary or the controller-only Git reconciliation methods. Before any exact retained-line Adopt, Resume, Commit, or Park effect, it must durably prepare one private hash-bound operation under the exact live mutation lease; at most one operation per controller may remain unfinished, and finalize accepts only the unchanged kind/request/result and immediate controller/attempt/line state. If that lease expires, the operation itself transitions to a separately token/deadline/epoch-fenced renewable/reclaimable claim and remains the sole live recovery authority; no pending schema-v12 row may be created. Adopt recovery through `RecoverPinnedLineAdoptReservation` and Resume recovery through `RecoverPinnedLineResumeReservation` acquire old and fresh reservation locks in canonical hash order and retain both through exact line convergence, inventory-v3 old-to-fresh ownership replacement, durable revocation, and result proof, closing the stale-bearer interval without adding an inventory version or history family. Commit recovery, under the continuously live operation claim, first uses exact old-to-fresh rotation and then deterministic `CommitPinned` with fresh. Park recovery uses only exact `ParkPinnedLine` replay with old, permanently retires it, and creates no replacement mutation bearer. Recovery finalization for Adopt/Resume/Commit appends linked already-finalized schema-v12 audit evidence, but that row is never pending, claimable, or authoritative. Park finalization atomically finalizes the operation, completes and version-advances an exact queued attempt when applicable, appends the immutable review fence, clears active attempt and mutation authority, and enters `review_pending`; a pre-controller completed attempt is unchanged and the private branch stays retained for the separate reservation-free reviewer, which now requires the exact completed orchestration before claiming that fence. An empty migrated v12 operation chain may hand off one already-bound live mutation directly to initial Commit/Park, while any established v13 history exclusively owns later Bind/Fence transitions. All operation/request/result/claim/recovery/bearer/controller/attempt/session/fence/path/ref evidence is structurally `json:"-"` and absent from ordinary Reader results, generic workspace tools/stats/maintenance, models, workflows, HTTP/UI/browser storage, logs, provider requests, and public errors. Missing preparation, wrong ordering, changed replay, stale claim, cross-owner evidence, dirty/drifted Git, reused reservation, partial Park state, corrupt chain, or inadequate headroom fails closed without a second effect or broader capability. Store methods execute no Git, and neither side discovers or runs CI, invokes a model/reviewer/gate, contacts a provider, publishes, pushes, merges, or acknowledges feedback. | Write-ahead effect identity and continuously fenced, least-authority reconciliation must make every SQLite/Git crash boundary recoverable without ever letting the expired worker, operation row, recovery claimant, retained branch, or atomic review handoff become a general edit, observation, or publication capability. |
| `FR-SEC-036` | MUST | Only a trusted local-development controller may request local-CI discovery or execution. Under the exact reservation-derived operation lock, Git Workspaces must revalidate the controller pin, exact pre-attempt parent, and current candidate evidence and materialize bounded disposable parent/candidate roots with no `.git`, symlink escape, special file, retained-checkout path, or lifecycle capability. One canonical full SHA-256 manifest binds every materialized candidate path, mode, type, size, and content to the exact parent/tree/candidate identities. Discovery reads only bounded supported definition and dependency files from both roots and executes no repository content. Exactly one `.picoclaw/ci.yml` or `.picoclaw/ci.yaml` is authoritative when present; otherwise a bounded repository-native quick profile precedes supported pull-request GitHub workflow fallback. Every accepted executable step runs in an independent fresh sandbox, so a GitHub job with multiple executable steps is incomplete `stateful_job_unsupported` rather than silently losing shared job state. Discovery produces a deterministic versioned nonempty ordered plan. Any definition or semantic-plan change is incomplete `plan_changed` and cannot execute or pass; dependency-only change alters the environment/result identity. Every required step must run only in the mandatory Linux Bubblewrap sandbox after a successful user-systemd cgroup-v2 supervisor handshake that proves delegated process and memory controls, never through host execution or fallback to the optional generic isolation runtime. The sandbox receives a clean allowlisted environment, trusted identified host toolchains, explicit controller-provided read-only offline dependency mounts, the disposable candidate plus bounded scratch/output filesystems, and no retained checkout or Git directory, inherited credential/config/locale/proxy/agent/provider state, network, sibling workspace, event database, or writable evidence store. Per-step and aggregate time/output/process limits are mandatory; cancellation or limit exhaustion terminates the complete cgroup process tree, and cleanup plus exact candidate postflight precede any `passed` result. Canonical result evidence binds the complete manifest, Git candidate, plan/policy/discovery, environment/toolchain/dependency, sandbox backend/profile, platform/architecture, and every required step/output digest. Owner-local immutable discovery records are indexed only by the exact parent/candidate manifests and discovery/plan versions, and execution evidence persists. Reusable result entries are permitted only for an unexpired, strictly decoded and re-hashed exact success under a complete immutable identity; production reuse is currently disabled because mutable host toolchains and dependency mounts lack complete immutable manifests. No prefix/fallback match or failed, canceled, incomplete, drifted, exhausted, cleanup-failed, or sandbox-unavailable result is green or reusable. Materialized roots and proven-quiescent scratch/process state are removed before return; inability to prove cgroup quiescence is non-green and leaves only quarantined owner-local scratch for operator cleanup. Discovery, execution, evidence lookup, and persistence create no model/controller/attempt/ledger/commit/park/branch/review/workflow/gate/provider/HTTP/UI/acknowledgement/publication/push/merge effect or authority. Missing or unsupported Bubblewrap/systemd/cgroup support or environment/toolchain; missing controller-provided mount or executable (`environment_unavailable`); a step discovering absent package contents (`failed`); stale or cross-workspace evidence; malformed/over-bound material; manifest drift; zero/incomplete plan; corrupt evidence; timeout/output/process-tree failure; cancellation; postflight; or cleanup fails closed. Dependency provisioning/downloading is outside this slice and must be wired later. | Repository-controlled CI definitions and dependencies must produce reproducible local evidence without becoming an ambient host-execution, credential/network exfiltration, retained-checkout tampering, cache-confusion, false-green, or publication capability. |
| `FR-SEC-037` | MUST | The PR-development attention launcher may observe only one integrity-checked reservation-free `attention_required` review snapshot and the already-persisted commit-addressed CI and retained-line review evidence. | The schema-v15 decision table and rich snapshot are owner-local private capabilities. The model-visible subject labels repository, provider, diff, CI, ledger, review, and conversation metadata as untrusted; it excludes controller/session/workspace/lease/reservation authority, provider credentials and object IDs, event/workflow provenance, storage hashes, attestation identities, raw internal errors, and conversation content. The complete exact conversation is copied only into a stable protected review-scoped session owned by the immutable repair agent, compare-and-swap fenced to the snapshot transcript, hidden from ordinary session discovery, and frozen once into the private workflow root. Isolated gates get no session/history/cache/tools; deterministic and zero gates invoke no model. | The runtime-generation lease and per-case projection lock cover session admission/replacement and synchronous private-root capture, but neither is held while a human task waits. Durable decision identity binds semantic evidence and policy rather than a live lease, controller revision, or session CAS token. Gate launch is read-only with respect to PR development: it acquires no mutation/review lease, changes no retained branch or controller, and grants no Git/provider/workflow caller access beyond the private decision run. | Stale or changed high-water evidence, cross-agent session scope, alias collision, rollback, CAS conflict, incomplete private capture, malformed policy/run, oversized mandatory evidence, or any attempt to turn a gate result into chat, repair, push, publication, acknowledgement, or merge authority fails closed. Raw ledger/conversation rows and the branch remain; compaction is logical and cannot summarize away the target attempt/review pair. | Asking for attention must not leak lifecycle capabilities into models or UI, freeze an idle branch, couple a human wait to a runtime lease, or let a workflow result mutate or publish code without a separately fenced later action. |
| `FR-SEC-038` | MUST | Automatic PR-development attention may add only one owner-local occurrence beside an exact `attention_required` ledger completion, lease only its delivery schedule, pin one canonical configured policy plus immutable subject identity, and expose one case-owned browser conversation after validating the complete occurrence-to-task chain. | Every trigger, lease, retry, policy, subject, run, task, controller, session, ledger, CI, Git, and integrity field is structurally private. The worker receives no mutation reservation or provider-write capability and releases its scheduling/runtime leases before a human wait. Working-context session projection remains hidden and exact-prefix fenced; isolated gates receive no history/cache/tools. The public DTO can represent only case version, bounded status, configured title/questions, accepted plain-text response, and an opaque response fence for exactly one waiting or recovery turn. The launcher replaces browser authority with the process bearer only for canonical same-origin requests; the protected handler rejects browser provenance. Tokens contain no decodable identity and are never placed in a URL, log, browser storage, generic workflow surface, or model prompt. | Response processing reloads and revalidates the case, current occurrence, canonical pin, subject/run decision link, private workflow reference, stable run/task snapshot, task payload hash, original waiting revision, and response fence before deriving a separate idempotent response identity. Exact accepted replay only reprojects authoritative state. A response may resume that task and nothing else; chat and repair remain separate explicit case operations, and provider refresh, branch mutation, push, publication, acknowledgement, merge, and deletion require later independently fenced capabilities. Generic workflow observation, task, retry, cancel, and retention paths continue to hide and preserve every exact private attention run. | A stale, cross-case, altered, duplicated, malformed, oversized, noncanonical, superseded, corrupt, or runtime-disabled request fails closed without partial authority or raw diagnostics. Trigger recovery states never cause an automatic second model/run, and no waiting task retains the occurrence lease, runtime generation, controller lease, or mutation reservation. | Bringing a user into an AI-mediated PR discussion must not reveal private orchestration identities, convert a browser token into general workflow access, keep code locked while waiting, or make answering a question equivalent to authorizing code or provider effects. |
| `FR-SEC-039` | MUST | An authenticated user reads the exact selected-case PR-development detail through the existing protected runtime and launcher GET boundary. | One SQLite snapshot integrity-validates the exact case, conversation, repair session and latest public attempt, verified provider thread, complete controller aggregate, and ledger before a dedicated field-by-field DTO may deliberately declassify at most one optional `local_development` object. Live or otherwise incomplete lifecycle is derived only from the public attempt. For a completed public attempt, the snapshot additionally loads its exact orchestration; commit/CI/review fields remain absent unless that orchestration proves completed and binds its validation receipt to the exact attempt-ledger entry. The DTO can represent only `attempt_id`, `attempt_ordinal`, `attempt_status`, bounded `summary`, exact local `commit_sha`, `no_changes`, terminal `ci_status`, canonical lowercase 64-hex equality-only `ci_plan_digest` and `ci_result_digest`, `review_status` (`not_started`, `pending`, or `completed`), fixed `review_outcome`, bounded `review_summary`, bounded `review_finding_count`, derived `local_ready`, and `updated_at`, under Event Automation's progressive omission contract. The opaque `pdr_` attempt identity is already public; the commit is candidate content identity, not a ref; and the two CI fingerprints reveal no plan, command, output, environment, attestation, or reusable cache authority. | The private source aggregate and all controller, orchestration, and ledger members remain structurally excluded from JSON. The read adds no route, schema, table, index, migration, backfill, or mutation; changes no database, browser, workspace, repository, branch, provider, run, or task state; invokes no model, CI, Git, or workflow; and grants no repair, reservation, provider refresh/write, push, publication, acknowledgement, merge, or deletion capability. `local_ready` is only a read-derived statement about the exact local candidate's passed CI, passed paired review, and matching reservation-free ready controller; it is neither a bearer nor a publication decision. | No live incomplete orchestration is required or exposed. Commit/CI/review fields require the exact completed orchestration-to-receipt-to-attempt-ledger linkage: a legacy or otherwise unbound ledger row whose compatibility loader defaults CI to passed remains `not_started` and can never make `local_ready` true. Invalid, cross-case, duplicated, nonlatest, unpaired, terminality-inconsistent, malformed, over-bound, or corrupt evidence fails the complete detail read with a fixed public error instead of partial fallback. The DTO cannot represent a workspace/checkout or internal Git ref; thread, controller, ledger, fence, checkpoint, orchestration, operation, run, task, or workflow identity/hash; source/tree/candidate/manifest/receipt/attestation/internal digest; lease, reservation, claim, worker, epoch, credential, provider-object, or provider-write field; raw finding; raw CI plan, step, command, path, output, error, or diagnostic; or sibling-case history. | Showing local progress must not turn private controller evidence, compatibility defaults, raw review/CI data, or a convenient green badge into an exfiltration channel or authority to mutate code or a provider. |
| `FR-SEC-040` | MUST | Only a trusted local-development controller may call `Manager.SuspendPinnedLine`, `Manager.SuspendPinnedLineCommitRecovery`, or `Manager.ResumeSuspendedPinnedLine`. Every call accepts exact canonical repository/source/workspace/line/agent/version/epoch/tip/tree identity, one caller-durable bounded intent, and either the current raw mutation bearer or one globally fresh resume bearer; commit-recovery additionally requires the complete immutable prepared `CommitPinned` request. Under the reservation-derived cross-process operation lock and inventory lock, suspend revalidates manager-owned nonsymlink paths, raw origin, source ancestry, exclusive no-reflog private ref, detached `HEAD`, real index, absence of merge/rebase/sequencer/Git-lock and pending-Park state, and all existing replacement/graft/sparse/config/control-plane exclusions. A manager-private temporary index captures all tracked and nonignored untracked ordinary content relative to the retained parent, rejects changed gitlinks and bounded-output overflow, and persists only `CandidateTree`/digest/count plus hashed identity. Commit-recovery recreates and byte-verifies the deterministic prepared child, hash-binds that child and its exact `PreparedTree`, accepts only the exact parent or child `HEAD`, records whether the child was applied, and never applies a missing commit, moves the retained ref, or discards later ordinary content. `CandidateTree` is independently captured from current ordinary content over the retained parent; it must equal `PreparedTree` while the child remains unapplied, and only an applied child may retain later ordinary edits in a differing candidate. One atomic inventory-v4 save appends a domain-separated per-line count/tail-anchored suspension record, permanently retires the current reservation hash, marks the line `suspended`, and clears workspace/line ownership while leaving the checkout, ref, ordinary files, and line version/tip/tree/epoch retained. Resume verifies the exact latest record and `CandidateTree` under a globally unused fresh bearer; for an applied child it verifies the prepared commit/tree pair, compare-and-swaps detached `HEAD` back to the retained parent, and resets only the real index from the prepared or parent tree to the retained parent while leaving worktree files byte-preserved, accepts only that exact crash-reconcilable child-or-parent transition, re-snapshots `CandidateTree` equality, then installs the fresh owner without changing the already-issued mutation epoch. Every Git subprocess is bounded direct argv with separately drained output, clean environment, hooks/signing/editor/pager/prompt disabled, no shell, network, fetch, replacement object, lazy fetch, or system/global/ambient configuration. Raw bearers exist only in live workspace locks and controller call memory; suspension records contain only domain-separated hashes and participate in store-wide nonreuse. Records, anchors, candidate/commit evidence, paths, refs, hashes, agent/intent identity, timestamps, and replay state are private inventory with no JSON/model/workflow/tool/log/stats/quota/HTTP/UI/provider projection. A suspended line is neither parked nor locally ready and grants no CI, review, attention, publication, push, merge, acknowledgement, release, cleanup, or deletion capability. Malformed, stale, cross-owner, reused, partial, nonlatest, corrupt, over-bound, unsafe-index/`HEAD`, changed ref/candidate control plane, prepared-commit/`PreparedTree` mismatch, rollback, compare-and-swap ambiguity, or later progress fails closed without partial record, authority transfer, ordinary-file rewrite, WIP commit, review fence, or fallback reset/clone. | Recovery and idle handoff must revoke edit authority while preserving exact unknown local work, and prepared-Commit ambiguity must not become data loss, a duplicate or fabricated commit, a generic observation channel, a false green/review state, or remote publication authority. |
| `FR-SEC-041` | MUST | Only the generation-owned PR-development recovery worker may claim or reclaim an eligible bound schema-v12 or schema-v13 recovery and compose it with schema-v17 suspension handoff; only the later repair controller holding the exact queued-orchestration scheduling claim may prepare and complete a suspended resume. Recovery scheduling authority, event-runtime generation ownership, Git mutation authority, and any later model/provider authority are distinct capabilities. | The worker receives only one exact private recovery claim and its kind-specific request. Adopt and Resume use the exact continuously locked old-to-fresh composites; bound-v12 and Commit use exact old-to-fresh rotation, with Commit then invoking only the prepared deterministic `CommitPinned` request; Park invokes only exact old-bearer `ParkPinnedLine` and its parked snapshot. For every non-Park effect, eventing must durably authenticate and checkpoint the complete exact recovery/rotation result before suspension, retain only the fresh raw bearer needed by the current renewable claim, issue no mutation lease, and expose no model-capable interval. Ordinary recovered lines call exact candidate suspension; Commit calls prepared-Commit recovery suspension so applied/unapplied child state and later ordinary content remain distinct and preserved. Finalization validates the exact suspension result, removes every raw old/fresh/claim copy, clears all authority, and leaves only a private hash-bound `suspended` retained line. Park bypasses suspension and reaches only its existing atomic reservation-free review handoff. | A later queued repair must persist one exact latest-suspension resume intent and globally unused bearer before Git, replay `ResumeSuspendedPinnedLine` after any crash, and atomically move that sole bearer into the live controller mutation lease before any context-compaction or edit model request. The staged resume copy is erased at that transition. If the scheduling owner disappears after a resume may have reached Git, only the generation-owned recovery worker may reclaim the expired resume, exact-replay it, atomically append a hash-linked `suspended_resume_recovery` handoff that receives the fresh bearer, and suspend again before releasing its claim; it never inherits model authority. `suspension_pending`, `suspended`, their checkpoint/result/candidate/prepared-Commit evidence, recovery and resume claims, raw or hashed bearers, paths, refs, controller/attempt/orchestration identities, and diagnostics remain structurally absent from JSON, browser/model/workflow/tool/provider inputs, generic workspace surfaces, logs, stats, and errors. Both phases are never parked, reviewed, locally ready, attention-ready, publishable, or cleanup/release eligible. | A stale generation or claim, altered exact result, cross-controller or cross-line evidence, reused bearer/intent, inadequate rotation/suspension/resume capacity, candidate or prepared-Commit drift, partial raw-key erasure, or ambiguous eventing finalization fails closed without a second Git effect, model call, reset, release, review fence, or provider action. Automatic selection must never claim, reclaim, rotate, or release a schema-v12 recovery without a complete bound-line fence; such legacy unbound state remains unchanged and non-ready until a separately reviewed idempotent retirement protocol covers lost responses and proves stale-bearer revocation. Worker construction and readiness are independent of GitHub/provider objects and model availability, and reload/shutdown drains the exact runtime generation before manager or store closure. | Recovery must minimize the lifetime and audience of raw mutation bearers, preserve crash-ambiguous local work exactly, and prove authority retirement before any AI can observe or continue the retained candidate. |
| `FR-SEC-042` | MUST | Only a trusted controller may call `Manager.PushPinnedLine` with equality fences for the stored repository/source pin, workspace/line identity, complete parked version/epoch/Park/base/tip/tree state, and one expected remote tip. The request cannot carry a credential, helper, checkout path, internal ref, reservation bearer, arbitrary refspec, push option, or transport command. | Under the manager mutex followed by the kernel inventory lock, the manager requires the exact reservation-free parked line, revalidates its clean detached checkout, private ref, origin and Git control plane, proves source commit <= expected remote tip <= parked tip, derives only `refs/heads/<stored source ref>`, and uses the literal stored repository rather than a configured remote name. Bounded direct-argv Git observes that one branch, performs at most one explicit expected-OID `--force-with-lease` update to the parked tip, and uses detached readback plus local postflight after a push may have started. Local client hooks, signing, tag following, submodule recursion, arbitrary push options, force-includes, prompts, and caller- or repository-supplied transport commands are disabled; the existing fixed trusted SSH command, ambient controller/operator transport, and remote endpoint including its server-side hooks remain trusted under the existing threat model. | The client submits at most that one remote-ref update; effects independently caused by the trusted remote endpoint are outside this primitive. No inventory, schema, history, local ref, `HEAD`, index, worktree, line/reservation/readiness/review state, credential record, provider object, or publication record changes. The sanitized transient result exposes only the exact non-path fence, derived destination, expected/observed tips, disposition, and local-cleanliness fact. It grants no provider refresh, acknowledgement, merge, workflow, gate, model, tool, HTTP, or UI authority. | Invalid/stale identity, unsafe local state/configuration, a missing or different preflight tip, alternate target, tag/delete/multi-ref behavior, bounded-output failure, or non-cancellation unavailable pre-effect transport fails closed with fixed sentinels and no raw remote output; caller cancellation or deadline expiry returns its context error. Once push may have started, only the exact desired remote tip proves success; otherwise outcome is unknown and MUST NOT be automatically retried because expected-to-tip-to-expected ABA is indistinguishable from no effect. Proven remote success plus local drift returns its sanitized receipt joined with the drift error. This contract adds no credential broker and makes no stronger proxy, SSL, netrc, or ambient-operator isolation claim. | An exact remote branch update must not turn caller data, repository configuration, uncertain transport outcome, a green local badge, or ambient model/workflow state into arbitrary Git, credential, provider, publication, or retry authority. |
| `FR-SEC-043` | MUST | Only trusted private eventing callers may create or transition a schema-v18 PR-development publication. The passed-review completion is the sole occurrence-admission boundary and must atomically bind one exact green non-compatibility CI receipt, adjacent immutable attempt/review ledger hashes, completed orchestration, ready reservation-free controller, and full retained-line source/Park fence; migration and historical replay never infer an occurrence. Every field of every publication DTO or record struct is structurally `json:"-"`. Policy, subject, provider observation, expected remote tip, private decision-run linkage, push request/result, lease owner/token/deadline/epoch, hashes, internal error detail, and timestamps remain owner-local private state with canonical bounded encodings. Canonical evidence blobs and hashes are create-once and equality-checked; lifecycle timestamps advance only through exact fenced transitions. No raw provider/Git output, credential, transport config, checkout path, runtime/system prompt, model response, workflow-private root or session, raw conversation, or browser data may be stored in the journal. Configured gate criteria/questions in the bounded policy and the bounded canonical gate-subject envelope are the only deliberately retained workflow inputs. A publication claim is scheduling authority only: `pending`, pre-effect `claimed`, `gate_waiting`, and `push_ready` hold no mutation reservation, do not make the parked line a generic capability, release ownership during human wait, and cannot block a later repair. Queue renewal accepts only live pre-effect `claimed` authority; PushJournal renewal accepts only live `push_started` authority. The separate DecisionRunStore binds one semantic immutable key to one deterministic private run inside the create callback's SQLite transaction. Deterministic RunID creation must be idempotent and replayable, and a non-nil callback error must guarantee that no external run was created. A nil callback return followed by SQLite commit uncertainty returns `ErrPRDevelopmentPublicationAdmissionUncertain`; automatic callers must not invoke create again and may only explicitly terminalize or recover through an exact store transition. `StartPRDevelopmentPublicationPush` is a write-ahead store transition, not Git authority: under immediate SQLite serialization it authenticates the exact push-ready claim, revalidates all local high-water and a caller-supplied provider observation strictly after the current claim and immutable pin and no more than five minutes old whose canonical facts are byte-identical to that pin, persists the full canonical request, and enters `push_started`. Controller mutation acquisition checks the same state, making `push_started` the only publication phase that excludes mutation and ensuring exactly one admission wins. The store may retain exact proven `applied`, `already_current`, or `reconciled` publication and local drift, but an expired or unproved `push_started` becomes execution-terminal and non-reclaimable `outcome_unknown`; it never returns to an execution queue. Only a distinct independently supplied minimal read-only remote-head observation strictly after unknown completion, no more than five minutes old, and equal to the immutable desired tip can reconcile it as published; it neither overwrites the original provider/review pin nor requires that review to remain unchanged. Expected/other/missing/unavailable observations remain unknown because remote OID ABA cannot prove no prior effect. Stale claim, changed pin, later attempt, local/provider drift, cross-record identity, malformed/over-bound bytes, partial result, or atomic insert failure fails closed without a provider, workflow, model, Git, gateway, worker, HTTP/UI, acknowledgement, or merge effect; a failed occurrence insert rolls back the entire review/fence/controller/ledger completion. Publication success grants neither review acknowledgement nor pull-request merge authority, whose concrete behaviors remain undefined and unimplemented. | A durable publisher needs crash-reconstructible evidence and a narrowly timed mutation fence without leaking controller/provider/workflow capabilities, holding an idle checkout through human discussion, retrying an ambiguous remote effect, or silently choosing acknowledgement and merge policy. |
| `FR-SEC-044` | MUST | Only a trusted local publication controller may invoke one of the distinct GitHub publication observers with one integrity-checked immutable development case and its exact non-legacy provider thread identity. | The full observer has only generation-fenced `github/pull_request_read` authority and reuses the bounded exact-JSON PR/review verifier before returning a structurally private schema-v18 provider observation. The distinct unknown-outcome observer performs exactly one read-only PR `get`, validates canonical provider origin plus immutable provider-object, author, pull URL/number, target-repository identity, and a strict canonical Git head ref/OID, and returns only current head repository/ref/SHA; it never reads or depends on the mutable review. Both sample their UTC `ObservedAt` locally only after complete validation and recheck cancellation before and after clock sampling. | Separate constructors copy verifier configuration into unexported concrete adapters implementing only their respective full or head-only interface. A caller cannot type-assert one into the other or recover a runner, clock, credential, artifact path, provider-write client, store, claim, workflow, model, session, checkout, Git, push, acknowledgement, merge, HTTP, or browser capability through the interface or result. Exact JSON artifacts remain confined to the existing private artifact root and are removed after their one bounded read. Closed/merged state or later review edit/dismissal is acceptable only to the head-only reconciliation read; the schema-v18 outcome reconciler separately requires a fresh post-unknown observation with exact immutable branch identity and desired tip. | Invalid durable evidence, legacy/cross-provider identity, noncanonical origin, provider drift, unsafe/malformed/oversized output, cancellation before, during, or immediately after a read, or read failure returns no partial result and causes no fallback or write. The full path still requires the exact actionable captured review and canonical clone endpoint; the head-only path ignores the unprojected clone endpoint, never retries an uncertain push, and never interprets remote-tip ABA as proof that the earlier effect did or did not occur. | Fresh provider facts are necessary publication input, but their acquisition must remain a minimal read capability that cannot inherit or manufacture effect authority. |
| `FR-SEC-045` | MUST | Only a trusted publication-gate coordinator holding the narrow gate-context reader and an exact live schema-v18 publication lease may request the local evidence used to build a first gate subject; that durable claim must still be `claimed` from `pending` under the supplied publication ID, token, epoch, and unexpired deadline. | One SQLite snapshot authenticates the lease and returns a detached, structurally private `PRDevelopmentPublicationGateContextSnapshot`: the claim-redacted publication, selected case ordinal, complete integrity-checked canonical conversation plus rolling transcript digest, exact adjacent passed attempt/review ledger tail, immutable case and provider thread, authority-redacted owner repair session, controller, matching review fence and completed orchestration, and full ledger. The returned session has its retired reservation key plus every attempt idempotency key, lease owner, lease token, and lease deadline cleared after full validation. It reuses the existing canonical conversation and evidence types and their validation paths; it creates no parallel raw-conversation encoding, table, message projection, or model input. | Every snapshot field and nested authority/evidence value remains `json:"-"`; the returned publication cannot carry a claim token and its owner session cannot carry repair scheduling authority. The first subject pin requires snapshot-derived expected conversation version and digest and compares both against the integrity-loaded current conversation in the same immediate transaction before storing the subject. An exact already-pinned replay is resolved before that mutable high-water comparison and returns the immutable prior result after later chat without revealing or rewriting the newer transcript; changed replay fails. Neither capability can widen into queue renewal, provider observation, workflow/run admission, model invocation or repair-session mutation, filesystem/checkout, Git, push, gateway, worker, HTTP/UI, acknowledgement, or merge authority. | Stale, expired, replaced, wrong-origin, cross-record, malformed, or canceled claims; corrupt or inconsistent publication/evidence graphs; conversation version/digest drift before first pin; invalid private JSON shape; or any failed nested validation yields no partial snapshot or mutation and no fallback read. The snapshot alone authorizes no gate result or publication transition, and exact replay authorizes no use of later chat. | A gate coordinator needs coherent least-authority local context without leaking its lease or private evidence graph, and conversation concurrency must neither silently retarget a first subject nor make a completed immutable pin unreplayable. |
| `FR-SEC-046` | MUST | Only a trusted in-process coordinator may call `prdevelopment.PublicationGateProcessor` with one already claimed-from-`pending` schema-v18 publication and separately injected narrow policy, exact-claim authentication, context-read, pre-effect queue, and full provider-observation capabilities. Before any caller-provided pin can select a fast path, `AuthenticateClaimedPRDevelopmentPublicationGate` proves the exact live token/epoch, pending claim origin, complete current local high-water, and authoritative pin progression while returning only the claim-redacted publication plus the exact repository policy selector and no conversation or rich gate context. The processor replaces stale caller state with that authoritative progression, while rejecting any supplied pin that differs, and cannot acquire, renew, release, or enumerate scheduling authority itself. It captures an unpinned policy through `attention.PolicySource`/`PreparePolicy` using that authenticated selector, thereafter accepts only the strict canonical `DecodePreparedPolicy` result, and uses `CompileGateWorkflow` solely to confirm that an empty, disabled, or all-zero composition has no executable workflow. Any active or mixed composition pins policy only and returns `requires_execution` without reading local subject context or the provider. A confirmed no-op alone may pin the bounded versioned conversation-fenced zero subject, pin a freshly verified read-only provider observation, and enter `push_ready` with no run ID. Exact replay checks and consumes each authenticated durable pin before the capability that produced it, so later chat, policy drift, or retry cannot repeat or retarget an earlier read. Every processor request/result, pin, observation, subject value, claim identity, and nested authority field remains `json:"-"`; no config or schema migration is added. The processor exposes no decision-run store, workflow executor, model/session, repair mutation, checkout/filesystem, Git/push, gateway/worker, provider-write, acknowledgement, or merge capability. Transient, malformed, unclassified, or processor-local inconsistency retains the still-live claim and completed pins for a separately authorized caller to pass to the exact safe-requeue seam in `FR-SEC-047`; the processor itself never invokes that seam. Only an exact validating store boundary may record a proven provider/local conflict, causal supersession, or other exact pre-start terminal outcome. A matching public sentinel returned by a policy or provider capability grants no terminalization authority. | A zero-gate fast path must not turn orchestration convenience, policy access, a provider reader, or replay after partial progress into broader scheduling, model, repository, provider-write, or serialization authority. |
| `FR-SEC-047` | MUST | Only a trusted coordinator that has not crossed the durable Git push-start boundary may call `Store.RequeuePRDevelopmentPublication` with a structurally private `PRDevelopmentPublicationRequeue` containing one exact live publication claim token/epoch, its expected durable origin (`pending`, `gate_waiting`, or `push_ready`), and a canonical `AvailableAt`. In one immediate schema-v18 transaction, the store first integrity-loads the publication. An already-restored unclaimed record is a no-write replay only when the stored claim epoch, restored status, and availability match exactly; that replay resolves before live clock/lease validation and may remain valid after becoming due. Otherwise the store authenticates the unexpired live claim and exact origin, requires availability no earlier than its own sampled transition time, restores only that origin, clears scheduling owner/token/deadline/origin fields, and preserves the claim/attempt counters plus every immutable occurrence identity, local-evidence, policy, subject, provider, decision-run, expected-tip, and recorded parked-line field. A different replay epoch, origin, or time conflicts. The method adds no migration or column, exposes only an authority-redacted result, stores no raw error, and neither classifies failures nor computes backoff. It cannot read or refresh local high-water or current branch/reservation state, observe or write a provider, acquire a reservation, inspect a checkout/filesystem, invoke Git/push, execute a workflow/model/run, start a gateway/worker, acknowledge a review, or merge a pull request. Invalid, stale, expired, replaced, cross-record, wrong-origin, any other unclaimed, `push_started`, terminal, corrupt, canceled, or live-transition past-availability input fails closed without releasing another claim or changing evidence; the already-restored exact committed replay is the sole unclaimed exception and does not repeat a clock or lease check. | A retry seam must release only idle scheduling authority without becoming a broad recovery capability, erasing restart pins, touching the retained branch, or allowing an ambiguous Git push effect to be replayed. |

| `FR-SEC-048` | MUST | Only the trusted PR-development case reader and existing case-owned attention bridge may declassify an already-linked schema-v18 before-push publication decision. The atomic store read may attach only one structurally private `PRDevelopmentPublicationAttentionProjection` to the exact case/conversation/current passed-review snapshot after validating the full publication row, case and review ID/hash/outcome binding, complete policy/subject/provider pins, decision link, and current local high-water. It keeps the local-review trigger and publication projections separate, derives independent `Current` and `AttentionRequired` flags, and rejects a snapshot in which both sources are current. Only a wrapped causal publication supersession becomes historical absence; conflict, corruption, or other lookup failure aborts the whole read. The public list boolean is only the OR of the existing exact local-review predicate and a bounded current publication relational tail in `gate_waiting` or `claimed` from `gate_waiting`; it checks structurally present pins/run while leaving canonical policy/hash/run validation to the case-owned detail read, and reveals no source, deadline, claim, pin, or run field. | The bridge accepts the atomic snapshot as its sole source selector and cannot fall back when the selected source is invalid. For a publication it strictly decodes the private pinned active policy, re-derives the exact semantic decision key and deterministic run ID, and matches the durable decision link and private workflow reference before using the existing bounded conversation engine. Publication response fences have a distinct domain and bind the server-loaded publication/review/policy/subject/provider decision identity plus exact task and waiting revision; the browser still submits only case version, opaque fence, and bounded normalized answer. `gate_waiting` or `claimed` from it may expose the exact actionable task; a linked transiently requeued `pending` publication is queued and `claimed` from `pending` is checking, with neither exposing a response token; `push_ready`, `claimed` from it, `push_started`, and current `published` may expose only consistent completed history. A waiting task after the publication crossed the gate is invalid. Every publication identity, status/origin/claim, pin, decision/run/task/input hash, provider/local evidence, and diagnostic remains absent from the unchanged public DTO, routes, URL, browser storage, logs, and errors. | A valid response consumes only the exact private human-task answer and resumes the already-admitted run continuation; that ordinary continuation may execute remaining configured gates/models or create later human tasks. The bridge cannot admit/start a run, interpret its result, renew/requeue/transition a publication, acquire a checkout or reservation, invoke CI/Git/provider actions, push, acknowledge a review, or merge outside that exact continuation. A stale/cross-source/cross-case fence, simultaneous current source, incomplete pin/link, wrong lifecycle, zero-only linked run, changed high-water, superseded publication, malformed run/task, unsettled workflow behind a terminal publication, unavailable executor, cancellation, or source mismatch fails closed in the detail/response path without returning partial private state or mutating publication/code/provider state. The coarse list hint may over-notify until detail rejects same-shape corrupt private pin/run data, but it never supplies a response fence or effect authority. List/GET polling remains inert, claimed-from-wait scheduling expiry grants no browser authority, exact accepted replay stays idempotent and source-bound, and a later attempt hides prior publication history rather than leaving response authority attached to stale code. | Reusing one case chat for local-review and before-push questions must not turn a coarse marker, browser response token, expired scheduler claim, or completed gate history into a provenance oracle or publication capability. |
| `FR-SEC-049` | MUST | Active before-push publication execution is available only to a trusted in-process gate handler receiving one exact already-claimed schema-v18 publication from `pending` or `gate_waiting` plus separately injected exact-claim authentication/renewal/requeue/transition, claimed context, immutable CI evidence, parked-review projection, full provider-read observation, decision binding/private runner, owner-agent runtime, and conversation-session capabilities. Every capability-bearing request/result wrapper, handler, claim, pin request, replay anchor, provider fact, decision/run identity, runtime/session reference, and nested private evidence field is structurally `json:"-"`; the sole serializable internal value is the bounded canonical active-subject envelope written only into the already-private `PinnedSubject` bytes. For `pending`, the handler first composes `FR-SEC-046`, strictly decodes only the pinned active policy, and consumes each complete policy, subject, provider, and decision-run pin before invoking its producer. A first subject is compiler-preflighted before pinning and is one canonical bounded private envelope binding exact publication/case/passed-review/policy/evidence identity plus captured conversation version/digest. Its nested gate subject is exactly the existing bounded untrusted attention-evidence projection: projection format/notice, provider/ledger/target/diff data, bounded CI evidence and sanitized plan diagnostics, and conversation count/version plus protected-storage marker may enter `CompileGateWorkflow` or a gate/model. Raw conversation, local IDs and hashes, transcript digest, publication/evidence replay bindings, capability data, runtime/session identity, and raw internal diagnostics never do. A working-context composition must name exactly the immutable repair owner; under one case lock and exact owner runtime, the current integrity-checked append-only transcript must contain the envelope's exact version/digest prefix, and only that prefix may be projected into the protected read-only session and frozen with the private workflow root. Isolated, deterministic, and zero gates receive no session. The full provider observation is consumed or pinned only after subject pinning. The semantic decision key and deterministic run ID are re-derived from immutable pins; an exact linked/orphan lookup precedes every avoidable mutable context, session, runtime, or provider read, and `attention.PrivateRunner` remains the sole create-once ordinary mixed-workflow admission/execution boundary. A `gate_waiting` handler may only inspect and map the already-linked run, never rebuild context, observe the provider, admit, recreate, execute, or resume it. Waiting releases the publication claim, case lock, runtime generation, and all transient owners while the retained line remains parked and mutation-reservation-free. Exact run status alone maps waiting to `gate_waiting`, only succeeded to `push_ready`, and failed/canceled/skipped to pre-start `failed/gate_failed`; missing, malformed, cross-bound, orphaned, or admission-uncertain identity maps to `recovery_required`. A linked `gate_waiting` run that is still `running` returns to reservation-free `gate_waiting` with bounded delay; a pending run still `running` after synchronous admission becomes `recovery_required`. Other classified transient pre-effect errors requeue only to the authenticated exact origin with bounded detached cleanup and preserved pins/link. Renewal ownership loss stops effects and relies only on exact fenced expiry and reclaim. Only the validating store may map exact provider/local conflict or causal supersession. The internal `PublicationDispatcher` accepts no queue capability, validates one caller-owned `claimed` record, routes solely by exact `claim_from`, forwards handler errors unchanged, and requires distinct pending, gate-waiting, and push-ready handlers; it never claims, renews, requeues, transitions, or widens a handler. Since this slice supplies no real push-ready handler, no gateway/worker/production claim loop may be constructed over `ClaimPRDevelopmentPublications`, which returns all three phases. Missing/stale/expired/replaced/cross-origin authority, policy/compiler disagreement, noncanonical or over-limit envelope/subject, conversation rollback/digest mismatch, owner-agent mismatch, changed evidence/provider/high-water, run ambiguity/corruption, cleanup-fence loss, or cancellation fails closed without a second run, later transcript use, or partial declassification. This contract adds no schema, configuration, route, DTO, UI, checkout/Git, local-CI execution, push, provider write, review acknowledgement, merge, or unknown-outcome reconciliation capability; `push_ready` remains scheduling state rather than effect authority. | Reusing ordinary mixed workflow primitives for publication must not turn durable replay metadata, a protected transcript, an idle retained line, a browser answer, a private run, or a partially composed phase router into model-visible provenance, mutation authority, duplicate execution, or an unfenced remote effect. |

| `FR-SEC-050` | MUST | Only a trusted in-process `PublicationPushReadyHandler` may compose one exact already-claimed schema-v18 publication from `push_ready` with separately injected push-claim authentication, pre-effect queue renewal/requeue, full read-only provider observation, push-journal start/renew/finalize, and the narrow `PushPinnedLine` capability. Every newly introduced capability-bearing request, result, and configuration-wrapper field is structurally `json:"-"`; the existing case/thread, provider, journal, and pinned-line evidence travels only beneath those private fields or through narrow interfaces and is never serialized or projected by this handler. An immediate renewal and `AuthenticateClaimedPRDevelopmentPublicationPush` must prove the unexpired exact ID/token/epoch/origin, complete current local high-water, authoritative immutable pins, exact immutable case, provider thread identity, and reservation-free parked-line fence before the handler may observe the provider. The fresh full observation must canonically repeat the pinned provider facts. The handler derives one exact parked-line request solely from that authenticated durable evidence and fresh expected remote tip, drains pre-effect renewal, and commits `StartPRDevelopmentPublicationPush` before receiving Git authority. Only the returned `newlyStarted=true` fact grants one call to `PushPinnedLine`, and that call must use the journal-returned request rather than caller state. An exact historical or lost-response start replay never invokes Git. After start, only push-journal renewal remains active until Git returns; it is drained at the immediate finalization barrier, and queue renewal/requeue is no longer valid. Exact complete Git results map deterministically to published, published-with-local-drift, `conflict/push_conflict`, or `failed/push_failed`; cancellation, deadline, explicit uncertainty, invalid/partial output, unclassified post-start error, or an unrecoverable confirmed-post-start response boundary is classified as `outcome_unknown`, which takes precedence over any joined conflict or workspace-drift error. A renewal failure records that outcome immediately only while the exact started claim remains live; definitively stale authority leaves inert `push_started` state for the store expiry transition. Exact finalization replay is the sole lost-response recovery and never repeats Git. Before start, only observation-proven provider drift or store-proven local conflict/supersession may terminalize, and a classified transient failure may requeue the exact ready origin with every pin intact. The branch remains parked and mutation-reservation-free throughout; `push_started` excludes concurrent controller mutation through the existing store fence rather than by lending a reservation to this handler. Stale, changed, cross-bound, malformed, incomplete, or lost authority fails closed, and no post-start path returns to an executable queue. The handler, authenticator, adapter, and dispatcher acquire no model/workflow, repair/controller mutation, checkout path, generic Git, caller-selectable credential/transport configuration or generic transport beyond the exact pinned-line effect, provider review-write, review acknowledgement, thread/comment resolution, merge, HTTP/UI, configuration, schema-migration, production claim-loop, or unknown-outcome-observer authority. | A publisher must be able to perform one exact remote branch compare-and-swap without turning a queue lease, parked branch, provider read, replayed journal row, or ambiguous external result into a generic repository capability or a duplicate remote effect. |

| `FR-SEC-051` | MUST | Only a trusted process-local `PublicationOutcomeReconciliationWorker` may compose bounded private enumeration and expiry from `PRDevelopmentPublicationOutcomeReconciler`, exact case/thread reads, and the distinct `PublicationRemoteHeadObserver`. Its configuration, cursor/page/filter, retry state, observations, publications, and all nested values are structurally `json:"-"`. The store returns only fully integrity-checked authority-redacted unclaimed `outcome_unknown` rows in stable bounded keyset order; the worker expires abandoned starts before observing unknowns and validates the exact immutable case and non-legacy provider thread before the head read. | The worker can transition only one exact request-hash-bound unknown row through the existing reconciliation seam. It derives the result solely from the durable write-ahead request and requests publication only when the minimal independently timed provider read repeats the pinned repository/pull/head identity and the current head equals the immutable desired tip. Racing observations and response loss may be accepted only by rereading a semantically identical already-published reconciled result. A distinct expected pre-push tip, foreign or missing tip, changed identity, unavailable provider, stale observation, malformed row, or cross-bound evidence remains unknown under bounded process-local read backoff; when expected and desired are identical, that one tip is exact desired-tip proof. Restart or bounded eviction may cause only another read. | Neither the worker nor its store interface exposes publication claiming/requeue, push-journal start or finalization, generic/full provider verification, provider write, Git/filesystem/checkout/push, model, workflow/gate, session, repair/controller mutation, acknowledgement, thread/comment resolution, merge, configuration, transport, gateway lifecycle, HTTP, or UI authority. It never reconstructs or repeats the original Git effect. Cancellation and corruption fail closed, and production scheduling is a separate later composition. | Crash reconciliation must be able to prove the one desired remote state while making every inconclusive state inert and preventing durable private evidence or a retry helper from becoming remote-mutation authority. |

| `FR-SEC-052` | MUST | Only the trusted gateway event-automation service may own the production publication claim source. Its worker and runtime configuration capabilities are structurally `json:"-"`, and the static composition is all-or-none across enabled workflows, the schema-v18 store, workflow executor, exact selected run store, resolved attention policies, persisted CI evidence, parked-line reader, owner-agent attention-runtime acquisition, distinct full and head-only provider observers, narrow pusher factory, and outer generation acquisition; a missing or typed-nil dependency creates neither runtime nor loop. Each publication or reconciliation iteration first acquires that outer service generation. Under that lease, one serialized initializer resolves the parked workspace and separately wrapped `PushPinnedLine` adapter before any durable read, claim, expiry, or reconciliation, then constructs both workers and every pending, waiting, ready, policy, run, provider, journal, and reconciliation seam together. The exact supplied run store is used by both active execution and waiting observation without fallback; the full observer reaches only gate/push preflight, the distinct head observer reaches only reconciliation, and the pusher exposes no generic manager or tool runner. `PublicationWorker` receives only a one-method claim capability, requests at most one due pre-effect row with a bounded handoff lease strictly shorter than every composed handler renewal lease, and transfers the exact claim to a complete dispatcher without renewal, requeue, finalization, error reinterpretation, or lifecycle widening. Publication and reconciliation run as separate generation-leased polling loops. Cancellation and reload join both before event store, CI evidence, provider, agent, or Git-generation teardown; initialized adapters are cached only for that service generation, while immutable policy/subject/provider/run pins remain authoritative across replacement. Disabled or incomplete static composition, dynamically unavailable workspace or Git push resolution, cancellation before initialization, generation mismatch, an equal-or-longer handoff lease, or an empty claim performs no durable publication operation; an impossible over-limit claim result performs no phase dispatch or further durable operation. No partial graph or fallback runtime is installed. Human gate wait releases the iteration generation and remains reservation-free; ambiguous started work remains inert for the separately bounded head-only reconciler. Neither the runtime, worker, loop, run result, provider observation, Git receipt, journal row, retry value, nor browser-visible readiness grants provider review-write, review acknowledgement, thread/comment resolution, or pull-request merge authority, and this composition adds no schema, configuration, route, DTO, or UI surface. | A production queue owner must drain every durable publication origin and restart-safe reconciliation path without allowing partial wiring, reload, scheduler ownership, a broad Git/provider adapter, or a convenient success signal to become leaked capability, duplicate mutation, acknowledgement, or merge approval. |

For `FR-SEC-025`, `FR-SEC-038`, and `FR-SEC-048`, the strict list DTO alone
may add the required boolean `attention_required`. It is true only when either
the authoritative current `attention_required` review occurrence is `pending`,
`claimed`, or `delivered`, or the bounded current publication relational tail is
`gate_waiting` or `claimed` from that wait. Every absent, superseded, no-op,
recovery, failed, incomplete relational tail, stale-high-water, or otherwise
non-waiting source contributes false. Same-shape corrupt private pins may cause
only a coarse over-notification until case detail rejects them. The marker
conveys no source, private status, or identity, and polling or selecting it grants no
notification, workflow, code, Git, publication, or provider authority.

For `FR-SEC-032` and `FR-SEC-034`, exact Bind replay first proves a
non-regressing clock and live mutation deadline; it never returns raw authority
past expiry. Bound rotation is authorized only after eventing has durably
recorded the post-Resume mutation epoch. Unbound rotation is authorized only
while Git still has no adopted line. A crash between Git Adopt or Resume and
its eventing Bind therefore fails closed as cross-store ambiguity rather than
being guessed from either side when no operation was prepared. `FR-SEC-035`
prevents new ambiguity by write-ahead fencing Adopt, Resume, Commit, and Park.
An active schema-v13 operation takes precedence over generic v12 expiry and
owns the only live recovery claim; any linked v12 evidence is inserted only
after proof as non-claimable finalized audit history. This does not retrofit
authority onto a pre-v13 ambiguous effect or a legacy `recovery_required` row.

The structural privacy in `FR-SEC-031` is a capability-boundary guarantee for
generic tools, workflows, HTTP, frontend, and model contexts. It is not
confidentiality against a same-UID operator or unrestricted shell that can walk
the manager root returned to trusted local administration; deployments that
grant such shell access must isolate the controller root or OS identity.

The statement in `FR-SEC-031` that the raw bearer exists only in the live
workspace lock describes Git-workspace line storage before the later durable
controller seam. `FR-SEC-032` is its explicit controller-private exception:
schema v10 inherits the exact owner-session bearer for first adoption, may
retain it or a later fresh controller bearer only for a live or
recovery-required mutation, must remove usable bearer state before
`review_pending`, and redacts it from Reader and every generic or untrusted
surface.

`FR-SEC-041` is the schema-v17 controller-private exception during automatic
recovery. After exact non-Park reconciliation, the controller itself holds no
mutation bearer: the one fresh raw value exists only in the
`suspension_pending` handoff for its live recovery claim and is erased when
`suspended`. A later raw resume value exists only in its exact prepared row
until it becomes the controller's sole live mutation reservation. The worker
therefore does not use the older v12/v13 finalizer that immediately reissues a
model-capable mutation lease. Park still uses the old-bearer terminal path.
Neither schema phase may satisfy `FR-SEC-039` local readiness, and automatic
processing never treats an unbound legacy v12 row as an idempotent release.

For `FR-SEC-030`, full-identity retry applies to provider-verified threads. A
migrated pre-v9 retry instead proves the same isolated case, ordinal-zero link,
capture, and provenance without claiming or inventing provider IDs; neither path
appends another membership.

For the migration portion of `FR-SEC-030`, “no payload read” means no retained
raw event-envelope payload is read or parsed. The migration integrity-loads the
normalized pre-v9 development-case row, including its capture hash and
timestamp, solely to bind one isolated legacy membership and fail closed on a
corrupt case.

For `FR-SEC-025`, the dedicated safe projection additionally omits private
thread identity, case ordinal, provider origin, repository/pull/author database
IDs, exact thread count, and legacy classification. Cases sharing one thread
remain separate list/detail selections, and browser state remains keyed only by
the public case ID.

For `FR-SEC-027`, `VerifyCase` first validates the selected case's thread
membership. For a provider-verified thread, it makes the stored canonical
origin, the directly matched author ID, and the repository/pull cross-binding
part of the pre-checkout provider fence. An isolated legacy thread retains the
pre-v9 case-scoped verification path, but gains no sibling or thread-wide
authority. Any malformed membership or applicable provider mismatch fails
before pin or model access. The refresh does not read a sibling case or mutate
thread membership.

For `FR-SEC-028`, the exact GitHub read tool controls generation wiring, while
public availability separately requires a side-effect-free proof that the
selected case has valid verified-or-isolated-legacy membership and its selected
agent has a usable local workspace plus at least one concrete
model/provider binding. Selection uses the configured default only before a
session exists; afterward projection, admission, and execution keep using the
stored immutable session agent. Changing the default never retargets that
session, and removing its agent leaves history visible but disables new
admission instead of falling back. The queue still reconciles while admission
is disabled. Thread membership never shares a transcript, repair session,
workspace reservation, attempt, or browser fence with another case. Safe
preparation reclaim rotates only
private lease authority; parent-generation or intentional heartbeat cancellation
cannot manufacture a public lease failure. The transition to `running`
atomically refreshes the execution lease immediately before runner invocation;
after `running`, only a pinned recovery outcome is safe. Revision headroom is
reserved before admission so an active attempt can always reach a terminal
public state. After acquiring its SQLite immediate transaction, each claim
uses one scan clock for a fixed batch of at most 32 ordered candidates, then
samples a fresh clock immediately before the ownership write and grants the
complete requested lease from that instant. It durably suppresses only
semantically invalid stored aggregates through private state, preserves
operational store failures, and advances to later work on the next poll after
a full corrupt batch, so corruption cannot head-of-line block another case or
create an unbounded write-lock scan. Expired running attempts are likewise
terminalized in batches of at most 32. Renewal takes the same immediate lock,
samples time after acquisition, and never shortens a newer execution deadline.
The 4-KiB durable/public instruction and summary caps keep a maximally populated
and worst-case escaped detail or error wrapper within the 32-MiB launcher
response ceiling. The browser treats capability freshness separately from
durable content versions, prevents its own chat and repair mutations from
overlapping, and requires recovery continuation to acknowledge possible
preserved edits.

For `FR-SEC-021`, the review-attention agent companion is a separate
identity-only read boundary. `GET /api/reviews/attention-agents` requires the
opaque complete-config revision from the policy response as one strong quoted
`If-Match`; an optional canonical decimal cursor is valid only when paired with
that same generation. Each page exposes at most 256 normalized `id`/`name`
pairs plus default/revision pagination metadata. Workspace, account, model,
skills, subagents, runtime effects, security bytes, local paths, and raw errors
are unrepresentable. Current-schema loading is inert; stale, legacy, orphaned
security, invalid agent, malformed header/query, and noncanonical route states
fail closed without migration, backup, public write, or security-sidecar write.

For `FR-SEC-023`, an automatic outgoing-review occurrence stores at most one
3-MiB canonical effective-policy pin inside the owner-local event database.
Only a generation-fenced worker may create that pin from the trusted policy
source, and it must do so under the current opaque lease before any private
effect. Every subsequent worker and the browser bridge treat the bytes as
untrusted persisted state:
strictly decode one versioned envelope, revalidate the detached resolution,
recompute its decision digest, and require its independent stored revision to
match before session projection, workflow admission, or browser projection.
Pin presence must match `policy_revision`; pending/claimed may legitimately be
pre-pin, no-op requires an all-zero effective policy and forbids a run, and
delivered requires an active effective policy plus a canonical private run ID.
Both terminal states require completion. Trigger rows, policy bytes,
lease identity/deadline, retry detail, and run linkage have no HTTP, browser,
event, log, or generic workflow projection. Errors are bounded and sanitized;
the automatic path invokes no repository or provider write, and only the
compiler-generated human task deliberately exposes bounded configured
questions.

For `FR-SEC-023`, infrastructure failure while rechecking a private durable-create
fence is likewise reduced to a fixed admission-unavailable sentinel; the HTTP
owner maps it to the existing dependency-check-unavailable response without
exposing filesystem or configuration diagnostics.

For `FR-SEC-024`, `sha256:` identifies only the opaque response-fence format;
the digest input and all of its private components remain server-side. A fence
is emitted only for the sole actionable current task and is replaced by a fresh
authoritative projection after response. It is never a bearer for generic run
inspection, task lookup, or review-case mutation.

## Data And State Model

Security state includes secure-string sentinels, credential records keyed by
provider and auth method with optional non-secret account email and OAuth
refresh metadata, process and supported-host cross-process auth-store locks, dashboard
password/session data, login attempt counters, configured secret filters,
private-host allowlists, isolation exposed paths, generated token IDs,
revocation metadata, per-connector event webhook formats/signing secrets, and
explicit normalized webhook body/header trust metadata. Non-secret model
selection state consists of concrete provider accounts, exact model aliases,
per-concrete-account alias overrides, independent account/model routers, and an
opaque public-plus-security revision used only as compare-and-save authority.
Workflow agent requests
also carry an explicit inherited-or-none tool policy; declared MCP actions use
their ordinary independently configured credentials rather than ingress signing
secrets. A compiler-private launch carries an unexported workflow hash that is
verified before admission. Its resulting run carries an explicit private-
visibility marker and integrity-bound root in the owner-local run file. That
root may contain normalized gate values and a detached read-only session
snapshot whose structured locators are frozen references, the corresponding
strict versioned `FrozenSet`, and an explicit representation of runtime-only
prompt provenance and tool-call internal fields; it is not a remotely managed
secret store. Default `Run` JSON is redacted, while only the local store bypass
encodes exact raw continuation state. Its raw key and scope remain usable only
for local evidence validation, while provider affinity derives from a
domain-separated pseudonym and public observation DTOs contain neither the root
nor frozen/materialized media or derived execution output. The gateway PID
bearer remains process-local management authority;
launcher sessions and owner-readable local CLI access can use it only through
the bounded event proxy/client, and event DTO types make lease and
deduplication credentials unrepresentable. Structured job/action editor
revisions are opaque hashes of caller-supplied draft bytes, not durable
authority. Editor inspections, operations, capability choices, and
effect-review acknowledgements are transient request/browser state and add no
configuration, credential, workflow, session, or run record. A detached media
snapshot and its versioned `FrozenSet` contain bounded user-supplied bytes and
sanitized metadata, not a source path or live store authority. They may be
sensitive durable context when an owning feature serializes them, but this
security contract does not itself choose a persistence location, encryption,
retention, or access-control policy. Their content-derived identities and
digests establish internal consistency only; they do not authenticate a whole
record controlled by a local storage attacker.
The attention response fence, response ID, browser draft, and focused route add
no security store. The first two are derived from existing owner-local state,
the draft is memory-only, and route state carries only public case selection and
the fixed focus affordance.
The own-PR development projection and advisory conversation add no security or
browser-persistent state. The opaque cursor is bound to validated filters and
retained only for the active read; safe DTOs are constructed from explicit
capture and transcript fields, never raw record serialization. Conversation
rows are local event-store state but contain no agent/session identity or
authority that a later action could reuse.
The controller-only repair runtime adds no security, eventing, session, or
browser store. Its only permitted durable effects are exact pinned-checkout
acquisition, including manager-owned repository/workspace inventory, lock,
history, and heartbeat state, plus ordinary content changes inside that
checkout. Failed or partial content changes remain under that exact pin; this
primitive never releases or resets them.
The retained development line is likewise Git-workspace-manager state, not a
security, eventing, workflow, session, model, or browser record. Its inventory
owner, source pin, internal branch, exact tip/tree, version, mutation epoch,
state, domain-separated reservation hash, and last-park replay evidence remain
private. The live reservation bearer exists only in the workspace lock while
mutating. Retained workspaces, repository-only rollups, line history, and every
related count and byte are structurally absent from generic stats and quota;
their storage accounting belongs to a later controller-owned lifecycle. A
parked review snapshot is transient bounded untrusted data tied to one exact
base/tip/tree; it carries no path, ref, reservation, or manager lifecycle
capability and creates no durable security state.

`PushPinnedLine` likewise adds no security, eventing, workflow, credential, or
Git-workspace inventory state. Its request and sanitized result exist only for
the caller-bounded operation; no credential, raw remote output, push intent,
published marker, acknowledgement, or retry authority is persisted. The only
possible durable effect is the exact remote source-ref compare-and-swap.

Schema-v18 `pr_development_publications` is the separate eventing-private
durability state. Its `pdpub_` identity and exact case/thread/controller/session,
attempt/orchestration/CI, adjacent ledger, review-fence, source, and parked-line
evidence are non-authorizing immutable facts. Progressive canonical policy,
subject, provider observation, decision-run, push-request, and exact-result
bytes are stored with domain-separated hashes and bounded source revisions; the
provider observation contains only bounded repository/pull/head/review facts,
never a credential or raw response. Its facts and first `ProviderPinnedAt`
instant are immutable, while only the latest `ProviderObservedAt` instant may
advance when push start verifies identical facts. Scheduling
owner/token/deadline/epoch, `claim_from`, availability, attempts, safe error
code/detail, and timestamps are private lease state rather than branch ownership.
Every field of each publication DTO or record struct is `json:"-"`. A trusted
private Reader exists, but there is no public or generic Reader/browser/workflow/
model/tool/log/provider/Git-workspace projection.

Only `push_started` represents possible external-effect admission. A partial
unique controller constraint plus the controller mutation check serializes its
creation against local mutation; every other publication phase leaves the
parked line reservation-free. The canonical request is persisted before any
future external call, and terminal proof preserves only sanitized exact result
facts and an independent local-drift bit. `outcome_unknown` retains audit and
read-only reconciliation identity but no reclaimable push authority. Its
reconciliation observation is a separate minimal repository/pull/head-
repository/head-ref/current-tip/observed-at value, not the original
provider/review observation. Reconciliation preserves the original unknown-
completion instant, so edited or dismissed review state cannot prevent
head-only resolution without erasing when uncertainty was declared. This table
stores no raw checkout path, reservation bearer, credential, transport command,
Git/provider output, prompt, transcript, private workflow root, or model answer,
and creates no security, configuration, provider, Git, or browser record.

Renewal authority is phase-specific: Queue renewal extends only a live
pre-effect `claimed` lease, while PushJournal renewal extends only a live
`push_started` lease. The separate DecisionRunStore requires deterministic RunID
creation to be idempotent and replayable. A non-nil create-callback error
guarantees no external run was created; a nil return followed by SQLite commit
uncertainty returns `ErrPRDevelopmentPublicationAdmissionUncertain` and forbids
automatic create retry.

Safe pre-effect requeue adds no schema-v18 column or authority record. It
restores only the exact `claim_from` origin, clears the scheduling owner, token,
deadline, and origin marker, and advances `available_at` plus `updated_at`.
Every pin, decision, local-evidence, counter, and recorded parked-line value
survives without reading current branch or reservation state. The retained
epoch, restored origin, and availability are its exact replay fence. That
already-restored replay resolves before the live clock/lease checks and may
remain valid after becoming due; no `push_started`, terminal, or other unclaimed
row is eligible.

Active mixed-gate execution also adds no table or public state. The existing
immutable `PinnedSubject` bytes contain one versioned canonical private replay
envelope with exact identity/evidence/conversation anchors and a distinct
bounded nested model subject; they contain no transcript text, session key,
runtime lease, claim bearer, reservation, path, or credential. A working gate's
exact transcript prefix is stored only through the existing protected
agent-session and private workflow-root freeze, while the normal private run
store retains the already defined workflow/run/task state. The phase dispatcher
persists nothing and owns no queue cursor or lifecycle state.

Suspended retained-line state remains in that same private Git inventory and
is deliberately distinct from parked review state. Inventory version 4 retains
an append-only per-line suspension history whose exact count and
domain-separated empty-or-tail digest are anchored on the line. Records contain
only hashed request/reservation identity and exact source, line, retained
parent, current `CandidateTree`, prepared-commit identity and distinct
`PreparedTree`, applied outcome, and replay evidence; the raw bearer
is erased when the workspace and line owner are cleared. The checkout and
private ref remain controller-owned. `PreparedTree` is the exact deterministic
child tree, whereas `CandidateTree` is the independently captured current
ordinary-content tree over the retained parent. It must equal the prepared tree
while the child is unapplied, but may differ after edits to an applied child. An applied prepared
child may remain detached `HEAD` while the ref and line tip stay at its parent. The line is
reservation-free but has no Park/review fence. A fresh exact resume may
normalize that known child and only the real index to the retained parent while
preserving ordinary files, then restore ownership at the already-issued
mutation epoch. Suspension adds no security, eventing, workflow, session,
model, browser, generic inventory, or provider record and is not a readiness or
publication fact.

Schema-v10 eventing controller storage is the later explicit trusted owner of
the retained-line lifecycle; it does not make Git-workspace inventory public.
Its private controller row may durably duplicate the raw reservation bearer
only while an exact mutation lease is live or preserved for explicit recovery.
The attempt-review-fence row contains exact parked line/review-digest evidence,
immutable authenticated retired-mutation lease/revision proof in its initial
rolling hash, and append-once authenticated review-completion proof folded into
the final tail hash before another fence can chain from it. Its
non-authorizing retired-reservation digest is unique store-wide, as is every
simultaneously active raw reservation. Recording that fence clears usable reservation and
mutation-lease state before a distinct reservation-free review lease can be
created, while leaving the private Git branch retained. The digest prevents
reissue on any controller without preserving historical bearer authority. An expired review
lease can safely rotate ownership of the same immutable fence; an expired
mutation lease retains recovery evidence and cannot be reclaimed. These tables
and their Go types have no JSON, HTTP, browser, workflow, tool, model, log,
generic workspace, or stats projection and execute no worker, Git, AI, CI,
commit, or provider effect. The private Reader additionally redacts live lease
and reservation bearers. First controller creation does atomically suppress
legacy claims for its owner session; a future controller-aware worker must
complete later queued attempts before they can publish another fence.

Schema-v12 recovery-intent storage and inventory-v3 rotation history extend
that private boundary without declassifying it. Before rotation, the eventing
row temporarily stages both raw bearers under an independent recovery claim;
after exact finalization it retains only digests and authenticated proofs. The
Git inventory stores only domain-separated reservation hashes, exact private
fences, and the append-only causal rotation chain. Each pinned workspace also
anchors the exact chain count and empty-or-tail digest outside the optional
history map, making whole-chain or suffix deletion invalid before any bearer
lookup. Neither copy appears in generic workspace projections, and the old
bearer is rejected permanently.

Schema-v13 `pr_development_controller_operation_intents` remains inside that
same controller-private boundary. Its canonical request/result bytes, hashes,
operation and recovery identities, claim owner/token/deadline/epoch, expired
lease proof, controller/attempt/source/line snapshots, reservation digests, and
temporarily staged replacement bearer are non-JSON capability state. The row
owns recovery instead of opening a pending schema-v12 intent. Finalization
erases raw staged replacement authority and retains only non-authorizing proof;
the mandatory linked v12 Adopt/Resume/Commit record is already finalized. Park
has no replacement and uses one SQLite immediate transaction for operation,
attempt/session, fence, reservation-retirement, and `review_pending` state, so
no reader can observe only part of that handoff. The private Git branch remains
retained, but review receives only its later exact-object bounded projection and
never the retired edit bearer.

Schema-v17 eventing suspension handoffs stay inside the same owner-local
capability boundary. `suspension_pending` contains no controller mutation lease
or bearer; only its exact live recovery claim may retrieve the one fresh raw
bearer from the checkpoint row long enough to invoke suspension. The checkpoint
hash-binds its v12/v13 source, kind, exact recovery/rotation result, line fence,
suspension intent, and, for Commit, prepared request plus deterministic commit
result. Every raw old copy and duplicate fresh copy is erased when that row is
committed. Final suspension erases the remaining fresh bearer and recovery
claim and changes the controller to `suspended`, whose eventing row and Git
inventory record retain only non-authorizing exact evidence. A separate
suspended-resume row may temporarily hold one globally fresh bearer only after
binding it to the latest suspension and a live queued-attempt orchestration
claim; successful exact finalization moves that bearer into the sole controller
mutation owner and erases the staged copy. These rows and both controller phases
are absent from all public/model/generic surfaces and are always locally not
ready. Unbound legacy v12 rows are neither migrated nor touched by the automatic
worker because no approved idempotent retirement evidence exists for them.

Local-CI state is separate owner-local evidence rather than security,
controller, eventing, workflow, session, model, or browser state. Parent and
candidate materialization roots are transient and private; `PinnedTreeManifest`
canonically binds every entry's path, mode, type, size, and SHA-256 content
identity to the exact candidate. The versioned plan separately binds bounded
definition and dependency inputs, ordered required steps, exact invocation,
working directories, environment requirements, and limits. Explicit local-plan
authority suppresses inference; without it, native quick profiles precede the
supported GitHub fallback, and multi-executable GitHub jobs are rejected because
their shared state cannot survive fresh per-step sandboxes. Definition changes
produce incomplete `plan_changed`; dependency-only changes produce a different
environment/result identity. The resolved plan graph persists only under its
exact parent/candidate manifests and discovery/plan versions. Execution evidence
binds that manifest and Git candidate to exact plan, environment, trusted
toolchain/dependency, Bubblewrap plus systemd/cgroup-v2 sandbox, platform,
per-step status, and output digests. It persists owner-locally and is neither
mounted writable nor addressable from the sandbox. Passing-result reuse is
disabled in production until host toolchains and controller-provided read-only
dependency mounts have complete immutable manifests. Disposable roots, scratch
space, and processes are removed after cgroup quiescence is proven; inability
to prove it is non-green and quarantines only the owner-local scratch for
operator cleanup. None of these records contains a secret, raw ambient
environment, retained-checkout path, reservation, controller lease, provider
credential, or publication capability.

## Surface Ownership

Owns: CODE pkg/auth/**
Owns: CODE pkg/config/**
Owns: CODE pkg/credential/**
Owns: CODE pkg/fileutil/**
Owns: CODE pkg/isolation/**
Owns: CODE pkg/logger/**
Owns: CODE pkg/netbind/**
Owns: CODE pkg/pid/**
Owns: CODE pkg/utils/**
Owns: CONFIG.isolation*
Owns: TEST pkg/auth/*
Owns: TEST pkg/credential/*
Owns: TEST pkg/isolation/*
Owns: TEST pkg/netbind/*
Owns: TEST pkg/pid/*
Owns: TEST pkg/logger/*
Owns: TEST pkg/fileutil/*
Owns: TEST pkg/utils/*
Owns: TEST pkg/config/security*
Owns: TEST pkg/config/migration*
Owns: TEST pkg/config/config*
Owns: TEST pkg/config/gateway*
Owns: TEST pkg/config/model*
Owns: TEST pkg/config/mcp*
Owns: TEST pkg/config/multikey*
Owns: TEST pkg/config/register*
Owns: TEST pkg/config/version*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Config | Secure strings, `isolation.*`, filtering fields, `model_list`, `model_aliases`, and model-selection validation | Secret preservation, isolation controls, sensitive-data filtering, strict account-plus-alias selection, safe v1-v3 migration, and early rejection of unsupported or provider-incompatible values. | `FR-SEC-001`, `FR-SEC-003`, `FR-SEC-006`, `FR-SEC-008` |
| Go API | `ConfigRevision`, `LoadCurrentConfigSnapshot`, `LoadCurrentConfigForUpdateSnapshot`, `LoadConfigForUpdateSnapshot`, `SaveConfigIfRevision`, `SaveReviewAttentionIfRevision` | Capture the exact public-plus-security generation, load current schema without migration, compare-and-save a complete update, or atomically patch only persisted review-attention policy without exposing or rewriting security bytes or overwriting a concurrent winner. | `FR-SEC-021` |
| Config | `events.ingress.webhooks.*.{format,secret}` | JSON-owned `standard`/`github` format plus masked JSON and secure-YAML merge/preservation for the corresponding per-connector secret, without security-only connector resurrection. | `FR-SEC-010`, `FR-SEC-012` |
| HTTP | `GET /api/config`, `PUT /api/config`, `PATCH /api/config` | Management reads expose `[NOT_HERE]`; omitted or masked webhook secrets preserve the current value, and a concrete replacement rotates it through the same secure persistence path. | `FR-SEC-010` |
| HTTP | `POST /webhooks/events/{connector}` with `format: github` | Exact-body HMAC-SHA256 authentication, bounded parsing, explicit unauthenticated-header metadata, and durable delivery-ID deduplication behind trusted TLS. | `FR-SEC-012` |
| Workflow / MCP / storage | `github-pr-development`, `prdevelopment.GitHubVerifier`, `pr_development_cases` | Treat the signed review projection and provider-returned body as untrusted data; use only exact generation-fenced read calls and bounded strict JSON from an inline result or confined artifact to match the provider-returned author ID, cross-bind the authenticated numeric repository/pull IDs through exact current origin/repository/URL/number facts, bind review-level identity and current PR/fork/head facts, retain the webhook node ID as trigger evidence only, and grant no model, inline-comment association, checkout, repository mutation, or provider-write authority. | `FR-SEC-012`, `FR-SEC-030` |
| Private storage / controller | Schema-v9 `pdt_` development threads and immutable case memberships | Key verified grouping only by canonical provider origin plus authenticated and provider-cross-bound repository and pull database IDs, preserve the exactly matched author identity as an invariant, validate complete contiguous membership, isolate every legacy case without inferred identity, preserve its existing case-scoped repair compatibility, and keep all stable IDs and grouping state out of public/model surfaces while current behavior remains case-scoped. | `FR-SEC-030` |
| HTTP / CLI | protected `/runtime/eventing/*`, launcher `/api/events*`, `picoclaw events *` | Translate authenticated launcher or owner-local PID authority into bounded live-gateway operator calls without exposing PID credentials, lease tokens, deduplication keys, or automatically fetched payloads. | `FR-SEC-014` |
| HTTP / UI | `/api/workflows/definitions/inspect`, `/api/workflows/templates/{name}/inspect`, `/agent/workflows` | Return and render one non-cacheable, fixed-code, bounded structural projection without exposing definition source, sensitive values, source paths, event payloads, or raw internal errors. | `FR-SEC-016` |
| HTTP / UI | protected `/runtime/workflows/authoring/capabilities`, launcher `/api/workflows/authoring/capabilities`, `/agent/workflows` | Translate the authenticated dashboard session into one bounded live-generation catalog containing only exact targets, fixed readiness, and typed parameter shapes; the browser can search and copy a ready target but cannot invoke it from this surface. | `FR-SEC-017` |
| HTTP / UI | `POST /api/workflows/development/jobs/inspect`, `POST /api/workflows/development/jobs/render`, `/agent/workflows` Jobs & actions/effect review | Transform only exact bounded caller-supplied YAML through a strictly decoded ordered AST projection or one revision-fenced operation; retain unsafe shapes as raw-only, keep all state in the browser/request, and require exact-identity conservative acknowledgement before the separate draft-test endpoint. | `FR-SEC-018` |
| HTTP / UI | `POST /api/workflows/development/triggers/simulate`, `POST /api/workflows/development/test/execute`, `/agent/workflows` trigger simulator/review | Strictly bound, payload-safe simulation uses a read-only current-config/PID snapshot to produce the only server review token accepted for one exact active draft and scenario; confirmed execution uses an unpruned lazy runtime and rechecks token expiry, identity, config, match, protected-event, and effect state before durable mutation or runtime authority. | `FR-SEC-019` |
| Workflow / HTTP / SSE / tool | Compiler-private `RunRequest.PrivateRoot`, file-run persistence, run/result/event projections, and the `workflow` tool | Admit and preserve exact owner-local gate evidence, including its rewritten snapshot and strict self-contained `FrozenSet`, while making private invocation context and derived diagnostics unrepresentable on generic observation surfaces; only a bounded generated human task is an explicit declassification. | `FR-SEC-023` |
| Storage / runtime | `pr_review_attention_triggers`, `eventing.ReviewAttentionTriggerQueue`, `reviews.AttentionTriggerWorker` | Keep one bounded canonical effective-policy pin and fresh lease authority owner-local; create the pin only from a trusted generation before effects, strictly revalidate it on every retry, expose no trigger state publicly, and invoke only the private gate launcher without repository or provider write authority. | `FR-SEC-023` |
| HTTP / UI / workflow | Protected and launcher case-owned review-attention GET/response routes, `/reviews?case={case}&focus=chat`, and generic workflow observation/mutation routes | Peek and numeric-local-validate gateway authority without process/PID side effects; validate the trigger-status-specific case authority and task payload hash; project only bounded deliberate declassification plus at most one opaque actionable fence; resume only through server-resolved identity with exact recovery; retain response state in memory; suppress exact reserved runs; scrub hidden relationships from ordinary reads/graphs; fence the transitive relationship component from mutation; and preserve reserved replay authority from ordinary retention. | `FR-SEC-024` |
| HTTP / UI | Protected `/runtime/eventing/pr-development` list/detail routes, launcher `/api/pr-development` mirrors, and `/reviews?view=development` | Replace browser authority, strictly validate the exact GET/filter/case surface, construct only the safe captured-snapshot DTO, retain response and cursor state in memory, render untrusted feedback as plain text, isolate external links, expose replay as a separate case, and provide no refresh or action capability. | `FR-SEC-025` |
| Storage / Go API / HTTP / UI | `eventing.PRDevelopmentWorkbench.LocalEvidence`, `prdevelopment.Detail.local_development`, the existing selected-case detail GETs, and the PR-development local-status card | Keep the exact controller/orchestration/ledger source structurally private while declassifying only the bounded latest-attempt lifecycle and exact completed-orchestration-bound candidate/CI/review facts. Strictly parse and cross-bind every field, reject legacy default-green or inconsistent evidence, adopt only newer memory state, render only bounded summaries/counts and shortened opaque equality fingerprints with a local-only notice, and perform no write or external effect. | `FR-SEC-039` |
| HTTP / UI / model / storage | Exact protected `POST /runtime/eventing/pr-development/{pdc_...}/chat`, launcher `POST /api/pr-development/{pdc_...}/chat`, and the selected case conversation | Require same-origin evidence only at the launcher, strip browser authority there, and reject every provenance header at the protected boundary. Repeat canonical path, no-query/ForceQuery, known-length identity JSON, exact two-key/case-sensitive, Unicode, depth, version, and text validation before authority use; load and integrity-check the two-table transcript before stale/capacity decisions; reserve a complete two-row/worst-case-byte turn; serialize the case process-wide while bounding AI per service; invoke one exact replacement-prompt isolated advisory model; and expose partial detail only after a fresh bounded reload. Keep UI state in memory, normalize like Go, bind and adopt detail strictly and monotonically, recover matching version-plus-one/version-plus-two outcomes, announce live/refetch state, preserve mobile focus, render plain text, and expose no repository/provider/gate action capability. The shared 135-second protected write timeout exceeds the 120-second launcher application budget. | `FR-SEC-026` |
| Internal Go API | `prdevelopment.GitHubVerifier.VerifyCase`, `agent.LocalRepairRunner`, guarded `tools.RunToolLoop`, and controller-only `gitworkspace.Manager.AcquirePinned` | Re-establish exact current provider/head authority, then confine one borrowed concrete model to four serialized bounded repository-content tools over one exact pin, denying Git control paths and unconditionally postflight-verifying ownership without receiving release, execution, Git, CI, or publication authority. | `FR-SEC-027` |
| HTTP / Go API / storage | Protected and launcher PR-repair routes, `eventing.PRDevelopmentRepairAdmitter`, `eventing.PRDevelopmentRepairQueue`, `prdevelopment.RepairWorker`, `agent.AgentLoop.NewControllerLocalRepairRunner` | Replace browser authority, persist only bounded intent and private controller state, declassify a narrow lifecycle, resolve one exact no-fallback edit capability under the active generation, and terminalize ambiguous post-invocation work without replay or pin release. | `FR-SEC-028` |
| Controller Go API | `gitworkspace.Manager.WithPinnedOperation`, `SnapshotPinnedCandidate`, `CommitPinned` | Cross-process serialize exact pinned filesystem effects through a callback-scoped derived context, compute bounded content-addressed candidate evidence, and create/reconcile one deterministic local commit without exposing path, process, remote, or publication authority. | `FR-SEC-029` |
| Controller Go API | `gitworkspace.Manager.AdoptPinnedLine`, `ResumePinnedLine`, `ParkPinnedLine`, `SnapshotPinnedLineReview` | Retain one exact private commit line, fence each mutation with a fresh reservation/version/epoch, park and release only after a compare-and-swapped exact commit/no-change proof, and read a bounded exact-object review while parked without exposing path, ref, reservation, Git lifecycle, or generic workspace authority. | `FR-SEC-031` |
| Private storage / controller Go API | `eventing.PRDevelopmentControllerReader`, `eventing.PRDevelopmentControllerStore`, schema-v10 controller and attempt-review-fence tables | Transfer one verified-thread pinned owner and its exact first workspace reservation from legacy claims to one private retained line; issue fresh reservations for later resumes; fence mutation and reservation-free review separately; redact lease and reservation bearers from Reader snapshots; require completed-attempt and exact Resume evidence; retire mutation authority before immutable review; hash-bind review completion; validate complete reachable chained evidence; safely reclaim review only; and convert an expired mutation encountered by an exact current operation to explicit recovery without exposing or executing local, model, CI, workflow, or provider capability. | `FR-SEC-032` |
| Private storage / controller Go API | `eventing.PRDevelopmentControllerStore`, schema-v12 recovery intents, `gitworkspace.Manager.RotatePinnedReservation`, inventory-v3 rotation records | Quarantine an expired bearer, lease only its exact idempotent old-to-fresh transfer, permanently revoke the old workspace/line owner, and install the fresh bearer under a new mutation lease while keeping all authority and evidence outside generic/public/model surfaces. | `FR-SEC-034` |
| Private storage / controller Go API | `eventing.PRDevelopmentControllerOperationStore`, schema-v13 `pr_development_controller_operation_intents`; controller-only `gitworkspace.Manager.RecoverPinnedLineAdoptReservation`, `RecoverPinnedLineResumeReservation`, `RotatePinnedReservation`, `CommitPinned`, and `ParkPinnedLine` | Write-ahead fence each exact effect; keep operation recovery separate from general mutation; hold old/fresh locks continuously for composite line recovery; rotate before Commit; replay-and-retire old for Park; and atomically remove edit authority before reservation-free review, while structurally withholding every capability and proof from untrusted or generic surfaces. | `FR-SEC-035` |
| Controller Go API / process | `pkg/prdevelopment/localci/**`; `gitworkspace.Manager.WithPinnedCandidateValidationRoots`, `PinnedCandidateValidationRequest`, `PinnedCandidateValidationRoots`, and `PinnedTreeManifest` | Revalidate and materialize exact `.git`-free parent/candidate evidence under the reservation lock; enforce explicit-plan, native-quick-profile, then GitHub-fallback discovery precedence and persist the exact-manifest plan graph; reject stateful multi-command fallback jobs; and run all required fresh steps only in the mandatory no-network Bubblewrap sandbox under user-systemd/cgroup-v2 supervision with explicit controller-provided offline dependencies. Persist full-identity evidence, while disabling production passing-result reuse for mutable host inputs, without exposing host or repository-lifecycle authority. | `FR-SEC-036` |
| Controller Go API / private Git inventory | `gitworkspace.Manager.SuspendPinnedLine`, `SuspendPinnedLineCommitRecovery`, `ResumeSuspendedPinnedLine`, and inventory-v4 suspension records/anchors | Under exact operation and inventory locking, snapshot bounded ordinary candidate evidence, distinguish the exact applied/unapplied prepared-Commit state, hash-chain and retire the current bearer into nonreviewable private suspension, and resume only the same candidate under a globally fresh bearer while preserving files and withholding every generic/model/browser/provider capability. | `FR-SEC-040` |
| Private storage / controller Go API / runtime | Schema-v17 suspension handoff and suspended-resume records, the tagged eventing recovery-work store, generation-owned PR-development recovery worker, and controller-only exact Git recovery/suspension methods | Separate scheduling, Git, mutation, model, and provider capabilities; persist exact non-Park recovery evidence before suspension; retain at most one claim-scoped fresh bearer during `suspension_pending`; erase all recovery authority at `suspended`; directly retire Park; and durably replay one fresh exact resume into the sole mutation owner before model access. Keep both phases and all proofs private/non-ready, and exclude unbound v12 recovery pending an approved idempotent retirement contract. | `FR-SEC-041` |
| Controller Go API | `gitworkspace.Manager.PushPinnedLine`, `PinnedLinePushRequest`, and `PinnedLinePushResult` | Under manager-plus-kernel inventory serialization and without a mutation reservation, revalidate one complete parked line and its stored target, exact-observe its derived remote branch, compare-and-swap only expected tip to parked tip, and return a sanitized transient remote/local postflight receipt. Accept no caller credential or transport command and confer no provider, readiness, publication, acknowledgement, merge, model, workflow, tool, HTTP, or UI authority. | `FR-SEC-042` |
| Private storage / Go API | Schema-v18 `pr_development_publications`; `eventing.PRDevelopmentPublicationReader` and `PRDevelopmentPublicationQueue` | Keep exact occurrence reads, renewable pre-effect `claimed` scheduling, immutable policy/subject/provider pins, reservation-free wait/readiness, and pre-start terminalization owner-local and structurally non-JSON. Queue authority cannot renew `push_started`, start/finalize/reconcile push, or invoke an external capability. | `FR-SEC-043` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationGateContextSnapshotReader`, `PRDevelopmentPublicationGateContextSnapshot`, and `Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot` | Authenticate only an exact live claimed-from-pending lease, return one detached claim-redacted and owner-session-authority-redacted all-private snapshot over existing canonical evidence, and fence the first subject pin with the current conversation version plus digest while preserving exact post-pin replay. The reader grants no queue, provider, workflow, model invocation, repair-session mutation, filesystem, Git, push, HTTP/UI, acknowledgement, or merge authority. | `FR-SEC-045` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationGateContextAnchor`, `PRDevelopmentPublicationPinnedGateContextSnapshotReader`, and `Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot` | Reconstruct only the exact subject-anchored canonical conversation prefix for a live pending-origin claim, while revalidating current publication high-water and returning the ordinary claim/session-authority-redacted private snapshot. Reject rollback or digest mismatch and exclude every later message without acquiring queue, model, provider, workflow, filesystem, Git, or mutation authority. | `FR-SEC-049` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationGateClaimAuthenticator` and `Store.AuthenticateClaimedPRDevelopmentPublicationGate` | Authenticate one exact live claimed-from-pending token/epoch and complete current local high-water, returning only the authority-redacted publication, its validated pin progression, and exact repository policy selector without conversation/rich-context projection, mutation, renewal, or effect authority. | `FR-SEC-046` |
| Internal Go API | `prdevelopment.PublicationGateProcessor` | Compose only separately injected least-authority exact-claim authentication, policy capture, claimed gate-context read, pre-effect pin/readiness transitions, and full provider observation for an existing claimed-from-pending occurrence. Active policy returns an authenticated policy-only execution handoff without rich context; compiler-confirmed zero policy alone may progressively pin its private conversation-fenced subject and provider observation and become push-ready without a run. Every value stays non-JSON, and the processor owns no claim/renewal, requeue, run/executor/model, repair/filesystem/Git/push, gateway/worker, provider-write, acknowledgement, or merge authority. | `FR-SEC-046` |
| Internal Go API | `prdevelopment.PublicationGateExecutor`, `PublicationPendingGateHandler`, `PublicationGateWaitingHandler`, their narrow phase-specific store/context/runtime/provider capabilities, and `attention.PrivateRunner` | Consume one existing pending or gate-waiting claim, keep the canonical replay envelope distinct from its bounded model subject, freeze only an authenticated conversation prefix for an owner-matched working gate, converge exact pins on one private run, and transition/requeue only from that run's exact durable state without retaining runtime, session, scheduling, or mutation authority through human wait. | `FR-SEC-049` |
| Internal Go API | `prdevelopment.PublicationDispatcher` and its three phase-handler interfaces | Route one structurally valid caller-owned claim to exactly its pending, gate-waiting, or push-ready handler without queue or lifecycle authority. Requiring all handlers prevents partial composition; the separately fenced ready handler completes the internal phase set without giving the dispatcher production queue authority. | `FR-SEC-049`, `FR-SEC-050` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationPushAuthentication`, `PRDevelopmentPublicationPushClaimAuthenticator`, and `Store.AuthenticateClaimedPRDevelopmentPublicationPush` | Authenticate one exact live claimed-from-ready token/epoch and complete current local high-water, returning only the authority-redacted authoritative publication, exact immutable case, and provider thread identity. It owns no renewal, requeue, provider read/write, journal start/finalize, Git, reservation, acknowledgement, thread-resolution, merge, HTTP, or UI authority. | `FR-SEC-050` |
| Internal Go API | `prdevelopment.PublicationPushReadyHandler`, `PublicationPushReadyStore`, and `PublicationPinnedLinePusher` | Compose one caller-owned ready claim through exact authentication, repeated full provider observation, derived write-ahead request, queue-to-push-journal renewal handoff, at-most-once pinned-line Git, and deterministic outcome classification. It finalizes while exact journal authority remains live; definitively stale started authority remains inert for store expiry. Historical start/finalize replay never repeats Git; the branch stays parked and reservation-free, and production claiming plus all provider-write/acknowledgement/thread-resolution/merge authority remain absent. | `FR-SEC-050` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationRequeue`; `PRDevelopmentPublicationQueue.RequeuePRDevelopmentPublication` and `Store.RequeuePRDevelopmentPublication` | Release only one exact live pre-effect scheduling claim back to its authenticated expected origin at an availability that is non-past at the live transition. Preserve every evidence/pin/decision/counter/recorded-parked-line value without reading current branch state, expose only authority-redacted output, and reject started, terminal, any other unclaimed, cross-origin, or changed input without obtaining high-water, provider, Git, model, workflow, or worker authority; the already-restored exact committed replay resolves before clock/lease validation and may succeed after becoming due. | `FR-SEC-047` |
| Pure internal Go API | `prdevelopment.PublicationRetryDelay` | Compute only the deterministic one-second-to-one-minute saturating delay from a claim count; it owns no clock, store, scheduler, error classification, or effect capability. | `FR-SEC-047` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationAttentionProjection`, publication fields on `PRDevelopmentAttentionTriggerCaseSnapshot`, and `Store.GetCurrentPRDevelopmentAttentionTriggerForCase` | Atomically validate and retain separate local-review and exact current-publication projections, reject dual-current source authority, and derive only source-private current/actionable flags plus the existing coarse public OR. The read can integrity-check high-water but cannot claim, transition, execute, or publish. | `FR-SEC-048` |
| Internal Go API / HTTP / UI | `prdevelopment.AttentionBridge`, publication decision-key helpers, unchanged case-owned attention routes/DTO, and own-PR gate presentation | Select only the atomic snapshot's one source, validate the exact private decision/run/task chain, use a publication-specific opaque response-fence domain, expose no source or private identity, and resume only the exact current waiting human task. Completed current publication decisions remain read-only; no response grants code, CI, Git, provider, push, acknowledgement, or merge authority. | `FR-SEC-048` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationPushJournal` | Own only exact live `push_started` renewal, transactionally revalidated write-ahead start, and request-hash-bound exact result finalization; it cannot renew or claim pre-effect work, obtain provider evidence, invoke Git, or reconcile unknown state. | `FR-SEC-043` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationOutcomeReconciler`, private unknown-outcome cursor/filter/page, `PRDevelopmentPublicationOutcomeReconciliation`, and distinct minimal `PRDevelopmentPublicationRemoteObservation` | Expire abandoned started work, enumerate restart-surviving non-reclaimable unknowns in bounded stable order, and accept only separately obtained fresh remote-head proof of desired tip; it cannot invoke push or depend on/overwrite the original provider review observation. | `FR-SEC-043`, `FR-SEC-051` |
| Internal Go API | `prdevelopment.PublicationOutcomeReconciliationWorker` | Compose only bounded expiry/enumeration, exact case/thread reads, the head-only observer, and exact desired-tip reconciliation with bounded process-local backoff and semantic concurrent convergence. It has no Git, model, workflow, full provider-read, provider-write, acknowledgement, merge, HTTP, UI, or production-loop capability. | `FR-SEC-051` |
| Internal Go API | `prdevelopment.PublicationWorker`, `PublicationWorkerConfig`, and its private one-method claim queue | Claim at most one due pre-effect publication with a stable bounded handoff lease strictly shorter than every composed handler renewal lease and pass the exact result to the complete dispatcher. It owns no renewal, requeue, finalization, phase transition, provider, workflow/run, Git, journal, reconciliation, acknowledgement, or merge capability, and it preserves the selected handler's error unchanged. | `FR-SEC-052` |
| Gateway runtime | `prDevelopmentPublicationRuntime`, `prDevelopmentPublicationRuntimeConfig`, `prDevelopmentPublicationPusher`, and the separate publication/reconciliation loops | Accept every static dependency as one non-JSON all-or-none graph; only after the outer generation is acquired, resolve parked-workspace and narrow push readiness, reuse the exact run store, and build both workers together with distinct full/head provider observers. Cache adapters only for that service generation and drain both loops before store, evidence, provider, agent, or Git-generation replacement. Incomplete or dynamically Git-unready composition is durably inert and grants no acknowledgement or merge authority. | `FR-SEC-052` |
| Private storage / Go API | `eventing.PRDevelopmentPublicationDecisionRunStore` | Bind one semantic decision key to one deterministic private run through create-once admission. RunID creation is idempotent/replayable and a non-nil callback error guarantees no external run was created; a nil return followed by uncertain commit returns a fixed sentinel that forbids automatic create retry and requires an explicit exact terminalization/recovery choice. | `FR-SEC-043` |
| Private provider-read Go API | `prdevelopment.PublicationProviderObserver`, `PublicationRemoteHeadObserver`, `NewGitHubPublicationProviderObserver`, and `NewGitHubPublicationRemoteHeadObserver` | Hide the generic workflow runner behind two separately constructed, unexported, non-widenable exact full and head-only `github/pull_request_read` projections, sample observation time only after validation, keep every result structurally non-JSON, and grant no provider-write, store, model, workflow, Git, push, retry, acknowledgement, merge, HTTP, or browser authority. | `FR-SEC-044` |
| Config / HTTP / UI | `reviews.attention`, `GET` and `PUT /api/reviews/attention-policies`, `/reviews?view=policies` | Keep gate authority in operator-owned configuration outside reviewed checkouts; expose only bounded non-secret policy plus opaque revisions/effect status, parse and serialize arbitrary question JSON losslessly, and retain the editable projection only in memory. Replace it only through one explicit strict same-origin public-plus-security compare-and-swap that raw-patches only persisted `reviews.attention`, preserves unrelated persisted values and numeric tokens, and leaves security state byte-identical; a conflict retains the local draft for explicit reload/discard and never retries or rebases automatically. Route state contains only the fixed policy-view selector, and editing, validation, effective preview, reload, and discard grant no workflow or provider authority. Broad config GET omits this subresource; broad PUT accepts only an empty compatibility placeholder, broad PATCH rejects the field, and both preserve its exact value during unrelated updates. | `FR-SEC-021` |
| HTTP | `GET /api/reviews/attention-agents` | Project only one fixed-256 page of canonical configured identity and default metadata, fenced by one strong policy-generation `If-Match` plus an optional canonical offset; broader agent configuration and all security state remain outside the DTO. | `FR-SEC-021` |
| Workflow / MCP | `agent/*` with `with.tools: none`; `mcp/github/add_issue_comment` | Remove tools from every classifier model path, then permit a GitHub mutation only as a declared conditional MCP step with signed-body identity and fixed output text. The GitHub MCP server and its write credential are configured explicitly and independently from ingress authentication. | `FR-SEC-013` |
| Storage | Credential store | Provider and MCP credential CRUD, transactional refresh updates, auth/OAuth metadata, cross-process serialization on supported hosts, and optional non-secret account email metadata extracted from OAuth token responses. | `FR-SEC-002`, `FR-SEC-007`, `FR-SEC-009` |
| Storage | `pkg/fileutil` durable path operations | Durable recursive parent creation, synced same-directory atomic replacement, and durable logical removal with POSIX directory sync or Windows write-through moves. | `FR-SEC-015` |
| Network | Safe HTTP clients, MCP OAuth transports, and net binding helpers | Private/special-use host controls, DNS-pinned MCP OAuth discovery/token/probe/refresh requests, same-origin redirects, explicit local-development policy, and bind behavior. | `FR-SEC-005` |
| Go API | `media.SnapshotReader.ReadSnapshot`, `media.FreezeInputs`, `media.FrozenSet.Materialize`, session frozen-media wrappers | Capture one live file capability and freeze/materialize one complete locator batch with fixed resource ceilings, strict schemes/encoding, no-follow regular-file handling, consistency validation, all-or-nothing output, and redacted errors. | `FR-SEC-022` |
| JSON | Versioned `media.FrozenSet` serialized beside a frozen session snapshot or embedded by its owning durable record | Strictly round-trip only canonical bounded records; reject unknown, duplicate, trailing, inconsistent, or over-budget state before any locator is materialized. | `FR-SEC-022` |

## Algorithms And Ordering

1. Normalize config and request inputs before comparing or persisting any secret
   values or optional model-list controls.
2. Preserve existing secure-string values when updates contain masked values;
   replace, clear, or reject secrets only through explicit update semantics.
3. Canonicalize provider-scoped credential aliases before lookup or persistence
   and run provider-specific token validators before saving token-backed
   credentials. Serialize each credential mutation with a process-local mutex
   and an OS file lock where the host supports it, reload the latest store under
   that lock, and keep the lock across a refresh read-modify-write transaction
   so a concurrent launcher replacement or unrelated credential update is not
   lost.
4. Parse OAuth token responses into credential records, copy non-secret email
   claims from JWT payloads when available, and retain an existing email when a
   refresh response omits it.
5. Authenticate dashboard requests before protected handlers and require POST
   semantics for logout so browser navigation cannot clear sessions.
6. Resolve HTTP targets to concrete host/IP data, deny private or internal
   destinations unless allow rules apply, then execute the request through the
   guarded client. For credential-bearing MCP OAuth discovery, exchange,
   probing, and refresh, validate each endpoint and keep each redirect on its
   request origin, disable environment proxies, pin an approved address into
   the transport dial, and allow a private result only when the configured
   intentional-local endpoint resolves to that same address.
7. Build isolation command specs from supported runtime configuration, validate
   exposed paths, start only supported commands, and return errors rather than
   weakening to unisolated execution.
8. Merge webhook secrets only into connector names already loaded from JSON,
   defer event-secret reference resolution until final master enablement is
   known, reject signing-secret substrings in connector and client-controlled
   durable identity fields with opaque errors, validate enabled values against
   their explicit effective format before listener/storage construction,
   compare signatures without disclosing mismatch details, and pass the same
   secret values into recursive durable content redaction. For GitHub, verify
   the exact body while separately marking event/delivery headers
   unauthenticated; require TLS rather than treating HMAC or durable
   deduplication as header authentication or signature freshness.
9. At durable event-store construction, collect resolved secure configuration
   values into a detached process-local list, discard values of three bytes or
   fewer to avoid redacting common text fragments, and pass the rest to exact
   recursive redaction without exposing the list through config JSON or logs.
10. Admit Delta Chat email as trusted automation only when contact verification
    and encrypted/signature authentication are both present, unless an explicit
    per-connector opt-in accepts unverified mail. Prefer provider receipt time,
    expose only bounded attachment metadata, and cap the actual copied stream
    independently of its declared size.
11. For event-derived classification configured with `tools: none`, remove
    tools from initial, repair, managed fallback, and child model execution.
    Treat the signed GitHub body projection as untrusted data, validate the
    required enum/enum/boolean result locally, and only then evaluate the
    separately declared MCP action using signed-body identity and fixed text.
    For own-PR development intake, treat the installed workflow output only as
    an opt-in marker, strictly validate the complete signed routing identity,
    consume only bounded exact JSON from the generation-fenced read-only GitHub
    tool, and compare exposed provider fields before immutable capture. Never
    infer the missing provider review node or inline-comment parent identity,
    and never pass review feedback to a model or action in this capture stage.
12. Wrap the live event operator controller in the gateway's existing
    constant-time PID bearer check. The launcher validates its dashboard
    session and affirmative same-origin replay metadata, allow-lists the path
    and query, overwrites client credentials with the PID token, bypasses
    environment HTTP proxies, bounds upstream responses, and maps stale
    internal authority to unavailable. Project rows
    into types without lease/deduplication fields and return payload only from
    the explicit no-store exact-text endpoint. The CLI uses the owner-readable
    PID file, validates bounded payload output without trimming or re-encoding
    it, requires deliberate replay confirmation, and never retries a replay
    POST.
13. For durable local mutation, create and persist missing directory entries
    from parent to child, sync replacement data before exposing the new name,
    and durably remove the old name. Use containing-directory sync on POSIX and
    write-through same-parent moves on Windows; a Windows deletion may leave a
    hidden tombstone after a crash, but never resurrects the original path.
14. For workflow inspection, open one exact source nonblocking under
    root-confined handles, verify the opened handle is regular, perform one
    bounded read, release the cross-process file lock, hash before projection,
    and construct the response from an explicit whitelist rather than
    serializing parser or runtime objects. Release the handler-local config
    mutation lock before encoding or response writing. Preflight each trigger
    family and omit it whole when its fixed field or aggregate-entry budget is
    exhausted or its visible text contains control or Unicode format
    characters. Apply independent topology, dependency, effect, validation,
    field, and encoded-response limits; return only fixed validation and limit
    codes, mark every omission incomplete, and preserve conservative action
    classification even when a target cannot be returned.
15. For workflow-authoring capability discovery, read the bounded PID record
    without cleanup and without loading configuration, validate its live
    authority, and authenticate one exact launcher-to-gateway GET with the
    process-local bearer. Acquire the existing loop's runtime-use lease and
    retain it across bounded registry selection, readiness checks,
    sanitization, and pre-marshalling. Never initialize MCP from this path;
    inspect ready MCP tools only when the pinned generation already owns a live
    manager. Address ordinary tools by effective core registration key and MCP
    tools by exact immutable server/tool identity. Treat every identity and
    parameter map as untrusted, contain per-tool panics, reject unsafe visible
    text before target construction, traverse only whitelisted shape keys with
    cycle and shared work budgets, and discard an unsafe shape as a whole.
    Bound and revalidate the internal response at the launcher, replace every
    internal failure with a fixed unavailable or partial state, and never
    forward browser credentials, process authority, descriptions, raw
    schema/config values, or error text.
16. For structured job/action authoring, authenticate through the existing
    dashboard boundary, strictly and incrementally decode one bounded request,
    and reject duplicates, unknown members, trailing values, invalid text,
    unpaired surrogate escapes, excessive numeric precision, and aggregate
    budgets before AST mutation.
    Hash the exact YAML, parse one document, and classify every structural node
    before projection; source directives, anchors/aliases, merges, unsafe tags,
    ambiguous mappings, and lossy values never enter typed mutation state.
    Global source directives, anchors/aliases, container ambiguity, and topology
    truncation block all mutation; a locally raw-only job/step
    remains present but does not block safe siblings. Share one aggregate
    budget across validation issue paths/messages, set fixed
    `validation_truncated` plus
    incomplete state whenever any diagnostic is omitted, but do not block an
    otherwise safe edit; pre-marshal under the final success ceiling. Compare
    the render revision, decode one operation and its set/remove envelopes under
    aggregate request work plus separate recursive budgets for each dynamic
    JSON value, reject operations whose exact target is raw-only, and reject a
    step move whose inclusive source-to-destination span contains a raw-only
    step. Apply the 256-byte single-line identity bound to step IDs and every
    `needs` reference. Expose every source-order insertion boundary in the
    browser, including boundaries beside raw-only nodes that cannot be crossed
    by a later move. Before sending a mutation, mirror the server's bounded
    string, key, control/format, collection, encoded-value, target, and numeric
    checks in the client while retaining the server as authority. Then edit one
    local AST and project the result without filesystem, config,
    development-store, runtime, event, or provider access.
    A set-only job-ID rename collision-checks the new exact key and edits only
    that key scalar; it never performs a broad textual rewrite or implicitly
    retargets `needs`. Sanitize validation and failures into fixed bounded
    data. In the browser, fence asynchronous editor/catalog/dependency responses
    to exact identities, treat manual and unprojectable actions as unknown
    authority, bind effect acknowledgement to the exact draft/scenario/review
    identity, and compare that captured identity again before invoking the
    separately authorized test request.
17. For current model selection, validate exact alias names and concrete model
    mappings, reject router names as override keys, expand every reachable
    account-router/model-router pairing, and verify the resolved model against
    the concrete account provider. Resolve an alias override only after account
    routing selects its concrete account. If the model alias is absent, stop
    with `no model configured`; if the account is absent, stop with
    `no account configured`. Never ask a provider for a default.
18. For a whole-config mutation, load the update-safe config and exact
    public-plus-security revision together under the shared lock, derive and
    validate one complete candidate, and compare-and-save against that revision.
    If another writer changed either file, return the stale-revision error
    without writing or automatically replaying the requested mutation. For the
    browser review-attention editor, strictly parse the complete GET projection
    into lossless tokens before hydration, keep the captured revision with the
    memory-only draft, locally validate one complete projected replacement,
    issue one PUT on explicit save, and replace draft state only with the
    authoritative successful response. A conflict or later observed revision
    remains an explicit reload/discard choice and never authorizes a rebase,
    retry, or gate execution.
19. For frozen media, enumerate and statically validate the entire locator
    batch before capture; preflight occurrence, locator, and supplied-metadata
    bounds, acquire one of four context-cancellable global capture slots, then
    charge distinct-asset, decoded, aggregate-occurrence, captured metadata, and
    encoded work while capturing. Open each live file through the platform's
    no-follow regular-file primitive while its store mapping is pinned, and
    compare its status-change token after the bounded read. Normalize a
    registered path to a cleaned absolute exact lexical lifecycle key and bind
    that live key to the first entry identity. For the same identity under a
    distinct key, retain both paths but make both lifecycles non-deleting; on
    re-registration, cancel older pending deletions by exact key and verified
    identity. Before final removal, recheck that token and entry identity under
    store synchronization; preserve a detected replacement.
    Build a deterministic complete set only after every asset succeeds.
    On decode or materialization, strictly validate the JSON envelope, version,
    canonical identities/order/metadata, size, digest, reference closure, and
    the same budgets before emitting any locator. Map all failures through the
    fixed redacted taxonomy and discard partial internal output.
20. For a compiler-private gate, reproduce the trusted in-memory workflow's
    compiler hash and verify its restricted shape, reject mixed public context, clone and bound the
    values, and capture exact owned conversation evidence before creating the
    run. Freeze every structured media locator during that capture, persist the
    strict versioned `FrozenSet` beside the rewritten snapshot, preserve
    runtime-only prompt/tool-call fields explicitly, and compute one immutable
    integrity revision over that frozen representation. Resume, retry, and
    restart validate the revision before materializing embedded media and never
    reread the live session or media store; provider calls derive only a
    domain-separated pseudonym.
    At every generic run, result, event, HTTP, SSE, development, and workflow-
    tool boundary, project by private visibility before encoding or publishing;
    default `Run` marshaling performs that redaction even without an explicit
    projector, while only the local store's encoder writes raw continuation.
    If projection authority or private integrity is unavailable, return the
    fixed failure rather than raw fallback. Present only the compiler-generated
    bounded human task when attention is deliberately requested.
    For automatic outgoing-review attention, commit only the occurrence beside
    the submitted transition. Under the current worker lease and runtime
    generation, resolve the first successful trusted policy capture and persist
    its bounded canonical envelope before private effects. On every attempt,
    strictly decode, validate, and re-hash the stored pin; a mismatch fails
    before session or run work. Retain the pin across retry and expose neither
    it nor lease, error, or linked-run state through public projections or logs.
21. For browser attention, authenticate first and replace browser authority at
    the launcher. Peek PID metadata without lifecycle effects and require an
    explicit nonzero port plus numeric loopback or literal current local-interface
    host before constructing a bearer request. On the protected gateway, validate
    the submitted case, immutable submission, and trigger first; branch historical,
    pending/claimed, no-op, and delivered state as specified by `FR-SEC-024`, and
    only for delivered load the exact decision link, private run, and stable task
    chain. Recompute each task payload hash before declassifying fixed state,
    configured questions, or an accepted response. Derive a length-prefixed domain-separated
    SHA-256 fence only for the sole current actionable waiting/recovery task; do
    not issue it when runtime resume is unavailable. For POST, reject all
    caller-supplied private linkage, recompute the fence from server state,
    derive a separate response ID from it and the normalized answer, resume the
    exact loaded task, and reproject so exact persisted replay recovers without
    accepting changed text. Keep the fence/draft out of route and persistent
    browser state. Independently classify the exact reserved workflow reference
    before every generic list, detail, events, SSE, graph, task, resume, cancel,
    or retry operation and return omission/not-found instead of raw fallback.
    Scrub direct hidden relationships and graph nodes/edges from visible ordinary
    reads, and deny mutation for the entire normalized transitive parent/child/retry
    component. Preserve exact reserved-reference runs from production web/CLI
    retention. With workflows disabled use no executor, expose no new response
    authority, keep exact replay read-only, and classify reserved resume before
    returning disabled state.
22. For an own-PR development read, authenticate first, match only the exact
    list or canonical public-case detail route, and reject every unsupported
    method, body, encoding, query, filter, cursor, or ID before store access.
    At the launcher, peek and numeric-local-validate PID authority without
    lifecycle effects, replace all browser authority, and make one bounded
    no-proxy/no-redirect protected GET. At the runtime, read one immutable page
    or case and construct the dedicated whitelist DTO field by field; never
    marshal a durable case. Treat every provider/ref/SHA value as captured at
    the returned time, preserve replay as a separate case, and keep feedback
    valid, bounded, and plain text. Omit all provenance, internal IDs, raw
    payload/error state, and capabilities. Navigation, list, detail, retry, and
    failure perform no provider read, model/gate/workflow action, checkout,
    filesystem/Git/CI operation, acknowledgement, or mutation.
23. For an own-PR development chat, require unambiguous same-origin provenance
    at the launcher, replace browser authority, and reject every provenance
    header at the protected runtime. At both boundaries reject a noncanonical
    route, raw query or bare `?`, invalid method/length/media/encoding/Unicode,
    excessive JSON depth/size, or anything except exact case-sensitive
    `expected_version` plus `content` before PID/store/model access. Preflight
    canonical case ID, version range, Go-trimmed bounded text, and agent config;
    then take the process-wide same-case lock and a per-service AI slot. Load
    and bind the case and complete count/byte/digest-validated transcript before
    rejecting stale state or reserving two rows plus worst-case assistant bytes.
    Append the human under the optimistic high-water fence and invoke only the
    exact replacement-prompt isolated no-tools/no-history/no-cache agent over a
    bounded detached historical projection. Validate and append its response
    separately. Any later failure preserves the human row and returns fixed
    text; detail is allowed only after an independent two-second safe reload.
    Keep browser state per case and memory-only, adopt details strictly and
    monotonically, recover matching version-plus-one human and version-plus-two
    completed outcomes, and never expose prompt/runtime/agent identity or infer
    checkout, provider, gate, or action authority from the conversation.
24. For controller-owned repair, independently validate the stored case and
    current provider review/PR/head before deriving the pin. Hold the concrete
    provider generation outside the runner, process-serialize the exact pin,
    acquire it, and validate the returned real locked checkout before constructing
    any model-visible capability. Create a new four-tool registry per call; bind
    each path through lexical and resolved-ancestor Git-control denial, validate
    the complete patch and tool-call batch before operation one, and execute
    mutations sequentially. Keep provider input detached and cache/affinity-free,
    preserve tool thought signatures, suppress argument logs, and reject a nil,
    malformed, oversized, unknown, duplicate, empty, or exhausted response. Once
    acquisition succeeds, always reacquire and compare the pin on exit, using a
    bounded cancellation-detached context if needed. Preserve allowed partial
    edits under the lock; never release, reset, execute, test, commit, or publish.
25. For a retained development line, admit adoption, resume, and park only
    through controller-owned exact fences under the reservation operation lock.
    Prove manager ownership, source identity, safe origin/control plane, detached
    clean commit/tree, line version/epoch, hashed reservation owner, and stable
    private ref before mutation. Advance and reference-fsync one exclusive loose
    ref without a reflog before clearing the mutation reservation, and reject
    parking while an inherited outer mutation operation is still live; after an ambiguous completed ref update,
    only the same durable pending tuple may reconcile it. For review, require the
    line already parked, hold manager/inventory serialization without acquiring
    an edit reservation, re-prove the exact prior-tip/tip/tree/ref and unlocked
    checkout, then run bounded sanitized Git object reads with attributes pinned
    to the exact tip and external helpers, local diff-driver configuration,
    ambient configuration, hooks, prompts, replacement objects, and lazy fetch
    disabled. Re-prove the parked state after the reads; bind the line version,
    park intent/epoch, commits/tree, paths, and diff into one domain-separated
    digest; validate every path and the complete diff encoding/limits before
    returning one all-or-nothing snapshot; never substitute a live worktree,
    generic workspace source, raw path, or mutable branch name.
26. For schema-v10 controller storage, validate one provider-verified thread,
    its complete membership, immutable pinned retained-workspace owner
    session/agent, exact latest queued-or-completed attempt, material revision,
    phase-specific lease shape, and complete review-fence hash chain before
    returning or mutating private state. Create a stable controller and line
    identity only under its first mutation claim and atomically suppress that
    owner from the legacy claim queue. Collision-check and inherit the exact
    owner-session reservation already locking the pinned workspace for first
    Adopt; use a globally fresh controller reservation for every later Resume.
    Fence every state-changing write with the exact current attempt, reserved
    revision headroom, fresh lease token and epoch, a non-regressing store time,
    and a still-live deadline. An exact committed Bind retry is a no-write
    replay only after those time/deadline checks; expiry enters recovery. Keep a raw reservation only in
    mutation or recovery-required state. After a trusted caller separately
    completes the latest attempt, parks, and snapshots the exact line,
    atomically record the exact contiguous
    attempt/version/epoch/intent/base/tip/tree/no-change/review-digest fence and
    a unique non-authorizing digest of the retired mutation bearer, then clear
    mutation authority before `review_pending`. Allow only a separate
    reservation-free review lease to claim that immutable fence. Rotate an
    expired review lease safely, but let only an exact current mutating or lease
    operation that encounters an expired mutation convert it to
    `recovery_required` without reclaim, replay, reset, or review. Finish or
    release review only with the same exact live fence and lease. Keep every
    field structurally private, redact raw reservation and lease tokens from the
    Reader result, and run no worker or filesystem/Git operation,
    model, AI review, CI, workflow, commit, push, merge, or provider action.
    Treat legacy claim suppression as storage ownership transfer, and do not
    imply that this slice can complete a later controller-owned queued attempt.
27. For schema-v12 recovery, create the hash-chained intent in the same
    transaction that clears the expired mutation token and enters
    `recovery_required`; never reconstruct legacy proof. Give only an exact
    renewable/reclaimable recovery claimant the old/fresh tuple. In the Git
    manager, take both reservation operation locks in canonical hash order,
    validate the exact unbound or bound-mutating fence, append the inventory-v3
    revocation record, and atomically replace ownership without any Git or code
    effect. Finalize only a matching live claim/result by installing the fresh
    controller bearer and fresh mutation token/epoch/deadline, erasing staged
    raw keys, and preserving line version/epoch/tip/tree. Reject pending park,
    changed evidence, reused hashes, later progress, corrupt chains, or
    insufficient revision headroom; do not run a worker, model, CI, workflow,
    provider, publication, push, or merge action.
28. For schema-v13 controller effects, validate private owner, controller,
    attempt, mutation lease, source/line state, operation order, request shape,
    prior hash, and exit headroom before durably preparing the exact operation;
    perform no Git effect first. Finalize a live operation only from its exact
    result and unchanged immediate authority. On expiry, move and claim that
    same operation rather than creating a pending v12 intent. Keep its recovery
    lease renewed while the trusted controller separately executes exactly one
    kind-specific reconciliation: composite Adopt/Resume with old and fresh
    locks held in canonical order through convergence and revocation; old-to-
    fresh rotation followed by exact Commit under fresh; or exact Park under
    old with no replacement. Finalize only a matching live claim, erase staged
    raw keys, and install fresh mutation authority for the first three kinds.
    For Park, atomically finalize operation, queued attempt/session when
    applicable, review fence, reservation retirement, and `review_pending`,
    leaving only the retained private branch. Mandatory linked v12 evidence for
    the first three kinds is already finalized and never independently
    claimable. Reject every changed, stale, cross-owner, partial, dirty,
    corrupt, or non-immediate replay without effect, fallback, or
    declassification.
29. For local-CI validation, first keep the exact reservation-derived operation
    lock while Git Workspaces revalidates the supplied pin, pre-attempt parent,
    and candidate evidence; materialize separate bounded `.git`-free roots and
    compute the full canonical SHA-256 manifest. Discover supported definitions
    and dependencies from both roots without process execution. Let exactly one
    explicit local plan suppress all inference; otherwise prefer a bounded
    repository-native quick profile and consult supported pull-request GitHub
    workflows only when no native step exists. Reject a GitHub job with multiple
    executable steps because each accepted step intentionally receives fresh
    filesystem/process state. Canonically order and digest a nonempty required-
    step plan; on any definition or semantic change, return incomplete
    `plan_changed` without execution. Bind a dependency-only change into a fresh
    environment/result identity. Persist or retrieve discovery only by the
    exact parent/candidate manifests and discovery/plan versions. Require
    Bubblewrap and a successful user-systemd cgroup-v2 supervision probe, then
    expose only the disposable candidate, bounded scratch/output filesystems,
    trusted identified host toolchains, explicit controller-provided read-only
    offline dependencies, and a clean allowlisted environment. Deny network,
    `.git`, the retained checkout, ambient credentials/config, provider/agent
    sockets, sibling workspaces, event storage, and writable evidence access.
    Execute every required step in deterministic order within per-step and total
    time/output/process limits; on cancellation or exhaustion, terminate the
    complete cgroup process tree. Revalidate the candidate fence and remove roots
    and proven-quiescent processes/scratch; if quiescence cannot be proven,
    return non-green and quarantine the owner-local scratch for operator cleanup.
    Persist only canonical evidence. Do not promote or reuse a passing production
    result while mutable toolchains or dependency mounts lack complete immutable
    manifests. A missing backend, environment, toolchain, mount, or executable;
    a command failure caused by missing package contents; failed postflight or
    cleanup; or any failed/incomplete required result fails closed without
    downloading, host fallback, or any model, controller, ledger, Git-lifecycle,
    workflow, provider, UI, or publication effect.
30. For retained-line suspension, require the exact current reservation-derived
    operation lock, private line fence, caller-durable request, and safe Git
    control plane before reading ordinary content. Build candidate evidence
    through a manager-private index and bounded sanitized plumbing. For
    prepared Commit, recreate and verify the deterministic child, accept only
    the exact retained parent or that child as detached `HEAD`, and bind the
    child to its exact prepared tree plus the applied bit without applying a
    missing child. Independently bind current candidate evidence over the
    retained parent; require exact prepared-tree candidate equality before an
    unapplied child and permit divergence only for retained edits after an
    applied child. Append and anchor the complete
    hashed suspension record in the same inventory save that retires the bearer
    and clears both owners; expose no record or raw bearer. For resume, require a
    globally fresh bearer and the exact current tail, re-prove candidate/ref and
    private ownership, normalize only an applied exact child and the real index
    to the retained parent without updating the worktree, re-prove candidate
    equality, then install that bearer at the unchanged version and mutation
    epoch. Treat the child-to-parent intermediate as replayable only for this
    exact resume. Reject all other drift or ambiguity without reset, checkout,
    clone, WIP commit, Park/review fence, CI/model/workflow/UI/provider effect,
    or generic declassification.
31. For schema-v17 recovery composition, acquire one exact event-runtime
    generation and one renewable tagged bound-v12/v13 recovery claim without a
    provider or model capability. Preflight history capacity before replacement
    rotation. Execute only the claim's exact kind: composite old-to-fresh Adopt
    or Resume; old-to-fresh rotation for bound v12; rotation followed by the
    immutable deterministic `CommitPinned` request for Commit; or old-only Park.
    Send Park directly to its existing atomic reservation-free handoff. For
    every other kind, persist and hash-bind the complete exact Git result before
    suspension, erase every raw old and duplicate fresh copy, issue no mutation
    lease, and let only the current recovery claim obtain the sole fresh bearer
    from `suspension_pending`. Call ordinary suspension for bound-v12,
    Adopt/Resume and prepared-Commit recovery suspension for Commit. Validate
    the exact result and candidate before atomically erasing the remaining
    bearer/claim and entering private `suspended`. On a later queued attempt,
    persist one globally unused bearer and exact resume intent under that
    attempt's scheduling claim, replay only the candidate-preserving Git resume,
    and atomically install the sole controller mutation owner before any
    compactor or edit model. Treat every crash boundary as exact replay of its
    last durable checkpoint, never as release, reset, clone, Park substitution,
    or a second model/effect. Do not select or mutate an unbound legacy v12 row
    until a separately approved idempotent retirement policy proves lost-response
    replay and stale-bearer revocation. Keep both phases, all raw/digested
    authority and evidence, and all errors outside JSON, logs, generic
    workspaces, models, workflows, browsers, providers, and local-readiness
    projection. Drain the generation and claim heartbeat before manager/store
    shutdown.
32. For exact parked-line push, reject malformed bounded request data before
    network access, then acquire the manager mutex and kernel inventory lock and,
    once acquired, hold both across admission, remote interaction, and
    postflight. Revalidate the exact stored target,
    reservation-free parked fence, source-to-expected-to-tip ancestry, private
    ref, clean detached checkout, origin, and Git control plane. Derive one
    source-branch destination and address the literal stored repository. Observe
    only that ref: return no-effect if it already equals tip, permit one
    expected-OID leased update if it equals expected, and conflict otherwise.
    Use bounded direct argv, fixed non-cancellation transport sentinels, no raw
    remote diagnostics, and no
    caller/repository credential or transport command; retain the existing
    trusted fixed SSH and ambient controller/operator transport boundary. After
    push may have started, reread under a bounded cancellation-detached context
    and revalidate local state. Return a receipt plus drift when remote success
    is proven but local postflight fails; otherwise return outcome-unknown and
    do not auto-retry across possible OID ABA. Persist no state and make no
    readiness, provider, publication, acknowledgement, or merge decision.
33. For schema-v18 publication storage, create an occurrence only inside the
    exact green passed-review transaction after complete local evidence and
    ledger/fence adjacency validation; never infer one during migration or
    backfill. Queue-renew only pre-effect `claimed` scheduling with fresh private
    tokens and epochs; PushJournal-renew only exact live `push_started` work.
    Create-once pin canonical operator policy, private subject, and provider
    observation, and compare exact hashes on replay. Through the separate
    DecisionRunStore, bind semantic decision-run linkage only when deterministic
    RunID creation is idempotent/replayable and a callback error guarantees no
    external creation; treat nil-return commit uncertainty as non-retryable
    admission uncertainty. Release the claim while a human task waits; neither waiting nor
    push-ready state owns a mutation reservation. Before possible push effect,
    authenticate a fresh claim and transactionally revalidate every current
    local and caller-supplied provider fence, persist the full canonical request,
    and enter `push_started` while controller mutation admission is excluded.
    Never invoke Git from the store. Finalize only the same claim/epoch/request
    with a bounded exact result; retain proven remote success independently from
    local drift. Expire unproved started state to non-reclaimable
    `outcome_unknown`; accept only separately supplied read-only desired-tip
    equality as later publication proof, never as permission for another push.
    Keep all records, canonical bytes, hashes, lease/decision identities, and
    diagnostics out of JSON, logs, models, workflows, providers, browsers, and
    generic workspace surfaces. Do not infer acknowledgement or merge behavior.
34. For the publication gate-context storage seam, accept only an exact live
    claim whose durable origin is `pending`. Under one SQLite read transaction,
    authenticate that lease and integrity-load the claim-redacted publication,
    selected case ordinal, canonical conversation and digest, exact passed
    attempt/review tail, case/thread/session/controller/fence/orchestration, and
    full ledger into one detached private snapshot. Reuse existing canonical
    conversation and evidence types instead of adding a raw transcript schema.
    On first subject pin, compare the snapshot-derived conversation version and
    digest inside the immediate write transaction; resolve an exact prior pin
    before that comparison so later chat cannot break idempotent replay. Run no
    provider, workflow, model, Git, filesystem, push, gateway, or worker effect.
35. For the uncomposed publication-gate processor, accept only one already
    claimed-from-pending private publication and separately injected narrow
    exact-claim authenticator, policy, context, queue, and full provider-observer
    capabilities. Authenticate the live token/epoch and authoritative pin
    progression before consuming any caller pin, returning only the exact
    repository policy selector alongside the redacted publication and no rich
    context.
    Consume a durable policy, subject, or provider pin before calling its producer.
    Pin policy only and return `requires_execution` for any active composition;
    compiler-confirm the zero-only case before its bounded conversation-fenced
    subject, one fresh provider observation, and empty-run push-readiness
    transition. Keep every request, result, pin, claim, subject, observation,
    and nested authority field outside JSON. Retain transient failures under
    the current claim for a separately authorized caller to pass to the safe
    pre-effect requeue primitive, and grant no
    claiming/renewal, run/executor/model, repair/filesystem/Git/push,
    gateway/worker, provider-write, acknowledgement, or merge capability.
36. For safe pre-effect requeue, require the caller to retain only the exact
    live scheduling claim and its durable origin. A pure shared helper may derive
    the deterministic one-second-to-one-minute saturating delay from claim count,
    but it obtains no store or effect capability. In one immediate transaction,
    first resolve the already-restored exact committed replay by epoch, origin,
    and availability before live clock/lease validation, even when it has become
    due. Otherwise authenticate ID/token/epoch/origin, require the supplied
    canonical availability not to predate the store-sampled transition, restore
    exactly that origin, and erase only scheduling ownership. Preserve all pins,
    decisions, local evidence, counters, and recorded parked-line state without
    reading the current branch or reservation. Admit no `push_started`, terminal,
    other unclaimed, cross-origin, or changed input; perform no high-water/
    provider/Git/model/workflow/worker action.
37. For active publication gates, lend a handler only the exact live claim and
    the phase-specific capabilities it needs. Consume immutable policy,
    subject, provider, and decision-run pins before their producers. On a first
    subject, build and compiler-preflight a canonical private replay envelope;
    pass only its nested bounded untrusted evidence projection into ordinary
    gates. Keep transcript text out of both envelope and model subject. When a
    working-context gate is present, require its sole agent to equal the repair
    owner, validate the envelope's exact prefix against the current canonical
    append-only conversation, and freeze only that prefix through the protected
    session/private-root path. Acquire no session for other gate kinds.

    After the subject pin, consume or obtain the full provider pin, derive the
    immutable decision/run identity, and resolve an exact linked or orphan run
    before avoidable mutable reads. Admit a new run only through the ordinary
    create-once private runner. Never rebuild pinned code/CI/diff evidence,
    advance the frozen transcript, repeat provider observation, or recreate a
    missing/uncertain run. Release case/runtime/scheduling ownership on wait;
    retain no mutation reservation. A gate-waiting handler observes and maps
    only the linked run. Only exact success reaches push readiness; failure,
    cancellation, and skip become `gate_failed`. A linked running continuation
    returns to the same reservation-free wait origin with a delay, while a
    pending run still running after synchronous admission becomes recovery.
    Fence every transition by the exact claim, requeue other classified
    transient pre-effect errors only to that claim's authenticated origin with
    pins intact, and stop effects on renewal ownership loss so only fenced
    expiry/reclaim can continue the work.

    Keep the phase dispatcher inert: validate one already-claimed record and
    invoke exactly one separately injected handler selected by `claim_from`.
    Give it no store scanner, claim, renewal, transition, or requeue capability,
    and require pending, gate-waiting, and push-ready handlers together.
    Requirement 050 supplies the separately fenced ready handler, but neither
    slice connects the dispatcher to the later production queue owner. No model,
    gate answer, workflow result, or push-ready state grants Git, push,
    provider-write, acknowledgement, merge, UI, config, or schema authority.
38. For a ready-origin publication, renew then atomically authenticate only the
    exact live claim and complete current high-water. Replace the detached
    caller record with the authority-redacted durable record, exact immutable
    case, and provider thread identity. While the queue lease remains live,
    perform one full read-only provider observation and require its canonical
    facts to repeat the immutable provider pin. Construct only the exact
    parked-line request from authenticated journal evidence; never accept a
    checkout path, refspec, credential, transport option, or alternate target
    from the caller.

    Drain queue renewal before write-ahead start. Replay a missing response only
    with the identical observation, observation time, and request. Only a newly
    committed exact start may lend the narrow pinned-line pusher for one
    invocation using the journal-returned request. An existing start, including
    exact recovery after a lost start response, lends no Git authority. After
    start, renew only the
    push journal until Git returns, drain that renewal at the immediate
    finalization barrier, and never requeue or obtain a mutation reservation.
    Map only a complete typed result to the bounded deterministic terminal
    outcomes; every cancellation, uncertain/partial/unclassified result or
    unrecoverable confirmed-post-start response boundary is unknown before any
    joined conflict or workspace-drift error is considered. A renewal failure
    may finalize unknown only while the exact started claim remains live;
    definitively stale authority leaves inert `push_started` state for the
    store's expiry transition to `outcome_unknown`. An unresolved Start
    response remains fail-closed without Git or finalization. Recover a missing
    finalize response only through an identical exact finalize replay.

    Preserve the parked reservation-free local line and withhold generic Git,
    provider write, acknowledgement, thread resolution, merge, workflow/model,
    HTTP/UI, config, production queue, and outcome-reconciliation capability.
    Before start, a classified transient may surrender only the exact ready
    lease with all evidence intact; after start, no path may authorize another
    remote effect.

## Cross-Feature Behavior

Launcher, tool execution, MCP stdio transports, providers, and web search all
depend on security behavior. Isolation can wrap command transports. Config
migration must preserve security defaults. Thread policy config shares the same
normalization and persistence path, while thread-specific behavior is owned by
the threads feature.
Workflow configuration, including definitions-directory defaulting and other
effective-value helpers, workflow tools, and workflow steps reuse the same config
validation, secret filtering, HTTP guard, and isolation policies instead of
introducing separate security controls. Model price and subscription metadata
remain non-secret config values while model credentials continue to use secure
string preservation. Model aliases and their concrete-account override keys are
also non-secret selection policy; they cannot contain provider credentials or
use an account router as override authority.
Workflow template and publish transactions also reuse the shared durable
directory, replacement, and removal primitives; their multi-file journaling and
recovery policy remain owned by the workflows feature.
Workflow definition inspection is likewise owned by the workflows feature. Its
authenticated UI and API expose a path-free whitelist rather than source YAML,
captured event content, authoring values, secrets, output expressions, or raw
internal errors.
Compiler-private gate storage and execution are likewise owned by the workflows
feature, while Agent Conversations owns frozen-provider execution. This feature
defines their shared non-declassification boundary: local persistence does not
grant generic observation authority, the raw PR-chat capability cannot become
provider cache/account identity, and a projection failure cannot fall back to a
raw run or event. The generated human task is an explicit bounded user-facing
handoff rather than an accidental diagnostic channel.
Workflow-authoring capability discovery is also owned by the workflows feature.
It reuses the process-local gateway authority and live runtime lease, reports
only exact addressable identity, fixed readiness, and bounded typed parameter
shape, and does not weaken the normal publish or execution readiness checks.
Structured job/action authoring is owned by the workflows feature. Security
defines its untrusted-input, statelessness, resource, stale-result, and
effect-acknowledgement boundary. The renderer does not inherit the catalog's
gateway authority; a catalog choice or manual target is merely draft text, and
the normal dependency, compatibility, workflow, tool, MCP, credential, and
isolation policies remain authoritative when a separately confirmed test or
published run executes it.
Review-attention policy selection, gate composition, and editor semantics are
owned by event automation, while launcher management owns authenticated route
registration and discoverability. Security owns the editor's lossless
projection, memory-only retention, exact captured-revision save fence, and
non-execution boundary. A reviewed repository cannot supply or override this
operator catalog, and neither an effective-policy preview nor an agent choice
grants model, tool, workflow, repository, provider, or GitHub authority. Event
automation also owns the outgoing submitted-review occurrence and its worker;
security requires its persisted policy pin and lease state to remain local,
strictly revalidated, and unavailable to browser or generic workflow surfaces.
The case-owned attention handoff is the outgoing-review browser
declassification: event automation owns its authoritative chain and lifecycle,
launcher management owns same-origin authority replacement and canonical focus,
and workflows own private task continuation. Security requires the opaque fence
to authorize only one current task, exact replay to remain idempotent, all
private identity and diagnostics to stay unrepresentable, the exact reserved
run to remain absent from generic workflow routes and ordinary retention, direct
hidden relationships to be scrubbed, and transitive relationship components to
be mutation-fenced. This outgoing-review bridge does not itself establish an
inbound own-PR feedback/development-case contract.
Event automation's separate own-PR development-capture contract establishes
only immutable provider-verified review-level intake. Workflows own its
explicit installed read-only trigger/action/output and ordered idempotent sink
handoff. Security requires the webhook and provider bodies to remain untrusted
data, the provider read to exactly match the authenticated author ID and
cross-bind authenticated repository/pull IDs through current PR facts on one
canonical origin, the trigger node ID never to be relabelled provider-verified,
and absent parent-review identity on inline comments never to be guessed. Only
that provider-cross-bound identity can append a private contiguous verified
thread membership; legacy migration isolates rather than infers, preserves the
existing case-scoped repair path, and cannot enter future thread-wide
orchestration without an explicit verified baseline. Connector or mutable
display facts cannot merge cases. This
capture stage provides no browser/API/CLI chat, gate, model, checkout, edit,
push, merge, acknowledgement, or GitHub action authority. Its later workbench
composes event automation's safe runtime projection with launcher authentication
and authority replacement. Security restricts reads to captured public facts
and plain-text feedback, keeps provenance and browser persistence outside the
boundary, keeps thread IDs/origin/object IDs private, and makes replay and
sibling reviews visible as separate cases. Its separate chat composes
only a bounded append-only transcript and isolated advisory model call; neither
viewer nor model acquires omitted development or provider authority. Any future
action must independently establish current local and provider state rather
than inheriting authority from capture, DTO, transcript, or answer.
The internal repair primitive performs that provider refresh only for one
edit-confined turn. Event automation owns durable case meaning and refreshed
review/head validation; Git workspaces owns exact pin acquisition, inventory,
control-plane checks, and later release; agent execution owns the isolated
borrowed-provider loop. Security requires their composition to expose only four
repository-content tools and never upgrades successful editing into review,
test, Git, CI, commit, push, merge, or provider-write authority.
The retained development-line primitive remains owned by Git workspaces and is
not yet invoked by that edit-only repair runner. A future trusted controller may
compose it after separate validation and commit evidence, but security requires
the mutating line to stay behind exact controller fences and the parked review
to use only bounded exact-object data without an edit reservation. Neither
composition exposes the private line, checkout, internal ref, reservation, or
Git process through the model, workflow runtime, generic workspace tool,
launcher, or browser.
The exact parked-line push in Git Workspaces requirement 018 and
`FR-SEC-042` is the narrow
`#126a` Git effect primitive. Git Workspaces owns its local/remote fences and
one-ref compare-and-swap; Security owns its authority minimization, sanitized
failure boundary, and trusted ambient-transport statement. Event Automation now
calls it only through the fenced ready-origin handler in `FR-SEC-050`; that
composition still cannot acknowledge a review, resolve a thread, or merge.
Event Automation requirement 069 and `FR-SEC-043` own
the `#126b1` effect-free schema-v18 local-evidence admission, private pins,
write-ahead request/result journal, and terminal uncertainty. `FR-SEC-044` now
supplies distinct least-authority full and head-only provider observation
capabilities; the uncomposed zero-gate processor may call only the full observer.
`FR-SEC-045` now supplies the atomic claimed-from-pending local gate-context read
and conversation-fenced first subject pin. `FR-SEC-046` composes those seams
only far enough to pin policy, return active compositions as
`requires_execution`, and compiler-confirm a zero-only path before readiness.
`FR-SEC-047` supplies only an exact pre-effect claim release and pure bounded
deterministic retry-delay calculation. `FR-SEC-048` safely projects an already
linked current publication decision through the existing case-owned attention
conversation with no new public source identity or publication authority.
`FR-SEC-049` composes the active mixed-gate path through a private replay
envelope, protected owner-matched transcript-prefix freeze, exact
policy/subject/provider/run pins, reservation-free wait, and an inert
already-claimed dispatcher. `FR-SEC-050` now supplies the third phase as a
least-authority push-ready handler over exact claim authentication, repeated
provider evidence, write-ahead start, at-most-once Git, and queue-to-journal
heartbeat transfer. `FR-SEC-051` supplies restart-safe read-only unknown-outcome
reconciliation, and `FR-SEC-052` supplies the complete generation-owned queue
runtime. That runtime composes journal, requeue, full and head-only observations,
the exact run store, and a narrow Git adapter through separate capabilities;
neither a journal row, context snapshot,
observation, processor result, retry delay, successful gate/run, primitive call,
nor `local_ready` supplies effect authority.
Acknowledgement and merge stay separate undefined features pending explicit
user-facing policy.
Schema-v10 eventing controller storage is that future controller-private
ownership seam, but not the future worker. Event automation owns its verified
thread/session binding, lease state, exact line projection, and immutable
attempt-review-fence chain; Git workspaces still owns every actual
adopt/resume/park/snapshot and retained-branch effect. The eventing store must
retire its mutation lease and usable bearer before a distinct reviewer can
claim the immutable fence, and mutation expiration must stop in explicit
recovery rather than being handled like safe review-lease expiration. This
composition adds no UI, model context, review agent, CI runner or cache,
workflow/gate execution, commit, publication, or provider authority, and its
`ready` phase cannot be used as evidence for any such effect.
Schema-v13 operation storage and Git-workspace composite recovery close only
the crash-safe local effect/capability seam. Event automation owns the private
request/result/claim chain and atomic Park database tuple; Git workspaces owns
inventory-v3, canonical old/fresh locking, exact line convergence, commit, and
park effects. The schema-v17 event-automation contract and `FR-SEC-041` compose them through a
generation-owned provider-independent worker and schema-v17 handoff.
Inventory-v4 suspension may consume only one already-converged current mutation
bearer at a time: old-to-fresh Adopt/Resume recovery or Commit rotation plus
exact deterministic Commit owns its revocation/effect first, eventing durably
checkpoints that exact result, and suspension then owns candidate capture and
fresh-bearer retirement. A crash between them remains an exact replayable
`suspension_pending` owner, not permission to guess, reset, commit, Park, or
publish. Park bypasses suspension and retires old through its existing atomic
review handoff. Before a later model call, Event Automation must persist a
globally fresh resume bearer and exact intent, replay the candidate-preserving
Git resume, and atomically install the sole mutation owner. Neither phase,
checkpoint, suspension fence, nor reservation-free state may enter a model,
generic workspace surface, browser local-status projection, review worker,
attention gate, or provider publication decision; legacy unbound v12 recovery
is excluded until a separately approved idempotent retirement protocol exists.
Local-CI discovery/sandbox/evidence is implemented by `#119` through this
requirement and the Event Automation local-CI contract. Git Workspaces owns exact
parent/candidate materialization, the reservation lock, and canonical manifest;
Event Automation owns deterministic precedence, persistent exact-manifest
discovery, and evidence identity; Security owns the mandatory disposable
no-network Bubblewrap plus user-systemd/cgroup-v2 boundary and its non-fallback
behavior. The controller supplies already-provisioned read-only offline
dependency mounts; downloading or hydration is not part of `#119`, so missing
inputs remain non-green until a later integration provides them. Execution
evidence persists, while production exact-success result reuse stays disabled
until toolchains and dependencies have complete immutable manifests. Generic
workflow primitives supply only a reusable step/DAG model: this validator does
not invoke the workflow executor or create a run, task, private context, event,
dispatch, or gate. Repair and ledger orchestration is implemented by `#120`,
the reservation-free AI review by `#121`, attention and read projection by
`#122` through `#124`, and suspended recovery composition by `#125b`; the
exact retained-line remote-tip CAS primitive is implemented by `#126a`, while
the eventing-only durable publication journal is implemented by `#126b1`.
The provider-observation boundary is implemented by `FR-SEC-044`, and the
atomic publication gate-context plus conversation-CAS storage boundary is
implemented by `FR-SEC-045`. The uncomposed, claimed-input zero-gate processor
is implemented by `FR-SEC-046`, and the safe pre-effect requeue/delay primitive
by `FR-SEC-047`. The publication decision attention projection is implemented
by `FR-SEC-048`, and active mixed-gate execution plus already-claimed phase
dispatch by `FR-SEC-049`. The fenced push-ready handler and exact pinned-line
adapter are implemented by `FR-SEC-050`. Restart-safe head-only reconciliation
is implemented by `FR-SEC-051`, and all-or-none generation-owned claiming plus
complete publication/reconciliation scheduling is implemented by `FR-SEC-052`.
Acknowledgement and merge remain separately unimplemented and deliberately
undefined.
None may infer its authority or successful outcome from a v13 operation,
completed repair row, retained branch, local-CI discovery/execution evidence, or
`review_pending` phase alone.
Git workspace configuration and tool enablement reuse the same config
normalization and defaulting path, while checkout retention, dirty preservation,
and workspace inventory security boundaries are owned by the git workspaces
feature.
Account router entries use first-class `account_routers[]` validation and
launcher normalization, and intentionally clear credential-bearing fields
because underlying account entries own secrets. Branch condition expressions are
validated as numeric account metrics and math constants, so routing decisions do
not require storing new secret material on router entries.
Generic durable event webhooks and channel-message adapters reuse secure-string
persistence and exact-value redaction; their transport lifecycle and request
contract are owned by
[Durable External Event Automation](event-automation.md).
The opt-in GitHub issue-triage workflow composes that authenticated body with
workflow-owned `tools: none` isolation and an ordinary declared MCP action. The
signing secret establishes ingress integrity only; it neither sanitizes issue
text nor supplies the MCP server's write credential.
Event operator access composes launcher authentication or local PID-file
authority with the gateway's protected runtime route. It does not add a second
database opener, listener credential, or browser-visible bearer.
MCP bearer and OAuth records reuse the shared auth store but remain keyed by
normalized server-scoped IDs, with collision-resistant forks when
rename/recreate/shared-name conflicts would otherwise reuse an active
credential. Runtime refresh and launcher replacement share the same
transactional locking boundary, while MCP-specific protocol and management
behavior remains owned by
[MCP Integration And Discovery](mcp-integration.md).
Frozen session media composes the tool feature's optional live-reference reader
with the session feature's versioned set and locator rewrite. Security owns the
safe-open, resource-bound, consistency-validation, and diagnostic-redaction
invariants; it does not wire the capability into agent/provider execution or
promise that a provider consumes every materialized modality.

## Failure And Edge Cases

- Partial secret updates preserve old value unless an explicit clear is requested.
- Empty defaults or a missing alias fail with `no model configured` before a
  provider transport is invoked. Raw model IDs are not accepted in
  alias-valued selection fields, and an account-router name cannot be used as
  an alias override key.
- A whole-config compare-and-save whose public JSON or security sidecar changed
  since its snapshot returns a revision mismatch and writes neither file; the
  concurrent winner remains intact for an explicit reload and retry.
- A malformed, duplicate, trailing, unsafe-Unicode, over-bound, or numerically
  lossy attention-policy response never partially hydrates the editor. Policy,
  questions, repositories, revisions, validation detail, and agent catalogs
  remain absent from route state, browser storage, logs, and toast text.
- A dirty or stale attention-policy draft stays only in memory. Background
  reads cannot overwrite it, save conflict never triggers an automatic rebase
  or retry, and only a confirmed explicit reload/discard may destroy it.
- Editing, validation, effective preview, refresh, save-conflict handling, and
  discard cannot launch a gate or workflow, call a model or tool, create a
  review action, write a repository, emit an event, or mutate a provider or
  GitHub resource.
- Submitted projection validates status-specific authority: historical absence
  is `none`, pending/claimed require no run, no-op requires a canonical all-zero
  pin/no run, and delivered requires a canonical active pin/link/run/task chain
  whose displayed payload matches its input hash. Malformed required state fails
  without raw fallback. Disabled runtime is read-only and may show waiting or
  recovery lifecycle but exposes neither `can_respond` nor a response fence;
  exact persisted replay remains idempotent and reserved resume returns 404 first.
- Noncanonical attention paths or queries, cross-site POST, malformed or
  oversized JSON, missing/zero-port, hostname, wildcard/unspecified, multicast
  or remote PID authority, redirect, proxy use, timeout, invalid upstream
  content type/body, stale/cross-case/cross-task fence, and altered replay fail
  without mutating a review case or consuming another task. An exact answer
  already persisted before continuation/transport failure recovers through its
  separate response ID and authoritative reprojection.
- Attention URLs, DTOs, browser storage, and fixed errors cannot contain process
  authority, private run/task/workflow/session/policy identity or revision,
  input hashes, trigger lease/retry state, or raw stored/upstream errors. Generic
  workflow routes suppress exact reserved attention references before any
  observation or mutation, including malformed impostors. Ordinary projections
  and graphs scrub direct hidden relationships; the normalized transitive
  parent/child/retry component is denied mutation; and production web/CLI
  retention preserves exact reserved-reference runs for restart replay.
- Development-chat prompt injection remains quoted historical data under an
  exact isolated replacement prompt. The launcher rejects missing/ambiguous or
  cross-site evidence and strips it; the runtime rejects any browser-provenance
  header. Invalid route/ForceQuery/media/encoding/Unicode/exact-key JSON, stale
  version, insufficient complete-turn capacity, corrupt transcript, unavailable
  model, invalid output, timeout, assistant append, or later validation failure
  exposes no raw diagnostic or capability. Any committed human row remains
  durable, but error detail is visible only through a separate fresh validated
  reload. Chat never changes capture ordering or grants a checkout, tool, gate,
  provider, or Git action.
- A malformed, stale, cross-line, wrong-reservation, wrong-agent, dirty,
  attached, non-direct-child, ref-moved, symlink-substituted, or inconsistent
  retained line fails before adoption, resume, park, or review can claim
  success. Failure never exposes the manager path/ref/reservation, resets
  ordinary files, unlocks a different mutation, adopts a generic checkout, or
  falls back to a fresh clone or live-worktree review.
- A parked exact-SHA review returns no partial result when its line becomes
  mutating, its version/base/tip/tree or ref changes, inventory is corrupt,
  cancellation wins, changed paths exceed count/individual/aggregate bounds,
  a path is noncanonical or contains control/format characters, or the diff is
  oversized, invalid UTF-8, bare-CR, or NUL-bearing. CRLF is canonicalized to LF,
  and it cannot invoke an external diff,
  text converter, hook, lazy fetch, network/provider call, model, generic tool,
  push, merge, or publication fallback.
- Exact parked-line push fails before effect when the stored target, complete
  parked fence, source-to-expected-to-tip ancestry, private ref, checkout, or
  control plane differs, or when preflight observes a missing/different tip.
  It never redirects through `origin`, accepts another ref/tag/delete, or emits
  a multi-ref update.
- Once push may have started, unreadable or non-tip readback is explicitly
  outcome-unknown and never an automatic retry; expected-to-tip-to-expected OID
  ABA cannot be distinguished safely. Proven remote success plus local drift
  returns the sanitized receipt and drift together rather than erasing the
  remote effect.
- Pinned-line push errors expose no raw remote output or credential material,
  and callers/repositories cannot supply a helper or transport command. The
  fixed trusted SSH command and ambient controller/operator transport remain
  trusted under the existing threat model; no stronger proxy, SSL, netrc, or
  credential-broker guarantee is introduced.
- A schema-v18 publication occurrence is inseparable from its exact green
  passed-review completion. If CI/ledger/fence/controller/source/line evidence
  is stale, incomplete, compatibility-defaulted, nonadjacent, or cannot be
  inserted, the complete review, ledger, fence, controller, and occurrence
  transaction rolls back. Migration never turns an older passed review into new
  action state.
- Publication leases are private scheduling credentials, not mutation
  reservations, provider credentials, or workflow authority. Pre-effect expiry
  can rotate only the same semantic work under a fresh token/epoch, and a human
  wait holds no claim. Queue renewal accepts only pre-effect `claimed`; PushJournal
  renewal accepts only `push_started`. A later repair may supersede pre-effect
  work. `push_started` alone blocks mutation; its admission and mutation
  acquisition cannot both commit.
- Safe pre-effect requeue restores only the exact authenticated claim origin at
  an availability that is non-past at the live transition and releases only
  scheduling ownership. Pins, decisions, counters, local evidence, and recorded
  parked-line state survive without a current branch or reservation read. Exact
  response-loss replay requires the same epoch, origin, and availability,
  resolves before live clock/lease validation, and may succeed after becoming
  due. Stale, expired, cross-origin, other unclaimed, started, terminal, or
  altered input changes nothing and acquires no high-water, provider, Git,
  model, or worker capability.
- An active subject pin is a canonical private replay envelope, not the model
  subject and not a transcript store. Only its bounded untrusted evidence
  projection reaches ordinary gates. Raw conversation may enter only an
  owner-matched protected session after exact prefix version/digest validation,
  and private-root freezing makes that prefix immutable across later chat,
  restart, and response. Isolated/deterministic/zero gates receive no session;
  owner mismatch, prefix rollback, changed evidence, or size/shape failure
  reaches no model or run admission.
- Active replay consumes policy, subject, provider, and decision link before
  the corresponding producer and resolves an existing/orphan run before
  avoidable mutable reads. Missing, malformed, cross-bound, or admission-
  uncertain work is never recreated. Human wait releases scheduling, case, and
  runtime owners and retains no mutation reservation; a reclaimed waiting phase
  may only observe the linked run. A still-running waiting continuation returns
  to that same wait origin; other classified transient pre-effect retry
  preserves pins and link, while renewal ownership loss stops effects and all
  status transitions require the exact live claim and durable run state. Only
  success reaches push readiness; failure, cancellation, and skip fail the gate.
- The phase dispatcher receives no queue or transition capability. It routes
  exactly one structurally valid caller-owned claim, requires all three phase
  handlers, and returns the selected handler's error without reinterpretation.
  The ready handler completes that internal set. The generation-owned
  publication worker is the sole production attachment: it receives only the
  claim method, asks for one row, and gives the exact result to this dispatcher.
- Production publication and head-only reconciliation use separate outer-
  generation-leased loops. Their all-or-none runtime resolves parked-workspace
  and narrow push readiness before any durable operation, builds both workers
  together over the exact run store and distinct provider observers, and drains
  both loops before store, evidence, provider, agent, or Git-generation teardown.
  Incomplete or dynamically Git-unready composition remains durably inert.
- The push-ready handler authenticates one live exact ready-origin lease and
  repeats the pinned provider facts before deriving the write-ahead request. It
  drains queue renewal before start, activates only push-journal renewal after
  start, and lets only `newlyStarted=true` authorize one narrow Git call. Start
  or finalize replay never repeats Git, the branch remains parked and
  reservation-free, and review-write, acknowledgement, thread resolution, and
  merge remain unavailable.
- Expired or unproved `push_started` state is `outcome_unknown` and cannot be
  reclaimed, returned to push-ready, or used to invoke Git. A current desired
  remote tip can prove publication for reconciliation, but expected/other/
  missing/unavailable observations cannot prove absence across remote ABA.
- Publication policy, subject, provider, decision, request/result, claim, hash,
  and diagnostic bytes have no JSON or generic projection and contain no raw
  external output or credential. The store has no provider/Git/model/workflow/
  gateway/worker capability; even proven publication supplies no review
  acknowledgement or pull-request merge authority.
- A schema-v13 crash after prepare exposes no generic capability and leaves only
  the exact operation recoverable. Expiry cannot create a competing pending v12
  claim. Adopt/Resume cannot drop the old lock before durable revocation,
  Commit cannot run before recovery rotation, and Park cannot mint a fresh
  bearer; stale or cross-kind callers therefore fail instead of racing a second
  effect.
- Schema-v17 automatic recovery never exposes the post-recovery fresh bearer as
  a controller mutation lease. Before its durable checkpoint only the exact Git
  recovery may replay; while `suspension_pending` only the current recovery
  claim may retrieve the sole fresh copy for exact suspension; at `suspended`
  no raw recovery bearer or claim remains. Corrupt or partial erasure fails
  aggregate validation and cannot fall back to model-capable mutation.
- Legacy unbound v12 recovery is not automatic cleanup authority. The worker
  skips pending and expired-claimed rows without a bound-line fence and never
  calls rotation or release for them; the private recovery remains non-ready
  until a separately reviewed idempotent retirement protocol proves exact
  lost-response replay and permanent stale-bearer revocation.
- Suspension cannot convert an arbitrary detached commit, staged-only state,
  changed private ref, malformed or truncated hash chain, reused bearer,
  candidate drift, or partial Commit evidence into reservation-free success.
  Resume recognizes only the latest exact candidate and the two
  crash-reconcilable prepared-child/retained-parent control-plane states; it
  never resets the worktree, deletes untracked files, creates a WIP commit,
  advances a ref or epoch, or exposes a review/local-ready/publication fact.
- Suspended resume is write-ahead and attempt-specific. A fresh bearer is staged
  only under the queued orchestration claim and exact latest suspension; no
  compactor or edit model may run until Git resume and eventing finalization
  install it as the sole owner. A stale attempt, changed candidate, lost
  generation, or ambiguous response replays the same intent or stops without
  another bearer, reset, release, model, review, or provider effect.
- A Park database failure publishes neither a partially completed queued
  attempt nor a review fence/phase without the others. Recovery must first
  prove exact Git Park replay under old, after which one transaction retires
  mutation authority while retaining the branch. The generation-owned reviewer
  can claim only its separate reservation-free lease after the completed
  orchestration validates, and requests the exact-object projection by line,
  version, base, tip, and tree before checking every returned fence field.
- Local-CI discovery cannot turn repository definitions into host execution.
  No required step, unsupported/incomplete definition, or any definition or
  semantic-plan change is non-green `plan_changed` and executes nothing.
  Dependency-only drift changes environment/result identity and invalidates a
  prior success even when the required step definition remains stable. An
  explicit plan is authoritative; otherwise native quick profiles precede
  GitHub fallback. A fallback GitHub job with multiple executable steps is
  rejected because the validator cannot preserve shared state across its fresh
  step sandboxes. Discovery persistence matches only the exact parent/candidate
  manifests and discovery/plan versions.
- Missing or unsupported local-CI sandbox support is a failed validation, not
  permission to use the host or generic optional isolation. The Linux backend
  requires Bubblewrap and a successful user-systemd delegated cgroup-v2
  supervisor handshake. Repository steps receive no `.git`, retained checkout,
  ambient credential/configuration, network, provider/agent socket, sibling
  workspace, event database, or writable evidence store. Timeout, output
  exhaustion, cancellation, surviving process descendants, candidate drift,
  failed postflight, or cleanup prevents success. If cgroup quiescence cannot
  be proven, the owner-local execution scratch is quarantined for operator
  cleanup rather than being unsafely deleted.
- Dependency mounts are explicit controller-provided read-only offline inputs.
  Local CI neither downloads nor provisions them; a missing required mount or
  executable is non-green `environment_unavailable`, while a command that
  discovers absent package contents may instead be non-green `failed`. A later
  integration must supply either before validation can pass.
- A malformed, failed, incomplete, digest-mismatched, or identity-mismatched
  local-CI evidence record never declassifies output or returns green. Exact
  matching covers the complete candidate and manifest, plan and policy,
  environment/toolchain/dependencies, sandbox/platform, required step set, and
  output/result digests. Production result promotion/reuse is disabled while
  host toolchains or dependency mounts remain mutable; a future immutable
  backend still permits no prefix, partial, or fallback reuse.
- Concurrent atomic writes do not fail due to temporary filename collisions.
- Concurrent auth-store writers preserve unrelated credentials; on hosts with
  OS file locking, a stale OAuth refresh cannot overwrite a credential replaced
  by another process while it waited for that lock.
- An empty durable path, failed parent-directory durability boundary, failed
  replacement, or failed logical deletion returns an error instead of treating
  the mutation as committed. A crash may leave a hidden Windows tombstone, but
  the removed original name does not reappear.
- Unsupported provider token families, such as classic GitHub PATs for
  Copilot-backed accounts, are rejected before persistence.
- Invalid protected command patterns fail validation.
- Unsupported isolation platform returns clear error.
- Private host requests are denied unless whitelisted.
- Public-looking MCP OAuth hosts cannot pivot to loopback, private, CGNAT, or
  other special-use addresses through DNS rebinding or environment proxies;
  redirects cannot change origin or downgrade HTTPS.
- A deliberately local MCP server may use loopback HTTP, but discovered OAuth
  endpoints cannot pivot to a different local address.
- Missing or malformed OAuth JWT email claims do not fail token parsing.
- Security YAML cannot enable, add, or resurrect a removed event webhook
  connector; malformed enabled secrets fail before the shared route is active.
- Inactive file/encrypted webhook references are preserved without resolution;
  credential-bearing connector/type/deduplication identities fail before config
  or event persistence and error responses omit the conflicting values.
- GitHub webhook bodies are authenticated before decoding, but event and
  delivery headers remain unauthenticated and there is no signed timestamp.
  Trusted TLS protects those headers; retained delivery-ID deduplication limits
  retries but is not cryptographic replay prevention and ends when retention
  pruning removes the original event.
- A valid GitHub body signature does not make issue title, body, author, or
  repository text trusted instructions. Unsupported no-tool modes fail
  validation; classifier output that fails its enum/boolean contract cannot
  invoke a hidden fallback action.
- A valid signature or successful provider read likewise does not make own-PR
  review feedback instructions or development authority. Missing, duplicate,
  trailing, malformed, deep, or oversized provider data; an unsafe confined
  artifact; exact review/PR mismatch; or bounded review-scan exhaustion fails
  before case creation. Provider omission of review node and inline parent
  identity is preserved as a stated limitation rather than guessed linkage.
- Development list/detail reads reject noncanonical paths, methods, encodings,
  filters, cursors, IDs, unsafe gateway authority, redirects, malformed or
  oversized JSON, and invalid stored text before returning a partial DTO. They
  never fall back to raw durable-case serialization or include provenance in an
  error.
- A development read never refetches GitHub or upgrades captured “current”
  fields into live authority. Feedback remains plain text, external links have
  opener isolation, replay remains separately visible, and no route, browser
  storage, or DTO retains cursors, credentials, internal IDs, payloads, or an
  action capability.
- Installing the issue-triage template never enables ingress or MCP. The
  declared GitHub action fails normally when its explicitly enabled,
  non-deferred MCP capability or separate write credential is unavailable; it
  does not borrow the webhook signing secret.
- Resolved channel and provider credentials are detached only at the trusted
  event-store construction boundary; they are never added to event identity,
  payload, config JSON, or logs.
- Missing/wrong PID bearer credentials never reach the event store. The
  launcher does not pass browser cookies or authorization upstream, and an
  internal `401` becomes operator unavailability rather than a dashboard login
  response. Cross-site or malformed replay requests create no event.
- Event and dispatch responses cannot include live lease tokens or
  deduplication keys through accidental struct serialization. Payload responses
  are opt-in, exact, and non-cacheable; clients render them only as text.
- Missing or wrong PID authority, a stale or old gateway, timeout, malformed or
  oversized internal capability response, and base runtime unavailability all
  become one fixed non-cacheable dashboard `503`; upstream credentials,
  headers, bodies, and error strings are never reflected. A missing live MCP
  manager instead returns a fixed partial catalog without initializing it or
  revealing its cause.
- Capability identities containing invalid UTF-8, control or Unicode format
  characters, unsafe separators, or over-bound text are omitted. Panicking,
  cyclic, composition-heavy, reference-bearing, or over-budget parameter maps
  cannot escape the typed shape whitelist or consume a fresh full budget per
  discarded schema; one omitted shape never exposes its raw value or error.
- Job/action requests with duplicate or unknown members, trailing JSON, missing
  required values, stale revision, invalid operation identity, invalid UTF-8 or
  unpaired surrogate escapes, non-JSON-compatible dynamic values,
  browser-unsafe numbers, or exceeded source/work/response budgets return fixed
  bounded failures and mutate nothing. A semantically invalid but structurally
  safe candidate is returned only with sanitized bounded validation so it can
  be repaired locally. A colliding job rename is rejected, and a successful
  rename cannot silently rewrite authority-bearing expressions or dependency
  references elsewhere.
- Validation count or aggregate text exhaustion sets the fixed
  `validation_truncated` limit, marks the result incomplete, and retains only
  bounded sanitized diagnostics; it cannot create an oversized or opaque
  success response.
- YAML version/tag directives, anchors/aliases, merges, unsafe tags, duplicate
  or non-string keys, ambiguous containers, cycles, and lossy scalar or dynamic
  shapes cannot be expanded, flattened, or partially edited by the job/action
  API. They stay outside typed
  mutation state; a safe sibling render preserves their nodes, values,
  comments, order, and scalar style, while only a semantic no-op guarantees
  byte-exact source identity. Global ambiguity or topology truncation blocks
  every structured operation; validation-only truncation does not. A locally
  raw-only job rejects patch/delete and a locally raw-only step rejects
  patch/delete/move or being crossed by another step's move, while other safe
  sibling edits and structurally safe insertions remain available.
- A capability-picker result, editor response, dependency report, or effect
  acknowledgement for another exact identity cannot authorize or change the
  current draft. Unknown/manual targets, `workflows/` calls, raw-only actions,
  and incomplete or empty projections remain conservatively effectful even
  when mixed with known targets. Closing or changing the review clears consent,
  and a final identity mismatch creates no run.
- A caller cannot manufacture private authority by serializing a compiled
  workflow, setting a visibility string, or attaching a private root to another
  workflow. Mutating a compiled workflow changes its hash and fails admission.
  Capture or integrity failure creates no executable context; an update cannot
  strip or swap the root. Resume, retry, and restart cannot recapture newer chat
  history, reread cleaned-up live media, lose runtime-only prompt/tool-call
  metadata, or replace the persisted `FrozenSet`; revision and strict set
  validation precede any materialization. Missing projection authority drops
  private event content or returns a fixed error rather than encoding raw
  inputs, outputs, errors, session, delivery, scope, frozen/materialized media,
  or provider identity.
- Unverified email is skipped by default; an explicit opt-in marks it
  unverified. Private Delta Chat blob paths and copy errors do not enter durable
  events or attachment diagnostics, and oversized files are not materialized.
- Frozen media rejects raw paths, network/file/unknown schemes, noncanonical
  `media://` UUID capabilities, malformed or noncanonical padded-base64 data,
  a resulting filename basename that is invalid UTF-8, contains a control, or
  exceeds 255 bytes, a MIME value that cannot normalize to parameter-free
  canonical form within 127 bytes, and any locator whose live store entry is
  absent at capture.
  It does not search temporary directories or reuse a path from an earlier
  process generation.
- A mapped symlink, reparse point, directory, FIFO, socket, or device is never
  read as frozen media. A safe open that cannot be established
  fails with a fixed unavailable/unsafe class; it does not fall back to ordinary
  `os.Open`, follow the entry, or wait on a special stream; non-Unix,
  non-Windows targets fail closed.
- Declared sizes and JSON/base64 lengths are hints only. Raw locator and
  metadata inputs are capped before linear scans or copies. Actual decoded/read
  bytes, repeated occurrence cost, distinct assets, metadata, and final encoded
  form are independently charged; an empty asset, exceeding 32 locators, 16
  assets, 2 MiB per asset, 3 MiB occurrence-weighted raw bytes, 5 MiB
  materialized encoding, or 5 MiB frozen-set JSON returns no partial result.
  At most four captures hold admission at once, and a saturated waiter remains
  cancellable before its reader is invoked.
- An unsupported version, duplicate, missing, or unused record, unknown or
  trailing JSON field, invalid UTF-8 or unpaired surrogate, invalid canonical
  order/identity, size/digest mismatch, dangling frozen reference, or changed
  provider-authoritative occurrence metadata fails before output. Returned
  snapshot, freeze, and materialization failures never echo a reference, path,
  filename, MIME value, data-URI/base64 text, decoded bytes, or underlying
  parser/filesystem error.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-SEC-001`, `FR-SEC-003` | [pkg/config/config_struct_test.go](../../pkg/config/config_struct_test.go), [pkg/config/security_test.go](../../pkg/config/security_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go), [docs/security/sensitive_data_filtering.md](../security/sensitive_data_filtering.md) |
| `FR-SEC-002`, `FR-SEC-007` | [pkg/credential/store_test.go](../../pkg/credential/store_test.go), [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/auth/store_test.go](../../pkg/auth/store_test.go), [pkg/auth/token_test.go](../../pkg/auth/token_test.go), [pkg/auth/pkce_test.go](../../pkg/auth/pkce_test.go), [pkg/mcp/auth_test.go](../../pkg/mcp/auth_test.go) |
| `FR-SEC-004` | [web/backend/api/auth_test.go](../../web/backend/api/auth_test.go), [web/backend/api/auth_csrf_test.go](../../web/backend/api/auth_csrf_test.go) |
| `FR-SEC-005`, `FR-SEC-006` | [pkg/utils/http_guard.go](../../pkg/utils/http_guard.go), [pkg/isolation/runtime_test.go](../../pkg/isolation/runtime_test.go), [pkg/netbind/netbind_test.go](../../pkg/netbind/netbind_test.go), [pkg/mcp/network_test.go](../../pkg/mcp/network_test.go), [pkg/mcp/oauth_test.go](../../pkg/mcp/oauth_test.go), [web/backend/api/mcp_oauth_test.go](../../web/backend/api/mcp_oauth_test.go) |
| `FR-SEC-008` | [pkg/config/model_config_test.go](../../pkg/config/model_config_test.go), [pkg/config/model_alias_test.go](../../pkg/config/model_alias_test.go), [pkg/config/model_alias_migration_test.go](../../pkg/config/model_alias_migration_test.go), [pkg/config/model_selection_test.go](../../pkg/config/model_selection_test.go), [pkg/config/account_router_test.go](../../pkg/config/account_router_test.go), [pkg/config/config_test.go](../../pkg/config/config_test.go), [pkg/providers/common/reasoning_effort_test.go](../../pkg/providers/common/reasoning_effort_test.go) |
| `FR-SEC-009` | [pkg/auth/oauth_test.go](../../pkg/auth/oauth_test.go), [web/backend/api/oauth_test.go](../../web/backend/api/oauth_test.go) |
| `FR-SEC-010` | [pkg/config/events_test.go](../../pkg/config/events_test.go), [pkg/config/events_secret_identity_test.go](../../pkg/config/events_secret_identity_test.go), [pkg/eventing/webhook/controller_test.go](../../pkg/eventing/webhook/controller_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [web/backend/api/config_test.go](../../web/backend/api/config_test.go), [web/backend/api/config_event_webhook_deferred_test.go](../../web/backend/api/config_event_webhook_deferred_test.go) |
| `FR-SEC-011` | [pkg/config/events_channels_test.go](../../pkg/config/events_channels_test.go), [pkg/eventing/channelmessage/backend_test.go](../../pkg/eventing/channelmessage/backend_test.go), [pkg/channels/deltachat/deltachat_test.go](../../pkg/channels/deltachat/deltachat_test.go), [pkg/gateway/event_channel_test.go](../../pkg/gateway/event_channel_test.go) |
| `FR-SEC-012` | [pkg/config/events_webhook_format_test.go](../../pkg/config/events_webhook_format_test.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/webhook/handler_store_test.go](../../pkg/eventing/webhook/handler_store_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go), [pkg/prdevelopment/capture_test.go](../../pkg/prdevelopment/capture_test.go), [pkg/gateway/pr_development_capture_test.go](../../pkg/gateway/pr_development_capture_test.go) |
| `FR-SEC-013` | [pkg/workflows/validator_test.go](../../pkg/workflows/validator_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/templates_test.go](../../pkg/workflows/templates_test.go), [pkg/gateway/event_webhook_test.go](../../pkg/gateway/event_webhook_test.go) |
| `FR-SEC-014` | [pkg/health/server_test.go](../../pkg/health/server_test.go), [pkg/eventing/operator](../../pkg/eventing/operator), [pkg/gateway/event_operator_test.go](../../pkg/gateway/event_operator_test.go), [web/backend/api/events_test.go](../../web/backend/api/events_test.go), [cmd/picoclaw/internal/events](../../cmd/picoclaw/internal/events) |
| `FR-SEC-015` | [pkg/fileutil/file_test.go](../../pkg/fileutil/file_test.go), [pkg/fileutil/durable.go](../../pkg/fileutil/durable.go), [pkg/fileutil/durable_unix.go](../../pkg/fileutil/durable_unix.go), [pkg/fileutil/durable_windows.go](../../pkg/fileutil/durable_windows.go) |
| `FR-SEC-016` | [pkg/workflows/inspection.go](../../pkg/workflows/inspection.go), [pkg/workflows/inspection_open_unix.go](../../pkg/workflows/inspection_open_unix.go), [pkg/workflows/inspection_open_other.go](../../pkg/workflows/inspection_open_other.go), [pkg/workflows/inspection_test.go](../../pkg/workflows/inspection_test.go), [pkg/workflows/inspection_open_unix_test.go](../../pkg/workflows/inspection_open_unix_test.go), [web/backend/api/workflow_inspection.go](../../web/backend/api/workflow_inspection.go), [web/backend/api/workflow_inspection_test.go](../../web/backend/api/workflow_inspection_test.go), [web/frontend/src/components/workflows/workflow-definition-inspector.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.tsx), [web/frontend/src/components/workflows/workflow-definition-inspector.test.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.test.tsx) |
| `FR-SEC-017` | [pkg/workflows/authoring_capabilities.go](../../pkg/workflows/authoring_capabilities.go), [pkg/workflows/authoring_capabilities_test.go](../../pkg/workflows/authoring_capabilities_test.go), [pkg/agent/workflow_authoring.go](../../pkg/agent/workflow_authoring.go), [pkg/agent/workflow_authoring_test.go](../../pkg/agent/workflow_authoring_test.go), [pkg/gateway/workflow_authoring.go](../../pkg/gateway/workflow_authoring.go), [pkg/gateway/workflow_authoring_test.go](../../pkg/gateway/workflow_authoring_test.go), [web/backend/api/workflow_authoring.go](../../web/backend/api/workflow_authoring.go), [web/backend/api/workflow_authoring_test.go](../../web/backend/api/workflow_authoring_test.go), [web/frontend/src/api/workflow-capabilities.test.ts](../../web/frontend/src/api/workflow-capabilities.test.ts), [web/frontend/src/components/workflows/workflow-capability-catalog.test.tsx](../../web/frontend/src/components/workflows/workflow-capability-catalog.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-018` | [pkg/workflows/editor_jobs.go](../../pkg/workflows/editor_jobs.go), [pkg/workflows/editor_jobs_test.go](../../pkg/workflows/editor_jobs_test.go), [web/backend/api/workflow_jobs_editor.go](../../web/backend/api/workflow_jobs_editor.go), [web/backend/api/workflow_jobs_editor_test.go](../../web/backend/api/workflow_jobs_editor_test.go), [web/frontend/src/api/workflow-jobs-editor.test.ts](../../web/frontend/src/api/workflow-jobs-editor.test.ts), [web/frontend/src/components/workflows/workflow-job-editor.test.tsx](../../web/frontend/src/components/workflows/workflow-job-editor.test.tsx), [web/frontend/src/components/workflows/workflow-capability-target-field.test.tsx](../../web/frontend/src/components/workflows/workflow-capability-target-field.test.tsx), [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx), [web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx](../../web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-019` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [pkg/workflows/trigger_simulation_test.go](../../pkg/workflows/trigger_simulation_test.go), [pkg/workflows/development_test_admission_test.go](../../pkg/workflows/development_test_admission_test.go), [web/backend/api/workflow_event_context_test.go](../../web/backend/api/workflow_event_context_test.go), [web/backend/api/workflow_trigger_simulation_test.go](../../web/backend/api/workflow_trigger_simulation_test.go), [web/frontend/src/api/workflows.test.ts](../../web/frontend/src/api/workflows.test.ts), [web/frontend/src/components/workflows/workflow-trigger-simulator.test.tsx](../../web/frontend/src/components/workflows/workflow-trigger-simulator.test.tsx), [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.test.tsx), [web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx](../../web/frontend/src/components/workflows/workflow-job-builder-integration.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-020` | [pkg/agent/definition_test.go](../../pkg/agent/definition_test.go), [web/backend/api/agent_capabilities_test.go](../../web/backend/api/agent_capabilities_test.go), [web/backend/api/agent_capabilities_cas_test.go](../../web/backend/api/agent_capabilities_cas_test.go), [web/backend/api/agent_capabilities_replace_linux_test.go](../../web/backend/api/agent_capabilities_replace_linux_test.go), [web/backend/api/agent_capabilities_request_test.go](../../web/backend/api/agent_capabilities_request_test.go), [web/backend/api/agent_capabilities_unix_test.go](../../web/backend/api/agent_capabilities_unix_test.go), [pkg/agent/activity_test.go](../../pkg/agent/activity_test.go), [pkg/gateway/agent_activity_test.go](../../pkg/gateway/agent_activity_test.go), [pkg/gateway/listen_test.go](../../pkg/gateway/listen_test.go), [web/backend/api/agent_activity_test.go](../../web/backend/api/agent_activity_test.go), [web/frontend/src/api/agents.test.ts](../../web/frontend/src/api/agents.test.ts) |
| `FR-SEC-021` | [pkg/config/mutation_test.go](../../pkg/config/mutation_test.go), [web/backend/api/config_writer_cas_test.go](../../web/backend/api/config_writer_cas_test.go), [web/backend/api/review_attention_policies_test.go](../../web/backend/api/review_attention_policies_test.go), [web/backend/api/review_attention_agents_test.go](../../web/backend/api/review_attention_agents_test.go), [cmd/picoclaw/internal/auth/config_revision_test.go](../../cmd/picoclaw/internal/auth/config_revision_test.go), [cmd/picoclaw/internal/mcp/command_test.go](../../cmd/picoclaw/internal/mcp/command_test.go), [cmd/picoclaw/internal/model/command_test.go](../../cmd/picoclaw/internal/model/command_test.go), [web/frontend/src/api/review-attention-agents.test.ts](../../web/frontend/src/api/review-attention-agents.test.ts), [web/frontend/src/api/review-attention-json.test.ts](../../web/frontend/src/api/review-attention-json.test.ts), [web/frontend/src/api/review-attention-policies.test.ts](../../web/frontend/src/api/review-attention-policies.test.ts), [web/frontend/src/components/reviews/review-attention-policy-model.test.ts](../../web/frontend/src/components/reviews/review-attention-policy-model.test.ts), [web/frontend/src/components/reviews/review-attention-policies-page.test.tsx](../../web/frontend/src/components/reviews/review-attention-policies-page.test.tsx), [web/frontend/src/components/reviews/reviews-page.test.tsx](../../web/frontend/src/components/reviews/reviews-page.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts) |
| `FR-SEC-022` | [pkg/media/store_test.go](../../pkg/media/store_test.go), [pkg/media/snapshot_test.go](../../pkg/media/snapshot_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go), [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go), [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go) |
| `FR-SEC-023` | [pkg/workflows/private_context.go](../../pkg/workflows/private_context.go), [pkg/workflows/private_session.go](../../pkg/workflows/private_session.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go), [pkg/workflows/store.go](../../pkg/workflows/store.go), [pkg/workflows/development.go](../../pkg/workflows/development.go), [pkg/workflows/gates_test.go](../../pkg/workflows/gates_test.go), [pkg/workflows/private_context_security_test.go](../../pkg/workflows/private_context_security_test.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/accountrouter/router.go](../../pkg/accountrouter/router.go), [pkg/accountrouter/router_test.go](../../pkg/accountrouter/router_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/tools/workflow.go](../../pkg/tools/workflow.go), [pkg/reviews/attention_test.go](../../pkg/reviews/attention_test.go), [pkg/eventing/review_decision_run_sqlite_test.go](../../pkg/eventing/review_decision_run_sqlite_test.go), [pkg/eventing/review_attention_trigger_sqlite_test.go](../../pkg/eventing/review_attention_trigger_sqlite_test.go), [pkg/gateway/review_attention_trigger_test.go](../../pkg/gateway/review_attention_trigger_test.go), [web/backend/api/workflows.go](../../web/backend/api/workflows.go), [web/backend/api/workflow_run_readiness_test.go](../../web/backend/api/workflow_run_readiness_test.go), [web/backend/api/workflow_runtime.go](../../web/backend/api/workflow_runtime.go), [web/backend/api/workflow_runtime_test.go](../../web/backend/api/workflow_runtime_test.go) |
| `FR-SEC-024` | [pkg/reviews/attention_bridge.go](../../pkg/reviews/attention_bridge.go), [pkg/reviews/attention_bridge_test.go](../../pkg/reviews/attention_bridge_test.go), [pkg/reviews/attention_bridge_sqlite_test.go](../../pkg/reviews/attention_bridge_sqlite_test.go), [pkg/reviews/workflow_retention.go](../../pkg/reviews/workflow_retention.go), [pkg/gateway/review_attention_bridge_test.go](../../pkg/gateway/review_attention_bridge_test.go), [web/backend/api/reviews_test.go](../../web/backend/api/reviews_test.go), [web/backend/api/agent_activity.go](../../web/backend/api/agent_activity.go), [web/backend/api/workflow_attention_privacy.go](../../web/backend/api/workflow_attention_privacy.go), [web/backend/api/review_attention_workflow_suppression_test.go](../../web/backend/api/review_attention_workflow_suppression_test.go), [cmd/picoclaw/internal/workflow/retention_test.go](../../cmd/picoclaw/internal/workflow/retention_test.go), [web/frontend/src/api/review-attention.test.ts](../../web/frontend/src/api/review-attention.test.ts), [web/frontend/src/components/reviews/reviews-page.test.tsx](../../web/frontend/src/components/reviews/reviews-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts) |
| `FR-SEC-025` | [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/eventing/operator/pr_development_delegation_test.go](../../pkg/eventing/operator/pr_development_delegation_test.go), [web/backend/main.go](../../web/backend/main.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/backend/middleware/launcher_dashboard_auth_test.go](../../web/backend/middleware/launcher_dashboard_auth_test.go), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-026` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_conversation_schema_sqlite.go](../../pkg/eventing/pr_development_conversation_schema_sqlite.go), [pkg/eventing/pr_development_conversation_store_sqlite.go](../../pkg/eventing/pr_development_conversation_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/review_store_sqlite.go](../../pkg/eventing/review_store_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/pr_development_conversation_store_sqlite_test.go](../../pkg/eventing/pr_development_conversation_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/prdevelopment/chat_test.go](../../pkg/prdevelopment/chat_test.go), [pkg/workflows/context.go](../../pkg/workflows/context.go), [pkg/agent/agent.go](../../pkg/agent/agent.go), [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/channels/manager.go](../../pkg/channels/manager.go), [pkg/channels/manager_test.go](../../pkg/channels/manager_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [web/backend/api/reviews.go](../../web/backend/api/reviews.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/i18n/locales/en.json](../../web/frontend/src/i18n/locales/en.json), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-027` | [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/github_case_test.go](../../pkg/prdevelopment/github_case_test.go), [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go), [pkg/tools/toolloop_test.go](../../pkg/tools/toolloop_test.go), [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go), [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-SEC-028` | [pkg/eventing/pr_development_repair_schema_sqlite.go](../../pkg/eventing/pr_development_repair_schema_sqlite.go), [pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go), [pkg/eventing/pr_development_repair_store_sqlite_test.go](../../pkg/eventing/pr_development_repair_store_sqlite_test.go), [pkg/prdevelopment/repair_worker.go](../../pkg/prdevelopment/repair_worker.go), [pkg/prdevelopment/repair_worker_test.go](../../pkg/prdevelopment/repair_worker_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go), [pkg/agent/local_repair_factory.go](../../pkg/agent/local_repair_factory.go), [pkg/agent/local_repair_factory_test.go](../../pkg/agent/local_repair_factory_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [pkg/gateway/pr_development_repair_runtime_test.go](../../pkg/gateway/pr_development_repair_runtime_test.go), [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-029` | [pkg/gitworkspace/pinned_commit.go](../../pkg/gitworkspace/pinned_commit.go), [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go), [pkg/agent/local_repair_test.go](../../pkg/agent/local_repair_test.go), [pkg/tools/integration/git_workspace_test.go](../../pkg/tools/integration/git_workspace_test.go) |
| `FR-SEC-030` | [pkg/eventing/webhook/github.go](../../pkg/eventing/webhook/github.go), [pkg/eventing/webhook/github_test.go](../../pkg/eventing/webhook/github_test.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_thread_schema_sqlite.go](../../pkg/eventing/pr_development_thread_schema_sqlite.go), [pkg/eventing/pr_development_thread_store_sqlite.go](../../pkg/eventing/pr_development_thread_store_sqlite.go), [pkg/eventing/pr_development_thread_store_sqlite_test.go](../../pkg/eventing/pr_development_thread_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/prdevelopment/capture.go](../../pkg/prdevelopment/capture.go), [pkg/prdevelopment/capture_test.go](../../pkg/prdevelopment/capture_test.go), [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/github_case_test.go](../../pkg/prdevelopment/github_case_test.go), [pkg/gateway/pr_development_capture_test.go](../../pkg/gateway/pr_development_capture_test.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts) |
| `FR-SEC-031` | [pkg/gitworkspace/development_line_test.go](../../pkg/gitworkspace/development_line_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/development_line_review_test.go](../../pkg/gitworkspace/development_line_review_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-SEC-032` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_controller_schema_sqlite.go](../../pkg/eventing/pr_development_controller_schema_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |
| `FR-SEC-033` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_ledger_schema_sqlite.go](../../pkg/eventing/pr_development_ledger_schema_sqlite.go), [pkg/eventing/pr_development_ledger_store_sqlite.go](../../pkg/eventing/pr_development_ledger_store_sqlite.go), [pkg/eventing/pr_development_ledger_store_sqlite_test.go](../../pkg/eventing/pr_development_ledger_store_sqlite_test.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/prdevelopment/thread_context.go](../../pkg/prdevelopment/thread_context.go), [pkg/prdevelopment/thread_context_test.go](../../pkg/prdevelopment/thread_context_test.go), [pkg/eventing/pr_development_review_store_sqlite_test.go](../../pkg/eventing/pr_development_review_store_sqlite_test.go), [pkg/prdevelopment/review_worker_test.go](../../pkg/prdevelopment/review_worker_test.go), [pkg/agent/controller_local_review_test.go](../../pkg/agent/controller_local_review_test.go), [pkg/gateway/pr_development_repair_runtime_test.go](../../pkg/gateway/pr_development_repair_runtime_test.go) |
| `FR-SEC-034` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_recovery_schema_sqlite.go](../../pkg/eventing/pr_development_recovery_schema_sqlite.go), [pkg/eventing/pr_development_recovery_store_sqlite.go](../../pkg/eventing/pr_development_recovery_store_sqlite.go), [pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go), [pkg/eventing/pr_development_recovery_store_sqlite_test.go](../../pkg/eventing/pr_development_recovery_store_sqlite_test.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-SEC-035` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_operation_schema_sqlite.go](../../pkg/eventing/pr_development_operation_schema_sqlite.go), [pkg/eventing/pr_development_operation_codec_sqlite.go](../../pkg/eventing/pr_development_operation_codec_sqlite.go), [pkg/eventing/pr_development_operation_store_sqlite.go](../../pkg/eventing/pr_development_operation_store_sqlite.go), [pkg/eventing/pr_development_operation_recovery_store_sqlite.go](../../pkg/eventing/pr_development_operation_recovery_store_sqlite.go), [pkg/eventing/pr_development_operation_validation_sqlite.go](../../pkg/eventing/pr_development_operation_validation_sqlite.go), [pkg/eventing/pr_development_operation_store_sqlite_test.go](../../pkg/eventing/pr_development_operation_store_sqlite_test.go), [pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go), [pkg/eventing/pr_development_controller_store_sqlite_test.go](../../pkg/eventing/pr_development_controller_store_sqlite_test.go), [pkg/eventing/pr_development_recovery_store_sqlite.go](../../pkg/eventing/pr_development_recovery_store_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/gitworkspace/pinned_line_recovery.go](../../pkg/gitworkspace/pinned_line_recovery.go), [pkg/gitworkspace/pinned_line_recovery_test.go](../../pkg/gitworkspace/pinned_line_recovery_test.go), [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/development_line.go](../../pkg/gitworkspace/development_line.go), [pkg/gitworkspace/development_line_test.go](../../pkg/gitworkspace/development_line_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/pinned_commit.go](../../pkg/gitworkspace/pinned_commit.go), [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-SEC-036` | [pkg/prdevelopment/localci](../../pkg/prdevelopment/localci), [pkg/gitworkspace/pinned_validation_roots.go](../../pkg/gitworkspace/pinned_validation_roots.go), [pkg/gitworkspace/pinned_validation_roots_change.go](../../pkg/gitworkspace/pinned_validation_roots_change.go), [pkg/gitworkspace/pinned_validation_roots_change_ctim.go](../../pkg/gitworkspace/pinned_validation_roots_change_ctim.go), [pkg/gitworkspace/pinned_validation_roots_test.go](../../pkg/gitworkspace/pinned_validation_roots_test.go) |
| `FR-SEC-037` | [pkg/attention/private_run_test.go](../../pkg/attention/private_run_test.go), [pkg/eventing/pr_development_attention_store_sqlite_test.go](../../pkg/eventing/pr_development_attention_store_sqlite_test.go), [pkg/prdevelopment/attention_test.go](../../pkg/prdevelopment/attention_test.go), [pkg/gateway/pr_development_attention_composition_test.go](../../pkg/gateway/pr_development_attention_composition_test.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go) |
| `FR-SEC-038` | [pkg/eventing/pr_development_attention_trigger_store_sqlite_test.go](../../pkg/eventing/pr_development_attention_trigger_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/attention/private_run_test.go](../../pkg/attention/private_run_test.go), [pkg/attention/conversation_test.go](../../pkg/attention/conversation_test.go), [pkg/prdevelopment/attention_trigger_worker_test.go](../../pkg/prdevelopment/attention_trigger_worker_test.go), [pkg/prdevelopment/attention_bridge_test.go](../../pkg/prdevelopment/attention_bridge_test.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [pkg/gateway/pr_development_attention_composition_test.go](../../pkg/gateway/pr_development_attention_composition_test.go), [web/backend/api/pr_development_test.go](../../web/backend/api/pr_development_test.go), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/api/pr-development-attention.test.ts](../../web/frontend/src/api/pr-development-attention.test.ts), [web/frontend/src/components/reviews/attention-conversation.test.tsx](../../web/frontend/src/components/reviews/attention-conversation.test.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/routes/-reviews-route.test.tsx](../../web/frontend/src/routes/-reviews-route.test.tsx), [web/frontend/src/routes/-reviews.test.ts](../../web/frontend/src/routes/-reviews.test.ts), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-039` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go), [pkg/eventing/pr_development_ledger_store_sqlite.go](../../pkg/eventing/pr_development_ledger_store_sqlite.go), [pkg/eventing/pr_development_orchestration_store_sqlite.go](../../pkg/eventing/pr_development_orchestration_store_sqlite.go), [pkg/eventing/pr_development_review_store_sqlite_test.go](../../pkg/eventing/pr_development_review_store_sqlite_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts), [web/frontend/src/api/pr-development.test.ts](../../web/frontend/src/api/pr-development.test.ts), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-040` | [pkg/gitworkspace/development_line_suspension.go](../../pkg/gitworkspace/development_line_suspension.go), [pkg/gitworkspace/development_line_suspension_api.go](../../pkg/gitworkspace/development_line_suspension_api.go), [pkg/gitworkspace/development_line_suspension_test.go](../../pkg/gitworkspace/development_line_suspension_test.go), [pkg/gitworkspace/development_line_suspension_api_test.go](../../pkg/gitworkspace/development_line_suspension_api_test.go), [pkg/gitworkspace/development_line_suspension_matrix_test.go](../../pkg/gitworkspace/development_line_suspension_matrix_test.go), [pkg/gitworkspace/development_line_suspension_adversarial_test.go](../../pkg/gitworkspace/development_line_suspension_adversarial_test.go), [pkg/gitworkspace/development_line_adversarial_test.go](../../pkg/gitworkspace/development_line_adversarial_test.go), [pkg/gitworkspace/pinned_commit.go](../../pkg/gitworkspace/pinned_commit.go), [pkg/gitworkspace/pinned_commit_test.go](../../pkg/gitworkspace/pinned_commit_test.go), [pkg/gitworkspace/pinned_reservation_rotation.go](../../pkg/gitworkspace/pinned_reservation_rotation.go), [pkg/gitworkspace/pinned_reservation_rotation_test.go](../../pkg/gitworkspace/pinned_reservation_rotation_test.go), [pkg/gitworkspace/manager_test.go](../../pkg/gitworkspace/manager_test.go) |
| `FR-SEC-041` | [pkg/eventing/pr_development_suspension_store_sqlite_test.go](../../pkg/eventing/pr_development_suspension_store_sqlite_test.go), [pkg/eventing/pr_development_suspended_resume_store_sqlite_test.go](../../pkg/eventing/pr_development_suspended_resume_store_sqlite_test.go), [pkg/eventing/pr_development_suspended_resume_recovery_store_sqlite_test.go](../../pkg/eventing/pr_development_suspended_resume_recovery_store_sqlite_test.go), [pkg/eventing/pr_development_recovery_queue_sqlite_test.go](../../pkg/eventing/pr_development_recovery_queue_sqlite_test.go), [pkg/gitworkspace/pinned_recovery_suspension_capacity_test.go](../../pkg/gitworkspace/pinned_recovery_suspension_capacity_test.go), [pkg/prdevelopment/controller_recovery_worker_test.go](../../pkg/prdevelopment/controller_recovery_worker_test.go), [pkg/prdevelopment/repair_controller_worker_test.go](../../pkg/prdevelopment/repair_controller_worker_test.go), [pkg/gateway/pr_development_repair_runtime_test.go](../../pkg/gateway/pr_development_repair_runtime_test.go) |
| `FR-SEC-042` | [pkg/gitworkspace/development_line_push.go](../../pkg/gitworkspace/development_line_push.go), [pkg/gitworkspace/development_line_push_test.go](../../pkg/gitworkspace/development_line_push_test.go) |
| `FR-SEC-043` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_schema_sqlite.go](../../pkg/eventing/pr_development_publication_schema_sqlite.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_store_sqlite_test.go), [pkg/eventing/pr_development_publication_renewal_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_renewal_store_sqlite_test.go), [pkg/eventing/pr_development_publication_admission_store_sqlite.go](../../pkg/eventing/pr_development_publication_admission_store_sqlite.go), [pkg/eventing/pr_development_publication_admission_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_admission_store_sqlite_test.go), [pkg/eventing/pr_development_ledger_store_sqlite.go](../../pkg/eventing/pr_development_ledger_store_sqlite.go), [pkg/eventing/pr_development_review_store_sqlite_test.go](../../pkg/eventing/pr_development_review_store_sqlite_test.go), [pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go), [pkg/eventing/pr_development_controller_store_sqlite_test.go](../../pkg/eventing/pr_development_controller_store_sqlite_test.go), [pkg/eventing/pr_development_orchestration_store_sqlite.go](../../pkg/eventing/pr_development_orchestration_store_sqlite.go), [pkg/eventing/store_sqlite.go](../../pkg/eventing/store_sqlite.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_schema_test.go](../../pkg/eventing/store_schema_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |
| `FR-SEC-044` | [pkg/prdevelopment/publication_provider.go](../../pkg/prdevelopment/publication_provider.go), [pkg/prdevelopment/publication_provider_test.go](../../pkg/prdevelopment/publication_provider_test.go), [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go), [pkg/prdevelopment/github_case_test.go](../../pkg/prdevelopment/github_case_test.go) |
| `FR-SEC-045` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_gate_context_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_gate_context_store_sqlite_test.go), [pkg/eventing/pr_development_publication_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_store_sqlite_test.go), [pkg/eventing/store_types.go](../../pkg/eventing/store_types.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |
| `FR-SEC-046` | [pkg/prdevelopment/publication_gate_processor.go](../../pkg/prdevelopment/publication_gate_processor.go), [pkg/prdevelopment/publication_gate_processor_test.go](../../pkg/prdevelopment/publication_gate_processor_test.go), [pkg/attention/policy.go](../../pkg/attention/policy.go), [pkg/workflows/gates.go](../../pkg/workflows/gates.go), [pkg/prdevelopment/publication_provider.go](../../pkg/prdevelopment/publication_provider.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_gate_claim_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_gate_claim_store_sqlite_test.go), [pkg/eventing/pr_development_publication_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_store_sqlite_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |
| `FR-SEC-047` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_requeue_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_requeue_store_sqlite_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/prdevelopment/publication_retry.go](../../pkg/prdevelopment/publication_retry.go), [pkg/prdevelopment/publication_retry_test.go](../../pkg/prdevelopment/publication_retry_test.go), [pkg/prdevelopment/attention_trigger_worker.go](../../pkg/prdevelopment/attention_trigger_worker.go) |
| `FR-SEC-048` | [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_attention_trigger_store_sqlite.go](../../pkg/eventing/pr_development_attention_trigger_store_sqlite.go), [pkg/eventing/pr_development_store_sqlite.go](../../pkg/eventing/pr_development_store_sqlite.go), [pkg/eventing/pr_development_publication_attention_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_attention_store_sqlite_test.go), [pkg/eventing/pr_development_attention_trigger_store_sqlite_test.go](../../pkg/eventing/pr_development_attention_trigger_store_sqlite_test.go), [pkg/eventing/pr_development_store_sqlite_test.go](../../pkg/eventing/pr_development_store_sqlite_test.go), [pkg/prdevelopment/publication_decision.go](../../pkg/prdevelopment/publication_decision.go), [pkg/prdevelopment/publication_decision_test.go](../../pkg/prdevelopment/publication_decision_test.go), [pkg/prdevelopment/attention_bridge.go](../../pkg/prdevelopment/attention_bridge.go), [pkg/prdevelopment/attention_bridge_test.go](../../pkg/prdevelopment/attention_bridge_test.go), [pkg/prdevelopment/publication_attention_bridge_test.go](../../pkg/prdevelopment/publication_attention_bridge_test.go), [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go), [pkg/prdevelopment/service_handler_test.go](../../pkg/prdevelopment/service_handler_test.go), [web/frontend/src/components/reviews/attention-conversation.tsx](../../web/frontend/src/components/reviews/attention-conversation.tsx), [web/frontend/src/components/reviews/attention-conversation.test.tsx](../../web/frontend/src/components/reviews/attention-conversation.test.tsx), [web/frontend/src/components/reviews/pr-development-page.tsx](../../web/frontend/src/components/reviews/pr-development-page.tsx), [web/frontend/src/components/reviews/pr-development-page.test.tsx](../../web/frontend/src/components/reviews/pr-development-page.test.tsx), [web/frontend/src/i18n/locales/en.json](../../web/frontend/src/i18n/locales/en.json), [web/frontend/tests/ui-smoke.spec.ts](../../web/frontend/tests/ui-smoke.spec.ts) |
| `FR-SEC-049` | [pkg/prdevelopment/publication_gate_processor.go](../../pkg/prdevelopment/publication_gate_processor.go), [pkg/prdevelopment/publication_gate_processor_test.go](../../pkg/prdevelopment/publication_gate_processor_test.go), [pkg/prdevelopment/publication_gate_executor.go](../../pkg/prdevelopment/publication_gate_executor.go), [pkg/prdevelopment/publication_gate_executor_test.go](../../pkg/prdevelopment/publication_gate_executor_test.go), [pkg/prdevelopment/publication_gate_handler.go](../../pkg/prdevelopment/publication_gate_handler.go), [pkg/prdevelopment/publication_gate_handler_test.go](../../pkg/prdevelopment/publication_gate_handler_test.go), [pkg/prdevelopment/publication_dispatcher.go](../../pkg/prdevelopment/publication_dispatcher.go), [pkg/prdevelopment/publication_dispatcher_test.go](../../pkg/prdevelopment/publication_dispatcher_test.go), [pkg/prdevelopment/attention_context.go](../../pkg/prdevelopment/attention_context.go), [pkg/prdevelopment/attention_test.go](../../pkg/prdevelopment/attention_test.go), [pkg/prdevelopment/publication_decision.go](../../pkg/prdevelopment/publication_decision.go), [pkg/prdevelopment/publication_decision_test.go](../../pkg/prdevelopment/publication_decision_test.go), [pkg/attention/private_run.go](../../pkg/attention/private_run.go), [pkg/attention/private_run_test.go](../../pkg/attention/private_run_test.go), [pkg/workflows/gates.go](../../pkg/workflows/gates.go), [pkg/workflows/gates_test.go](../../pkg/workflows/gates_test.go), [pkg/workflows/private_context_security_test.go](../../pkg/workflows/private_context_security_test.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_gate_context_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_gate_context_store_sqlite_test.go), [pkg/eventing/pr_development_publication_requeue_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_requeue_store_sqlite_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |

| `FR-SEC-050` | [pkg/prdevelopment/publication_push_handler.go](../../pkg/prdevelopment/publication_push_handler.go), [pkg/prdevelopment/publication_push_handler_test.go](../../pkg/prdevelopment/publication_push_handler_test.go), [pkg/prdevelopment/publication_push_handler_sqlite_test.go](../../pkg/prdevelopment/publication_push_handler_sqlite_test.go), [pkg/prdevelopment/publication_push_heartbeat.go](../../pkg/prdevelopment/publication_push_heartbeat.go), [pkg/prdevelopment/publication_gate_handler.go](../../pkg/prdevelopment/publication_gate_handler.go), [pkg/prdevelopment/publication_gate_handler_test.go](../../pkg/prdevelopment/publication_gate_handler_test.go), [pkg/prdevelopment/publication_retry.go](../../pkg/prdevelopment/publication_retry.go), [pkg/prdevelopment/publication_retry_test.go](../../pkg/prdevelopment/publication_retry_test.go), [pkg/prdevelopment/publication_dispatcher.go](../../pkg/prdevelopment/publication_dispatcher.go), [pkg/prdevelopment/publication_dispatcher_test.go](../../pkg/prdevelopment/publication_dispatcher_test.go), [pkg/prdevelopment/publication_provider.go](../../pkg/prdevelopment/publication_provider.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_push_claim_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_push_claim_store_sqlite_test.go), [pkg/eventing/pr_development_publication_requeue_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_requeue_store_sqlite_test.go), [pkg/eventing/pr_development_publication_renewal_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_renewal_store_sqlite_test.go), [pkg/eventing/pr_development_publication_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_store_sqlite_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go), [pkg/gitworkspace/development_line_push.go](../../pkg/gitworkspace/development_line_push.go), [pkg/gitworkspace/development_line_push_test.go](../../pkg/gitworkspace/development_line_push_test.go) |
| `FR-SEC-051` | [pkg/prdevelopment/publication_outcome_reconciliation_worker.go](../../pkg/prdevelopment/publication_outcome_reconciliation_worker.go), [pkg/prdevelopment/publication_outcome_reconciliation_worker_test.go](../../pkg/prdevelopment/publication_outcome_reconciliation_worker_test.go), [pkg/prdevelopment/publication_outcome_reconciliation_worker_sqlite_test.go](../../pkg/prdevelopment/publication_outcome_reconciliation_worker_sqlite_test.go), [pkg/prdevelopment/publication_provider.go](../../pkg/prdevelopment/publication_provider.go), [pkg/prdevelopment/publication_provider_test.go](../../pkg/prdevelopment/publication_provider_test.go), [pkg/prdevelopment/publication_retry.go](../../pkg/prdevelopment/publication_retry.go), [pkg/prdevelopment/publication_retry_test.go](../../pkg/prdevelopment/publication_retry_test.go), [pkg/eventing/pr_development_types.go](../../pkg/eventing/pr_development_types.go), [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go), [pkg/eventing/pr_development_publication_unknown_outcome_store_sqlite_test.go](../../pkg/eventing/pr_development_publication_unknown_outcome_store_sqlite_test.go), [pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go), [pkg/eventing/store_unsupported_test.go](../../pkg/eventing/store_unsupported_test.go) |
| `FR-SEC-052` | [pkg/prdevelopment/publication_worker.go](../../pkg/prdevelopment/publication_worker.go), [pkg/prdevelopment/publication_worker_test.go](../../pkg/prdevelopment/publication_worker_test.go), [pkg/prdevelopment/publication_worker_sqlite_test.go](../../pkg/prdevelopment/publication_worker_sqlite_test.go), [pkg/prdevelopment/publication_dispatcher.go](../../pkg/prdevelopment/publication_dispatcher.go), [pkg/prdevelopment/publication_dispatcher_test.go](../../pkg/prdevelopment/publication_dispatcher_test.go), [pkg/prdevelopment/publication_gate_handler.go](../../pkg/prdevelopment/publication_gate_handler.go), [pkg/prdevelopment/publication_push_handler.go](../../pkg/prdevelopment/publication_push_handler.go), [pkg/prdevelopment/publication_outcome_reconciliation_worker.go](../../pkg/prdevelopment/publication_outcome_reconciliation_worker.go), [pkg/prdevelopment/publication_provider.go](../../pkg/prdevelopment/publication_provider.go), [pkg/gateway/pr_development_publication_runtime.go](../../pkg/gateway/pr_development_publication_runtime.go), [pkg/gateway/pr_development_publication_runtime_test.go](../../pkg/gateway/pr_development_publication_runtime_test.go), [pkg/gateway/event_automation.go](../../pkg/gateway/event_automation.go), [pkg/gateway/event_automation_test.go](../../pkg/gateway/event_automation_test.go), [pkg/agent/runtime_gate.go](../../pkg/agent/runtime_gate.go), [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go) |

Additional `FR-SEC-030` acceptance anchors are
[pkg/eventing/store_types.go](../../pkg/eventing/store_types.go),
[pkg/eventing/store_unsupported.go](../../pkg/eventing/store_unsupported.go),
[pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go),
and [pkg/prdevelopment/repair_worker.go](../../pkg/prdevelopment/repair_worker.go).

Additional `FR-SEC-032` implementation and acceptance anchors are
[pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go),
[pkg/eventing/pr_development_controller_store_sqlite_test.go](../../pkg/eventing/pr_development_controller_store_sqlite_test.go),
[pkg/eventing/pr_development_repair_store_sqlite.go](../../pkg/eventing/pr_development_repair_store_sqlite.go),
and [pkg/eventing/pr_development_repair_store_sqlite_test.go](../../pkg/eventing/pr_development_repair_store_sqlite_test.go).

`FR-SEC-033` additionally relies on the authenticated controller-fence
validation in
[pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go).

## Implementation Anchors

- [pkg/config/config_struct.go](../../pkg/config/config_struct.go)
- [pkg/config/config.go](../../pkg/config/config.go)
- [pkg/config/model_selection.go](../../pkg/config/model_selection.go)
- [pkg/config/model_selection_compatibility.go](../../pkg/config/model_selection_compatibility.go)
- [pkg/config/mutation.go](../../pkg/config/mutation.go)
- [pkg/config/events.go](../../pkg/config/events.go)
- [pkg/prdevelopment/capture.go](../../pkg/prdevelopment/capture.go)
- [pkg/prdevelopment/github.go](../../pkg/prdevelopment/github.go)
- [pkg/prdevelopment/service.go](../../pkg/prdevelopment/service.go)
- [pkg/prdevelopment/handler.go](../../pkg/prdevelopment/handler.go)
- [pkg/prdevelopment/thread_context.go](../../pkg/prdevelopment/thread_context.go)
- [pkg/prdevelopment/attention.go](../../pkg/prdevelopment/attention.go)
- [pkg/prdevelopment/attention_context.go](../../pkg/prdevelopment/attention_context.go)
- [pkg/prdevelopment/publication_gate_executor.go](../../pkg/prdevelopment/publication_gate_executor.go)
- [pkg/prdevelopment/publication_gate_handler.go](../../pkg/prdevelopment/publication_gate_handler.go)
- [pkg/prdevelopment/publication_provider.go](../../pkg/prdevelopment/publication_provider.go)
- [pkg/prdevelopment/publication_retry.go](../../pkg/prdevelopment/publication_retry.go)
- [pkg/prdevelopment/publication_push_handler.go](../../pkg/prdevelopment/publication_push_handler.go)
- [pkg/prdevelopment/publication_push_heartbeat.go](../../pkg/prdevelopment/publication_push_heartbeat.go)
- [pkg/prdevelopment/publication_dispatcher.go](../../pkg/prdevelopment/publication_dispatcher.go)
- [pkg/prdevelopment/localci](../../pkg/prdevelopment/localci)
- [pkg/eventing/pr_development_conversation_schema_sqlite.go](../../pkg/eventing/pr_development_conversation_schema_sqlite.go)
- [pkg/eventing/pr_development_ledger_schema_sqlite.go](../../pkg/eventing/pr_development_ledger_schema_sqlite.go)
- [pkg/eventing/pr_development_ledger_store_sqlite.go](../../pkg/eventing/pr_development_ledger_store_sqlite.go)
- [pkg/eventing/pr_development_review_store_sqlite.go](../../pkg/eventing/pr_development_review_store_sqlite.go)
- [pkg/eventing/pr_development_publication_schema_sqlite.go](../../pkg/eventing/pr_development_publication_schema_sqlite.go)
- [pkg/eventing/pr_development_publication_admission_store_sqlite.go](../../pkg/eventing/pr_development_publication_admission_store_sqlite.go)
- [pkg/eventing/pr_development_publication_store_sqlite.go](../../pkg/eventing/pr_development_publication_store_sqlite.go)
- [pkg/eventing/pr_development_controller_schema_sqlite.go](../../pkg/eventing/pr_development_controller_schema_sqlite.go)
- [pkg/eventing/pr_development_controller_store_sqlite.go](../../pkg/eventing/pr_development_controller_store_sqlite.go)
- [pkg/eventing/pr_development_recovery_schema_sqlite.go](../../pkg/eventing/pr_development_recovery_schema_sqlite.go)
- [pkg/eventing/pr_development_recovery_store_sqlite.go](../../pkg/eventing/pr_development_recovery_store_sqlite.go)
- [pkg/eventing/pr_development_attention_schema_sqlite.go](../../pkg/eventing/pr_development_attention_schema_sqlite.go)
- [pkg/eventing/pr_development_attention_store_sqlite.go](../../pkg/eventing/pr_development_attention_store_sqlite.go)
- [pkg/eventing/pr_development_conversation_store_sqlite.go](../../pkg/eventing/pr_development_conversation_store_sqlite.go)
- [pkg/agent/agent.go](../../pkg/agent/agent.go)
- [pkg/agent/local_repair.go](../../pkg/agent/local_repair.go)
- [pkg/agent/controller_local_review.go](../../pkg/agent/controller_local_review.go)
- [pkg/prdevelopment/review_worker.go](../../pkg/prdevelopment/review_worker.go)
- [pkg/agent/prompt_turn.go](../../pkg/agent/prompt_turn.go)
- [pkg/tools/toolloop.go](../../pkg/tools/toolloop.go)
- [pkg/tools/apply_patch.go](../../pkg/tools/apply_patch.go)
- [pkg/gitworkspace/manager.go](../../pkg/gitworkspace/manager.go)
- [pkg/gitworkspace/development_line.go](../../pkg/gitworkspace/development_line.go)
- [pkg/gitworkspace/development_line_push.go](../../pkg/gitworkspace/development_line_push.go)
- [pkg/gitworkspace/development_line_suspension.go](../../pkg/gitworkspace/development_line_suspension.go)
- [pkg/gitworkspace/development_line_suspension_api.go](../../pkg/gitworkspace/development_line_suspension_api.go)
- [pkg/gitworkspace/pinned_validation_roots.go](../../pkg/gitworkspace/pinned_validation_roots.go)
- [pkg/channels/manager.go](../../pkg/channels/manager.go)
- [pkg/attention](../../pkg/attention)
- [pkg/gateway/pr_development_attention.go](../../pkg/gateway/pr_development_attention.go)
- [web/backend/api/pr_development.go](../../web/backend/api/pr_development.go)
- [web/backend/main.go](../../web/backend/main.go)
- [web/backend/api/config.go](../../web/backend/api/config.go)
- [web/backend/api/review_attention_policies.go](../../web/backend/api/review_attention_policies.go)
- [web/frontend/src/api/review-attention-json.ts](../../web/frontend/src/api/review-attention-json.ts)
- [web/frontend/src/api/review-attention.ts](../../web/frontend/src/api/review-attention.ts)
- [web/frontend/src/api/pr-development.ts](../../web/frontend/src/api/pr-development.ts)
- [web/frontend/src/components/reviews](../../web/frontend/src/components/reviews)
- [web/frontend/src/i18n/locales/en.json](../../web/frontend/src/i18n/locales/en.json)
- [pkg/auth/oauth.go](../../pkg/auth/oauth.go)
- [pkg/auth/store.go](../../pkg/auth/store.go)
- [pkg/mcp/network.go](../../pkg/mcp/network.go)
- [pkg/mcp/oauth.go](../../pkg/mcp/oauth.go)
- [pkg/credential](../../pkg/credential)
- [pkg/fileutil](../../pkg/fileutil)
- [pkg/media/store.go](../../pkg/media/store.go)
- [pkg/media/snapshot.go](../../pkg/media/snapshot.go)
- [pkg/media/snapshot_file.go](../../pkg/media/snapshot_file.go)
- [pkg/media/snapshot_file_unix.go](../../pkg/media/snapshot_file_unix.go)
- [pkg/media/snapshot_file_windows.go](../../pkg/media/snapshot_file_windows.go)
- [pkg/media/snapshot_file_other.go](../../pkg/media/snapshot_file_other.go)
- [pkg/media/frozen.go](../../pkg/media/frozen.go)
- [pkg/session/frozen_media.go](../../pkg/session/frozen_media.go)
- [pkg/isolation](../../pkg/isolation)
- [pkg/workflows/inspection.go](../../pkg/workflows/inspection.go)
- [pkg/workflows/inspection_open_unix.go](../../pkg/workflows/inspection_open_unix.go)
- [pkg/workflows/inspection_open_other.go](../../pkg/workflows/inspection_open_other.go)
- [pkg/workflows/authoring_capabilities.go](../../pkg/workflows/authoring_capabilities.go)
- [pkg/workflows/editor_jobs.go](../../pkg/workflows/editor_jobs.go)
- [pkg/workflows/trigger_simulation.go](../../pkg/workflows/trigger_simulation.go)
- [pkg/workflows/development_test_admission.go](../../pkg/workflows/development_test_admission.go)
- [pkg/workflows/context.go](../../pkg/workflows/context.go)
- [pkg/workflows/private_context.go](../../pkg/workflows/private_context.go)
- [pkg/workflows/private_session.go](../../pkg/workflows/private_session.go)
- [pkg/workflows/executor.go](../../pkg/workflows/executor.go)
- [pkg/workflows/store.go](../../pkg/workflows/store.go)
- [pkg/reviews/attention_bridge.go](../../pkg/reviews/attention_bridge.go)
- [pkg/accountrouter/router.go](../../pkg/accountrouter/router.go)
- [pkg/tools/workflow.go](../../pkg/tools/workflow.go)
- [pkg/agent/workflow_authoring.go](../../pkg/agent/workflow_authoring.go)
- [pkg/gateway/workflow_authoring.go](../../pkg/gateway/workflow_authoring.go)
- [web/backend/api/workflow_inspection.go](../../web/backend/api/workflow_inspection.go)
- [web/backend/api/workflow_authoring.go](../../web/backend/api/workflow_authoring.go)
- [web/backend/api/workflow_jobs_editor.go](../../web/backend/api/workflow_jobs_editor.go)
- [web/backend/api/workflow_event_context.go](../../web/backend/api/workflow_event_context.go)
- [web/backend/api/workflows.go](../../web/backend/api/workflows.go)
- [web/backend/api/reviews.go](../../web/backend/api/reviews.go)
- [web/backend/api/workflow_human_tasks.go](../../web/backend/api/workflow_human_tasks.go)
- [web/backend/api/workflow_trigger_simulation.go](../../web/backend/api/workflow_trigger_simulation.go)
- [web/frontend/src/components/workflows/workflow-capability-catalog.tsx](../../web/frontend/src/components/workflows/workflow-capability-catalog.tsx)
- [web/frontend/src/components/workflows/workflow-job-editor.tsx](../../web/frontend/src/components/workflows/workflow-job-editor.tsx)
- [web/frontend/src/components/workflows/workflow-trigger-simulator.tsx](../../web/frontend/src/components/workflows/workflow-trigger-simulator.tsx)
- [web/frontend/src/components/workflows/workflow-draft-test-review-dialog.tsx](../../web/frontend/src/components/workflows/workflow-draft-test-review-dialog.tsx)
- [web/frontend/src/components/workflows/workflow-definition-inspector.tsx](../../web/frontend/src/components/workflows/workflow-definition-inspector.tsx)
- [pkg/workflows/templates.go](../../pkg/workflows/templates.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
