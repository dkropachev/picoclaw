import {
  IconAlertTriangle,
  IconCheck,
  IconCode,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"

import {
  WorkflowAPIError,
  type WorkflowCallTrigger,
  type WorkflowChannelMessageTrigger,
  type WorkflowCommandTrigger,
  type WorkflowDevelopmentValidation,
  type WorkflowEventEntityTrigger,
  type WorkflowEventTrigger,
  type WorkflowInputDefinition,
  type WorkflowRuntimeEventTrigger,
  type WorkflowTriggerKind,
  type WorkflowTriggerValueMap,
  type WorkflowTriggersInspection,
  inspectWorkflowTriggers,
  renderWorkflowTrigger,
  workflowTriggerKinds,
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

import { workflowJSONNumberIsBrowserSafe } from "./workflow-json-number"

export interface WorkflowTriggerInspectionState {
  yaml: string
  status: "loading" | "ready" | "error"
  eventTriggerPresent: boolean
  inspection?: WorkflowTriggersInspection
  reason?: string
}

export interface WorkflowTriggerEditorActivity {
  dirty: boolean
  applying: boolean
  conflict: boolean
}

interface ExternalYAMLConflict {
  yaml: string
  inspection?: WorkflowTriggersInspection
  error?: string
}

const idleWorkflowTriggerEditorActivity: WorkflowTriggerEditorActivity = {
  dirty: false,
  applying: false,
  conflict: false,
}

const triggerLabels: Record<WorkflowTriggerKind, string> = {
  manual: "Manual",
  schedule: "Schedule",
  channel_message: "Channel message",
  command: "Command",
  runtime_event: "Runtime event",
  event: "Durable event",
  workflow_call: "Workflow call",
}

const triggerDescriptions: Record<WorkflowTriggerKind, string> = {
  manual: "Allow this workflow to be started explicitly by an operator.",
  schedule: "Start this workflow from one or more validated cron schedules.",
  channel_message:
    "Match inbound chat messages and choose their conversation context.",
  command:
    "Bind a slash-style command and its typed arguments to this workflow.",
  runtime_event:
    "Match internal PicoClaw lifecycle events using deterministic filters.",
  event:
    "Match normalized GitHub, chat, email, or webhook events before actions run.",
  workflow_call:
    "Declare typed inputs, secrets, and outputs for reusable and manual calls.",
}

export function WorkflowTriggerEditor({
  yaml,
  disabled,
  onYAMLChange,
  onInspectionChange,
  onActivityChange,
  onOpenYAML,
}: {
  yaml: string
  disabled: boolean
  onYAMLChange: (yaml: string) => void
  onInspectionChange: (state: WorkflowTriggerInspectionState) => void
  onActivityChange: (activity: WorkflowTriggerEditorActivity) => void
  onOpenYAML: () => void
}) {
  const latestYAMLRef = useRef(yaml)
  const latestInspectionRef = useRef<WorkflowTriggersInspection | null>(null)
  const inspectionYAMLRef = useRef<string | null>(null)
  const selectedKindRef = useRef<WorkflowTriggerKind>("manual")
  const dirtyRef = useRef(false)
  const mountedRef = useRef(false)
  const applyControllerRef = useRef<AbortController | null>(null)
  const applyGenerationRef = useRef(0)
  const [inspection, setInspection] =
    useState<WorkflowTriggersInspection | null>(null)
  const [inspectionYAML, setInspectionYAML] = useState<string | null>(null)
  const [selectedKind, setSelectedKind] =
    useState<WorkflowTriggerKind>("manual")
  const [draft, setDraft] = useState<TriggerDraft>(() =>
    emptyTriggerDraft("manual"),
  )
  const [initialDraft, setInitialDraft] = useState<TriggerDraft>(() =>
    emptyTriggerDraft("manual"),
  )
  const [loading, setLoading] = useState(true)
  const [applying, setApplying] = useState(false)
  const [loadError, setLoadError] = useState("")
  const [applyError, setApplyError] = useState("")
  const [selectionError, setSelectionError] = useState("")
  const [candidateValidation, setCandidateValidation] =
    useState<WorkflowDevelopmentValidation>()
  const [externalYAMLConflict, setExternalYAMLConflict] =
    useState<ExternalYAMLConflict | null>(null)
  const [inspectionNonce, setInspectionNonce] = useState(0)

  const dirty =
    triggerDraftSignature(draft) !== triggerDraftSignature(initialDraft)
  const hasExternalYAMLConflict =
    externalYAMLConflict != null ||
    (dirty && inspectionYAML != null && inspectionYAML !== yaml)

  useLayoutEffect(() => {
    dirtyRef.current = dirty
    latestYAMLRef.current = yaml
  }, [dirty, yaml])

  useEffect(() => {
    latestInspectionRef.current = inspection
  }, [inspection])

  useEffect(() => {
    selectedKindRef.current = selectedKind
  }, [selectedKind])

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
    onActivityChange({
      dirty,
      applying,
      conflict: hasExternalYAMLConflict,
    })
  }, [applying, dirty, hasExternalYAMLConflict, onActivityChange])

  useEffect(
    () => () => onActivityChange(idleWorkflowTriggerEditorActivity),
    [onActivityChange],
  )

  useEffect(() => {
    const controller = new AbortController()
    if (inspectionYAMLRef.current !== yaml) {
      applyControllerRef.current?.abort()
    }
    setLoading(true)
    setLoadError("")
    setSelectionError("")
    setCandidateValidation(undefined)
    if (!dirtyRef.current) {
      setApplyError("")
    }
    onInspectionChange({
      yaml,
      status: "loading",
      eventTriggerPresent: false,
    })
    const timeout = window.setTimeout(() => {
      void inspectWorkflowTriggers(yaml, controller.signal)
        .then((result) => {
          if (controller.signal.aborted || latestYAMLRef.current !== yaml) {
            return
          }
          if (
            dirtyRef.current &&
            inspectionYAMLRef.current != null &&
            inspectionYAMLRef.current !== yaml
          ) {
            setExternalYAMLConflict({ yaml, inspection: result })
            setLoading(false)
            onInspectionChange(inspectionState(yaml, result))
            return
          }
          if (dirtyRef.current && inspectionYAMLRef.current === yaml) {
            setExternalYAMLConflict(null)
            setLoading(false)
            onInspectionChange(inspectionState(yaml, result))
            return
          }
          const nextKind = preferredTriggerKind(result, selectedKindRef.current)
          const nextDraft = triggerDraftFromInspection(result, nextKind)
          latestInspectionRef.current = result
          inspectionYAMLRef.current = yaml
          setInspection(result)
          setInspectionYAML(yaml)
          selectedKindRef.current = nextKind
          setSelectedKind(nextKind)
          setDraft(nextDraft)
          setInitialDraft(nextDraft)
          setExternalYAMLConflict(null)
          setLoading(false)
          onInspectionChange(inspectionState(yaml, result))
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted || latestYAMLRef.current !== yaml) {
            return
          }
          const message = errorMessage(error)
          if (
            dirtyRef.current &&
            inspectionYAMLRef.current != null &&
            inspectionYAMLRef.current !== yaml
          ) {
            setExternalYAMLConflict({ yaml, error: message })
            setLoading(false)
            onInspectionChange({
              yaml,
              status: "error",
              eventTriggerPresent: false,
              reason: message,
            })
            return
          }
          latestInspectionRef.current = null
          inspectionYAMLRef.current = null
          setInspection(null)
          setInspectionYAML(null)
          setLoadError(message)
          setLoading(false)
          onInspectionChange({
            yaml,
            status: "error",
            eventTriggerPresent: false,
            reason: message,
          })
        })
    }, 250)

    return () => {
      window.clearTimeout(timeout)
      controller.abort()
    }
  }, [inspectionNonce, onInspectionChange, yaml])

  const conversion = useMemo(() => triggerFromDraft(draft), [draft])
  const projection = inspection?.triggers[selectedKind]
  const canApply =
    !disabled &&
    !loading &&
    !applying &&
    !hasExternalYAMLConflict &&
    inspectionYAML === yaml &&
    projection?.editable === true &&
    dirty &&
    conversion.errors.length === 0

  const chooseKind = (kind: WorkflowTriggerKind) => {
    if (kind === selectedKind) {
      return
    }
    if (dirty) {
      setSelectionError(
        `Apply or reset the ${triggerLabels[selectedKind].toLowerCase()} changes before switching trigger types.`,
      )
      return
    }
    if (inspection == null) {
      return
    }
    const nextDraft = triggerDraftFromInspection(inspection, kind)
    selectedKindRef.current = kind
    setSelectedKind(kind)
    setDraft(nextDraft)
    setInitialDraft(nextDraft)
    setApplyError("")
    setCandidateValidation(undefined)
    setSelectionError("")
  }

  const updateDraft = (next: TriggerDraft) => {
    setDraft(next)
    setApplyError("")
    setCandidateValidation(undefined)
    setSelectionError("")
  }

  const reset = () => {
    setDraft(initialDraft)
    setApplyError("")
    setCandidateValidation(undefined)
    setSelectionError("")
  }

  const discardAndLoadLatestYAML = () => {
    const latest = externalYAMLConflict
    setApplyError("")
    setCandidateValidation(undefined)
    setSelectionError("")
    if (latest?.yaml === yaml && latest.inspection != null) {
      const nextKind = preferredTriggerKind(
        latest.inspection,
        selectedKindRef.current,
      )
      const nextDraft = triggerDraftFromInspection(latest.inspection, nextKind)
      latestInspectionRef.current = latest.inspection
      inspectionYAMLRef.current = yaml
      selectedKindRef.current = nextKind
      setInspection(latest.inspection)
      setInspectionYAML(yaml)
      setSelectedKind(nextKind)
      setDraft(nextDraft)
      setInitialDraft(nextDraft)
      setExternalYAMLConflict(null)
      setLoadError("")
      onInspectionChange(inspectionState(yaml, latest.inspection))
      return
    }
    setDraft(initialDraft)
    latestInspectionRef.current = null
    inspectionYAMLRef.current = null
    setInspection(null)
    setInspectionYAML(null)
    setExternalYAMLConflict(null)
    setInspectionNonce((current) => current + 1)
  }

  const apply = async () => {
    if (!canApply || inspection == null) {
      return
    }
    const submittedYAML = yaml
    const submittedRevision = inspection.revision
    const submittedKind = selectedKind
    const submittedInitialSignature = triggerDraftSignature(initialDraft)
    applyControllerRef.current?.abort()
    const controller = new AbortController()
    applyControllerRef.current = controller
    applyGenerationRef.current += 1
    const generation = applyGenerationRef.current
    const requestIsCurrent = () =>
      mountedRef.current &&
      !controller.signal.aborted &&
      applyGenerationRef.current === generation
    setApplying(true)
    setApplyError("")
    setCandidateValidation(undefined)
    try {
      const result = await renderWorkflowTrigger(
        {
          yaml: submittedYAML,
          revision: submittedRevision,
          trigger_type: submittedKind,
          trigger: conversion.value as
            | WorkflowTriggerValueMap[typeof submittedKind]
            | null,
        },
        controller.signal,
      )
      if (!requestIsCurrent()) {
        return
      }
      if (
        latestYAMLRef.current !== submittedYAML ||
        latestInspectionRef.current?.revision !== submittedRevision ||
        inspectionYAMLRef.current !== submittedYAML
      ) {
        setApplyError(
          "The YAML changed while the trigger was rendering. Review the latest draft and apply again.",
        )
        return
      }
      const nextDraft = triggerDraftFromInspection(result, submittedKind)
      latestYAMLRef.current = result.yaml
      latestInspectionRef.current = result
      inspectionYAMLRef.current = result.yaml
      setInspection(result)
      setInspectionYAML(result.yaml)
      setDraft(nextDraft)
      setInitialDraft(nextDraft)
      setExternalYAMLConflict(null)
      setSelectionError("")
      onYAMLChange(result.yaml)
      onInspectionChange(inspectionState(result.yaml, result))
    } catch (error) {
      if (!requestIsCurrent()) {
        return
      }
      const message = triggerRenderErrorMessage(error)
      setApplyError(message)
      setCandidateValidation(
        error instanceof WorkflowAPIError
          ? error.candidateValidation
          : undefined,
      )
      if (
        error instanceof WorkflowAPIError &&
        error.status === 409 &&
        latestYAMLRef.current === submittedYAML
      ) {
        try {
          const refreshed = await inspectWorkflowTriggers(
            submittedYAML,
            controller.signal,
          )
          if (requestIsCurrent() && latestYAMLRef.current === submittedYAML) {
            const refreshedDraft = triggerDraftFromInspection(
              refreshed,
              submittedKind,
            )
            if (
              triggerDraftSignature(refreshedDraft) !==
              submittedInitialSignature
            ) {
              setExternalYAMLConflict({
                yaml: submittedYAML,
                inspection: refreshed,
              })
              onInspectionChange(inspectionState(submittedYAML, refreshed))
              return
            }
            latestInspectionRef.current = refreshed
            inspectionYAMLRef.current = submittedYAML
            setInspection(refreshed)
            setInspectionYAML(submittedYAML)
            setInitialDraft(refreshedDraft)
            onInspectionChange(inspectionState(submittedYAML, refreshed))
          }
        } catch {
          // Preserve the original, actionable revision error and the user's form.
        }
      }
    } finally {
      if (mountedRef.current && applyGenerationRef.current === generation) {
        setApplying(false)
      }
      if (applyControllerRef.current === controller) {
        applyControllerRef.current = null
      }
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-auto p-4">
        <div className="mx-auto grid w-full max-w-4xl gap-4">
          <div className="border-border bg-muted/30 grid gap-3 rounded-md border p-3">
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(14rem,0.7fr)] sm:items-end">
              <div>
                <div className="text-sm font-medium">
                  Typed workflow triggers
                </div>
                <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                  Apply one trigger family at a time. Unrelated triggers, jobs,
                  and comments remain in the authoritative YAML draft.
                </p>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="workflow-trigger-type">Trigger type</Label>
                <Select
                  value={selectedKind}
                  onValueChange={(value) =>
                    chooseKind(value as WorkflowTriggerKind)
                  }
                  disabled={loading || applying || hasExternalYAMLConflict}
                >
                  <SelectTrigger id="workflow-trigger-type">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {workflowTriggerKinds.map((kind) => {
                      const item = inspection?.triggers[kind]
                      const state =
                        item?.present === true
                          ? item.editable
                            ? "Configured"
                            : "YAML only"
                          : "Not configured"
                      return (
                        <SelectItem key={kind} value={kind}>
                          {triggerLabels[kind]} · {state}
                        </SelectItem>
                      )
                    })}
                  </SelectContent>
                </Select>
              </div>
            </div>
          </div>

          {hasExternalYAMLConflict ? (
            <div
              role="alert"
              className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-xs"
            >
              <div className="font-medium">
                The authoritative YAML changed outside this builder.
              </div>
              <p className="text-muted-foreground mt-1 leading-relaxed">
                Your pending trigger edits are preserved but cannot be applied
                to the newer YAML revision. Discard them and load the latest
                trigger values before continuing.
              </p>
              {externalYAMLConflict?.error ? (
                <p className="text-destructive mt-2 break-words">
                  Latest trigger inspection failed: {externalYAMLConflict.error}
                </p>
              ) : null}
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={discardAndLoadLatestYAML}
                disabled={applying}
              >
                <IconRefresh className="size-4" />
                Discard edits and load latest YAML
              </Button>
            </div>
          ) : null}

          {loading ? (
            <div
              role="status"
              className="text-muted-foreground flex min-h-32 items-center justify-center text-sm"
            >
              Inspecting workflow triggers…
            </div>
          ) : loadError ? (
            <EditorBoundaryMessage
              title="Trigger builder unavailable"
              message={loadError}
              onOpenYAML={onOpenYAML}
            />
          ) : projection?.editable === false ? (
            <EditorBoundaryMessage
              title={`Edit ${triggerLabels[selectedKind].toLowerCase()} in YAML`}
              message={
                projection.reason ??
                "This trigger shape cannot be changed safely by the structured editor."
              }
              onOpenYAML={onOpenYAML}
            />
          ) : (
            <>
              <div className="border-border flex items-start justify-between gap-4 rounded-md border p-3">
                <div className="min-w-0">
                  <Label
                    htmlFor="workflow-trigger-enabled"
                    className="text-sm font-medium"
                  >
                    {triggerLabels[selectedKind]} trigger
                  </Label>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {triggerDescriptions[selectedKind]}
                  </p>
                </div>
                <Switch
                  id="workflow-trigger-enabled"
                  checked={draft.enabled}
                  onCheckedChange={(enabled) =>
                    updateDraft({ ...draft, enabled })
                  }
                  disabled={disabled || applying || hasExternalYAMLConflict}
                  aria-label={`Enable ${triggerLabels[selectedKind].toLowerCase()} trigger`}
                />
              </div>

              {draft.enabled ? (
                <TriggerFields
                  draft={draft}
                  disabled={disabled || applying || hasExternalYAMLConflict}
                  onChange={updateDraft}
                />
              ) : (
                <div className="border-border text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-sm">
                  Applying removes only <code>on.{selectedKind}</code>. Other
                  triggers and jobs remain unchanged.
                </div>
              )}

              {conversion.errors.length > 0 ? (
                <div
                  role="alert"
                  className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-xs"
                >
                  <div className="font-medium">
                    Fix this trigger before applying:
                  </div>
                  <ul className="mt-1 list-disc space-y-1 pl-5">
                    {conversion.errors.map((message) => (
                      <li key={message}>{message}</li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {candidateValidation != null ? (
                <ValidationSummary
                  validation={candidateValidation}
                  title="Candidate trigger validation"
                  alert
                />
              ) : null}
              <ValidationSummary
                validation={inspection?.validation}
                title="Current YAML validation"
              />
            </>
          )}

          {selectionError ? (
            <div
              role="alert"
              className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs"
            >
              {selectionError}
            </div>
          ) : null}
          {applyError ? (
            <div
              role="alert"
              className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-xs break-words"
            >
              {applyError}
            </div>
          ) : null}
        </div>
      </div>

      <div className="border-border bg-background flex flex-wrap items-center justify-between gap-2 border-t p-3">
        <div className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs">
          {dirty ? (
            <Badge variant="outline">Builder changes not applied</Badge>
          ) : (
            <>
              <IconCheck className="size-4" />
              Builder matches the YAML draft
            </>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={reset}
            disabled={!dirty || applying || hasExternalYAMLConflict}
          >
            <IconRefresh className="size-4" />
            Reset builder
          </Button>
          <Button
            type="button"
            size="sm"
            onClick={() => void apply()}
            disabled={!canApply}
            title={
              dirty && conversion.errors.length > 0
                ? conversion.errors[0]
                : undefined
            }
          >
            <IconCode className="size-4" />
            {applying ? "Applying" : "Apply to YAML"}
          </Button>
        </div>
      </div>
    </div>
  )
}

function TriggerFields({
  draft,
  disabled,
  onChange,
}: {
  draft: TriggerDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  switch (draft.kind) {
    case "manual":
      return (
        <div className="border-border bg-muted/20 rounded-md border p-4 text-sm">
          Manual triggers have no additional settings.
        </div>
      )
    case "schedule":
      return (
        <ScheduleFields draft={draft} disabled={disabled} onChange={onChange} />
      )
    case "channel_message":
      return (
        <ChannelMessageFields
          draft={draft}
          disabled={disabled}
          onChange={onChange}
        />
      )
    case "command":
      return (
        <CommandFields draft={draft} disabled={disabled} onChange={onChange} />
      )
    case "runtime_event":
      return (
        <RuntimeEventFields
          draft={draft}
          disabled={disabled}
          onChange={onChange}
        />
      )
    case "event":
      return (
        <EventFields draft={draft} disabled={disabled} onChange={onChange} />
      )
    case "workflow_call":
      return (
        <WorkflowCallFields
          draft={draft}
          disabled={disabled}
          onChange={onChange}
        />
      )
  }
}

function ScheduleFields({
  draft,
  disabled,
  onChange,
}: {
  draft: ScheduleDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  return (
    <fieldset disabled={disabled} className="grid gap-3">
      <legend className="sr-only">Cron schedules</legend>
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium">Cron schedules</div>
          <p className="text-muted-foreground mt-1 text-xs">
            PicoClaw validates each cron expression before applying it.
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() =>
            onChange({
              ...draft,
              crons: [...draft.crons, { id: newRowID("schedule"), value: "" }],
            })
          }
        >
          <IconPlus className="size-4" />
          Add schedule
        </Button>
      </div>
      {draft.crons.length === 0 ? (
        <p className="text-muted-foreground rounded-md border border-dashed p-4 text-center text-xs">
          No schedules are configured.
        </p>
      ) : (
        draft.crons.map((cron, index) => (
          <div
            key={cron.id}
            className="border-border grid gap-2 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end"
          >
            <div className="grid gap-1.5">
              <Label htmlFor={`workflow-trigger-schedule-${cron.id}`}>
                Cron expression {index + 1}
              </Label>
              <Input
                id={`workflow-trigger-schedule-${cron.id}`}
                value={cron.value}
                onChange={(event) =>
                  onChange({
                    ...draft,
                    crons: replaceRow(draft.crons, index, {
                      ...cron,
                      value: event.target.value,
                    }),
                  })
                }
                placeholder="0 8 * * *"
                className="font-mono text-xs"
              />
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() =>
                onChange({
                  ...draft,
                  crons: draft.crons.filter((item) => item.id !== cron.id),
                })
              }
              aria-label={`Remove cron schedule ${index + 1}`}
            >
              <IconTrash className="size-4" />
            </Button>
          </div>
        ))
      )}
    </fieldset>
  )
}

function ChannelMessageFields({
  draft,
  disabled,
  onChange,
}: {
  draft: ChannelMessageDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  return (
    <fieldset disabled={disabled} className="grid gap-5">
      <legend className="sr-only">Channel message trigger</legend>
      <div className="grid gap-4 md:grid-cols-3">
        <TextListField
          id="workflow-trigger-channel-message-channels"
          label="Channels"
          value={draft.channels}
          placeholder={"telegram\nslack"}
          onChange={(channels) => onChange({ ...draft, channels })}
        />
        <TextListField
          id="workflow-trigger-channel-message-chats"
          label="Chats"
          value={draft.chats}
          onChange={(chats) => onChange({ ...draft, chats })}
        />
        <TextListField
          id="workflow-trigger-channel-message-senders"
          label="Senders"
          value={draft.senders}
          onChange={(senders) => onChange({ ...draft, senders })}
        />
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <StringField
          id="workflow-trigger-channel-message-command"
          label="Command word"
          value={draft.command}
          placeholder="ask"
          onChange={(command) => onChange({ ...draft, command })}
        />
        <StringField
          id="workflow-trigger-channel-message-regex"
          label="Text regular expression"
          value={draft.textMatches}
          placeholder="^/ask"
          mono
          onChange={(textMatches) => onChange({ ...draft, textMatches })}
        />
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <TriStateField
          id="workflow-trigger-channel-message-mentioned"
          label="Mention requirement"
          value={draft.mentioned}
          options={[
            ["default", "Any message"],
            ["true", "Must mention the agent"],
            ["false", "Must not mention the agent"],
          ]}
          onChange={(mentioned) => onChange({ ...draft, mentioned })}
        />
        <TriStateField
          id="workflow-trigger-channel-message-passthrough"
          label="Normal agent handling"
          value={draft.passthrough}
          options={[
            ["default", "Consume message (default)"],
            ["true", "Continue normal handling"],
            ["false", "Consume message"],
          ]}
          onChange={(passthrough) => onChange({ ...draft, passthrough })}
        />
      </div>
      <ConversationFields
        session={draft.session}
        delivery={draft.delivery}
        onSessionChange={(session) => onChange({ ...draft, session })}
        onDeliveryChange={(delivery) => onChange({ ...draft, delivery })}
      />
    </fieldset>
  )
}

function CommandFields({
  draft,
  disabled,
  onChange,
}: {
  draft: CommandDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  return (
    <fieldset disabled={disabled} className="grid gap-5">
      <legend className="sr-only">Command trigger</legend>
      <StringField
        id="workflow-trigger-command-name"
        label="Command name"
        value={draft.name}
        placeholder="summarize"
        onChange={(name) => onChange({ ...draft, name })}
      />
      <div className="grid gap-4 md:grid-cols-3">
        <TextListField
          id="workflow-trigger-command-channels"
          label="Channels"
          value={draft.channels}
          onChange={(channels) => onChange({ ...draft, channels })}
        />
        <TextListField
          id="workflow-trigger-command-chats"
          label="Chats"
          value={draft.chats}
          onChange={(chats) => onChange({ ...draft, chats })}
        />
        <TextListField
          id="workflow-trigger-command-senders"
          label="Senders"
          value={draft.senders}
          onChange={(senders) => onChange({ ...draft, senders })}
        />
      </div>
      <DefinitionRows
        legend="Command arguments"
        singular="argument"
        rows={draft.args}
        onChange={(args) => onChange({ ...draft, args })}
      />
      <TriStateField
        id="workflow-trigger-command-passthrough"
        label="Normal agent handling"
        value={draft.passthrough}
        options={[
          ["default", "Consume message (default)"],
          ["true", "Continue normal handling"],
          ["false", "Consume message"],
        ]}
        onChange={(passthrough) => onChange({ ...draft, passthrough })}
      />
      <ConversationFields
        session={draft.session}
        delivery={draft.delivery}
        onSessionChange={(session) => onChange({ ...draft, session })}
        onDeliveryChange={(delivery) => onChange({ ...draft, delivery })}
      />
    </fieldset>
  )
}

function RuntimeEventFields({
  draft,
  disabled,
  onChange,
}: {
  draft: RuntimeEventDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  return (
    <fieldset disabled={disabled} className="grid gap-4">
      <legend className="sr-only">Runtime event filters</legend>
      <div className="border-border bg-muted/30 rounded-md border p-3 text-xs">
        Populate at least one field. Values within a field use OR; populated
        fields use AND.
      </div>
      <div className="grid gap-4 md:grid-cols-3">
        <TextListField
          id="workflow-trigger-runtime-kinds"
          label="Event kinds"
          value={draft.kinds}
          placeholder={"agent.turn.end\nworkflow.run.*"}
          onChange={(kinds) => onChange({ ...draft, kinds })}
        />
        <TextListField
          id="workflow-trigger-runtime-sources"
          label="Sources"
          value={draft.sources}
          placeholder={"agent\nagent/main"}
          onChange={(sources) => onChange({ ...draft, sources })}
        />
        <TextListField
          id="workflow-trigger-runtime-agents"
          label="Agents"
          value={draft.agents}
          onChange={(agents) => onChange({ ...draft, agents })}
        />
        <TextListField
          id="workflow-trigger-runtime-sessions"
          label="Sessions"
          value={draft.sessions}
          onChange={(sessions) => onChange({ ...draft, sessions })}
        />
        <TextListField
          id="workflow-trigger-runtime-channels"
          label="Channels"
          value={draft.channels}
          onChange={(channels) => onChange({ ...draft, channels })}
        />
        <TextListField
          id="workflow-trigger-runtime-chats"
          label="Chats"
          value={draft.chats}
          onChange={(chats) => onChange({ ...draft, chats })}
        />
      </div>
    </fieldset>
  )
}

function EventFields({
  draft,
  disabled,
  onChange,
}: {
  draft: EventDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  return (
    <fieldset disabled={disabled} className="grid gap-5">
      <legend className="sr-only">Durable event filters</legend>
      <div className="border-border bg-muted/30 rounded-md border p-3 text-xs">
        <div className="font-medium">Deterministic event routing</div>
        <p className="text-muted-foreground mt-1 leading-relaxed">
          Patterns are fully anchored and support <code>*</code> and{" "}
          <code>?</code>. Values within one field use OR; populated fields use
          AND. Normalized types and connector identity are case-insensitive; IDs
          and attribute values are case-sensitive.
        </p>
      </div>
      <div className="grid gap-4 md:grid-cols-3">
        <TextListField
          id="workflow-event-sources"
          label="Sources"
          value={draft.sources}
          placeholder={"github\nchannel\ndeltachat"}
          onChange={(sources) => onChange({ ...draft, sources })}
        />
        <TextListField
          id="workflow-event-connectors"
          label="Connectors"
          value={draft.connectors}
          placeholder={"primary\nsupport-*"}
          onChange={(connectors) => onChange({ ...draft, connectors })}
        />
        <TextListField
          id="workflow-event-types"
          label="Event types"
          value={draft.types}
          placeholder={"issues.opened\npull_request.*"}
          onChange={(types) => onChange({ ...draft, types })}
        />
      </div>
      <AttributeRows
        legend="Event attributes"
        scope="event"
        rows={draft.attributes}
        onChange={(attributes) => onChange({ ...draft, attributes })}
      />
      <EntityFields
        kind="actor"
        value={draft.actor}
        onChange={(actor) => onChange({ ...draft, actor })}
      />
      <EntityFields
        kind="subject"
        value={draft.subject}
        onChange={(subject) => onChange({ ...draft, subject })}
      />
    </fieldset>
  )
}

function EntityFields({
  kind,
  value,
  onChange,
}: {
  kind: "actor" | "subject"
  value: EntityDraft
  onChange: (value: EntityDraft) => void
}) {
  const title = kind === "actor" ? "Actor filters" : "Subject filters"
  return (
    <fieldset className="border-border grid gap-4 rounded-md border p-3">
      <legend className="px-1 text-sm font-medium">{title}</legend>
      <div className="flex items-center justify-between gap-3">
        <p className="text-muted-foreground text-xs">
          Require a matching normalized {kind} entity.
        </p>
        <Switch
          checked={value.enabled}
          onCheckedChange={(enabled) => onChange({ ...value, enabled })}
          aria-label={`Enable ${kind} filters`}
        />
      </div>
      {value.enabled ? (
        <>
          <div className="grid gap-4 md:grid-cols-2">
            <TextListField
              id={`workflow-event-${kind}-ids`}
              label={`${kind === "actor" ? "Actor" : "Subject"} IDs`}
              value={value.ids}
              onChange={(ids) => onChange({ ...value, ids })}
            />
            <TextListField
              id={`workflow-event-${kind}-types`}
              label={`${kind === "actor" ? "Actor" : "Subject"} types`}
              value={value.types}
              onChange={(types) => onChange({ ...value, types })}
            />
          </div>
          <AttributeRows
            legend={`${kind === "actor" ? "Actor" : "Subject"} attributes`}
            scope={kind}
            rows={value.attributes}
            onChange={(attributes) => onChange({ ...value, attributes })}
          />
        </>
      ) : null}
    </fieldset>
  )
}

function WorkflowCallFields({
  draft,
  disabled,
  onChange,
}: {
  draft: WorkflowCallDraft
  disabled: boolean
  onChange: (draft: TriggerDraft) => void
}) {
  return (
    <fieldset disabled={disabled} className="grid gap-5">
      <legend className="sr-only">Workflow call contract</legend>
      <DefinitionRows
        legend="Inputs"
        singular="input"
        rows={draft.inputs}
        onChange={(inputs) => onChange({ ...draft, inputs })}
      />
      <RequiredRows
        legend="Secrets"
        singular="secret"
        rows={draft.secrets}
        onChange={(secrets) => onChange({ ...draft, secrets })}
      />
      <OutputRows
        rows={draft.outputs}
        onChange={(outputs) => onChange({ ...draft, outputs })}
      />
    </fieldset>
  )
}

function ConversationFields({
  session,
  delivery,
  onSessionChange,
  onDeliveryChange,
}: {
  session: ConversationSession
  delivery: ConversationDelivery
  onSessionChange: (value: ConversationSession) => void
  onDeliveryChange: (value: ConversationDelivery) => void
}) {
  return (
    <fieldset className="border-border grid gap-4 rounded-md border p-3 md:grid-cols-2">
      <legend className="px-1 text-sm font-medium">Conversation</legend>
      <SelectField
        id="workflow-trigger-conversation-session"
        label="Session scope"
        value={session}
        options={conversationSessionOptions(session)}
        onChange={onSessionChange}
      />
      <SelectField
        id="workflow-trigger-conversation-delivery"
        label="Delivery"
        value={delivery}
        options={conversationDeliveryOptions(delivery)}
        onChange={onDeliveryChange}
      />
    </fieldset>
  )
}

function DefinitionRows({
  legend,
  singular,
  rows,
  onChange,
}: {
  legend: string
  singular: string
  rows: DefinitionRow[]
  onChange: (rows: DefinitionRow[]) => void
}) {
  return (
    <fieldset className="border-border grid gap-3 rounded-md border p-3">
      <legend className="sr-only">{legend}</legend>
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">{legend}</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onChange([...rows, emptyDefinitionRow(singular)])}
        >
          <IconPlus className="size-4" />
          Add {singular}
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No {legend.toLowerCase()} declared.
        </p>
      ) : (
        rows.map((row, index) => (
          <div
            key={row.id}
            className="border-border/70 grid gap-3 rounded-md border p-3"
          >
            <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(9rem,0.45fr)_auto_auto] md:items-end">
              <StringField
                id={`workflow-trigger-${singular}-${row.id}-name`}
                label={`${capitalize(singular)} name`}
                value={row.name}
                mono
                onChange={(name) =>
                  onChange(replaceRow(rows, index, { ...row, name }))
                }
              />
              <SelectField
                id={`workflow-trigger-${singular}-${row.id}-type`}
                label="Type"
                value={row.type}
                options={inputTypeOptionsFor(row.type)}
                onChange={(type) =>
                  onChange(
                    replaceRow(rows, index, {
                      ...row,
                      type,
                      hasType: true,
                      defaultValue: defaultValueForType(
                        type as InputType,
                        row.defaultValue,
                        row.type,
                      ),
                    }),
                  )
                }
              />
              <div className="flex min-h-9 items-center gap-2">
                <Switch
                  id={`workflow-trigger-${singular}-${row.id}-required`}
                  checked={row.required}
                  onCheckedChange={(required) =>
                    onChange(replaceRow(rows, index, { ...row, required }))
                  }
                />
                <Label
                  htmlFor={`workflow-trigger-${singular}-${row.id}-required`}
                >
                  Required
                </Label>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={() =>
                  onChange(rows.filter((item) => item.id !== row.id))
                }
                aria-label={`Remove ${singular} ${index + 1}`}
              >
                <IconTrash className="size-4" />
              </Button>
            </div>
            <div className="grid gap-2">
              <div className="flex items-center gap-2">
                <Switch
                  id={`workflow-trigger-${singular}-${row.id}-default-enabled`}
                  checked={row.hasDefault}
                  onCheckedChange={(hasDefault) =>
                    onChange(replaceRow(rows, index, { ...row, hasDefault }))
                  }
                />
                <Label
                  htmlFor={`workflow-trigger-${singular}-${row.id}-default-enabled`}
                >
                  Declare default
                </Label>
              </div>
              {row.hasDefault ? (
                <DefaultValueField
                  id={`workflow-trigger-${singular}-${row.id}-default`}
                  row={row}
                  onChange={(defaultValue) =>
                    onChange(replaceRow(rows, index, { ...row, defaultValue }))
                  }
                />
              ) : null}
            </div>
          </div>
        ))
      )}
    </fieldset>
  )
}

function DefaultValueField({
  id,
  row,
  onChange,
}: {
  id: string
  row: DefinitionRow
  onChange: (value: string) => void
}) {
  if (row.type === "boolean") {
    return (
      <SelectField
        id={id}
        label="Default value"
        value={row.defaultValue === "true" ? "true" : "false"}
        options={[
          ["false", "False"],
          ["true", "True"],
        ]}
        onChange={onChange}
      />
    )
  }
  if (row.type === "object" || row.type === "array") {
    return (
      <div className="grid gap-1.5">
        <Label htmlFor={id}>Default JSON</Label>
        <Textarea
          id={id}
          value={row.defaultValue}
          onChange={(event) => onChange(event.target.value)}
          spellCheck={false}
          className="min-h-20 resize-y font-mono text-xs"
        />
      </div>
    )
  }
  if (!isKnownInputType(row.type)) {
    return (
      <div className="grid gap-1.5">
        <Label htmlFor={id}>Default JSON</Label>
        <Textarea
          id={id}
          value={row.defaultValue}
          onChange={(event) => onChange(event.target.value)}
          spellCheck={false}
          className="min-h-20 resize-y font-mono text-xs"
        />
      </div>
    )
  }
  return (
    <StringField
      id={id}
      label="Default value"
      value={row.defaultValue}
      mono={row.type === "number"}
      onChange={onChange}
    />
  )
}

function RequiredRows({
  legend,
  singular,
  rows,
  onChange,
}: {
  legend: string
  singular: string
  rows: RequiredRow[]
  onChange: (rows: RequiredRow[]) => void
}) {
  return (
    <fieldset className="border-border grid gap-3 rounded-md border p-3">
      <legend className="sr-only">{legend}</legend>
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">{legend}</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() =>
            onChange([
              ...rows,
              { id: newRowID(singular), name: "", required: false },
            ])
          }
        >
          <IconPlus className="size-4" />
          Add {singular}
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No {legend.toLowerCase()} declared.
        </p>
      ) : (
        rows.map((row, index) => (
          <div
            key={row.id}
            className="border-border/70 grid gap-3 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_auto_auto] sm:items-end"
          >
            <StringField
              id={`workflow-trigger-${singular}-${row.id}-name`}
              label={`${capitalize(singular)} name`}
              value={row.name}
              mono
              onChange={(name) =>
                onChange(replaceRow(rows, index, { ...row, name }))
              }
            />
            <div className="flex min-h-9 items-center gap-2">
              <Switch
                id={`workflow-trigger-${singular}-${row.id}-required`}
                checked={row.required}
                onCheckedChange={(required) =>
                  onChange(replaceRow(rows, index, { ...row, required }))
                }
              />
              <Label
                htmlFor={`workflow-trigger-${singular}-${row.id}-required`}
              >
                Required
              </Label>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() =>
                onChange(rows.filter((item) => item.id !== row.id))
              }
              aria-label={`Remove ${singular} ${index + 1}`}
            >
              <IconTrash className="size-4" />
            </Button>
          </div>
        ))
      )}
    </fieldset>
  )
}

function OutputRows({
  rows,
  onChange,
}: {
  rows: OutputRow[]
  onChange: (rows: OutputRow[]) => void
}) {
  return (
    <fieldset className="border-border grid gap-3 rounded-md border p-3">
      <legend className="sr-only">Outputs</legend>
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">Outputs</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() =>
            onChange([...rows, { id: newRowID("output"), name: "", value: "" }])
          }
        >
          <IconPlus className="size-4" />
          Add output
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-xs">No outputs declared.</p>
      ) : (
        rows.map((row, index) => (
          <div
            key={row.id}
            className="border-border/70 grid gap-3 rounded-md border p-3 sm:grid-cols-[minmax(9rem,0.6fr)_minmax(0,1.4fr)_auto] sm:items-end"
          >
            <StringField
              id={`workflow-trigger-output-${row.id}-name`}
              label="Output name"
              value={row.name}
              mono
              onChange={(name) =>
                onChange(replaceRow(rows, index, { ...row, name }))
              }
            />
            <StringField
              id={`workflow-trigger-output-${row.id}-value`}
              label="Value or expression"
              value={row.value}
              mono
              placeholder="${{ jobs.build.outputs.result }}"
              onChange={(value) =>
                onChange(replaceRow(rows, index, { ...row, value }))
              }
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() =>
                onChange(rows.filter((item) => item.id !== row.id))
              }
              aria-label={`Remove output ${index + 1}`}
            >
              <IconTrash className="size-4" />
            </Button>
          </div>
        ))
      )}
    </fieldset>
  )
}

