import {
  IconAlertTriangle,
  IconArrowDown,
  IconArrowUp,
  IconCheck,
  IconCode,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconX,
} from "@tabler/icons-react"
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"

import {
  type WorkflowDevelopmentValidation,
  type WorkflowEditorField,
  type WorkflowEditorFieldMutation,
  type WorkflowEditorJSONValue,
  type WorkflowJobEditorContext,
  type WorkflowJobEditorJob,
  type WorkflowJobEditorJobFields,
  type WorkflowJobEditorJobPatch,
  type WorkflowJobEditorOperation,
  type WorkflowJobEditorStep,
  type WorkflowJobEditorStepFields,
  type WorkflowJobEditorStepPatch,
  WorkflowJobsEditorAPIError,
  type WorkflowJobsInspection,
  inspectWorkflowJobs,
  renderWorkflowJobs,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
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

import { WorkflowCapabilityTargetField } from "./workflow-capability-target-field"
import type { WorkflowDraftActionReview } from "./workflow-draft-test-review"
import { workflowJSONNumberIsBrowserSafe } from "./workflow-json-number"

export interface WorkflowJobEditorActivity {
  dirty: boolean
  applying: boolean
  conflict: boolean
}

export interface WorkflowStructuredActionsState {
  yaml: string
  status: "loading" | "ready" | "error"
  review?: WorkflowDraftActionReview
  reason?: string
}

type MutationMode = "keep" | "set" | "remove"

interface FieldDraft {
  mode: MutationMode
  value: string
}

type JobDraft = Record<keyof WorkflowJobEditorJobFields, FieldDraft>
type StepDraft = Record<keyof WorkflowJobEditorStepFields, FieldDraft>

interface ExternalYAMLConflict {
  yaml: string
  inspection?: WorkflowJobsInspection
  error?: string
}

const idleActivity: WorkflowJobEditorActivity = {
  dirty: false,
  applying: false,
  conflict: false,
}

const jobFieldLabels: Record<keyof WorkflowJobEditorJobFields, string> = {
  name: "Display name",
  runs_on: "Runs on",
  needs: "Dependencies",
  uses: "Reusable workflow",
  if: "Job condition",
  continue_on_error: "Continue on error",
  with: "Reusable inputs",
  secrets: "Reusable secrets",
  outputs: "Job outputs",
  context: "Run context",
}

const stepFieldLabels: Record<keyof WorkflowJobEditorStepFields, string> = {
  id: "Step ID",
  name: "Display name",
  uses: "Action target",
  if: "Step condition",
  continue_on_error: "Continue on error",
  with: "Action inputs",
  context: "Run context",
}

export function WorkflowJobEditor({
  yaml,
  disabled,
  onYAMLChange,
  onActivityChange,
  onStructuredActionsChange,
  onOpenYAML,
}: {
  yaml: string
  disabled: boolean
  onYAMLChange: (yaml: string) => void
  onActivityChange: (activity: WorkflowJobEditorActivity) => void
  onStructuredActionsChange: (state: WorkflowStructuredActionsState) => void
  onOpenYAML: () => void
}) {
  const latestYAMLRef = useRef(yaml)
  const inspectionYAMLRef = useRef<string | null>(null)
  const inspectionRef = useRef<WorkflowJobsInspection | null>(null)
  const applyControllerRef = useRef<AbortController | null>(null)
  const applyGenerationRef = useRef(0)
  const dirtyRef = useRef(false)
  const mountedRef = useRef(false)
  const [inspection, setInspection] = useState<WorkflowJobsInspection | null>(
    null,
  )
  const [inspectionYAML, setInspectionYAML] = useState<string | null>(null)
  const [selectedJobID, setSelectedJobID] = useState<string | null>(null)
  const [selectedStepIndex, setSelectedStepIndex] = useState<number | null>(
    null,
  )
  const [jobIDDraft, setJobIDDraft] = useState<FieldDraft | null>(null)
  const [jobDraft, setJobDraft] = useState<JobDraft | null>(null)
  const [stepDraft, setStepDraft] = useState<StepDraft | null>(null)
  const [loading, setLoading] = useState(true)
  const [applying, setApplying] = useState(false)
  const [loadError, setLoadError] = useState("")
  const [applyError, setApplyError] = useState("")
  const [selectionError, setSelectionError] = useState("")
  const [candidateValidation, setCandidateValidation] =
    useState<WorkflowDevelopmentValidation>()
  const [externalConflict, setExternalConflict] =
    useState<ExternalYAMLConflict | null>(null)
  const [inspectionNonce, setInspectionNonce] = useState(0)
  const [newJobOpen, setNewJobOpen] = useState(false)
  const [newJobID, setNewJobID] = useState("")
  const [newJobIndex, setNewJobIndex] = useState(0)
  const [newStepOpen, setNewStepOpen] = useState(false)
  const [newStepID, setNewStepID] = useState("")
  const [newStepTarget, setNewStepTarget] = useState("")
  const [newStepIndex, setNewStepIndex] = useState(0)
  const [deleteJobID, setDeleteJobID] = useState<string | null>(null)
  const [deleteStepIndex, setDeleteStepIndex] = useState<number | null>(null)

  const selectedJob =
    inspection?.jobs.find((job) => job.id === selectedJobID) ?? null
  const selectedStep =
    selectedJob?.steps.find((step) => step.index === selectedStepIndex) ?? null
  const newJobInsertionIndex = boundedInsertionIndex(
    newJobIndex,
    inspection?.jobs.length ?? 0,
  )
  const newStepInsertionIndex = boundedInsertionIndex(
    newStepIndex,
    selectedJob?.steps.length ?? 0,
  )
  const jobFormDirty =
    (jobIDDraft != null && jobIDDraft.mode !== "keep") ||
    (jobDraft != null && fieldDraftsDirty(jobDraft))
  const stepFormDirty = stepDraft != null && fieldDraftsDirty(stepDraft)
  const formDirty = jobFormDirty || stepFormDirty
  const structuralDraftDirty =
    newJobOpen || newStepOpen || deleteJobID != null || deleteStepIndex != null
  const dirty = formDirty || structuralDraftDirty
  const hasConflict =
    externalConflict != null ||
    (dirty && inspectionYAML != null && inspectionYAML !== yaml)
  const patchResult = useMemo(
    () =>
      stepFormDirty && selectedStep != null && stepDraft != null
        ? stepPatchFromDraft(stepDraft)
        : selectedJob != null && jobDraft != null
          ? jobPatchFromDraft(jobDraft)
          : { fields: {}, errors: [] },
    [jobDraft, selectedJob, selectedStep, stepDraft, stepFormDirty],
  )
  const jobIDError =
    jobIDDraft?.mode === "set"
      ? workflowEditorIDError(jobIDDraft.value, "Job ID", false, true)
      : null
  const newJobIDError = newJobOpen
    ? newJobID.trim() === ""
      ? "New job ID cannot be blank."
      : workflowEditorIDError(newJobID, "New job ID", false, true)
    : null
  const newStepIDError =
    newStepOpen && newStepID !== ""
      ? workflowEditorIDError(newStepID, "Step ID", true)
      : null

  useLayoutEffect(() => {
    latestYAMLRef.current = yaml
    dirtyRef.current = dirty
  }, [dirty, yaml])

  useEffect(() => {
    inspectionRef.current = inspection
  }, [inspection])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      applyGenerationRef.current += 1
      applyControllerRef.current?.abort()
      applyControllerRef.current = null
    }
  }, [])

  useEffect(() => {
    onActivityChange({ dirty, applying, conflict: hasConflict })
  }, [applying, dirty, hasConflict, onActivityChange])

  useEffect(() => () => onActivityChange(idleActivity), [onActivityChange])

  useEffect(() => {
    const controller = new AbortController()
    if (inspectionYAMLRef.current !== yaml) {
      applyControllerRef.current?.abort()
    }
    setLoading(true)
    setLoadError("")
    setSelectionError("")
    if (!dirtyRef.current) {
      setApplyError("")
      setCandidateValidation(undefined)
    }
    onStructuredActionsChange({ yaml, status: "loading" })
    const timeout = window.setTimeout(() => {
      void inspectWorkflowJobs(yaml, controller.signal)
        .then((result) => {
          if (controller.signal.aborted || latestYAMLRef.current !== yaml) {
            return
          }
          onStructuredActionsChange({
            yaml,
            status: "ready",
            review: workflowDraftActionReview(result),
          })
          if (
            dirtyRef.current &&
            inspectionYAMLRef.current != null &&
            inspectionYAMLRef.current !== yaml
          ) {
            setExternalConflict({ yaml, inspection: result })
            setLoading(false)
            return
          }
          if (dirtyRef.current && inspectionYAMLRef.current === yaml) {
            setLoading(false)
            return
          }
          hydrateInspection(result, yaml)
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted || latestYAMLRef.current !== yaml) {
            return
          }
          const message = errorMessage(error)
          onStructuredActionsChange({
            yaml,
            status: "error",
            reason: message,
          })
          if (
            dirtyRef.current &&
            inspectionYAMLRef.current != null &&
            inspectionYAMLRef.current !== yaml
          ) {
            setExternalConflict({ yaml, error: message })
            setLoading(false)
            return
          }
          inspectionRef.current = null
          inspectionYAMLRef.current = null
          setInspection(null)
          setInspectionYAML(null)
          setLoadError(message)
          setLoading(false)
        })
    }, 250)

    return () => {
      window.clearTimeout(timeout)
      controller.abort()
    }
    // Hydration intentionally reads the current selection from refs/state
    // captured for this exact YAML identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inspectionNonce, onStructuredActionsChange, yaml])

  function hydrateInspection(
    result: WorkflowJobsInspection,
    sourceYAML: string,
    preferred?: { jobID?: string | null; stepIndex?: number | null },
  ) {
    const nextJob =
      result.jobs.find(
        (job) => job.id === (preferred?.jobID ?? selectedJobID),
      ) ??
      result.jobs[0] ??
      null
    const nextStep =
      nextJob?.steps.find(
        (step) => step.index === (preferred?.stepIndex ?? selectedStepIndex),
      ) ??
      nextJob?.steps[0] ??
      null
    inspectionRef.current = result
    inspectionYAMLRef.current = sourceYAML
    setInspection(result)
    setInspectionYAML(sourceYAML)
    setSelectedJobID(nextJob?.id ?? null)
    setSelectedStepIndex(nextStep?.index ?? null)
    setJobIDDraft(nextJob == null ? null : { mode: "keep", value: nextJob.id })
    setJobDraft(nextJob == null ? null : jobDraftFromProjection(nextJob))
    setStepDraft(nextStep == null ? null : stepDraftFromProjection(nextStep))
    setExternalConflict(null)
    setNewJobOpen(false)
    setNewJobID("")
    setNewJobIndex(0)
    setNewStepOpen(false)
    setNewStepID("")
    setNewStepTarget("")
    setNewStepIndex(0)
    setDeleteJobID(null)
    setDeleteStepIndex(null)
    setLoading(false)
  }

  const selectJob = (job: WorkflowJobEditorJob) => {
    if (job.id === selectedJobID) {
      return
    }
    if (dirty) {
      setSelectionError(
        "Apply, reset, or cancel pending job and action changes before selecting another job.",
      )
      return
    }
    const step = job.steps[0] ?? null
    setSelectedJobID(job.id)
    setSelectedStepIndex(step?.index ?? null)
    setJobIDDraft({ mode: "keep", value: job.id })
    setJobDraft(jobDraftFromProjection(job))
    setStepDraft(step == null ? null : stepDraftFromProjection(step))
    clearEditorMessages()
  }

  const selectStep = (step: WorkflowJobEditorStep) => {
    if (step.index === selectedStepIndex) {
      return
    }
    if (dirty) {
      setSelectionError(
        "Apply, reset, or cancel pending job and action changes before selecting another step.",
      )
      return
    }
    setSelectedStepIndex(step.index)
    setStepDraft(stepDraftFromProjection(step))
    clearEditorMessages()
  }

  const clearEditorMessages = () => {
    setSelectionError("")
    setApplyError("")
    setCandidateValidation(undefined)
  }

  const resetForm = () => {
    setJobIDDraft(
      selectedJob == null ? null : { mode: "keep", value: selectedJob.id },
    )
    setJobDraft(
      selectedJob == null ? null : jobDraftFromProjection(selectedJob),
    )
    setStepDraft(
      selectedStep == null ? null : stepDraftFromProjection(selectedStep),
    )
    setNewJobOpen(false)
    setNewJobID("")
    setNewJobIndex(0)
    setNewStepOpen(false)
    setNewStepID("")
    setNewStepTarget("")
    setNewStepIndex(0)
    setDeleteJobID(null)
    setDeleteStepIndex(null)
    clearEditorMessages()
  }

  const discardConflictAndLoad = () => {
    const conflict = externalConflict
    resetForm()
    if (conflict?.inspection != null) {
      hydrateInspection(conflict.inspection, conflict.yaml)
      return
    }
    setExternalConflict(null)
    setInspectionNonce((nonce) => nonce + 1)
  }

  const applyOperation = async (
    operation: WorkflowJobEditorOperation,
    preferred?: { jobID?: string | null; stepIndex?: number | null },
  ) => {
    const baseInspection = inspectionRef.current
    const baseYAML = latestYAMLRef.current
    if (
      disabled ||
      applying ||
      hasConflict ||
      baseInspection == null ||
      inspectionYAMLRef.current !== baseYAML
    ) {
      return
    }
    applyControllerRef.current?.abort()
    const controller = new AbortController()
    const generation = applyGenerationRef.current + 1
    applyGenerationRef.current = generation
    applyControllerRef.current = controller
    setApplying(true)
    setApplyError("")
    setCandidateValidation(undefined)
    try {
      const result = await renderWorkflowJobs(
        {
          yaml: baseYAML,
          revision: baseInspection.revision,
          operation,
        },
        controller.signal,
      )
      if (
        controller.signal.aborted ||
        !mountedRef.current ||
        generation !== applyGenerationRef.current ||
        latestYAMLRef.current !== baseYAML ||
        inspectionRef.current?.revision !== baseInspection.revision
      ) {
        return
      }
      hydrateInspection(
        result,
        result.yaml,
        preferredSelection(result, operation, preferred),
      )
      setCandidateValidation(result.validation)
      onStructuredActionsChange({
        yaml: result.yaml,
        status: "ready",
        review: workflowDraftActionReview(result),
      })
      onYAMLChange(result.yaml)
    } catch (error: unknown) {
      if (
        controller.signal.aborted ||
        !mountedRef.current ||
        generation !== applyGenerationRef.current ||
        latestYAMLRef.current !== baseYAML ||
        inspectionRef.current?.revision !== baseInspection.revision
      ) {
        return
      }
      if (
        error instanceof WorkflowJobsEditorAPIError &&
        error.inspection != null &&
        latestYAMLRef.current === baseYAML
      ) {
        inspectionRef.current = error.inspection
        setInspection(error.inspection)
        setCandidateValidation(error.inspection.validation)
      }
      setApplyError(errorMessage(error))
    } finally {
      if (mountedRef.current && generation === applyGenerationRef.current) {
        setApplying(false)
        applyControllerRef.current = null
      }
    }
  }

  const applyPatch = () => {
    if (
      patchResult.errors.length !== 0 ||
      jobIDError != null ||
      selectedJob == null ||
      !formDirty
    ) {
      return
    }
    if (stepFormDirty && selectedStep != null) {
      void applyOperation({
        type: "step.patch",
        job_id: selectedJob.id,
        step_index: selectedStep.index,
        fields: patchResult.fields as WorkflowJobEditorStepPatch,
      })
      return
    }
    void applyOperation(
      {
        type: "job.patch",
        job_id: selectedJob.id,
        fields: patchResult.fields as WorkflowJobEditorJobPatch,
        ...(jobIDDraft?.mode === "set"
          ? {
              new_job_id: {
                mode: "set" as const,
                value: jobIDDraft.value,
              },
            }
          : {}),
      },
      {
        jobID: jobIDDraft?.mode === "set" ? jobIDDraft.value : selectedJob.id,
      },
    )
  }

  const canMutate =
    !disabled &&
    !loading &&
    !applying &&
    !hasConflict &&
    inspectionYAML === yaml &&
    inspection?.editable === true
  const canApply =
    canMutate &&
    formDirty &&
    !structuralDraftDirty &&
    patchResult.errors.length === 0 &&
    jobIDError == null &&
    (stepFormDirty
      ? selectedStep?.editable === true
      : selectedJob?.editable === true)
  const jobFieldsDisabled = !canMutate || stepFormDirty
  const stepFieldsDisabled = !canMutate || jobFormDirty

  return (
    <div className="flex size-full min-h-0 flex-col overflow-hidden">
      <div className="border-border flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
        <div className="min-w-0">
          <h3 className="text-sm font-medium">Jobs & actions</h3>
          <p className="text-muted-foreground text-xs">
            Structured changes update draft YAML only. They never execute a
            workflow.
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onOpenYAML}
          disabled={dirty || applying}
          title={
            dirty
              ? "Apply, reset, or cancel pending structured changes first."
              : "Open Workflow YAML"
          }
        >
          <IconCode className="size-4" />
          Raw YAML
        </Button>
      </div>

      {externalConflict != null ? (
        <div
          role="alert"
          className="border-b border-amber-500/30 bg-amber-500/10 px-3 py-3 text-xs"
        >
          <div className="font-medium">Workflow YAML changed elsewhere.</div>
          <p className="text-muted-foreground mt-1">
            Pending job or action edits were preserved and cannot be applied to
            a different source revision.
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="mt-2"
            onClick={discardConflictAndLoad}
          >
            <IconRefresh className="size-4" />
            Discard pending edits and load latest YAML
          </Button>
        </div>
      ) : null}

      {loadError ? (
        <EditorError
          message={loadError}
          onRetry={() => setInspectionNonce((nonce) => nonce + 1)}
          onOpenYAML={onOpenYAML}
        />
      ) : loading || inspection == null ? (
        <div
          role="status"
          className="text-muted-foreground m-4 rounded-md border border-dashed px-4 py-10 text-center text-sm"
        >
          Inspecting ordered jobs and actions…
        </div>
      ) : (
        <>
          {!inspection.editable ? (
            <RawOnlyNotice
              title="This workflow is raw-only"
              reason={
                inspection.reason ??
                "The jobs mapping uses source features that cannot be edited safely in the structured builder."
              }
              onOpenYAML={onOpenYAML}
            />
          ) : !inspection.complete ? (
            <div
              role="alert"
              className="border-b border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs"
            >
              The projection is partial:{" "}
              {inspection.limits.map(limitLabel).join(", ")}. Omitted source is
              preserved; use Workflow YAML for anything not shown.
            </div>
          ) : null}

          <div className="grid min-h-0 flex-1 overflow-y-auto lg:grid-cols-[minmax(14rem,0.72fr)_minmax(0,1.28fr)] lg:overflow-hidden">
            <aside className="border-border grid content-start gap-3 border-b p-3 lg:min-h-0 lg:overflow-y-auto lg:border-r lg:border-b-0">
              <div className="flex items-center justify-between gap-2">
                <div>
                  <h4 className="text-sm font-medium">Job graph</h4>
                  <p className="text-muted-foreground text-xs">
                    Source order with declared dependencies.
                  </p>
                </div>
                <Badge variant="outline">{inspection.jobs.length}</Badge>
              </div>
              <div
                role="list"
                aria-label="Workflow job graph"
                className="grid gap-2"
              >
                {inspection.jobs.length === 0 ? (
                  <div className="text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-xs">
                    No jobs yet.
                  </div>
                ) : (
                  inspection.jobs.map((job) => (
                    <div key={job.id} role="listitem">
                      <button
                        type="button"
                        aria-current={
                          selectedJobID === job.id ? "true" : undefined
                        }
                        className={cn(
                          "border-border hover:bg-muted/50 w-full min-w-0 rounded-md border p-3 text-left transition-colors",
                          selectedJobID === job.id &&
                            "border-primary bg-primary/5",
                        )}
                        onClick={() => selectJob(job)}
                      >
                        <span className="flex min-w-0 items-center justify-between gap-2">
                          <span className="truncate font-mono text-xs">
                            {job.id}
                          </span>
                          <Badge variant="outline" className="shrink-0">
                            {job.steps.length}{" "}
                            {job.steps.length === 1 ? "step" : "steps"}
                          </Badge>
                        </span>
                        <span className="text-muted-foreground mt-1 block text-xs break-words">
                          {job.fields.needs.present
                            ? `Needs: ${job.fields.needs.value?.join(", ") || "explicit empty list"}`
                            : "No declared dependencies"}
                        </span>
                        {!job.editable ? (
                          <span className="mt-2 block text-xs text-amber-700 dark:text-amber-300">
                            Raw-only
                          </span>
                        ) : job.advanced_fields_present ? (
                          <span className="text-muted-foreground mt-2 block text-xs">
                            Advanced source preserved
                          </span>
                        ) : null}
                      </button>
                    </div>
                  ))
                )}
              </div>
              {newJobOpen ? (
                <div className="border-border grid gap-2 rounded-md border p-3">
                  <Label htmlFor="workflow-new-job-id">New job ID</Label>
                  <Input
                    id="workflow-new-job-id"
                    value={newJobID}
                    onChange={(event) => setNewJobID(event.target.value)}
                    className="font-mono text-xs"
                  />
                  {newJobIDError != null ? (
                    <p role="alert" className="text-destructive text-xs">
                      {newJobIDError}
                    </p>
                  ) : null}
                  <div className="grid min-w-0 gap-1.5">
                    <Label htmlFor="workflow-new-job-position">
                      Insertion position
                    </Label>
                    <Select
                      value={String(newJobInsertionIndex)}
                      disabled={!canMutate}
                      onValueChange={(value) =>
                        setNewJobIndex(
                          parseInsertionIndex(value, inspection.jobs.length),
                        )
                      }
                    >
                      <SelectTrigger
                        id="workflow-new-job-position"
                        aria-label="New job insertion position"
                        aria-describedby="workflow-new-job-position-help"
                        className="w-full min-w-0"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {Array.from(
                          { length: inspection.jobs.length + 1 },
                          (_, index) => (
                            <SelectItem key={index} value={String(index)}>
                              {insertionPositionLabel(
                                inspection.jobs,
                                index,
                                "job",
                              )}
                            </SelectItem>
                          ),
                        )}
                      </SelectContent>
                    </Select>
                    <p
                      id="workflow-new-job-position-help"
                      className="text-muted-foreground text-xs"
                    >
                      Choose any source-order boundary. Raw-only neighbors stay
                      unchanged.
                    </p>
                  </div>
                  <p className="text-muted-foreground text-xs">
                    New jobs start with <code>runs-on: picoclaw</code>. An empty
                    step list may remain invalid until you add an action.
                  </p>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      type="button"
                      size="sm"
                      disabled={!canMutate || newJobIDError != null || applying}
                      onClick={() =>
                        void applyOperation(
                          {
                            type: "job.insert",
                            job_id: newJobID,
                            index: newJobInsertionIndex,
                            fields: {
                              runs_on: { mode: "set", value: "picoclaw" },
                            },
                          },
                          { jobID: newJobID, stepIndex: null },
                        )
                      }
                    >
                      <IconPlus className="size-4" />
                      Add job to YAML
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setNewJobOpen(false)
                        setNewJobID("")
                        setNewJobIndex(0)
                      }}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={!canMutate || dirty}
                  onClick={() => {
                    setNewJobIndex(inspection.jobs.length)
                    setNewJobOpen(true)
                  }}
                >
                  <IconPlus className="size-4" />
                  Add job
                </Button>
              )}
            </aside>

            <main className="grid min-h-0 content-start gap-4 p-3 lg:overflow-y-auto">
              {selectedJob == null || jobDraft == null ? (
                <div className="text-muted-foreground rounded-md border border-dashed px-4 py-10 text-center text-sm">
                  Add or select a job to edit its actions.
                </div>
              ) : (
                <>
                  <section
                    aria-labelledby="workflow-selected-job-heading"
                    className="grid gap-3"
                  >
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0">
                        <h4
                          id="workflow-selected-job-heading"
                          className="text-sm font-medium"
                        >
                          Job{" "}
                          <code className="break-all">{selectedJob.id}</code>
                        </h4>
                        <p className="text-muted-foreground mt-1 text-xs">
                          Renaming changes only this YAML mapping key.
                          Dependency references are not rewritten; candidate
                          validation will surface stale <code>needs</code>{" "}
                          entries.
                        </p>
                      </div>
                      {deleteJobID === selectedJob.id ? (
                        <div
                          role="group"
                          aria-label={`Confirm delete job ${selectedJob.id}`}
                          className="flex flex-wrap gap-2"
                        >
                          <Button
                            type="button"
                            variant="destructive"
                            size="sm"
                            disabled={!canMutate}
                            onClick={() =>
                              void applyOperation({
                                type: "job.delete",
                                job_id: selectedJob.id,
                              })
                            }
                          >
                            Confirm delete
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => setDeleteJobID(null)}
                          >
                            Cancel
                          </Button>
                        </div>
                      ) : (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={
                            !canMutate || dirty || !selectedJob.editable
                          }
                          onClick={() => setDeleteJobID(selectedJob.id)}
                          aria-label={`Delete job ${selectedJob.id}`}
                        >
                          <IconTrash className="size-4" />
                          Delete job
                        </Button>
                      )}
                    </div>

                    {!selectedJob.editable ? (
                      <RawOnlyNotice
                        title={`Job ${selectedJob.id} is raw-only`}
                        reason={
                          selectedJob.reason ??
                          "This job uses source features that cannot be mutated safely."
                        }
                        onOpenYAML={onOpenYAML}
                      />
                    ) : (
                      <>
                        {selectedJob.advanced_fields_present ? (
                          <div className="text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs">
                            Advanced sibling fields are preserved in YAML while
                            known fields are edited.
                          </div>
                        ) : null}
                        {jobIDDraft != null ? (
                          <JobIDMutationField
                            currentID={selectedJob.id}
                            draft={jobIDDraft}
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            onChange={setJobIDDraft}
                          />
                        ) : null}
                        <FieldGrid>
                          <MutationField
                            id="workflow-job-name"
                            label={jobFieldLabels.name}
                            current={selectedJob.fields.name}
                            draft={jobDraft.name}
                            kind="string"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "name", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-runs-on"
                            label={jobFieldLabels.runs_on}
                            current={selectedJob.fields.runs_on}
                            draft={jobDraft.runs_on}
                            kind="string"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "runs_on", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-needs"
                            label={jobFieldLabels.needs}
                            current={selectedJob.fields.needs}
                            draft={jobDraft.needs}
                            kind="json"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            help="JSON array of job IDs. Set [] preserves an explicit empty list."
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "needs", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-uses"
                            label={jobFieldLabels.uses}
                            current={selectedJob.fields.uses}
                            draft={jobDraft.uses}
                            kind="string"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            help="Exact reusable workflow ref, such as workflows/child.yml."
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "uses", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-if"
                            label={jobFieldLabels.if}
                            current={selectedJob.fields.if}
                            draft={jobDraft.if}
                            kind="string"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "if", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-continue-on-error"
                            label={jobFieldLabels.continue_on_error}
                            current={selectedJob.fields.continue_on_error}
                            draft={jobDraft.continue_on_error}
                            kind="boolean"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(
                                  current,
                                  "continue_on_error",
                                  draft,
                                ),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-with"
                            label={jobFieldLabels.with}
                            current={selectedJob.fields.with}
                            draft={jobDraft.with}
                            kind="json"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            help="Bounded JSON object. Empty strings, false, zero, and null remain distinct values."
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "with", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-secrets"
                            label={jobFieldLabels.secrets}
                            current={selectedJob.fields.secrets}
                            draft={jobDraft.secrets}
                            kind="json-or-inherit"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            help="Enter inherit or a JSON object. Secrets are references, never resolved values."
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "secrets", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-outputs"
                            label={jobFieldLabels.outputs}
                            current={selectedJob.fields.outputs}
                            draft={jobDraft.outputs}
                            kind="json"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            help="JSON object whose values are workflow expressions."
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "outputs", draft),
                              )
                            }
                          />
                          <MutationField
                            id="workflow-job-context"
                            label={jobFieldLabels.context}
                            current={selectedJob.fields.context}
                            draft={jobDraft.context}
                            kind="json"
                            disabled={
                              jobFieldsDisabled || !selectedJob.editable
                            }
                            help='JSON object with optional "session" and "delivery" strings.'
                            onChange={(draft) =>
                              setJobDraft((current) =>
                                updateDraft(current, "context", draft),
                              )
                            }
                          />
                        </FieldGrid>
                      </>
                    )}
                  </section>

                  <section
                    aria-labelledby="workflow-job-steps-heading"
                    className="border-border grid gap-3 border-t pt-4"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div>
                        <h4
                          id="workflow-job-steps-heading"
                          className="text-sm font-medium"
                        >
                          Ordered actions
                        </h4>
                        <p className="text-muted-foreground text-xs">
                          Move controls preserve exact source order.
                        </p>
                      </div>
                      <Badge variant="outline">
                        {selectedJob.steps.length}
                      </Badge>
                    </div>
                    <div
                      role="list"
                      aria-label={`Actions in job ${selectedJob.id}`}
                      className="grid gap-2"
                    >
                      {selectedJob.steps.map((step) => {
                        const label = stepLabel(step)
                        return (
                          <div
                            key={step.index}
                            role="listitem"
                            className={cn(
                              "border-border grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-md border p-2",
                              selectedStepIndex === step.index &&
                                "border-primary bg-primary/5",
                            )}
                          >
                            <span className="text-muted-foreground w-5 text-right font-mono text-xs">
                              {step.index + 1}
                            </span>
                            <button
                              type="button"
                              className="min-w-0 text-left"
                              onClick={() => selectStep(step)}
                            >
                              <span className="block truncate text-xs font-medium">
                                {label}
                              </span>
                              <span className="text-muted-foreground block truncate font-mono text-[11px]">
                                {step.fields.uses.present
                                  ? step.fields.uses.value
                                  : "target absent"}
                              </span>
                            </button>
                            <div className="flex gap-1">
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon-sm"
                                aria-label={`Move action ${step.index + 1} up`}
                                disabled={
                                  !canMutate ||
                                  dirty ||
                                  !step.editable ||
                                  step.index === 0 ||
                                  !workflowStepMoveAllowed(
                                    selectedJob,
                                    step.index,
                                    step.index - 1,
                                  )
                                }
                                title={
                                  step.index > 0 &&
                                  !workflowStepMoveAllowed(
                                    selectedJob,
                                    step.index,
                                    step.index - 1,
                                  )
                                    ? "A raw-only action cannot be reordered indirectly."
                                    : undefined
                                }
                                onClick={() =>
                                  void applyOperation(
                                    {
                                      type: "step.move",
                                      job_id: selectedJob.id,
                                      step_index: step.index,
                                      to_index: step.index - 1,
                                    },
                                    {
                                      jobID: selectedJob.id,
                                      stepIndex: step.index - 1,
                                    },
                                  )
                                }
                              >
                                <IconArrowUp className="size-4" />
                              </Button>
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon-sm"
                                aria-label={`Move action ${step.index + 1} down`}
                                disabled={
                                  !canMutate ||
                                  dirty ||
                                  !step.editable ||
                                  step.index === selectedJob.steps.length - 1 ||
                                  !workflowStepMoveAllowed(
                                    selectedJob,
                                    step.index,
                                    step.index + 1,
                                  )
                                }
                                title={
                                  step.index < selectedJob.steps.length - 1 &&
                                  !workflowStepMoveAllowed(
                                    selectedJob,
                                    step.index,
                                    step.index + 1,
                                  )
                                    ? "A raw-only action cannot be reordered indirectly."
                                    : undefined
                                }
                                onClick={() =>
                                  void applyOperation(
                                    {
                                      type: "step.move",
                                      job_id: selectedJob.id,
                                      step_index: step.index,
                                      to_index: step.index + 1,
                                    },
                                    {
                                      jobID: selectedJob.id,
                                      stepIndex: step.index + 1,
                                    },
                                  )
                                }
                              >
                                <IconArrowDown className="size-4" />
                              </Button>
                            </div>
                          </div>
                        )
                      })}
                    </div>

                    {newStepOpen ? (
                      <div className="border-border grid gap-3 rounded-md border p-3">
                        <div className="grid gap-1.5">
                          <Label htmlFor="workflow-new-step-id">
                            Step ID (optional)
                          </Label>
                          <Input
                            id="workflow-new-step-id"
                            value={newStepID}
                            onChange={(event) =>
                              setNewStepID(event.target.value)
                            }
                            className="font-mono text-xs"
                          />
                          {newStepIDError != null ? (
                            <p
                              role="alert"
                              className="text-destructive text-xs"
                            >
                              {newStepIDError}
                            </p>
                          ) : null}
                        </div>
                        <WorkflowCapabilityTargetField
                          id="workflow-new-step-target"
                          value={newStepTarget}
                          disabled={!canMutate}
                          onChange={setNewStepTarget}
                        />
                        <div className="grid min-w-0 gap-1.5">
                          <Label htmlFor="workflow-new-step-position">
                            Insertion position
                          </Label>
                          <Select
                            value={String(newStepInsertionIndex)}
                            disabled={!canMutate}
                            onValueChange={(value) =>
                              setNewStepIndex(
                                parseInsertionIndex(
                                  value,
                                  selectedJob.steps.length,
                                ),
                              )
                            }
                          >
                            <SelectTrigger
                              id="workflow-new-step-position"
                              aria-label="New action insertion position"
                              aria-describedby="workflow-new-step-position-help"
                              className="w-full min-w-0"
                            >
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {Array.from(
                                { length: selectedJob.steps.length + 1 },
                                (_, index) => (
                                  <SelectItem key={index} value={String(index)}>
                                    {insertionPositionLabel(
                                      selectedJob.steps,
                                      index,
                                      "action",
                                    )}
                                  </SelectItem>
                                ),
                              )}
                            </SelectContent>
                          </Select>
                          <p
                            id="workflow-new-step-position-help"
                            className="text-muted-foreground text-xs"
                          >
                            Choose any source-order boundary. Raw-only actions
                            can remain immediately before or after the new
                            action.
                          </p>
                        </div>
                        <div className="flex flex-wrap gap-2">
                          <Button
                            type="button"
                            size="sm"
                            disabled={
                              !canMutate ||
                              newStepTarget.trim() === "" ||
                              newStepIDError != null
                            }
                            onClick={() =>
                              void applyOperation(
                                {
                                  type: "step.insert",
                                  job_id: selectedJob.id,
                                  index: newStepInsertionIndex,
                                  fields: {
                                    ...(newStepID !== ""
                                      ? {
                                          id: {
                                            mode: "set" as const,
                                            value: newStepID,
                                          },
                                        }
                                      : {}),
                                    uses: {
                                      mode: "set",
                                      value: newStepTarget,
                                    },
                                  },
                                },
                                {
                                  jobID: selectedJob.id,
                                  stepIndex: newStepInsertionIndex,
                                },
                              )
                            }
                          >
                            <IconPlus className="size-4" />
                            Add action to YAML
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => {
                              setNewStepOpen(false)
                              setNewStepID("")
                              setNewStepTarget("")
                              setNewStepIndex(0)
                            }}
                          >
                            Cancel
                          </Button>
                        </div>
                      </div>
                    ) : (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={!canMutate || dirty}
                        onClick={() => {
                          setNewStepIndex(selectedJob.steps.length)
                          setNewStepOpen(true)
                        }}
                      >
                        <IconPlus className="size-4" />
                        Add action
                      </Button>
                    )}
                  </section>

                  {selectedStep != null && stepDraft != null ? (
                    <section
                      aria-labelledby="workflow-selected-step-heading"
                      className="border-border grid gap-3 border-t pt-4"
                    >
                      <div className="flex flex-wrap items-start justify-between gap-2">
                        <div>
                          <h4
                            id="workflow-selected-step-heading"
                            className="text-sm font-medium"
                          >
                            Action {selectedStep.index + 1}
                          </h4>
                          <p className="text-muted-foreground text-xs">
                            {stepLabel(selectedStep)}
                          </p>
                        </div>
                        {deleteStepIndex === selectedStep.index ? (
                          <div
                            role="group"
                            aria-label={`Confirm delete action ${selectedStep.index + 1}`}
                            className="flex flex-wrap gap-2"
                          >
                            <Button
                              type="button"
                              variant="destructive"
                              size="sm"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              onClick={() =>
                                void applyOperation({
                                  type: "step.delete",
                                  job_id: selectedJob.id,
                                  step_index: selectedStep.index,
                                })
                              }
                            >
                              Confirm delete
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => setDeleteStepIndex(null)}
                            >
                              Cancel
                            </Button>
                          </div>
                        ) : (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            disabled={
                              !canMutate || dirty || !selectedStep.editable
                            }
                            onClick={() =>
                              setDeleteStepIndex(selectedStep.index)
                            }
                            aria-label={`Delete action ${selectedStep.index + 1}`}
                          >
                            <IconTrash className="size-4" />
                            Delete action
                          </Button>
                        )}
                      </div>

                      {!selectedStep.editable ? (
                        <RawOnlyNotice
                          title={`Action ${selectedStep.index + 1} is raw-only`}
                          reason={
                            selectedStep.reason ??
                            "This action uses source features that cannot be mutated safely."
                          }
                          onOpenYAML={onOpenYAML}
                        />
                      ) : (
                        <>
                          {selectedStep.advanced_fields_present ? (
                            <div className="text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs">
                              Advanced sibling fields are preserved in YAML
                              while known fields are edited.
                            </div>
                          ) : null}
                          <FieldGrid>
                            <MutationField
                              id="workflow-step-id"
                              label={stepFieldLabels.id}
                              current={selectedStep.fields.id}
                              draft={stepDraft.id}
                              kind="string"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(current, "id", draft),
                                )
                              }
                            />
                            <MutationField
                              id="workflow-step-name"
                              label={stepFieldLabels.name}
                              current={selectedStep.fields.name}
                              draft={stepDraft.name}
                              kind="string"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(current, "name", draft),
                                )
                              }
                            />
                            <TargetMutationField
                              id="workflow-step-uses"
                              current={selectedStep.fields.uses}
                              draft={stepDraft.uses}
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(current, "uses", draft),
                                )
                              }
                            />
                            <MutationField
                              id="workflow-step-if"
                              label={stepFieldLabels.if}
                              current={selectedStep.fields.if}
                              draft={stepDraft.if}
                              kind="string"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(current, "if", draft),
                                )
                              }
                            />
                            <MutationField
                              id="workflow-step-continue-on-error"
                              label={stepFieldLabels.continue_on_error}
                              current={selectedStep.fields.continue_on_error}
                              draft={stepDraft.continue_on_error}
                              kind="boolean"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(
                                    current,
                                    "continue_on_error",
                                    draft,
                                  ),
                                )
                              }
                            />
                            <MutationField
                              id="workflow-step-with"
                              label={stepFieldLabels.with}
                              current={selectedStep.fields.with}
                              draft={stepDraft.with}
                              kind="json"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              help="Bounded JSON object. Empty strings, false, zero, and null remain distinct values."
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(current, "with", draft),
                                )
                              }
                            />
                            <MutationField
                              id="workflow-step-context"
                              label={stepFieldLabels.context}
                              current={selectedStep.fields.context}
                              draft={stepDraft.context}
                              kind="json"
                              disabled={
                                stepFieldsDisabled || !selectedStep.editable
                              }
                              help='JSON object with optional "session" and "delivery" strings.'
                              onChange={(draft) =>
                                setStepDraft((current) =>
                                  updateDraft(current, "context", draft),
                                )
                              }
                            />
                          </FieldGrid>
                        </>
                      )}
                    </section>
                  ) : null}
                </>
              )}

              {selectionError ? (
                <div role="alert" className="text-destructive text-xs">
                  {selectionError}
                </div>
              ) : null}
              {patchResult.errors.length > 0 ? (
                <div
                  role="alert"
                  className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-2 text-xs"
                >
                  {patchResult.errors.join(" ")}
                </div>
              ) : null}
              {jobIDError != null ? (
                <div role="alert" className="text-destructive text-xs">
                  {jobIDError}
                </div>
              ) : null}
              {applyError ? (
                <div
                  role="alert"
                  className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-2 text-xs"
                >
                  {applyError}
                </div>
              ) : null}
              <ValidationSummary
                validation={candidateValidation ?? inspection.validation}
              />
            </main>
          </div>

          <div className="border-border flex flex-wrap items-center justify-between gap-2 border-t p-3">
            <p className="text-muted-foreground text-xs">
              {applying
                ? "Applying one revision-fenced YAML operation…"
                : dirty
                  ? "Pending structured changes have not touched YAML."
                  : "Structured editor is synchronized with the current YAML."}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!dirty || applying}
                onClick={resetForm}
              >
                <IconX className="size-4" />
                Reset pending
              </Button>
              <Button
                type="button"
                size="sm"
                disabled={!canApply}
                onClick={applyPatch}
              >
                <IconCheck className="size-4" />
                {applying ? "Applying" : "Apply fields to YAML"}
              </Button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}

