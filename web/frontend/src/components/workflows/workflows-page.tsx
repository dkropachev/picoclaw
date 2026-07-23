import {
  IconActivity,
  IconAlertTriangle,
  IconCheck,
  IconCode,
  IconDeviceFloppy,
  IconExternalLink,
  IconGitBranch,
  IconPencil,
  IconPlayerPlay,
  IconPlayerStop,
  IconRefresh,
  IconReload,
  IconRocket,
  IconRotateClockwise,
  IconSparkles,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type WorkflowCompatibilitySummary,
  type WorkflowDefinition,
  type WorkflowDeliveryPayload,
  type WorkflowDevelopmentSession,
  type WorkflowDevelopmentTestResult,
  type WorkflowInputDefinition,
  type WorkflowRun,
  type WorkflowRunEvent,
  type WorkflowValidationIssue,
  type WorkflowValidationStamp,
  aiReviseWorkflowDevelopment,
  cancelWorkflowRun,
  discardWorkflowDevelopment,
  getWorkflowDevelopment,
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  listWorkflowRuns,
  listWorkflows,
  publishWorkflowDevelopment,
  reloadWorkflows,
  retryWorkflowRun,
  revalidateWorkflows,
  reviseWorkflowDevelopment,
  runWorkflow,
  startWorkflowDevelopment,
  testWorkflowDevelopment,
  validateWorkflowDevelopment,
  workflowRunEventsStreamURL,
} from "@/api/workflows"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

