import {
  IconAlertTriangle,
  IconPlayerPause,
  IconPlayerPlay,
  IconRefresh,
  IconRotateClockwise,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import {
  type RepositoryReviewAutomation,
  type ReviewAccountOption,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewAutomations,
  pauseRepositoryReviewAutomation,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

const runsKey = ["repository-review-automations"] as const
const activeStatuses = new Set(["running", "stopping"])

export function RepositoryReviewRunsPage() {
  const queryClient = useQueryClient()
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
    }: {
      run: RepositoryReviewAutomation
      action: "start" | "pause" | "resume" | "restart"
    }) => {
      const input = { expected_version: run.version }
      if (action === "start")
        return startRepositoryReviewAutomation(run.id, input)
      if (action === "pause")
        return pauseRepositoryReviewAutomation(run.id, input)
      if (action === "resume")
        return resumeRepositoryReviewAutomation(run.id, input)
      return restartRepositoryReviewAutomation(run.id, input)
    },
    onSuccess: (updated) => {
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
  const runs = runsQuery.data?.automations ?? []

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
          {mutation.isError && (
            <div
              role="alert"
              className="text-destructive flex items-center gap-2 text-sm"
            >
              <IconAlertTriangle className="size-4" />
              {errorMessage(mutation.error)}
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
                  busy={mutation.isPending}
                  onAction={(action) => mutation.mutate({ run, action })}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function RunCard({
  run,
  accounts,
  busy,
  onAction,
}: {
  run: RepositoryReviewAutomation
  accounts: ReviewAccountOption[]
  busy: boolean
  onAction: (action: "start" | "pause" | "resume" | "restart") => void
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
        {run.scope_plan?.commit_sha && (
          <p className="text-muted-foreground text-xs break-all">
            Resolved commit {run.scope_plan.commit_sha}
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
          {run.status === "running" && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => onAction("pause")}
            >
              <IconPlayerPause /> Pause safely
            </Button>
          )}
          {run.status === "paused" && (
            <Button
              size="sm"
              disabled={busy}
              onClick={() => onAction("resume")}
            >
              <IconPlayerPlay /> Resume
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