function FieldGrid({ children }: { children: React.ReactNode }) {
  return <div className="grid gap-3 xl:grid-cols-2">{children}</div>
}

function JobIDMutationField({
  currentID,
  draft,
  disabled,
  onChange,
}: {
  currentID: string
  draft: FieldDraft
  disabled: boolean
  onChange: (draft: FieldDraft) => void
}) {
  return (
    <div className="border-border grid gap-2 rounded-md border p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <Label htmlFor="workflow-job-id-mode">Job ID</Label>
          <p className="text-muted-foreground mt-1 font-mono text-xs break-all">
            Source: {currentID}
          </p>
        </div>
        <Select
          value={draft.mode}
          disabled={disabled}
          onValueChange={(mode) =>
            onChange({ ...draft, mode: mode as "keep" | "set" })
          }
        >
          <SelectTrigger
            id="workflow-job-id-mode"
            aria-label="Job ID mutation"
            className="w-32"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="keep">Keep source</SelectItem>
            <SelectItem value="set">Rename</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {draft.mode === "set" ? (
        <Input
          aria-label="New job ID"
          value={draft.value}
          disabled={disabled}
          onChange={(event) =>
            onChange({ ...draft, value: event.target.value })
          }
          className="font-mono text-xs"
        />
      ) : null}
      <p className="text-muted-foreground text-xs">
        Rename is collision-safe and changes only the mapping key. Update any
        <code> needs</code> references separately.
      </p>
    </div>
  )
}

