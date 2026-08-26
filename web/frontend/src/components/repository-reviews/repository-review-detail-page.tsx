import {
  IconExternalLink,
  IconFileDescription,
  IconListDetails,
  IconPlayerPause,
  IconPlayerPlay,
  IconRotateClockwise,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewAutomation,
  type RepositoryReviewCommitOption,
  type RepositoryReviewCommitOptions,
  getRepositoryReviewAutomation,
  getRepositoryReviewCommitOptions,
  pauseRepositoryReviewAutomation,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { CollectionDetailShell } from "@/components/collection"
import {
  githubCommitURL,
  shortCommitSHA,
} from "@/components/repository-reviews/repository-review-actions"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"

const fullCommitSHA = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/iu

type ReviewAction = "start" | "pause" | "resume" | "restart"
type CommitChoice = "remembered" | "latest" | "custom"

interface ContinueDialogState {
  review: RepositoryReviewAutomation
  options: RepositoryReviewCommitOptions
  choice: CommitChoice
  customSHA: string
}

export function RepositoryReviewDetailPage({
  id,
  onBack,
  onReport,
  onIssues,
}: {
  id: string
  onBack: () => void
  onReport: () => void
  onIssues: () => void
}) {
  const [continueDialog, setContinueDialog] =
    useState<ContinueDialogState | null>(null)
  const query = useQuery({
    queryKey: ["repository-review-automation", id],
    queryFn: ({ signal }) => getRepositoryReviewAutomation(id, signal),
    retry: false,
    refetchInterval: (state) =>
      state.state.data && isActive(state.state.data) ? 2_000 : false,
  })
  const review = query.data
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const actionMutation = useMutation({
    mutationFn: async ({
      review: current,
      action,
      expectedVersion,
      commitSHA,
    }: {
      review: RepositoryReviewAutomation
      action: ReviewAction
      expectedVersion?: number
      commitSHA?: string
    }) => {
      const input = { expected_version: expectedVersion ?? current.version }
      if (action === "start") {
        return startRepositoryReviewAutomation(current.id, input)
      }
      if (action === "pause") {
        const runID = current.active_run_id || current.run_ids.at(-1)
        return pauseRepositoryReviewAutomation(current.id, {
          ...input,
          ...(runID ? { run_id: runID } : {}),
        })
      }
      if (action === "resume") {
        return resumeRepositoryReviewAutomation(current.id, {
          ...input,
          ...(commitSHA ? { commit_sha: commitSHA } : {}),
        })
      }
      return restartRepositoryReviewAutomation(current.id, input)
    },
    onSuccess: async () => {
      setContinueDialog(null)
      await query.refetch()
      toast.success("Repository review action started.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "Review action failed.",
      )
      void query.refetch()
    },
  })
  const commitOptions = useMutation({
    mutationFn: (current: RepositoryReviewAutomation) =>
      getRepositoryReviewCommitOptions(current.id),
    onSuccess: (options, current) => {
      if (!options.newer_commit_available) {
        actionMutation.mutate({
          review: current,
          action: "resume",
          expectedVersion: options.expected_version,
        })
        return
      }
      setContinueDialog({
        review: current,
        options,
        choice: "remembered",
        customSHA: "",
      })
    },
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : "Commit options could not be loaded.",
      ),
  })
  const busy = actionMutation.isPending || commitOptions.isPending

  return (
    <>
      <CollectionDetailShell
        title={review?.repository || "Repository review"}
        identity={<span className="font-mono text-xs">{id}</span>}
        status={
          review ? <Badge variant="outline">{review.status}</Badge> : undefined
        }
        actions={
          review ? (
            <LifecycleAction
              review={review}
              busy={busy}
              resolvingCommit={commitOptions.isPending}
              onAction={(action) => {
                if (action === "resume") commitOptions.mutate(review)
                else actionMutation.mutate({ review, action })
              }}
            />
          ) : undefined
        }
        loading={query.isLoading}
        error={!notFound ? query.error?.message : undefined}
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="All repository reviews"
      >
        {review && (
          <div className="space-y-6">
            <div className="grid gap-3 sm:grid-cols-2">
              <RelatedButton
                icon={<IconFileDescription />}
                label="Report"
                detail={`${review.progress.findings} durable finding${review.progress.findings === 1 ? "" : "s"}`}
                onClick={onReport}
              />
              <RelatedButton
                icon={<IconListDetails />}
                label="Issue previews"
                detail="Review, edit, link, and publish durable previews"
                onClick={onIssues}
              />
            </div>

            <DetailRows
              rows={[
                ["Review", review.name || "Repository review"],
                [
                  "Branch",
                  review.branch || review.ref || "Default repository branch",
                ],
                ["Stage", review.progress.stage || "waiting"],
                ["Progress", progressLabel(review)],
                [
                  "Reviewer",
                  review.reviewer_models.join(", ") || "Unavailable",
                ],
                [
                  "Issue writer",
                  review.issue_writer_model ||
                    review.reviewer_models[0] ||
                    "Unavailable",
                ],
                [
                  "Account",
                  review.effective_account_ref ||
                    review.account_ref ||
                    "Default account",
                ],
                ["Updated", formatTimestamp(review.updated_at)],
              ]}
            />

            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Metric
                label="Reviewed files"
                value={review.progress.reviewed_files}
              />
              <Metric
                label="Remaining files"
                value={review.progress.remaining_files}
              />
              <Metric
                label="Unsupported"
                value={review.progress.unsupported_files}
              />
              <Metric label="Findings" value={review.progress.findings} />
            </div>

            <section aria-labelledby="review-usage" className="space-y-3">
              <h2 id="review-usage" className="font-semibold">
                Usage
              </h2>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                <Metric
                  label="Prompt tokens"
                  value={review.usage.prompt_tokens}
                />
                <Metric
                  label="Completion tokens"
                  value={review.usage.completion_tokens}
                />
                <Metric
                  label="Total tokens"
                  value={review.usage.total_tokens}
                />
                <div className="border-border rounded-lg border p-3">
                  <p className="text-muted-foreground text-xs">
                    Estimated cost
                  </p>
                  <p className="mt-1 text-lg font-medium tabular-nums">
                    ${review.estimated_cost_usd.toFixed(2)}
                  </p>
                </div>
              </div>
            </section>

            {resolvedCommit(review) && (
              <section className="border-border rounded-lg border p-4">
                <h2 className="font-semibold">Resolved commit</h2>
                <p className="mt-2 text-sm break-all">
                  <CommitReference
                    repository={review.repository}
                    commit={{
                      sha: resolvedCommit(review)!,
                      short_sha: shortCommitSHA(resolvedCommit(review)!),
                    }}
                  />
                </p>
              </section>
            )}

            {review.pause_detail && (
              <p
                role="status"
                className="bg-muted rounded-lg border p-3 text-sm"
              >
                {review.status === "failed" ? "Failed" : "Paused"}:{" "}
                {review.pause_detail}
              </p>
            )}

            {review.scope_plan && (
              <section className="border-border space-y-3 rounded-lg border p-4">
                <h2 className="font-semibold">Pinned scope</h2>
                <p className="text-muted-foreground text-sm">
                  {review.scope_plan.summary}
                </p>
                <div className="grid gap-2 text-xs sm:grid-cols-3">
                  <span>
                    {review.scope_plan.counts.selected_files} selected
                  </span>
                  <span>
                    {review.scope_plan.counts.excluded_files} excluded
                  </span>
                  <span>
                    {review.scope_plan.counts.total_files} inventory files
                  </span>
                </div>
                {review.scope_plan.warnings.length > 0 && (
                  <ul className="text-muted-foreground list-inside list-disc text-xs">
                    {review.scope_plan.warnings.map((warning) => (
                      <li key={warning}>{warning}</li>
                    ))}
                  </ul>
                )}
              </section>
            )}

            <section aria-labelledby="review-history" className="space-y-3">
              <h2 id="review-history" className="font-semibold">
                Run history
              </h2>
              {review.run_ids.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  No durable runs yet.
                </p>
              ) : (
                <ol className="border-border divide-border rounded-lg border">
                  {review.run_ids
                    .slice()
                    .reverse()
                    .map((runID) => (
                      <li
                        key={runID}
                        className="border-b px-3 py-2 font-mono text-xs break-all last:border-b-0"
                      >
                        {runID}
                      </li>
                    ))}
                </ol>
              )}
            </section>
          </div>
        )}
      </CollectionDetailShell>

      <ContinueReviewDialog
        state={continueDialog}
        busy={actionMutation.isPending}
        onClose={() => setContinueDialog(null)}
        onChange={setContinueDialog}
        onConfirm={(state, commitSHA) =>
          actionMutation.mutate({
            review: state.review,
            action: "resume",
            expectedVersion: state.options.expected_version,
            commitSHA,
          })
        }
      />
    </>
  )
}

