import {
  IconAlertTriangle,
  IconArrowRight,
  IconGitPullRequest,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconSparkles,
} from "@tabler/icons-react"
import { useInfiniteQuery } from "@tanstack/react-query"
import { useMemo, useState } from "react"

import {
  type DevelopmentWorkspaceSummary,
  listDevelopmentWorkspaces,
} from "@/api/development-workspaces"
import {
  DevelopmentIntentBadge,
  DevelopmentPhaseBadge,
  DevelopmentStateBadge,
} from "@/components/development-workspaces/development-workspace-status"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

export function DevelopmentPortfolioPage({
  onCreate,
  onOpenWorkspace,
}: {
  onCreate: () => void
  onOpenWorkspace: (workspaceID: string) => void
}) {
  const [filter, setFilter] = useState("")
  const query = useInfiniteQuery({
    queryKey: ["development-workspaces"],
    queryFn: ({ signal, pageParam }) =>
      listDevelopmentWorkspaces(
        { limit: 100, ...(pageParam ? { cursor: pageParam } : {}) },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor,
    refetchInterval: 5_000,
  })
  const workspaces = useMemo(() => {
    const needle = filter.trim().toLowerCase()
    const values = [
      ...new Map(
        (query.data?.pages.flatMap((page) => page.workspaces) ?? []).map(
          (workspace) => [workspace.id, workspace],
        ),
      ).values(),
    ]
    if (!needle) return values
    return values.filter(
      (workspace) =>
        workspace.title.toLowerCase().includes(needle) ||
        workspace.repository.toLowerCase().includes(needle) ||
        workspace.phase.toLowerCase().includes(needle),
    )
  }, [filter, query.data?.pages])

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="development-portfolio"
      aria-busy={query.isPending || query.isFetchingNextPage}
    >
      <PageHeader title="Development">
        <Button
          type="button"
          size="icon"
          variant="outline"
          aria-label="Refresh workspaces"
          title="Refresh workspaces"
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          <IconRefresh
            className={cn("size-4", query.isFetching && "animate-spin")}
          />
        </Button>
        <Button
          type="button"
          aria-label="New work"
          title="New work"
          onClick={onCreate}
        >
          <IconPlus />
          <span className="hidden sm:inline">New work</span>
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto px-4 pb-6 md:px-6">
        <div className="mx-auto flex w-full max-w-7xl flex-col gap-4">
          <label
            htmlFor="development-workspace-filter"
            className="relative block max-w-xl"
          >
            <span className="sr-only">Filter development workspaces</span>
            <IconSearch className="text-muted-foreground pointer-events-none absolute top-2.5 left-3 size-4" />
            <Input
              id="development-workspace-filter"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              className="pl-9"
              placeholder="Filter by title, repository, or phase"
            />
          </label>

          {query.isPending ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              Loading development workspaces…
            </p>
          ) : query.isError ? (
            <div
              role="alert"
              className="border-destructive/40 bg-destructive/5 text-destructive flex items-center gap-2 rounded-lg border p-4 text-sm"
            >
              <IconAlertTriangle className="size-4" />
              Development workspaces could not be loaded.
            </div>
          ) : workspaces.length === 0 ? (
            <div className="border-border text-muted-foreground flex flex-col items-center gap-3 rounded-lg border border-dashed py-16 text-sm">
              <IconSparkles className="size-8" />
              <p>
                {filter.trim()
                  ? "No matching work."
                  : "No development work yet."}
              </p>
              {!filter.trim() && (
                <Button type="button" variant="outline" onClick={onCreate}>
                  Start development
                </Button>
              )}
            </div>
          ) : (
            <section aria-label="Development workspaces" className="space-y-2">
              <p className="text-muted-foreground text-xs">
                {workspaces.length} workspace
                {workspaces.length === 1 ? "" : "s"}
              </p>
              <div className="grid gap-2 xl:grid-cols-2">
                {workspaces.map((workspace) => (
                  <WorkspaceRow
                    key={workspace.id}
                    workspace={workspace}
                    onOpen={() => onOpenWorkspace(workspace.id)}
                  />
                ))}
              </div>
              {query.hasNextPage && (
                <div className="flex justify-center pt-2">
                  <Button
                    type="button"
                    variant="outline"
                    disabled={query.isFetchingNextPage}
                    onClick={() => void query.fetchNextPage()}
                  >
                    {query.isFetchingNextPage && (
                      <IconRefresh className="animate-spin" />
                    )}
                    Load more workspaces
                  </Button>
                </div>
              )}
            </section>
          )}
        </div>
      </div>
    </div>
  )
}

function WorkspaceRow({
  workspace,
  onOpen,
}: {
  workspace: DevelopmentWorkspaceSummary
  onOpen: () => void
}) {
  const SourceIcon =
    workspace.source_kind === "pull_request" ? IconGitPullRequest : IconSparkles
  return (
    <button
      type="button"
      onClick={onOpen}
      className="border-border bg-card hover:bg-muted/50 focus-visible:ring-ring grid min-w-0 gap-3 rounded-lg border p-4 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none sm:grid-cols-[minmax(0,1fr)_auto]"
    >
      <span className="min-w-0 space-y-2">
        <span className="flex min-w-0 items-center gap-2">
          <SourceIcon className="text-muted-foreground size-4 shrink-0" />
          <span className="truncate font-medium">{workspace.title}</span>
        </span>
        <span className="text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
          <span>{workspace.repository}</span>
          <span>Updated {formatTimestamp(workspace.updated_at)}</span>
        </span>
        <span className="flex flex-wrap gap-1.5">
          <DevelopmentIntentBadge intent={workspace.intent} />
          <DevelopmentPhaseBadge phase={workspace.phase} />
          <DevelopmentStateBadge state={workspace.execution_state} />
        </span>
      </span>
      <IconArrowRight className="text-muted-foreground hidden size-4 self-center sm:block" />
    </button>
  )
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString()
}
