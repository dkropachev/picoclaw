import {
  IconAlertTriangle,
  IconExternalLink,
  IconPlayerPause,
  IconPlayerPlay,
  IconRefresh,
  IconRotateClockwise,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"

import {
  type RepositoryReviewAutomation,
  type RepositoryReviewCommitOption,
  type RepositoryReviewCommitOptions,
  type ReviewAccountOption,
  getRepositoryReviewAutomationOptions,
  getRepositoryReviewCommitOptions,
  listRepositoryReviewAutomations,
  pauseRepositoryReviewAutomation,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { PageHeader } from "@/components/page-header"
import {
  githubCommitURL,
  shortCommitSHA,
} from "@/components/repository-reviews/repository-review-actions"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"

const runsKey = ["repository-review-automations"] as const
const activeStatuses = new Set(["running", "stopping"])
const fullCommitSHA = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/iu

type ReviewRunAction = "start" | "pause" | "resume" | "restart"
type CommitChoice = "remembered" | "latest" | "custom"

interface ContinueDialogState {
  run: RepositoryReviewAutomation
  options: RepositoryReviewCommitOptions
  choice: CommitChoice
  customSHA: string
}

export function RepositoryReviewRunsPage() {
  const queryClient = useQueryClient()
  const [continueDialog, setContinueDialog] =
    useState<ContinueDialogState | null>(null)
  const runsQuery = useQuery({
    queryKey: runsKey,
    queryFn: ({ signal }) => listRepositoryReviewAutomations(signal),
    refetchInterval: (query) =>
      (query.state.data?.automations ?? []).some(
        (run) => activeStatuses.has(run.status) || isQueuedHandoff(run),
      )
        ? 2_000
        : false,
  })
  const optionsQuery = useQuery({
    queryKey: ["repository-review-automation-options"],
    queryFn: ({ signal }) => getRepositoryReviewAutomationOptions(signal),
  })
  const mutation = useMutation({
    mutationFn: async ({
      run,
      action,
      expectedVersion,
      commitSHA,
    }: {
      run: RepositoryReviewAutomation
      action: ReviewRunAction
      expectedVersion?: number
      commitSHA?: string
    }) => {
      const input = { expected_version: expectedVersion ?? run.version }
      const observedRunID = run.active_run_id || run.run_ids.at(-1)
      if (action === "start")
        return startRepositoryReviewAutomation(run.id, input)
      if (action === "pause")
        return pauseRepositoryReviewAutomation(run.id, {
          ...input,
          ...(observedRunID ? { run_id: observedRunID } : {}),
        })
      if (action === "resume")
        return resumeRepositoryReviewAutomation(run.id, {
          ...input,
          ...(commitSHA ? { commit_sha: commitSHA } : {}),
        })
      return restartRepositoryReviewAutomation(run.id, input)
    },
    onSuccess: (updated) => {
      setContinueDialog(null)
      queryClient.setQueryData<{ automations: RepositoryReviewAutomation[] }>(
        runsKey,
        (current) => ({
          automations: (current?.automations ?? []).map((run) =>
            run.id === updated.id ? updated : run,
          ),
        }),
      )
    },
    onError: () => void runsQuery.refetch(),
  })
  const commitOptionsMutation = useMutation({
    mutationFn: (run: RepositoryReviewAutomation) =>
      getRepositoryReviewCommitOptions(run.id),
    onSuccess: (commitOptions, run) => {
      if (!commitOptions.newer_commit_available) {
        mutation.mutate({
          run,
          action: "resume",
          expectedVersion: commitOptions.expected_version,
        })
        return
      }
      setContinueDialog({
        run,
        options: commitOptions,
        choice: "remembered",
        customSHA: "",
      })
    },
    onError: () => void runsQuery.refetch(),
  })
  const runs = runsQuery.data?.automations ?? []
  const actionError = mutation.error || commitOptionsMutation.error

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title="Review runs">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={runsQuery.isFetching}
          onClick={() => void runsQuery.refetch()}
        >
          <IconRefresh /> Refresh
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 md:px-6">
        <div className="mx-auto max-w-6xl space-y-4">
          <div className="text-muted-foreground text-sm">
            Start and monitor durable repository reviews. Each run uses its
            repository&apos;s assigned profile and one reviewer model.
          </div>
          {actionError && (
            <div
              role="alert"
              className="text-destructive flex items-center gap-2 text-sm"
            >
              <IconAlertTriangle className="size-4" />
              {errorMessage(actionError)}
            </div>
          )}
          {runsQuery.isPending ? (
            <EmptyCard text="Loading review runs…" />
          ) : runsQuery.isError ? (
            <EmptyCard text="Review runs could not be loaded." />
          ) : runs.length === 0 ? (
            <EmptyCard text="No repository configured. Assign a profile from Repositories first." />
          ) : (
            <div className="grid gap-4 xl:grid-cols-2">
              {runs.map((run) => (
                <RunCard
                  key={run.id}
                  run={run}
                  accounts={optionsQuery.data?.accounts ?? []}
                  busy={mutation.isPending || commitOptionsMutation.isPending}
                  resolvingCommit={
                    commitOptionsMutation.isPending &&
                    commitOptionsMutation.variables?.id === run.id
                  }
                  onAction={(action) => mutation.mutate({ run, action })}
                  onContinue={() => {
                    mutation.reset()
                    commitOptionsMutation.mutate(run)
                  }}
                />
              ))}
            </div>
          )}
        </div>
      </div>
      <ContinueReviewDialog
        state={continueDialog}
        busy={mutation.isPending}
        error={continueDialog && mutation.isError ? mutation.error : null}
        onClose={() => setContinueDialog(null)}
        onChange={setContinueDialog}
        onConfirm={(state, commitSHA) =>
          mutation.mutate({
            run: state.run,
            action: "resume",
            expectedVersion: state.options.expected_version,
            commitSHA,
          })
        }
      />
    </div>
  )
}

function RunCard({
  run,
  accounts,
  busy,
  resolvingCommit,
  onAction,
  onContinue,
}: {
  run: RepositoryReviewAutomation
  accounts: ReviewAccountOption[]
  busy: boolean
  resolvingCommit: boolean
  onAction: (action: Exclude<ReviewRunAction, "resume">) => void
  onContinue: () => void
}) {
  const branch = run.branch || run.ref
  const model = run.reviewer_models[0] || "Profile model unavailable"
  const effectiveAccountRef = run.effective_account_ref || run.account_ref
  const account = effectiveAccountRef
    ? accounts.find((candidate) => candidate.id === effectiveAccountRef)
    : accounts.find((candidate) => candidate.default)
  const accountLabel = effectiveAccountRef
    ? account?.label || effectiveAccountRef
    : account?.label
      ? `Default (${account.label})`
      : "Default account"
  const priceKnown =
    run.reviewer_models.length > 0 &&
    run.reviewer_models.every((reviewer) => {
      const price = run.model_prices[reviewer]
      return Boolean(
        price &&
        (price.input_price_per_1m > 0 || price.output_price_per_1m > 0),
      )
    })
  const progress = run.progress.total_batches
    ? Math.round(
        (run.progress.completed_batches / run.progress.total_batches) * 100,
      )
    : 0
  const handoffQueued = isQueuedHandoff(run)
  const resolvedCommitSHA =
    run.resolved_commit_sha || run.scope_plan?.commit_sha

  return (
    <Card size="sm" data-testid={`review-run-${run.id}`}>
      <CardHeader>
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <CardTitle className="truncate">{run.repository}</CardTitle>
            <CardDescription className="mt-1">
              {run.name} ·{" "}
              {branch ? `Branch ${branch}` : "Default repository branch"} ·{" "}
              {model}
            </CardDescription>
          </div>
          <Badge variant="secondary">
            {handoffQueued ? "continuing" : run.status}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Metric label="Stage" value={run.progress.stage || "waiting"} />
          <Metric
            label="Progress"
            value={run.progress.total_batches ? `${progress}%` : "Not started"}
          />
          <Metric label="Reviewed" value={run.progress.reviewed_files} />
          <Metric label="Findings" value={run.progress.findings} />
        </div>
        <div className="text-muted-foreground grid gap-1 text-xs sm:grid-cols-2">
          <span>{formatInteger(run.usage.total_tokens)} tokens used</span>
          <span>
            {priceKnown
              ? `$${run.estimated_cost_usd.toFixed(2)} estimated cost`
              : "Estimated cost unknown"}
          </span>
          <span>{run.progress.remaining_files} files remaining</span>
          <span>{run.progress.unsupported_files} unsupported files</span>
          <span>Account: {accountLabel}</span>
        </div>
        {resolvedCommitSHA && (
          <p className="text-muted-foreground text-xs break-all">
            Resolved commit{" "}
            <CommitReference
              repository={run.repository}
              commit={{
                sha: resolvedCommitSHA,
                short_sha: shortCommitSHA(resolvedCommitSHA),
              }}
            />
          </p>
        )}
        {run.pause_detail && (
          <p className="bg-muted rounded-md p-3 text-sm">
            {run.status === "failed" ? "Failed" : "Paused"}: {run.pause_detail}
          </p>
        )}
        {run.run_ids.length > 0 && (
          <details className="rounded-lg border p-3 text-xs">
            <summary className="cursor-pointer font-medium">
              Run history ({run.run_ids.length})
            </summary>
            <ul className="text-muted-foreground mt-2 space-y-1">
              {run.run_ids
                .slice()
                .reverse()
                .map((runID) => (
                  <li key={runID} className="break-all">
                    {runID}
                  </li>
                ))}
            </ul>
          </details>
        )}
        <div className="flex flex-wrap gap-2 border-t pt-4">
          {run.status === "idle" && !handoffQueued && (
            <Button size="sm" disabled={busy} onClick={() => onAction("start")}>
              <IconPlayerPlay /> Start review
            </Button>
          )}
          {(run.status === "running" || handoffQueued) && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => onAction("pause")}
            >
              <IconPlayerPause /> Stop safely
            </Button>
          )}
          {run.status === "paused" && (
            <Button size="sm" disabled={busy} onClick={onContinue}>
              <IconPlayerPlay />
              {resolvingCommit ? "Resolving commits…" : "Continue review"}
            </Button>
          )}
          {(run.status === "completed" || run.status === "failed") && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => onAction("restart")}
            >
              <IconRotateClockwise /> Run again
            </Button>
          )}
          {run.status === "stopping" && (
            <Button size="sm" variant="outline" disabled>
              Stopping safely…
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function ContinueReviewDialog({
  state,
  busy,
  error,
  onClose,
  onChange,
  onConfirm,
}: {
  state: ContinueDialogState | null
  busy: boolean
  error: unknown
  onClose: () => void
  onChange: (state: ContinueDialogState | null) => void
  onConfirm: (state: ContinueDialogState, commitSHA: string) => void
}) {
  const selectedSHA = state ? selectedCommitSHA(state) : ""
  const customSHA = state?.customSHA.trim() ?? ""
  const customInvalid = Boolean(
    state?.choice === "custom" && customSHA && !fullCommitSHA.test(customSHA),
  )
  const canContinue = Boolean(selectedSHA && fullCommitSHA.test(selectedSHA))

  return (
    <Dialog
      open={state !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      {state && (
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Choose commit to continue</DialogTitle>
            <DialogDescription>
              Newer commit found for {state.run.repository}. Continue from
              remembered work or move review to another exact commit.
            </DialogDescription>
          </DialogHeader>

          {error != null && (
            <div
              role="alert"
              className="text-destructive flex items-center gap-2 text-sm"
            >
              <IconAlertTriangle className="size-4" />
              {errorMessage(error)}
            </div>
          )}

          <fieldset className="space-y-3">
            <legend className="sr-only">Commit to review</legend>
            <CommitOptionChoice
              id="review-commit-remembered"
              label="Continue on remembered commit"
              repository={state.run.repository}
              commit={state.options.remembered}
              checked={state.choice === "remembered"}
              onChange={() => onChange({ ...state, choice: "remembered" })}
            />
            <CommitOptionChoice
              id="review-commit-latest"
              label="Continue on latest commit"
              repository={state.run.repository}
              commit={state.options.latest}
              checked={state.choice === "latest"}
              onChange={() => onChange({ ...state, choice: "latest" })}
            />
            <div className="rounded-lg border p-3">
              <div className="flex items-start gap-3">
                <input
                  id="review-commit-custom"
                  type="radio"
                  name="review-commit"
                  className="mt-1"
                  checked={state.choice === "custom"}
                  onChange={() => onChange({ ...state, choice: "custom" })}
                />
                <div className="min-w-0 flex-1 space-y-2">
                  <label
                    htmlFor="review-commit-custom"
                    className="cursor-pointer font-medium"
                  >
                    Choose another commit
                  </label>
                  <label htmlFor="review-custom-commit-sha" className="sr-only">
                    Custom commit SHA
                  </label>
                  <Input
                    id="review-custom-commit-sha"
                    value={state.customSHA}
                    placeholder="Full 40- or 64-character commit SHA"
                    aria-invalid={customInvalid}
                    aria-describedby="review-custom-commit-help"
                    onFocus={() => onChange({ ...state, choice: "custom" })}
                    onChange={(event) =>
                      onChange({
                        ...state,
                        choice: "custom",
                        customSHA: event.target.value,
                      })
                    }
                  />
                  <p
                    id="review-custom-commit-help"
                    className={
                      customInvalid
                        ? "text-destructive text-xs"
                        : "text-muted-foreground text-xs"
                    }
                  >
                    {customInvalid
                      ? "Enter a full 40- or 64-character hexadecimal SHA."
                      : "Commit must exist in this repository."}
                  </p>
                  {state.choice === "custom" &&
                    fullCommitSHA.test(customSHA) && (
                      <CommitReference
                        repository={state.run.repository}
                        commit={{
                          sha: customSHA,
                          short_sha: shortCommitSHA(customSHA),
                        }}
                      />
                    )}
                </div>
              </div>
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
              disabled={busy || !canContinue}
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

function CommitOptionChoice({
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
        className="mt-1"
        checked={checked}
        aria-describedby={`${id}-reference`}
        onChange={onChange}
      />
      <div className="min-w-0 space-y-1">
        <label htmlFor={id} className="cursor-pointer font-medium">
          {label}
        </label>
        <div id={`${id}-reference`}>
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
  if (!url) {
    return (
      <code className="text-foreground" title={commit.sha}>
        {label}
      </code>
    )
  }
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      title={commit.sha}
      className="text-foreground inline-flex items-center gap-1 font-mono underline underline-offset-2"
    >
      {label}
      <IconExternalLink className="size-3" aria-hidden="true" />
    </a>
  )
}

function selectedCommitSHA(state: ContinueDialogState): string {
  if (state.choice === "remembered") return state.options.remembered.sha.trim()
  if (state.choice === "latest") return state.options.latest.sha.trim()
  return state.customSHA.trim()
}

function Metric({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="bg-muted/50 rounded-md p-2">
      <div className="text-muted-foreground text-[0.7rem]">{label}</div>
      <div className="truncate text-sm font-medium">{value}</div>
    </div>
  )
}

function EmptyCard({ text }: { text: string }) {
  return (
    <Card size="sm" className="border-dashed">
      <CardContent className="text-muted-foreground py-10 text-center text-sm">
        {text}
      </CardContent>
    </Card>
  )
}

function formatInteger(value: number): string {
  return new Intl.NumberFormat().format(value)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Review action failed."
}

function isQueuedHandoff(run: RepositoryReviewAutomation): boolean {
  return (
    run.status === "idle" &&
    run.auto_continue &&
    run.progress.stage.trim().toLowerCase() === "next batch queued"
  )
}
