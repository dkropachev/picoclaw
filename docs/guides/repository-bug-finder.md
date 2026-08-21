# Repository Bug Finder

PicoClaw's repository bug finder reviews an exact Git commit, stores validated
findings, and skips unchanged Git blobs on later runs.

## Configure And Run From The Dashboard

Open **Repository reviews** and choose **New pre-review**. Save a reusable
profile with:

- the repository URL or local checkout, ref, target, and review focus;
- one or more target code types: hot-path production code, normal production
  code, tests, and benchmark/performance tests;
- optional exact include-folder and exclude-folder prefixes (excludes win), plus
  free-text guidance that AI may use only to narrow that structured boundary;
- one reviewer alias, or several aliases in comparison mode;
- the bounded files per batch and parallel child count;
- optional input/output prices for aliases whose account configuration has no
  safe price metadata;
- a maximum token or estimated USD budget;
- account-specific default, daily, weekly, or provider-window minimum remaining
  percentages; and
- whether unknown account telemetry pauses the review, whether completed
  batches continue automatically, and whether quota-paused work resumes after
  the criteria recover.

When any token, cost, or account guard is active, the controller enforces one
provider request at a time. A threshold is evaluated from actual usage after a
response returns, so the maximum overshoot is one provider response; that paid
response is still validated and checkpointed when possible, and no subsequent
request is admitted.

Select **Start pre-review** to launch the first bounded batch. The control card
shows the live workflow stage, run ID, files reviewed and remaining, actual
tokens, estimated cost, current account snapshots, and a per-model comparison.
**Pause safely** finishes and checkpoints the current bounded batch but admits
no next batch. **Resume** continues pending blobs; a token/cost pause offers a
budget-counter reset. **Restart** resets campaign progress and statistics (the
repository's durable blob ledger still prevents unchanged work unless force
mode is enabled).

Quota monitoring and automatic resume run in the launcher backend, so the
dashboard does not have to remain open. After a launcher restart, an interrupted
profile is shown with the `service_restart` reason and resumes automatically
only when that profile opted in.

Before the first reviewer call in every batch, PicoClaw inventories the exact
commit and classifies the structured scope. It releases the checkout, asks AI
to plan a metadata-only target filter, reacquires only the pinned commit, and
validates the plan natively against opaque candidate IDs and folder/type
boundaries. The scope preflight summary records its commit, selection counts,
rationale, and warnings. AI cannot invent a path, re-include an ignored folder,
or select an unchosen code type.

## Install And Run From The CLI

Install the built-in workflow once:

```sh
picoclaw workflow install repository-bug-finder
```

Run it with a local checkout or clone URL:

```sh
picoclaw workflow run workflows/repository-bug-finder.yml \
  --inputs '{"repository":"https://github.com/owner/repository.git","ref":"main"}'
```

The default run admits at most 24 pending files, groups related files into
bounded contexts of up to three, and may reduce the effective file count when
many required reviewer aliases are selected. Inspect `remainingFiles` in the
run output or the banner on **Repository reviews**, then run the workflow again
until the latest run reports zero remaining files. Unchanged blob
SHA/size pairs under the same review profile are not sent to a model again. The
profile includes the resolved model graph, so a relevant alias, account route,
or model configuration change invalidates the checkpoint.

To challenge every file with several configured model aliases:

```sh
picoclaw workflow run workflows/repository-bug-finder.yml \
  --inputs '{"repository":"https://github.com/owner/repository.git","ref":"main","review_models":"review-a,review-b"}'
```

Each bounded file-context chunk is reviewed under four correctness, security,
recovery, and integration challenge nudges. Without `review_models`, the main
agent's ordinary model/fallback chain is required and configured fallback
aliases are also tried as opportunistic corroborators. With `review_models`,
every requested alias receives the same chunks as an independent required
reviewer with inherited fallbacks disabled. The finding records the alias that
actually produced each validated observation.

Use passive API-backed reviewer providers. Repository review rejects
`codex-cli` and `claude-cli` aliases because those agentic CLIs run with broad
local execution permissions that are incompatible with the immutable no-tool
evidence boundary.

Use `force: true` to start or continue a durable force-review campaign. The
campaign advances through bounded batches rather than repeatedly selecting the
first files.

## Findings And Follow-up

Open **Repository reviews** in the dashboard to:

- inspect complete commit/blob hashes, validation checks, model contributors,
  and the exact file manifest for every opaque context ID;
- select one or many findings and start a durable AI discussion seeded with
  that provenance;
- dismiss, reopen, or mark findings posted;
- prepare and edit an issue draft, then publish immediately or after chatting.

For GitHub publication, the reviewed checkout's actual `origin` must resolve to
`github.com/owner/repository`, and the GitHub MCP connection must provide issue
create and search capabilities. Publication freezes the draft before the
external call and reconciles a stable marker after an ambiguous response; it
does not blindly create a second issue.

## Bounded Failures

Repository bytes are read from their immutable Git objects only into no-tool
model requests. Binary and individually oversized files become visible terminal
unsupported entries. Aggregate-limited or failed files remain retryable and
rotate behind unattempted files. Explicit provider safety/content-filter errors
use bounded request-local handling without marking the provider or account
globally unhealthy; a failed opportunistic corroborator does not block a
successful default fallback chain. Required reviewer failures remain pending.
Each workflow run owns a distinct Git-workspace lease, so concurrent launches
cannot share or release one another's checkout.
