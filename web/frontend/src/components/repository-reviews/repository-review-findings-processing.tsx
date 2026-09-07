import { IconExternalLink } from "@tabler/icons-react"

import type { RepositoryReviewFindingsProcessingCounters } from "@/api/repository-reviews"
import { Button } from "@/components/ui/button"

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