function AttributeRows({
  legend,
  scope,
  rows,
  onChange,
}: {
  legend: string
  scope: string
  rows: AttributeRow[]
  onChange: (rows: AttributeRow[]) => void
}) {
  return (
    <fieldset className="border-border grid gap-3 rounded-md border p-3">
      <legend className="sr-only">{legend}</legend>
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">{legend}</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() =>
            onChange([...rows, { id: newRowID(scope), key: "", patterns: "" }])
          }
        >
          <IconPlus className="size-4" />
          Add attribute
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No attribute filters configured.
        </p>
      ) : (
        rows.map((row, index) => (
          <div
            key={row.id}
            className="border-border/70 grid gap-3 rounded-md border p-3 md:grid-cols-[minmax(10rem,0.7fr)_minmax(0,1.3fr)_auto]"
          >
            <StringField
              id={`workflow-event-${scope}-attribute-${row.id}-key`}
              label="Attribute name"
              value={row.key}
              mono
              onChange={(key) =>
                onChange(replaceRow(rows, index, { ...row, key }))
              }
            />
            <TextListField
              id={`workflow-event-${scope}-attribute-${row.id}-patterns`}
              label="Value patterns"
              value={row.patterns}
              onChange={(patterns) =>
                onChange(replaceRow(rows, index, { ...row, patterns }))
              }
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className="self-start"
              onClick={() =>
                onChange(rows.filter((item) => item.id !== row.id))
              }
              aria-label={`Remove ${legend.toLowerCase()} row ${index + 1}`}
            >
              <IconTrash className="size-4" />
            </Button>
          </div>
        ))
      )}
    </fieldset>
  )
}

