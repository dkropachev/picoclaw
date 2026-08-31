import { useInfiniteQuery, useMutation } from "@tanstack/react-query"
import { useMemo } from "react"
import { toast } from "sonner"

import {
  type RepositoryReviewRawFinding,
  listRepositoryReviewAutomationRawFindingsPage,
  retryRepositoryReviewHistoricalDeduplication,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  StandardCollectionPage,
} from "@/components/collection"
import { Badge } from "@/components/ui/badge"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

import {
  repositoryReviewFindingHealthNeedsPolling,
  repositoryReviewHistoricalConsolidationIsActive,
  useRepositoryReviewFindingHealth,
} from "./repository-review-finding-health"
import {
  RepositoryReviewFindingsProcessing,
  RepositoryReviewHistoricalConsolidationNotice,
} from "./repository-review-findings-processing"
import {
  type RepositoryReviewCollectionSearch,
  repositoryReviewRawFindingsDefaultQuery,
  repositoryReviewViews,
} from "./repository-review-route-state"

export function RepositoryReviewRawFindingsPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenRawFinding,
  onOpenFinding,
}: {
  automationID: string
  search: RepositoryReviewCollectionSearch
  onSearchChange: (next: CollectionRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenRawFinding: (sourceID: string) => void
  onOpenFinding: (findingID: string) => void
}) {
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewRawFindingsDefaultQuery,
    supportedViews: repositoryReviewViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: ["repository-review-raw-findings", automationID, activeQuery],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      listRepositoryReviewAutomationRawFindingsPage(
        automationID,
        {
          query: activeQuery,
          cursor: pageParam || undefined,
          limit: 50,
        },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    refetchInterval: (current) => {
      const pages = current.state.data?.pages ?? []
      return pages.some((page) => {
        return (
          isReviewActive(page.automation) ||
          Boolean(
            page.findings_processing &&
            (page.findings_processing.pending > 0 ||
              page.findings_processing.processing > 0),
          ) ||
          repositoryReviewHistoricalConsolidationIsActive(
            page.historical_deduplication,
          )
        )
      })
        ? 2_000
        : false
    },
  })
  const pages = query.data?.pages
  const firstPage = pages?.[0]
  const healthQuery = useRepositoryReviewFindingHealth(
    automationID,
    firstPage?.automation,
  )
  const rawFindings = useMemo(
    () => pages?.flatMap((page) => page.raw_findings) ?? [],
    [pages],
  )
  const retryHistorical = useMutation({
    mutationFn: () =>
      retryRepositoryReviewHistoricalDeduplication(automationID),
    onSuccess: async () => {
      await Promise.all([query.refetch(), healthQuery.refetch()])
      toast.success("Historical consolidation queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Historical consolidation could not be retried.",
      )
      void query.refetch()
    },
  })
  const definition = useMemo<CollectionDefinition<RepositoryReviewRawFinding>>(
    () => ({
      key: `repository-review-raw-findings:${automationID}`,
      title: "Raw findings",
      defaultQuery: repositoryReviewRawFindingsDefaultQuery,
      supportedViews: repositoryReviewViews,
      defaultView: "list",
      getItemID: (finding) => finding.id,
      getItemLabel: (finding) => finding.title,
      getItemIdentity: (finding) => ({
        title: finding.title,
        description: `${finding.id} · ${rawFindingLocation(finding)}`,
        metadata: `${finding.model}${finding.reviewer ? ` · reviewer ${finding.reviewer}` : ""} · ${finding.deduplication_state} · ${finding.disposition}${finding.deduplicated_finding_id ? ` · finding ${finding.deduplicated_finding_id}` : ""} · Created ${formatTimestamp(finding.created_at)}`,
      }),
      columns: [
        {
          id: "location",
          header: "Location",
          cell: rawFindingLocation,
        },
        {
          id: "severity",
          header: "Severity",
          cell: (finding) => finding.severity,
          className: "w-24",
        },
        {
          id: "model",
          header: "Model / reviewer",
          cell: (finding) =>
            `${finding.model}${finding.reviewer ? ` / ${finding.reviewer}` : ""}`,
        },
        {
          id: "state",
          header: "Deduplication",
          cell: (finding) => finding.deduplication_state,
          className: "w-28",
        },
        {
          id: "disposition",
          header: "Disposition",
          cell: (finding) => finding.disposition,
          className: "w-24",
        },
        {
          id: "linked-finding",
          header: "Finding",
          cell: (finding) => finding.deduplicated_finding_id || "—",
        },
        {
          id: "created",
          header: "Created",
          cell: (finding) => formatTimestamp(finding.created_at),
          className: "w-44",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (finding) => formatTimestamp(finding.updated_at),
          className: "w-44",
        },
      ],
      gridFacts: [
        {
          id: "severity",
          label: "Severity",
          value: (finding) => finding.severity,
        },
        {
          id: "model",
          label: "Model",
          value: (finding) => finding.model,
        },
        {
          id: "state",
          label: "Deduplication",
          value: (finding) => finding.deduplication_state,
        },
        {
          id: "created",
          label: "Created",
          value: (finding) => formatTimestamp(finding.created_at),
        },
      ],
      badges: [
        {
          id: "severity",
          label: (finding) => finding.severity,
          variant: "outline",
        },
        {
          id: "state",
          label: (finding) => finding.deduplication_state,
          variant: "secondary",
        },
      ],
      actions: [
        {
          id: "open-parent-finding",
          label: "Open deduplicated finding",
          hidden: (finding) => !finding.deduplicated_finding_id,
          onSelect: (finding) =>
            onOpenFinding(finding.deduplicated_finding_id ?? ""),
        },
      ],
    }),
    [automationID, onOpenFinding],
  )

  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={rawFindings}
      total={firstPage?.total}
      schema={firstPage?.query_schema}
      canonicalQuery={firstPage?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching || healthQuery.isFetching}
      error={query.error}
      context={{
        backLabel: "Review details",
        onBack,
        identity: firstPage ? (
          <span className="text-muted-foreground truncate text-xs">
            {firstPage.automation.repository}
          </span>
        ) : undefined,
        status: firstPage ? (
          <Badge variant="outline">{firstPage.automation.status}</Badge>
        ) : undefined,
      }}
      onRefresh={async () => {
        await Promise.all([query.refetch(), healthQuery.refetch()])
      }}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={(finding) => onOpenRawFinding(finding.id)}
      beforeResults={
        firstPage ? (
          <>
            <RepositoryReviewFindingsProcessing
              counters={firstPage.findings_processing}
            />
            <RepositoryReviewHistoricalConsolidationNotice
              consolidation={healthQuery.data?.historical_consolidation}
              retrying={retryHistorical.isPending}
              onRetry={() => retryHistorical.mutate()}
            />
          </>
        ) : undefined
      }
      emptyTitle={
        firstPage &&
        repositoryReviewFindingHealthNeedsPolling(
          firstPage.automation,
          healthQuery.data,
        )
          ? "Raw findings are pending"
          : "No raw findings"
      }
      emptyDescription={
        firstPage && isReviewActive(firstPage.automation)
          ? "Validated raw diagnoses will appear as review work completes."
          : "This review campaign has not retained a raw finding."
      }
    />
  )
}

function rawFindingLocation(finding: RepositoryReviewRawFinding): string {
  const path = finding.path || finding.file?.path || "Unknown path"
  return `${path}${finding.line == null ? "" : `:${finding.line}`}${finding.symbol ? ` · ${finding.symbol}` : ""}`
}

function isReviewActive(review: {
  status: string
  auto_continue: boolean
  progress: { stage: string }
}): boolean {
  return (
    review.status === "running" ||
    review.status === "stopping" ||
    (review.status === "idle" &&
      review.auto_continue &&
      review.progress.stage.trim().toLowerCase() === "next batch queued")
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}