function MutationField<Value>({
  id,
  label,
  current,
  draft,
  kind,
  disabled,
  help,
  onChange,
}: {
  id: string
  label: string
  current: WorkflowEditorField<Value>
  draft: FieldDraft
  kind: "string" | "boolean" | "json" | "json-or-inherit"
  disabled: boolean
  help?: string
  onChange: (draft: FieldDraft) => void
}) {
  return (
    <div className="border-border grid min-w-0 gap-2 rounded-md border p-3">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <Label htmlFor={`${id}-mode`}>{label}</Label>
          <p className="text-muted-foreground mt-1 text-xs break-words">
            Source: {fieldSummary(current)}
          </p>
        </div>
        <Select
          value={draft.mode}
          disabled={disabled}
          onValueChange={(mode) =>
            onChange({ ...draft, mode: mode as MutationMode })
          }
        >
          <SelectTrigger
            id={`${id}-mode`}
            aria-label={`${label} mutation`}
            className="w-32"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="keep">Keep source</SelectItem>
            <SelectItem value="set">Set value</SelectItem>
            <SelectItem value="remove">Remove</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {draft.mode === "set" ? (
        kind === "boolean" ? (
          <div className="flex items-center justify-between gap-3">
            <Label htmlFor={`${id}-value`}>Explicit value</Label>
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground text-xs">
                {draft.value === "true" ? "true" : "false"}
              </span>
              <Switch
                id={`${id}-value`}
                checked={draft.value === "true"}
                disabled={disabled}
                onCheckedChange={(checked) =>
                  onChange({ ...draft, value: checked ? "true" : "false" })
                }
              />
            </div>
          </div>
        ) : kind === "string" ? (
          <Input
            id={`${id}-value`}
            aria-label={`${label} value`}
            value={draft.value}
            disabled={disabled}
            onChange={(event) =>
              onChange({ ...draft, value: event.target.value })
            }
            className="font-mono text-xs"
          />
        ) : (
          <Textarea
            id={`${id}-value`}
            aria-label={`${label} value`}
            value={draft.value}
            disabled={disabled}
            spellCheck={false}
            onChange={(event) =>
              onChange({ ...draft, value: event.target.value })
            }
            className="min-h-24 resize-y font-mono text-xs"
          />
        )
      ) : draft.mode === "remove" ? (
        <p className="text-muted-foreground text-xs">
          The key will be removed. Existing false or empty values are not
          treated as absent.
        </p>
      ) : null}
      {help ? <p className="text-muted-foreground text-xs">{help}</p> : null}
    </div>
  )
}