function LifecycleAction({
  review,
  busy,
  resolvingCommit,
  onAction,
}: {
  review: RepositoryReviewAutomation
  busy: boolean
  resolvingCommit: boolean
  onAction: (action: ReviewAction) => void
}) {
  if (review.status === "idle" && !isQueuedHandoff(review)) {
    return (
      <Button size="sm" disabled={busy} onClick={() => onAction("start")}>
        <IconPlayerPlay /> Start
      </Button>
    )
  }
  if (review.status === "running" || isQueuedHandoff(review)) {
    return (
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        onClick={() => onAction("pause")}
      >
        <IconPlayerPause /> Stop safely
      </Button>
    )
  }
  if (review.status === "paused") {
    return (
      <Button size="sm" disabled={busy} onClick={() => onAction("resume")}>
        <IconPlayerPlay /> {resolvingCommit ? "Resolving commits…" : "Continue"}
      </Button>
    )
  }
  if (review.status === "completed" || review.status === "failed") {
    return (
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        onClick={() => onAction("restart")}
      >
        <IconRotateClockwise /> Run again
      </Button>
    )
  }
  return (
    <Button size="sm" variant="outline" disabled>
      Stopping safely…
    </Button>
  )
}

function ContinueReviewDialog({
  state,
  busy,
  onClose,
  onChange,
  onConfirm,
}: {
  state: ContinueDialogState | null
  busy: boolean
  onClose: () => void
  onChange: (state: ContinueDialogState | null) => void
  onConfirm: (state: ContinueDialogState, commitSHA: string) => void
}) {
  const selectedSHA = state ? selectedCommitSHA(state) : ""
  const customInvalid = Boolean(
    state?.choice === "custom" &&
    state.customSHA.trim() &&
    !fullCommitSHA.test(state.customSHA.trim()),
  )
  return (
    <Dialog open={state !== null} onOpenChange={(open) => !open && onClose()}>
      {state && (
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Choose commit to continue</DialogTitle>
            <DialogDescription>
              The branch moved. Continue remembered work or select another exact
              commit.
            </DialogDescription>
          </DialogHeader>
          <fieldset className="space-y-3">
            <legend className="sr-only">Commit to review</legend>
            <CommitChoiceRow
              id="review-commit-remembered"
              label="Continue on remembered commit"
              repository={state.review.repository}
              commit={state.options.remembered}
              checked={state.choice === "remembered"}
              onChange={() => onChange({ ...state, choice: "remembered" })}
            />
            <CommitChoiceRow
              id="review-commit-latest"
              label="Continue on latest commit"
              repository={state.review.repository}
              commit={state.options.latest}
              checked={state.choice === "latest"}
              onChange={() => onChange({ ...state, choice: "latest" })}
            />
            <div className="rounded-lg border p-3">
              <label
                className="flex items-center gap-2 font-medium"
                htmlFor="review-commit-custom"
              >
                <input
                  id="review-commit-custom"
                  type="radio"
                  name="review-commit"
                  checked={state.choice === "custom"}
                  onChange={() => onChange({ ...state, choice: "custom" })}
                />
                Choose another commit
              </label>
              <Input
                className="mt-2"
                aria-label="Custom commit SHA"
                value={state.customSHA}
                aria-invalid={customInvalid}
                placeholder="Full 40- or 64-character commit SHA"
                onFocus={() => onChange({ ...state, choice: "custom" })}
                onChange={(event) =>
                  onChange({
                    ...state,
                    choice: "custom",
                    customSHA: event.target.value,
                  })
                }
              />
              {customInvalid && (
                <p role="alert" className="text-destructive mt-1 text-xs">
                  Enter a full hexadecimal commit SHA.
                </p>
              )}
            </div>
          </fieldset>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={onClose}
            >
              Cancel
            </Button>
            <Button
              type="button"
              disabled={busy || !fullCommitSHA.test(selectedSHA)}
              onClick={() => onConfirm(state, selectedSHA.toLowerCase())}
            >
              {busy ? "Continuing…" : "Continue review"}
            </Button>
          </DialogFooter>
        </DialogContent>
      )}
    </Dialog>
  )
}

