import { useInfiniteQuery } from "@tanstack/react-query"
import { useMemo } from "react"

import {
  type RepositoryReviewAutomation,
  listRepositoryReviewAutomationsPage,
} from "@/api/repository-reviews"
import { type CollectionDefinition } from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

import {
  repositoryReviewFileProgressLabel,
  repositoryReviewInspectedFilesLabel,
} from "./repository-review-file-progress"
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
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: repositoryReviewViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: [...runsKey, activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listRepositoryReviewAutomationsPage(
        {
          query: activeQuery,
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
          cell: repositoryReviewFileProgressLabel,
          className: "w-24 tabular-nums",
        },
        {
          id: "findings",
          header: "Finding occurrences",
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
        {
          id: "progress",
          label: "Progress",
          value: repositoryReviewFileProgressLabel,
        },
        {
          id: "inspected",
          label: "Inspected files",
          value: repositoryReviewInspectedFilesLabel,
        },
        {
          id: "reviewed",
          label: "Fully reviewed files",
          value: (review) => review.progress.reviewed_files,
        },
        {
          id: "findings",
          label: "Finding occurrences",
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
  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={reviews}
      total={firstPage?.total}
      schema={firstPage?.query_schema}
      canonicalQuery={firstPage?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching}
      error={query.error}
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={onOpen}
      emptyTitle="No repository configured"
      emptyDescription="Assign a review profile from Repositories first."
    />
  )
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
