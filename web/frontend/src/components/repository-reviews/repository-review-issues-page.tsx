import { IconBrandGithub, IconRefresh } from "@tabler/icons-react"
import { useInfiniteQuery, useMutation } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewIssueDraft,
  listRepositoryReviewAutomationIssues,
  publishRepositoryReviewIssues,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  CollectionResults,
} from "@/components/collection"
import { githubRepositoryPath } from "@/components/repository-reviews/repository-review-actions"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  type CollectionRouteSearch,
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"

import {
  type RepositoryReviewRouteSearch,
  repositoryReviewDefaultQuery,
} from "./repository-review-route-state"

export function RepositoryReviewIssuesPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenIssue,
}: {
  automationID: string
  search: RepositoryReviewRouteSearch
  onSearchChange: (next: RepositoryReviewRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenIssue: (draftID: string) => void
}) {
  const [publishOpen, setPublishOpen] = useState(false)
  const [resultMessages, setResultMessages] = useState<string[]>([])
  const selection = useCollectionRouteState({
    collectionKey: `repository-review-issues:${automationID}:${search.generation_id || "all"}`,
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: ["list"],
    defaultView: "list",
    search,
    onSearchChange: (collectionSearch: CollectionRouteSearch, replace) =>
      onSearchChange(
        {
          ...search,
          q: collectionSearch.q,
          ...(collectionSearch.view ? { view: collectionSearch.view } : {}),
        },
        replace,
      ),
  })
  const query = useInfiniteQuery({
    queryKey: ["repository-review-issues", automationID, search.generation_id],
    initialPageParam: 0,
    queryFn: ({ signal, pageParam }) =>
      listRepositoryReviewAutomationIssues(
        automationID,
        {
          generation_id: search.generation_id,
          offset: pageParam,
          limit: 50,
        },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_offset,
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
  const page = query.data?.pages[0]
  const issues = useMemo(
    () => query.data?.pages.flatMap((candidate) => candidate.issues) ?? [],
    [query.data?.pages],
  )
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const byID = useMemo(
    () => new Map(issues.map((issue) => [issue.id, issue])),
    [issues],
  )
  const publish = useMutation({
    mutationFn: () =>
      publishRepositoryReviewIssues(automationID, {
        issues: [...selection.selectedIDs].map((id) => ({
          id,
          expected_version: byID.get(id)?.version ?? 0,
        })),
        confirmed: true,
      }),
    onSuccess: async (response) => {
      const messages = (response.results ?? []).map(
        (result) =>
          `${result.draft_id || result.id || "Preview"}: ${result.outcome || (result.success ? "posted" : "failed")}${result.message ? ` — ${result.message}` : ""}`,
      )
      setResultMessages(messages)
      setPublishOpen(false)
      selection.clearSelection()
      await query.refetch()
      toast.success(
        "Publication request reconciled. Review each preview outcome below.",
      )
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Publication failed.",
      ),
  })
  const github =
    page?.capabilities?.github ??
    Boolean(page && githubRepositoryPath(page.automation.repository))
  const canPublish = page?.capabilities?.can_publish ?? github
  const definition = useMemo<CollectionDefinition<RepositoryReviewIssueDraft>>(
    () => ({
      key: "repository-review-issue-previews",
      title: "Issue previews",
      defaultQuery: "",
      supportedViews: ["list"],
      defaultView: "list",
      getItemID: (issue) => issue.id,
      getItemLabel: (issue) => issue.title || issue.id,
      getItemIdentity: (issue) => ({
        title:
          issue.title ||
          (issue.state === "generating"
            ? "Generating preview…"
            : "Preview generation failed"),
        description:
          issue.finding_ids.length === 1
            ? `Finding ${issue.finding_ids[0]}`
            : `${issue.finding_ids.length} legacy grouped findings`,
        metadata: issue.generation_error || formatTimestamp(issue.updated_at),
      }),
      columns: [],
      badges: [
        { id: "state", label: (issue) => issue.state, variant: "outline" },
        {
          id: "origin",
          label: (issue) => issue.origin || "legacy",
          variant: "secondary",
        },
        {
          id: "conflict",
          label: (issue) => (issue.canonical === false ? "read only" : null),
          variant: "destructive",
        },
      ],
    }),
    [],
  )

  return (
    <>
      <CollectionDetailShell
        title="Issue previews"
        identity={
          page ? (
            <span className="text-muted-foreground text-xs">
              {page.automation.repository}
            </span>
          ) : undefined
        }
        actions={
          <Button
            type="button"
            size="icon-sm"
            variant="outline"
            aria-label="Refresh issue previews"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <IconRefresh />
          </Button>
        }
        loading={query.isLoading}
        error={!notFound ? query.error?.message : undefined}
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="Review details"
        contentRef={selection.setScrollContainerRef}
        onContentScroll={selection.onResultsScroll}
      >
        {page && (
          <div className="space-y-4">
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
                    onSearchChange({ ...search, generation_id: undefined })
                  }
                >
                  Show all previews
                </Button>
              </div>
            )}

            {!github && (
              <p
                role="status"
                className="border-border rounded-lg border p-3 text-sm"
              >
                Preview editing is available. Publication and existing-issue
                actions are unavailable because this review is not bound to a
                canonical GitHub repository.
              </p>
            )}

            {selection.selectedCount > 0 && (
              <div className="border-border bg-muted/20 flex flex-wrap items-center gap-2 rounded-lg border p-3">
                <strong className="mr-auto text-sm">
                  {selection.selectedCount} selected
                </strong>
                {canPublish && (
                  <Button
                    type="button"
                    size="sm"
                    onClick={() => setPublishOpen(true)}
                  >
                    <IconBrandGithub /> Publish selected
                  </Button>
                )}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={selection.clearSelection}
                >
                  Clear selection
                </Button>
              </div>
            )}

            {resultMessages.length > 0 && (
              <div
                role="status"
                className="border-border rounded-lg border p-3 text-sm"
              >
                <h2 className="font-medium">Publication outcomes</h2>
                <ul className="text-muted-foreground mt-2 list-inside list-disc">
                  {resultMessages.map((message) => (
                    <li key={message}>{message}</li>
                  ))}
                </ul>
              </div>
            )}

            <CollectionResults
              definition={definition}
              items={issues}
              view="list"
              selection={{
                selectedIDs: selection.selectedIDs,
                additive: true,
                maximumSelected: 200,
                isItemDisabled: (issue) => !canPublish || !publishable(issue),
                onSelectionChange: selection.setSelection,
              }}
              onOpenItem={(issue) => onOpenIssue(issue.id)}
              emptyTitle={
                search.generation_id
                  ? "No previews in this generation"
                  : "No issue previews"
              }
              emptyDescription="Generate previews from explicitly selected report findings."
            />

            {query.hasNextPage && (
              <div className="flex justify-center">
                <Button
                  type="button"
                  variant="outline"
                  disabled={query.isFetchingNextPage}
                  onClick={() => void query.fetchNextPage()}
                >
                  {query.isFetchingNextPage ? "Loading…" : "Load more previews"}
                </Button>
              </div>
            )}
          </div>
        )}
      </CollectionDetailShell>

      <AlertDialog open={publishOpen} onOpenChange={setPublishOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Publish {selection.selectedCount} selected preview
              {selection.selectedCount === 1 ? "" : "s"}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Only this explicit subset is sent through the protected GitHub
              gateway. Ambiguous results remain reconcilable and are never
              blindly duplicated.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={publish.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={publish.isPending || selection.selectedCount === 0}
              onClick={(event) => {
                event.preventDefault()
                publish.mutate()
              }}
            >
              {publish.isPending ? "Publishing…" : "Publish selected"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function publishable(issue: RepositoryReviewIssueDraft): boolean {
  if (issue.canonical === false || issue.read_only) return false
  if (typeof issue.publishable === "boolean") return issue.publishable
  return (
    issue.state === "editing" ||
    issue.state === "publishing" ||
    issue.state === "unknown"
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}
