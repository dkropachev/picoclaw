import {
  IconActivity,
  IconEdit,
  IconPlayerPlay,
  IconPlayerStop,
  IconPlus,
  IconSettings,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type WorkflowDefinitionSummary,
  type WorkflowRunSummary,
  cancelWorkflowRun,
  listWorkflowDefinitions,
  listWorkflowRuns,
} from "@/api/workflows"
import {
  type CollectionDefinition,
  StandardCollectionPage,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { Button } from "@/components/ui/button"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

import {
  WorkflowCancelDialog,
  type WorkflowCancelTarget,
} from "./workflow-cancel-dialog"
import {
  normalizeWorkflowDefinitionsSearch,
  normalizeWorkflowRunsSearch,
  workflowDefinitionViews,
  workflowDefinitionsDefaultQuery,
  workflowRunViews,
  workflowRunsDefaultQuery,
} from "./workflow-collection-route-state"

export function WorkflowDefinitionsCollectionPage({
  search,
  onSearchChange,
  onOpen,
  onEdit,
  onRun,
  onNew,
  onRuns,
  onSettings,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen: (workflow: WorkflowDefinitionSummary) => void
  onEdit: (workflow: WorkflowDefinitionSummary) => void
  onRun: (workflow: WorkflowDefinitionSummary) => void
  onNew: () => void
  onRuns: () => void
  onSettings: () => void
}) {
  const activeQuery = normalizeWorkflowDefinitionsSearch(search).q
  const query = useInfiniteQuery({
    queryKey: ["workflows", "definitions", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listWorkflowDefinitions(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = useMemo(
    () => uniqueByID(query.data?.pages.flatMap((page) => page.workflows) ?? []),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<WorkflowDefinitionSummary>>(
    () => ({
      key: "workflows",
      title: "Workflows",
      defaultQuery: workflowDefinitionsDefaultQuery,
      supportedViews: workflowDefinitionViews,
      defaultView: "list",
      getItemID: (workflow) => workflow.id,
      getItemLabel: (workflow) => workflow.name || workflow.ref,
      getItemIdentity: (workflow) => ({
        title: workflow.name || workflow.ref,
        description: workflow.name ? workflow.ref : undefined,
        metadata: workflow.trigger || "No trigger summary",
      }),
      columns: [
        { id: "status", header: "Status", cell: (item) => title(item.status) },
        {
          id: "trigger",
          header: "Trigger",
          cell: (item) => item.trigger || "—",
        },
        { id: "inputs", header: "Inputs", cell: (item) => item.inputs },
        { id: "secrets", header: "Secrets", cell: (item) => item.secrets },
      ],
      gridFacts: [
        { id: "status", label: "Status", value: (item) => title(item.status) },
        {
          id: "trigger",
          label: "Trigger",
          value: (item) => item.trigger || "—",
        },
        { id: "inputs", label: "Inputs", value: (item) => item.inputs },
        { id: "secrets", label: "Secrets", value: (item) => item.secrets },
      ],
      badges: [
        {
          id: "status",
          label: (item) => title(item.status),
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit workflow",
          icon: <IconEdit />,
          onSelect: onEdit,
        },
        {
          id: "run",
          label: "Run workflow",
          icon: <IconPlayerPlay />,
          disabled: (item) => !workflowCanRun(item),
          onSelect: onRun,
        },
      ],
    }),
    [onEdit, onRun],
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
      onOpenItem={onOpen}
      addAction={
        <>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onRuns}
            aria-label="Workflow runs"
            title="Workflow runs"
          >
            <IconActivity /> <span className="hidden sm:inline">Runs</span>
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onSettings}
            aria-label="Workflow settings"
            title="Workflow settings"
          >
            <IconSettings /> <span className="hidden sm:inline">Settings</span>
          </Button>
          <Button
            type="button"
            size="sm"
            onClick={onNew}
            aria-label="New workflow"
            title="New workflow"
          >
            <IconPlus /> <span className="hidden sm:inline">New workflow</span>
          </Button>
        </>
      }
      emptyTitle="No workflow definitions"
      emptyDescription="Create a workflow or install a built-in template to get started."
    />
  )
}

export function WorkflowRunsCollectionPage({
  search,
  onSearchChange,
  onOpen,
  onDefinitions,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen: (run: WorkflowRunSummary) => void
  onDefinitions: () => void
}) {
  const queryClient = useQueryClient()
  const [cancelTarget, setCancelTarget] = useState<WorkflowCancelTarget | null>(
    null,
  )
  const activeQuery = normalizeWorkflowRunsSearch(search).q
  const query = useInfiniteQuery({
    queryKey: ["workflows", "runs", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listWorkflowRuns(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: 5_000,
    retry: false,
  })
  const items = useMemo(
    () => uniqueByID(query.data?.pages.flatMap((page) => page.runs) ?? []),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const cancel = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      cancelWorkflowRun(id, reason),
    onSuccess: async () => {
      setCancelTarget(null)
      await queryClient.invalidateQueries({
        queryKey: ["workflows", "runs"],
      })
      toast.success("Workflow cancellation requested")
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Cancellation failed.",
      ),
  })
  const definition = useMemo<CollectionDefinition<WorkflowRunSummary>>(
    () => ({
      key: "workflow-runs",
      title: "Workflow runs",
      defaultQuery: workflowRunsDefaultQuery,
      supportedViews: workflowRunViews,
      defaultView: "list",
      getItemID: (run) => run.id,
      getItemLabel: (run) => `${run.workflow_ref} ${run.id}`,
      getItemIdentity: (run) => ({
        title: run.workflow_ref,
        description: run.id,
        metadata: formatDate(run.created_at),
      }),
      columns: [
        { id: "status", header: "Status", cell: (run) => title(run.status) },
        { id: "session", header: "Session", cell: (run) => run.session || "—" },
        { id: "origin", header: "Origin", cell: (run) => originLabel(run) },
        {
          id: "created",
          header: "Created",
          cell: (run) => formatDate(run.created_at),
        },
        {
          id: "updated",
          header: "Updated",
          cell: (run) => formatDate(run.updated_at),
        },
      ],
      badges: [
        { id: "status", label: (run) => title(run.status), variant: "outline" },
        {
          id: "origin",
          label: (run) => (run.origin ? originLabel(run) : null),
          variant: "secondary",
        },
      ],
      actions: [
        {
          id: "cancel",
          label: "Cancel run",
          icon: <IconPlayerStop />,
          destructive: true,
          hidden: (run) => !["running", "waiting"].includes(run.status),
          onSelect: (run) =>
            setCancelTarget({ id: run.id, workflowRef: run.workflow_ref }),
        },
        {
          id: "retry",
          label: "Retry run…",
          icon: <IconActivity />,
          hidden: (run) =>
            !terminalRunStatuses.has(run.status) ||
            run.workflow_ref.startsWith("draft:") ||
            run.origin?.kind === "external_event_draft_test",
          onSelect: onOpen,
        },
      ],
    }),
    [onOpen],
  )

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
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onDefinitions}
          >
            Workflows
          </Button>
        }
        emptyTitle="No workflow runs"
        emptyDescription="Runs appear after a published workflow or draft test starts."
      />
      <WorkflowCancelDialog
        target={cancelTarget}
        pending={cancel.isPending}
        requestError={
          cancel.isError && cancel.error instanceof Error
            ? cancel.error.message
            : undefined
        }
        onDismiss={() => setCancelTarget(null)}
        onConfirm={(reason) => {
          if (cancelTarget) cancel.mutate({ id: cancelTarget.id, reason })
        }}
      />
    </>
  )
}

function uniqueByID<T extends { id: string }>(items: T[]): T[] {
  return [...new Map(items.map((item) => [item.id, item])).values()]
}

const terminalRunStatuses = new Set([
  "succeeded",
  "failed",
  "canceled",
  "skipped",
])

function workflowCanRun(workflow: WorkflowDefinitionSummary): boolean {
  return !workflow.error && ["valid", "needs_review"].includes(workflow.status)
}

function originLabel(run: WorkflowRunSummary): string {
  return run.origin?.kind ? title(run.origin.kind) : "Manual"
}

function title(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/^./, (letter) => letter.toUpperCase())
}

function formatDate(value?: string): string {
  if (!value) return "—"
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
