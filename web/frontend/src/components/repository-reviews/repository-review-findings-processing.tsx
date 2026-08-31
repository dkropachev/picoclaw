import { IconExternalLink, IconRefresh } from "@tabler/icons-react"

import type {
  RepositoryReviewFindingsProcessingCounters,
  RepositoryReviewHistoricalDeduplication,
} from "@/api/repository-reviews"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export function RepositoryReviewFindingsProcessing({
  counters,
  historical,
  retryingHistorical = false,
  onRetryHistorical,
  onOpenRawFindings,
}: {
  counters?: RepositoryReviewFindingsProcessingCounters
  historical?: RepositoryReviewHistoricalDeduplication
  retryingHistorical?: boolean
  onRetryHistorical?: () => void
  onOpenRawFindings?: () => void
}) {
  if (!counters && !historical?.required) return null
  const replayActive =
    historical?.required &&
    new Set(["pending", "replaying", "merging"]).has(historical.status ?? "")
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
          {historical?.required && historical.status && (
            <Badge
              variant={
                historical.status === "failed" ? "destructive" : "outline"
              }
            >
              Historical replay {historical.status}
            </Badge>
          )}
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

      {counters && (
        <dl className="grid grid-cols-2 gap-2 text-sm sm:grid-cols-4 lg:grid-cols-7">
          <Counter label="Raw" value={counters.raw_total} />
          <Counter label="Completed" value={counters.completed} />
          <Counter label="New" value={counters.new} />
          <Counter label="Duplicates" value={counters.duplicates} />
          <Counter label="Pending" value={counters.pending} />
          <Counter label="Processing" value={counters.processing} />
          <Counter label="Failed" value={counters.failed} />
        </dl>
      )}

      {replayActive && (
        <p className="text-muted-foreground text-sm" role="status">
          Historical findings are being replayed as one review campaign.
        </p>
      )}
      {historical?.required && historical.status === "failed" && (
        <div
          className="border-destructive/50 bg-destructive/5 flex flex-wrap items-start justify-between gap-3 rounded-md border p-3 text-sm"
          role="alert"
        >
          <div>
            <p className="font-medium">Historical deduplication failed</p>
            <p className="text-muted-foreground mt-1">
              {historical.error ||
                "Inspect the raw findings, then retry the complete historical replay."}
            </p>
          </div>
          {onRetryHistorical && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={retryingHistorical}
              onClick={onRetryHistorical}
            >
              <IconRefresh />
              {retryingHistorical
                ? "Retrying…"
                : "Retry historical deduplication"}
            </Button>
          )}
        </div>
      )}
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