function TargetMutationField({
  id,
  current,
  draft,
  disabled,
  onChange,
}: {
  id: string
  current: WorkflowEditorField<string>
  draft: FieldDraft
  disabled: boolean
  onChange: (draft: FieldDraft) => void
}) {
  return (
    <div className="border-border grid min-w-0 gap-2 rounded-md border p-3">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <Label htmlFor={`${id}-mode`}>Action target</Label>
          <p className="text-muted-foreground mt-1 text-xs break-all">
            Source: {fieldSummary(current)}
          </p>
        </div>
        <Select
          value={draft.mode}
          disabled={disabled}
          onValueChange={(mode) =>
            onChange({ ...draft, mode: mode as MutationMode })
          }
        >
          <SelectTrigger
            id={`${id}-mode`}
            aria-label="Action target mutation"
            className="w-32"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="keep">Keep source</SelectItem>
            <SelectItem value="set">Set value</SelectItem>
            <SelectItem value="remove">Remove</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {draft.mode === "set" ? (
        <WorkflowCapabilityTargetField
          id={`${id}-value`}
          value={draft.value}
          disabled={disabled}
          onChange={(value) => onChange({ ...draft, value })}
        />
      ) : draft.mode === "remove" ? (
        <p className="text-muted-foreground text-xs">
          The action target key will be removed. The candidate YAML may remain
          invalid until another target is set.
        </p>
      ) : null}
    </div>
  )
}

