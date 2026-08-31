# Agent Execution Optimization

## Feature ID

`FR-AGENT-EXECUTION-OPTIMIZATION`

## Behavior Summary

Agent execution optimization lets a workflow author keep a single visible
`agent/*` workflow step while the runtime internally splits the step into
smaller hidden child agent calls. Splits can be by declared workflow scope, by
textual `Tasks:` responsibilities from the selected agent definition, or by
the cartesian product of both. The feature exists only for structured JSON
outputs: child responses are validated against the same output contract,
calibrated against a grouped baseline before the real split run, combined into
one structured result, and persisted with diagnostics about split planning,
calibration, child summaries, model choice, reasoning effort, token estimates,
and estimated cost. When the visible step uses the workflow exact read-only
decision profile, every grouped, calibration, child, fallback, and repair call
uses the same frozen existing-session snapshot and the result carries its
opaque history revision. A compiler-private gate supplies the locator-rewritten
snapshot and strict self-contained `FrozenSet` captured before durable run
creation. The runtime validates the frozen revision before materializing the
embedded bytes, reuses that detached evidence in every branch without live
session or media rereads, uses only a fixed private session marker inside the
step, and relies on the enclosing workflow's private projection to hide all
managed and structured outputs and frozen/materialized media from generic run
observations.
When the visible step instead uses the exact workflow ephemeral profile, every
one of those calls reuses one request-local stateless side-question closure:
there is no session, history, prompt cache, tool authority, or account-router
affinity, and only the fixed ephemeral audit marker reaches the visible result.

Agent execution optimization is generic. It does not know domain semantics such
as code review, planning, data extraction, or summarization. Domain-specific
behavior belongs in the workflow prompt, the agent definition, the workflow
scope items, and the structured output schema.

When a trusted domain supplies an explicit managed assignment plan and durable
checkpoint callback, the executor forwards the already-admitted owning
automation ID, resolved `agent/<id>`, and one-based runtime child ordinal with
the validated output. Those values identify the callback boundary without
teaching generic split planning about repository-review semantics or exposing
them in ordinary managed output.

## Reconstruction Notes

- Similarity target: recreate a workflow-agent execution layer that preserves
  the workflow executor's single-step contract while adding an internal hidden
  split/validate/combine pipeline for large structured agent tasks.
- Core types/functions: `workflows.AgentOutputContract`,
  `workflows.StructuredOutputResult`, `workflows.AgentRequest`,
  `workflows.ParseAgentOutputContract`,
  `workflows.ValidateAgentStructuredOutput`,
  `workflows.CombineStructuredOutputs`, `workflows.CompareStructuredOutputs`,
  `workflowAgentRunner.RunAgent`, `workflowManagedSplitStrategy`,
  `workflowManagedOptions`, `workflowManagedChildPlans`,
  `workflowAgentRunner.runManagedSplit`,
  `workflowAgentRunner.runManagedSplitCalibration`,
  `workflowRunManagedChildren`, `workflowManagedRunChoice`, and
  `workflowAgentRunner.ensureWorkflowManagedProviders`; the
  `workflows.AgentRequest.EphemeralSession` handoff selects the stateless
  request-local call profile before any of those managed branches are planned.
- Runtime ordering: parse `with.output` and `with.managed`, build the normal
  agent message, select a split strategy only when managed mode and structured
  output are both enabled, derive child plans from scope and/or agent tasks,
  run grouped-vs-split calibration without model/effort optimization, fall
  back to one full structured run on calibration failure, initialize candidate
  providers for configured cheaper child aliases, run hidden children with
  bounded parallelism, repair and validate each child JSON result, combine child
  structured outputs through the schema, validate the combined object, and return
  one visible step output with managed diagnostics.
- Structured validation recursively enforces string `pattern` expressions in
  the bounded, linear-time Go RE2 dialect for ordinary, repaired,
  managed-child, and combined output. One
  request-local compiled-pattern cache keeps large arrays bounded; malformed
  pattern declarations fail closed instead of silently disabling the contract.
- Every concurrently dispatched managed child owns a detached output-contract
  validation cache while retaining the same immutable normalized schema.
  Provider-call telemetry counts every dispatch separately from the subset
  returning valid reported usage, including structured-output repairs, so
  missing, estimated, malformed, or overflowed observations cannot become a
  complete aggregate through concurrent combination.
- Non-obvious constraints: managed splitting is disabled without an enabled
  JSON output contract; hidden children must not write to chat history or
  publish intermediate responses; calibration intentionally disables
  optimization to compare only split quality; task splitting uses textual agent
  responsibilities as semantic hints, not workflow DAG steps; model
  optimization is allowed only when candidate price metadata is known from
  managed options or resolved concrete-model metadata; and all managed metadata is diagnostic
  workflow output, not a separate child workflow run graph. Managed splitting
  does not weaken an enclosing exact read-only decision profile: it cannot
  reread or write the session and cannot enable tools for child work. In the
  compiler-private form, planning starts only after the persisted frozen
  snapshot/set revision is validated and media is materialized from embedded
  bytes; no calibration, child, fallback, or repair can reacquire a live-media
  reader. It also
  cannot weaken an enclosing ephemeral profile: every branch remains free of
  session, history, prompt-cache, hook, MCP, tool, and account-affinity state.