function CommitChoiceRow({
  id,
  label,
  repository,
  commit,
  checked,
  onChange,
}: {
  id: string
  label: string
  repository: string
  commit: RepositoryReviewCommitOption
  checked: boolean
  onChange: () => void
}) {
  return (
    <div className="flex items-start gap-3 rounded-lg border p-3">
      <input
        id={id}
        type="radio"
        name="review-commit"
        checked={checked}
        onChange={onChange}
      />
      <div>
        <label htmlFor={id} className="cursor-pointer font-medium">
          {label}
        </label>
        <div className="mt-1">
          <CommitReference repository={repository} commit={commit} />
        </div>
      </div>
    </div>
  )
}

function CommitReference({
  repository,
  commit,
}: {
  repository: string
  commit: RepositoryReviewCommitOption
}) {
  const label = commit.short_sha || shortCommitSHA(commit.sha)
  const url = commit.url || githubCommitURL(repository, commit.sha)
  if (!url) return <code title={commit.sha}>{label}</code>
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      title={commit.sha}
      className="inline-flex items-center gap-1 font-mono underline underline-offset-2"
    >
      {label} <IconExternalLink className="size-3" aria-hidden="true" />
    </a>
  )
}

function RelatedButton({
  icon,
  label,
  detail,
  onClick,
}: {
  icon: React.ReactNode
  label: string
  detail: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      className="border-border hover:bg-muted/30 flex min-w-0 items-center gap-3 rounded-lg border p-4 text-left"
      onClick={onClick}
    >
      <span className="text-muted-foreground [&_svg]:size-5">{icon}</span>
      <span className="min-w-0">
        <span className="block font-medium">{label}</span>
        <span className="text-muted-foreground block text-xs">{detail}</span>
      </span>
    </button>
  )
}

