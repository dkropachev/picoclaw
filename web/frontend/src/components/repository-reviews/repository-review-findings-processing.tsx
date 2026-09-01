import { IconExternalLink, IconRefresh } from "@tabler/icons-react"
import { useMutation } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewFindingsProcessingCounters,
  type RepositoryReviewHistoricalConsolidation,
  restartRepositoryReviewHistoricalDeduplication,
  retryRepositoryReviewHistoricalDeduplication,
} from "@/api/repository-reviews"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import { repositoryReviewHistoricalConsolidationLabel } from "./repository-review-processing-labels"

export function RepositoryReviewHistoricalConsolidationNotice({
  automationID,
  consolidation,
  onRefresh,
}: {
  automationID: string
  consolidation?: RepositoryReviewHistoricalConsolidation
  onRefresh?: () => void | Promise<unknown>
}) {
  const [restartRequired, setRestartRequired] = useState(false)
  const [restartOpen, setRestartOpen] = useState(false)
  useEffect(() => {
    setRestartRequired(false)
    setRestartOpen(false)
  }, [automationID])
  useEffect(() => {
    if (!consolidation?.required || consolidation.status !== "failed") {
      setRestartRequired(false)
      setRestartOpen(false)
    }
  }, [consolidation?.required, consolidation?.status])
  const resume = useMutation({
    mutationFn: () =>
      retryRepositoryReviewHistoricalDeduplication(automationID),
    onSuccess: async () => {
      setRestartRequired(false)
      await onRefresh?.()
      toast.success("Historical consolidation resumed.")
    },
    onError: (error) => {
      if (
        error instanceof RepositoryReviewAPIError &&
        error.status === 409 &&
        error.code === "historical_consolidation_restart_required"
      ) {
        setRestartRequired(true)
        toast.warning(
          "Historical dependencies changed. Restart incompatible work to continue.",
        )
        void onRefresh?.()
        return
      }
      setRestartRequired(false)
      toast.error(
        error instanceof Error
          ? error.message
          : "Historical consolidation could not be resumed.",
      )
      void onRefresh?.()
    },
  })
  const restart = useMutation({
    mutationFn: () =>
      restartRepositoryReviewHistoricalDeduplication(automationID),
    onSuccess: async () => {
      setRestartRequired(false)
      setRestartOpen(false)
      await onRefresh?.()
      toast.success("Incompatible historical work restarted.")
    },
    onError: (error) => {
      if (
        error instanceof RepositoryReviewAPIError &&
        error.code === "historical_deduplication_campaign_recovery_required"
      ) {
        setRestartRequired(false)
        setRestartOpen(false)
      }
      toast.error(
        error instanceof Error
          ? error.message
          : "Historical consolidation could not be restarted.",
      )
      void onRefresh?.()
    },
  })
  if (
    !consolidation?.required ||
    consolidation.status === "not_required" ||
    consolidation.status === "completed"
  ) {
    return null
  }
  const failed = consolidation.status === "failed"
  return (
    <section
      className={
        failed
          ? "border-destructive/50 bg-destructive/5 rounded-lg border p-4"
          : "border-border rounded-lg border p-4"
      }
      role={failed ? "alert" : "status"}
      aria-labelledby="historical-consolidation-notice"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="historical-consolidation-notice" className="font-semibold">
            Historical consolidation
          </h2>
          <p className="text-muted-foreground mt-1 text-sm">
            {failed
              ? "Historical findings could not be consolidated into the canonical repository ledger."
              : "Historical findings are being replayed into the canonical repository ledger."}
          </p>
          {restartRequired && (
            <p className="text-muted-foreground mt-1 text-sm">
              The current profile or campaign no longer matches the saved
              checkpoints. Resume cannot continue until incompatible work is
              restarted.
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline">
            {repositoryReviewHistoricalConsolidationLabel(consolidation.status)}
          </Badge>
          {failed && consolidation.retryable && !restartRequired && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={resume.isPending}
              onClick={() => resume.mutate()}
            >
              <IconRefresh />
              {resume.isPending
                ? "Resuming…"
                : "Resume historical consolidation"}
            </Button>
          )}
          {failed && consolidation.retryable && restartRequired && (
            <Button
              type="button"
              size="sm"
              variant="destructive"
              disabled={restart.isPending}
              onClick={() => setRestartOpen(true)}
            >
              <IconRefresh />
              {restart.isPending ? "Restarting…" : "Restart incompatible work"}
            </Button>
          )}
        </div>
      </div>
      <AlertDialog open={restartOpen} onOpenChange={setRestartOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Restart incompatible historical work?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Completed results in affected historical buckets will be
              reprocessed. Completed work in unrelated buckets will remain
              preserved.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={restart.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={restart.isPending}
              onClick={(event) => {
                event.preventDefault()
                restart.mutate()
              }}
            >
              {restart.isPending ? "Restarting…" : "Restart incompatible work"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

export function RepositoryReviewFindingsProcessing({
  counters,
  onOpenRawFindings,
}: {
  counters?: RepositoryReviewFindingsProcessingCounters
  onOpenRawFindings?: () => void
}) {
  if (!counters) return null
  return (
    <section
      aria-labelledby="findings-processing-summary"
      className="border-border space-y-3 rounded-lg border p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="findings-processing-summary" className="font-semibold">
            Raw finding processing
          </h2>
          <p className="text-muted-foreground mt-1 text-sm">
            Raw validated diagnoses remain inspectable while completed sources
            are grouped into findings.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {onOpenRawFindings && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={onOpenRawFindings}
            >
              <IconExternalLink /> Raw findings
            </Button>
          )}
        </div>
      </div>

      <dl className="grid grid-cols-2 gap-2 text-sm sm:grid-cols-4 lg:grid-cols-7">
        <Counter label="Raw" value={counters.raw_total} />
        <Counter label="Completed" value={counters.completed} />
        <Counter label="New" value={counters.new} />
        <Counter label="Duplicates" value={counters.duplicates} />
        <Counter label="Pending" value={counters.pending} />
        <Counter label="Processing" value={counters.processing} />
        <Counter label="Failed" value={counters.failed} />
      </dl>
    </section>
  )
}

function Counter({ label, value }: { label: string; value: number }) {
  return (
    <div className="bg-muted/20 rounded-md p-2">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="mt-0.5 font-medium tabular-nums">{value}</dd>
    </div>
  )
}
