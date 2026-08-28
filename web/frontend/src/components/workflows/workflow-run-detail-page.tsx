import { IconPlayerStop, IconRotateClockwise } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type WorkflowRunEvent,
  cancelWorkflowRun,
  checkWorkflowDependencies,
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  retryWorkflowRun,
  workflowRunEventsStreamURL,
} from "@/api/workflows"
import { CollectionDetailShell } from "@/components/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

import {
  EventsPanel,
  ExecutionPanel,
  ManagedExecutionPanel,
  RunGraphPanel,
  RunSummary,
} from "./workflow-authoring-page"
import {
  WorkflowCancelDialog,
  type WorkflowCancelTarget,
} from "./workflow-cancel-dialog"
import {
  workflowDependencyFence,
  workflowDependencyFenceMessage,
} from "./workflow-dependency-fence"
import { trustedWorkflowRunOrigin } from "./workflow-run-origin"

const terminalStatuses = new Set(["succeeded", "failed", "canceled", "skipped"])
const activeStatuses = new Set(["running", "waiting"])
const streamKinds = [
  "workflow.run.start",
  "workflow.run.end",
  "workflow.run.failed",
  "workflow.run.canceled",
  "workflow.job.start",
  "workflow.job.end",
  "workflow.job.failed",
  "workflow.step.start",
  "workflow.step.end",
  "workflow.step.failed",
] as const