function TextListField({
  id,
  label,
  value,
  placeholder,
  onChange,
}: {
  id: string
  label: string
  value: string
  placeholder?: string
  onChange: (value: string) => void
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Textarea
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        className="min-h-20 resize-y font-mono text-xs"
        aria-describedby={`${id}-help`}
      />
      <p id={`${id}-help`} className="text-muted-foreground text-xs">
        One value per line.
      </p>
    </div>
  )
}

function StringField({
  id,
  label,
  value,
  placeholder,
  mono,
  onChange,
}: {
  id: string
  label: string
  value: string
  placeholder?: string
  mono?: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className={cn(mono && "font-mono text-xs")}
      />
    </div>
  )
}

function TriStateField({
  id,
  label,
  value,
  options,
  onChange,
}: {
  id: string
  label: string
  value: TriState
  options: Array<[TriState, string]>
  onChange: (value: TriState) => void
}) {
  return (
    <SelectField
      id={id}
      label={label}
      value={value}
      options={options}
      onChange={(next) => onChange(next as TriState)}
    />
  )
}

function SelectField({
  id,
  label,
  value,
  options,
  onChange,
}: {
  id: string
  label: string
  value: string
  options: ReadonlyArray<readonly [string, string]>
  onChange: (value: string) => void
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger id={id}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([optionValue, optionLabel]) => (
            <SelectItem key={optionValue} value={optionValue}>
              {optionLabel}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

function EditorBoundaryMessage({
  title,
  message,
  onOpenYAML,
}: {
  title: string
  message: string
  onOpenYAML: () => void
}) {
  return (
    <div
      role="alert"
      className="border-border bg-muted/30 grid gap-3 rounded-md border p-4"
    >
      <div className="flex items-start gap-2">
        <IconAlertTriangle className="text-muted-foreground mt-0.5 size-4 shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-medium">{title}</div>
          <p className="text-muted-foreground mt-1 text-xs break-words">
            {message}
          </p>
        </div>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        onClick={onOpenYAML}
      >
        <IconCode className="size-4" />
        Open YAML
      </Button>
    </div>
  )
}

function ValidationSummary({
  validation,
  title,
  alert = false,
}: {
  validation?: WorkflowDevelopmentValidation
  title?: string
  alert?: boolean
}) {
  if (validation == null) {
    return null
  }
  const issues = [...(validation.errors ?? []), ...(validation.warnings ?? [])]
  if (issues.length === 0) {
    if (!validation.valid) {
      return (
        <div
          role={alert ? "alert" : undefined}
          className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-xs"
        >
          {title ? <div className="font-medium">{title}</div> : null}
          <p className={cn(title && "mt-1")}>
            Validation failed without additional details.
          </p>
        </div>
      )
    }
    return (
      <div className="text-muted-foreground flex items-center gap-2 text-xs">
        <IconCheck className="size-4" />
        {title
          ? `${title}: valid.`
          : "The current workflow parses and validates."}
      </div>
    )
  }
  return (
    <div
      role={alert ? "alert" : undefined}
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        validation.valid
          ? "border-border bg-muted/30 text-muted-foreground"
          : "border-destructive/40 bg-destructive/10 text-destructive",
      )}
    >
      {title ? <div className="mb-1 font-medium">{title}</div> : null}
      <ul className="list-disc space-y-1 pl-5">
        {issues.slice(0, 8).map((issue, index) => (
          <li key={`${issue.path ?? ""}-${issue.message}-${index}`}>
            {issue.path ? <code>{issue.path}: </code> : null}
            {issue.message}
          </li>
        ))}
      </ul>
    </div>
  )
}

type TriState = "default" | "true" | "false"
type ConversationSession = string
type ConversationDelivery = string
type InputType = string

interface ManualDraft {
  kind: "manual"
  enabled: boolean
}

interface ScheduleDraft {
  kind: "schedule"
  enabled: boolean
  crons: Array<{ id: string; value: string }>
}

interface ChannelMessageDraft {
  kind: "channel_message"
  enabled: boolean
  channels: string
  chats: string
  senders: string
  mentioned: TriState
  command: string
  textMatches: string
  passthrough: TriState
  session: ConversationSession
  delivery: ConversationDelivery
}

interface CommandDraft {
  kind: "command"
  enabled: boolean
  name: string
  channels: string
  chats: string
  senders: string
  args: DefinitionRow[]
  passthrough: TriState
  session: ConversationSession
  delivery: ConversationDelivery
}

interface RuntimeEventDraft {
  kind: "runtime_event"
  enabled: boolean
  kinds: string
  sources: string
  agents: string
  sessions: string
  channels: string
  chats: string
}

interface EventDraft {
  kind: "event"
  enabled: boolean
  sources: string
  connectors: string
  types: string
  attributes: AttributeRow[]
  actor: EntityDraft
  subject: EntityDraft
}

interface WorkflowCallDraft {
  kind: "workflow_call"
  enabled: boolean
  inputs: DefinitionRow[]
  secrets: RequiredRow[]
  outputs: OutputRow[]
}

type TriggerDraft =
  | ManualDraft
  | ScheduleDraft
  | ChannelMessageDraft
  | CommandDraft
  | RuntimeEventDraft
  | EventDraft
  | WorkflowCallDraft

interface DefinitionRow {
  id: string
  name: string
  type: InputType
  hasType: boolean
  required: boolean
  hasDefault: boolean
  defaultValue: string
}

interface RequiredRow {
  id: string
  name: string
  required: boolean
}

interface OutputRow {
  id: string
  name: string
  value: string
}

interface AttributeRow {
  id: string
  key: string
  patterns: string
}

interface EntityDraft {
  enabled: boolean
  ids: string
  types: string
  attributes: AttributeRow[]
}

const inputTypeOptions = [
  ["string", "String"],
  ["number", "Number"],
  ["boolean", "Boolean"],
  ["object", "Object"],
  ["array", "Array"],
] as const

const knownInputTypes = new Set<string>(
  inputTypeOptions.map(([value]) => value),
)

function isKnownInputType(value: string) {
  return knownInputTypes.has(value)
}

function inputTypeOptionsFor(
  value: string,
): ReadonlyArray<readonly [string, string]> {
  if (isKnownInputType(value)) {
    return inputTypeOptions
  }
  return [
    [value, `Unsupported YAML value: ${JSON.stringify(value)}`],
    ...inputTypeOptions,
  ]
}

const canonicalConversationSessionOptions = [
  ["default", "Discussion (default)"],
  ["discussion", "Discussion"],
  ["sender", "Sender"],
  ["global", "Global"],
] as const

const canonicalConversationDeliveryOptions = [
  ["default", "Same discussion (default)"],
  ["same_discussion", "Same discussion"],
  ["none", "None"],
] as const

function conversationSessionOptions(
  value: ConversationSession,
): ReadonlyArray<readonly [string, string]> {
  return optionsPreservingUnsupportedValue(
    value,
    canonicalConversationSessionOptions,
  )
}

function conversationDeliveryOptions(
  value: ConversationDelivery,
): ReadonlyArray<readonly [string, string]> {
  return optionsPreservingUnsupportedValue(
    value,
    canonicalConversationDeliveryOptions,
  )
}

function optionsPreservingUnsupportedValue(
  value: string,
  options: ReadonlyArray<readonly [string, string]>,
): ReadonlyArray<readonly [string, string]> {
  if (options.some(([option]) => option === value)) {
    return options
  }
  return [
    [value, `Unsupported YAML value: ${JSON.stringify(value)}`],
    ...options,
  ]
}

let nextRowID = 0

function newRowID(scope: string) {
  nextRowID += 1
  return `${scope}-${nextRowID}`
}

function emptyTriggerDraft(kind: WorkflowTriggerKind): TriggerDraft {
  switch (kind) {
    case "manual":
      return { kind, enabled: false }
    case "schedule":
      return { kind, enabled: false, crons: [] }
    case "channel_message":
      return {
        kind,
        enabled: false,
        channels: "",
        chats: "",
        senders: "",
        mentioned: "default",
        command: "",
        textMatches: "",
        passthrough: "default",
        session: "default",
        delivery: "default",
      }
    case "command":
      return {
        kind,
        enabled: false,
        name: "",
        channels: "",
        chats: "",
        senders: "",
        args: [],
        passthrough: "default",
        session: "default",
        delivery: "default",
      }
    case "runtime_event":
      return {
        kind,
        enabled: false,
        kinds: "",
        sources: "",
        agents: "",
        sessions: "",
        channels: "",
        chats: "",
      }
    case "event":
      return {
        kind,
        enabled: false,
        sources: "",
        connectors: "",
        types: "",
        attributes: [],
        actor: emptyEntityDraft(),
        subject: emptyEntityDraft(),
      }
    case "workflow_call":
      return {
        kind,
        enabled: false,
        inputs: [],
        secrets: [],
        outputs: [],
      }
  }
}

function preferredTriggerKind(
  inspection: WorkflowTriggersInspection,
  current: WorkflowTriggerKind,
) {
  if (inspection.triggers[current]?.present) {
    return current
  }
  return (
    workflowTriggerKinds.find((kind) => inspection.triggers[kind].present) ??
    current
  )
}

function triggerDraftFromInspection(
  inspection: WorkflowTriggersInspection,
  kind: WorkflowTriggerKind,
): TriggerDraft {
  const projection = inspection.triggers[kind]
  if (!projection.present || projection.value == null) {
    return emptyTriggerDraft(kind)
  }
  switch (kind) {
    case "manual":
      return { kind, enabled: true }
    case "schedule":
      return {
        kind,
        enabled: true,
        crons: (projection.value as WorkflowTriggerValueMap["schedule"]).map(
          (schedule) => ({
            id: newRowID("schedule"),
            value: schedule.cron ?? "",
          }),
        ),
      }
    case "channel_message": {
      const value =
        projection.value as WorkflowTriggerValueMap["channel_message"]
      return {
        kind,
        enabled: true,
        channels: listText(value.channels),
        chats: listText(value.chats),
        senders: listText(value.senders),
        mentioned: triStateFrom(value.mentioned),
        command: value.command ?? "",
        textMatches: value.text_matches ?? "",
        passthrough: triStateFrom(value.passthrough),
        session: conversationSessionFrom(value.conversation?.session),
        delivery: conversationDeliveryFrom(value.conversation?.delivery),
      }
    }
    case "command": {
      const value = projection.value as WorkflowTriggerValueMap["command"]
      return {
        kind,
        enabled: true,
        name: value.name ?? "",
        channels: listText(value.channels),
        chats: listText(value.chats),
        senders: listText(value.senders),
        args: definitionRowsFrom("argument", value.args),
        passthrough: triStateFrom(value.passthrough),
        session: conversationSessionFrom(value.conversation?.session),
        delivery: conversationDeliveryFrom(value.conversation?.delivery),
      }
    }
    case "runtime_event": {
      const value = projection.value as WorkflowTriggerValueMap["runtime_event"]
      return {
        kind,
        enabled: true,
        kinds: listText(value.kinds),
        sources: listText(value.sources),
        agents: listText(value.agents),
        sessions: listText(value.sessions),
        channels: listText(value.channels),
        chats: listText(value.chats),
      }
    }
    case "event": {
      const value = projection.value as WorkflowTriggerValueMap["event"]
      return {
        kind,
        enabled: true,
        sources: listText(value.sources),
        connectors: listText(value.connectors),
        types: listText(value.types),
        attributes: attributeRowsFrom("event", value.attributes),
        actor: entityDraftFrom("actor", value.actor),
        subject: entityDraftFrom("subject", value.subject),
      }
    }
    case "workflow_call": {
      const value = projection.value as WorkflowTriggerValueMap["workflow_call"]
      return {
        kind,
        enabled: true,
        inputs: definitionRowsFrom("input", value.inputs),
        secrets: Object.entries(value.secrets ?? {})
          .sort(([left], [right]) => left.localeCompare(right))
          .map(([name, secret]) => ({
            id: newRowID("secret"),
            name,
            required: secret.required === true,
          })),
        outputs: Object.entries(value.outputs ?? {})
          .sort(([left], [right]) => left.localeCompare(right))
          .map(([name, output]) => ({
            id: newRowID("output"),
            name,
            value: output.value ?? "",
          })),
      }
    }
  }
}

function triggerFromDraft(draft: TriggerDraft): {
  value: WorkflowTriggerValueMap[WorkflowTriggerKind] | null
  errors: string[]
} {
  if (!draft.enabled) {
    return { value: null, errors: [] }
  }
  const errors: string[] = []
  switch (draft.kind) {
    case "manual":
      return { value: {}, errors }
    case "schedule": {
      const value = draft.crons.map((row, index) => {
        if (row.value.trim() === "") {
          errors.push(`Cron schedule ${index + 1} requires an expression.`)
        }
        return { cron: row.value }
      })
      return { value, errors: unique(errors) }
    }
    case "channel_message": {
      const value: WorkflowChannelMessageTrigger = {}
      setList(value, "channels", draft.channels)
      setList(value, "chats", draft.chats)
      setList(value, "senders", draft.senders)
      setTriState(value, "mentioned", draft.mentioned)
      setString(value, "command", draft.command)
      setString(value, "text_matches", draft.textMatches)
      setTriState(value, "passthrough", draft.passthrough)
      setConversation(value, draft.session, draft.delivery)
      return { value, errors }
    }
    case "command": {
      const value: WorkflowCommandTrigger = {}
      if (draft.name.trim() === "") {
        errors.push("Command name is required.")
      } else {
        value.name = draft.name
      }
      setList(value, "channels", draft.channels)
      setList(value, "chats", draft.chats)
      setList(value, "senders", draft.senders)
      value.args = definitionsFromRows("Command arguments", draft.args, errors)
      if (Object.keys(value.args).length === 0) {
        delete value.args
      }
      setTriState(value, "passthrough", draft.passthrough)
      setConversation(value, draft.session, draft.delivery)
      return { value, errors: unique(errors) }
    }
    case "runtime_event": {
      const value: WorkflowRuntimeEventTrigger = {}
      setList(value, "kinds", draft.kinds)
      setList(value, "sources", draft.sources)
      setList(value, "agents", draft.agents)
      setList(value, "sessions", draft.sessions)
      setList(value, "channels", draft.channels)
      setList(value, "chats", draft.chats)
      if (Object.keys(value).length === 0) {
        errors.push("Add at least one runtime event filter.")
      }
      return { value, errors }
    }
    case "event": {
      const value: WorkflowEventTrigger = {}
      setList(value, "sources", draft.sources)
      setList(value, "connectors", draft.connectors)
      setList(value, "types", draft.types)
      const attributes = attributesFromRows(
        "Event attributes",
        draft.attributes,
        errors,
      )
      if (attributes != null) {
        value.attributes = attributes
      }
      const actor = entityFromDraft("Actor", draft.actor, errors)
      if (actor != null) {
        value.actor = actor
      }
      const subject = entityFromDraft("Subject", draft.subject, errors)
      if (subject != null) {
        value.subject = subject
      }
      if (!eventTriggerHasFilter(value)) {
        errors.push(
          'Add at least one filter. Use event type "*" for an explicit catch-all.',
        )
      }
      return { value, errors: unique(errors) }
    }
    case "workflow_call": {
      const value: WorkflowCallTrigger = {}
      const inputs = definitionsFromRows("Inputs", draft.inputs, errors)
      if (Object.keys(inputs).length > 0) {
        value.inputs = inputs
      }
      const secrets = requiredMapFromRows("Secrets", draft.secrets, errors)
      if (Object.keys(secrets).length > 0) {
        value.secrets = secrets
      }
      const outputs = outputMapFromRows(draft.outputs, errors)
      if (Object.keys(outputs).length > 0) {
        value.outputs = outputs
      }
      return { value, errors: unique(errors) }
    }
  }
}

function definitionRowsFrom(
  scope: string,
  values: Record<string, WorkflowInputDefinition> | undefined,
) {
  return Object.entries(values ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, input]) => {
      const type = inputTypeFrom(input.type)
      return {
        id: newRowID(scope),
        name,
        type,
        hasType: Object.hasOwn(input, "type"),
        required: input.required === true,
        hasDefault: Object.hasOwn(input, "default"),
        defaultValue: defaultText(input.default, type),
      }
    })
}