const terminalStatuses = new Set(["succeeded", "failed", "canceled", "skipped"])
const workflowEventStreamKinds = [
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
const workflowTerminalEventKinds = new Set([
  "workflow.run.end",
  "workflow.run.failed",
  "workflow.run.canceled",
])

type PageMode = "develop" | "operate"
type DevelopmentPendingAction =
  | "start-ai"
  | "start"
  | "save"
  | "ai-revise"
  | "regenerate"
  | "validate"
  | "test"
  | "test-running"
  | "publish"
  | "discard"
type WorkflowDevelopmentMutationResult = {
  session: WorkflowDevelopmentSession
  conflict?: boolean
}
type DraftTestSnapshot = {
  sessionID: string
  draftKey: string
  runID?: string
  status: string
  error?: string
  testedAt: string
}
type DraftEditorSnapshot = {
  sessionID: string
  prompt: string
  targetRef: string
  yaml: string
}
type WorkflowRepairStart = {
  ref: string
  status?: string
}
type WorkflowRunInputValues = Record<string, string>
type WorkflowRunSecretValues = Record<string, string>

export function WorkflowsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<PageMode>("develop")
  const [query, setQuery] = useState("")
  const [selectedRunID, setSelectedRunID] = useState<string | null>(null)
  const [startPrompt, setStartPrompt] = useState("")
  const [startTargetRef, setStartTargetRef] = useState("")
  const [draftPrompt, setDraftPrompt] = useState("")
  const [draftTargetRef, setDraftTargetRef] = useState("")
  const [draftYAML, setDraftYAML] = useState("")
  const [testInputsJSON, setTestInputsJSON] = useState("{}")
  const [testSecretsJSON, setTestSecretsJSON] = useState("{}")
  const [testSession, setTestSession] = useState("")
  const [testDeliveryJSON, setTestDeliveryJSON] = useState("{}")
  const [selectedWorkflowRef, setSelectedWorkflowRef] = useState<string | null>(
    null,
  )
  const [runInputValues, setRunInputValues] = useState<WorkflowRunInputValues>(
    {},
  )
  const [runSecretValues, setRunSecretValues] =
    useState<WorkflowRunSecretValues>({})
  const [runSecretsJSON, setRunSecretsJSON] = useState("{}")
  const [runSession, setRunSession] = useState("")
  const [runDeliveryJSON, setRunDeliveryJSON] = useState("{}")
  const [retrySecretsJSON, setRetrySecretsJSON] = useState("{}")
  const [lastDraftTest, setLastDraftTest] = useState<DraftTestSnapshot | null>(
    null,
  )
  const [appliedDraftSnapshot, setAppliedDraftSnapshot] =
    useState<DraftEditorSnapshot | null>(null)
  const [streamedRunID, setStreamedRunID] = useState<string | null>(null)
  const [streamedEvents, setStreamedEvents] = useState<WorkflowRunEvent[]>([])
  const [eventStreamActive, setEventStreamActive] = useState(false)

  const workflowsQuery = useQuery({
    queryKey: ["workflows"],
    queryFn: listWorkflows,
  })
  const developmentQuery = useQuery({
    queryKey: ["workflows", "development"],
    queryFn: getWorkflowDevelopment,
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

  const session = developmentQuery.data?.session ?? null
  const runs = useMemo(() => runsQuery.data?.runs ?? [], [runsQuery.data?.runs])
  const workflows = useMemo(
    () => workflowsQuery.data?.workflows ?? [],
    [workflowsQuery.data?.workflows],
  )
  const compatibility = workflowsQuery.data?.compatibility
  const compatibilityByRef = useMemo(() => {
    const map = new Map<string, WorkflowValidationStamp>()
    for (const stamp of compatibility?.workflows ?? []) {
      map.set(stamp.workflow_ref, stamp)
    }
    return map
  }, [compatibility?.workflows])

  const selectedRun =
    runQuery.data ?? runs.find((run) => run.id === selectedRunID)
  const queriedEvents = useMemo(
    () => eventsQuery.data?.events ?? [],
    [eventsQuery.data?.events],
  )
  const selectedEvents = useMemo(() => {
    if (streamedRunID !== selectedRunID) {
      return queriedEvents
    }
    return mergeWorkflowEventLists(queriedEvents, streamedEvents)
  }, [queriedEvents, selectedRunID, streamedEvents, streamedRunID])

  useEffect(() => {
    if (lastDraftTest?.status !== "running" || !lastDraftTest.runID) {
      return
    }
    const draftRun =
      selectedRun?.id === lastDraftTest.runID
        ? selectedRun
        : runs.find((run) => run.id === lastDraftTest.runID)
    if (draftRun == null || !terminalStatuses.has(draftRun.status)) {
      return
    }
    setLastDraftTest((current) => {
      if (
        current?.status !== "running" ||
        current.runID !== lastDraftTest.runID
      ) {
        return current
      }
      return {
        ...current,
        status: draftRun.status,
        error: draftRun.error || draftRun.cancel_reason,
        testedAt: draftRun.completed_at ?? new Date().toISOString(),
      }
    })
    void invalidateWorkflowQueries(queryClient)
  }, [
    lastDraftTest?.runID,
    lastDraftTest?.status,
    queryClient,
    runs,
    selectedRun,
  ])

  const applySessionDraft = useCallback(
    (nextSession: WorkflowDevelopmentSession) => {
      setDraftPrompt(nextSession.prompt ?? "")
      setDraftTargetRef(nextSession.target_workflow_ref)
      setDraftYAML(nextSession.yaml)
      const nextSnapshot = draftEditorSnapshotFromSession(nextSession)
      setAppliedDraftSnapshot((current) =>
        draftEditorSnapshotsEqual(current, nextSnapshot)
          ? current
          : nextSnapshot,
      )
    },
    [],
  )

  useEffect(() => {
    if (selectedRunID == null && runs.length > 0) {
      setSelectedRunID(runs[0].id)
    }
  }, [runs, selectedRunID])

  useEffect(() => {
    if (workflows.length === 0) {
      setSelectedWorkflowRef(null)
      return
    }
    if (
      selectedWorkflowRef == null ||
      !workflows.some((workflow) => workflow.ref === selectedWorkflowRef)
    ) {
      setSelectedWorkflowRef(workflows[0].ref)
    }
  }, [selectedWorkflowRef, workflows])

  useEffect(() => {
    if (
      selectedRunID == null ||
      typeof window === "undefined" ||
      typeof window.EventSource === "undefined"
    ) {
      setStreamedRunID(null)
      setStreamedEvents([])
      setEventStreamActive(false)
      return
    }

    const runID = selectedRunID
    setStreamedRunID(runID)
    setStreamedEvents([])
    setEventStreamActive(true)
    const source = new window.EventSource(workflowRunEventsStreamURL(runID))
    const onEvent = (event: Event) => {
      const message = event as MessageEvent<string>
      try {
        const nextEvent = JSON.parse(message.data) as WorkflowRunEvent
        setStreamedEvents((current) =>
          mergeWorkflowEventLists(current, [nextEvent]),
        )
        if (workflowTerminalEventKinds.has(nextEvent.kind)) {
          void invalidateRunQueries(queryClient, runID)
          void invalidateWorkflowQueries(queryClient)
          setEventStreamActive(false)
          source.close()
        }
      } catch {
        // Ignore malformed event-stream messages and keep the polling fallback.
      }
    }

    for (const kind of workflowEventStreamKinds) {
      source.addEventListener(kind, onEvent)
    }
    source.onerror = () => {
      setEventStreamActive(false)
      source.close()
    }
    return () => {
      setEventStreamActive(false)
      source.close()
    }
  }, [queryClient, selectedRunID])

  useEffect(() => {
    setRetrySecretsJSON("{}")
  }, [selectedRunID])

  useEffect(() => {
    if (session == null) {
      setDraftPrompt("")
      setDraftTargetRef("")
      setDraftYAML("")
      setAppliedDraftSnapshot(null)
      return
    }
    if (
      appliedDraftSnapshot?.sessionID === session.id &&
      !editorMatchesDraftSnapshot(
        { prompt: draftPrompt, targetRef: draftTargetRef, yaml: draftYAML },
        appliedDraftSnapshot,
      )
    ) {
      return
    }
    applySessionDraft(session)
  }, [
    appliedDraftSnapshot,
    applySessionDraft,
    draftPrompt,
    draftTargetRef,
    draftYAML,
    session,
  ])

  useEffect(() => {
    setLastDraftTest(draftTestSnapshotFromSession(session))
  }, [session])

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

  const invalidWorkflows = useMemo(
    () =>
      (compatibility?.workflows ?? []).filter((workflow) =>
        ["invalid", "pending_revalidation"].includes(workflow.status),
      ),
    [compatibility?.workflows],
  )
  const currentDraftKey = useMemo(
    () => draftKey(draftTargetRef, draftYAML),
    [draftTargetRef, draftYAML],
  )
  const draftDirty =
    session != null &&
    (session.target_workflow_ref !== draftTargetRef ||
      normalizeWorkflowYAML(session.yaml) !== normalizeWorkflowYAML(draftYAML))
  const currentValidationInvalid =
    session?.validation?.valid === false && !draftDirty
  const lastDraftTestStale =
    lastDraftTest != null && lastDraftTest.draftKey !== currentDraftKey
  const draftTestRunning = lastDraftTest?.status === "running"
  const publishTestReady = isPublishTestReady(lastDraftTest, lastDraftTestStale)
  const selectedWorkflow = useMemo(
    () => workflows.find((workflow) => workflow.ref === selectedWorkflowRef),
    [selectedWorkflowRef, workflows],
  )
  const selectedWorkflowContractSignature = useMemo(
    () => workflowRunContractSignature(selectedWorkflow ?? null),
    [selectedWorkflow],
  )
  useEffect(() => {
    setRunInputValues(workflowRunInitialInputValues(selectedWorkflow ?? null))
    setRunSecretValues(workflowRunInitialSecretValues(selectedWorkflow ?? null))
  }, [selectedWorkflow, selectedWorkflowContractSignature])
  const testPayloadError = firstMessage([
    jsonObjectValidationMessage(testInputsJSON, "Inputs"),
    jsonStringObjectValidationMessage(testSecretsJSON, "Secrets"),
    jsonObjectValidationMessage(testDeliveryJSON, "Delivery"),
  ])
  const testReadinessMessage = workflowTestReadinessMessage({
    session,
    targetRef: draftTargetRef,
    yaml: draftYAML,
    payloadError: testPayloadError,
    runningTest: draftTestRunning,
  })
  const runSecretsJSONError = jsonStringObjectValidationMessage(
    runSecretsJSON,
    "Secrets",
  )
  const runPayloadError = firstMessage([
    workflowRunInputValidationMessage(selectedWorkflow ?? null, runInputValues),
    runSecretsJSONError,
    workflowRunSecretValidationMessage(
      selectedWorkflow ?? null,
      runSecretValues,
      runSecretsJSON,
    ),
    jsonObjectValidationMessage(runDeliveryJSON, "Delivery"),
  ])
  const retryPayloadError = jsonStringObjectValidationMessage(
    retrySecretsJSON,
    "Retry secrets",
  )
  const publishReadinessMessage = workflowPublishReadinessMessage({
    session,
    targetRef: draftTargetRef,
    yaml: draftYAML,
    currentValidationInvalid,
    testResult: lastDraftTest,
    testStale: lastDraftTestStale,
  })
  const selectedWorkflowStamp =
    selectedWorkflowRef == null
      ? undefined
      : compatibilityByRef.get(selectedWorkflowRef)
  const selectedRunWorkflow =
    selectedRun == null || selectedRun.workflow_ref.startsWith("draft:")
      ? null
      : (workflows.find(
          (workflow) => workflow.ref === selectedRun.workflow_ref,
        ) ?? null)
  const selectedRunWorkflowStamp =
    selectedRun == null || selectedRun.workflow_ref.startsWith("draft:")
      ? undefined
      : compatibilityByRef.get(selectedRun.workflow_ref)
  const canRunSelectedWorkflow =
    selectedWorkflow != null &&
    !selectedWorkflow.error &&
    selectedWorkflow.ref.trim() !== "" &&
    isRunnableWorkflowStatus(selectedWorkflowStamp?.status, compatibility) &&
    runPayloadError == null

  const startMutation = useMutation({
    mutationFn: startWorkflowDevelopment,
    onSuccess: ({ session: nextSession, conflict }) => {
      toast.success(
        conflict
          ? "Workflow development resumed"
          : "Workflow development started",
      )
      setMode("develop")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const startWithAIMutation = useMutation({
    mutationFn: async (): Promise<WorkflowDevelopmentMutationResult> => {
      const started = await startWorkflowDevelopment({
        reason: "new",
        prompt: startPrompt,
        target_ref: startTargetRef || undefined,
      })
      if (started.conflict) {
        return started
      }
      return aiReviseWorkflowDevelopment({
        prompt: startPrompt,
        target_ref: startTargetRef || started.session.target_workflow_ref,
      })
    },
    onSuccess: ({ session: nextSession, conflict }) => {
      toast.success(
        conflict
          ? "Workflow development resumed"
          : nextSession.validation?.valid
            ? "AI workflow draft ready"
            : "AI draft needs fixes",
      )
      setMode("develop")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => {
      toast.error(errorMessage(err))
      void invalidateWorkflowQueries(queryClient)
    },
  })

  const startRepairWithAIMutation = useMutation({
    mutationFn: async ({
      ref,
      status,
    }: WorkflowRepairStart): Promise<WorkflowDevelopmentMutationResult> => {
      const prompt = workflowRepairPrompt(status)
      const started = await startWorkflowDevelopment({
        reason: "version_revalidation",
        prompt,
        ref,
        target_ref: ref,
      })
      if (started.conflict) {
        return started
      }
      return aiReviseWorkflowDevelopment({
        prompt,
        target_ref: ref,
        yaml: started.session.yaml,
      })
    },
    onSuccess: ({ session: nextSession, conflict }) => {
      toast.success(
        conflict
          ? "Workflow development resumed"
          : nextSession.validation?.valid
            ? "AI workflow review ready"
            : "AI workflow repair needs fixes",
      )
      setMode("develop")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => {
      toast.error(errorMessage(err))
      void invalidateWorkflowQueries(queryClient)
    },
  })

  const saveMutation = useMutation({
    mutationFn: () =>
      reviseWorkflowDevelopment({
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
      }),
    onSuccess: ({ session: nextSession }) => {
      toast.success("Workflow draft saved")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const regenerateMutation = useMutation({
    mutationFn: () =>
      reviseWorkflowDevelopment({
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        regenerate: true,
      }),
    onSuccess: ({ session: nextSession }) => {
      toast.success("Workflow draft regenerated")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const aiReviseMutation = useMutation({
    mutationFn: (promptOverride?: string) =>
      aiReviseWorkflowDevelopment({
        prompt: promptOverride ?? draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
      }),
    onSuccess: ({ session: nextSession }) => {
      toast.success(
        nextSession.validation?.valid
          ? "AI workflow draft ready"
          : "AI draft needs fixes",
      )
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const fixDraftTestMutation = useMutation({
    mutationFn: async () => {
      let repairRun = selectedRun
      let repairEvents = selectedEvents
      const runID = lastDraftTest?.runID
      if (runID) {
        try {
          repairRun = await queryClient.fetchQuery({
            queryKey: ["workflows", "runs", runID],
            queryFn: () => getWorkflowRun(runID),
          })
        } catch {
          // Keep the cached context if the detailed run lookup is unavailable.
        }
        try {
          const eventResult = await queryClient.fetchQuery({
            queryKey: ["workflows", "runs", runID, "events"],
            queryFn: () => getWorkflowRunEvents(runID),
          })
          repairEvents = mergeWorkflowEventLists(
            eventResult.events,
            streamedRunID === runID ? streamedEvents : [],
          )
        } catch {
          // Event context is helpful for repair, but the failed test itself is enough to proceed.
        }
      }
      return aiReviseWorkflowDevelopment({
        prompt: workflowDraftTestRepairPrompt(
          draftPrompt,
          lastDraftTest,
          lastDraftTestStale,
          repairRun,
          repairEvents,
        ),
        target_ref: draftTargetRef,
        yaml: draftYAML,
      })
    },
    onSuccess: ({ session: nextSession }) => {
      toast.success(
        nextSession.validation?.valid
          ? "AI workflow draft ready"
          : "AI draft needs fixes",
      )
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const validateMutation = useMutation({
    mutationFn: async () => {
      await reviseWorkflowDevelopment({
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
      })
      return validateWorkflowDevelopment()
    },
    onSuccess: ({ session: nextSession }) => {
      toast.success(
        nextSession.validation?.valid
          ? "Workflow validation passed"
          : "Workflow validation failed",
      )
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const testDraftMutation = useMutation<WorkflowDevelopmentTestResult, Error>({
    mutationFn: () =>
      testWorkflowDevelopment({
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
        inputs: parseJSONObject(testInputsJSON, "Inputs"),
        secrets: parseStringJSONObject(testSecretsJSON, "Secrets"),
        session: optionalString(testSession),
        delivery: parseDeliveryJSONObject(testDeliveryJSON, "Delivery"),
        async: true,
      }),
    onSuccess: ({ session: nextSession, result, error }) => {
      applySessionDraft(nextSession)
      setLastDraftTest({
        sessionID: nextSession.id,
        draftKey: draftKey(nextSession.target_workflow_ref, nextSession.yaml),
        runID: result?.run_id,
        status: result?.status ?? "validation_failed",
        error: error ?? result?.error,
        testedAt: new Date().toISOString(),
      })
      if (result?.run_id) {
        setSelectedRunID(result.run_id)
        if (error) {
          toast.error(`Draft test ${result.status}: ${error}`)
        } else {
          toast.success(`Draft test ${result.status}`)
        }
        void invalidateRunQueries(queryClient, result.run_id)
      } else {
        toast.error(error ?? "Draft test did not create a run")
      }
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const publishMutation = useMutation({
    mutationFn: async () => {
      await reviseWorkflowDevelopment({
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
      })
      return publishWorkflowDevelopment()
    },
    onSuccess: (result) => {
      toast.success(`Published ${result.workflow_ref}`)
      setMode("operate")
      setLastDraftTest(null)
      void invalidateWorkflowQueries(queryClient).then(() => {
        setSelectedWorkflowRef(result.workflow_ref)
      })
    },
    onError: (err) => {
      toast.error(errorMessage(err))
      void invalidateWorkflowQueries(queryClient)
    },
  })

  const discardMutation = useMutation({
    mutationFn: discardWorkflowDevelopment,
    onSuccess: () => {
      toast.success("Workflow development discarded")
      setLastDraftTest(null)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const revalidateMutation = useMutation({
    mutationFn: revalidateWorkflows,
    onSuccess: (summary) => {
      const invalid = summary.counts.invalid ?? 0
      toast.success(
        invalid === 0
          ? "Workflows revalidated"
          : `Revalidated with ${invalid} invalid workflow(s)`,
      )
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

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

  const runWorkflowMutation = useMutation({
    mutationFn: () => {
      if (selectedWorkflowRef == null) {
        throw new Error("Select a workflow to run")
      }
      return runWorkflow({
        ref: selectedWorkflowRef,
        inputs: workflowRunInputsPayload(
          selectedWorkflow ?? null,
          runInputValues,
        ),
        secrets: workflowRunSecretsPayload(
          selectedWorkflow ?? null,
          runSecretValues,
          runSecretsJSON,
        ),
        session: optionalString(runSession),
        delivery: parseDeliveryJSONObject(runDeliveryJSON, "Delivery"),
        async: true,
      })
    },
    onSuccess: ({ result, error }) => {
      toast[error ? "error" : "success"](
        error
          ? `Workflow run ${result.status}: ${error}`
          : `Workflow run ${result.status}`,
      )
      setSelectedRunID(result.run_id)
      void invalidateRunQueries(queryClient, result.run_id)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const cancelMutation = useMutation({
    mutationFn: (runID: string) => cancelWorkflowRun(runID),
    onSuccess: () => {
      toast.success("Workflow run canceled")
      void invalidateRunQueries(queryClient, selectedRunID)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const retryMutation = useMutation({
    mutationFn: (runID: string) =>
      retryWorkflowRun(
        runID,
        parseStringJSONObject(retrySecretsJSON, "Retry secrets"),
      ),
    onSuccess: ({ result, error }) => {
      toast[error ? "error" : "success"](
        error
          ? `Workflow retry ${result.status}: ${error}`
          : "Workflow retry started",
      )
      setRetrySecretsJSON("{}")
      setSelectedRunID(result.run_id)
      void invalidateRunQueries(queryClient, result.run_id)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const startScaffold = () => {
    startMutation.mutate({
      reason: "new",
      prompt: startPrompt,
      target_ref: startTargetRef || undefined,
    })
  }
  const canStartNew =
    session == null &&
    startPrompt.trim() !== "" &&
    !startMutation.isPending &&
    !startWithAIMutation.isPending &&
    !startRepairWithAIMutation.isPending
  const canTestDraft =
    session != null &&
    draftTargetRef.trim() !== "" &&
    draftYAML.trim() !== "" &&
    testPayloadError == null &&
    !draftTestRunning &&
    !testDraftMutation.isPending
  const canPublish =
    session != null &&
    !publishMutation.isPending &&
    !draftTestRunning &&
    draftTargetRef.trim() !== "" &&
    draftYAML.trim() !== "" &&
    !currentValidationInvalid &&
    publishTestReady
  const canCancel = selectedRun?.status === "running"
  const canRetry =
    selectedRun != null &&
    terminalStatuses.has(selectedRun.status) &&
    !selectedRun.workflow_ref.startsWith("draft:") &&
    isRunnableWorkflowStatus(selectedRunWorkflowStamp?.status, compatibility) &&
    retryPayloadError == null
  const retryReadinessMessage =
    retryPayloadError ??
    workflowRetryReadinessMessage(
      selectedRun,
      selectedRunWorkflow,
      selectedRunWorkflowStamp,
      compatibility,
    )
  const developmentPendingAction: DevelopmentPendingAction | null =
    startWithAIMutation.isPending || startRepairWithAIMutation.isPending
      ? "start-ai"
      : startMutation.isPending
        ? "start"
        : saveMutation.isPending
          ? "save"
          : aiReviseMutation.isPending || fixDraftTestMutation.isPending
            ? "ai-revise"
            : regenerateMutation.isPending
              ? "regenerate"
              : validateMutation.isPending
                ? "validate"
                : testDraftMutation.isPending
                  ? "test"
                  : draftTestRunning
                    ? "test-running"
                    : publishMutation.isPending
                      ? "publish"
                      : discardMutation.isPending
                        ? "discard"
                        : null
  const developmentBusy =
    startMutation.isPending ||
    startWithAIMutation.isPending ||
    startRepairWithAIMutation.isPending ||
    saveMutation.isPending ||
    aiReviseMutation.isPending ||
    fixDraftTestMutation.isPending ||
    regenerateMutation.isPending ||
    validateMutation.isPending ||
    testDraftMutation.isPending ||
    draftTestRunning ||
    publishMutation.isPending ||
    discardMutation.isPending

  const refresh = () => {
    void invalidateWorkflowQueries(queryClient)
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.workflows", "Workflows")}>
        <div className="border-border bg-background flex rounded-md border p-0.5">
          <Button
            variant={mode === "develop" ? "secondary" : "ghost"}
            size="sm"
            onClick={() => setMode("develop")}
          >
            <IconSparkles className="size-4" />
            Develop
          </Button>
          <Button
            variant={mode === "operate" ? "secondary" : "ghost"}
            size="sm"
            onClick={() => setMode("operate")}
          >
            <IconActivity className="size-4" />
            Operate
          </Button>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={refresh}
          disabled={
            workflowsQuery.isFetching ||
            runsQuery.isFetching ||
            developmentQuery.isFetching
          }
          title="Refresh"
        >
          <IconRefresh className="size-4" />
          Refresh
        </Button>
      </PageHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-hidden p-4 sm:p-6">
        <CompatibilityBanner
          compatibility={compatibility}
          invalidWorkflows={invalidWorkflows}
          onRevalidate={() => revalidateMutation.mutate()}
          revalidating={revalidateMutation.isPending}
        />
        {mode === "develop" ? (
          <DevelopSurface
            session={session}
            workflows={workflows}
            compatibilityByRef={compatibilityByRef}
            invalidWorkflows={invalidWorkflows}
            startPrompt={startPrompt}
            startTargetRef={startTargetRef}
            draftPrompt={draftPrompt}
            draftTargetRef={draftTargetRef}
            draftYAML={draftYAML}
            testInputsJSON={testInputsJSON}
            testSecretsJSON={testSecretsJSON}
            testSession={testSession}
            testDeliveryJSON={testDeliveryJSON}
            onStartPromptChange={setStartPrompt}
            onStartTargetRefChange={setStartTargetRef}
            onDraftPromptChange={setDraftPrompt}
            onDraftTargetRefChange={setDraftTargetRef}
            onDraftYAMLChange={setDraftYAML}
            onTestInputsJSONChange={setTestInputsJSON}
            onTestSecretsJSONChange={setTestSecretsJSON}
            onTestSessionChange={setTestSession}
            onTestDeliveryJSONChange={setTestDeliveryJSON}
            onStartWithAI={() => startWithAIMutation.mutate()}
            onStartScaffold={startScaffold}
            onStartEdit={(ref) =>
              startMutation.mutate({ reason: "edit", ref, target_ref: ref })
            }
            onStartRepair={(ref) =>
              startMutation.mutate({
                reason: "version_revalidation",
                ref,
                target_ref: ref,
              })
            }
            onStartRepairWithAI={(ref, status) =>
              startRepairWithAIMutation.mutate({ ref, status })
            }
            onSave={() => saveMutation.mutate()}
            onAIRevise={() => aiReviseMutation.mutate(undefined)}
            onFixTestWithAI={() => fixDraftTestMutation.mutate()}
            onRegenerate={() => regenerateMutation.mutate()}
            onValidate={() => validateMutation.mutate()}
            onTest={() => testDraftMutation.mutate()}
            onPublish={() => publishMutation.mutate()}
            onDiscard={() => discardMutation.mutate()}
            onOpenTestRun={(runID) => {
              setSelectedRunID(runID)
              setMode("operate")
            }}
            canStartNew={canStartNew}
            canTestDraft={canTestDraft}
            canPublish={canPublish}
            testReadinessMessage={testReadinessMessage}
            publishReadinessMessage={publishReadinessMessage}
            draftDirty={draftDirty}
            currentValidationInvalid={currentValidationInvalid}
            lastDraftTest={lastDraftTest}
            lastDraftTestStale={lastDraftTestStale}
            pendingAction={developmentPendingAction}
            busy={developmentBusy}
          />
        ) : (
          <OperateSurface
            query={query}
            workflows={workflows}
            compatibilityByRef={compatibilityByRef}
            compatibility={compatibility}
            selectedWorkflowRef={selectedWorkflowRef}
            runInputValues={runInputValues}
            runSecretValues={runSecretValues}
            runSecretsJSON={runSecretsJSON}
            runSession={runSession}
            runDeliveryJSON={runDeliveryJSON}
            retrySecretsJSON={retrySecretsJSON}
            runs={filteredRuns}
            allRuns={runs}
            selectedRunID={selectedRunID}
            selectedRun={selectedRun}
            events={selectedEvents}
            graph={graphQuery.data}
            loadingRuns={runsQuery.isLoading}
            loadingEvents={eventsQuery.isLoading}
            streamingEvents={
              eventStreamActive && streamedRunID === selectedRunID
            }
            loadingGraph={graphQuery.isLoading}
            onQueryChange={setQuery}
            onSelectWorkflow={setSelectedWorkflowRef}
            onRunInputChange={(name, value) =>
              setRunInputValues((current) => ({ ...current, [name]: value }))
            }
            onRunSecretChange={(name, value) =>
              setRunSecretValues((current) => ({ ...current, [name]: value }))
            }
            onRunSecretsJSONChange={setRunSecretsJSON}
            onRunSessionChange={setRunSession}
            onRunDeliveryJSONChange={setRunDeliveryJSON}
            onRetrySecretsJSONChange={setRetrySecretsJSON}
            onSelectRun={setSelectedRunID}
            onReload={() => reloadMutation.mutate()}
            onRunWorkflow={() => runWorkflowMutation.mutate()}
            reloading={reloadMutation.isPending}
            runningWorkflow={runWorkflowMutation.isPending}
            onCancel={() =>
              selectedRun && cancelMutation.mutate(selectedRun.id)
            }
            onRetry={() => selectedRun && retryMutation.mutate(selectedRun.id)}
            canceling={cancelMutation.isPending}
            retrying={retryMutation.isPending}
            canRunWorkflow={canRunSelectedWorkflow}
            runPayloadError={runPayloadError}
            canCancel={canCancel}
            canRetry={canRetry}
            retryReadinessMessage={retryReadinessMessage}
          />
        )}
      </div>
    </div>
  )
}

function CompatibilityBanner({
  compatibility,
  invalidWorkflows,
  onRevalidate,
  revalidating,
}: {
  compatibility?: WorkflowCompatibilitySummary
  invalidWorkflows: WorkflowValidationStamp[]
  onRevalidate: () => void
  revalidating: boolean
}) {
  if (compatibility == null) {
    return null
  }
  const pending = compatibility.counts.pending_revalidation ?? 0
  const invalid = compatibility.counts.invalid ?? 0
  if (!compatibility.version_changed && pending === 0 && invalid === 0) {
    return null
  }
  return (
    <section className="border-border bg-card/60 flex flex-col gap-3 rounded-lg border px-4 py-3 md:flex-row md:items-center md:justify-between">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <IconAlertTriangle className="text-destructive size-4 shrink-0" />
          <h2 className="text-sm font-medium">Release revalidation</h2>
          <Badge variant={invalid > 0 ? "destructive" : "outline"}>
            {invalidWorkflows.length} blocked
          </Badge>
        </div>
        <div className="text-muted-foreground mt-1 truncate text-xs">
          Picoclaw {compatibility.current.picoclaw_version}
          {compatibility.current.git_commit
            ? ` (${compatibility.current.git_commit})`
            : ""}
        </div>
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-2">
        <Badge variant="outline">{pending} pending</Badge>
        <Badge variant={invalid > 0 ? "destructive" : "outline"}>
          {invalid} invalid
        </Badge>
        <Button
          variant="outline"
          size="sm"
          onClick={onRevalidate}
          disabled={revalidating}
        >
          <IconCheck className="size-4" />
          Revalidate
        </Button>
      </div>
    </section>
  )
}

function DevelopSurface({
  session,
  workflows,
  compatibilityByRef,
  invalidWorkflows,
  startPrompt,
  startTargetRef,
  draftPrompt,
  draftTargetRef,
  draftYAML,
  testInputsJSON,
  testSecretsJSON,
  testSession,
  testDeliveryJSON,
  onStartPromptChange,
  onStartTargetRefChange,
  onDraftPromptChange,
  onDraftTargetRefChange,
  onDraftYAMLChange,
  onTestInputsJSONChange,
  onTestSecretsJSONChange,
  onTestSessionChange,
  onTestDeliveryJSONChange,
  onStartWithAI,
  onStartScaffold,
  onStartEdit,
  onStartRepair,
  onStartRepairWithAI,
  onSave,
  onAIRevise,
  onFixTestWithAI,
  onRegenerate,
  onValidate,
  onTest,
  onPublish,
  onDiscard,
  onOpenTestRun,
  canStartNew,
  canTestDraft,
  canPublish,
  testReadinessMessage,
  publishReadinessMessage,
  draftDirty,
  currentValidationInvalid,
  lastDraftTest,
  lastDraftTestStale,
  pendingAction,
  busy,
}: {
  session: WorkflowDevelopmentSession | null
  workflows: WorkflowDefinition[]
  compatibilityByRef: Map<string, WorkflowValidationStamp>
  invalidWorkflows: WorkflowValidationStamp[]
  startPrompt: string
  startTargetRef: string
  draftPrompt: string
  draftTargetRef: string
  draftYAML: string
  testInputsJSON: string
  testSecretsJSON: string
  testSession: string
  testDeliveryJSON: string
  onStartPromptChange: (value: string) => void
  onStartTargetRefChange: (value: string) => void
  onDraftPromptChange: (value: string) => void
  onDraftTargetRefChange: (value: string) => void
  onDraftYAMLChange: (value: string) => void
  onTestInputsJSONChange: (value: string) => void
  onTestSecretsJSONChange: (value: string) => void
  onTestSessionChange: (value: string) => void
  onTestDeliveryJSONChange: (value: string) => void
  onStartWithAI: () => void
  onStartScaffold: () => void
  onStartEdit: (ref: string) => void
  onStartRepair: (ref: string) => void
  onStartRepairWithAI: (ref: string, status?: string) => void
  onSave: () => void
  onAIRevise: () => void
  onFixTestWithAI: () => void
  onRegenerate: () => void
  onValidate: () => void
  onTest: () => void
  onPublish: () => void
  onDiscard: () => void
  onOpenTestRun: (runID: string) => void
  canStartNew: boolean
  canTestDraft: boolean
  canPublish: boolean
  testReadinessMessage: string
  publishReadinessMessage: string
  draftDirty: boolean
  currentValidationInvalid: boolean
  lastDraftTest: DraftTestSnapshot | null
  lastDraftTestStale: boolean
  pendingAction: DevelopmentPendingAction | null
  busy: boolean
}) {
  const busyLabel = developmentBusyLabel(pendingAction)
  if (session == null) {
    const startingAI = pendingAction === "start-ai"
    const starting = pendingAction === "start"
    const startReadinessMessage = workflowStartReadinessMessage(
      startPrompt,
      pendingAction,
    )
    return (
      <div className="grid min-h-0 flex-1 gap-4 overflow-hidden lg:grid-cols-[minmax(320px,0.85fr)_minmax(0,1.15fr)]">
        <section className="border-border bg-card/40 flex min-h-0 flex-col rounded-lg border">
          <div className="border-border border-b px-4 py-3">
            <h2 className="text-sm font-medium">New workflow</h2>
          </div>
          <div className="flex min-h-0 flex-1 flex-col gap-3 p-4">
            <Textarea
              value={startPrompt}
              onChange={(event) => onStartPromptChange(event.target.value)}
              placeholder="Describe the workflow outcome"
              className="min-h-40 resize-none"
            />
            <Input
              value={startTargetRef}
              onChange={(event) => onStartTargetRefChange(event.target.value)}
              placeholder="workflows/name.yml"
              className="font-mono text-xs"
            />
            <div className="flex flex-wrap gap-2">
              <Button
                onClick={onStartWithAI}
                disabled={!canStartNew}
                title={!canStartNew ? startReadinessMessage : undefined}
              >
                <IconSparkles className="size-4" />
                {startingAI ? "Drafting" : "Start with AI"}
              </Button>
              <Button
                variant="outline"
                onClick={onStartScaffold}
                disabled={!canStartNew}
                title={!canStartNew ? startReadinessMessage : undefined}
              >
                <IconRotateClockwise className="size-4" />
                {starting ? "Starting" : "Start Scaffold"}
              </Button>
            </div>
            <div className="text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs">
              {startReadinessMessage}
            </div>
          </div>
        </section>

        <section className="border-border bg-card/40 flex min-h-0 flex-col overflow-hidden rounded-lg border">
          <div className="border-border flex items-center justify-between gap-3 border-b px-4 py-3">
            <h2 className="text-sm font-medium">Published workflows</h2>
            <Badge variant="outline">{workflows.length}</Badge>
          </div>
          <ScrollRegion
            label="Workflow development candidates"
            className="min-h-0 flex-1 overflow-auto p-3"
          >
            {invalidWorkflows.length > 0 ? (
              <div className="mb-3 grid gap-2">
                {invalidWorkflows.map((workflow) => {
                  const actionLabel =
                    workflow.status === "pending_revalidation" ? "Open" : "Open"
                  const aiActionLabel =
                    workflow.status === "pending_revalidation"
                      ? "AI Review"
                      : "AI Repair"
                  return (
                    <WorkflowCandidate
                      key={workflow.workflow_ref}
                      refName={workflow.workflow_ref}
                      status={workflow.status}
                      issues={workflowStampIssues(workflow)}
                      actionLabel={actionLabel}
                      onAction={() => onStartRepair(workflow.workflow_ref)}
                      aiActionLabel={aiActionLabel}
                      onAIAction={() =>
                        onStartRepairWithAI(
                          workflow.workflow_ref,
                          workflow.status,
                        )
                      }
                      blocked={busy}
                    />
                  )
                })}
              </div>
            ) : null}
            <div className="grid gap-2">
              {workflows.length === 0 ? (
                <EmptyPanel label="No definitions" compact />
              ) : (
                workflows.map((workflow) => {
                  const stamp = compatibilityByRef.get(workflow.ref)
                  return (
                    <WorkflowCandidate
                      key={workflow.ref}
                      refName={workflow.ref}
                      title={workflow.name}
                      status={workflow.error ? "invalid" : stamp?.status}
                      issues={
                        workflow.error
                          ? [{ message: workflow.error }]
                          : workflowStampIssues(stamp)
                      }
                      actionLabel="Edit"
                      onAction={() => onStartEdit(workflow.ref)}
                      blocked={busy}
                    />
                  )
                })
              )}
            </div>
          </ScrollRegion>
        </section>
      </div>
    )
  }

  return (
    <div className="grid min-h-0 flex-1 gap-4 overflow-hidden xl:grid-cols-[minmax(360px,0.75fr)_minmax(0,1.25fr)]">
      <section className="border-border bg-card/40 flex min-h-0 flex-col rounded-lg border">
        <DevelopmentHeader session={session} />
        {busyLabel ? <DevelopmentBusyBar label={busyLabel} /> : null}
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto p-4">
          <div className="grid gap-2">
            <label
              className="text-muted-foreground text-xs"
              htmlFor="workflow-target-ref"
            >
              Target
            </label>
            <Input
              id="workflow-target-ref"
              value={draftTargetRef}
              onChange={(event) => onDraftTargetRefChange(event.target.value)}
              className="font-mono text-xs"
            />
          </div>
          <div className="grid gap-2">
            <label
              className="text-muted-foreground text-xs"
              htmlFor="workflow-brief"
            >
              AI brief
            </label>
            <Textarea
              id="workflow-brief"
              value={draftPrompt}
              onChange={(event) => onDraftPromptChange(event.target.value)}
              className="min-h-32 resize-none"
            />
          </div>
          <ValidationPanel validation={session.validation} />
          <Panel title="Test run">
            <div className="grid gap-3">
              <div className="grid gap-2">
                <label
                  className="text-muted-foreground text-xs"
                  htmlFor="workflow-test-inputs"
                >
                  Inputs JSON
                </label>
                <Textarea
                  id="workflow-test-inputs"
                  value={testInputsJSON}
                  onChange={(event) =>
                    onTestInputsJSONChange(event.target.value)
                  }
                  spellCheck={false}
                  className="min-h-20 resize-none font-mono text-xs"
                />
              </div>
              <div className="grid gap-2">
                <label
                  className="text-muted-foreground text-xs"
                  htmlFor="workflow-test-secrets"
                >
                  Secrets JSON
                </label>
                <Textarea
                  id="workflow-test-secrets"
                  value={testSecretsJSON}
                  onChange={(event) =>
                    onTestSecretsJSONChange(event.target.value)
                  }
                  spellCheck={false}
                  className="min-h-20 resize-none font-mono text-xs"
                />
              </div>
              <div className="grid gap-2">
                <label
                  className="text-muted-foreground text-xs"
                  htmlFor="workflow-test-session"
                >
                  Session
                </label>
                <Input
                  id="workflow-test-session"
                  value={testSession}
                  onChange={(event) => onTestSessionChange(event.target.value)}
                  placeholder="workflow:test"
                  className="font-mono text-xs"
                />
              </div>
              <div className="grid gap-2">
                <label
                  className="text-muted-foreground text-xs"
                  htmlFor="workflow-test-delivery"
                >
                  Delivery JSON
                </label>
                <Textarea
                  id="workflow-test-delivery"
                  value={testDeliveryJSON}
                  onChange={(event) =>
                    onTestDeliveryJSONChange(event.target.value)
                  }
                  spellCheck={false}
                  className="min-h-20 resize-none font-mono text-xs"
                />
              </div>
              <DraftTestResultPanel
                result={lastDraftTest}
                stale={lastDraftTestStale}
                onOpenRun={onOpenTestRun}
                onFixWithAI={onFixTestWithAI}
                fixingWithAI={pendingAction === "ai-revise"}
              />
              <div
                className={cn(
                  "text-xs",
                  canTestDraft ? "text-muted-foreground" : "text-destructive",
                )}
              >
                {testReadinessMessage}
              </div>
            </div>
          </Panel>
          <PublishReadinessPanel
            targetRef={draftTargetRef}
            yaml={draftYAML}
            validation={session.validation}
            validationStale={draftDirty}
            currentValidationInvalid={currentValidationInvalid}
            testResult={lastDraftTest}
            testStale={lastDraftTestStale}
            readinessMessage={publishReadinessMessage}
          />
        </div>
        <div className="border-border flex flex-wrap gap-2 border-t p-3">
          <Button variant="outline" size="sm" onClick={onSave} disabled={busy}>
            <IconDeviceFloppy className="size-4" />
            {pendingAction === "save" ? "Saving" : "Save Draft"}
          </Button>
          <Button size="sm" onClick={onAIRevise} disabled={busy}>
            <IconSparkles className="size-4" />
            {pendingAction === "ai-revise" ? "Drafting" : "Ask AI"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onRegenerate}
            disabled={busy}
          >
            <IconRotateClockwise className="size-4" />
            {pendingAction === "regenerate" ? "Scaffolding" : "Scaffold"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onValidate}
            disabled={busy}
          >
            <IconCheck className="size-4" />
            {pendingAction === "validate" ? "Validating" : "Validate"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onTest}
            disabled={!canTestDraft || busy}
            title={!canTestDraft ? testReadinessMessage : undefined}
          >
            <IconPlayerPlay className="size-4" />
            {pendingAction === "test" || pendingAction === "test-running"
              ? "Testing"
              : "Test Draft"}
          </Button>
          <Button
            size="sm"
            onClick={onPublish}
            disabled={!canPublish || busy}
            title={!canPublish ? publishReadinessMessage : undefined}
          >
            <IconRocket className="size-4" />
            {pendingAction === "publish" ? "Publishing" : "Publish"}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={onDiscard}
            disabled={busy}
          >
            <IconTrash className="size-4" />
            {pendingAction === "discard" ? "Discarding" : "Discard"}
          </Button>
        </div>
      </section>

      <section className="border-border bg-card/40 flex min-h-0 flex-col overflow-hidden rounded-lg border">
        <div className="border-border flex items-center justify-between gap-3 border-b px-4 py-3">
          <div className="flex items-center gap-2">
            <IconCode className="text-muted-foreground size-4" />
            <h2 className="text-sm font-medium">Workflow YAML</h2>
          </div>
          <StatusBadge status={session.status} />
        </div>
        <Textarea
          aria-label="Workflow YAML"
          value={draftYAML}
          onChange={(event) => onDraftYAMLChange(event.target.value)}
          spellCheck={false}
          className="min-h-0 flex-1 resize-none rounded-none border-0 p-4 font-mono text-xs shadow-none focus-visible:ring-0"
        />
      </section>
    </div>
  )
}

function DevelopmentHeader({
  session,
}: {
  session: WorkflowDevelopmentSession
}) {
  return (
    <div className="border-border border-b px-4 py-3">
      <div className="flex min-w-0 items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <h2 className="min-w-0 truncate text-sm font-medium">
              {session.target_workflow_ref}
            </h2>
            <Badge variant="outline" className="capitalize">
              {session.reason.replaceAll("_", " ")}
            </Badge>
            <Badge variant="outline">Only active draft</Badge>
          </div>
          <div className="text-muted-foreground mt-1 truncate font-mono text-xs">
            {session.id}
          </div>
        </div>
        <StatusBadge status={session.status} />
      </div>
    </div>
  )
}

function DevelopmentBusyBar({ label }: { label: string }) {
  return (
    <div
      className="border-border bg-muted/40 text-muted-foreground flex min-w-0 items-center gap-2 border-b px-4 py-2 text-xs"
      aria-live="polite"
    >
      <IconActivity className="size-4 shrink-0" />
      <span className="min-w-0 truncate">{label}</span>
    </div>
  )
}

function developmentBusyLabel(action: DevelopmentPendingAction | null) {
  switch (action) {
    case "start-ai":
    case "ai-revise":
      return "AI is drafting workflow YAML"
    case "start":
      return "Starting workflow development"
    case "save":
      return "Saving workflow draft"
    case "regenerate":
      return "Regenerating deterministic scaffold"
    case "validate":
      return "Validating workflow draft"
    case "test":
      return "Running draft workflow"
    case "test-running":
      return "Draft workflow test is running"
    case "publish":
      return "Publishing workflow"
    case "discard":
      return "Discarding workflow development"
    default:
      return null
  }
}

function workflowStartReadinessMessage(
  prompt: string,
  action: DevelopmentPendingAction | null,
) {
  const busyLabel = developmentBusyLabel(action)
  if (busyLabel) {
    return busyLabel
  }
  if (prompt.trim() === "") {
    return "Describe the workflow outcome before starting."
  }
  return "Ready to start. One workflow draft can be active at a time."
}

function workflowRepairPrompt(status?: string) {
  if (status === "pending_revalidation") {
    return "Review this workflow against the current PicoClaw runtime. Keep the behavior intact and update the YAML only where needed for current compatibility."
  }
  return "Repair this workflow for the current PicoClaw runtime. Preserve the intended behavior while fixing validation errors and compatibility issues."
}

function WorkflowCandidate({
  refName,
  title,
  status,
  issues,
  actionLabel,
  onAction,
  aiActionLabel,
  onAIAction,
  blocked,
}: {
  refName: string
  title?: string
  status?: string
  issues?: WorkflowValidationIssue[]
  actionLabel: string
  onAction: () => void
  aiActionLabel?: string
  onAIAction?: () => void
  blocked: boolean
}) {
  const issueSummary = issues?.[0] ? formatIssueSummary(issues[0]) : ""
  const description = issueSummary || title || "Workflow"
  const issueIsBlocking =
    issueSummary !== "" && (status === "invalid" || status === "blocked")
  return (
    <div className="border-border/70 flex min-w-0 items-center justify-between gap-3 rounded-md border px-3 py-2">
      <div className="min-w-0">
        <div className="truncate font-mono text-xs">{refName}</div>
        <div
          className={cn(
            "text-muted-foreground mt-0.5 truncate text-xs",
            issueIsBlocking && "text-destructive",
          )}
          title={description}
        >
          {description}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {status ? <ValidationStatusBadge status={status} /> : null}
        {aiActionLabel && onAIAction ? (
          <Button size="sm" onClick={onAIAction} disabled={blocked}>
            <IconSparkles className="size-4" />
            {aiActionLabel}
          </Button>
        ) : null}
        <Button
          variant="outline"
          size="sm"
          onClick={onAction}
          disabled={blocked}
        >
          <IconPencil className="size-4" />
          {actionLabel}
        </Button>
      </div>
    </div>
  )
}

function OperateSurface({
  query,
  workflows,
  compatibilityByRef,
  compatibility,
  selectedWorkflowRef,
  runInputValues,
  runSecretValues,
  runSecretsJSON,
  runSession,
  runDeliveryJSON,
  retrySecretsJSON,
  runs,
  allRuns,
  selectedRunID,
  selectedRun,
  events,
  graph,
  loadingRuns,
  loadingEvents,
  streamingEvents,
  loadingGraph,
  onQueryChange,
  onSelectWorkflow,
  onRunInputChange,
  onRunSecretChange,
  onRunSecretsJSONChange,
  onRunSessionChange,
  onRunDeliveryJSONChange,
  onRetrySecretsJSONChange,
  onSelectRun,
  onReload,
  onRunWorkflow,
  reloading,
  runningWorkflow,
  onCancel,
  onRetry,
  canceling,
  retrying,
  canRunWorkflow,
  runPayloadError,
  canCancel,
  canRetry,
  retryReadinessMessage,
}: {
  query: string
  workflows: WorkflowDefinition[]
  compatibilityByRef: Map<string, WorkflowValidationStamp>
  compatibility?: WorkflowCompatibilitySummary
  selectedWorkflowRef: string | null
  runInputValues: WorkflowRunInputValues
  runSecretValues: WorkflowRunSecretValues
  runSecretsJSON: string
  runSession: string
  runDeliveryJSON: string
  retrySecretsJSON: string
  runs: WorkflowRun[]
  allRuns: WorkflowRun[]
  selectedRunID: string | null
  selectedRun?: WorkflowRun
  events: Awaited<ReturnType<typeof getWorkflowRunEvents>>["events"]
  graph?: Awaited<ReturnType<typeof getWorkflowRunGraph>>
  loadingRuns: boolean
  loadingEvents: boolean
  streamingEvents: boolean
  loadingGraph: boolean
  onQueryChange: (value: string) => void
  onSelectWorkflow: (ref: string) => void
  onRunInputChange: (name: string, value: string) => void
  onRunSecretChange: (name: string, value: string) => void
  onRunSecretsJSONChange: (value: string) => void
  onRunSessionChange: (value: string) => void
  onRunDeliveryJSONChange: (value: string) => void
  onRetrySecretsJSONChange: (value: string) => void
  onSelectRun: (runID: string) => void
  onReload: () => void
  onRunWorkflow: () => void
  reloading: boolean
  runningWorkflow: boolean
  onCancel: () => void
  onRetry: () => void
  canceling: boolean
  retrying: boolean
  canRunWorkflow: boolean
  runPayloadError: string | null
  canCancel: boolean
  canRetry: boolean
  retryReadinessMessage: string
}) {
  const selectedWorkflow =
    workflows.find((workflow) => workflow.ref === selectedWorkflowRef) ?? null
  const selectedStamp =
    selectedWorkflowRef == null
      ? undefined
      : compatibilityByRef.get(selectedWorkflowRef)
  return (
    <div className="grid min-h-0 flex-1 gap-4 overflow-hidden lg:grid-cols-[minmax(320px,0.9fr)_minmax(0,1.3fr)]">
      <section className="border-border bg-card/40 flex min-h-0 flex-col overflow-hidden rounded-lg border">
        <div className="border-border flex items-center justify-between gap-3 border-b p-3">
          <Input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder="Filter runs"
            className="h-8"
          />
          <Button
            variant="outline"
            size="sm"
            onClick={onReload}
            disabled={reloading}
            title="Reload definitions"
          >
            <IconReload className="size-4" />
            Reload
          </Button>
        </div>
        <div className="grid min-h-0 flex-1 grid-rows-[auto_auto_minmax(0,1fr)]">
          <DefinitionStrip
            workflows={workflows}
            compatibilityByRef={compatibilityByRef}
            selectedWorkflowRef={selectedWorkflowRef}
            onSelectWorkflow={onSelectWorkflow}
          />
          <WorkflowRunPanel
            workflows={workflows}
            workflow={selectedWorkflow}
            stamp={selectedStamp}
            compatibility={compatibility}
            selectedWorkflowRef={selectedWorkflowRef}
            inputValues={runInputValues}
            secretValues={runSecretValues}
            secretsJSON={runSecretsJSON}
            session={runSession}
            deliveryJSON={runDeliveryJSON}
            onSelectWorkflow={onSelectWorkflow}
            onInputChange={onRunInputChange}
            onSecretChange={onRunSecretChange}
            onSecretsJSONChange={onRunSecretsJSONChange}
            onSessionChange={onRunSessionChange}
            onDeliveryJSONChange={onRunDeliveryJSONChange}
            onRun={onRunWorkflow}
            running={runningWorkflow}
            canRun={canRunWorkflow}
            payloadError={runPayloadError}
          />
          <RunList
            runs={runs}
            selectedRunID={selectedRunID}
            onSelect={onSelectRun}
            loading={loadingRuns}
            totalRuns={allRuns.length}
          />
        </div>
      </section>

      <section className="border-border bg-card/40 flex min-h-0 flex-col overflow-hidden rounded-lg border">
        <RunDetailHeader
          run={selectedRun}
          onCancel={onCancel}
          onRetry={onRetry}
          canceling={canceling}
          retrying={retrying}
          canCancel={canCancel}
          canRetry={canRetry}
          retryReadinessMessage={retryReadinessMessage}
          retrySecretsJSON={retrySecretsJSON}
          onRetrySecretsJSONChange={onRetrySecretsJSONChange}
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
              <RunGraphPanel graph={graph} loading={loadingGraph} />
              <ExecutionPanel run={selectedRun} />
              <ManagedExecutionPanel run={selectedRun} />
              <EventsPanel
                events={events}
                loading={loadingEvents}
                streaming={streamingEvents}
              />
            </div>
          )}
        </ScrollRegion>
      </section>
    </div>
  )
}

function DefinitionStrip({
  workflows,
  compatibilityByRef,
  selectedWorkflowRef,
  onSelectWorkflow,
}: {
  workflows: WorkflowDefinition[]
  compatibilityByRef: Map<string, WorkflowValidationStamp>
  selectedWorkflowRef: string | null
  onSelectWorkflow: (ref: string) => void
}) {
  return (
    <div className="border-border border-b p-3">
      <div className="mb-2 flex items-center justify-between">
        <h3 className="text-sm font-medium">Definitions</h3>
        <Badge variant="outline">{workflows.length}</Badge>
      </div>
      <ScrollRegion
        label="Workflow definitions"
        className="flex max-h-32 flex-col gap-1 overflow-auto rounded-md"
      >
        {workflows.length === 0 ? (
          <span className="text-muted-foreground text-sm">No definitions</span>
        ) : (
          workflows.map((workflow) => {
            const stamp = compatibilityByRef.get(workflow.ref)
            return (
              <button
                type="button"
                key={workflow.ref}
                onClick={() => onSelectWorkflow(workflow.ref)}
                className={cn(
                  "text-muted-foreground hover:bg-muted/60 flex min-w-0 items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-xs",
                  selectedWorkflowRef === workflow.ref &&
                    "bg-accent/70 text-accent-foreground",
                )}
              >
                <span className="min-w-0 truncate font-mono">
                  {workflow.ref}
                </span>
                <ValidationStatusBadge
                  status={
                    workflow.error ? "invalid" : (stamp?.status ?? "unknown")
                  }
                />
              </button>
            )
          })
        )}
      </ScrollRegion>
    </div>
  )
}

function WorkflowRunPanel({
  workflows,
  workflow,
  stamp,
  compatibility,
  selectedWorkflowRef,
  inputValues,
  secretValues,
  secretsJSON,
  session,
  deliveryJSON,
  onSelectWorkflow,
  onInputChange,
  onSecretChange,
  onSecretsJSONChange,
  onSessionChange,
  onDeliveryJSONChange,
  onRun,
  running,
  canRun,
  payloadError,
}: {
  workflows: WorkflowDefinition[]
  workflow: WorkflowDefinition | null
  stamp?: WorkflowValidationStamp
  compatibility?: WorkflowCompatibilitySummary
  selectedWorkflowRef: string | null
  inputValues: WorkflowRunInputValues
  secretValues: WorkflowRunSecretValues
  secretsJSON: string
  session: string
  deliveryJSON: string
  onSelectWorkflow: (ref: string) => void
  onInputChange: (name: string, value: string) => void
  onSecretChange: (name: string, value: string) => void
  onSecretsJSONChange: (value: string) => void
  onSessionChange: (value: string) => void
  onDeliveryJSONChange: (value: string) => void
  onRun: () => void
  running: boolean
  canRun: boolean
  payloadError: string | null
}) {
  const [open, setOpen] = useState(false)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const status = workflowRunStatus(workflow, stamp, compatibility)
  const readinessMessage =
    payloadError ?? workflowRunReadinessMessage(workflow, stamp, compatibility)
  const inputs = workflowRunInputEntries(workflow)
  const secrets = workflowRunSecretEntries(workflow)
  const runnable = canRun && !running
  const runNow = () => {
    if (!runnable) {
      return
    }
    setOpen(false)
    onRun()
  }
  return (
    <div className="border-border border-b p-3">
      <div className="flex min-w-0 items-center justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-sm font-medium">Run workflow</h3>
          <div
            id="workflow-run-selected-ref"
            className="text-muted-foreground mt-0.5 truncate font-mono text-xs"
          >
            {workflow?.ref ?? "No workflow selected"}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <ValidationStatusBadge status={status} />
          <Popover open={open} onOpenChange={setOpen}>
            <PopoverTrigger asChild>
              <Button
                size="sm"
                disabled={workflow == null}
                title={workflow == null ? readinessMessage : "Run workflow"}
              >
                <IconPlayerPlay className="size-4" />
                Run workflow
              </Button>
            </PopoverTrigger>
            <PopoverContent
              align="end"
              className="w-[min(420px,calc(100vw-2rem))] p-0"
            >
              <div className="border-border flex items-center justify-between gap-3 border-b px-3 py-2.5">
                <div className="min-w-0">
                  <h3 className="text-sm font-medium">Run workflow</h3>
                  <div className="text-muted-foreground mt-0.5 truncate font-mono text-xs">
                    {workflow?.ref ?? "No workflow selected"}
                  </div>
                </div>
                <ValidationStatusBadge status={status} />
              </div>
              <div className="grid gap-3 p-3">
                <div className="grid gap-1.5">
                  <Label htmlFor="workflow-run-ref">Workflow</Label>
                  <Select
                    value={selectedWorkflowRef ?? ""}
                    onValueChange={onSelectWorkflow}
                    disabled={workflows.length === 0 || running}
                  >
                    <SelectTrigger
                      id="workflow-run-ref"
                      className="w-full font-mono text-xs"
                    >
                      <SelectValue placeholder="Select workflow" />
                    </SelectTrigger>
                    <SelectContent>
                      {workflows.map((candidate) => (
                        <SelectItem key={candidate.ref} value={candidate.ref}>
                          {candidate.ref}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                {inputs.length > 0 ? (
                  <div className="grid gap-3">
                    {inputs.map(({ name, input }) => (
                      <WorkflowRunInputField
                        key={name}
                        name={name}
                        input={input}
                        value={inputValues[name] ?? ""}
                        disabled={running}
                        onChange={(value) => onInputChange(name, value)}
                      />
                    ))}
                  </div>
                ) : (
                  <div className="text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs">
                    No declared inputs
                  </div>
                )}
                {secrets.length > 0 ? (
                  <div className="grid gap-3">
                    {secrets.map(({ name, secret }) => (
                      <div key={name} className="grid gap-1.5">
                        <div className="flex items-center gap-2">
                          <Label
                            htmlFor={`workflow-run-secret-${fieldIDPart(name)}`}
                          >
                            {name}
                          </Label>
                          {secret.required ? (
                            <Badge variant="outline">Required</Badge>
                          ) : null}
                        </div>
                        <Input
                          id={`workflow-run-secret-${fieldIDPart(name)}`}
                          type="password"
                          value={secretValues[name] ?? ""}
                          onChange={(event) =>
                            onSecretChange(name, event.target.value)
                          }
                          disabled={running}
                          className="font-mono text-xs"
                        />
                      </div>
                    ))}
                  </div>
                ) : null}
                <Collapsible
                  open={advancedOpen}
                  onOpenChange={setAdvancedOpen}
                  className="grid gap-2"
                >
                  <CollapsibleTrigger asChild>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="justify-self-start px-2"
                    >
                      Advanced options
                    </Button>
                  </CollapsibleTrigger>
                  <CollapsibleContent className="grid gap-2">
                    <Textarea
                      id="workflow-run-secrets"
                      aria-label="Additional secrets JSON"
                      value={secretsJSON}
                      onChange={(event) =>
                        onSecretsJSONChange(event.target.value)
                      }
                      spellCheck={false}
                      placeholder="Additional secrets JSON"
                      className="min-h-16 resize-none font-mono text-xs"
                    />
                    <Input
                      id="workflow-run-session"
                      aria-label="Manual run session"
                      value={session}
                      onChange={(event) => onSessionChange(event.target.value)}
                      placeholder="Session"
                      className="font-mono text-xs"
                    />
                    <Textarea
                      id="workflow-run-delivery"
                      aria-label="Manual run delivery JSON"
                      value={deliveryJSON}
                      onChange={(event) =>
                        onDeliveryJSONChange(event.target.value)
                      }
                      spellCheck={false}
                      placeholder="Delivery JSON"
                      className="min-h-16 resize-none font-mono text-xs"
                    />
                  </CollapsibleContent>
                </Collapsible>
                {workflow?.error ? (
                  <div className="text-destructive text-xs">
                    {workflow.error}
                  </div>
                ) : null}
                <div
                  className={cn(
                    "text-xs",
                    canRun ? "text-muted-foreground" : "text-destructive",
                  )}
                >
                  {readinessMessage}
                </div>
                <Button
                  size="sm"
                  onClick={runNow}
                  disabled={!runnable}
                  className="justify-self-start"
                  title={!runnable ? readinessMessage : undefined}
                >
                  <IconPlayerPlay className="size-4" />
                  {running ? "Running" : "Run workflow"}
                </Button>
              </div>
            </PopoverContent>
          </Popover>
        </div>
      </div>
    </div>
  )
}

function WorkflowRunInputField({
  name,
  input,
  value,
  disabled,
  onChange,
}: {
  name: string
  input: WorkflowInputDefinition
  value: string
  disabled: boolean
  onChange: (value: string) => void
}) {
  const id = `workflow-run-input-${fieldIDPart(name)}`
  const type = workflowInputType(input)
  const required = input.required === true
  if (type === "boolean") {
    return (
      <div className="flex min-h-9 items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <Label htmlFor={id} className="min-w-0 truncate">
            {name}
          </Label>
          {required ? <Badge variant="outline">Required</Badge> : null}
        </div>
        <Switch
          id={id}
          checked={value === "true"}
          onCheckedChange={(checked) => onChange(checked ? "true" : "false")}
          disabled={disabled}
        />
      </div>
    )
  }
  if (type === "object" || type === "array") {
    return (
      <div className="grid gap-1.5">
        <div className="flex items-center gap-2">
          <Label htmlFor={id}>{name}</Label>
          {required ? <Badge variant="outline">Required</Badge> : null}
          <Badge variant="secondary">{type}</Badge>
        </div>
        <Textarea
          id={id}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          disabled={disabled}
          spellCheck={false}
          className="min-h-20 resize-none font-mono text-xs"
        />
      </div>
    )
  }
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center gap-2">
        <Label htmlFor={id}>{name}</Label>
        {required ? <Badge variant="outline">Required</Badge> : null}
        {type === "number" ? <Badge variant="secondary">number</Badge> : null}
      </div>
      <Input
        id={id}
        type={type === "number" ? "number" : "text"}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled}
        className={cn(type === "number" && "font-mono text-xs")}
      />
    </div>
  )
}

function RunList({
  runs,
  selectedRunID,
  onSelect,
  loading,
  totalRuns,
}: {
  runs: WorkflowRun[]
  selectedRunID: string | null
  onSelect: (runID: string) => void
  loading: boolean
  totalRuns: number
}) {
  if (loading) {
    return <EmptyPanel label="Loading runs" />
  }
  if (runs.length === 0) {
    return <EmptyPanel label="No runs" />
  }
  const running = runs.filter((run) => run.status === "running").length
  return (
    <ScrollRegion label="Workflow runs" className="min-h-0 overflow-auto p-2">
      <div className="mb-2 flex items-center gap-2 px-1">
        <Badge variant={running > 0 ? "default" : "outline"}>
          {running} running
        </Badge>
        <Badge variant="outline">{totalRuns} total</Badge>
      </div>
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
  retryReadinessMessage,
  retrySecretsJSON,
  onRetrySecretsJSONChange,
  onCancel,
  onRetry,
}: {
  run?: WorkflowRun
  canCancel: boolean
  canRetry: boolean
  canceling: boolean
  retrying: boolean
  retryReadinessMessage: string
  retrySecretsJSON: string
  onRetrySecretsJSONChange: (value: string) => void
  onCancel: () => void
  onRetry: () => void
}) {
  const showRetrySecrets =
    run != null &&
    terminalStatuses.has(run.status) &&
    !run.workflow_ref.startsWith("draft:")
  return (
    <div className="border-border min-h-14 border-b px-4 py-3">
      <div className="flex items-center justify-between gap-3">
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
            title={canRetry ? "Retry run" : retryReadinessMessage}
          >
            <IconRotateClockwise className="size-4" />
            Retry
          </Button>
        </div>
      </div>
      {showRetrySecrets ? (
        <div className="mt-3 grid gap-2">
          <label
            className="text-muted-foreground text-xs"
            htmlFor="workflow-retry-secrets"
          >
            Retry secrets JSON
          </label>
          <Textarea
            id="workflow-retry-secrets"
            aria-label="Retry secrets JSON"
            value={retrySecretsJSON}
            onChange={(event) => onRetrySecretsJSONChange(event.target.value)}
            spellCheck={false}
            className="min-h-16 resize-none font-mono text-xs"
          />
          <div
            className={cn(
              "text-xs",
              canRetry ? "text-muted-foreground" : "text-destructive",
            )}
          >
            {retryReadinessMessage}
          </div>
        </div>
      ) : null}
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
        <Meta label="Caller job" value={run.caller_job_id ?? "-"} mono />
        <Meta label="Retry of" value={run.retry_of_run_id ?? "-"} mono />
        <Meta label="Children" value={formatIDList(run.child_run_ids)} mono />
      </dl>
      {run.error || run.cancel_reason ? (
        <div className="bg-destructive/10 text-destructive mt-3 rounded-md px-3 py-2 text-sm">
          {run.cancel_reason || run.error}
        </div>
      ) : null}
      <JsonBlock label="Inputs" value={run.inputs} />
      <JsonBlock label="Outputs" value={run.outputs} />
      <JsonBlock label="Delivery" value={run.delivery} />
      <JsonBlock label="Event" value={run.event} />
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

function ManagedExecutionPanel({ run }: { run: WorkflowRun }) {
  const entries = managedExecutionEntries(run)
  if (entries.length === 0) {
    return null
  }
  return (
    <Panel title="Managed Execution">
      <div className="grid gap-3">
        {entries.map(({ id, managed }) => {
          const split = recordValue(managed.split)
          const calibration = recordValue(managed.calibration)
          const optimization = recordValue(managed.optimization)
          const model = recordValue(optimization.model)
          const effort = recordValue(optimization.effort)
          const cost = recordValue(optimization.cost)
          return (
            <div
              key={id}
              className="border-border/70 rounded-md border px-3 py-2"
            >
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="min-w-0 truncate font-mono text-xs">{id}</span>
                <Badge variant="outline">
                  {stringValue(managed.strategy) || "single_run"}
                </Badge>
              </div>
              <dl className="grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
                <Meta
                  label="Children"
                  value={stringValue(split.child_count) || "0"}
                />
                <Meta
                  label="Calibration"
                  value={stringValue(calibration.status) || "-"}
                />
                <Meta
                  label="Model"
                  value={
                    stringValue(model.changed) === "true" ? "changed" : "same"
                  }
                />
                <Meta
                  label="Effort"
                  value={
                    stringValue(effort.changed) === "true" ? "changed" : "same"
                  }
                />
                <Meta
                  label="Saved"
                  value={formatCostValue(cost.estimated_savings_usd)}
                />
                <Meta
                  label="Selected"
                  value={
                    stringValue(model.selected_counts) ||
                    stringValue(model.selected) ||
                    "-"
                  }
                />
              </dl>
            </div>
          )
        })}
      </div>
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
            outputs: job.outputs,
          }))}
        />
        <ExecutionList
          title="Steps"
          items={steps.map(([id, step]) => ({
            id,
            status: step.status,
            error: step.error,
            outputs: step.outputs,
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
  items: Array<{
    id: string
    status: string
    error?: string
    outputs?: Record<string, unknown>
  }>
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
              <ExecutionOutputs id={item.id} outputs={item.outputs} />
            </div>
          ))
        )}
      </ScrollRegion>
    </div>
  )
}

function ExecutionOutputs({
  id,
  outputs,
}: {
  id: string
  outputs?: Record<string, unknown>
}) {
  if (outputs == null || Object.keys(outputs).length === 0) {
    return null
  }
  return (
    <ScrollRegion
      label={`${id} outputs`}
      className="bg-muted/50 mt-2 max-h-36 overflow-auto rounded-md p-2 font-mono text-xs"
    >
      <pre className="m-0">{JSON.stringify(outputs, null, 2)}</pre>
    </ScrollRegion>
  )
}

function EventsPanel({
  events,
  loading,
  streaming,
}: {
  events: Awaited<ReturnType<typeof getWorkflowRunEvents>>["events"]
  loading: boolean
  streaming: boolean
}) {
  return (
    <Panel
      title="Events"
      titleExtra={
        streaming ? <Badge variant="secondary">Live</Badge> : undefined
      }
    >
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
              {event.job_id || event.step_id ? (
                <div className="text-muted-foreground mt-1 flex min-w-0 flex-wrap gap-x-3 gap-y-1 font-mono text-xs">
                  {event.job_id ? (
                    <span className="min-w-0 truncate">
                      job: {event.job_id}
                    </span>
                  ) : null}
                  {event.step_id ? (
                    <span className="min-w-0 truncate">
                      step: {event.step_id}
                    </span>
                  ) : null}
                </div>
              ) : null}
              <JsonBlock label="Payload" value={event.payload} />
            </div>
          ))}
        </ScrollRegion>
      )}
    </Panel>
  )
}

function ValidationPanel({
  validation,
}: {
  validation?: WorkflowDevelopmentSession["validation"]
}) {
  if (validation == null) {
    return (
      <Panel title="Validation">
        <EmptyPanel label="Not validated" compact />
      </Panel>
    )
  }
  const issues = [
    ...(validation.errors ?? []).map((issue) => ({ ...issue, level: "error" })),
    ...(validation.warnings ?? []).map((issue) => ({
      ...issue,
      level: "warning",
    })),
  ]
  return (
    <Panel title="Validation">
      <div className="mb-3 flex items-center gap-2">
        <ValidationStatusBadge
          status={validation.valid ? "valid" : "invalid"}
        />
        <span className="text-muted-foreground text-xs">
          {formatDate(validation.validated_at)}
        </span>
      </div>
      {issues.length === 0 ? (
        <EmptyPanel label="No issues" compact />
      ) : (
        <div className="grid gap-2">
          {issues.map((issue, index) => (
            <IssueRow
              key={`${issue.path ?? ""}-${issue.message}-${index}`}
              issue={issue}
            />
          ))}
        </div>
      )}
    </Panel>
  )
}

function DraftTestResultPanel({
  result,
  stale,
  onOpenRun,
  onFixWithAI,
  fixingWithAI,
}: {
  result: DraftTestSnapshot | null
  stale: boolean
  onOpenRun: (runID: string) => void
  onFixWithAI: () => void
  fixingWithAI: boolean
}) {
  if (result == null) {
    return <EmptyPanel label="No draft test" compact />
  }
  const canFixWithAI = canFixDraftTestWithAI(result, stale)
  return (
    <div className="border-border bg-muted/30 rounded-md border px-3 py-2">
      <div className="flex min-w-0 items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <StatusBadge status={stale ? "stale" : result.status} />
          <span className="text-muted-foreground truncate text-xs">
            {formatDate(result.testedAt)}
          </span>
        </div>
        {result.runID ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onOpenRun(result.runID ?? "")}
            title="Open run"
          >
            <IconExternalLink className="size-4" />
            Open Run
          </Button>
        ) : null}
        {canFixWithAI ? (
          <Button
            variant="outline"
            size="sm"
            onClick={onFixWithAI}
            disabled={fixingWithAI}
            title="Ask AI to fix this draft test failure"
          >
            <IconSparkles className="size-4" />
            {fixingWithAI ? "Fixing" : "Fix With AI"}
          </Button>
        ) : null}
      </div>
      {result.error ? (
        <div className="text-destructive mt-2 text-xs">{result.error}</div>
      ) : null}
      {result.runID ? (
        <div className="text-muted-foreground mt-1 truncate font-mono text-xs">
          {result.runID}
        </div>
      ) : null}
    </div>
  )
}

function PublishReadinessPanel({
  targetRef,
  yaml,
  validation,
  validationStale,
  currentValidationInvalid,
  testResult,
  testStale,
  readinessMessage,
}: {
  targetRef: string
  yaml: string
  validation?: WorkflowDevelopmentSession["validation"]
  validationStale: boolean
  currentValidationInvalid: boolean
  testResult: DraftTestSnapshot | null
  testStale: boolean
  readinessMessage: string
}) {
  const validationStatus = publishValidationStatus(
    validation,
    validationStale,
    currentValidationInvalid,
  )
  const testStatus = publishTestStatus(testResult, testStale)
  return (
    <Panel title="Publish readiness">
      <div className="grid gap-2">
        <ReadinessRow
          label="Target"
          status={targetRef.trim() === "" ? "missing" : "ready"}
        />
        <ReadinessRow
          label="YAML"
          status={yaml.trim() === "" ? "missing" : "ready"}
        />
        <ReadinessRow label="Validation" status={validationStatus} />
        <ReadinessRow label="Latest test" status={testStatus} />
        <div className="text-muted-foreground border-border/70 mt-1 border-t pt-2 text-xs">
          {readinessMessage}
        </div>
      </div>
    </Panel>
  )
}

function ReadinessRow({ label, status }: { label: string; status: string }) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-3 text-sm">
      <span className="text-muted-foreground min-w-0 truncate">{label}</span>
      <ValidationStatusBadge status={status} />
    </div>
  )
}

function publishValidationStatus(
  validation: WorkflowDevelopmentSession["validation"],
  validationStale: boolean,
  currentValidationInvalid: boolean,
) {
  if (currentValidationInvalid) {
    return "invalid"
  }
  if (validationStale) {
    return "stale"
  }
  if (validation?.valid === true) {
    return "valid"
  }
  if (validation?.valid === false) {
    return "invalid"
  }
  return "pending"
}

function publishTestStatus(result: DraftTestSnapshot | null, stale: boolean) {
  if (result == null) {
    return "not_run"
  }
  if (stale) {
    return "stale"
  }
  return result.status
}

function workflowPublishReadinessMessage({
  session,
  targetRef,
  yaml,
  currentValidationInvalid,
  testResult,
  testStale,
}: {
  session: WorkflowDevelopmentSession | null
  targetRef: string
  yaml: string
  currentValidationInvalid: boolean
  testResult: DraftTestSnapshot | null
  testStale: boolean
}) {
  if (session == null) {
    return "Start workflow development before publishing."
  }
  if (targetRef.trim() === "") {
    return "Set a target workflow ref before publishing."
  }
  if (yaml.trim() === "") {
    return "Add workflow YAML before publishing."
  }
  if (currentValidationInvalid) {
    return "Fix validation errors before publishing."
  }
  if (testResult == null) {
    return "Run a successful draft test before publishing."
  }
  if (testResult.status === "running") {
    return "Wait for the draft test to finish."
  }
  if (testStale) {
    return "Run the draft again after the latest edits."
  }
  if (testResult.status !== "succeeded") {
    return "Fix the failing draft test before publishing."
  }
  return "Ready to publish."
}

function workflowTestReadinessMessage({
  session,
  targetRef,
  yaml,
  payloadError,
  runningTest,
}: {
  session: WorkflowDevelopmentSession | null
  targetRef: string
  yaml: string
  payloadError: string | null
  runningTest: boolean
}) {
  if (session == null) {
    return "Start workflow development before testing."
  }
  if (targetRef.trim() === "") {
    return "Set a target workflow ref before testing."
  }
  if (yaml.trim() === "") {
    return "Add workflow YAML before testing."
  }
  if (payloadError != null) {
    return payloadError
  }
  if (runningTest) {
    return "Wait for the running draft test to finish."
  }
  return "Ready to test."
}

function isPublishTestReady(result: DraftTestSnapshot | null, stale: boolean) {
  return publishTestStatus(result, stale) === "succeeded"
}

function canFixDraftTestWithAI(result: DraftTestSnapshot, stale: boolean) {
  return (
    !stale &&
    result.status !== "running" &&
    result.status !== "succeeded" &&
    result.status !== "skipped"
  )
}

function workflowDraftTestRepairPrompt(
  prompt: string,
  result: DraftTestSnapshot | null,
  stale: boolean,
  run?: WorkflowRun,
  events?: WorkflowRunEvent[],
) {
  const base = prompt.trim()
  const lines = [
    base === "" ? "Fix the workflow draft so its draft test passes." : base,
  ]
  if (result != null && canFixDraftTestWithAI(result, stale)) {
    lines.push("")
    lines.push(
      "Last draft test failed. Update the workflow YAML so the next draft test passes.",
    )
    lines.push(`Test status: ${result.status}`)
    if (result.runID) {
      lines.push(`Run ID: ${result.runID}`)
    }
    if (result.error) {
      lines.push(`Error: ${result.error}`)
    }
    appendWorkflowPromptJSONBlock(
      lines,
      "Failed run context",
      workflowDraftTestRunContext(run, result),
    )
    appendWorkflowPromptJSONBlock(
      lines,
      "Recent failed run events",
      workflowDraftTestEventContext(events, result),
    )
  }
  return lines.join("\n")
}

const workflowRepairPromptBlockLimit = 4000

function workflowDraftTestRunContext(
  run: WorkflowRun | undefined,
  result: DraftTestSnapshot | null,
) {
  if (run == null || result?.runID == null || run.id !== result.runID) {
    return null
  }
  return {
    run_id: run.id,
    workflow_ref: run.workflow_ref,
    status: run.status,
    error: run.error,
    inputs: run.inputs,
    trigger_event: run.event,
    outputs: run.outputs,
    jobs: compactWorkflowExecutions(run.jobs),
    steps: compactWorkflowExecutions(run.steps),
  }
}

function workflowDraftTestEventContext(
  events: WorkflowRunEvent[] | undefined,
  result: DraftTestSnapshot | null,
) {
  if (result?.runID == null) {
    return []
  }
  return (events ?? [])
    .filter((event) => event.run_id === result.runID)
    .slice(-8)
    .map((event) => ({
      time: event.time,
      kind: event.kind,
      job_id: event.job_id,
      step_id: event.step_id,
      message: event.message,
      payload: event.payload,
    }))
}

function compactWorkflowExecutions(
  executions?: Record<
    string,
    {
      status: string
      error?: string
      outputs?: Record<string, unknown>
    }
  >,
) {
  if (executions == null) {
    return undefined
  }
  return Object.fromEntries(
    Object.entries(executions).map(([id, execution]) => [
      id,
      {
        status: execution.status,
        error: execution.error,
        outputs: execution.outputs,
      },
    ]),
  )
}

function appendWorkflowPromptJSONBlock(
  lines: string[],
  label: string,
  value: unknown,
) {
  const text = JSON.stringify(value, null, 2)
  if (!text || text === "{}" || text === "[]" || text === "null") {
    return
  }
  lines.push("")
  lines.push(`${label}:`)
  lines.push(
    text.length > workflowRepairPromptBlockLimit
      ? `${text.slice(0, workflowRepairPromptBlockLimit)}\n... truncated`
      : text,
  )
}

function IssueRow({
  issue,
}: {
  issue: WorkflowValidationIssue & { level: string }
}) {
  return (
    <div
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        issue.level === "error"
          ? "border-destructive/40 bg-destructive/10 text-destructive"
          : "border-border bg-muted/40 text-muted-foreground",
      )}
    >
      {issue.path ? <span className="font-mono">{issue.path}: </span> : null}
      {issue.message}
    </div>
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
  const destructive =
    status === "failed" ||
    status === "canceled" ||
    status === "invalid" ||
    status === "validation_failed"
  const variant = destructive
    ? "default"
    : status === "running" || status === "ready_to_publish"
      ? "default"
      : status === "succeeded" || status === "valid" || status === "ready"
        ? "secondary"
        : "outline"
  return (
    <Badge
      variant={variant}
      className={cn(
        "capitalize",
        destructive && "bg-destructive dark:text-background text-white",
      )}
    >
      {status.replaceAll("_", " ")}
    </Badge>
  )
}

function ValidationStatusBadge({ status }: { status: string }) {
  const destructive =
    status === "invalid" ||
    status === "failed" ||
    status === "validation_failed" ||
    status === "missing" ||
    status === "blocked"
  const variant = destructive
    ? "default"
    : status === "valid" ||
        status === "ready" ||
        status === "succeeded" ||
        status === "runnable" ||
        status === "needs_review"
      ? "secondary"
      : status === "pending_revalidation"
        ? "default"
        : "outline"
  return (
    <Badge
      variant={variant}
      className={cn(
        "capitalize",
        destructive && "bg-destructive dark:text-background text-white",
      )}
    >
      {status.replaceAll("_", " ")}
    </Badge>
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

function managedExecutionEntries(run: WorkflowRun) {
  const entries: Array<{ id: string; managed: Record<string, unknown> }> = []
  for (const [id, step] of Object.entries(run.steps ?? {})) {
    const outputs = step.outputs
    if (outputs == null) {
      continue
    }
    const managed = recordValue(outputs.managed)
    if (Object.keys(managed).length > 0) {
      entries.push({ id, managed })
    }
  }
  return entries
}

function recordValue(value: unknown): Record<string, unknown> {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return {}
  }
  return value as Record<string, unknown>
}

function stringValue(value: unknown) {
  if (value == null) {
    return ""
  }
  if (typeof value === "string") {
    return value
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value)
  }
  if (Array.isArray(value)) {
    return value
      .map((item) =>
        typeof item === "object" ? JSON.stringify(item) : String(item),
      )
      .join(", ")
  }
  return ""
}

function formatCostValue(value: unknown) {
  if (typeof value !== "number") {
    return "-"
  }
  return `$${value.toFixed(6)}`
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

function formatIDList(values?: string[]) {
  if (values == null || values.length === 0) {
    return "-"
  }
  return values.join(", ")
}

function workflowStampIssues(stamp?: WorkflowValidationStamp) {
  if (stamp == null) {
    return []
  }
  return [...(stamp.errors ?? []), ...(stamp.warnings ?? [])]
}

function mergeWorkflowEventLists(
  current: WorkflowRunEvent[],
  incoming: WorkflowRunEvent[],
) {
  const merged: WorkflowRunEvent[] = []
  const seen = new Set<string>()
  for (const event of [...current, ...incoming]) {
    const key = workflowEventKey(event)
    if (seen.has(key)) {
      continue
    }
    seen.add(key)
    merged.push(event)
  }
  return merged
}

function workflowEventKey(event: WorkflowRunEvent) {
  return JSON.stringify([
    event.time,
    event.kind,
    event.run_id,
    event.job_id ?? "",
    event.step_id ?? "",
    event.message ?? "",
    event.payload ?? null,
  ])
}

function formatIssueSummary(issue: WorkflowValidationIssue) {
  if (issue.path && issue.path.trim() !== "") {
    return `${issue.path}: ${issue.message}`
  }
  return issue.message
}

function firstMessage(messages: Array<string | null>) {
  return messages.find((message) => message != null) ?? null
}

function workflowRunInputEntries(workflow: WorkflowDefinition | null) {
  return Object.entries(workflow?.workflow_call?.inputs ?? {})
    .map(([name, input]) => ({ name, input }))
    .sort((left, right) => left.name.localeCompare(right.name))
}

function workflowRunSecretEntries(workflow: WorkflowDefinition | null) {
  return Object.entries(workflow?.workflow_call?.secrets ?? {})
    .map(([name, secret]) => ({ name, secret }))
    .sort((left, right) => left.name.localeCompare(right.name))
}

function workflowRunContractSignature(workflow: WorkflowDefinition | null) {
  return JSON.stringify({
    inputs: workflowRunInputEntries(workflow),
    secrets: workflowRunSecretEntries(workflow),
  })
}

function workflowRunInitialInputValues(workflow: WorkflowDefinition | null) {
  const values: WorkflowRunInputValues = {}
  for (const { name, input } of workflowRunInputEntries(workflow)) {
    values[name] = workflowInputInitialValue(input)
  }
  return values
}

function workflowRunInitialSecretValues(workflow: WorkflowDefinition | null) {
  const values: WorkflowRunSecretValues = {}
  for (const { name } of workflowRunSecretEntries(workflow)) {
    values[name] = ""
  }
  return values
}

function workflowInputInitialValue(input: WorkflowInputDefinition) {
  const type = workflowInputType(input)
  const defaultValue = input.default
  if (defaultValue == null) {
    return type === "boolean" && input.required ? "false" : ""
  }
  if (type === "object" || type === "array") {
    return JSON.stringify(defaultValue, null, 2)
  }
  return String(defaultValue)
}

function workflowInputType(input: WorkflowInputDefinition) {
  const type = input.type?.trim().toLowerCase()
  switch (type) {
    case "number":
    case "boolean":
    case "object":
    case "array":
      return type
    default:
      return "string"
  }
}

function workflowRunInputValidationMessage(
  workflow: WorkflowDefinition | null,
  values: WorkflowRunInputValues,
) {
  for (const { name, input } of workflowRunInputEntries(workflow)) {
    const type = workflowInputType(input)
    const value = values[name] ?? ""
    const trimmed = value.trim()
    if (input.required && trimmed === "" && type !== "boolean") {
      return `Input "${name}" is required.`
    }
    if (trimmed === "" && !input.required) {
      continue
    }
    if (type === "number" && Number.isNaN(Number(trimmed))) {
      return `Input "${name}" must be a number.`
    }
    if (type === "object" || type === "array") {
      try {
        const parsed = JSON.parse(trimmed) as unknown
        if (type === "array" && !Array.isArray(parsed)) {
          return `Input "${name}" must be a JSON array.`
        }
        if (
          type === "object" &&
          (parsed == null ||
            Array.isArray(parsed) ||
            typeof parsed !== "object")
        ) {
          return `Input "${name}" must be a JSON object.`
        }
      } catch (err) {
        return `Input "${name}" JSON is invalid: ${jsonSyntaxMessage(err)}`
      }
    }
  }
  return null
}

function workflowRunSecretValidationMessage(
  workflow: WorkflowDefinition | null,
  values: WorkflowRunSecretValues,
  secretsJSON: string,
) {
  const advancedSecrets = tryParseStringJSONObject(secretsJSON) ?? {}
  for (const { name, secret } of workflowRunSecretEntries(workflow)) {
    if (!secret.required) {
      continue
    }
    const value = values[name] ?? advancedSecrets[name] ?? ""
    if (value.trim() === "") {
      return `Secret "${name}" is required.`
    }
  }
  return null
}

function workflowRunInputsPayload(
  workflow: WorkflowDefinition | null,
  values: WorkflowRunInputValues,
) {
  const payload: Record<string, unknown> = {}
  for (const { name, input } of workflowRunInputEntries(workflow)) {
    const type = workflowInputType(input)
    const value = values[name] ?? ""
    const trimmed = value.trim()
    if (trimmed === "" && !input.required) {
      continue
    }
    payload[name] = workflowInputPayloadValue(type, value)
  }
  return payload
}

function workflowInputPayloadValue(type: string, value: string) {
  if (type === "boolean") {
    return value === "true"
  }
  if (type === "number") {
    return Number(value.trim())
  }
  if (type === "object" || type === "array") {
    return JSON.parse(value.trim()) as unknown
  }
  return value
}

function workflowRunSecretsPayload(
  workflow: WorkflowDefinition | null,
  values: WorkflowRunSecretValues,
  secretsJSON: string,
) {
  const payload = parseStringJSONObject(secretsJSON, "Secrets") ?? {}
  for (const { name } of workflowRunSecretEntries(workflow)) {
    const value = values[name]
    if (value != null && value.trim() !== "") {
      payload[name] = value
    }
  }
  return payload
}

function tryParseStringJSONObject(value: string) {
  try {
    return parseStringJSONObject(value, "Secrets")
  } catch {
    return {}
  }
}

function fieldIDPart(value: string) {
  return value.replace(/[^A-Za-z0-9_-]+/g, "-")
}

function jsonObjectValidationMessage(value: string, label: string) {
  const trimmed = value.trim()
  if (trimmed === "") {
    return null
  }
  try {
    const parsed = JSON.parse(trimmed) as unknown
    if (parsed == null || Array.isArray(parsed) || typeof parsed !== "object") {
      return `${label} must be a JSON object.`
    }
  } catch (err) {
    return `${label} JSON is invalid: ${jsonSyntaxMessage(err)}`
  }
  return null
}

function jsonStringObjectValidationMessage(value: string, label: string) {
  const objectError = jsonObjectValidationMessage(value, label)
  if (objectError != null) {
    return objectError
  }
  const trimmed = value.trim()
  if (trimmed === "") {
    return null
  }
  const parsed = JSON.parse(trimmed) as Record<string, unknown>
  for (const [key, item] of Object.entries(parsed)) {
    if (typeof item !== "string") {
      return `${label}.${key} must be a string.`
    }
  }
  return null
}

function jsonSyntaxMessage(err: unknown) {
  return err instanceof Error ? err.message : "invalid JSON"
}

function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : "Workflow request failed"
}

function isRunnableWorkflowStatus(
  status: string | undefined,
  compatibility?: WorkflowCompatibilitySummary,
) {
  if (status === "valid" || status === "needs_review") {
    return true
  }
  return compatibility == null && status == null
}

function workflowRunStatus(
  workflow: WorkflowDefinition | null,
  stamp?: WorkflowValidationStamp,
  compatibility?: WorkflowCompatibilitySummary,
) {
  if (workflow == null) {
    return "missing"
  }
  if (workflow.error) {
    return "invalid"
  }
  if (isRunnableWorkflowStatus(stamp?.status, compatibility)) {
    return "runnable"
  }
  return stamp?.status ?? "unknown"
}

function workflowRunReadinessMessage(
  workflow: WorkflowDefinition | null,
  stamp?: WorkflowValidationStamp,
  compatibility?: WorkflowCompatibilitySummary,
) {
  if (workflow == null) {
    return "Select a workflow before running."
  }
  if (workflow.error) {
    return "Repair this workflow before running it."
  }
  if (isRunnableWorkflowStatus(stamp?.status, compatibility)) {
    return "Ready to run."
  }
  switch (stamp?.status) {
    case "pending_revalidation":
      return "Revalidate this workflow before running it."
    case "invalid":
      return "Repair this workflow before running it."
    case "blocked":
      return "Resolve blocking workflow issues before running it."
    default:
      return compatibility == null
        ? "Workflow status is still loading."
        : "Revalidate this workflow before running it."
  }
}

function workflowRetryReadinessMessage(
  run: WorkflowRun | undefined,
  workflow: WorkflowDefinition | null,
  stamp?: WorkflowValidationStamp,
  compatibility?: WorkflowCompatibilitySummary,
) {
  if (run == null) {
    return "Select a workflow run before retrying."
  }
  if (run.workflow_ref.startsWith("draft:")) {
    return "Draft test runs cannot be retried."
  }
  if (!terminalStatuses.has(run.status)) {
    return "Wait for the workflow run to finish before retrying."
  }
  if (workflow == null) {
    return "The workflow definition for this run is no longer available."
  }
  if (workflow.error) {
    return "Repair this workflow before retrying the run."
  }
  if (isRunnableWorkflowStatus(stamp?.status, compatibility)) {
    return "Ready to retry."
  }
  switch (stamp?.status) {
    case "pending_revalidation":
      return "Revalidate this workflow before retrying the run."
    case "invalid":
      return "Repair this workflow before retrying the run."
    case "blocked":
      return "Resolve blocking workflow issues before retrying the run."
    default:
      return compatibility == null
        ? "Workflow status is still loading."
        : "Revalidate this workflow before retrying the run."
  }
}

function parseJSONObject(value: string, label: string) {
  const trimmed = value.trim()
  if (trimmed === "") {
    return undefined
  }
  const parsed = JSON.parse(trimmed) as unknown
  if (parsed == null || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error(`${label} must be a JSON object`)
  }
  return parsed as Record<string, unknown>
}

function parseStringJSONObject(value: string, label: string) {
  const parsed = parseJSONObject(value, label)
  if (parsed == null) {
    return undefined
  }
  for (const [key, item] of Object.entries(parsed)) {
    if (typeof item !== "string") {
      throw new Error(`${label}.${key} must be a string`)
    }
  }
  return parsed as Record<string, string>
}

function optionalString(value: string) {
  const trimmed = value.trim()
  return trimmed === "" ? undefined : trimmed
}

function parseDeliveryJSONObject(value: string, label: string) {
  return parseJSONObject(value, label) as WorkflowDeliveryPayload | undefined
}

function draftTestSnapshotFromSession(
  session: WorkflowDevelopmentSession | null,
): DraftTestSnapshot | null {
  if (session?.last_test == null) {
    return null
  }
  return {
    sessionID: session.id,
    draftKey: session.last_test.draft_key,
    runID: session.last_test.run_id,
    status: session.last_test.status,
    error: session.last_test.error,
    testedAt: session.last_test.tested_at,
  }
}

function draftEditorSnapshotFromSession(
  session: WorkflowDevelopmentSession,
): DraftEditorSnapshot {
  return {
    sessionID: session.id,
    prompt: session.prompt ?? "",
    targetRef: session.target_workflow_ref,
    yaml: session.yaml,
  }
}

function editorMatchesDraftSnapshot(
  editor: Omit<DraftEditorSnapshot, "sessionID">,
  snapshot: DraftEditorSnapshot,
) {
  return (
    editor.prompt === snapshot.prompt &&
    editor.targetRef === snapshot.targetRef &&
    normalizeWorkflowYAML(editor.yaml) === normalizeWorkflowYAML(snapshot.yaml)
  )
}

function draftEditorSnapshotsEqual(
  left: DraftEditorSnapshot | null,
  right: DraftEditorSnapshot,
) {
  return (
    left != null &&
    left.sessionID === right.sessionID &&
    left.prompt === right.prompt &&
    left.targetRef === right.targetRef &&
    normalizeWorkflowYAML(left.yaml) === normalizeWorkflowYAML(right.yaml)
  )
}

function normalizeWorkflowYAML(value: string) {
  const trimmed = value.trimEnd()
  return trimmed === "" ? "" : `${trimmed}\n`
}

function draftKey(targetRef: string, yaml: string) {
  return `${targetRef.trim()}\u0000${normalizeWorkflowYAML(yaml)}`
}

async function invalidateWorkflowQueries(
  queryClient: ReturnType<typeof useQueryClient>,
) {
  await queryClient.invalidateQueries({ queryKey: ["workflows"] })
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
