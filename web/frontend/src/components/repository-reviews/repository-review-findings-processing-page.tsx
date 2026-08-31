import { IconRefresh } from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"
import { useMemo } from "react"
import { toast } from "sonner"

import {
  type RepositoryReviewRawFinding,
  listRepositoryReviewFindingsProcessingPage,
  retryRepositoryReviewFindingsProcessingSources,
  retryRepositoryReviewHistoricalDeduplication,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  StandardCollectionPage,
  type StandardCollectionSelectionState,
} from "@/components/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

import {
  repositoryReviewAutomationIsActive,
  repositoryReviewFindingHealthNeedsPolling,
  repositoryReviewFindingHealthQueryKey,
  repositoryReviewHistoricalConsolidationIsActive,
  useRepositoryReviewFindingHealth,
} from "./repository-review-finding-health"
import { RepositoryReviewHistoricalConsolidationNotice } from "./repository-review-findings-processing"
import {
  repositoryReviewProcessingDispositionLabel,
  repositoryReviewProcessingStateLabel,
} from "./repository-review-processing-labels"
import {
  type RepositoryReviewCollectionSearch,
  repositoryReviewFindingsProcessingDefaultQuery,
  repositoryReviewViews,
} from "./repository-review-route-state"

export function RepositoryReviewFindingsProcessingPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenSource,
}: {
  automationID: string
  search: RepositoryReviewCollectionSearch
  onSearchChange: (next: CollectionRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenSource: (sourceID: string) => void
}) {
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewFindingsProcessingDefaultQuery,
    supportedViews: repositoryReviewViews,
  }).q
  const queryClient = useQueryClient()
  const query = useInfiniteQuery({
    queryKey: [
      "repository-review-findings-processing",
      automationID,
      activeQuery,
    ],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      listRepositoryReviewFindingsProcessingPage(
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
      const first = current.state.data?.pages[0]
      return Boolean(
        first && repositoryReviewAutomationIsActive(first.automation),
      ) ||
        current.state.data?.pages.some(
          (page) =>
            page.sources.some((source) =>
              new Set(["pending", "running"]).has(source.deduplication_state),
            ) ||
            repositoryReviewHistoricalConsolidationIsActive(
              page.historical_consolidation,
            ),
        )
        ? 2_000
        : false
    },
  })
  const pages = query.data?.pages
  const firstPage = pages?.[0]
  const sources = useMemo(
    () => pages?.flatMap((page) => page.sources) ?? [],
    [pages],
  )
  const healthQuery = useRepositoryReviewFindingHealth(
    automationID,
    firstPage?.automation,
  )
  const retrySelected = useMutation({
    mutationFn: ({ sourceIDs }: { sourceIDs: string[] }) =>
      retryRepositoryReviewFindingsProcessingSources(automationID, sourceIDs),
    onSuccess: async (response, variables) => {
      queryClient.setQueryData(
        repositoryReviewFindingHealthQueryKey(automationID),
        response.health,
      )
      await query.refetch()
      if (response.failures.length > 0) {
        toast.warning(
          `${response.retried_ids.length} of ${variables.sourceIDs.length} selected finding${variables.sourceIDs.length === 1 ? "" : "s"} queued. ${response.failures.length} remained selected.`,
        )
      } else {
        toast.success(
          `${response.retried_ids.length} finding${response.retried_ids.length === 1 ? "" : "s"} queued for retry.`,
        )
      }
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Selected findings could not be retried.",
      )
      void query.refetch()
    },
  })
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
      void healthQuery.refetch()
    },
  })
  const definition = useMemo<CollectionDefinition<RepositoryReviewRawFinding>>(
    () => ({
      key: `repository-review-findings-processing:${automationID}`,
      title: "Findings processing",
      defaultQuery: repositoryReviewFindingsProcessingDefaultQuery,
      supportedViews: repositoryReviewViews,
      defaultView: "list",
      getItemID: (source) => source.id,
      getItemLabel: (source) => source.title,
      getItemIdentity: (source) => ({
        title: <TruncatedValue value={source.title} />,
        description: <TruncatedValue value={processingLocation(source)} />,
        metadata: (
          <TruncatedValue
            value={`${source.model || "Unknown model"}${source.reviewer ? ` · ${source.reviewer}` : ""} · ${repositoryReviewProcessingStateLabel(source.deduplication_state)} · ${repositoryReviewProcessingDispositionLabel(source.disposition)} · Updated ${formatCompactDate(source.updated_at)}`}
          />
        ),
      }),
      columns: [
        {
          id: "path",
          header: "Path",
          cell: (source) => (
            <span
              className="block max-w-36 truncate"
              title={processingLocation(source)}
            >
              {processingLocation(source)}
            </span>
          ),
          className: "w-36 max-w-36 px-2 text-xs",
          headerClassName: "w-36 max-w-36 px-2",
        },
        {
          id: "model-reviewer",
          header: "Model / reviewer",
          cell: (source) => {
            const value = `${source.model || "Unknown"}${source.reviewer ? ` / ${source.reviewer}` : ""}`
            return (
              <span className="block max-w-32 truncate" title={value}>
                {value}
              </span>
            )
          },
          className: "w-32 max-w-32 px-2 text-xs",
          headerClassName: "w-32 max-w-32 px-2",
        },
        {
          id: "state",
          header: "State",
          cell: (source) =>
            repositoryReviewProcessingStateLabel(source.deduplication_state),
          className: "w-20 max-w-20 px-2 text-xs",
          headerClassName: "w-20 max-w-20 px-2",
        },
        {
          id: "disposition",
          header: "Disposition",
          cell: (source) =>
            repositoryReviewProcessingDispositionLabel(source.disposition),
          className: "w-24 max-w-24 px-2 text-xs",
          headerClassName: "w-24 max-w-24 px-2",
        },
        {
          id: "severity",
          header: "Severity",
          cell: (source) => source.severity,
          className: "w-16 max-w-16 px-2 text-xs",
          headerClassName: "w-16 max-w-16 px-2",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (source) => (
            <time
              dateTime={source.updated_at}
              title={formatTimestamp(source.updated_at)}
            >
              {formatCompactDate(source.updated_at)}
            </time>
          ),
          className: "w-28 max-w-28 px-2 text-xs",
          headerClassName: "w-28 max-w-28 px-2",
        },
      ],
      gridFacts: [
        {
          id: "path",
          label: "Path",
          value: processingLocation,
        },
        {
          id: "model-reviewer",
          label: "Model / reviewer",
          value: (source) =>
            `${source.model || "Unknown"}${source.reviewer ? ` / ${source.reviewer}` : ""}`,
        },
        {
          id: "state",
          label: "State",
          value: (source) =>
            repositoryReviewProcessingStateLabel(source.deduplication_state),
        },
        {
          id: "disposition",
          label: "Disposition",
          value: (source) =>
            repositoryReviewProcessingDispositionLabel(source.disposition),
        },
        {
          id: "updated",
          label: "Updated",
          value: (source) => formatTimestamp(source.updated_at),
        },
      ],
      badges: [
        {
          id: "severity",
          label: (source) => source.severity,
          variant: "outline",
        },
        {
          id: "failed",
          label: (source) =>
            source.deduplication_state === "failed" ? "Retry needed" : null,
          variant: "outline",
        },
      ],
    }),
    [automationID],
  )

  const selectionActions = (state: StandardCollectionSelectionState) => (
    <Button
      type="button"
      size="sm"
      disabled={retrySelected.isPending || state.selectedCount === 0}
      onClick={() => {
        const sourceIDs = [...state.selectedIDs]
        retrySelected.mutate(
          { sourceIDs },
          {
            onSuccess: (response) =>
              state.reconcileSelection({
                deleted_ids: response.retried_ids,
                failures: response.failures.map((failure) => ({
                  id: failure.source_id,
                  code: failure.code,
                  blockers: [failure.message],
                })),
              }),
          },
        )
      }}
    >
      <IconRefresh />
      {retrySelected.isPending ? "Retrying…" : "Retry selected"}
    </Button>
  )

  const consolidation = healthQuery.data?.historical_consolidation
  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={sources}
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
          <Badge variant="outline">
            {repositoryReviewAutomationIsActive(firstPage.automation)
              ? "Processing"
              : firstPage.automation.status}
          </Badge>
        ) : undefined,
      }}
      onRefresh={async () => {
        await Promise.all([query.refetch(), healthQuery.refetch()])
      }}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={(source) => onOpenSource(source.id)}
      selection={{
        disabled: retrySelected.isPending,
        maximumSelected: 200,
        isItemSelectable: (source) => source.deduplication_state === "failed",
        renderActions: selectionActions,
      }}
      beforeResults={
        <RepositoryReviewHistoricalConsolidationNotice
          consolidation={consolidation}
          retrying={retryHistorical.isPending}
          onRetry={() => retryHistorical.mutate()}
        />
      }
      emptyTitle={
        repositoryReviewFindingHealthNeedsPolling(
          firstPage?.automation,
          healthQuery.data,
        )
          ? "Finding processing is pending"
          : "No finding processing records"
      }
      emptyDescription="Validated diagnoses appear here as they enter the canonical repository ledger."
    />
  )
}

function processingLocation(source: RepositoryReviewRawFinding): string {
  const path = source.path || source.file?.path || "Unknown path"
  return `${path}${source.line == null ? "" : `:${source.line}`}${source.symbol ? ` · ${source.symbol}` : ""}`
}

function TruncatedValue({ value }: { value: string }) {
  return (
    <span className="block max-w-48 truncate" title={value}>
      {value}
    </span>
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}

function formatCompactDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleDateString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
      })
}
