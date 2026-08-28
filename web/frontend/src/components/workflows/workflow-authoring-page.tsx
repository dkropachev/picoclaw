/* eslint-disable react-refresh/only-export-components -- Routed authoring and run-detail pages share established workflow form helpers during the hard cutover. */
import {
  IconActivity,
  IconAlertTriangle,
  IconCheck,
  IconCode,
  IconDeviceFloppy,
  IconExternalLink,
  IconGitBranch,
  IconPlayerPlay,
  IconRefresh,
  IconRocket,
  IconRotateClockwise,
  IconSparkles,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { toast } from "sonner"

import {
  type WorkflowCompatibilitySummary,
  type WorkflowDefinition,
  type WorkflowDeliveryPayload,
  type WorkflowDependencyCheckResponse,
  type WorkflowDevelopmentSession,
  type WorkflowDevelopmentTestReconciliation,
  type WorkflowDevelopmentTestResult,
  type WorkflowInputDefinition,
  type WorkflowRun,
  type WorkflowRunEvent,
  type WorkflowTemplateCatalogEntry,
  type WorkflowValidationIssue,
  type WorkflowValidationStamp,
  aiReviseWorkflowDevelopment,
  checkWorkflowDependencies,
  discardWorkflowDevelopment,
  executeWorkflowDevelopmentTrigger,
  getWorkflowCompatibility,
  getWorkflowDevelopment,
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  installWorkflowTemplate,
  listWorkflowRuns,
  listWorkflowTemplates,
  publishWorkflowDevelopment,
  revalidateWorkflows,
  reviseWorkflowDevelopment,
  startWorkflowDevelopment,
  validateWorkflowDevelopment,
  workflowRunEventsStreamURL,
} from "@/api/workflows"
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

import {
  type WorkflowBuilderSection,
  WorkflowBuilderTabs,
} from "./workflow-builder-tabs"
import { WorkflowDefinitionInspector } from "./workflow-definition-inspector"
import { WorkflowDraftTestReviewDialog } from "./workflow-draft-test-review-dialog"
import {
  type WorkflowEditorMode,
  WorkflowEditorTabs,
} from "./workflow-editor-tabs"
import {
  WorkflowJobEditor,
  type WorkflowJobEditorActivity,
} from "./workflow-job-editor"
import {
  type WorkflowDependencyCheckState,
  WorkflowDependencyReadinessPanel,
  WorkflowPublishReadinessPanel,
} from "./workflow-publish-readiness"
import { workflowDraftTestRepairPrompt } from "./workflow-repair-context"
import {
  isWorkflowRunID,
  navigableWorkflowRef,
} from "./workflow-route-identities"
import { trustedWorkflowRunOrigin } from "./workflow-run-origin"
import { WorkflowTemplateCatalog } from "./workflow-template-catalog"
import {
  WorkflowTriggerEditor,
  type WorkflowTriggerEditorActivity,
  type WorkflowTriggerInspectionState,
} from "./workflow-trigger-editor"
import {
  WorkflowTriggerSimulator,
  type WorkflowTriggerSimulatorState,
} from "./workflow-trigger-simulator"

const terminalStatuses = new Set(["succeeded", "failed", "canceled", "skipped"])
const activeStatuses = new Set(["running", "waiting"])
const workflowDefinitionInspectionQueryKey = [
  "workflows",
  "definition-inspections",
] as const
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

type DraftEditorMode = WorkflowEditorMode
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
  draftRevision?: string
  runID?: string
  eventID?: string
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
type WorkflowDevelopmentSessionConflict = {
  baseSession: WorkflowDevelopmentSession
  incomingSession: WorkflowDevelopmentSession | null
}
type WorkflowDependencyDraftSnapshot = {
  key: number
  sessionID: string
  targetRef: string
  yaml: string
}
type WorkflowRunInputValues = Record<string, string>
type WorkflowRunSecretValues = Record<string, string>
type WorkflowTriggerExecutionSubmission = Extract<
  WorkflowTriggerSimulatorState,
  { status: "ready" }
>

const initialEventTriggerInspection: WorkflowTriggerInspectionState = {
  yaml: "",
  status: "loading",
  eventTriggerPresent: false,
}

const initialTriggerEditorActivity: WorkflowTriggerEditorActivity = {
  dirty: false,
  applying: false,
  conflict: false,
}

const initialJobEditorActivity: WorkflowJobEditorActivity = {
  dirty: false,
  applying: false,
  conflict: false,
}

export function WorkflowAuthoringPage({
  showTemplates = true,
  onDevelopmentConflict,
  onOpenRun,
  onPublished,
}: {
  showTemplates?: boolean
  onDevelopmentConflict?: (session: WorkflowDevelopmentSession) => void
  onOpenRun?: (runID: string) => void
  onPublished?: (workflowID: string, workflowRef: string) => void
}) {
  const queryClient = useQueryClient()
  const [triggerEditorActivity, setTriggerEditorActivity] =
    useState<WorkflowTriggerEditorActivity>(initialTriggerEditorActivity)
  const [jobEditorActivity, setJobEditorActivity] =
    useState<WorkflowJobEditorActivity>(initialJobEditorActivity)
  const [retainedDevelopmentSession, setRetainedDevelopmentSession] =
    useState<WorkflowDevelopmentSession | null>(null)
  const [developmentSessionConflict, setDevelopmentSessionConflict] =
    useState<WorkflowDevelopmentSessionConflict | null>(null)
  const [triggerEditorResetKey, setTriggerEditorResetKey] = useState(0)
  const [jobEditorResetKey, setJobEditorResetKey] = useState(0)
  const triggerEditorBlockingMessage =
    workflowDevelopmentSessionConflictMessage(developmentSessionConflict) ??
    workflowTriggerEditorBlockingMessage(triggerEditorActivity) ??
    workflowJobEditorBlockingMessage(jobEditorActivity)
  const triggerEditorBlocked = triggerEditorBlockingMessage != null
  const [selectedRunID, setSelectedRunID] = useState<string | null>(null)
  const [startPrompt, setStartPrompt] = useState("")
  const [startTargetRef, setStartTargetRef] = useState("")
  const [draftPrompt, setDraftPrompt] = useState("")
  const [draftTargetRef, setDraftTargetRef] = useState("")
  const [draftYAML, setDraftYAML] = useState("")
  const [draftEditorMode, setDraftEditorMode] =
    useState<DraftEditorMode>("yaml")
  const [eventTriggerInspection, setEventTriggerInspection] =
    useState<WorkflowTriggerInspectionState>(initialEventTriggerInspection)
  const [lastDraftTest, setLastDraftTest] = useState<DraftTestSnapshot | null>(
    null,
  )
  const [appliedDraftSnapshot, setAppliedDraftSnapshot] =
    useState<DraftEditorSnapshot | null>(null)
  const [streamedRunID, setStreamedRunID] = useState<string | null>(null)
  const [streamedEvents, setStreamedEvents] = useState<WorkflowRunEvent[]>([])
  const [, setEventStreamActive] = useState(false)
  const [dependencyDraftSnapshot, setDependencyDraftSnapshot] =
    useState<WorkflowDependencyDraftSnapshot | null>(null)
  const dependencySnapshotCounter = useRef(0)

  const compatibilityQuery = useQuery({
    queryKey: ["workflows", "compatibility"],
    queryFn: getWorkflowCompatibility,
  })
  const developmentQuery = useQuery({
    queryKey: ["workflows", "development"],
    queryFn: getWorkflowDevelopment,
  })
  const templatesQuery = useQuery({
    queryKey: ["workflows", "templates"],
    queryFn: listWorkflowTemplates,
    enabled: showTemplates,
  })
  const runsQuery = useQuery({
    queryKey: ["workflows", "runs"],
    queryFn: ({ signal }) => listWorkflowRuns({}, signal),
    refetchInterval: 5000,
  })
  const runQuery = useQuery({
    queryKey: ["workflows", "runs", selectedRunID],
    queryFn: () => getWorkflowRun(selectedRunID ?? ""),
    enabled: selectedRunID != null,
    refetchInterval: (query) => {
      const run = query.state.data
      return run != null && !terminalStatuses.has(run.status) ? 3000 : false
    },
    retry: false,
  })
  const selectedRun = runQuery.data
  const eventsQuery = useQuery({
    queryKey: ["workflows", "runs", selectedRunID, "events"],
    queryFn: () => getWorkflowRunEvents(selectedRunID ?? ""),
    enabled: selectedRunID != null && runQuery.data != null,
    refetchInterval:
      selectedRunID == null || runQuery.data == null ? false : 3000,
  })
  const authoritativeSession = developmentQuery.data?.session
  const session =
    developmentSessionConflict?.baseSession ??
    retainedDevelopmentSession ??
    authoritativeSession ??
    null
  const dependencySessionID = session?.id
  const dependencyCandidateTargetRef = draftTargetRef.trim()
  const dependencyCandidateYAML = draftYAML
  useEffect(() => {
    if (
      dependencySessionID == null ||
      dependencyCandidateTargetRef === "" ||
      dependencyCandidateYAML.trim() === ""
    ) {
      setDependencyDraftSnapshot(null)
      return
    }
    const timer = setTimeout(() => {
      dependencySnapshotCounter.current += 1
      setDependencyDraftSnapshot({
        key: dependencySnapshotCounter.current,
        sessionID: dependencySessionID,
        targetRef: dependencyCandidateTargetRef,
        yaml: dependencyCandidateYAML,
      })
    }, 300)
    return () => clearTimeout(timer)
  }, [
    dependencyCandidateTargetRef,
    dependencyCandidateYAML,
    dependencySessionID,
  ])
  const dependencyQuery = useQuery({
    queryKey: [
      "workflows",
      "dependencies",
      "draft",
      dependencyDraftSnapshot?.sessionID,
      dependencyDraftSnapshot?.key,
    ],
    queryFn: ({ signal }) => {
      if (dependencyDraftSnapshot == null) {
        throw new Error("Workflow dependency readiness is unavailable.")
      }
      return checkWorkflowDependencies(
        {
          draft: {
            target_ref: dependencyDraftSnapshot.targetRef,
            yaml: dependencyDraftSnapshot.yaml,
          },
        },
        signal,
      )
    },
    enabled: dependencyDraftSnapshot != null,
    retry: false,
  })
  const runs = useMemo(() => runsQuery.data?.runs ?? [], [runsQuery.data?.runs])
  const compatibility = compatibilityQuery.data
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
    if (
      !activeStatuses.has(lastDraftTest?.status ?? "") ||
      !lastDraftTest?.runID
    ) {
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
        current == null ||
        !activeStatuses.has(current.status) ||
        current.runID !== lastDraftTest.runID
      ) {
        return current
      }
      return {
        ...current,
        status: draftRun.status,
        error:
          selectedRun?.id === draftRun.id
            ? selectedRun.error || selectedRun.cancel_reason
            : current.error,
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

  const loadAuthoritativeDevelopmentSession = useCallback(() => {
    if (developmentSessionConflict == null) {
      return
    }
    const nextSession = developmentSessionConflict.incomingSession
    setDevelopmentSessionConflict(null)
    setTriggerEditorActivity(initialTriggerEditorActivity)
    setJobEditorActivity(initialJobEditorActivity)
    setTriggerEditorResetKey((current) => current + 1)
    setJobEditorResetKey((current) => current + 1)
    setRetainedDevelopmentSession(nextSession)
    if (nextSession == null) {
      setDraftPrompt("")
      setDraftTargetRef("")
      setDraftYAML("")
      setAppliedDraftSnapshot(null)
      return
    }
    applySessionDraft(nextSession)
  }, [applySessionDraft, developmentSessionConflict])

  useEffect(() => {
    if (
      selectedRunID == null ||
      selectedRun?.id !== selectedRunID ||
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
  }, [queryClient, selectedRun?.id, selectedRunID])

  useEffect(() => {
    if (developmentQuery.data === undefined) {
      return
    }
    const nextSession = authoritativeSession ?? null
    if (developmentSessionConflict != null) {
      if (
        !workflowDevelopmentSessionsEqual(
          developmentSessionConflict.incomingSession,
          nextSession,
        )
      ) {
        setDevelopmentSessionConflict((current) =>
          current == null
            ? current
            : { ...current, incomingSession: nextSession },
        )
      }
      return
    }
    const localDraftChanged =
      retainedDevelopmentSession != null &&
      appliedDraftSnapshot?.sessionID === retainedDevelopmentSession.id &&
      !editorMatchesDraftSnapshot(
        { prompt: draftPrompt, targetRef: draftTargetRef, yaml: draftYAML },
        appliedDraftSnapshot,
      )
    if (
      !workflowDevelopmentSessionsEqual(
        retainedDevelopmentSession,
        nextSession,
      ) &&
      (triggerEditorBlocked || localDraftChanged) &&
      retainedDevelopmentSession != null
    ) {
      setDevelopmentSessionConflict({
        baseSession: retainedDevelopmentSession,
        incomingSession: nextSession,
      })
      return
    }
    if (nextSession == null) {
      setDraftPrompt("")
      setDraftTargetRef("")
      setDraftYAML("")
      setAppliedDraftSnapshot(null)
      setRetainedDevelopmentSession(null)
      return
    }
    if (localDraftChanged) {
      return
    }
    applySessionDraft(nextSession)
    setRetainedDevelopmentSession(nextSession)
  }, [
    appliedDraftSnapshot,
    applySessionDraft,
    authoritativeSession,
    developmentQuery.data,
    developmentSessionConflict,
    draftPrompt,
    draftTargetRef,
    draftYAML,
    retainedDevelopmentSession,
    triggerEditorBlocked,
  ])

  useEffect(() => {
    const snapshot = draftTestSnapshotFromSession(session)
    setLastDraftTest(snapshot)
    if (snapshot?.runID) setSelectedRunID(snapshot.runID)
  }, [session])

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
      session.yaml !== draftYAML)
  const currentValidationInvalid =
    session?.validation?.valid === false && !draftDirty
  const lastDraftTestStale =
    lastDraftTest != null &&
    (lastDraftTest.draftKey !== currentDraftKey ||
      session == null ||
      lastDraftTest.draftRevision == null ||
      lastDraftTest.draftRevision !== session.draft_revision)
  const draftTestRunning = activeStatuses.has(lastDraftTest?.status ?? "")
  const publishTestReady = isPublishTestReady(lastDraftTest, lastDraftTestStale)
  const publishValidationState = publishValidationStatus(
    session?.validation,
    draftDirty,
    currentValidationInvalid,
  )
  const publishTestState = publishTestStatus(lastDraftTest, lastDraftTestStale)
  const publishSessionSnapshotReady =
    session?.last_test?.status === "succeeded" &&
    session.last_test.draft_revision != null &&
    session.last_test.draft_revision === session.draft_revision
  const dependencySnapshotCurrent =
    session != null &&
    dependencyDraftSnapshot != null &&
    dependencyDraftSnapshot.sessionID === session.id &&
    dependencyDraftSnapshot.targetRef === dependencyCandidateTargetRef &&
    dependencyDraftSnapshot.yaml === dependencyCandidateYAML
  const dependencyCheckState: WorkflowDependencyCheckState =
    session == null ||
    dependencyCandidateTargetRef === "" ||
    dependencyCandidateYAML.trim() === ""
      ? "idle"
      : !dependencySnapshotCurrent
        ? "stale"
        : dependencyQuery.isFetching
          ? "loading"
          : dependencyQuery.isError
            ? "error"
            : dependencyQuery.data == null
              ? "loading"
              : "current"
  const currentDependencyReport =
    dependencyCheckState === "current" ? dependencyQuery.data : undefined
  const dependencyReady = currentDependencyReport?.ready === true
  const triggerInspectionCurrent =
    eventTriggerInspection.yaml === draftYAML &&
    eventTriggerInspection.status === "ready" &&
    eventTriggerInspection.inspection != null
  const triggerSimulationReadinessError =
    eventTriggerInspection.yaml !== draftYAML ||
    eventTriggerInspection.status === "loading"
      ? "Wait for the current YAML to be inspected before testing."
      : eventTriggerInspection.status === "error"
        ? `Workflow trigger inspection failed: ${eventTriggerInspection.reason ?? "unknown error"}`
        : !triggerInspectionCurrent
          ? "Wait for the current YAML to be inspected before testing."
          : null
  const testReadinessMessage = workflowTestReadinessMessage({
    session,
    targetRef: draftTargetRef,
    yaml: draftYAML,
    payloadError: triggerSimulationReadinessError,
    runningTest: draftTestRunning,
  })
  const publishReadinessMessage = workflowPublishReadinessMessage({
    session,
    targetRef: draftTargetRef,
    yaml: draftYAML,
    validationStatus: publishValidationState,
    testResult: lastDraftTest,
    testStale: lastDraftTestStale,
    sessionSnapshotReady: publishSessionSnapshotReady,
    dependencyState: dependencyCheckState,
    dependencyReport: currentDependencyReport,
  })
  const handleDevelopmentMutationError = useCallback(
    (err: unknown) => {
      toast.error(errorMessage(err))
      if (
        err != null &&
        typeof err === "object" &&
        "status" in err &&
        err.status === 409
      ) {
        void queryClient.invalidateQueries({
          queryKey: ["workflows", "development"],
        })
      }
    },
    [queryClient],
  )
  const startMutation = useMutation({
    mutationFn: startWorkflowDevelopment,
    onSuccess: ({ session: nextSession, conflict }) => {
      if (conflict) {
        onDevelopmentConflict?.(nextSession)
        toast.warning("Another workflow draft is already active.")
        return
      }
      toast.success("Workflow development started")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: handleDevelopmentMutationError,
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
        ...workflowDevelopmentDraftFence(started.session),
        prompt: startPrompt,
        target_ref: startTargetRef || started.session.target_workflow_ref,
      })
    },
    onSuccess: ({ session: nextSession, conflict }) => {
      if (conflict) {
        onDevelopmentConflict?.(nextSession)
        toast.warning("Another workflow draft is already active.")
        return
      }
      toast.success(
        nextSession.validation?.valid
          ? "AI workflow draft ready"
          : "AI draft needs fixes",
      )
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
        ...workflowDevelopmentDraftFence(session),
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
      }),
    onSuccess: ({ session: nextSession }) => {
      toast.success("Workflow draft saved")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: handleDevelopmentMutationError,
  })

  const regenerateMutation = useMutation({
    mutationFn: () =>
      reviseWorkflowDevelopment({
        ...workflowDevelopmentDraftFence(session),
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        regenerate: true,
      }),
    onSuccess: ({ session: nextSession }) => {
      toast.success("Workflow draft regenerated")
      applySessionDraft(nextSession)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: handleDevelopmentMutationError,
  })

  const aiReviseMutation = useMutation({
    mutationFn: (promptOverride?: string) =>
      aiReviseWorkflowDevelopment({
        ...workflowDevelopmentDraftFence(session),
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
    onError: handleDevelopmentMutationError,
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
      const repairResult =
        lastDraftTest == null
          ? null
          : {
              ...lastDraftTest,
              eventID:
                typeof repairRun?.event?.id === "string"
                  ? repairRun.event.id
                  : lastDraftTest.eventID,
            }
      return aiReviseWorkflowDevelopment({
        ...workflowDevelopmentDraftFence(session),
        prompt: workflowDraftTestRepairPrompt(
          draftPrompt,
          repairResult,
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
    onError: handleDevelopmentMutationError,
  })

  const validateMutation = useMutation({
    mutationFn: async () => {
      const revised = await reviseWorkflowDevelopment({
        ...workflowDevelopmentDraftFence(session),
        prompt: draftPrompt,
        target_ref: draftTargetRef,
        yaml: draftYAML,
      })
      return validateWorkflowDevelopment(
        workflowDevelopmentDraftFence(revised.session),
      )
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
    onError: handleDevelopmentMutationError,
  })

  const triggerExecutionFenceRef = useRef({
    sessionID: session?.id,
    sessionRevision: session?.session_revision,
    draftRevision: session?.draft_revision,
    prompt: draftPrompt,
    targetRef: draftTargetRef,
    yaml: draftYAML,
  })
  useEffect(() => {
    triggerExecutionFenceRef.current = {
      sessionID: session?.id,
      sessionRevision: session?.session_revision,
      draftRevision: session?.draft_revision,
      prompt: draftPrompt,
      targetRef: draftTargetRef,
      yaml: draftYAML,
    }
  }, [
    draftPrompt,
    draftTargetRef,
    draftYAML,
    session?.draft_revision,
    session?.id,
    session?.session_revision,
  ])
  const testDraftMutation = useMutation<
    WorkflowDevelopmentTestResult,
    Error,
    WorkflowTriggerExecutionSubmission
  >({
    mutationFn: (submission) => {
      const token = submission.response.review_token
      if (!submission.response.simulation.executable || token == null) {
        throw new Error(
          "The exact trigger scenario does not have an executable server review.",
        )
      }
      return executeWorkflowDevelopmentTrigger(submission.request, token)
    },
    onSuccess: (
      { session: nextSession, result, reconciliation },
      submission,
    ) => {
      const fence = triggerExecutionFenceRef.current
      const request = submission.request
      if (
        fence.sessionID !== request.session_id ||
        fence.sessionRevision !== request.expected_session_revision ||
        fence.draftRevision !== request.expected_draft_revision ||
        fence.prompt !== request.prompt ||
        fence.targetRef !== request.target_ref ||
        fence.yaml !== request.yaml
      ) {
        toast.warning(
          "The draft changed while execution was starting. The stale response was ignored.",
        )
        return
      }
      if (nextSession != null) {
        applySessionDraft(nextSession)
      }
      setLastDraftTest({
        sessionID: nextSession?.id ?? request.session_id,
        draftKey: draftKey(
          nextSession?.target_workflow_ref ?? request.target_ref,
          nextSession?.yaml ?? request.yaml,
        ),
        draftRevision:
          nextSession?.last_test?.draft_revision ??
          nextSession?.draft_revision ??
          request.expected_draft_revision,
        runID: result?.run_id,
        eventID:
          request.trigger.type === "event"
            ? (request.scenario as { event_id: string }).event_id
            : undefined,
        status: result?.status ?? "validation_failed",
        error: result?.error,
        testedAt: new Date().toISOString(),
      })
      if (result?.run_id) {
        setSelectedRunID(result.run_id)
        if (result.error) {
          toast.error(`Draft test ${result.status}: ${result.error}`)
        } else if (reconciliation != null) {
          toast.warning(reconciliation.message)
        } else {
          toast.success(`Draft test ${result.status}`)
        }
        void invalidateRunQueries(queryClient, result.run_id)
      } else {
        toast.error("Draft test did not create a run")
      }
      if (
        nextSession == null &&
        reconciliation?.reason === "draft_test_response_truncated"
      ) {
        void queryClient.refetchQueries({
          queryKey: ["workflows", "development"],
          exact: true,
        })
      }
      void invalidateWorkflowQueries(queryClient)
    },
    onError: (err, submission) => {
      const fence = triggerExecutionFenceRef.current
      const request = submission.request
      if (
        fence.sessionID === request.session_id &&
        fence.sessionRevision === request.expected_session_revision &&
        fence.draftRevision === request.expected_draft_revision &&
        fence.prompt === request.prompt &&
        fence.targetRef === request.target_ref &&
        fence.yaml === request.yaml
      ) {
        toast.error(errorMessage(err))
      }
    },
  })

  const publishMutation = useMutation({
    mutationFn: async () => {
      const publishSession = session
      const checkedSnapshot = dependencyDraftSnapshot
      const checkedReport = currentDependencyReport
      if (
        publishSession == null ||
        dependencyCheckState !== "current" ||
        checkedSnapshot == null ||
        checkedReport?.ready !== true ||
        checkedReport.revision === ""
      ) {
        throw new Error(
          "Wait for a current successful dependency check before publishing.",
        )
      }
      const exactTest = publishSession.last_test
      if (
        publishSession.id !== checkedSnapshot.sessionID ||
        publishSession.target_workflow_ref !== checkedReport.root_ref ||
        publishSession.yaml !== checkedSnapshot.yaml ||
        publishSession.validation?.valid !== true ||
        exactTest?.status !== "succeeded" ||
        exactTest.draft_revision == null ||
        exactTest.draft_revision !== publishSession.draft_revision
      ) {
        throw new Error(
          "The draft changed. Validate it, run a successful test, and wait for a fresh dependency check.",
        )
      }
      if (
        publishSession.session_revision === "" ||
        publishSession.draft_revision === "" ||
        publishSession.base_target_revision === ""
      ) {
        throw new Error("Reload the workflow draft before publishing.")
      }
      return publishWorkflowDevelopment({
        session_id: publishSession.id,
        expected_session_revision: publishSession.session_revision,
        expected_draft_revision: publishSession.draft_revision,
        expected_base_target_revision: publishSession.base_target_revision,
        expected_dependency_revision: checkedReport.revision,
      })
    },
    onSuccess: (result) => {
      toast.success(`Published ${result.workflow_ref}`)
      onPublished?.(result.workflow_id, result.workflow_ref)
      setLastDraftTest(null)
      void invalidateWorkflowQueries(queryClient)
      void invalidateWorkflowDefinitionInspections(queryClient)
    },
    onError: (err) => {
      toast.error(errorMessage(err))
      void invalidateWorkflowQueries(queryClient)
    },
  })

  const discardMutation = useMutation({
    mutationFn: () => {
      if (session == null) throw new Error("No workflow draft is active.")
      return discardWorkflowDevelopment({
        session_id: session.id,
        expected_session_revision: session.session_revision,
      })
    },
    onSuccess: () => {
      toast.success("Workflow development discarded")
      setLastDraftTest(null)
      void invalidateWorkflowQueries(queryClient)
    },
    onError: handleDevelopmentMutationError,
  })

  const installTemplateMutation = useMutation({
    mutationFn: ({ name, overwrite }: { name: string; overwrite: boolean }) =>
      installWorkflowTemplate(name, overwrite),
    onSuccess: ({ result, templates }) => {
      queryClient.setQueryData(["workflows", "templates"], { templates })
      toast.success(
        result.overwritten
          ? `Restored ${result.ref}`
          : result.installed
            ? `Installed ${result.ref}`
            : `${result.ref} is already installed`,
      )
      void queryClient.invalidateQueries({
        queryKey: ["workflows"],
        exact: true,
      })
      void queryClient.invalidateQueries({
        queryKey: ["workflows", "templates"],
      })
      void queryClient.invalidateQueries({
        queryKey: ["workflows", "dependencies"],
      })
      void invalidateWorkflowDefinitionInspections(queryClient)
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
    !startWithAIMutation.isPending
  const acceptedTestRequest = testDraftMutation.variables?.request
  const acceptedTestReconciliation =
    testDraftMutation.data != null &&
    acceptedTestRequest != null &&
    session != null &&
    acceptedTestRequest.session_id === session.id &&
    acceptedTestRequest.expected_session_revision ===
      session.session_revision &&
    acceptedTestRequest.expected_draft_revision === session.draft_revision &&
    acceptedTestRequest.prompt === draftPrompt &&
    acceptedTestRequest.target_ref === draftTargetRef &&
    acceptedTestRequest.yaml === draftYAML
      ? testDraftMutation.data.reconciliation
      : undefined
  const currentDevelopmentReconciliation =
    developmentQuery.data?.reconciliation ?? acceptedTestReconciliation
  const canTestDraft =
    session != null &&
    draftTargetRef.trim() !== "" &&
    draftYAML.trim() !== "" &&
    triggerSimulationReadinessError == null &&
    !draftTestRunning &&
    !testDraftMutation.isPending
  const canPublish =
    session != null &&
    !publishMutation.isPending &&
    !draftTestRunning &&
    draftTargetRef.trim() !== "" &&
    draftYAML.trim() !== "" &&
    publishValidationState === "valid" &&
    publishTestReady &&
    publishSessionSnapshotReady &&
    dependencyReady
  const developmentPendingAction: DevelopmentPendingAction | null =
    startWithAIMutation.isPending
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
    saveMutation.isPending ||
    aiReviseMutation.isPending ||
    fixDraftTestMutation.isPending ||
    regenerateMutation.isPending ||
    validateMutation.isPending ||
    testDraftMutation.isPending ||
    draftTestRunning ||
    publishMutation.isPending ||
    discardMutation.isPending ||
    triggerEditorActivity.applying

  return (
    <div className="flex h-full flex-col">
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-hidden p-4 sm:p-6">
        <CompatibilityBanner
          compatibility={compatibility}
          invalidWorkflows={invalidWorkflows}
          onRevalidate={() => revalidateMutation.mutate()}
          revalidating={revalidateMutation.isPending}
        />
        <DevelopSurface
          session={session}
          reconciliation={currentDevelopmentReconciliation}
          templates={templatesQuery.data?.templates ?? []}
          templatesLoading={templatesQuery.isLoading}
          templatesUnavailable={templatesQuery.isError}
          templatesUnavailableMessage={
            templatesQuery.isError
              ? errorMessage(templatesQuery.error)
              : undefined
          }
          showTemplates={showTemplates}
          installingTemplateName={
            installTemplateMutation.isPending
              ? installTemplateMutation.variables?.name
              : undefined
          }
          startPrompt={startPrompt}
          startTargetRef={startTargetRef}
          draftPrompt={draftPrompt}
          draftTargetRef={draftTargetRef}
          draftYAML={draftYAML}
          draftEditorMode={draftEditorMode}
          triggerEditorBlockingMessage={triggerEditorBlockingMessage}
          developmentSessionConflict={developmentSessionConflict != null}
          triggerEditorResetKey={triggerEditorResetKey}
          jobEditorResetKey={jobEditorResetKey}
          eventTriggerInspection={eventTriggerInspection}
          onStartPromptChange={setStartPrompt}
          onStartTargetRefChange={setStartTargetRef}
          onDraftPromptChange={setDraftPrompt}
          onDraftTargetRefChange={setDraftTargetRef}
          onDraftYAMLChange={setDraftYAML}
          onDraftEditorModeChange={(nextMode) => {
            if (nextMode === "yaml" && triggerEditorBlockingMessage != null) {
              toast.warning(triggerEditorBlockingMessage)
              return
            }
            setDraftEditorMode(nextMode)
          }}
          onEventTriggerInspectionChange={setEventTriggerInspection}
          onTriggerEditorActivityChange={setTriggerEditorActivity}
          onJobEditorActivityChange={setJobEditorActivity}
          onLoadAuthoritativeDevelopmentSession={
            loadAuthoritativeDevelopmentSession
          }
          onStartWithAI={() => startWithAIMutation.mutate()}
          onStartScaffold={startScaffold}
          onInstallTemplate={(name, overwrite) =>
            installTemplateMutation.mutate({ name, overwrite })
          }
          onSave={() => saveMutation.mutate()}
          onAIRevise={() => aiReviseMutation.mutate(undefined)}
          onFixTestWithAI={() => fixDraftTestMutation.mutate()}
          onRegenerate={() => regenerateMutation.mutate()}
          onValidate={() => validateMutation.mutate()}
          onTest={(submission) => testDraftMutation.mutate(submission)}
          onPublish={() => publishMutation.mutate()}
          onDiscard={() => discardMutation.mutate()}
          onOpenTestRun={(runID) => {
            if (triggerEditorBlockingMessage != null) {
              toast.warning(triggerEditorBlockingMessage)
              return
            }
            onOpenRun?.(runID)
          }}
          canStartNew={canStartNew}
          canTestDraft={canTestDraft}
          canPublish={canPublish}
          testReadinessMessage={testReadinessMessage}
          publishReadinessMessage={publishReadinessMessage}
          publishValidationStatus={publishValidationState}
          publishTestStatus={publishTestState}
          dependencyState={dependencyCheckState}
          dependencyReport={currentDependencyReport}
          lastDraftTest={lastDraftTest}
          lastDraftTestStale={lastDraftTestStale}
          pendingAction={developmentPendingAction}
          busy={developmentBusy}
        />
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
  reconciliation,
  templates,
  templatesLoading,
  templatesUnavailable,
  templatesUnavailableMessage,
  showTemplates,
  installingTemplateName,
  startPrompt,
  startTargetRef,
  draftPrompt,
  draftTargetRef,
  draftYAML,
  draftEditorMode,
  triggerEditorBlockingMessage,
  developmentSessionConflict,
  triggerEditorResetKey,
  jobEditorResetKey,
  eventTriggerInspection,
  onStartPromptChange,
  onStartTargetRefChange,
  onDraftPromptChange,
  onDraftTargetRefChange,
  onDraftYAMLChange,
  onDraftEditorModeChange,
  onEventTriggerInspectionChange,
  onTriggerEditorActivityChange,
  onJobEditorActivityChange,
  onLoadAuthoritativeDevelopmentSession,
  onStartWithAI,
  onStartScaffold,
  onInstallTemplate,
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
  publishValidationStatus,
  publishTestStatus,
  dependencyState,
  dependencyReport,
  lastDraftTest,
  lastDraftTestStale,
  pendingAction,
  busy,
}: {
  session: WorkflowDevelopmentSession | null
  reconciliation?: WorkflowDevelopmentTestReconciliation
  templates: WorkflowTemplateCatalogEntry[]
  templatesLoading: boolean
  templatesUnavailable: boolean
  templatesUnavailableMessage?: string
  showTemplates: boolean
  installingTemplateName?: string
  startPrompt: string
  startTargetRef: string
  draftPrompt: string
  draftTargetRef: string
  draftYAML: string
  draftEditorMode: DraftEditorMode
  triggerEditorBlockingMessage: string | null
  developmentSessionConflict: boolean
  triggerEditorResetKey: number
  jobEditorResetKey: number
  eventTriggerInspection: WorkflowTriggerInspectionState
  onStartPromptChange: (value: string) => void
  onStartTargetRefChange: (value: string) => void
  onDraftPromptChange: (value: string) => void
  onDraftTargetRefChange: (value: string) => void
  onDraftYAMLChange: (value: string) => void
  onDraftEditorModeChange: (value: DraftEditorMode) => void
  onEventTriggerInspectionChange: (
    value: WorkflowTriggerInspectionState,
  ) => void
  onTriggerEditorActivityChange: (value: WorkflowTriggerEditorActivity) => void
  onJobEditorActivityChange: (value: WorkflowJobEditorActivity) => void
  onLoadAuthoritativeDevelopmentSession: () => void
  onStartWithAI: () => void
  onStartScaffold: () => void
  onInstallTemplate: (name: string, overwrite: boolean) => void
  onSave: () => void
  onAIRevise: () => void
  onFixTestWithAI: () => void
  onRegenerate: () => void
  onValidate: () => void
  onTest: (submission: WorkflowTriggerExecutionSubmission) => void
  onPublish: () => void
  onDiscard: () => void
  onOpenTestRun: (runID: string) => void
  canStartNew: boolean
  canTestDraft: boolean
  canPublish: boolean
  testReadinessMessage: string
  publishReadinessMessage: string
  publishValidationStatus: string
  publishTestStatus: string
  dependencyState: WorkflowDependencyCheckState
  dependencyReport?: WorkflowDependencyCheckResponse
  lastDraftTest: DraftTestSnapshot | null
  lastDraftTestStale: boolean
  pendingAction: DevelopmentPendingAction | null
  busy: boolean
}) {
  const busyLabel = developmentBusyLabel(pendingAction)
  const triggerEditorBlocked = triggerEditorBlockingMessage != null
  const draftActionDisabled = busy || triggerEditorBlocked
  const [builderSection, setBuilderSection] =
    useState<WorkflowBuilderSection>("triggers")
  const [testReviewOpen, setTestReviewOpen] = useState(false)
  const [triggerSimulation, setTriggerSimulation] =
    useState<WorkflowTriggerSimulatorState>({
      status: "idle",
      message: "Choose a trigger scenario to simulate.",
    })
  const simulationBase = {
    sessionID: session?.id,
    sessionRevision: session?.session_revision,
    draftRevision: session?.draft_revision,
    prompt: draftPrompt,
    targetRef: draftTargetRef,
    yaml: draftYAML,
  }
  const simulationBaseIdentity = JSON.stringify(simulationBase)
  const invalidateTriggerSimulation = useCallback(() => {
    setTestReviewOpen(false)
    setTriggerSimulation({
      status: "idle",
      message: "The draft or trigger scenario changed. Simulate it again.",
    })
  }, [])
  useEffect(() => {
    invalidateTriggerSimulation()
  }, [invalidateTriggerSimulation, simulationBaseIdentity])
  const handleTriggerSimulationChange = useCallback(
    (next: WorkflowTriggerSimulatorState) => {
      if (next.status === "ready") {
        const request = next.request
        if (
          session?.id !== request.session_id ||
          session.session_revision !== request.expected_session_revision ||
          session.draft_revision !== request.expected_draft_revision ||
          draftPrompt !== request.prompt ||
          draftTargetRef !== request.target_ref ||
          draftYAML !== request.yaml
        ) {
          return
        }
      }
      setTestReviewOpen(false)
      setTriggerSimulation(next)
    },
    [draftPrompt, draftTargetRef, draftYAML, session],
  )
  const currentTriggerSimulation =
    triggerSimulation.status === "ready" &&
    session != null &&
    triggerSimulation.request.session_id === session.id &&
    triggerSimulation.request.expected_session_revision ===
      session.session_revision &&
    triggerSimulation.request.expected_draft_revision ===
      session.draft_revision &&
    triggerSimulation.request.prompt === draftPrompt &&
    triggerSimulation.request.target_ref === draftTargetRef &&
    triggerSimulation.request.yaml === draftYAML
      ? triggerSimulation
      : null
  const triggerReviewReady =
    currentTriggerSimulation?.response.simulation.executable === true &&
    currentTriggerSimulation.response.review.complete &&
    currentTriggerSimulation.response.review_token != null
  const ignoreStructuredActionsChange = useCallback(() => {}, [])
  const requestDraftTest = () => {
    if (!triggerReviewReady) {
      toast.warning(
        "Wait for a matching, complete server simulation before execution.",
      )
      return
    }
    setTestReviewOpen(true)
  }
  if (session == null) {
    const startingAI = pendingAction === "start-ai"
    const starting = pendingAction === "start"
    const startReadinessMessage = workflowStartReadinessMessage(
      startPrompt,
      pendingAction,
    )
    return (
      <div className="grid min-h-0 flex-1 gap-4 overflow-hidden">
        <section className="border-border bg-card/40 flex min-h-0 flex-col rounded-lg border">
          <div className="border-border border-b px-4 py-3">
            <h2 className="text-sm font-medium">New workflow</h2>
          </div>
          <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
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
            {showTemplates ? (
              <WorkflowTemplateCatalog
                templates={templates}
                loading={templatesLoading}
                unavailable={templatesUnavailable}
                unavailableMessage={templatesUnavailableMessage}
                installingName={installingTemplateName}
                disabled={busy || installingTemplateName != null}
                onInstall={onInstallTemplate}
              />
            ) : null}
          </div>
        </section>
      </div>
    )
  }

  return (
    <div className="grid min-h-0 flex-1 gap-4 overflow-auto xl:grid-cols-[minmax(360px,0.75fr)_minmax(0,1.25fr)] xl:overflow-hidden">
      <section className="border-border bg-card/40 flex min-h-[36rem] flex-col rounded-lg border xl:min-h-0">
        <DevelopmentHeader session={session} />
        {busyLabel ? <DevelopmentBusyBar label={busyLabel} /> : null}
        {triggerEditorBlockingMessage ? (
          <div
            role="alert"
            className="border-b border-amber-500/30 bg-amber-500/10 px-4 py-3 text-xs"
          >
            <div className="font-medium">
              {developmentSessionConflict
                ? "Workflow development changed elsewhere."
                : "Structured builder changes are pending."}
            </div>
            <p className="text-muted-foreground mt-1">
              {triggerEditorBlockingMessage}
            </p>
            {developmentSessionConflict ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={onLoadAuthoritativeDevelopmentSession}
                disabled={busy}
              >
                <IconRefresh className="size-4" />
                Discard local edits and load latest state
              </Button>
            ) : null}
          </div>
        ) : null}
        {reconciliation != null ? (
          <DevelopmentReconciliationWarning
            reconciliation={reconciliation}
            onOpenRun={onOpenTestRun}
          />
        ) : null}
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
              onChange={(event) => {
                invalidateTriggerSimulation()
                onDraftTargetRefChange(event.target.value)
              }}
              disabled={draftActionDisabled}
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
              onChange={(event) => {
                invalidateTriggerSimulation()
                onDraftPromptChange(event.target.value)
              }}
              disabled={draftActionDisabled}
              className="min-h-32 resize-none"
            />
          </div>
          <ValidationPanel validation={session.validation} />
          <Panel title="Trigger simulator">
            <div className="grid gap-3">
              <WorkflowTriggerSimulator
                session={session}
                prompt={draftPrompt}
                targetRef={draftTargetRef}
                yaml={draftYAML}
                inspectionState={eventTriggerInspection}
                disabled={draftActionDisabled}
                onSimulationChange={handleTriggerSimulationChange}
              />
              <DraftTestResultPanel
                result={lastDraftTest}
                stale={lastDraftTestStale}
                onOpenRun={onOpenTestRun}
                onFixWithAI={onFixTestWithAI}
                fixingWithAI={pendingAction === "ai-revise"}
                actionsDisabled={triggerEditorBlocked}
                disabledReason={triggerEditorBlockingMessage ?? undefined}
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
          <WorkflowPublishReadinessPanel
            targetReady={draftTargetRef.trim() !== ""}
            yamlReady={draftYAML.trim() !== ""}
            validationStatus={publishValidationStatus}
            testStatus={publishTestStatus}
            dependencyState={dependencyState}
            dependencyReport={dependencyReport}
            readinessMessage={publishReadinessMessage}
          />
          {showTemplates ? (
            <WorkflowTemplateCatalog
              templates={templates}
              loading={templatesLoading}
              unavailable={templatesUnavailable}
              unavailableMessage={templatesUnavailableMessage}
              installingName={installingTemplateName}
              disabled
              disabledReason="Finish or discard the active workflow draft before installing or restoring templates."
              onInstall={onInstallTemplate}
            />
          ) : null}
        </div>
        <div className="border-border flex flex-wrap gap-2 border-t p-3">
          <Button
            variant="outline"
            size="sm"
            onClick={onSave}
            disabled={draftActionDisabled}
            title={triggerEditorBlockingMessage ?? undefined}
          >
            <IconDeviceFloppy className="size-4" />
            {pendingAction === "save" ? "Saving" : "Save Draft"}
          </Button>
          <Button
            size="sm"
            onClick={onAIRevise}
            disabled={draftActionDisabled}
            title={triggerEditorBlockingMessage ?? undefined}
          >
            <IconSparkles className="size-4" />
            {pendingAction === "ai-revise" ? "Drafting" : "Ask AI"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onRegenerate}
            disabled={draftActionDisabled}
            title={triggerEditorBlockingMessage ?? undefined}
          >
            <IconRotateClockwise className="size-4" />
            {pendingAction === "regenerate" ? "Scaffolding" : "Scaffold"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onValidate}
            disabled={draftActionDisabled}
            title={triggerEditorBlockingMessage ?? undefined}
          >
            <IconCheck className="size-4" />
            {pendingAction === "validate" ? "Validating" : "Validate"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="disabled:opacity-60"
            onClick={requestDraftTest}
            disabled={
              !canTestDraft || draftActionDisabled || !triggerReviewReady
            }
            title={
              triggerEditorBlockingMessage ??
              (!triggerReviewReady
                ? "Wait for a matching, complete server simulation before execution."
                : !canTestDraft
                  ? testReadinessMessage
                  : undefined)
            }
          >
            <IconPlayerPlay className="size-4" />
            {pendingAction === "test" || pendingAction === "test-running"
              ? "Executing"
              : "Review & execute"}
          </Button>
          <Button
            size="sm"
            onClick={onPublish}
            disabled={!canPublish || draftActionDisabled}
            title={
              triggerEditorBlockingMessage ??
              (!canPublish ? publishReadinessMessage : undefined)
            }
          >
            <IconRocket className="size-4" />
            {pendingAction === "publish" ? "Publishing" : "Publish"}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={onDiscard}
            disabled={draftActionDisabled}
            title={triggerEditorBlockingMessage ?? undefined}
          >
            <IconTrash className="size-4" />
            {pendingAction === "discard" ? "Discarding" : "Discard"}
          </Button>
        </div>
      </section>

      <section className="border-border bg-card/40 flex min-h-[36rem] flex-col overflow-hidden rounded-lg border xl:min-h-0">
        <div className="border-border flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
          <div className="flex min-w-0 items-center gap-2">
            <IconCode className="text-muted-foreground size-4" />
            <h2 className="truncate text-sm font-medium">
              {draftEditorMode === "builder"
                ? builderSection === "jobs"
                  ? "Jobs & actions builder"
                  : "Trigger builder"
                : "Workflow YAML"}
            </h2>
          </div>
          <WorkflowEditorTabs
            mode={draftEditorMode}
            onModeChange={onDraftEditorModeChange}
          />
          <StatusBadge status={session.status} />
        </div>
        <div
          id="workflow-builder-panel"
          role="tabpanel"
          aria-labelledby="workflow-builder-tab"
          hidden={draftEditorMode !== "builder"}
          className={cn(
            "min-h-0 flex-1 flex-col",
            draftEditorMode === "builder" ? "flex" : "hidden",
          )}
        >
          <div className="border-border flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
            <WorkflowBuilderTabs
              section={builderSection}
              disabledReason={triggerEditorBlockingMessage ?? undefined}
              onSectionChange={setBuilderSection}
            />
            <span className="text-muted-foreground text-xs">
              Raw YAML remains the advanced escape hatch.
            </span>
          </div>
          <div
            id="workflow-triggers-builder-panel"
            role="tabpanel"
            aria-labelledby="workflow-triggers-builder-tab"
            hidden={builderSection !== "triggers"}
            className="min-h-0 flex-1"
          >
            <WorkflowTriggerEditor
              key={triggerEditorResetKey}
              yaml={draftYAML}
              disabled={busy || developmentSessionConflict}
              onYAMLChange={(nextYAML) => {
                invalidateTriggerSimulation()
                onDraftYAMLChange(nextYAML)
              }}
              onInspectionChange={onEventTriggerInspectionChange}
              onActivityChange={onTriggerEditorActivityChange}
              onOpenYAML={() => onDraftEditorModeChange("yaml")}
            />
          </div>
          <div
            id="workflow-jobs-builder-panel"
            role="tabpanel"
            aria-labelledby="workflow-jobs-builder-tab"
            hidden={builderSection !== "jobs"}
            className="min-h-0 flex-1"
          >
            <WorkflowJobEditor
              key={jobEditorResetKey}
              yaml={draftYAML}
              disabled={busy || developmentSessionConflict}
              onYAMLChange={(nextYAML) => {
                invalidateTriggerSimulation()
                onDraftYAMLChange(nextYAML)
              }}
              onActivityChange={onJobEditorActivityChange}
              onStructuredActionsChange={ignoreStructuredActionsChange}
              onOpenYAML={() => onDraftEditorModeChange("yaml")}
            />
          </div>
        </div>
        <div
          id="workflow-yaml-panel"
          role="tabpanel"
          aria-labelledby="workflow-yaml-tab"
          hidden={draftEditorMode !== "yaml"}
          className="min-h-0 flex-1"
        >
          <Textarea
            aria-label="Workflow YAML"
            value={draftYAML}
            onChange={(event) => {
              invalidateTriggerSimulation()
              onDraftYAMLChange(event.target.value)
            }}
            disabled={busy}
            spellCheck={false}
            className="size-full min-h-0 resize-none rounded-none border-0 p-4 font-mono text-xs shadow-none focus-visible:ring-0"
          />
        </div>
      </section>
      {currentTriggerSimulation != null ? (
        <WorkflowDraftTestReviewDialog
          open={testReviewOpen}
          pending={pendingAction === "test" || pendingAction === "test-running"}
          identity={currentTriggerSimulation.identity}
          simulation={currentTriggerSimulation.response.simulation}
          review={currentTriggerSimulation.response.review}
          onOpenChange={setTestReviewOpen}
          onConfirm={(reviewedIdentity) => {
            const request = currentTriggerSimulation.request
            if (
              reviewedIdentity !== currentTriggerSimulation.identity ||
              session.id !== request.session_id ||
              session.session_revision !== request.expected_session_revision ||
              session.draft_revision !== request.expected_draft_revision ||
              draftPrompt !== request.prompt ||
              draftTargetRef !== request.target_ref ||
              draftYAML !== request.yaml
            ) {
              toast.warning(
                "The draft or trigger scenario changed. Simulate and review it again.",
              )
              invalidateTriggerSimulation()
              return
            }
            setTestReviewOpen(false)
            setTriggerSimulation({
              status: "idle",
              message: "Execution started. Simulate again before another run.",
            })
            onTest(currentTriggerSimulation)
          }}
        />
      ) : null}
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

function DevelopmentReconciliationWarning({
  reconciliation,
  onOpenRun,
}: {
  reconciliation: WorkflowDevelopmentTestReconciliation
  onOpenRun: (runID: string) => void
}) {
  const canOpenRun = isWorkflowRunID(reconciliation.run_id)
  return (
    <div
      role="alert"
      className="text-foreground flex min-w-0 items-start gap-3 border-b border-amber-500/40 bg-amber-500/10 px-4 py-3 text-xs"
    >
      <IconAlertTriangle
        className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400"
        aria-hidden="true"
      />
      <div className="min-w-0 flex-1">
        <div className="font-medium">Draft test reconciliation degraded</div>
        <div className="mt-1 break-words">{reconciliation.message}</div>
        {reconciliation.run_id ? (
          <div className="mt-1 font-mono break-all">
            {reconciliation.run_id}
          </div>
        ) : null}
      </div>
      {canOpenRun ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onOpenRun(reconciliation.run_id)}
          aria-label={`Open reconciled run ${reconciliation.run_id}`}
        >
          <IconExternalLink className="size-4" aria-hidden="true" />
          Open Run
        </Button>
      ) : null}
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

export function WorkflowRunPanel({
  workflows,
  workflow,
  stamp,
  compatibility,
  selectedWorkflowRef,
  dependencyState,
  dependencyReport,
  inputValues,
  secretValues,
  secretsJSON,
  session,
  deliveryJSON,
  onSelectWorkflow,
  onRetryDependencies,
  onInputChange,
  onSecretChange,
  onSecretsJSONChange,
  onSessionChange,
  onDeliveryJSONChange,
  onRun,
  running,
  canRun,
  readinessMessage,
}: {
  workflows: WorkflowDefinition[]
  workflow: WorkflowDefinition | null
  stamp?: WorkflowValidationStamp
  compatibility?: WorkflowCompatibilitySummary
  selectedWorkflowRef: string | null
  dependencyState: WorkflowDependencyCheckState
  dependencyReport?: WorkflowDependencyCheckResponse
  inputValues: WorkflowRunInputValues
  secretValues: WorkflowRunSecretValues
  secretsJSON: string
  session: string
  deliveryJSON: string
  onSelectWorkflow: (ref: string) => void
  onRetryDependencies: () => void
  onInputChange: (name: string, value: string) => void
  onSecretChange: (name: string, value: string) => void
  onSecretsJSONChange: (value: string) => void
  onSessionChange: (value: string) => void
  onDeliveryJSONChange: (value: string) => void
  onRun: () => Promise<boolean>
  running: boolean
  canRun: boolean
  readinessMessage: string
}) {
  const [open, setOpen] = useState(false)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const status = workflowRunStatus(workflow, stamp, compatibility)
  const inputs = workflowRunInputEntries(workflow)
  const secrets = workflowRunSecretEntries(workflow)
  const busy = running || submitting
  const runnable = canRun && !busy
  const runNow = async () => {
    if (!runnable) {
      return
    }
    setSubmitting(true)
    try {
      if (await onRun()) {
        setOpen(false)
      }
    } finally {
      setSubmitting(false)
    }
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
          <Popover>
            <PopoverTrigger asChild>
              <Button
                type="button"
                variant="outline"
                size="icon-sm"
                disabled={workflow == null}
                aria-label="Inspect workflow dependencies"
                title="Inspect workflow dependencies"
              >
                <IconGitBranch className="size-4" />
              </Button>
            </PopoverTrigger>
            <PopoverContent
              align="end"
              className="max-h-[min(36rem,calc(100dvh-2rem))] w-[min(480px,calc(100vw-2rem))] overflow-y-auto p-0"
            >
              <WorkflowDependencyReadinessPanel
                workflowRef={workflow?.ref ?? "No workflow selected"}
                dependencyState={dependencyState}
                dependencyReport={dependencyReport}
                onRetry={onRetryDependencies}
              />
            </PopoverContent>
          </Popover>
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
                    disabled={workflows.length === 0 || busy}
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
                        disabled={busy}
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
                          disabled={busy}
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
                      disabled={busy}
                      placeholder="Additional secrets JSON"
                      className="min-h-16 resize-none font-mono text-xs"
                    />
                    <Input
                      id="workflow-run-session"
                      aria-label="Manual run session"
                      value={session}
                      onChange={(event) => onSessionChange(event.target.value)}
                      placeholder="Session"
                      disabled={busy}
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
                      disabled={busy}
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
                  {busy ? "Running" : "Run workflow"}
                </Button>
              </div>
            </PopoverContent>
          </Popover>
        </div>
      </div>
      {workflow != null ? (
        <WorkflowDefinitionInspector
          key={workflow.ref}
          target={{ kind: "published", ref: workflow.ref }}
          className="mt-3"
        />
      ) : null}
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

export function RunSummary({ run }: { run: WorkflowRun }) {
  const workflowRef = navigableWorkflowRef(run.workflow_ref)
  const origin = trustedWorkflowRunOrigin(run.origin)
  return (
    <Panel title="Summary">
      <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
        <Meta
          label="Workflow"
          value={
            workflowRef && run.workflow_id ? (
              <Link
                to="/agent/workflows/$id"
                params={{ id: run.workflow_id }}
                search={{ q: "ORDER BY ref ASC" }}
                className="text-primary hover:underline"
              >
                {workflowRef}
              </Link>
            ) : (
              run.workflow_ref
            )
          }
          mono
        />
        <Meta label="Origin" value={origin?.kind.replaceAll("_", " ") ?? "-"} />
        <Meta
          label="Event"
          value={
            origin ? (
              <Link
                to="/events"
                search={{ event: origin.event_id }}
                className="text-primary hover:underline"
              >
                {origin.event_id}
              </Link>
            ) : (
              "-"
            )
          }
          mono
        />
        <Meta
          label="Dispatch"
          value={
            origin?.dispatch_id ? (
              <Link
                to="/events"
                search={{
                  view: "dispatches",
                  dispatch: origin.dispatch_id,
                }}
                className="text-primary hover:underline"
              >
                {origin.dispatch_id}
              </Link>
            ) : (
              "-"
            )
          }
          mono
        />
        <Meta
          label="Root run"
          value={
            origin == null ? (
              "-"
            ) : origin.root_run_id === run.id ? (
              origin.root_run_id
            ) : (
              <Link
                to="/agent/workflows/runs/$id"
                params={{ id: origin.root_run_id }}
                search={{ q: "ORDER BY created DESC" }}
                className="text-primary hover:underline"
              >
                {origin.root_run_id}
              </Link>
            )
          }
          mono
        />
        <Meta label="Created" value={formatDate(run.created_at)} />
        <Meta label="Updated" value={formatDate(run.updated_at)} />
        <Meta
          label="Cancel requested"
          value={formatDate(run.cancel_requested_at)}
        />
        <Meta label="Completed" value={formatDate(run.completed_at)} />
        <Meta
          label="Cancel reason"
          value={run.cancel_reason ?? "-"}
          className="sm:col-span-2"
          valueClassName="break-words whitespace-pre-wrap"
        />
        <Meta label="Session" value={run.session ?? "-"} mono />
        <Meta
          label="Parent"
          value={<WorkflowRunLink runID={run.parent_run_id} />}
          mono
        />
        <Meta label="Caller job" value={run.caller_job_id ?? "-"} mono />
        <Meta
          label="Retry of"
          value={<WorkflowRunLink runID={run.retry_of_run_id} />}
          mono
        />
        <Meta
          label="Children"
          value={<WorkflowRunLinks runIDs={run.child_run_ids} />}
          mono
        />
      </dl>
      {run.error ? (
        <div className="bg-destructive/10 text-destructive mt-3 rounded-md px-3 py-2 text-sm">
          {run.error}
        </div>
      ) : null}
      <JsonBlock label="Inputs" value={run.inputs} />
      <JsonBlock label="Outputs" value={run.outputs} />
      <JsonBlock label="Delivery" value={run.delivery} />
      <JsonBlock label="Event" value={run.event} />
    </Panel>
  )
}

function WorkflowRunLink({ runID }: { runID?: string }) {
  if (!runID) return "-"
  if (!isWorkflowRunID(runID)) return runID
  return (
    <Link
      to="/agent/workflows/runs/$id"
      params={{ id: runID }}
      search={{ q: "ORDER BY created DESC" }}
      className="text-primary hover:underline"
    >
      {runID}
    </Link>
  )
}

function WorkflowRunLinks({ runIDs }: { runIDs?: string[] }) {
  if (!runIDs || runIDs.length === 0) return "-"
  return (
    <span className="flex flex-wrap gap-x-2 gap-y-1">
      {runIDs.map((runID) => (
        <WorkflowRunLink key={runID} runID={runID} />
      ))}
    </span>
  )
}

export function RunGraphPanel({
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

export function ManagedExecutionPanel({ run }: { run: WorkflowRun }) {
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

export function ExecutionPanel({ run }: { run: WorkflowRun }) {
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

export function EventsPanel({
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
  actionsDisabled = false,
  disabledReason,
}: {
  result: DraftTestSnapshot | null
  stale: boolean
  onOpenRun: (runID: string) => void
  onFixWithAI: () => void
  fixingWithAI: boolean
  actionsDisabled?: boolean
  disabledReason?: string
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
            disabled={actionsDisabled}
            title={disabledReason ?? "Open run"}
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
            disabled={fixingWithAI || actionsDisabled}
            title={disabledReason ?? "Ask AI to fix this draft test failure"}
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
  validationStatus,
  testResult,
  testStale,
  sessionSnapshotReady,
  dependencyState,
  dependencyReport,
}: {
  session: WorkflowDevelopmentSession | null
  targetRef: string
  yaml: string
  validationStatus: string
  testResult: DraftTestSnapshot | null
  testStale: boolean
  sessionSnapshotReady: boolean
  dependencyState: WorkflowDependencyCheckState
  dependencyReport?: WorkflowDependencyCheckResponse
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
  if (validationStatus === "invalid") {
    return "Fix validation errors before publishing."
  }
  if (validationStatus === "stale") {
    return "Validate the draft again after the latest edits."
  }
  if (validationStatus !== "valid") {
    return "Validate the current draft before publishing."
  }
  if (testResult == null) {
    return "Run a successful draft test before publishing."
  }
  if (activeStatuses.has(testResult.status)) {
    return "Wait for the draft test to finish."
  }
  if (testStale) {
    return "Run the draft again after the latest edits."
  }
  if (testResult.status !== "succeeded") {
    return "Fix the failing draft test before publishing."
  }
  if (!sessionSnapshotReady) {
    return "Refreshing the tested draft revision before publishing."
  }
  if (dependencyState === "idle") {
    return "Complete the draft before checking dependencies."
  }
  if (dependencyState === "stale") {
    return "Wait for dependency readiness to catch up with the latest edits."
  }
  if (dependencyState === "loading") {
    return "Checking dependencies for the current draft."
  }
  if (dependencyState === "error" || dependencyReport == null) {
    return "Dependency readiness could not be checked. Refresh and try again."
  }
  if (!dependencyReport.workflow_enabled) {
    return "Enable workflows in Settings before publishing."
  }
  if (!dependencyReport.structural_ready) {
    return "Resolve the structural dependency blockers before publishing."
  }
  if (!dependencyReport.runtime_ready) {
    return "Resolve the runtime dependency blockers before publishing."
  }
  if (!dependencyReport.ready) {
    return "Resolve the dependency blockers before publishing."
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

function workflowTriggerEditorBlockingMessage(
  activity: WorkflowTriggerEditorActivity,
) {
  if (activity.applying) {
    return "Wait for the trigger builder to finish applying before leaving or running another draft action."
  }
  if (activity.conflict) {
    return "Discard the preserved trigger builder edits and load the latest YAML before leaving or running another draft action."
  }
  if (activity.dirty) {
    return "Apply or reset the trigger builder changes before leaving or running another draft action."
  }
  return null
}

function workflowJobEditorBlockingMessage(activity: WorkflowJobEditorActivity) {
  if (activity.applying) {
    return "Wait for the jobs and actions builder to finish applying before leaving or running another draft action."
  }
  if (activity.conflict) {
    return "Discard the preserved jobs and actions edits and load the latest YAML before leaving or running another draft action."
  }
  if (activity.dirty) {
    return "Apply, reset, or cancel the jobs and actions changes before leaving or running another draft action."
  }
  return null
}

function workflowDevelopmentSessionConflictMessage(
  conflict: WorkflowDevelopmentSessionConflict | null,
) {
  if (conflict == null) {
    return null
  }
  if (conflict.incomingSession == null) {
    return "The active workflow development session was removed elsewhere. Your unsaved draft and builder edits remain local until you explicitly discard them and load the latest state."
  }
  if (conflict.incomingSession.id !== conflict.baseSession.id) {
    return "A different workflow development session became active elsewhere. Your unsaved draft and builder edits remain local until you explicitly discard them and load the latest state."
  }
  return "The authoritative workflow draft changed elsewhere. Your unsaved draft and builder edits remain local until you explicitly discard them and load the latest state."
}

function workflowDevelopmentSessionsEqual(
  left: WorkflowDevelopmentSession | null,
  right: WorkflowDevelopmentSession | null,
) {
  if (left == null || right == null) {
    return left === right
  }
  return (
    left.id === right.id &&
    left.session_revision === right.session_revision &&
    left.draft_revision === right.draft_revision &&
    left.base_target_revision === right.base_target_revision &&
    left.status === right.status &&
    left.prompt === right.prompt &&
    left.target_workflow_ref === right.target_workflow_ref &&
    left.yaml === right.yaml &&
    left.updated_at === right.updated_at
  )
}

function isPublishTestReady(result: DraftTestSnapshot | null, stale: boolean) {
  return publishTestStatus(result, stale) === "succeeded"
}

function canFixDraftTestWithAI(result: DraftTestSnapshot, stale: boolean) {
  return (
    !stale &&
    !activeStatuses.has(result.status) &&
    result.status !== "succeeded" &&
    result.status !== "skipped"
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
    : activeStatuses.has(status) || status === "ready_to_publish"
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
  className,
  valueClassName,
}: {
  label: string
  value: ReactNode
  mono?: boolean
  className?: string
  valueClassName?: string
}) {
  return (
    <div className={cn("min-w-0", className)}>
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd
        className={cn(
          "min-w-0 truncate",
          mono && "font-mono text-xs",
          valueClassName,
        )}
      >
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

export function workflowRunInitialInputValues(
  workflow: WorkflowDefinition | null,
) {
  const values: WorkflowRunInputValues = {}
  for (const { name, input } of workflowRunInputEntries(workflow)) {
    values[name] = workflowInputInitialValue(input)
  }
  return values
}

export function workflowRunInitialSecretValues(
  workflow: WorkflowDefinition | null,
) {
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

export function workflowRunPayloadValidationMessage(
  workflow: WorkflowDefinition | null,
  inputValues: WorkflowRunInputValues,
  secretValues: WorkflowRunSecretValues,
  secretsJSON: string,
  deliveryJSON: string,
): string | null {
  return firstMessage([
    workflowRunInputValidationMessage(workflow, inputValues),
    jsonStringObjectValidationMessage(secretsJSON, "Secrets"),
    workflowRunSecretValidationMessage(workflow, secretValues, secretsJSON),
    jsonObjectValidationMessage(deliveryJSON, "Delivery"),
  ])
}

export function workflowRunInputsPayload(
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

export function workflowRunSecretsPayload(
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

function workflowDevelopmentDraftFence(
  session: WorkflowDevelopmentSession | null,
) {
  if (
    session == null ||
    session.id === "" ||
    session.session_revision === "" ||
    session.draft_revision === ""
  ) {
    throw new Error("Reload the workflow draft before changing it.")
  }
  return {
    session_id: session.id,
    expected_session_revision: session.session_revision,
    expected_draft_revision: session.draft_revision,
  }
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

export function optionalString(value: string) {
  const trimmed = value.trim()
  return trimmed === "" ? undefined : trimmed
}

export function parseDeliveryJSONObject(value: string, label: string) {
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
    // The persisted legacy draft key normalizes trailing whitespace. UI
    // currentness must instead track the exact editor bytes because dependency
    // and publish revisions are byte-exact.
    draftKey: draftKey(session.target_workflow_ref, session.yaml),
    draftRevision: session.last_test.draft_revision,
    runID: session.last_test.run_id,
    eventID: session.last_test.event_id,
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
    editor.yaml === snapshot.yaml
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
    left.yaml === right.yaml
  )
}

function draftKey(targetRef: string, yaml: string) {
  return `${targetRef.trim()}\u0000${yaml}`
}

async function invalidateWorkflowQueries(
  queryClient: ReturnType<typeof useQueryClient>,
) {
  await queryClient.invalidateQueries({ queryKey: ["workflows"] })
}

async function invalidateWorkflowDefinitionInspections(
  queryClient: ReturnType<typeof useQueryClient>,
) {
  await queryClient.invalidateQueries({
    queryKey: workflowDefinitionInspectionQueryKey,
  })
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
