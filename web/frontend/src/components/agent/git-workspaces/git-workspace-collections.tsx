import {
  IconClearAll,
  IconGitBranch,
  IconHistory,
  IconRotateClockwise,
  IconSettings,
  IconTrash,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type GitWorkspaceHistoryEntry,
  type GitWorkspaceSummary,
  cleanupGitWorkspace,
  dropGitWorkspace,
  listGitWorkspaceHistory,
  listGitWorkspaces,
  reconcileGitWorkspaces,
} from "@/api/git-workspaces"
import {
  type CollectionDefinition,
  StandardCollectionPage,
  type StandardCollectionPageSearch,
} from "@/components/collection"
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
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

import { formatBytes, formatDate } from "./git-workspace-format"
import {
  gitWorkspaceHistoryDefaultQuery,
  gitWorkspaceHistoryViews,
  gitWorkspaceViews,
  gitWorkspacesDefaultQuery,
  normalizeGitWorkspaceHistorySearch,
  normalizeGitWorkspacesSearch,
} from "./git-workspace-route-state"

type MaintenanceAction = "cleanup" | "drop"
type MaintenanceTarget = {
  action: MaintenanceAction
  workspace: GitWorkspaceSummary
}

export function GitWorkspacesCollectionPage({
  search,
  onSearchChange,
  onOpen,
  onHistory,
  onSettings,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen: (workspace: GitWorkspaceSummary) => void
  onHistory: () => void
  onSettings: () => void
}) {
  const queryClient = useQueryClient()
  const activeQuery = normalizeGitWorkspacesSearch(search).q
  const [target, setTarget] = useState<MaintenanceTarget | null>(null)
  const query = useInfiniteQuery({
    queryKey: ["git-workspaces", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listGitWorkspaces(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: 10_000,
    retry: false,
  })
  const items = useMemo(
    () =>
      uniqueByID(query.data?.pages.flatMap((page) => page.workspaces) ?? []),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ["git-workspaces"] })
  }
  const cleanup = useMutation({
    mutationFn: (id: string) => cleanupGitWorkspace(id),
    onSuccess: async () => {
      setTarget(null)
      toast.success("Ignored files cleaned")
      await invalidate()
    },
    onError: (error) => toast.error(errorMessage(error, "Cleanup failed")),
  })
  const drop = useMutation({
    mutationFn: (id: string) => dropGitWorkspace(id),
    onSuccess: async () => {
      setTarget(null)
      toast.success("Workspace dropped")
      await invalidate()
    },
    onError: (error) => toast.error(errorMessage(error, "Drop failed")),
  })
  const reconcile = useMutation({
    mutationFn: reconcileGitWorkspaces,
    onSuccess: async (result) => {
      toast.success(
        `Workspace maintenance completed: ${result.cleaned.length} cleaned, ${result.dropped.length} dropped.`,
      )
      await invalidate()
    },
    onError: (error) => toast.error(errorMessage(error, "Maintenance failed")),
  })
  const definition = useMemo<CollectionDefinition<GitWorkspaceSummary>>(
    () => ({
      key: "git-workspaces",
      title: "Git Workspaces",
      defaultQuery: gitWorkspacesDefaultQuery,
      supportedViews: gitWorkspaceViews,
      defaultView: "list",
      getItemID: (workspace) => workspace.id,
      getItemLabel: (workspace) =>
        `${workspace.repository} ${workspace.branch || workspace.id}`,
      getItemIdentity: (workspace) => ({
        title: workspace.repository,
        description: workspace.branch || "Detached checkout",
        metadata: `Updated ${formatDate(workspace.updated)}`,
      }),
      columns: [
        { id: "branch", header: "Branch", cell: (item) => item.branch || "—" },
        { id: "status", header: "Status", cell: (item) => title(item.status) },
        {
          id: "dirty",
          header: "Working tree",
          cell: (item) => (item.dirty ? "Dirty" : "Clean"),
        },
        { id: "size", header: "Size", cell: (item) => formatBytes(item.size) },
        {
          id: "ignored",
          header: "Ignored",
          cell: (item) => formatBytes(item.ignored),
        },
        {
          id: "updated",
          header: "Updated",
          cell: (item) => formatDate(item.updated),
        },
      ],
      gridFacts: [
        { id: "branch", label: "Branch", value: (item) => item.branch || "—" },
        { id: "status", label: "Status", value: (item) => title(item.status) },
        { id: "size", label: "Size", value: (item) => formatBytes(item.size) },
        {
          id: "updated",
          label: "Updated",
          value: (item) => formatDate(item.updated),
        },
      ],
      badges: [
        {
          id: "status",
          label: (item) => title(item.status),
          variant: "outline",
        },
        {
          id: "locked",
          label: (item) =>
            item.locked && item.status !== "locked" ? "Locked" : null,
          variant: "secondary",
        },
        {
          id: "dirty",
          label: (item) => (item.dirty ? "Dirty" : null),
          variant: "secondary",
        },
      ],
      actions: [
        {
          id: "cleanup",
          label: "Clean ignored files",
          icon: <IconClearAll />,
          disabled: maintenanceDisabled,
          onSelect: (workspace) => setTarget({ action: "cleanup", workspace }),
        },
        {
          id: "drop",
          label: "Drop workspace",
          icon: <IconTrash />,
          destructive: true,
          disabled: maintenanceDisabled,
          onSelect: (workspace) => setTarget({ action: "drop", workspace }),
        },
      ],
    }),
    [],
  )
  const mutationPending = cleanup.isPending || drop.isPending

  return (
    <>
      <StandardCollectionPage
        definition={definition}
        search={search}
        onSearchChange={onSearchChange}
        items={items}
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
        onOpenItem={onOpen}
        addAction={
          <>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={reconcile.isPending}
              onClick={() => reconcile.mutate()}
              aria-label="Maintain git workspaces"
              title="Maintain git workspaces"
            >
              <IconRotateClockwise />
              <span className="hidden md:inline">Maintain</span>
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onHistory}
              aria-label="Git workspace history"
              title="Git workspace history"
            >
              <IconHistory /> <span className="hidden sm:inline">History</span>
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onSettings}
              aria-label="Git workspace settings"
              title="Git workspace settings"
            >
              <IconSettings />{" "}
              <span className="hidden sm:inline">Settings</span>
            </Button>
          </>
        }
        emptyTitle="No git workspaces"
        emptyDescription="Git workspaces appear after an agent or automation allocates a checkout."
      />
      <MaintenanceConfirmation
        target={target}
        pending={mutationPending}
        onOpenChange={(open) => {
          if (!open && !mutationPending) setTarget(null)
        }}
        onConfirm={() => {
          if (!target) return
          if (target.action === "cleanup") cleanup.mutate(target.workspace.id)
          else drop.mutate(target.workspace.id)
        }}
      />
    </>
  )
}