function definitionsFromRows(
  label: string,
  rows: DefinitionRow[],
  errors: string[],
) {
  const result = Object.create(null) as Record<string, WorkflowInputDefinition>
  for (const [index, row] of rows.entries()) {
    const name = row.name
    if (name.trim() === "") {
      errors.push(`${label} row ${index + 1} needs a name.`)
      continue
    }
    if (Object.hasOwn(result, name)) {
      errors.push(`${label} contains duplicate name "${name}".`)
      continue
    }
    const input: WorkflowInputDefinition = {}
    if (row.hasType) {
      input.type = row.type
    }
    if (row.required) {
      input.required = true
    }
    if (row.hasDefault) {
      const parsed = parseDefaultValue(row, label, index, errors)
      if (parsed.ok) {
        input.default = parsed.value
      }
    }
    result[name] = input
  }
  return result
}

function parseDefaultValue(
  row: DefinitionRow,
  label: string,
  index: number,
  errors: string[],
): { ok: true; value: unknown } | { ok: false } {
  switch (row.type) {
    case "string":
      return { ok: true, value: row.defaultValue }
    case "boolean":
      return { ok: true, value: row.defaultValue === "true" }
    case "number": {
      const raw = row.defaultValue.trim()
      const value = Number(raw)
      if (
        raw === "" ||
        !/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(raw) ||
        !Number.isFinite(value)
      ) {
        errors.push(`${label} row ${index + 1} needs a finite number default.`)
        return { ok: false }
      }
      if (Number.isInteger(value) && !Number.isSafeInteger(value)) {
        errors.push(
          `${label} row ${index + 1} number default must be a safe integer.`,
        )
        return { ok: false }
      }
      if (raw !== String(value)) {
        errors.push(
          `${label} row ${index + 1} number default must use canonical notation, such as ${String(value)}.`,
        )
        return { ok: false }
      }
      return { ok: true, value }
    }
    case "object":
    case "array": {
      const parsed = parseStrictJSONDefault(
        row.defaultValue,
        label,
        index,
        errors,
      )
      if (!parsed.ok) {
        return { ok: false }
      }
      if (
        row.type === "array"
          ? !Array.isArray(parsed.value)
          : parsed.value == null ||
            Array.isArray(parsed.value) ||
            typeof parsed.value !== "object"
      ) {
        errors.push(
          `${label} row ${index + 1} default must be a JSON ${row.type}.`,
        )
        return { ok: false }
      }
      return parsed
    }
    default:
      return parseStrictJSONDefault(row.defaultValue, label, index, errors)
  }
}

