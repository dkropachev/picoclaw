import { IconBrandGithub } from "@tabler/icons-react"
import { useInfiniteQuery, useMutation } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type RepositoryReviewIssueSummary,
  listRepositoryReviewAutomationIssuesPage,
  publishRepositoryReviewIssues,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  StandardCollectionPage,
  type StandardCollectionSelectionState,
} from "@/components/collection"
import { githubRepositoryPath } from "@/components/repository-reviews/repository-review-actions"
import { repositoryReviewIssueStateLabel } from "@/components/repository-reviews/repository-review-issue-state"
import { Button } from "@/components/ui/button"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

import {
  type RepositoryReviewIssueRouteSearch,
  repositoryReviewIssuesDefaultQuery,
  repositoryReviewViews,
} from "./repository-review-route-state"

export function RepositoryReviewIssuesPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenIssue,
}: {
  automationID: string
  search: RepositoryReviewIssueRouteSearch
  onSearchChange: (
    next: RepositoryReviewIssueRouteSearch,
    replace?: boolean,
  ) => void
  onBack: () => void
  onOpenIssue: (draftID: string) => void
}) {
  const [resultMessages, setResultMessages] = useState<string[]>([])
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewIssuesDefaultQuery,
    supportedViews: repositoryReviewViews,
  }).q
  const changeSearch = (next: CollectionRouteSearch, replace?: boolean) =>
    onSearchChange(
      {
        ...next,
        ...(search.generation_id
          ? { generation_id: search.generation_id }
          : {}),
      },
      replace,
    )
  const query = useInfiniteQuery({
    queryKey: [
      "repository-review-issues",
      automationID,
      activeQuery,
      search.generation_id,
    ],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      listRepositoryReviewAutomationIssuesPage(
        automationID,
        {
          query: activeQuery,
          cursor: pageParam || undefined,
          limit: 50,
          generation_id: search.generation_id,
        },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    refetchInterval: (current) =>
      current.state.data?.pages.some((page) =>
        page.issues.some((issue) =>
          new Set(["generating", "publishing", "unknown"]).has(issue.state),
        ),
      )
        ? 2_000
        : false,
  })
  const firstPage = query.data?.pages[0]
  const issues = useMemo(
    () => query.data?.pages.flatMap((page) => page.issues) ?? [],
    [query.data?.pages],
  )
  const byID = useMemo(
    () => new Map(issues.map((issue) => [issue.id, issue])),
    [issues],
  )
  const publish = useMutation({
    mutationFn: ({ ids }: { ids: string[]; clearSelection: () => void }) =>
      publishRepositoryReviewIssues(automationID, {
        issues: ids.map((id) => ({
          id,
          expected_version: byID.get(id)?.version ?? 0,
        })),
        confirmed: true,
      }),
    onSuccess: async (response, variables) => {
      const outcomes = response.results ?? []
      const messages = outcomes.map(
        (result) =>
          `${result.draft_id || result.id || "Preview"}: ${result.outcome || (result.success ? "posted" : "failed")}${result.message ? ` — ${result.message}` : ""}`,
      )
      setResultMessages(messages)
      variables.clearSelection()
      await query.refetch()
      const successes = outcomes.filter((outcome) => outcome.success).length
      if (outcomes.length > 0 && successes === 0) {
        toast.error(
          "No selected preview was posted. Review each outcome below.",
        )
      } else if (successes < outcomes.length) {
        toast.warning(
          `${successes} of ${outcomes.length} selected previews were posted.`,
        )
      } else {
        toast.success(
          "Posting request reconciled. Review each preview outcome below.",
        )
      }
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : "Posting failed."),
  })
  const github =
    firstPage?.capabilities?.github ??
    Boolean(firstPage && githubRepositoryPath(firstPage.automation.repository))
  const canPublish = firstPage?.capabilities?.can_publish === true
  const definition = useMemo<
    CollectionDefinition<RepositoryReviewIssueSummary>
  >(
    () => ({
      key: `repository-review-issue-previews:${automationID}:${search.generation_id || "all"}`,
      title: "Issue previews",
      defaultQuery: repositoryReviewIssuesDefaultQuery,
      supportedViews: repositoryReviewViews,
      defaultView: "list",
      getItemID: (issue) => issue.id,
      getItemLabel: (issue) => issue.title || issue.id,
      getItemIdentity: (issue) => ({
        title:
          issue.title ||
          (issue.state === "generating"
            ? "Generating preview…"
            : "Preview generation failed"),
        description: `${issue.finding_count} finding${issue.finding_count === 1 ? "" : "s"}`,
        metadata: formatTimestamp(issue.updated_at),
      }),
      columns: [
        {
          id: "repository",
          header: "Repository",
          cell: (issue) => issue.repository,
        },
        {
          id: "generation",
          header: "Generation",
          cell: (issue) => issue.generation_id || "Legacy",
        },
        {
          id: "state",
          header: "State",
          cell: (issue) => repositoryReviewIssueStateLabel(issue.state),
          className: "w-28",
        },
        {
          id: "origin",
          header: "Origin",
          cell: (issue) => issue.origin || "legacy",
          className: "w-28",
        },
        {
          id: "findings",
          header: "Findings",
          cell: (issue) => issue.finding_count,
          className: "w-24 tabular-nums",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (issue) => formatTimestamp(issue.updated_at),
          className: "w-44",
        },
      ],
      gridFacts: [
        {
          id: "repository",
          label: "Repository",
          value: (issue) => issue.repository,
        },
        {
          id: "generation",
          label: "Generation",
          value: (issue) => issue.generation_id || "Legacy",
        },
        {
          id: "state",
          label: "State",
          value: (issue) => repositoryReviewIssueStateLabel(issue.state),
        },
        {
          id: "updated",
          label: "Updated",
          value: (issue) => formatTimestamp(issue.updated_at),
        },
      ],
      badges: [
        {
          id: "state",
          label: (issue) => repositoryReviewIssueStateLabel(issue.state),
          variant: "outline",
        },
        {
          id: "conflict",
          label: (issue) => (issue.canonical === false ? "read only" : null),
          variant: "destructive",
        },
      ],
    }),
    [automationID, search.generation_id],
  )

  const selectionActions = (state: StandardCollectionSelectionState) => {
    const selectedPublishable = [...state.selectedIDs].every((id) => {
      const issue = byID.get(id)
      return issue != null && publishable(issue)
    })
    return canPublish ? (
      <Button
        type="button"
        size="sm"
        disabled={publish.isPending || !selectedPublishable}
        onClick={() =>
          publish.mutate({
            ids: [...state.selectedIDs],
            clearSelection: state.clearSelection,
          })
        }
      >
        <IconBrandGithub />
        {publish.isPending ? "Posting…" : "Post selected"}
      </Button>
    ) : null
  }

  const notices = (
    <>
      {search.generation_id && (
        <div className="border-border bg-muted/20 flex flex-wrap items-center gap-2 rounded-lg border p-3 text-sm">
          <span className="mr-auto">
            Showing generation <code>{search.generation_id}</code>
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() =>
              onSearchChange({
                q: search.q,
                ...(search.view ? { view: search.view } : {}),
              })
            }
          >
            Show all previews
          </Button>
        </div>
      )}
      {firstPage && !github && (
        <p
          role="status"
          className="border-border rounded-lg border p-3 text-sm"
        >
          Preview editing is available. Posting and existing-issue actions are
          unavailable because this review is not bound to a canonical GitHub
          repository.
        </p>
      )}
      {resultMessages.length > 0 && (
        <div
          role="status"
          className="border-border rounded-lg border p-3 text-sm"
        >
          <h2 className="font-medium">Posting outcomes</h2>
          <ul className="text-muted-foreground mt-2 list-inside list-disc">
            {resultMessages.map((message) => (
              <li key={message}>{message}</li>
            ))}
          </ul>
        </div>
      )}
    </>
  )
  const hasNotices = Boolean(
    search.generation_id || (firstPage && !github) || resultMessages.length,
  )

  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={changeSearch}
      items={issues}
      total={firstPage?.total}
      schema={firstPage?.query_schema}
      canonicalQuery={firstPage?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching}
      error={query.error}
      context={{
        backLabel: "Review details",
        onBack,
        identity: firstPage ? (
          <span className="text-muted-foreground text-xs">
            {firstPage.automation.repository}
          </span>
        ) : undefined,
      }}
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={(issue) => onOpenIssue(issue.id)}
      selection={{
        disabled: publish.isPending,
        maximumSelected: 200,
        isItemSelectable: (issue) => canPublish && publishable(issue),
        renderActions: selectionActions,
      }}
      beforeResults={hasNotices ? notices : undefined}
      emptyTitle={
        search.generation_id
          ? "No previews in this generation"
          : "No issue previews"
      }
      emptyDescription="Draft previews from explicitly selected repository findings."
    />
  )
}

function publishable(issue: RepositoryReviewIssueSummary): boolean {
  return (
    issue.canonical && issue.publishable && issue.publish_blockers.length === 0
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}