The agent-local calibration cache may retain only strategy/hash experience; it
is distinct from workflow prompt caching and never stores an ephemeral
identity or supplied call context.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-AGENT-EXECUTION-OPTIMIZATION-001` | MUST | A workflow `agent/*` step declares `with.output` as `json` or a schema map. | The executor passes an enabled `AgentOutputContract` to the agent runner; the prompt includes instructions to return only JSON and to satisfy the schema when provided; validated output is exposed as `structured`, `structured_json`, `structured_valid`, and `structured_repairs`. | Step outputs persist the parsed structured object and validation metadata in the normal workflow run record. | Unsupported formats fail before the agent call; invalid JSON or schema mismatch is repaired up to `repair_attempts`, then fails the step with `structured_error`. | Downstream workflow steps need a reliable data contract instead of parsing prose. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-002` | MUST | A workflow `agent/*` step declares `with.managed` as `auto`, `true`, or a map. | Managed metadata is included in outputs and records mode, default single-run strategy, agent task count, scope count, estimated prompt tokens, split status, calibration status, and optimization status. | No separate durable child run is created by this layer; diagnostics remain in the visible step outputs. | `false`, `off`, `none`, missing `managed`, or missing structured output contract must run as one normal agent call. | Operators must be able to see whether agent execution optimization was considered even when the step is not split. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-003` | MUST | Managed mode is enabled and the request has an enabled structured output contract. | The runtime selects `scope_split`, `task_split`, `hybrid_split`, or no split from requested strategy aliases and current scope/task sizes or scope token budget. | Strategy selection is in memory only; the selected strategy is persisted later in `managed.strategy`. | Requested `none` disables splitting; requested scope/task/hybrid strategies are ignored if their required dimensions fit configured chunk and token limits; auto chooses hybrid before scope before task. | Splitting should happen only when it can reduce child prompt size without surprising the workflow author. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-004` | MUST | Scope splitting is selected and `with.scope` is a list or `{items: [...]}`. | Scope items are chunked by `max_items_per_chunk` and, when adaptive chunking is enabled, packed up to an internal effective child prompt target derived from the agent context window and adjusted from trusted calibration-cache experience, so small items form larger chunks and oversized items split sooner. Each child prompt contains only its assigned scope chunk while preserving the original scope shape when possible. | `managed.split` records total scope count, child count, child scope counts, max chunk size, adaptive chunking settings, effective/learned token-efficiency diagnostics, bounded parallelism, and hidden-child status. | Empty scope produces no split; non-positive chunk sizes fall back to one chunk; object scope wrappers keep their non-`items` keys; a single item that exceeds the effective target still runs alone. | Large item sets need bounded child prompts without losing workflow-level scope metadata, and small items should not be over-split into high-overhead child calls. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-005` | MUST | Task splitting is selected and the selected agent definition has textual `Tasks:` entries or a previous managed layer assigned tasks in context. | Tasks are chunked by `max_tasks_per_chunk`; each child prompt appends `Assigned textual agent tasks:` with only the assigned task subset. | `managed.split` records total task count and child task counts; each child output records its task count and task labels. | Empty tasks produce no task split; assigned tasks in the request context override full agent-definition tasks to support calibration and nested planning. | Agent responsibilities are a generic semantic split axis independent of workflow DAG structure. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-006` | MUST | Hybrid splitting is selected. | The runtime builds a child plan for every scope-chunk and task-chunk pair; each child receives both its scope chunk and task subset. | `managed.split.child_count` equals `scope_chunk_count * task_chunk_count`; child outputs include labels for both dimensions. | Hybrid is selected only when both dimensions are splittable, or explicitly requested and both are splittable. | Some agent work scales by both inputs and responsibilities, so splitting along only one dimension is insufficient. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-007` | MUST | Calibration is enabled for a selected split strategy. | The runtime builds a bounded sample request, expanding the sample until it exercises at least two child plans when the original request can split; it then runs one grouped baseline, runs split children over the same sample without model or effort optimization, combines the sample children, and compares grouped versus split structured output. | `managed.calibration` records status, match flag, strategy, trials, required matches, sample scope/task counts, repairs, comparison method, baseline object, split-combined object, and calibration-cache diagnostics. | If the full available sample still fits one child plan, calibration is skipped as a non-cacheable match; baseline errors, split-child errors, or comparison mismatch fail calibration; required matches and max trials are enforced. | Splitting must be quality-gated before using cheaper or smaller child runs for real output. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-008` | MUST | Calibration is disabled or passes. | Hidden child calls execute with at most `max_parallel_children`; each child is prompted not to perform write actions and to return only the assigned structured result. | Step outputs include `managed_children`, each with index, label, scope count, task count, tasks, text, valid flag, repair count, structured object if present, error fields, model metadata, effort metadata, estimated cost, and a content/source-capability-free scope-reference projection. | Child errors do not short-circuit sibling collection. By default the first error fails the step after diagnostics are built. With explicit `continue_on_child_error: true`, at least one schema-valid child permits either a schema-validated partial aggregate or, when combination is disabled, the complete diagnostic child set, and records exact failed/successful child counts; all-child failure still fails the step. | Operators need enough child diagnostics to audit split behavior while preserving one visible workflow step, and explicitly partial-safe domain workflows need to checkpoint successful scopes without losing failed-scope identity. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-009` | MUST | Calibration fails or child planning produces one or fewer child plans. | The runtime falls back to a single full structured agent call with `Managed` disabled in the prompt context and returns that result with managed diagnostics. | `managed.calibration` persists the failure or skipped reason; no `managed_children` are emitted for a calibration-fallback result. | Fallback must still validate and repair against the same output contract; fallback errors fail the visible step normally. | Failed split quality must not silently produce a lower-quality combined result. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-010` | MUST | All real child runs complete successfully. | By default, child structured outputs are combined through `CombineStructuredOutputs`; object schemas concatenate string fields, concatenate and deduplicate array fields, carry scalar/object fields from the first available child, and include unknown fields from child objects. With explicit `combine_structured_outputs: false`, each child still satisfies the original contract but the visible step emits only `managed_children` and total `structured_repairs`, with empty text and no top-level `structured`, `structured_json`, `structured_valid`, or `structured_error`. | The default visible step output text is the combined JSON with its structured envelope. Children-only mode persists no synthetic aggregate. | Default combined output is validated against the original schema and invalid output fails the visible step. Children-only mode still fails when any required child exhausts repair without satisfying that schema; it may not bypass per-child validation. | Most downstream steps need one schema-valid object; domain-owned reducers instead need every independently bounded child without lossy concatenation or applying a per-child bound to an aggregate. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-011` | MUST | Managed model optimization is enabled with candidate aliases. | For each child, the runtime estimates prompt and output tokens, compares known input/output prices, and selects the cheapest configured exact alias with known price; the selected alias is passed as an isolated child-run override while `account_ref` remains unchanged. | Child outputs and `managed.optimization` record baseline alias, selected alias counts, price source, known-price flags, selected/baseline USD estimates, and total estimated savings. | Unknown-price aliases are ignored for replacement; subscription-backed aliases may inherit estimate prices from an alias-valued `subscription_equivalent_model`; missing aliases fail strict validation and provider initialization failures do not prevent the baseline alias from running. | Agent execution optimization should reduce cost only when the runtime can explain and estimate the replacement without changing account policy. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-012` | MUST | Managed effort optimization is enabled. | The runtime chooses a per-child reasoning effort from estimated child prompt size, scope count, and task count, and passes it as an isolated child-run override. | Child outputs and `managed.optimization.effort` record selected effort counts and whether effort changed. | Disabled effort optimization leaves the override empty; very large child prompts may choose higher effort. | Smaller child prompts can often use lower reasoning effort without changing the workflow contract. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-013` | MUST | A configured candidate child alias is cheaper and absent from the agent's candidate provider map. | The runtime resolves that exact alias through the effective concrete account, creates the provider with the loop's provider factory, and stores it under the resolved provider/model key used by alias override resolution. | `agent.CandidateProviders` is initialized or extended in memory for the agent instance. | Missing aliases, missing effective accounts, or provider factory errors are returned to the caller for logging; the same account-and-alias candidate is not initialized twice. | Child alias overrides must have providers available before hidden child calls execute. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-014` | MUST | The workflow dashboard displays a run whose step outputs contain `managed`. | The run detail view shows one optimization panel entry per managed step, including strategy, child count, calibration status, model-change status, effort-change status, estimated savings, and selected model information. | Dashboard rendering does not mutate run state. | Missing or malformed managed metadata hides the panel or displays fallback values without breaking the run detail page. | Agent execution optimization must be inspectable by operators without reading raw JSON output. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-015` | MUST | A split contract has a passing calibration and calibration caching is enabled. | The agent instance remembers the passing calibration by a hash over strategy, model, prompt/context hash, output schema, task list, chunking options, child plan shape, scope path/hash/content/language signals, repository signals, and the effective child prompt target used by the planner. Exact matching runs calibrate aggressively at first, then only when the expanding use interval is due. When an exact key is absent, the runtime may borrow once from a trusted similar entry, create a separate provisional entry for the new key, and force verification on that key's next matching use. Verification success promotes the provisional entry with inherited confidence adjusted by similarity and split-fit score; verification failure clears inherited confidence and the key behaves like a fresh untrusted entry. Passing calibration stores a learned child prompt target based on observed child prompt sizes so later similar splits can reuse the learned target instead of a static configuration value. | `managed.calibration.status` is `trusted_cache` on exact or similar cache hits; `managed.calibration.cache` records the key, decision (`hit`, `similar_hit`, `borrowed_due`, `due`, `miss`, or `previous_not_trusted`), use count, success streak, provisional flag, borrowed source/similarity when present, split-fit score, next due use, model, language, repository, scope, task metadata, effective target, learned target, target source, and observed child prompt token statistics. | Failed or skipped calibrations are not trusted; materially different strategy, plan shape, schema, prompt, tasks, model, language, repository/scope identity, chunking, or effective target below the similarity threshold produces a cache miss. Low split-fit scores shorten the next probe interval even after successful calibration. | Proven split behavior should reduce repeated calibration token spend without blindly trusting small changes or weak split plans. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-016` | MUST | A structured or managed workflow agent request is admitted with `history: read_only`. | The runtime captures the exact existing-session snapshot before planning, or accepts the compiler-private owner/revision-matched snapshot captured before durable run creation. That private snapshot has every structured media locator replaced at capture and carries the strict canonical versioned self-contained `FrozenSet` persisted with it. Before planning, the runtime validates the frozen snapshot revision and complete set, then materializes only integrity-checked embedded bytes; the unsplit request, grouped calibration baseline, sampled and real children, fallback, and structured-output repairs receive separately graph-detached copies of that one materialized evidence graph, including runtime-only message/system-block prompt provenance and tool-call name/arguments/thought signature preserved across wait/restart. An ordinary visible output retains its canonical `session` and opaque `history_revision`; the private agent result substitutes fixed `session: private` and `session_mode: private`, an empty cache key/message ID, and the same pre-materialization revision internally, while the enclosing private workflow exposes none of its structured/managed output, frozen references/set, materialized data, or media diagnostics through generic observations. | No child call writes, restores, compacts, or rereads the session or media store; no child receives tools, raw session/inbound/scope/media authority, or a different cache/account identity, and provider-side mutation of one call's message graph cannot change later calls' frozen context. | A missing/corrupt/unowned or private agent/revision/runtime-metadata-mismatched snapshot, invalid strict `FrozenSet`, failed materialization, attempted live lookup, or child tool-call response fails the visible step; a session append or live-media replacement/release after capture is intentionally irrelevant to every call in that execution and after resume/retry/restart. | Optimization must preserve the exact evidence, prompt construction, privacy, and authority boundary of an AI gate instead of turning one decision into several differently contextualized turns. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-017` | MUST | A structured or managed workflow agent request is admitted with the exact `session: ephemeral`, `history: none`, `cache: none`, and `tools: none` profile. | Before planning, the runtime acquires one revocable exact-generation lease and creates one request-local stateless side-question closure carrying that generation diagnostic policy; it uses the closure for the unsplit request, grouped calibration baseline, sampled and real children, calibration fallback, and every structured-output repair. First-party workflow roots install zero diagnostic policy, so no branch activates preview output. Every call preserves ordinary account/model alias selection and explicit model/reasoning overrides but supplies no session-affinity key, history, prompt cache, hooks, MCP, or tools. The visible output retains fixed `session: ephemeral` and `session_mode: ephemeral` markers plus normal structured and managed diagnostics. | No branch creates or mutates session, history, summary, active-turn, account-router affinity, or prompt-cache state. Existing agent-local calibration strategy state and visible managed diagnostics retain their normal bounded hashes and measurements, but never the random ephemeral identity or session-like state. | Incompatible modes, a provider-authored tool call, or loss of the stateless profile in any initial, repair, calibration, child, or fallback call fails the visible step. Cancellation propagates, and the random identity is never exposed in child diagnostics, logs, events, or outputs. | Structured repair and managed optimization must not silently turn a stateless AI gate into one or more durable or account-affine conversations. |
| `FR-AGENT-EXECUTION-OPTIMIZATION-018` | MUST | `reviewer_models` names two or more distinct configured model aliases on a managed structured request. | Child planning takes the Cartesian product of each selected scope/task plan and every reviewer alias, including when a single scope item would otherwise not split. Each child uses its assigned alias as an explicit model override with an explicit empty inherited-fallback list, without changing account policy. | Split metadata records the reviewer alias list; each child records its exact assigned model, while downstream domain persistence may retain all corroborating model observations. | Aliases are trimmed, deduplicated, bounded to eight, initialized and validated before calls, and unavailable aliases fail before child execution. Reviewer assignment overrides price optimization for that child. The runtime admits at most 4,096 total children and 64 parallel workers independent of authored values; larger domain scopes must batch before managed execution. | A workflow must be able to ask independent configured models to inspect the same immutable scope instead of treating model choice only as a cheapest-child optimization or falsely counting a fallback twice. |

| `FR-AGENT-EXECUTION-OPTIMIZATION-019` | MUST | A repository evaluation configured with two to eight reviewer aliases supplies identical frozen scope chunks and the one to eight aliases still pending for the current batch attempt. | Child planning takes the Cartesian product of scope chunks and aliases with model/adaptive optimization disabled, bounded parallelism, and `combine_structured_outputs: false`; every child records requested alias, actual concrete-model metadata, safe scope references, structured result, usage, and failure. | Only normal managed diagnostics and independently bounded child results are persisted in the workflow step; the evaluation controller separately blinds and checkpoints bounded judgments and rotates reviewer order between batches. | Unavailable aliases fail before child execution, partial failures remain explicit, every child retains the original output bound, and alias/concrete identity is removed before the judge receives candidate outputs. No unused cross-model aggregate is built or revalidated against a per-child schema, including a recovery attempt with one pending alias. | Comparing models requires the same evidence and contract per alias without optimization silently changing the candidate, losing claims in a synthetic merge, or mistaking order bias for quality. |

## Data And State Model

Agent execution optimization uses only normal workflow step outputs for durable state.
There are no standalone managed child workflow records, retry records, or graph
nodes.

Visible optimized agent step outputs may contain:

```text
text                         combined JSON or fallback text; empty in children-only mode
agent_id                     selected workflow agent id
session                      durable session key or fixed ephemeral/private audit marker
session_mode                 fixed ephemeral or private marker when applicable
history                      workflow agent history mode
history_revision             opaque frozen-session revision for exact read-only decisions
cache                        normalized workflow agent cache mode
cache_key                    prompt cache key, if any
message_id                   workflow/message correlation id
structured                   parsed combined JSON object or array, when enabled
structured_json              normalized combined JSON text, when enabled
structured_valid             combined validation result, when enabled
structured_repairs           total child repair attempts used
structured_error             combined validation or repair error, when present
managed                      diagnostic object for mode/split/calibration/optimization
managed_children             hidden child diagnostic objects, only for real split runs
```

An ephemeral request has no durable session key or history revision and leaves
`cache_key` empty. Its internal random identity is request-local and absent from
the visible result, child diagnostics, managed diagnostics, and calibration
cache; `session` and `session_mode` both contain only the literal `ephemeral`.

A compiler-private read-only request carries the same opaque history revision
through every branch, but its raw session key, inbound context, and scope are
absent. Its private frozen representation contains a locator-rewritten snapshot
and strict versioned `FrozenSet`, plus runtime-only prompt layer/slot/source and
tool-call name/arguments/thought-signature fields that ordinary session JSON
omits. The persisted revision is checked before the set is materialized, and no
branch stores the resulting data URIs in managed diagnostics. Internally it
uses one domain-separated agent-plus-revision pseudonym
for enabled prompt cache and account affinity. Its step result contains only
the literal `private` for `session` and `session_mode`, an empty public cache
key/message ID, and is itself removed from every generic private-run
projection.

`managed` contains:

```text
enabled                      managed mode was not off
mode                         off, auto, or configured mode string
strategy                     single_run, scope_split, task_split, hybrid_split
agent_tasks                  textual tasks from the agent definition
agent_task_count             number of agent tasks considered
scope_count                  number of scope items considered
estimated_prompt_tokens      rough prompt estimate before splitting
estimated_scope_tokens       rough scope estimate
split                        split plan metadata
split.token_efficiency       unsplit, per-child, total, ratio, and over-split token estimates
calibration                  grouped-vs-split calibration result
calibration.cache            cache key, decision, interval, and identity metadata
optimization                 model, effort, token, and cost summaries
```

`managed_children[]` contains per-child diagnostic data:

```text
index                        1-based child plan index
label                        human-readable scope/task chunk label
scope_count                  count of assigned scope items
task_count                   count of assigned tasks
tasks                        assigned task labels
text                         raw child response text
valid                        child structured validation result
repairs                      repair attempts used for this child
structured                   parsed child JSON, if valid enough to parse
error                        structured validation error
run_error                    child run or repair error
model                        selected/default/price metadata
effort                       reasoning-effort metadata
estimated_cost               selected/baseline token and USD estimate
```

Optimization options are read from `with.managed` as either a string/bool mode
or a map. Supported map keys include:

```text
mode                         auto, off, or implementation-defined mode
enabled                      false disables agent execution optimization
split | strategy             auto, none, scope_split, task_split, hybrid_split
max_items_per_chunk          scope chunk size, default 8
max_tasks_per_chunk          task chunk size, default 2
max_parallel_children        child concurrency limit, default 4
reviewer_models              comma string or list of up to 8 exact aliases
continue_on_child_error      continue with a partial aggregate or child set when one succeeds
combine_structured_outputs   emit and validate a synthetic aggregate, default true
adaptive_chunking            pack scope chunks by token budget, default true
estimated_output_tokens      per-child output estimate, default 1000
calibration.enabled          enable split quality check, default true
calibration.sample_size      scope sample count, default 6
calibration.task_sample_size task sample count, default 3
calibration.required_matches required matching trials, default 1
calibration.max_trials       maximum calibration trials, default 1
calibration.cache_enabled    reuse recent passing calibration, default true
calibration.cache_max_interval maximum uses between probes, default 16
calibration.similarity_threshold minimum score for soft cache reuse, default 0.72
optimization.model.enabled   enable child model replacement, default true
optimization.model.candidates list of model aliases or candidate maps
optimization.effort.enabled  enable effort override, default true
```

Candidate maps name exact aliases and may include:

```text
name | model
input_price_per_1m
output_price_per_1m
subscription
subscription_equivalent_model  exact alias used only for price estimation
```

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Workflow YAML | `uses: agent/<id>` with `with.output` | `output: json` or an output map enables JSON extraction, schema validation, repair attempts, and `steps.<id>.outputs.structured`. | `FR-AGENT-EXECUTION-OPTIMIZATION-001`, `FR-AGENT-EXECUTION-OPTIMIZATION-010` |
| Workflow YAML | `uses: agent/<id>` with `with.managed` | `managed` enables or configures hidden split execution only when structured output is also enabled. | `FR-AGENT-EXECUTION-OPTIMIZATION-002`, `FR-AGENT-EXECUTION-OPTIMIZATION-003` |
| Workflow YAML | `with.scope` | A list or `{items: [...]}` supplies splittable scope items for scope and hybrid strategies. | `FR-AGENT-EXECUTION-OPTIMIZATION-004`, `FR-AGENT-EXECUTION-OPTIMIZATION-006` |
| Workflow YAML / private handoff | `history: read_only` with `tools: none` and optional pre-captured `FrozenReadOnlySession` | Preserve one strict existing-session snapshot and its self-contained frozen-media set across structured repairs and every managed execution branch, materialize only after frozen-revision validation without live rereads, and return the ordinary canonical identity or only fixed private audit markers as appropriate. | `FR-AGENT-EXECUTION-OPTIMIZATION-016` |
| Workflow YAML | `session: ephemeral` with `history: none`, `cache: none`, and `tools: none` | Preserve one stateless no-affinity execution profile across structured repairs and every managed execution branch, returning only fixed ephemeral audit markers. | `FR-AGENT-EXECUTION-OPTIMIZATION-017` |
| Agent definition | `agent.Tasks` | Textual tasks are used as semantic task-splitting responsibilities and are injected into child prompts. | `FR-AGENT-EXECUTION-OPTIMIZATION-005`, `FR-AGENT-EXECUTION-OPTIMIZATION-006` |
| Go API | `workflows.ParseAgentOutputContract(raw any)` | Parses workflow `with.output` into an `AgentOutputContract` with JSON format, schema, and repair attempts. | `FR-AGENT-EXECUTION-OPTIMIZATION-001` |
| Go API | `workflows.ValidateAgentStructuredOutput(text, contract)` | Extracts JSON from raw agent text, validates a supported schema subset, and returns structured data plus error metadata. | `FR-AGENT-EXECUTION-OPTIMIZATION-001` |
| Go API | `workflows.CombineStructuredOutputs(values, schema)` | Combines child JSON values into one schema-shaped output. | `FR-AGENT-EXECUTION-OPTIMIZATION-010` |
| Go API | `workflows.CompareStructuredOutputs(left, right)` | Compares grouped and split outputs using canonical JSON, comparable array fields, or stable object-array identities. | `FR-AGENT-EXECUTION-OPTIMIZATION-007` |
| Go API | `workflowAgentRunner.RunAgent(ctx, req)` | Chooses managed or normal execution and returns one visible workflow step output. | `FR-AGENT-EXECUTION-OPTIMIZATION-002` through `FR-AGENT-EXECUTION-OPTIMIZATION-013` |
| UI | Workflow run detail optimization panel | Reads `step.outputs.managed` and renders strategy, child count, calibration, model, effort, savings, and selected-model data. | `FR-AGENT-EXECUTION-OPTIMIZATION-014` |

## Algorithms And Ordering

1. The workflow executor parses `with.output` before dispatching an `agent/*`
   step. Invalid output contract syntax fails the step before the agent runner
   is invoked.
2. The agent runner builds the same base prompt used for normal workflow agent
   steps: prompt, context, scope, message, then structured-output
   instructions. For an exact read-only request it first freezes the existing
   session once, or validates and consumes the private snapshot already frozen
   before run creation. The private form strictly validates its persisted
   `FrozenSet` and pre-materialization snapshot revision, materializes embedded
   bytes once without a live store, and gives every call described below a
   graph-detached copy of that evidence, no tools, and no live-history/media
   reread. The private form
   shares one pseudonymous cache/account identity and no raw session, inbound,
   or scope authority. For an exact ephemeral
   request it instead creates one random request-local identity and stateless
   side-question closure before planning; every call below has no session,
   history, prompt cache, hooks, MCP, tools, or account-router affinity.
3. The runner normalizes managed mode. `nil`, `false`, `off`, `none`, and an
   explicit disabled map are treated as off.
4. `workflowManagedSplitStrategy` returns no strategy unless managed mode is on
   and the output contract is enabled.
5. Requested split aliases are normalized. `none` disables splitting. Explicit
   scope, task, or hybrid requests are honored only if the required dimensions
   exceed configured chunk sizes or scope token budget. Auto chooses hybrid,
   then scope, then task.
6. `workflowManagedChildPlans` builds scope chunks, task chunks, or their
   cartesian product. Adaptive scope chunking greedily packs contiguous scope
   items up to the prompt-token target and max item count, so small items share
   child calls and large items split sooner. Scope chunks are copied into either
   a list or the original `{items: [...]}` wrapper. Task chunks are appended as
   assigned-task context.
7. Managed metadata is initialized before calibration so fallback results still
   explain the attempted plan.
8. If the plan has one or fewer children, the runtime performs a single full
   structured run and returns it with non-split managed metadata.
9. If calibration is enabled, the runtime builds a sampled request. Scope
   sampling starts with the first `sample_size` scope items. Task sampling
   starts with the first `task_sample_size` assigned or agent tasks. If that
   sample still fits one child plan while the original request splits, the
   runtime expands the sample until it exercises split behavior or exhausts the
   available scope/tasks.
10. Calibration executes a grouped baseline and split children using the same
    output contract and repair rules, but with model and effort optimization
    disabled. Passing calibrations update an agent-local cache keyed by model,
    language, repository/scope identity, schema, prompt, tasks, strategy, and
    chunking shape. Cache hits record `status: trusted_cache`; exact hits use
    the stored cadence, while similar hits create a provisional new-key entry
    and must verify on the next matching use. If verification fails, inherited
    confidence is discarded and the key starts fresh; if it passes, the new key
    continues with inherited confidence adjusted by similarity and split-fit
    score. The first successful uses calibrate frequently, then the interval
    doubles up to the configured cap, with low split-fit scores shortening the
    interval. This agent-local strategy cache remains available to an ephemeral
    request because it is not workflow prompt caching, but its entries never
    contain the ephemeral identity or any session-like state.
11. Calibration combines sample child results and compares the combined object with
    the baseline. Failed validation, child run errors, or comparison mismatch
    mark calibration failed.
12. Failed calibration returns a single full structured run, with calibration
    details in `managed.calibration` and without `managed_children`.
13. Passing or disabled calibration runs real children through a semaphore
    bounded by `max_parallel_children`.
14. For each child, the runtime estimates child prompt tokens, reads alias
    prices from managed options and resolved concrete-model metadata, selects
    the cheapest known exact alias when optimization is enabled, and chooses
    reasoning effort when effort optimization is enabled. It never changes the
    effective `account_ref`.
15. Before optimized child calls, candidate aliases are resolved through the
    effective concrete account and their providers are registered in
    `agent.CandidateProviders` so alias overrides can use the normal pipeline.
16. Each child call suppresses chat response publishing, disables visible
    history writes, suppresses tool feedback, and tells the child not to perform
    write actions. Ephemeral children and their repairs additionally reuse the
    same stateless closure and blank account-affinity key as the visible call.
17. Child responses are repaired and validated independently. All children are
    allowed to finish so diagnostics are complete even when one child fails.
18. Successful child structured outputs are combined, serialized, validated
    against the original contract, and returned as the single visible step
    result by default. With explicit `combine_structured_outputs: false`, the
    runtime keeps the independently validated `managed_children` and aggregate
    repair count but emits no synthetic structured result.
19. The dashboard reads persisted run step outputs. It does not recompute
    managed state.

## Cross-Feature Behavior

Agent execution optimization is a specialization of workflow `agent/*` step
execution and therefore depends on the workflows feature for YAML parsing, step
persistence, run records, dashboard run details, and compatibility gating. It
depends on agent conversations for agent definitions, prompt construction, hidden
no-history child calls, alias override routing, provider factories, and
reasoning-effort overrides. It depends on strict model aliases and concrete
account configuration for candidate price metadata and alias-valued
subscription-equivalent estimates. It intentionally does
not own workflow DAG execution, reusable workflows, workflow triggers, or
domain-specific split/combine rules.
For compiler-private read-only execution, workflows own capture admission,
durable private persistence, retry/resume reuse, and redacted observation;
Agent Conversations owns frozen-revision validation, materialization handoff,
provider calls, and pseudonymous identity. Session Memory owns complete media
graph freeze/materialization and strict `FrozenSet` semantics, while Tool
Execution owns the optional live snapshot reader used only at initial capture.
This feature only preserves the resulting immutable evidence across every
managed and repair branch and never reacquires either live capability.
The workflows feature owns admission of the exact ephemeral YAML profile and
its fixed output markers. Agent conversations owns the stateless
side-question call and blank account-affinity selection. This feature owns
preserving that same profile through structured repairs, grouped calibration,
fallback, and all managed child calls while keeping its strategy cache distinct
from prohibited workflow prompt-cache and session state.

## Failure And Edge Cases

- `with.managed` without structured output runs as a normal single agent call.
- Output contracts support JSON only; unsupported formats fail before agent
  execution.
- JSON extraction accepts raw JSON, fenced JSON, and JSON embedded in prose,
  but the normalized result must satisfy the schema.
- Schema validation intentionally supports the subset used by workflow output
  contracts: object, array, string, integer, number, boolean, enum, required,
  properties, and items.
- `repair_attempts` defaults to one; non-positive configured values use the
  same default.
- Empty scope or empty task lists do not create meaningless child plans.
- A configured split strategy that cannot split the requested dimension is
  ignored rather than forcing duplicate work.
- Calibration sample sizes larger than available scope/tasks clamp to the
  available count.
- Adaptive chunking does not reorder scope items and does not split one scope
  item internally; a single oversized item runs as its own child.
- The adaptive child prompt target is not a workflow configuration key; it is
  derived from the agent context window, then learned from trusted calibration
  cache entries and reported in diagnostics.
- Token-efficiency diagnostics can show aggregate child prompt overhead even
  when splitting is needed for per-call context limits.
- Calibration comparison may match by canonical JSON, comparable scalar array
  fields, or stable object-array identities such as id, key, scope/task, or
  file/line/severity.
- Calibration failure never returns the split combined result as the final step
  output.
- Disabled calibration is allowed and records `status: not_run`.
- Child validation errors fail the visible step, but diagnostics include every
  child that completed.
- `combine_structured_outputs: false` skips only synthetic aggregation and its
  validation; it never skips child schema validation or repair.
- Combined validation errors fail the visible step even when every child was
  individually valid.
- Unknown-price candidate aliases do not replace the baseline alias.
- Provider initialization failures are warnings during workflow execution and
  do not fail strategy selection by themselves.
- Hidden child prompts instruct the agent not to perform write actions, but
  side-effect safety still relies on the existing agent/tool policy layer.
- Managed child diagnostics may contain proposed child text and structured
  output, so normal workflow run output visibility rules apply.
- Exact read-only managed execution fails closed when its existing-session
  snapshot cannot be captured or owned by the selected agent. Calibration,
  fallback, child, and repair calls cannot observe an append made after that
  capture, and a provider cannot mutate the frozen graph seen by later calls.
  The compiler-private form additionally rejects live lookup, an agent or
  revision mismatch, invalid frozen-set encoding/reference/metadata/digest, or
  failed materialization. Resume, retry, and restart never recapture media from
  temporary storage. Any branch that exposes the raw session, inbound context,
  scope, cache identity, frozen references/set, materialized data, capture
  diagnostics, or managed output through a generic private-run projection fails
  closed.
- Ephemeral managed execution fails closed when any initial, calibration,
  fallback, child, or repair call attempts to acquire history, prompt cache,
  tools, or session/account affinity. A provider-authored tool call is never
  ignored or executed. Normal calibration hashes and diagnostics may persist,
  but the request-local identity cannot enter them, child diagnostics, logs,
  events, or visible outputs.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-AGENT-EXECUTION-OPTIMIZATION-019` | [pkg/agent/workflow_managed_ensemble_test.go](../../pkg/agent/workflow_managed_ensemble_test.go), [pkg/workflows/repository_model_evaluation_workflows_test.go](../../pkg/workflows/repository_model_evaluation_workflows_test.go), [web/backend/api/repository_model_evaluation_controller_test.go](../../web/backend/api/repository_model_evaluation_controller_test.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-001` | [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go), [pkg/workflows/agent_output.go](../../pkg/workflows/agent_output.go), [pkg/workflows/executor.go](../../pkg/workflows/executor.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-002`, `FR-AGENT-EXECUTION-OPTIMIZATION-003` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-004`, `FR-AGENT-EXECUTION-OPTIMIZATION-005`, `FR-AGENT-EXECUTION-OPTIMIZATION-006` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-007`, `FR-AGENT-EXECUTION-OPTIMIZATION-009`, `FR-AGENT-EXECUTION-OPTIMIZATION-015` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go), [pkg/workflows/agent_output.go](../../pkg/workflows/agent_output.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-008`, `FR-AGENT-EXECUTION-OPTIMIZATION-010` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go), [pkg/workflows/agent_output.go](../../pkg/workflows/agent_output.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-011`, `FR-AGENT-EXECUTION-OPTIMIZATION-012`, `FR-AGENT-EXECUTION-OPTIMIZATION-013` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go), [pkg/config/model_config_test.go](../../pkg/config/model_config_test.go), [pkg/config/subscription_equivalent_model_test.go](../../pkg/config/subscription_equivalent_model_test.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-014` | [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [web/frontend/src/components/workflows/workflow-authoring-page.tsx](../../web/frontend/src/components/workflows/workflow-authoring-page.tsx) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-016` | [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/workflows/private_session_test.go](../../pkg/workflows/private_session_test.go), [pkg/session/session_store.go](../../pkg/session/session_store.go), [pkg/session/frozen_media_test.go](../../pkg/session/frozen_media_test.go), [pkg/media/frozen_test.go](../../pkg/media/frozen_test.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go), [pkg/agent/runtime_gate_test.go](../../pkg/agent/runtime_gate_test.go), [pkg/agent/runtime_policy_constructors_test.go](../../pkg/agent/runtime_policy_constructors_test.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-017` | [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go), [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go) |
| `FR-AGENT-EXECUTION-OPTIMIZATION-018` | [pkg/agent/workflow_managed_ensemble_test.go](../../pkg/agent/workflow_managed_ensemble_test.go), [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go), [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go) |

## Implementation Anchors

- [pkg/workflows/agent_output.go](../../pkg/workflows/agent_output.go)
- [pkg/workflows/executor.go](../../pkg/workflows/executor.go)
- [pkg/workflows/context.go](../../pkg/workflows/context.go)
- [pkg/agent/workflow_runtime.go](../../pkg/agent/workflow_runtime.go)
- [pkg/agent/workflow_managed.go](../../pkg/agent/workflow_managed.go)
- [pkg/agent/turn_coord.go](../../pkg/agent/turn_coord.go)
- [pkg/agent/workflow_runtime_test.go](../../pkg/agent/workflow_runtime_test.go)
- [pkg/workflows/agent_output_test.go](../../pkg/workflows/agent_output_test.go)
- [pkg/workflows/executor_test.go](../../pkg/workflows/executor_test.go)
- [web/frontend/src/components/workflows/workflow-authoring-page.tsx](../../web/frontend/src/components/workflows/workflow-authoring-page.tsx)

## Surface Ownership

Owns: CODE pkg/workflows/agent_output.go
Owns: CODE pkg/workflows/context.go
Owns: CODE pkg/workflows/executor.go
Owns: CODE pkg/agent/workflow_runtime.go
Owns: CODE pkg/agent/workflow_managed.go
Owns: TEST pkg/workflows/agent_output_test.go
Owns: TEST pkg/workflows/executor_test.go
Owns: TEST pkg/agent/workflow_runtime_test.go
Owns: TEST pkg/config/subscription_equivalent_model_test.go *
Owns: WORKFLOW agent/* with with.output
Owns: WORKFLOW agent/* with with.managed