export function GitWorkspaceHistoryCollectionPage({
  search,
  onSearchChange,
  onWorkspaces,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onWorkspaces: () => void
}) {
  const activeQuery = normalizeGitWorkspaceHistorySearch(search).q
  const query = useInfiniteQuery({
    queryKey: ["git-workspaces", "history", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listGitWorkspaceHistory(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: 10_000,
    retry: false,
  })
  const items = useMemo(
    () => uniqueByID(query.data?.pages.flatMap((page) => page.history) ?? []),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<GitWorkspaceHistoryEntry>>(
    () => ({
      key: "git-workspace-history",
      title: "Git Workspace History",
      defaultQuery: gitWorkspaceHistoryDefaultQuery,
      supportedViews: gitWorkspaceHistoryViews,
      defaultView: "list",
      getItemID: (entry) => entry.id,
      getItemLabel: (entry) =>
        `${entry.action} ${entry.workspace || entry.repository || entry.id}`,
      getItemIdentity: (entry) => ({
        title: title(entry.action),
        description: entry.workspace || entry.repository || "Workspace event",
        metadata: formatDate(entry.time),
      }),
      columns: [
        {
          id: "workspace",
          header: "Workspace",
          cell: (entry) => entry.workspace || "—",
        },
        {
          id: "repository",
          header: "Repository",
          cell: (entry) => entry.repository || "—",
        },
        { id: "agent", header: "Agent", cell: (entry) => entry.agent || "—" },
        { id: "time", header: "Time", cell: (entry) => formatDate(entry.time) },
      ],
      badges: [
        {
          id: "action",
          label: (entry) => title(entry.action),
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
      items={items}
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
      addAction={
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onWorkspaces}
          aria-label="Git workspaces"
          title="Git workspaces"
        >
          <IconGitBranch /> <span className="hidden sm:inline">Workspaces</span>
        </Button>
      }
      emptyTitle="No workspace history"
      emptyDescription="Workspace allocation and maintenance events appear here."
    />
  )
}

function MaintenanceConfirmation({
  target,
  pending,
  onOpenChange,
  onConfirm,
}: {
  target: MaintenanceTarget | null
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  const cleanup = target?.action === "cleanup"
  return (
    <AlertDialog open={target != null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {cleanup ? "Clean ignored files?" : "Drop local checkout?"}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {cleanup
              ? `Ignored files in ${target?.workspace.repository ?? "this workspace"} will be removed.`
              : `${target?.workspace.repository ?? "This workspace"} will be dropped from local inventory. Repository history remains available.`}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant={cleanup ? "default" : "destructive"}
            disabled={pending || !target}
            onClick={(event) => {
              event.preventDefault()
              onConfirm()
            }}
          >
            {cleanup ? <IconClearAll /> : <IconTrash />}
            {pending ? "Working…" : cleanup ? "Clean" : "Drop"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function maintenanceDisabled(workspace: GitWorkspaceSummary): boolean {
  return workspace.locked || workspace.status === "dropped"
}

function uniqueByID<T extends { id: string }>(items: T[]): T[] {
  return [...new Map(items.map((item) => [item.id, item])).values()]
}

function title(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/^./, (letter) => letter.toUpperCase())
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