function RawOnlyNotice({
  title,
  reason,
  onOpenYAML,
}: {
  title: string
  reason: string
  onOpenYAML: () => void
}) {
  return (
    <div
      role="alert"
      className="grid gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs"
    >
      <div className="flex items-start gap-2">
        <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
        <div>
          <div className="font-medium">{title}</div>
          <p className="text-muted-foreground mt-1 break-words">{reason}</p>
        </div>
      </div>
      <Button type="button" variant="outline" size="sm" onClick={onOpenYAML}>
        <IconCode className="size-4" />
        Open Workflow YAML
      </Button>
    </div>
  )
}

function EditorError({
  message,
  onRetry,
  onOpenYAML,
}: {
  message: string
  onRetry: () => void
  onOpenYAML: () => void
}) {
  return (
    <div
      role="alert"
      className="border-destructive/30 bg-destructive/5 m-4 grid gap-3 rounded-md border p-4"
    >
      <div className="flex items-start gap-2">
        <IconAlertTriangle className="text-destructive mt-0.5 size-4 shrink-0" />
        <div>
          <div className="font-medium">Jobs and actions are unavailable</div>
          <p className="text-muted-foreground mt-1 text-xs break-words">
            {message}
          </p>
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onRetry}>
          <IconRefresh className="size-4" />
          Retry inspection
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={onOpenYAML}>
          <IconCode className="size-4" />
          Open Workflow YAML
        </Button>
      </div>
    </div>
  )
}