function parseStrictJSONDefault(
  raw: string,
  label: string,
  index: number,
  errors: string[],
): { ok: true; value: unknown } | { ok: false } {
  try {
    const value = JSON.parse(raw) as unknown
    const tokenInspection = inspectStrictJSONTokens(raw)
    if (tokenInspection.duplicateKey != null) {
      errors.push(
        `${label} row ${index + 1} default contains duplicate object key ${JSON.stringify(tokenInspection.duplicateKey)}.`,
      )
      return { ok: false }
    }
    if (tokenInspection.unsafeNumber != null) {
      errors.push(
        `${label} row ${index + 1} default contains numeric token ${JSON.stringify(tokenInspection.unsafeNumber)} that cannot be represented exactly in the browser or as a safe integer.`,
      )
      return { ok: false }
    }
    if (value == null) {
      errors.push(
        `${label} row ${index + 1} default must not be top-level null.`,
      )
      return { ok: false }
    }
    if (containsInvalidJSONNumber(value)) {
      errors.push(
        `${label} row ${index + 1} default contains a non-finite number or unsafe integer.`,
      )
      return { ok: false }
    }
    return { ok: true, value }
  } catch {
    errors.push(`${label} row ${index + 1} default contains invalid JSON.`)
    return { ok: false }
  }
}

