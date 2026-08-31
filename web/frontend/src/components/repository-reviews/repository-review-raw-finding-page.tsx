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
  getRepositoryReviewRawSource,
  retryRepositoryReviewRawSource,
} from "@/api/repository-reviews"
import { CollectionDetailShell } from "@/components/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export function RepositoryReviewRawFindingPage({
  automationID,
  sourceID,
  onBack,
  onCanonicalSource,
  onOpenFinding,
}: {
  automationID: string
  sourceID: string
  onBack: () => void
  onCanonicalSource: (sourceID: string) => void
  onOpenFinding: (findingID: string) => void
}) {
  const query = useQuery({
    queryKey: ["repository-review-raw-finding", automationID, sourceID],
    queryFn: ({ signal }) =>
      getRepositoryReviewRawSource(automationID, sourceID, signal),
    retry: false,
    refetchInterval: (current) => {
      const state = current.state.data?.source.deduplication_state
      return state === "pending" || state === "running" ? 2_000 : false
    },
  })
  const detail = query.data
  const source = detail?.source
  const parentFindingID = source?.deduplicated_finding_id || detail?.finding?.id
  const canRetrySource = Boolean(
    source?.failure?.retryable &&
    source.assignment_id !== "historical-replay" &&
    !(source.legacy_finding_id && !source.assignment_id),
  )
  useEffect(() => {
    if (source?.id && source.id !== sourceID) onCanonicalSource(source.id)
  }, [onCanonicalSource, source?.id, sourceID])
  const retryMutation = useMutation({
    mutationFn: () =>
      retryRepositoryReviewRawSource(automationID, source?.id || sourceID),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Raw finding deduplication queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Raw finding deduplication could not be retried.",
      )
      void query.refetch()
    },
  })
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404

  return (
    <CollectionDetailShell
      title={source?.title || "Raw finding"}
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
            <Badge variant={severityVariant(source.severity)}>
              {source.severity}
            </Badge>
            <Badge variant="outline">{source.deduplication_state}</Badge>
            <Badge variant="secondary">{source.disposition}</Badge>
          </div>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Raw findings"
    >
      {detail && source && (
        <div className="space-y-6">
          {parentFindingID && (
            <section className="border-border flex flex-wrap items-center justify-between gap-3 rounded-lg border p-4">
              <div>
                <h2 className="font-semibold">Deduplicated finding</h2>
                <p className="text-muted-foreground mt-1 font-mono text-xs">
                  {parentFindingID}
                </p>
              </div>
              <Button
                type="button"
                size="sm"
                onClick={() => onOpenFinding(parentFindingID)}
              >
                <IconExternalLink /> Open finding
              </Button>
            </section>
          )}
          {source.failure && (
            <section
              className="border-destructive/50 bg-destructive/5 space-y-3 rounded-lg border p-4"
              aria-labelledby="raw-finding-failure"
              role="alert"
            >
              <div>
                <h2 id="raw-finding-failure" className="font-semibold">
                  Deduplication failed
                </h2>
                <p className="text-muted-foreground mt-1 text-sm">
                  {source.failure.message}
                </p>
                <p className="text-muted-foreground mt-1 text-xs">
                  {source.failure.code} · {formatTimestamp(source.failure.at)}
                </p>
              </div>
              {canRetrySource && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={retryMutation.isPending}
                  onClick={() => retryMutation.mutate()}
                >
                  <IconRefresh />
                  {retryMutation.isPending ? "Retrying…" : "Retry raw finding"}
                </Button>
              )}
            </section>
          )}

          <section aria-labelledby="raw-finding-location" className="space-y-3">
            <h2 id="raw-finding-location" className="font-semibold">
              Location and provenance
            </h2>
            <dl className="border-border bg-muted/20 grid gap-2 rounded-lg border p-4 text-sm">
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

          <section
            aria-labelledby="raw-finding-diagnosis"
            className="space-y-3"
          >
            <h2 id="raw-finding-diagnosis" className="font-semibold">
              Raw diagnosis
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
                  <li key={check}>{check}</li>
                ))}
              </ul>
            )}
          </section>

          {source.match_hints && (
            <section
              aria-labelledby="raw-finding-identity-hints"
              className="space-y-3"
            >
              <h2 id="raw-finding-identity-hints" className="font-semibold">
                Causal identity hints
              </h2>
              <dl className="border-border divide-border rounded-lg border">
                <FindingText
                  label="Component"
                  value={source.match_hints.component || "Unknown"}
                />
                <FindingText
                  label="Operation"
                  value={source.match_hints.operation || "Unknown"}
                />
                <FindingText
                  label="Failure mode"
                  value={source.match_hints.failure_mode || "Unknown"}
                />
                <FindingText
                  label="Trigger"
                  value={source.match_hints.trigger || "Unknown"}
                />
                <FindingText
                  label="Violated invariant"
                  value={source.match_hints.violated_invariant || "Unknown"}
                />
                <FindingText
                  label="Observable outcome"
                  value={source.match_hints.observable_outcome || "Unknown"}
                />
                <FindingText
                  label="Related symbols"
                  value={
                    source.match_hints.related_symbols.join(", ") || "None"
                  }
                />
                <FindingText
                  label="Source anchors"
                  value={source.match_hints.source_anchors.join(", ") || "None"}
                />
                <FindingText
                  label="Distinguishing facts"
                  value={
                    source.match_hints.distinguishing_facts.join("\n") || "None"
                  }
                />
              </dl>
            </section>
          )}

          {source.fix_effort && (
            <section
              aria-labelledby="raw-finding-fix-effort"
              className="space-y-3"
            >
              <h2 id="raw-finding-fix-effort" className="font-semibold">
                Estimated fix effort
              </h2>
              <div className="grid gap-3 sm:grid-cols-2">
                <EffortCard
                  label="Quick containment"
                  effort={source.fix_effort.quick}
                />
                <EffortCard
                  label="Best-quality correction"
                  effort={source.fix_effort.quality}
                />
              </div>
            </section>
          )}

          {detail.context && (
            <section
              aria-labelledby="raw-finding-context"
              className="space-y-3"
            >
              <h2 id="raw-finding-context" className="font-semibold">
                Immutable context
              </h2>
              <dl className="border-border grid gap-2 rounded-lg border p-4 text-sm sm:grid-cols-2">
                <DetailRow label="Context ID" value={detail.context.id} mono />
                <DetailRow label="Model" value={detail.context.model} />
                <DetailRow
                  label="Reviewer"
                  value={detail.context.reviewer || "Not reported"}
                />
                <DetailRow label="Run" value={detail.context.run_id} mono />
                <DetailRow
                  label="Commit SHA"
                  value={detail.context.commit_sha}
                  mono
                />
                <DetailRow
                  label="Inventory"
                  value={detail.context.inventory_hash}
                  mono
                />
                <DetailRow
                  label="Profile"
                  value={detail.context.profile_hash || "Unavailable"}
                  mono
                />
                <DetailRow
                  label="Recorded"
                  value={formatTimestamp(detail.context.created_at)}
                />
              </dl>
            </section>
          )}

          {(source.history?.length ?? 0) > 0 && (
            <section
              aria-labelledby="raw-finding-history"
              className="space-y-3"
            >
              <h2 id="raw-finding-history" className="font-semibold">
                Processing history
              </h2>
              <div className="space-y-2">
                {source.history?.map((entry, index) => (
                  <article
                    key={`${entry.at}:${index}`}
                    className="border-border rounded-lg border p-3 text-sm"
                  >
                    <div className="flex flex-wrap gap-2">
                      <Badge variant="outline">{entry.state}</Badge>
                      <Badge variant="secondary">{entry.disposition}</Badge>
                      <span className="text-muted-foreground text-xs">
                        {formatTimestamp(entry.at)}
                      </span>
                    </div>
                    {entry.failure?.message && (
                      <p className="text-destructive mt-2">
                        {entry.failure.message}
                      </p>
                    )}
                  </article>
                ))}
              </div>
            </section>
          )}
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
      <dd className="mt-1 text-sm whitespace-pre-wrap">{value}</dd>
    </div>
  )
}

function EffortCard({
  label,
  effort,
}: {
  label: string
  effort: { loc_min: number; loc_max: number; class: string; rationale: string }
}) {
  return (
    <article className="border-border rounded-lg border p-4 text-sm">
      <h3 className="font-medium">{label}</h3>
      <p className="text-muted-foreground mt-1">
        {effort.class} · {effort.loc_min}–{effort.loc_max} lines
      </p>
      <p className="mt-2 whitespace-pre-wrap">{effort.rationale}</p>
    </article>
  )
}

function severityVariant(
  severity: string,
): "destructive" | "secondary" | "outline" {
  const normalized = severity.trim().toLowerCase()
  if (normalized === "critical" || normalized === "high") return "destructive"
  if (normalized === "medium") return "secondary"
  return "outline"
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}