function ValidationSummary({
  validation,
}: {
  validation: WorkflowDevelopmentValidation
}) {
  const issues = [
    ...(validation.errors ?? []).map((issue) => ({
      ...issue,
      level: "Error",
    })),
    ...(validation.warnings ?? []).map((issue) => ({
      ...issue,
      level: "Warning",
    })),
  ]
  return (
    <section
      aria-labelledby="workflow-job-editor-validation"
      className="border-border grid gap-2 rounded-md border p-3"
    >
      <div className="flex items-center justify-between gap-2">
        <h4 id="workflow-job-editor-validation" className="text-sm font-medium">
          Candidate validation
        </h4>
        <Badge variant={validation.valid ? "default" : "destructive"}>
          {validation.valid ? "Valid" : "Needs fixes"}
        </Badge>
      </div>
      {issues.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No validation diagnostics for this exact YAML revision.
        </p>
      ) : (
        <ul className="grid gap-1.5">
          {issues.map((issue, index) => (
            <li key={`${issue.level}:${issue.path ?? ""}:${index}`}>
              <span className="font-medium">{issue.level}:</span>{" "}
              {issue.path ? (
                <code className="break-all">{issue.path}</code>
              ) : null}{" "}
              <span className="break-words">{issue.message}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function jobDraftFromProjection(job: WorkflowJobEditorJob): JobDraft {
  return Object.fromEntries(
    Object.entries(job.fields).map(([key, field]) => [
      key,
      fieldDraft(field, key as keyof WorkflowJobEditorJobFields),
    ]),
  ) as JobDraft
}

function stepDraftFromProjection(step: WorkflowJobEditorStep): StepDraft {
  return Object.fromEntries(
    Object.entries(step.fields).map(([key, field]) => [
      key,
      fieldDraft(field, key as keyof WorkflowJobEditorStepFields),
    ]),
  ) as StepDraft
}

function fieldDraft(
  field: WorkflowEditorField<unknown>,
  key: keyof WorkflowJobEditorJobFields | keyof WorkflowJobEditorStepFields,
): FieldDraft {
  return {
    mode: "keep",
    value:
      field.present && field.value != null
        ? fieldValueText(field.value)
        : defaultFieldValue(key),
  }
}

function defaultFieldValue(
  key: keyof WorkflowJobEditorJobFields | keyof WorkflowJobEditorStepFields,
) {
  switch (key) {
    case "continue_on_error":
      return "false"
    case "needs":
      return "[]"
    case "with":
    case "outputs":
    case "context":
      return "{}"
    case "secrets":
      return "inherit"
    default:
      return ""
  }
}

function fieldValueText(value: unknown) {
  if (typeof value === "string") {
    return value
  }
  if (typeof value === "boolean") {
    return String(value)
  }
  return JSON.stringify(value, null, 2)
}

function fieldSummary(field: WorkflowEditorField<unknown>) {
  if (!field.present) {
    return "(absent)"
  }
  if (field.value === "") {
    return "(empty string)"
  }
  const text = fieldValueText(field.value)
  return text.length > 120 ? `${text.slice(0, 117)}…` : text
}

function fieldDraftsDirty(draft: Record<string, FieldDraft>) {
  return Object.values(draft).some((field) => field.mode !== "keep")
}

function updateDraft<Draft extends Record<string, FieldDraft>>(
  current: Draft | null,
  key: keyof Draft,
  value: FieldDraft,
) {
  return current == null ? current : { ...current, [key]: value }
}

function jobPatchFromDraft(draft: JobDraft) {
  const fields: WorkflowJobEditorJobPatch = {}
  const errors: string[] = []
  assignMutation(
    fields,
    "name",
    stringMutation(draft.name, "Display name", true),
    errors,
  )
  assignMutation(
    fields,
    "runs_on",
    stringMutation(draft.runs_on, "Runs on", true),
    errors,
  )
  assignMutation(
    fields,
    "needs",
    jsonMutation(
      draft.needs,
      isJobIDReferenceArray,
      "Dependencies must be a JSON array of non-empty, single-line job IDs no larger than 256 UTF-8 bytes.",
    ),
    errors,
  )
  assignMutation(
    fields,
    "uses",
    stringMutation(draft.uses, "Reusable workflow", false),
    errors,
  )
  assignMutation(
    fields,
    "if",
    stringMutation(draft.if, "Job condition", true),
    errors,
  )
  assignMutation(
    fields,
    "continue_on_error",
    booleanMutation(draft.continue_on_error),
    errors,
  )
  assignMutation(
    fields,
    "with",
    jsonMutation(
      draft.with,
      isJSONObject,
      "Reusable inputs must be a JSON object.",
    ),
    errors,
  )
  assignMutation(fields, "secrets", secretsMutation(draft.secrets), errors)
  assignMutation(
    fields,
    "outputs",
    jsonMutation(
      draft.outputs,
      isStringRecord,
      "Job outputs must be a JSON object of string values.",
    ),
    errors,
  )
  assignMutation(
    fields,
    "context",
    jsonMutation(
      draft.context,
      isRunContext,
      "Run context accepts only optional string session and delivery fields.",
    ),
    errors,
  )
  return { fields, errors }
}

function stepPatchFromDraft(draft: StepDraft) {
  const fields: WorkflowJobEditorStepPatch = {}
  const errors: string[] = []
  assignMutation(fields, "id", idReferenceMutation(draft.id, "Step ID"), errors)
  assignMutation(
    fields,
    "name",
    stringMutation(draft.name, "Display name", true),
    errors,
  )
  assignMutation(
    fields,
    "uses",
    stringMutation(draft.uses, "Action target", false),
    errors,
  )
  assignMutation(
    fields,
    "if",
    stringMutation(draft.if, "Step condition", true),
    errors,
  )
  assignMutation(
    fields,
    "continue_on_error",
    booleanMutation(draft.continue_on_error),
    errors,
  )
  assignMutation(
    fields,
    "with",
    jsonMutation(
      draft.with,
      isJSONObject,
      "Action inputs must be a JSON object.",
    ),
    errors,
  )
  assignMutation(
    fields,
    "context",
    jsonMutation(
      draft.context,
      isRunContext,
      "Run context accepts only optional string session and delivery fields.",
    ),
    errors,
  )
  return { fields, errors }
}

type MutationResult<Value> =
  | { mutation?: WorkflowEditorFieldMutation<Value> }
  | { error: string }

function stringMutation(
  draft: FieldDraft,
  label: string,
  multiline: boolean,
): MutationResult<string> {
  if (draft.mode === "keep") {
    return {}
  }
  if (draft.mode === "remove") {
    return { mutation: { mode: "remove" } }
  }
  return workflowEditorFieldStringSafe(draft.value, multiline)
    ? { mutation: { mode: "set", value: draft.value } }
    : {
        error: `${label} must be valid text no larger than 16 KiB without unsupported control characters.`,
      }
}

function idReferenceMutation(
  draft: FieldDraft,
  label: string,
): MutationResult<string> {
  if (draft.mode === "keep") {
    return {}
  }
  if (draft.mode === "remove") {
    return { mutation: { mode: "remove" } }
  }
  const error = workflowEditorIDError(draft.value, label, true)
  return error == null
    ? { mutation: { mode: "set", value: draft.value } }
    : { error }
}

function workflowEditorIDError(
  value: string,
  label: string,
  allowEmpty: boolean,
  requireTrimmedValue = false,
) {
  if (!allowEmpty && value === "") {
    return `${label} cannot be empty.`
  }
  if (requireTrimmedValue && value.trim() !== value) {
    return `${label} must not have leading or trailing whitespace.`
  }
  if (
    new TextEncoder().encode(value).byteLength > 256 ||
    /[\p{Cc}\p{Cf}\p{Cs}]/u.test(value)
  ) {
    return `${label} must be a single-line value no larger than 256 UTF-8 bytes.`
  }
  return null
}

function booleanMutation(draft: FieldDraft): MutationResult<boolean> {
  if (draft.mode === "keep") {
    return {}
  }
  if (draft.mode === "remove") {
    return { mutation: { mode: "remove" } }
  }
  return {
    mutation: { mode: "set", value: draft.value === "true" },
  }
}

function jsonMutation<Value>(
  draft: FieldDraft,
  valid: (value: unknown) => value is Value,
  error: string,
): MutationResult<Value> {
  if (draft.mode === "keep") {
    return {}
  }
  if (draft.mode === "remove") {
    return { mutation: { mode: "remove" } }
  }
  try {
    const value = parseStrictWorkflowEditorJSON(draft.value)
    return valid(value) ? { mutation: { mode: "set", value } } : { error }
  } catch (parseError: unknown) {
    return {
      error:
        parseError instanceof Error &&
        parseError.message === "duplicate object member"
          ? `${error} Duplicate JSON object keys are not allowed.`
          : error,
    }
  }
}

function secretsMutation(
  draft: FieldDraft,
): MutationResult<"inherit" | Record<string, WorkflowEditorJSONValue>> {
  if (draft.mode === "keep") {
    return {}
  }
  if (draft.mode === "remove") {
    return { mutation: { mode: "remove" } }
  }
  if (draft.value.trim() === "inherit") {
    return { mutation: { mode: "set", value: "inherit" } }
  }
  return jsonMutation(
    draft,
    isJSONObject,
    'Reusable secrets must be "inherit" or a JSON object.',
  )
}

function assignMutation<
  Fields extends Record<string, unknown>,
  Key extends keyof Fields,
>(fields: Fields, key: Key, result: MutationResult<unknown>, errors: string[]) {
  if ("error" in result) {
    errors.push(result.error)
    return
  }
  if (result.mutation != null) {
    fields[key] = result.mutation as Fields[Key]
  }
}

function isJobIDReferenceArray(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.every(
      (item) =>
        typeof item === "string" &&
        item.trim() !== "" &&
        item.trim() === item &&
        workflowEditorIDError(item, "Job ID", true) == null,
    )
  )
}

function isJSONObject(
  value: unknown,
): value is Record<string, WorkflowEditorJSONValue> {
  return (
    value != null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    isJSONValue(value)
  )
}

function isJSONValue(
  value: unknown,
  depth = 0,
): value is WorkflowEditorJSONValue {
  if (depth > 16) {
    return false
  }
  if (value == null || typeof value === "boolean") {
    return true
  }
  if (typeof value === "string") {
    return workflowEditorFieldStringSafe(value, true)
  }
  if (typeof value === "number") {
    return (
      Number.isFinite(value) &&
      (!Number.isInteger(value) || Number.isSafeInteger(value))
    )
  }
  if (Array.isArray(value)) {
    return value.every((item) => isJSONValue(item, depth + 1))
  }
  return (
    typeof value === "object" &&
    Object.entries(value).every(
      ([key, item]) =>
        workflowEditorJSONKeySafe(key) && isJSONValue(item, depth + 1),
    )
  )
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return (
    value != null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.entries(value).every(
      ([key, item]) =>
        workflowEditorJSONKeySafe(key) &&
        typeof item === "string" &&
        workflowEditorFieldStringSafe(item, true),
    )
  )
}

function isRunContext(value: unknown): value is WorkflowJobEditorContext {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return (
    Object.keys(record).every((key) => ["session", "delivery"].includes(key)) &&
    Object.values(record).every(
      (item) =>
        typeof item === "string" && workflowEditorFieldStringSafe(item, true),
    )
  )
}

function workflowEditorFieldStringSafe(value: string, multiline: boolean) {
  if (
    hasUnpairedSurrogate(value) ||
    new TextEncoder().encode(value).byteLength > 16 << 10 ||
    (!multiline && /[\r\n]/u.test(value))
  ) {
    return false
  }
  for (const character of value) {
    if (/[\p{Cf}\p{Cs}]/u.test(character)) {
      return false
    }
    if (
      /\p{Cc}/u.test(character) &&
      character !== "\t" &&
      character !== "\n" &&
      character !== "\r"
    ) {
      return false
    }
  }
  return true
}

function workflowEditorJSONKeySafe(value: string) {
  return (
    value !== "" &&
    !hasUnpairedSurrogate(value) &&
    new TextEncoder().encode(value).byteLength <= 256 &&
    !/[\p{Cc}\p{Cf}\p{Cs}]/u.test(value)
  )
}

function parseStrictWorkflowEditorJSON(source: string): unknown {
  const encoder = new TextEncoder()
  if (encoder.encode(source).byteLength > 256 << 10) {
    throw new Error("value too large")
  }
  let index = 0
  let nodes = 0

  const whitespace = () => {
    while (
      source[index] === " " ||
      source[index] === "\t" ||
      source[index] === "\n" ||
      source[index] === "\r"
    ) {
      index += 1
    }
  }

  const string = (maximumBytes: number, key = false) => {
    if (source[index] !== '"') {
      throw new Error("expected string")
    }
    const start = index
    index += 1
    let escaped = false
    while (index < source.length) {
      const character = source[index]
      if (!escaped && character === '"') {
        index += 1
        const parsed = JSON.parse(source.slice(start, index)) as unknown
        if (
          typeof parsed !== "string" ||
          encoder.encode(parsed).byteLength > maximumBytes ||
          hasUnpairedSurrogate(parsed) ||
          (key
            ? !workflowEditorJSONKeySafe(parsed)
            : !workflowEditorFieldStringSafe(parsed, true))
        ) {
          throw new Error("invalid string")
        }
        return parsed
      }
      if (!escaped && character === "\\") {
        escaped = true
        index += 1
        continue
      }
      if (escaped) {
        escaped = false
      } else if ((character.codePointAt(0) ?? 0) < 32) {
        throw new Error("invalid string")
      }
      index += 1
    }
    throw new Error("unterminated string")
  }

  const value = (depth: number): unknown => {
    whitespace()
    nodes += 1
    if (depth > 16 || nodes > 4096) {
      throw new Error("too deep")
    }
    const character = source[index]
    if (character === '"') {
      return string(16 << 10)
    }
    if (character === "{") {
      index += 1
      whitespace()
      const members: Array<[string, unknown]> = []
      const names = new Set<string>()
      if (source[index] === "}") {
        index += 1
        return {}
      }
      while (index < source.length) {
        whitespace()
        const name = string(256, true)
        if (names.has(name)) {
          throw new Error("duplicate object member")
        }
        names.add(name)
        whitespace()
        if (source[index] !== ":") {
          throw new Error("expected colon")
        }
        index += 1
        members.push([name, value(depth + 1)])
        whitespace()
        if (source[index] === "}") {
          index += 1
          return Object.fromEntries(members)
        }
        if (source[index] !== ",") {
          throw new Error("expected comma")
        }
        index += 1
      }
      throw new Error("unterminated object")
    }
    if (character === "[") {
      index += 1
      whitespace()
      const items: unknown[] = []
      if (source[index] === "]") {
        index += 1
        return items
      }
      while (index < source.length) {
        items.push(value(depth + 1))
        whitespace()
        if (source[index] === "]") {
          index += 1
          return items
        }
        if (source[index] !== ",") {
          throw new Error("expected comma")
        }
        index += 1
      }
      throw new Error("unterminated array")
    }
    for (const [literal, parsed] of [
      ["true", true],
      ["false", false],
      ["null", null],
    ] as const) {
      if (source.startsWith(literal, index)) {
        index += literal.length
        return parsed
      }
    }
    const numberMatch = source
      .slice(index)
      .match(/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/)
    if (numberMatch == null) {
      throw new Error("invalid value")
    }
    index += numberMatch[0].length
    const parsed = Number(numberMatch[0])
    if (!workflowJSONNumberIsBrowserSafe(numberMatch[0])) {
      throw new Error("unsafe number")
    }
    return parsed
  }

  const parsed = value(0)
  whitespace()
  if (index !== source.length) {
    throw new Error("trailing input")
  }
  return parsed
}

function hasUnpairedSurrogate(value: string) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const following = value.charCodeAt(index + 1)
      if (!(following >= 0xdc00 && following <= 0xdfff)) {
        return true
      }
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true
    }
  }
  return false
}

function workflowDraftActionReview(
  inspection: WorkflowJobsInspection,
): WorkflowDraftActionReview {
  const targets: string[] = []
  const seenTargets = new Set<string>()
  let stepCount = 0
  let rawOnlyCount = inspection.editable ? 0 : 1
  for (const job of inspection.jobs) {
    if (!job.editable || job.advanced_fields_present) {
      rawOnlyCount += 1
    }
    if (
      job.fields.uses.present &&
      job.fields.uses.value != null &&
      !seenTargets.has(job.fields.uses.value)
    ) {
      seenTargets.add(job.fields.uses.value)
      targets.push(job.fields.uses.value)
    }
    for (const step of job.steps) {
      stepCount += 1
      if (!step.editable || step.advanced_fields_present) {
        rawOnlyCount += 1
      }
      if (
        step.fields.uses.present &&
        step.fields.uses.value != null &&
        !seenTargets.has(step.fields.uses.value)
      ) {
        seenTargets.add(step.fields.uses.value)
        targets.push(step.fields.uses.value)
      }
    }
  }
  if (!inspection.complete) {
    rawOnlyCount += 1
  }
  return {
    jobCount: inspection.jobs.length,
    stepCount,
    targets,
    rawOnlyCount,
  }
}

function preferredSelection(
  result: WorkflowJobsInspection,
  operation: WorkflowJobEditorOperation,
  preferred?: { jobID?: string | null; stepIndex?: number | null },
) {
  if (preferred != null) {
    return preferred
  }
  switch (operation.type) {
    case "job.insert":
      return { jobID: operation.job_id, stepIndex: null }
    case "job.delete": {
      const job =
        result.jobs.find((candidate) => candidate.id !== operation.job_id) ??
        null
      return { jobID: job?.id ?? null, stepIndex: job?.steps[0]?.index ?? null }
    }
    case "step.insert":
      return { jobID: operation.job_id, stepIndex: operation.index }
    case "step.delete":
      return {
        jobID: operation.job_id,
        stepIndex: Math.max(0, operation.step_index - 1),
      }
    case "step.move":
      return { jobID: operation.job_id, stepIndex: operation.to_index }
    case "job.patch":
      return { jobID: operation.job_id }
    case "step.patch":
      return { jobID: operation.job_id, stepIndex: operation.step_index }
  }
}

function stepLabel(step: WorkflowJobEditorStep) {
  if (step.fields.name.present && step.fields.name.value !== "") {
    return step.fields.name.value ?? `Action ${step.index + 1}`
  }
  if (step.fields.id.present && step.fields.id.value !== "") {
    return `#${step.fields.id.value}`
  }
  return `Action ${step.index + 1}`
}

function boundedInsertionIndex(index: number, length: number) {
  if (!Number.isSafeInteger(index)) {
    return length
  }
  return Math.min(Math.max(index, 0), length)
}

function parseInsertionIndex(value: string, length: number) {
  return boundedInsertionIndex(Number(value), length)
}

function insertionPositionLabel(
  items: readonly { editable: boolean }[],
  index: number,
  itemName: "job" | "action",
) {
  const before = items[index]
  const after = items[index - 1]
  const rawOnly = (item: { editable: boolean } | undefined) =>
    item?.editable === false ? " (raw-only)" : ""

  if (before == null && after == null) {
    return `First ${itemName} (append)`
  }
  if (after == null) {
    return `Before ${itemName} 1${rawOnly(before)}`
  }
  if (before == null) {
    return `After ${itemName} ${index}${rawOnly(after)} (append)`
  }
  return `After ${itemName} ${index}${rawOnly(after)}; before ${itemName} ${index + 1}${rawOnly(before)}`
}

function workflowStepMoveAllowed(
  job: WorkflowJobEditorJob,
  fromIndex: number,
  toIndex: number,
) {
  const start = Math.min(fromIndex, toIndex)
  const end = Math.max(fromIndex, toIndex)
  return job.steps.slice(start, end + 1).every((step) => step.editable)
}

function limitLabel(limit: string) {
  return limit.replaceAll("_", " ")
}

function errorMessage(error: unknown) {
  return error instanceof Error && error.message.trim() !== ""
    ? error.message
    : "The jobs and actions editor is unavailable."
}
