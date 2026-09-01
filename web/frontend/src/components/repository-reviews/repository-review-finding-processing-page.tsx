import {
  IconExternalLink,
  IconFileCode,
  IconRefresh,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useEffect } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  getRepositoryReviewFindingsProcessingSource,
  retryRepositoryReviewFindingsProcessingSource,
} from "@/api/repository-reviews"
import { CollectionDetailShell } from "@/components/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import {
  repositoryReviewFindingHealthNeedsPolling,
  useRepositoryReviewFindingHealth,
} from "./repository-review-finding-health"
import { RepositoryReviewHistoricalConsolidationNotice } from "./repository-review-findings-processing"
import {
  repositoryReviewProcessingDispositionLabel,
  repositoryReviewProcessingStateLabel,
} from "./repository-review-processing-labels"

export function RepositoryReviewFindingProcessingPage({
  automationID,
  sourceID,
  onBack,
  onCanonicalSource,
  onOpenFinding,
  onOpenRepositoryFinding,
}: {
  automationID: string
  sourceID: string
  onBack: () => void
  onCanonicalSource: (sourceID: string) => void
  onOpenFinding: (findingID: string) => void
  onOpenRepositoryFinding: (findingID: string) => void
}) {
  const healthQuery = useRepositoryReviewFindingHealth(automationID)
  const query = useQuery({
    queryKey: ["repository-review-finding-processing", automationID, sourceID],
    queryFn: ({ signal }) =>
      getRepositoryReviewFindingsProcessingSource(
        automationID,
        sourceID,
        signal,
      ),
    retry: false,
    refetchInterval: (current) => {
      const detail = current.state.data
      return detail?.source.deduplication_state === "pending" ||
        detail?.source.deduplication_state === "running" ||
        repositoryReviewFindingHealthNeedsPolling(
          detail?.automation,
          healthQuery.data,
        )
        ? 2_000
        : false
    },
  })
  const detail = query.data
  const source = detail?.source
  useEffect(() => {
    if (source?.id && source.id !== sourceID) onCanonicalSource(source.id)
  }, [onCanonicalSource, source?.id, sourceID])
  const retryMutation = useMutation({
    mutationFn: () =>
      retryRepositoryReviewFindingsProcessingSource(
        automationID,
        source?.id || sourceID,
      ),
    onSuccess: async () => {
      await Promise.all([query.refetch(), healthQuery.refetch()])
      toast.success("Finding processing queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Finding processing could not be retried.",
      )
      void query.refetch()
    },
  })
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const historicalSource = Boolean(
    source?.assignment_id === "historical-replay" ||
    (source?.legacy_finding_id && !source.assignment_id),
  )
  const canRetry = Boolean(
    source?.deduplication_state === "failed" &&
    source.failure?.retryable &&
    !historicalSource,
  )
  const findingID = source?.deduplicated_finding_id || detail?.finding?.id
  const repositoryFindingID = detail?.repository_finding?.id

  return (
    <CollectionDetailShell
      title={source?.title || "Finding processing"}
      identity={
        <span
          className="block max-w-40 truncate font-mono text-xs sm:max-w-72"
          title={source?.id || sourceID}
        >
          {source?.id || sourceID}
        </span>
      }
      status={
        source ? (
          <div className="flex flex-wrap gap-2">
            <Badge variant="outline">{source.severity}</Badge>
            <Badge variant="outline">
              {repositoryReviewProcessingStateLabel(source.deduplication_state)}
            </Badge>
            <Badge variant="secondary">
              {repositoryReviewProcessingDispositionLabel(source.disposition)}
            </Badge>
          </div>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Findings processing"
    >
      {detail && source && (
        <div className="space-y-6">
          {source.failure && (
            <section
              className="border-destructive/50 bg-destructive/5 space-y-3 rounded-lg border p-4"
              aria-labelledby="finding-processing-failure"
              role="alert"
            >
              <div>
                <h2 id="finding-processing-failure" className="font-semibold">
                  Processing failed
                </h2>
                <p className="text-muted-foreground mt-1 text-sm">
                  {source.failure.message}
                </p>
                <p className="text-muted-foreground mt-1 text-xs">
                  {source.failure.code} · {formatTimestamp(source.failure.at)}
                </p>
              </div>
              {canRetry && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={retryMutation.isPending}
                  onClick={() => retryMutation.mutate()}
                >
                  <IconRefresh />
                  {retryMutation.isPending ? "Retrying…" : "Retry"}
                </Button>
              )}
            </section>
          )}

          <RepositoryReviewHistoricalConsolidationNotice
            automationID={automationID}
            consolidation={
              detail.health?.historical_consolidation ??
              detail.historical_consolidation ??
              healthQuery.data?.historical_consolidation
            }
            onRefresh={() =>
              Promise.all([query.refetch(), healthQuery.refetch()])
            }
          />

          {(findingID || repositoryFindingID) && (
            <section className="border-border flex flex-wrap items-center justify-between gap-3 rounded-lg border p-4">
              <div>
                <h2 className="font-semibold">Linked findings</h2>
                <p className="text-muted-foreground mt-1 text-sm">
                  Open the deduplicated diagnosis or its canonical repository
                  identity.
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                {findingID && (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => onOpenFinding(findingID)}
                  >
                    <IconExternalLink /> Deduplicated finding
                  </Button>
                )}
                {repositoryFindingID && (
                  <Button
                    type="button"
                    size="sm"
                    onClick={() => onOpenRepositoryFinding(repositoryFindingID)}
                  >
                    <IconExternalLink /> Repository finding
                  </Button>
                )}
              </div>
            </section>
          )}

          <section
            aria-labelledby="finding-processing-diagnosis"
            className="space-y-3"
          >
            <h2 id="finding-processing-diagnosis" className="font-semibold">
              Immutable diagnosis
            </h2>
            <dl className="border-border divide-border rounded-lg border">
              <FindingText
                label="Mechanism"
                value={source.message || source.evidence || "Not reported"}
              />
              <FindingText
                label="Evidence"
                value={source.evidence || "Not reported"}
              />
              <FindingText
                label="Impact"
                value={source.impact || "Not reported"}
              />
              <FindingText
                label="Validation"
                value={
                  source.validation
                    ? `${source.validation.status} — ${source.validation.summary}`
                    : "Not reported"
                }
              />
            </dl>
            {(source.validation?.checks?.length ?? 0) > 0 && (
              <ul className="border-border text-muted-foreground list-inside list-disc rounded-lg border p-4 text-sm">
                {source.validation?.checks?.map((check) => (
                  <li key={check} className="break-words">
                    {check}
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section
            aria-labelledby="finding-processing-provenance"
            className="space-y-3"
          >
            <h2 id="finding-processing-provenance" className="font-semibold">
              Provenance
            </h2>
            <dl className="border-border bg-muted/20 grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
              <DetailRow
                icon={<IconFileCode className="size-4" />}
                label="File"
                value={`${source.file?.path || source.path || "Unknown path"}${source.line == null ? "" : `:${source.line}`}`}
                mono
              />
              <DetailRow
                label="Symbol"
                value={source.symbol || "Not reported"}
                mono
              />
              <DetailRow
                label="Commit SHA"
                value={source.commit_sha || "Not reported"}
                mono
              />
              <DetailRow
                label="Blob SHA"
                value={source.file?.blob_sha || "Not reported"}
                mono
              />
              <DetailRow
                label="Repository"
                value={source.repository || detail.automation.repository}
              />
              <DetailRow
                label="Campaign"
                value={source.campaign_id || "Not reported"}
                mono
              />
              <DetailRow
                label="Run"
                value={source.run_id || "Not reported"}
                mono
              />
              <DetailRow
                label="Assignment"
                value={source.assignment_id || "Not reported"}
                mono
              />
              <DetailRow
                label="Context"
                value={
                  source.context_id || detail.context?.id || "Not reported"
                }
                mono
              />
              <DetailRow label="Model" value={source.model || "Not reported"} />
              <DetailRow
                label="Model alias"
                value={source.model_alias || "Not reported"}
              />
              <DetailRow
                label="Account"
                value={source.account || "Not reported"}
              />
              <DetailRow
                label="Reviewer"
                value={source.reviewer || "Not reported"}
              />
              <DetailRow
                label="Created"
                value={formatTimestamp(source.created_at)}
              />
              <DetailRow
                label="Updated"
                value={formatTimestamp(source.updated_at)}
              />
            </dl>
          </section>
        </div>
      )}
    </CollectionDetailShell>
  )
}

function DetailRow({
  label,
  value,
  mono = false,
  icon,
}: {
  label: string
  value: string
  mono?: boolean
  icon?: React.ReactNode
}) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground flex items-center gap-1 text-xs">
        {icon}
        {label}
      </dt>
      <dd className={`mt-0.5 break-words ${mono ? "font-mono text-xs" : ""}`}>
        {value}
      </dd>
    </div>
  )
}

function FindingText({ label, value }: { label: string; value: string }) {
  return (
    <div className="p-4">
      <dt className="text-muted-foreground text-xs font-medium uppercase">
        {label}
      </dt>
      <dd className="mt-1 text-sm break-words whitespace-pre-wrap">{value}</dd>
    </div>
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}