function DetailRows({ rows }: { rows: Array<[string, string]> }) {
  return (
    <dl className="border-border divide-border rounded-lg border">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="grid gap-1 border-b px-3 py-3 last:border-b-0 sm:grid-cols-[10rem_minmax(0,1fr)]"
        >
          <dt className="text-muted-foreground text-sm">{label}</dt>
          <dd className="min-w-0 text-sm break-words">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="border-border rounded-lg border p-3">
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className="mt-1 text-lg font-medium tabular-nums">
        {new Intl.NumberFormat().format(value)}
      </p>
    </div>
  )
}

function selectedCommitSHA(state: ContinueDialogState): string {
  if (state.choice === "remembered") return state.options.remembered.sha.trim()
  if (state.choice === "latest") return state.options.latest.sha.trim()
  return state.customSHA.trim()
}

function progressLabel(review: RepositoryReviewAutomation): string {
  if (!review.progress.total_batches) return "Not started"
  return `${Math.round((review.progress.completed_batches / review.progress.total_batches) * 100)}%`
}

function resolvedCommit(
  review: RepositoryReviewAutomation,
): string | undefined {
  return review.resolved_commit_sha || review.scope_plan?.commit_sha
}

function isQueuedHandoff(review: RepositoryReviewAutomation): boolean {
  return (
    review.status === "idle" &&
    review.auto_continue &&
    review.progress.stage.trim().toLowerCase() === "next batch queued"
  )
}

function isActive(review: RepositoryReviewAutomation): boolean {
  return (
    review.status === "running" ||
    review.status === "stopping" ||
    isQueuedHandoff(review)
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}
