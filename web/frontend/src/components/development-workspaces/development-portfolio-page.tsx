import { IconPlus } from "@tabler/icons-react"
import { useInfiniteQuery } from "@tanstack/react-query"
import { useMemo } from "react"

import {
  type DevelopmentWorkspaceCollectionSummary,
  listDevelopmentWorkspaces,
} from "@/api/development-workspaces"
import {
  type CollectionDefinition,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import {
  developmentWorkspaceCollectionViews,
  developmentWorkspacesDefaultQuery,
  normalizeDevelopmentWorkspacesSearch,
} from "@/components/development-workspaces/development-workspace-collection-route-state"
import { humanize } from "@/components/development-workspaces/development-workspace-labels"
import { Button } from "@/components/ui/button"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export function DevelopmentPortfolioPage({
  search,
  onSearchChange,
  onCreate,
  onOpenWorkspace,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onCreate: () => void
  onOpenWorkspace: (workspaceID: string) => void
}) {
  const activeQuery =
    normalizeDevelopmentWorkspacesSearch(search).q ??
    developmentWorkspacesDefaultQuery
  const query = useInfiniteQuery({
    queryKey: ["development-workspaces", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      listDevelopmentWorkspaces(
        {
          query: activeQuery,
          cursor: pageParam || undefined,
          limit: 50,
        },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    refetchInterval: 5_000,
    retry: false,
  })
  const workspaces = useMemo(
    () =>
      uniqueByID(query.data?.pages.flatMap((page) => page.workspaces) ?? []),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const definition = useMemo<
    CollectionDefinition<DevelopmentWorkspaceCollectionSummary>
  >(
    () => ({
      key: "development-workspaces",
      title: "Development workspaces",
      defaultQuery: developmentWorkspacesDefaultQuery,
      supportedViews: developmentWorkspaceCollectionViews,
      defaultView: "list",
      getItemID: (workspace) => workspace.id,
      getItemLabel: (workspace) => workspace.title,
      getItemIdentity: (workspace) => ({
        title: workspace.title,
        description: workspace.repository,
        metadata: `Updated ${formatTimestamp(workspace.updated)}`,
      }),
      columns: [
        {
          id: "source",
          header: "Source",
          cell: (workspace) => sourceLabel(workspace.source),
        },
        {
          id: "intent",
          header: "Intent",
          cell: (workspace) => intentLabel(workspace.intent),
        },
        {
          id: "repository",
          header: "Repository",
          cell: (workspace) => workspace.repository,
        },
        {
          id: "phase",
          header: "Phase",
          cell: (workspace) => humanize(workspace.phase),
        },
        {
          id: "updated",
          header: "Updated",
          cell: (workspace) => formatTimestamp(workspace.updated),
          className: "w-44",
        },
      ],
      gridFacts: [
        {
          id: "repository",
          label: "Repository",
          value: (workspace) => workspace.repository,
        },
        {
          id: "source",
          label: "Source",
          value: (workspace) => sourceLabel(workspace.source),
        },
        {
          id: "intent",
          label: "Intent",
          value: (workspace) => intentLabel(workspace.intent),
        },
        {
          id: "updated",
          label: "Updated",
          value: (workspace) => formatTimestamp(workspace.updated),
        },
      ],
      badges: [
        {
          id: "phase",
          label: (workspace) => humanize(workspace.phase),
          variant: "secondary",
        },
        {
          id: "state",
          label: (workspace) => humanize(workspace.execution_state),
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
      items={workspaces}
      total={first?.total}
      schema={first?.query_schema}
      canonicalQuery={first?.canonical_query}
      loading={query.isPending}
      fetching={query.isFetching}
      error={query.error}
      onRefresh={() => query.refetch()}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={() => query.fetchNextPage()}
      onOpenItem={(workspace) => onOpenWorkspace(workspace.id)}
      addAction={
        <Button type="button" size="sm" onClick={onCreate}>
          <IconPlus /> New work
        </Button>
      }
      emptyTitle="No development work"
      emptyDescription="Start from an issue, a feature brief, or an existing pull request."
    />
  )
}

function uniqueByID(
  workspaces: DevelopmentWorkspaceCollectionSummary[],
): DevelopmentWorkspaceCollectionSummary[] {
  return [
    ...new Map(
      workspaces.map((workspace) => [workspace.id, workspace]),
    ).values(),
  ]
}

function sourceLabel(
  source: DevelopmentWorkspaceCollectionSummary["source"],
): string {
  switch (source) {
    case "pull_request":
      return "Pull request"
    case "issue":
      return "Issue"
    case "brief":
      return "Brief"
  }
}

function intentLabel(
  intent: DevelopmentWorkspaceCollectionSummary["intent"],
): string {
  return intent === "implement_feature" ? "Feature" : "PR pickup"
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString()
}
