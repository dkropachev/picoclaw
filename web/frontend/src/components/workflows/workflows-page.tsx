import {
  IconGitBranch,
  IconPlayerStop,
  IconRefresh,
  IconReload,
  IconRotateClockwise,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type ReactNode, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type WorkflowDefinition,
  type WorkflowRun,
  cancelWorkflowRun,
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  listWorkflowRuns,
  listWorkflows,
  reloadWorkflows,
  retryWorkflowRun,
} from "@/api/workflows"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

const terminalStatuses = new Set(["succeeded", "failed", "canceled", "skipped"])

export function WorkflowsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [query, setQuery] = useState("")
  const [selectedRunID, setSelectedRunID] = useState<string | null>(null)

  const workflowsQuery = useQuery({
    queryKey: ["workflows"],
    queryFn: listWorkflows,
  })
  const runsQuery = useQuery({
    queryKey: ["workflows", "runs"],
    queryFn: listWorkflowRuns,
    refetchInterval: 5000,
  })
  const runQuery = useQuery({
    queryKey: ["workflows", "runs", selectedRunID],
    queryFn: () => getWorkflowRun(selectedRunID ?? ""),
    enabled: selectedRunID != null,
    refetchInterval: selectedRunID == null ? false : 3000,
  })
  const eventsQuery = useQuery({
    queryKey: ["workflows", "runs", selectedRunID, "events"],
    queryFn: () => getWorkflowRunEvents(selectedRunID ?? ""),
    enabled: selectedRunID != null,
    refetchInterval: selectedRunID == null ? false : 3000,
  })
  const graphQuery = useQuery({
    queryKey: ["workflows", "runs", selectedRunID, "graph"],
    queryFn: () => getWorkflowRunGraph(selectedRunID ?? ""),
    enabled: selectedRunID != null,
    refetchInterval: selectedRunID == null ? false : 5000,
  })

  const runs = useMemo(() => runsQuery.data?.runs ?? [], [runsQuery.data?.runs])
  const workflows = useMemo(
    () => workflowsQuery.data?.workflows ?? [],
    [workflowsQuery.data?.workflows],
  )
  const selectedRun =
    runQuery.data ?? runs.find((run) => run.id === selectedRunID)

  useEffect(() => {
    if (selectedRunID == null && runs.length > 0) {
      setSelectedRunID(runs[0].id)
    }
  }, [runs, selectedRunID])

  const filteredRuns = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (needle === "") {
      return runs
    }
    return runs.filter((run) =>
      [run.id, run.workflow_ref, run.status, run.session]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(needle)),
    )
  }, [query, runs])

  const reloadMutation = useMutation({
    mutationFn: reloadWorkflows,
    onSuccess: (result) => {
      toast.success(
        result.errors.length === 0
          ? "Workflow definitions reloaded"
          : `Reloaded with ${result.errors.length} validation error(s)`,
      )
      void queryClient.invalidateQueries({ queryKey: ["workflows"] })
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const cancelMutation = useMutation({
    mutationFn: (runID: string) => cancelWorkflowRun(runID),
    onSuccess: () => {
      toast.success("Workflow run canceled")
      void invalidateRunQueries(queryClient, selectedRunID)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const retryMutation = useMutation({
    mutationFn: (runID: string) => retryWorkflowRun(runID),
    onSuccess: (result) => {
      toast.success("Workflow retry started")
      setSelectedRunID(result.run_id)
      void invalidateRunQueries(queryClient, result.run_id)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["workflows"] })
  }

  const canCancel = selectedRun?.status === "running"
  const canRetry =
    selectedRun != null && terminalStatuses.has(selectedRun.status)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.workflows", "Workflows")}>
        <Button
          variant="outline"
          size="sm"
          onClick={refresh}
          disabled={workflowsQuery.isFetching || runsQuery.isFetching}
          title="Refresh"
        >
          <IconRefresh className="size-4" />
          Refresh
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => reloadMutation.mutate()}
          disabled={reloadMutation.isPending}
          title="Reload definitions"
        >
          <IconReload className="size-4" />
          Reload
        </Button>
      </PageHeader>

      <div className="grid min-h-0 flex-1 gap-4 overflow-hidden p-4 sm:p-6 lg:grid-cols-[minmax(320px,0.9fr)_minmax(0,1.3fr)]">
        <section className="border-border bg-card/40 flex min-h-0 flex-col overflow-hidden rounded-lg border">
          <div className="border-border flex items-center justify-between gap-3 border-b p-3">
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Filter runs"
              className="h-8"
            />
            <RunCount
              running={runs.filter((run) => run.status === "running").length}
              total={runs.length}
            />
          </div>
          <div className="grid min-h-0 flex-1 grid-rows-[auto_minmax(0,1fr)]">
            <DefinitionStrip workflows={workflows} />
            <RunList
              runs={filteredRuns}
              selectedRunID={selectedRunID}
              onSelect={setSelectedRunID}
              loading={runsQuery.isLoading}
            />
          </div>
        </section>

        <section className="border-border bg-card/40 flex min-h-0 flex-col overflow-hidden rounded-lg border">
          <RunDetailHeader
            run={selectedRun}
            onCancel={() =>
              selectedRun && cancelMutation.mutate(selectedRun.id)
            }
            onRetry={() => selectedRun && retryMutation.mutate(selectedRun.id)}
            canceling={cancelMutation.isPending}
            retrying={retryMutation.isPending}
            canCancel={canCancel}
            canRetry={canRetry}
          />
          <ScrollRegion
            label="Workflow run detail"
            className="min-h-0 flex-1 overflow-auto p-4"
          >
            {selectedRun == null ? (
              <EmptyPanel label="No run selected" />
            ) : (
              <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(280px,0.65fr)]">
                <RunSummary run={selectedRun} />
                <RunGraphPanel
                  graph={graphQuery.data}
                  loading={graphQuery.isLoading}
                />
                <ExecutionPanel run={selectedRun} />
                <EventsPanel
                  events={eventsQuery.data?.events ?? []}
                  loading={eventsQuery.isLoading}
                />
              </div>
            )}
          </ScrollRegion>
        </section>
      </div>
    </div>
  )
}

function DefinitionStrip({ workflows }: { workflows: WorkflowDefinition[] }) {
  return (
    <div className="border-border border-b p-3">
      <div className="mb-2 flex items-center justify-between">
        <h3 className="text-sm font-medium">Definitions</h3>
        <Badge variant="outline">{workflows.length}</Badge>
      </div>
      <ScrollRegion
        label="Workflow definitions"
        className="flex max-h-28 flex-col gap-1 overflow-auto rounded-md"
      >
        {workflows.length === 0 ? (
          <span className="text-muted-foreground text-sm">No definitions</span>
        ) : (
          workflows.map((workflow) => (
            <div
              key={workflow.ref}
              className="text-muted-foreground flex items-center justify-between gap-2 rounded-md px-2 py-1 text-xs"
            >
              <span className="min-w-0 truncate font-mono">{workflow.ref}</span>
              {workflow.error ? (
                <Badge variant="destructive">invalid</Badge>
              ) : (
                <Badge variant="secondary">ok</Badge>
              )}
            </div>
          ))
        )}
      </ScrollRegion>
    </div>
  )
}

function RunList({
  runs,
  selectedRunID,
  onSelect,
  loading,
}: {
  runs: WorkflowRun[]
  selectedRunID: string | null
  onSelect: (runID: string) => void
  loading: boolean
}) {
  if (loading) {
    return <EmptyPanel label="Loading runs" />
  }
  if (runs.length === 0) {
    return <EmptyPanel label="No runs" />
  }
  return (
    <ScrollRegion label="Workflow runs" className="min-h-0 overflow-auto p-2">
      <div className="flex flex-col gap-1">
        {runs.map((run) => (
          <button
            key={run.id}
            type="button"
            onClick={() => onSelect(run.id)}
            className={cn(
              "border-border/70 hover:bg-muted/60 grid min-w-0 gap-1 rounded-md border px-3 py-2 text-left",
              selectedRunID === run.id && "bg-accent/70 text-accent-foreground",
            )}
          >
            <div className="flex min-w-0 items-center justify-between gap-2">
              <span className="min-w-0 truncate font-mono text-xs">
                {run.workflow_ref}
              </span>
              <StatusBadge status={run.status} />
            </div>
            <div className="text-muted-foreground flex min-w-0 items-center justify-between gap-2 text-xs">
              <span className="min-w-0 truncate">{run.id}</span>
              <span className="shrink-0">{formatDate(run.created_at)}</span>
            </div>
          </button>
        ))}
      </div>
    </ScrollRegion>
  )
}

function RunDetailHeader({
  run,
  canCancel,
  canRetry,
  canceling,
  retrying,
  onCancel,
  onRetry,
}: {
  run?: WorkflowRun
  canCancel: boolean
  canRetry: boolean
  canceling: boolean
  retrying: boolean
  onCancel: () => void
  onRetry: () => void
}) {
  return (
    <div className="border-border flex min-h-14 items-center justify-between gap-3 border-b px-4 py-3">
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <h3 className="min-w-0 truncate text-sm font-medium">
            {run?.workflow_ref ?? "Run detail"}
          </h3>
          {run ? <StatusBadge status={run.status} /> : null}
        </div>
        <div className="text-muted-foreground mt-0.5 truncate font-mono text-xs">
          {run?.id ?? "Select a workflow run"}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={onCancel}
          disabled={!canCancel || canceling}
          title="Cancel run"
        >
          <IconPlayerStop className="size-4" />
          Cancel
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={onRetry}
          disabled={!canRetry || retrying}
          title="Retry run"
        >
          <IconRotateClockwise className="size-4" />
          Retry
        </Button>
      </div>
    </div>
  )
}

function RunSummary({ run }: { run: WorkflowRun }) {
  return (
    <Panel title="Summary">
      <dl className="grid grid-cols-2 gap-3 text-sm">
        <Meta label="Created" value={formatDate(run.created_at)} />
        <Meta label="Updated" value={formatDate(run.updated_at)} />
        <Meta label="Completed" value={formatDate(run.completed_at)} />
        <Meta label="Session" value={run.session ?? "-"} mono />
        <Meta label="Parent" value={run.parent_run_id ?? "-"} mono />
        <Meta label="Retry of" value={run.retry_of_run_id ?? "-"} mono />
      </dl>
      {run.error || run.cancel_reason ? (
        <div className="bg-destructive/10 text-destructive mt-3 rounded-md px-3 py-2 text-sm">
          {run.cancel_reason || run.error}
        </div>
      ) : null}
      <JsonBlock label="Inputs" value={run.inputs} />
      <JsonBlock label="Outputs" value={run.outputs} />
    </Panel>
  )
}

function RunGraphPanel({
  graph,
  loading,
}: {
  graph?: Awaited<ReturnType<typeof getWorkflowRunGraph>>
  loading: boolean
}) {
  return (
    <Panel
      title="Graph"
      titleExtra={<IconGitBranch className="text-muted-foreground size-4" />}
    >
      {loading ? (
        <EmptyPanel label="Loading graph" compact />
      ) : graph == null || graph.nodes.length === 0 ? (
        <EmptyPanel label="No graph" compact />
      ) : (
        <div className="flex flex-col gap-2">
          {graph.nodes.map((node) => (
            <div
              key={node.id}
              className="border-border/70 rounded-md border px-3 py-2"
            >
              <div className="flex items-center justify-between gap-2">
                <span className="min-w-0 truncate font-mono text-xs">
                  {node.workflow_ref}
                </span>
                <StatusBadge status={node.status} />
              </div>
              <div className="text-muted-foreground mt-1 truncate font-mono text-xs">
                {node.id}
              </div>
            </div>
          ))}
          {graph.edges.length > 0 ? (
            <div className="text-muted-foreground border-border/70 rounded-md border px-3 py-2 text-xs">
              {graph.edges.map((edge) => (
                <div key={`${edge.from}-${edge.to}-${edge.kind}`}>
                  {edge.kind}: {shortID(edge.from)} {"->"} {shortID(edge.to)}
                  {edge.job_id ? ` (${edge.job_id})` : ""}
                </div>
              ))}
            </div>
          ) : null}
        </div>
      )}
    </Panel>
  )
}

function ExecutionPanel({ run }: { run: WorkflowRun }) {
  const jobs = Object.values(run.jobs ?? {})
  const steps = Object.entries(run.steps ?? {})
  return (
    <Panel title="Execution">
      <div className="grid gap-3 md:grid-cols-2">
        <ExecutionList
          title="Jobs"
          items={jobs.map((job) => ({
            id: job.id,
            status: job.status,
            error: job.error,
          }))}
        />
        <ExecutionList
          title="Steps"
          items={steps.map(([id, step]) => ({
            id,
            status: step.status,
            error: step.error,
          }))}
        />
      </div>
    </Panel>
  )
}

function ExecutionList({
  title,
  items,
}: {
  title: string
  items: Array<{ id: string; status: string; error?: string }>
}) {
  return (
    <div className="min-w-0">
      <h4 className="mb-2 text-sm font-medium">{title}</h4>
      <ScrollRegion
        label={`${title} execution list`}
        className="flex max-h-72 flex-col gap-1 overflow-auto rounded-md"
      >
        {items.length === 0 ? (
          <span className="text-muted-foreground text-sm">None</span>
        ) : (
          items.map((item) => (
            <div
              key={item.id}
              className="border-border/70 rounded-md border px-3 py-2"
            >
              <div className="flex items-center justify-between gap-2">
                <span className="min-w-0 truncate font-mono text-xs">
                  {item.id}
                </span>
                <StatusBadge status={item.status} />
              </div>
              {item.error ? (
                <div className="text-destructive mt-1 text-xs">
                  {item.error}
                </div>
              ) : null}
            </div>
          ))
        )}
      </ScrollRegion>
    </div>
  )
}

function EventsPanel({
  events,
  loading,
}: {
  events: Awaited<ReturnType<typeof getWorkflowRunEvents>>["events"]
  loading: boolean
}) {
  return (
    <Panel title="Events">
      {loading ? (
        <EmptyPanel label="Loading events" compact />
      ) : events.length === 0 ? (
        <EmptyPanel label="No events" compact />
      ) : (
        <ScrollRegion
          label="Workflow events"
          className="flex max-h-96 flex-col gap-2 overflow-auto rounded-md"
        >
          {events.map((event, index) => (
            <div
              key={`${event.time}-${event.kind}-${index}`}
              className="border-border/70 rounded-md border px-3 py-2"
            >
              <div className="flex items-center justify-between gap-2 text-xs">
                <span className="font-mono">{event.kind}</span>
                <span className="text-muted-foreground shrink-0">
                  {formatDate(event.time)}
                </span>
              </div>
              {event.message ? (
                <div className="text-muted-foreground mt-1 text-xs">
                  {event.message}
                </div>
              ) : null}
            </div>
          ))}
        </ScrollRegion>
      )}
    </Panel>
  )
}

// Keyboard focus is required for scrollable regions; this ARIA region is intentionally focusable.
/* eslint-disable jsx-a11y/no-noninteractive-tabindex */
function ScrollRegion({
  label,
  className,
  children,
}: {
  label: string
  className?: string
  children: ReactNode
}) {
  return (
    <div
      className={cn(
        "focus-visible:ring-ring/40 outline-none focus-visible:ring-2",
        className,
      )}
      role="region"
      aria-label={label}
      tabIndex={0}
    >
      {children}
    </div>
  )
}
/* eslint-enable jsx-a11y/no-noninteractive-tabindex */

function Panel({
  title,
  titleExtra,
  children,
}: {
  title: string
  titleExtra?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="border-border bg-background/60 rounded-lg border p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{title}</h3>
        {titleExtra}
      </div>
      {children}
    </div>
  )
}

function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "failed" || status === "canceled"
      ? "destructive"
      : status === "running"
        ? "default"
        : status === "succeeded"
          ? "secondary"
          : "outline"
  return (
    <Badge variant={variant} className="capitalize">
      {status}
    </Badge>
  )
}

function RunCount({ running, total }: { running: number; total: number }) {
  return (
    <div className="flex shrink-0 items-center gap-2">
      <Badge variant={running > 0 ? "default" : "outline"}>
        {running} running
      </Badge>
      <Badge variant="outline">{total} total</Badge>
    </div>
  )
}

function Meta({
  label,
  value,
  mono,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className={cn("min-w-0 truncate", mono && "font-mono text-xs")}>
        {value}
      </dd>
    </div>
  )
}

function JsonBlock({ label, value }: { label: string; value?: unknown }) {
  if (
    value == null ||
    (typeof value === "object" && Object.keys(value).length === 0)
  ) {
    return null
  }
  return (
    <div className="mt-3">
      <div className="text-muted-foreground mb-1 text-xs">{label}</div>
      <ScrollRegion
        label={`${label} JSON`}
        className="bg-muted/50 max-h-48 overflow-auto rounded-md p-3 font-mono text-xs"
      >
        <pre className="m-0">{JSON.stringify(value, null, 2)}</pre>
      </ScrollRegion>
    </div>
  )
}

function EmptyPanel({ label, compact }: { label: string; compact?: boolean }) {
  return (
    <div
      className={cn(
        "text-muted-foreground flex items-center justify-center text-sm",
        compact ? "min-h-20" : "min-h-48",
      )}
    >
      {label}
    </div>
  )
}

function formatDate(value?: string) {
  if (!value) {
    return "-"
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date)
}

function shortID(value: string) {
  return value.length <= 10 ? value : `${value.slice(0, 10)}...`
}

function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : "Workflow request failed"
}

async function invalidateRunQueries(
  queryClient: ReturnType<typeof useQueryClient>,
  runID: string | null,
) {
  await queryClient.invalidateQueries({ queryKey: ["workflows", "runs"] })
  if (runID != null) {
    await queryClient.invalidateQueries({
      queryKey: ["workflows", "runs", runID],
    })
  }
}
