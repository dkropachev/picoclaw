import { IconRefresh } from "@tabler/icons-react"
import { useInfiniteQuery } from "@tanstack/react-query"
import { useEffect, useMemo } from "react"

import {
  type RepositoryReviewAutomation,
  listRepositoryReviewAutomationsPage,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  CollectionResults,
  CollectionShell,
  CollectionToolbar,
} from "@/components/collection"
import { Button } from "@/components/ui/button"
import {
  type CollectionRouteSearch,
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"

import {
  repositoryReviewDefaultQuery,
  repositoryReviewViews,
} from "./repository-review-route-state"

const runsKey = ["repository-review-automations"] as const
const activeStatuses = new Set(["running", "stopping"])

export function RepositoryReviewRunsPage({
  search,
  onSearchChange,
  onOpen,
}: {
  search: { q?: string; view?: "list" | "table" | "grid" }
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen?: (review: RepositoryReviewAutomation) => void
}) {
  const routeState = useCollectionRouteState({
    collectionKey: "repository-review-runs",
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: repositoryReviewViews,
    defaultView: "list",
    search,
    onSearchChange,
  })
  const query = useInfiniteQuery({
    queryKey: [...runsKey, routeState.query],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listRepositoryReviewAutomationsPage(
        {
          query: routeState.query,
          cursor: pageParam || undefined,
          limit: 50,
        },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
    refetchInterval: (state) =>
      (state.state.data?.pages ?? []).some((page) =>
        page.automations.some(
          (review) =>
            activeStatuses.has(review.status) || isQueuedHandoff(review),
        ),
      )
        ? 2_000
        : false,
  })
  const reviews = useMemo(
    () => query.data?.pages.flatMap((page) => page.automations) ?? [],
    [query.data?.pages],
  )
  const firstPage = query.data?.pages[0]
  const commitQuerySuccess = routeState.commitQuerySuccess

  useEffect(() => {
    if (firstPage?.canonical_query) {
      commitQuerySuccess(firstPage.canonical_query)
    }
  }, [commitQuerySuccess, firstPage?.canonical_query])

  const definition = useMemo<CollectionDefinition<RepositoryReviewAutomation>>(
    () => ({
      key: "repository-review-runs",
      title: "Repository reviews",
      defaultQuery: repositoryReviewDefaultQuery,
      supportedViews: repositoryReviewViews,
      defaultView: "list",
      getItemID: (review) => review.id,
      getItemLabel: (review) => review.repository || review.name || review.id,
      getItemIdentity: (review) => ({
        title: review.repository || "Repository review",
        description:
          review.branch || review.ref
            ? `Branch ${review.branch || review.ref}`
            : "Default repository branch",
        metadata: `${review.name || "Review"} · ${formatTimestamp(review.updated_at)}`,
      }),
      columns: [
        { id: "status", header: "Status", cell: (review) => review.status },
        {
          id: "progress",
          header: "Progress",
          cell: progressLabel,
          className: "w-24 tabular-nums",
        },
        {
          id: "findings",
          header: "Findings",
          cell: (review) => review.progress.findings,
          className: "w-24 tabular-nums",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (review) => formatTimestamp(review.updated_at),
          className: "w-44",
        },
      ],
      gridFacts: [
        { id: "status", label: "Status", value: (review) => review.status },
        { id: "progress", label: "Progress", value: progressLabel },
        {
          id: "reviewed",
          label: "Reviewed files",
          value: (review) => review.progress.reviewed_files,
        },
        {
          id: "findings",
          label: "Findings",
          value: (review) => review.progress.findings,
        },
      ],
      badges: [
        {
          id: "status",
          label: (review) =>
            isQueuedHandoff(review) ? "continuing" : review.status,
          variant: "outline",
        },
      ],
    }),
    [],
  )
  const error = query.error instanceof Error ? query.error.message : undefined
  const queryError = collectionQueryError(query.error)

  return (
    <CollectionShell
      title="Repository reviews"
      total={firstPage?.total}
      resultsRef={routeState.setScrollContainerRef}
      onResultsScroll={routeState.onResultsScroll}
      actions={
        <Button
          type="button"
          size="icon-sm"
          variant="outline"
          disabled={query.isFetching}
          aria-label="Refresh repository reviews"
          title="Refresh"
          onClick={() => void query.refetch()}
        >
          <IconRefresh />
        </Button>
      }
      toolbar={
        <CollectionToolbar
          activeQuery={routeState.query}
          defaultQuery={repositoryReviewDefaultQuery}
          schema={firstPage?.query_schema}
          queryError={queryError}
          onApplyQuery={routeState.applyQuery}
          view={routeState.view}
          supportedViews={routeState.supportedViews}
          recentQueries={routeState.recentQueries}
          onClearHistory={routeState.clearHistory}
          onViewChange={routeState.setView}
        />
      }
    >
      <CollectionResults
        definition={definition}
        items={reviews}
        view={routeState.view}
        loading={query.isLoading}
        error={error}
        onRetry={() => void query.refetch()}
        onOpenItem={onOpen}
        hasNextPage={query.hasNextPage}
        loadingMore={query.isFetchingNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        emptyTitle="No repository configured"
        emptyDescription="Assign a review profile from Repositories first."
      />
    </CollectionShell>
  )
}

function progressLabel(review: RepositoryReviewAutomation): string {
  if (!review.progress.total_batches) return "Not started"
  return `${Math.round(
    (review.progress.completed_batches / review.progress.total_batches) * 100,
  )}%`
}

function isQueuedHandoff(review: RepositoryReviewAutomation): boolean {
  return (
    review.status === "idle" &&
    review.auto_continue &&
    review.progress.stage.trim().toLowerCase() === "next batch queued"
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}

function collectionQueryError(
  error: unknown,
): { position: number; message: string } | undefined {
  if (!error || typeof error !== "object") return undefined
  const candidate = error as { position?: unknown; message?: unknown }
  if (typeof candidate.position !== "number") return undefined
  return {
    position: candidate.position,
    message:
      typeof candidate.message === "string"
        ? candidate.message
        : "Invalid collection query",
  }
}
