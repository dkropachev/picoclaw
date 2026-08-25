# Repository Model Evaluations

Repository model evaluations compare two or more configured model aliases over
one reproducible repository corpus and a deterministic range of provider-call
work sizes. They are separate from Repository Reviews: an evaluation reads and
snapshots a review profile but produces comparison statistics, not repository
findings, issue drafts, or changes to that profile.

## Prepare The Experiment

1. Create or choose a reusable profile under **Repository reviews → Profiles**.
   Its reviewer, execution account, focus, structured scope, files per batch,
   content bytes per batch, and parallel workers define the probe policy. A
   profile with more than 128 files per batch is not eligible for a model probe.
2. Open **Model review probes** and choose a repository, the profile, and an
   optional branch, tag, or commit. Blank revision uses the repository default.
3. Select two to eight candidate aliases. The profile reviewer must be one of
   them; the page selects it and a second compatible model initially. Candidate
   aliases are the experiment variable, so all must be executable through the
   selected profile account.
4. Select **Run probe**. Scope, corpus quotas, selector, judge, and work sizing
   are not custom probe options. The server derives them from the profile and
   rejects a profile-driven request that also supplies custom values.

At admission the launcher loads the latest profile, resolves a blank account to
the current runtime default, and freezes the profile ID/version/name, effective
account, reviewer, focus/scope, files/content maxima, and parallelism on the
probe. The reviewer performs metadata-only corpus selection. The launcher
chooses a compatible judge automatically, preferring an alias outside the
candidate set. Later profile edits or default-account changes do not alter the
running or completed experiment. Actual-review controls such as force mode,
automatic continuation, and the task-admission guard are not applied to a
comparison probe.

## Understand The Work-Sizing Sweep

The profile's **Files per batch** and **Content bytes per batch** are configured
maxima. For each maximum, the launcher creates a deduplicated ladder at:

- `ceil(maximum / 4)`;
- `ceil(maximum / 2)`; and
- the configured maximum.

The file sweep changes only files per provider call while holding content bytes
at the configured maximum. The content sweep changes only content bytes while
holding files at the configured maximum. The point containing both configured
maxima is shared rather than run twice. For example, a profile configured for
24 files and 524,288 bytes produces file values 6, 12, and 24 and byte values
131,072, 262,144, and 524,288, with the 24-file/524,288-byte
configured-baseline point serving as the common reference without being
executed twice.

These are real provider-call bounds. The workflow freezes related-file groups
under both requested limits and managed candidate dispatch sends those groups
directly; it does not retain a hidden three-file child split. One individually
reviewable file may stand alone above a smaller requested group-byte value so
the sweep does not silently omit it. The report therefore preserves requested
values separately from observed provider-call file and content-byte values.

## Review The Fixed Corpus And Live Run

Preflight resolves the ref to one commit, inventories tracked Git blobs,
detects eligible programming languages and codebase regions, and asks the
profile reviewer to rank a safe metadata-only candidate pool. Native validation
rejects invented or stale IDs, enforces profile scope and folder/type
boundaries, fills safe omissions, and retains every eligible language.

Selection targets at most 20 files per language. If that yields more than 128
files, the launcher caps the corpus by taking languages round-robin, preserving
representation. That exact ordered path/blob corpus is persisted once and used
for every candidate at every sizing point. A sweep never selects different code
to make a requested point easier to attain.

Each selected run has four keyboard-accessible tabs:

- **Status** shows durable stage/call progress, active provider work, warnings,
  usage, failures, and run history.
- **Corpus by language** shows available, selected, and completed files, source
  bytes, regions, and **limited** languages.
- **Corpus preview** pages safe immutable references: path, language, code type,
  region, blob identity, and size. It never returns source or checkout paths.
- **Final report** becomes available after completed comparisons are durable and
  renders the complete report inline. **Open dedicated report** deep-links to
  `/model-evaluations/{evaluation_id}/report` for sharing or later inspection.

Candidate order rotates between groups. Every candidate receives identical
immutable source, prompt, output schema, effort, and output limits. Outputs are
blinded before the judge compares them against the second one-shot copy of the
same evidence. Quality scores are explicitly **AI judged** comparative evidence,
not ground-truth benchmark measurements. Judge/candidate overlap is warned.

## Read The Work-Sizing Statistics

For every model and requested point, the final report shows:

- requested files and content bytes per batch;
- observed provider-call files and content bytes as minimum, mean, and maximum;
- completion plus attempted, successful, and failed candidate calls;
- files and source bytes actually analyzed;
- supported and unsupported judge-classified claims;
- correctness, evidence, coverage, diagnostic utility, and overall score sample
  count, completed-file-weighted mean, minimum, maximum, and population standard
  deviation; and
- candidate requests, tokens, cached input, reasoning, provider time, known
  estimated cost, effective tokens, and effective tokens per KiB analyzed.

Cached input is already included in input tokens. The report weights it as one
tenth of ordinary input without double-counting it:

```text
effective tokens = (input tokens - cached input tokens)
                 + 0.1 * cached input tokens
                 + output tokens

effective tokens per KiB = effective tokens * 1024 / analyzed source bytes
```

Cached input is clamped to input tokens. Per-KiB efficiency is unavailable when
no source bytes were analyzed. Reasoning tokens are displayed separately but
are not added again because provider output usage already includes them.

## Interpret A Quality Ceiling

The report derives a separate files-per-batch and content-bytes-per-batch ceiling
for each model. It sorts points by the maximum workload actually observed, not
the requested cap. Only complete, scored, nonempty observations that stayed
within their requested ceiling are eligible. Partial, failed, unscored, empty,
and oversized-single-file observations remain visible but cannot establish or
move a ceiling. An underfilled 128 KiB request observed at 50 KiB contributes a
50 KiB measurement; it never manufactures a 128 KiB ceiling.

The smallest eligible point is the analytical score baseline. At the first
larger eligible point whose overall weighted mean is at least 5.0 points lower
than that baseline, the preceding observed value is reported as the ceiling. If no
eligible point degrades by 5.0 points, the result says the ceiling is **at least**
the largest eligible value. If no point is eligible, the ceiling is **not
established**. The configured-maxima point is the shared reference at the upper
end of both one-dimensional sweeps.

Use the ceiling beside the full score distribution, failures, observed grouping,
claim assessment, and weighted token efficiency. It marks the first tested
quality drop under this corpus and judge; it is not a universal provider limit.
Unknown price remains unknown rather than free.

## Recovery

Progress is backend-owned and continues when the page is closed. The durable
recovery boundary is a complete fully judged sizing-point batch. Restart on a
failed probe keeps the same pinned commit, profile snapshot, sizing plan, and
corpus and skips those complete checkpoints. If a judged checkpoint contains
missing candidate work, it is discarded and the complete original batch is
rerun and rejudged so recovered scores never use a smaller peer context.
**Start over** creates a fresh probe with the same frozen experiment
configuration and a new preflight. Work inside an unpersisted
candidate-or-judge batch is retried rather than presented as recovered. If the
pinned commit is unavailable, execution fails explicitly instead of
substituting a newer branch tip.