function inspectStrictJSONTokens(raw: string): {
  duplicateKey: string | null
  unsafeNumber: string | null
} {
  let index = 0
  let duplicate: string | null = null
  let unsafeNumber: string | null = null

  const skipWhitespace = () => {
    while (/\s/.test(raw[index] ?? "")) {
      index += 1
    }
  }
  const readString = () => {
    const start = index
    index += 1
    while (index < raw.length) {
      if (raw[index] === "\\") {
        index += 2
        continue
      }
      if (raw[index] === '"') {
        index += 1
        return JSON.parse(raw.slice(start, index)) as string
      }
      index += 1
    }
    return ""
  }
  const readValue = () => {
    skipWhitespace()
    switch (raw[index]) {
      case "{": {
        index += 1
        skipWhitespace()
        const keys = new Set<string>()
        if (raw[index] === "}") {
          index += 1
          return
        }
        while (index < raw.length) {
          skipWhitespace()
          const key = readString()
          if (keys.has(key) && duplicate == null) {
            duplicate = key
          }
          keys.add(key)
          skipWhitespace()
          index += 1
          readValue()
          skipWhitespace()
          if (raw[index] === "}") {
            index += 1
            return
          }
          index += 1
        }
        return
      }
      case "[":
        index += 1
        skipWhitespace()
        if (raw[index] === "]") {
          index += 1
          return
        }
        while (index < raw.length) {
          readValue()
          skipWhitespace()
          if (raw[index] === "]") {
            index += 1
            return
          }
          index += 1
        }
        return
      case '"':
        readString()
        return
      default: {
        const token = raw
          .slice(index)
          .match(
            /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/,
          )?.[0]
        if (
          token != null &&
          jsonNumberTokenPattern.test(token) &&
          unsafeNumber == null &&
          !workflowJSONNumberIsBrowserSafe(token)
        ) {
          unsafeNumber = token
        }
        index += token?.length ?? 1
      }
    }
  }

  readValue()
  return { duplicateKey: duplicate, unsafeNumber }
}