export function WorkflowRunDetailPage({
  runID,
  onBack,
  onRetryCreated,
}: {
  runID: string
  onBack: () => void
  onRetryCreated: (runID: string) => void
}) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ["workflows", "runs", runID],
    queryFn: () => getWorkflowRun(runID),
    retry: false,
    refetchInterval: ({ state }) =>
      state.data && !terminalStatuses.has(state.data.status) ? 3_000 : false,
  })
  const run = query.data
  const runIdentity = run?.id
  const refetchRun = query.refetch
  const events = useQuery({
    queryKey: ["workflows", "runs", runID, "events"],
    queryFn: () => getWorkflowRunEvents(runID),
    enabled: run != null,
    refetchInterval:
      run == null || terminalStatuses.has(run.status) ? false : 3_000,
  })
  const graph = useQuery({
    queryKey: ["workflows", "runs", runID, "graph"],
    queryFn: () => getWorkflowRunGraph(runID),
    enabled: run != null,
    refetchInterval:
      run == null || terminalStatuses.has(run.status) ? false : 5_000,
  })
  const retryableRef =
    run && !run.workflow_ref.startsWith("draft:") ? run.workflow_ref : null
  const dependency = useQuery({
    queryKey: ["workflows", "dependencies", "retry", retryableRef],
    queryFn: ({ signal }) =>
      retryableRef == null
        ? Promise.reject(new Error("This run is not retryable."))
        : checkWorkflowDependencies({ ref: retryableRef }, signal),
    enabled:
      run != null &&
      retryableRef != null &&
      terminalStatuses.has(run.status) &&
      trustedWorkflowRunOrigin(run.origin)?.kind !==
        "external_event_draft_test",
    retry: false,
  })
  const [streamedEvents, setStreamedEvents] = useState<WorkflowRunEvent[]>([])
  const [streaming, setStreaming] = useState(false)
  const [cancelTarget, setCancelTarget] = useState<WorkflowCancelTarget | null>(
    null,
  )
  const [retrySecrets, setRetrySecrets] = useState("{}")

  useEffect(() => {
    setStreamedEvents([])
    if (
      runIdentity == null ||
      typeof window === "undefined" ||
      typeof window.EventSource === "undefined"
    ) {
      setStreaming(false)
      return
    }
    setStreaming(true)
    const source = new window.EventSource(workflowRunEventsStreamURL(runID))
    const receive = (event: Event) => {
      try {
        const next = JSON.parse(
          (event as MessageEvent<string>).data,
        ) as WorkflowRunEvent
        setStreamedEvents((current) => mergeEvents(current, [next]))
        if (
          [
            "workflow.run.end",
            "workflow.run.failed",
            "workflow.run.canceled",
          ].includes(next.kind)
        ) {
          setStreaming(false)
          source.close()
          void refetchRun()
        }
      } catch {
        // Polling remains the fallback for malformed stream messages.
      }
    }
    for (const kind of streamKinds) source.addEventListener(kind, receive)
    source.onerror = () => {
      setStreaming(false)
      source.close()
    }
    return () => source.close()
  }, [refetchRun, runID, runIdentity])

  const displayedEvents = useMemo(
    () => mergeEvents(events.data?.events ?? [], streamedEvents),
    [events.data?.events, streamedEvents],
  )
  const cancel = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      cancelWorkflowRun(id, reason),
    onSuccess: (updated) => {
      queryClient.setQueryData(["workflows", "runs", runID], updated)
      void queryClient.invalidateQueries({
        queryKey: ["workflows", "runs", "collection"],
      })
      setCancelTarget(null)
      toast.success("Workflow cancellation requested")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const fence = workflowDependencyFence(
    retryableRef,
    dependency.isFetching
      ? "loading"
      : dependency.isError
        ? "error"
        : dependency.data
          ? "current"
          : "idle",
    dependency.data,
  )
  const retry = useMutation({
    mutationFn: async () => {
      if (run == null || fence.status !== "ready" || !fence.revision) {
        throw new Error(workflowDependencyFenceMessage("retry", fence))
      }
      return retryWorkflowRun(run.id, {
        expected_dependency_revision: fence.revision,
        secrets: parseStringObject(retrySecrets),
      })
    },
    onSuccess: ({ result, error }) => {
      void queryClient.invalidateQueries({ queryKey: ["workflows", "runs"] })
      if (error) toast.error(`Workflow retry ${result.status}: ${error}`)
      else toast.success("Workflow retry started")
      onRetryCreated(result.run_id)
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const origin = trustedWorkflowRunOrigin(run?.origin)
  const canCancel = run != null && activeStatuses.has(run.status)
  const canRetry =
    run != null &&
    terminalStatuses.has(run.status) &&
    !run.workflow_ref.startsWith("draft:") &&
    origin?.kind !== "external_event_draft_test" &&
    fence.status === "ready" &&
    !retry.isPending
  const notFound = statusOf(query.error) === 404

  return (
    <>
      <CollectionDetailShell
        title={run?.workflow_ref ?? "Workflow run"}
        identity={run?.id ?? runID}
        status={
          run ? <Badge variant="outline">{title(run.status)}</Badge> : undefined
        }
        actions={
          run ? (
            <>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!canCancel || cancel.isPending}
                onClick={() =>
                  setCancelTarget({ id: run.id, workflowRef: run.workflow_ref })
                }
              >
                <IconPlayerStop /> Cancel
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!canRetry}
                onClick={() => retry.mutate()}
              >
                <IconRotateClockwise />{" "}
                {retry.isPending ? "Retrying…" : "Retry"}
              </Button>
            </>
          ) : undefined
        }
        loading={query.isPending}
        error={
          notFound
            ? undefined
            : query.error
              ? errorMessage(query.error)
              : undefined
        }
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="Back to workflow runs"
        contentClassName="max-w-[100rem]"
      >
        {run ? (
          <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(280px,0.65fr)]">
            <RunSummary run={run} />
            <RunGraphPanel graph={graph.data} loading={graph.isLoading} />
            <ExecutionPanel run={run} />
            <ManagedExecutionPanel run={run} />
            <EventsPanel
              events={displayedEvents}
              loading={events.isLoading}
              streaming={streaming}
            />
            {terminalStatuses.has(run.status) &&
            !run.workflow_ref.startsWith("draft:") ? (
              <Card size="sm">
                <CardHeader>
                  <CardTitle>Retry secrets</CardTitle>
                </CardHeader>
                <CardContent className="grid gap-2">
                  <Label htmlFor="workflow-run-retry-secrets">
                    Secrets JSON
                  </Label>
                  <Textarea
                    id="workflow-run-retry-secrets"
                    value={retrySecrets}
                    onChange={(event) => setRetrySecrets(event.target.value)}
                    disabled={retry.isPending}
                    spellCheck={false}
                    className="min-h-20 font-mono text-xs"
                  />
                  <p className="text-muted-foreground text-xs">
                    {workflowDependencyFenceMessage("retry", fence)}
                  </p>
                </CardContent>
              </Card>
            ) : null}
          </div>
        ) : null}
      </CollectionDetailShell>
      <WorkflowCancelDialog
        target={cancelTarget}
        pending={cancel.isPending}
        requestError={cancel.isError ? errorMessage(cancel.error) : undefined}
        onDismiss={() => setCancelTarget(null)}
        onConfirm={(reason) => {
          if (cancelTarget) cancel.mutate({ id: cancelTarget.id, reason })
        }}
      />
    </>
  )
}

function mergeEvents(
  left: WorkflowRunEvent[],
  right: WorkflowRunEvent[],
): WorkflowRunEvent[] {
  const values = new Map<string, WorkflowRunEvent>()
  for (const event of [...left, ...right]) {
    values.set(
      `${event.time}\u0000${event.kind}\u0000${event.run_id}\u0000${event.job_id ?? ""}\u0000${event.step_id ?? ""}`,
      event,
    )
  }
  return [...values.values()].sort((a, b) => a.time.localeCompare(b.time))
}

function parseStringObject(value: string): Record<string, string> | undefined {
  const parsed = JSON.parse(value) as unknown
  if (parsed == null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Retry secrets must be a JSON object.")
  }
  for (const item of Object.values(parsed)) {
    if (typeof item !== "string")
      throw new Error("Retry secret values must be strings.")
  }
  return Object.keys(parsed).length === 0
    ? undefined
    : (parsed as Record<string, string>)
}

function statusOf(error: unknown): number | undefined {
  return error != null && typeof error === "object" && "status" in error
    ? Number(error.status)
    : undefined
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Workflow run is unavailable."
}

function title(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/^./, (letter) => letter.toUpperCase())
}
