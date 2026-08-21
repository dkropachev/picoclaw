# Repository Model Evaluations

Repository model evaluations compare two or more configured model aliases over
one reproducible repository corpus. They are separate from Repository Reviews:
an evaluation produces a comparison table, not bug findings or issue drafts.

## Create And Analyze

1. Open **Model evaluations** from Services.
2. Choose a repository and optional ref.
3. Select the eligible code types. You may include exact repository-relative
   folder prefixes, exclude prefixes, and add free-text guidance. Exclusions
   always win; AI guidance can narrow but never widen the structured scope.
4. Select two to eight candidate model aliases, a file-selector alias, and a
   judge/analyzer alias.
5. Choose the default files per language (20 by default and at most 20), save,
   and select **Analyze repository**.

Preflight resolves the ref to one commit, inventories tracked Git blobs,
detects eligible programming languages and codebase regions, and asks the
selector AI to rank a safe metadata-only candidate pool. Native validation
rejects invented or stale IDs, enforces quotas and folder/type boundaries, and
fills safe omissions so every eligible language remains represented.

## Review The Corpus

When preflight is ready, the language table shows eligible and selected files,
selected source bytes, represented regions, and a **limited** marker when a
language has fewer eligible files than requested.

Change a per-language value from 1 through 20 and save to invalidate the old
corpus, then analyze again. The paged corpus preview contains only safe Git
references (path, language, code type, region, blob, and size), never source
content or checkout paths.

## Run And Interpret

Select **Start evaluation**. Each candidate receives the same immutable source
chunks and structured task. Candidate order rotates between batches. Outputs
are blinded before the judge compares them against exact source, and a final AI
analyzer aggregates bounded batch judgments.

For each batch, the launcher revalidates only the persisted selected files at
the pinned commit, then reads their blobs in one bounded Git batch and freezes
two one-shot copies of the same evidence. The candidate phase consumes one and
the judge consumes the other; the full repository is not recataloged per batch.
Live stages come from actual backend workflow activity. Candidate work is shown
at the batch level so the UI never attributes a serial child call to the model
that just finished; the exact judge alias is shown during the judge call.

The durable recovery boundary is a fully judged batch. Resume skips successful
alias/file pairs recorded in those batches. If the launcher stops inside an
unpersisted candidate-or-judge batch, that in-flight batch is retried against
the same pinned commit rather than claiming its transient output survived.

The table combines AI-judged correctness, evidence, coverage, actionability,
overall score, rank, verdict, strengths/limitations, partial/failure state,
corpus coverage, concrete routed-model distribution, requests, tokens, latency,
and known estimated cost.

Quality scores are explicitly **AI judged**. They are comparative evidence, not
ground-truth benchmark scores. Unknown price is displayed as unknown, not free.
If the judge is also a candidate, the evaluation records a visible bias warning.

## Recovery

Progress is backend-owned and continues when the page is closed. Cancel stops at
a durable boundary. Resume retains the same pinned corpus and completed batch
checkpoints; it never substitutes a newer branch tip. Restart creates a new
attempt and preflight. If the pinned commit is unavailable, execution fails
explicitly instead of evaluating different source.