const jsonNumberTokenPattern = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/

function containsInvalidJSONNumber(value: unknown): boolean {
  if (typeof value === "number") {
    return (
      !Number.isFinite(value) ||
      (Number.isInteger(value) && !Number.isSafeInteger(value))
    )
  }
  if (Array.isArray(value)) {
    return value.some(containsInvalidJSONNumber)
  }
  if (value != null && typeof value === "object") {
    return Object.values(value).some(containsInvalidJSONNumber)
  }
  return false
}

function requiredMapFromRows(
  label: string,
  rows: RequiredRow[],
  errors: string[],
) {
  const result = Object.create(null) as Record<string, { required?: boolean }>
  for (const [index, row] of rows.entries()) {
    if (row.name.trim() === "") {
      errors.push(`${label} row ${index + 1} needs a name.`)
      continue
    }
    if (Object.hasOwn(result, row.name)) {
      errors.push(`${label} contains duplicate name "${row.name}".`)
      continue
    }
    result[row.name] = row.required ? { required: true } : {}
  }
  return result
}

function outputMapFromRows(rows: OutputRow[], errors: string[]) {
  const result = Object.create(null) as Record<string, { value?: string }>
  for (const [index, row] of rows.entries()) {
    if (row.name.trim() === "") {
      errors.push(`Outputs row ${index + 1} needs a name.`)
      continue
    }
    if (Object.hasOwn(result, row.name)) {
      errors.push(`Outputs contains duplicate name "${row.name}".`)
      continue
    }
    if (row.value.trim() === "") {
      errors.push(`Output "${row.name}" needs a value or expression.`)
      continue
    }
    result[row.name] = { value: row.value }
  }
  return result
}

function entityDraftFrom(
  scope: string,
  value: WorkflowEventEntityTrigger | undefined,
): EntityDraft {
  if (value == null) {
    return emptyEntityDraft()
  }
  return {
    enabled: true,
    ids: listText(value.ids),
    types: listText(value.types),
    attributes: attributeRowsFrom(scope, value.attributes),
  }
}

function emptyEntityDraft(): EntityDraft {
  return { enabled: false, ids: "", types: "", attributes: [] }
}

function entityFromDraft(
  label: string,
  draft: EntityDraft,
  errors: string[],
): WorkflowEventEntityTrigger | undefined {
  if (!draft.enabled) {
    return undefined
  }
  const value: WorkflowEventEntityTrigger = {}
  setList(value, "ids", draft.ids)
  setList(value, "types", draft.types)
  const attributes = attributesFromRows(
    `${label} attributes`,
    draft.attributes,
    errors,
  )
  if (attributes != null) {
    value.attributes = attributes
  }
  if (!entityHasFilter(value)) {
    errors.push(`${label} filters require at least one ID, type, or attribute.`)
  }
  return value
}

function attributeRowsFrom(
  scope: string,
  values: Record<string, string[]> | undefined,
) {
  return Object.entries(values ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, patterns]) => ({
      id: newRowID(scope),
      key,
      patterns: listText(patterns),
    }))
}

function attributesFromRows(
  label: string,
  rows: AttributeRow[],
  errors: string[],
) {
  const result = Object.create(null) as Record<string, string[]>
  for (const [index, row] of rows.entries()) {
    const patterns = parseList(row.patterns)
    if (row.key.trim() === "") {
      errors.push(`${label} row ${index + 1} needs an attribute name.`)
      continue
    }
    if (Object.hasOwn(result, row.key)) {
      errors.push(`${label} contains duplicate attribute "${row.key}".`)
      continue
    }
    if (patterns.length === 0) {
      errors.push(`${label} "${row.key}" needs a value pattern.`)
      continue
    }
    result[row.key] = patterns
  }
  return Object.keys(result).length > 0 ? result : undefined
}

function eventTriggerHasFilter(value: WorkflowEventTrigger) {
  return (
    hasList(value.sources) ||
    hasList(value.connectors) ||
    hasList(value.types) ||
    hasAttributeMap(value.attributes) ||
    entityHasFilter(value.actor) ||
    entityHasFilter(value.subject)
  )
}

function entityHasFilter(value: WorkflowEventEntityTrigger | undefined) {
  return (
    value != null &&
    (hasList(value.ids) ||
      hasList(value.types) ||
      hasAttributeMap(value.attributes))
  )
}

function hasList(value: string[] | undefined) {
  return (value?.length ?? 0) > 0
}

function hasAttributeMap(value: Record<string, string[]> | undefined) {
  return Object.values(value ?? {}).some((patterns) => patterns.length > 0)
}

function setConversation(
  target: WorkflowChannelMessageTrigger | WorkflowCommandTrigger,
  session: ConversationSession,
  delivery: ConversationDelivery,
) {
  const conversation: NonNullable<
    WorkflowChannelMessageTrigger["conversation"]
  > = {}
  if (session !== "default") {
    conversation.session = session
  }
  if (delivery !== "default") {
    conversation.delivery = delivery
  }
  if (Object.keys(conversation).length > 0) {
    target.conversation = conversation
  }
}

function setList<Target extends object, Key extends keyof Target>(
  target: Target,
  key: Key,
  raw: string,
) {
  const values = parseList(raw)
  if (values.length > 0) {
    target[key] = values as Target[Key]
  }
}

function setString<Target extends object, Key extends keyof Target>(
  target: Target,
  key: Key,
  raw: string,
) {
  if (raw.trim() !== "") {
    target[key] = raw as Target[Key]
  }
}

function setTriState<Target extends object, Key extends keyof Target>(
  target: Target,
  key: Key,
  value: TriState,
) {
  if (value !== "default") {
    target[key] = (value === "true") as Target[Key]
  }
}

function parseList(value: string) {
  return value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean)
}

function listText(value: string[] | undefined) {
  return (value ?? []).join("\n")
}

function triStateFrom(value: boolean | undefined): TriState {
  return value == null ? "default" : value ? "true" : "false"
}

function conversationSessionFrom(
  value: string | undefined,
): ConversationSession {
  return value == null || value === "" ? "default" : value
}

function conversationDeliveryFrom(
  value: string | undefined,
): ConversationDelivery {
  return value == null || value === "" ? "default" : value
}

function inputTypeFrom(value: string | undefined): InputType {
  return value == null || value === "" ? "string" : value
}

function defaultText(value: unknown, type: InputType) {
  if (value === undefined) {
    return type === "boolean" ? "false" : ""
  }
  if (type === "object" || type === "array" || !isKnownInputType(type)) {
    return JSON.stringify(value, null, 2)
  }
  return String(value)
}

function defaultValueForType(
  type: InputType,
  current: string,
  previousType: InputType,
) {
  let normalizedCurrent = current
  if (!isKnownInputType(previousType)) {
    try {
      const parsed = JSON.parse(current) as unknown
      if (type === "string" && typeof parsed === "string") {
        normalizedCurrent = parsed
      } else if (type === "number" && typeof parsed === "number") {
        normalizedCurrent = String(parsed)
      } else if (type === "boolean" && typeof parsed === "boolean") {
        normalizedCurrent = String(parsed)
      } else if (type === "object" || type === "array") {
        normalizedCurrent = JSON.stringify(parsed, null, 2)
      }
    } catch {
      // Keep the current text so the user can correct it explicitly.
    }
  }
  if (type === "boolean") {
    return normalizedCurrent === "true" ? "true" : "false"
  }
  if (type === "object") {
    return normalizedCurrent.trim().startsWith("{") ? normalizedCurrent : "{}"
  }
  if (type === "array") {
    return normalizedCurrent.trim().startsWith("[") ? normalizedCurrent : "[]"
  }
  return normalizedCurrent
}

function emptyDefinitionRow(scope: string): DefinitionRow {
  return {
    id: newRowID(scope),
    name: "",
    type: "string",
    hasType: true,
    required: false,
    hasDefault: false,
    defaultValue: "",
  }
}

function replaceRow<Row>(rows: Row[], index: number, next: Row) {
  return rows.map((row, rowIndex) => (rowIndex === index ? next : row))
}

function triggerDraftSignature(draft: TriggerDraft) {
  return JSON.stringify(draft, (key, value: unknown) =>
    key === "id" ? undefined : value,
  )
}

function unique(values: string[]) {
  return Array.from(new Set(values))
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function inspectionState(
  yaml: string,
  inspection: WorkflowTriggersInspection,
): WorkflowTriggerInspectionState {
  return {
    yaml,
    status: "ready",
    eventTriggerPresent: inspection.triggers.event.present,
    inspection,
  }
}

function triggerRenderErrorMessage(error: unknown) {
  if (!(error instanceof WorkflowAPIError)) {
    return errorMessage(error)
  }
  switch (error.message) {
    case "workflow_trigger_revision_mismatch":
      return "The workflow YAML changed. Review the latest draft and apply again."
    case "workflow_trigger_raw_only":
      return "This trigger now requires the authoritative YAML editor."
    case "invalid_workflow_trigger":
      return "The trigger is invalid. Review its fields and validation errors."
    case "unsupported_trigger_type":
      return "This trigger type is not supported by the current server."
    case "invalid_trigger_request":
      return "The trigger request is invalid. Inspect the latest YAML and try again."
    default:
      return error.message
  }
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Workflow request failed"
}
