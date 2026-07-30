import { IconCheck, IconRefresh, IconX } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react"

import { type EventView, listEvents } from "@/api/events"
import {
  type WorkflowDevelopmentSession,
  type WorkflowTriggerKind,
  type WorkflowTriggerMessageEnvelope,
  type WorkflowTriggerRuntimeEventEnvelope,
  type WorkflowTriggerSimulationRequest,
  type WorkflowTriggerSimulationResponse,
  simulateWorkflowDevelopmentTrigger,
  workflowTriggerKinds,
  workflowTriggerSimulationIdentity,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

import { workflowJSONNumberIsBrowserSafe } from "./workflow-json-number"
import type { WorkflowTriggerInspectionState } from "./workflow-trigger-editor"

export type WorkflowTriggerSimulatorState =
  | { status: "idle" | "loading"; message: string }
  | { status: "error"; message: string }
  | {
      status: "ready"
      message: string
      request: WorkflowTriggerSimulationRequest
      response: WorkflowTriggerSimulationResponse
      identity: string
    }

interface InvocationDraft {
  inputs: string
  secrets: string
  session: string
  delivery: string
}

interface MessageDraft {
  channel: string
  account: string
  chatID: string
  chatType: string
  topicID: string
  spaceID: string
  spaceType: string
  senderID: string
  senderUsername: string
  senderName: string
  messageID: string
  replyToMessageID: string
  mentioned: boolean
  text: string
  additional: string
}

interface RuntimeEventDraft {
  id: string
  kind: string
  time: string
  sourceComponent: string
  sourceName: string
  severity: string
  scope: string
  correlation: string
  payload: string
  attrs: string
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

const initialInvocationDraft: InvocationDraft = {
  inputs: "{}",
  secrets: "{}",
  session: "",
  delivery: "{}",
}

function initialMessageDraft(): MessageDraft {
  return {
    channel: "",
    account: "",
    chatID: "",
    chatType: "",
    topicID: "",
    spaceID: "",
    spaceType: "",
    senderID: "",
    senderUsername: "",
    senderName: "",
    messageID: "",
    replyToMessageID: "",
    mentioned: false,
    text: "",
    additional: "{}",
  }
}

function initialRuntimeEventDraft(): RuntimeEventDraft {
  return {
    id: "simulated-runtime-event",
    kind: "workflow.simulation",
    time: new Date().toISOString(),
    sourceComponent: "dashboard",
    sourceName: "workflow-simulator",
    severity: "info",
    scope: "{}",
    correlation: "{}",
    payload: "{}",
    attrs: "{}",
  }
}

export function WorkflowTriggerSimulator({
  session,
  prompt,
  targetRef,
  yaml,
  inspectionState,
  disabled,
  onSimulationChange,
}: {
  session: WorkflowDevelopmentSession
  prompt: string
  targetRef: string
  yaml: string
  inspectionState: WorkflowTriggerInspectionState
  disabled: boolean
  onSimulationChange: (state: WorkflowTriggerSimulatorState) => void
}) {
  const [selectedKind, setSelectedKind] = useState<WorkflowTriggerKind | "">("")
  const [scheduleIndex, setScheduleIndex] = useState("")
  const [scheduledAt, setScheduledAt] = useState(() => new Date().toISOString())
  const [manual, setManual] = useState<InvocationDraft>(initialInvocationDraft)
  const [workflowCall, setWorkflowCall] = useState<InvocationDraft>(
    initialInvocationDraft,
  )
  const [channelMessage, setChannelMessage] =
    useState<MessageDraft>(initialMessageDraft)
  const [command, setCommand] = useState<MessageDraft>(initialMessageDraft)
  const [runtimeEvent, setRuntimeEvent] = useState<RuntimeEventDraft>(
    initialRuntimeEventDraft,
  )
  const [eventID, setEventID] = useState("")
  const [state, setState] = useState<WorkflowTriggerSimulatorState>({
    status: "idle",
    message: "Choose a trigger scenario to simulate.",
  })
  const controllerRef = useRef<AbortController | null>(null)
  const generationRef = useRef(0)
  const latestRequestIdentityRef = useRef("")
  const onSimulationChangeRef = useRef(onSimulationChange)
  const selectionWasExplicitRef = useRef(false)

  useEffect(() => {
    onSimulationChangeRef.current = onSimulationChange
  }, [onSimulationChange])

  const inspection =
    inspectionState.status === "ready" && inspectionState.yaml === yaml
      ? inspectionState.inspection
      : undefined
  const presentKinds = useMemo(
    () =>
      workflowTriggerKinds.filter(
        (kind) => inspection?.triggers[kind].present === true,
      ),
    [inspection],
  )
  const presentKindsIdentity = presentKinds.join("\u0000")

  useEffect(() => {
    setSelectedKind((current) => {
      if (presentKinds.length === 1) {
        selectionWasExplicitRef.current = false
        return presentKinds[0]
      }
      if (
        selectionWasExplicitRef.current &&
        current !== "" &&
        presentKinds.includes(current)
      ) {
        return current
      }
      selectionWasExplicitRef.current = false
      return ""
    })
  }, [presentKinds, presentKindsIdentity])

  const schedules =
    inspection?.triggers.schedule.present === true
      ? (inspection.triggers.schedule.value ?? [])
      : []
  useEffect(() => {
    if (selectedKind !== "schedule") {
      return
    }
    setScheduleIndex((current) => {
      if (schedules.length === 1) {
        return "0"
      }
      const parsed = Number(current)
      return Number.isInteger(parsed) &&
        parsed >= 0 &&
        parsed < schedules.length
        ? current
        : ""
    })
  }, [schedules.length, selectedKind])

  const eventsQuery = useQuery({
    queryKey: ["events", "workflow-trigger-simulator"],
    queryFn: () => listEvents({ limit: 20 }),
    enabled: selectedKind === "event",
    staleTime: 5000,
    refetchOnWindowFocus: false,
  })
  const events = eventsQuery.data?.events ?? []

  const candidate = useMemo(
    () =>
      workflowTriggerSimulationCandidate({
        session,
        prompt,
        targetRef,
        yaml,
        inspectionReady: inspection != null,
        presentKinds,
        selectedKind,
        scheduleIndex,
        scheduledAt,
        manual,
        workflowCall,
        channelMessage,
        command,
        runtimeEvent,
        eventID,
      }),
    [
      channelMessage,
      command,
      eventID,
      inspection,
      manual,
      presentKinds,
      prompt,
      runtimeEvent,
      scheduleIndex,
      scheduledAt,
      selectedKind,
      session,
      targetRef,
      workflowCall,
      yaml,
    ],
  )

  useEffect(() => {
    generationRef.current += 1
    const generation = generationRef.current
    controllerRef.current?.abort()
    controllerRef.current = null
    latestRequestIdentityRef.current =
      candidate.request == null
        ? ""
        : workflowTriggerSimulationIdentity(candidate.request)
    if (candidate.request == null) {
      const next: WorkflowTriggerSimulatorState = {
        status: "idle",
        message: candidate.message,
      }
      setState(next)
      onSimulationChangeRef.current(next)
      return
    }
    if (disabled) {
      const next: WorkflowTriggerSimulatorState = {
        status: "idle",
        message: "Finish the current workflow operation before simulating.",
      }
      setState(next)
      onSimulationChangeRef.current(next)
      return
    }

    const request = candidate.request
    const requestIdentity = workflowTriggerSimulationIdentity(request)
    const loading: WorkflowTriggerSimulatorState = {
      status: "loading",
      message: "Simulating this exact trigger scenario…",
    }
    setState(loading)
    onSimulationChangeRef.current(loading)
    const controller = new AbortController()
    controllerRef.current = controller
    const timeout = window.setTimeout(() => {
      void simulateWorkflowDevelopmentTrigger(request, controller.signal)
        .then((response) => {
          if (
            controller.signal.aborted ||
            generationRef.current !== generation ||
            latestRequestIdentityRef.current !== requestIdentity
          ) {
            return
          }
          const next: WorkflowTriggerSimulatorState = {
            status: "ready",
            message: workflowTriggerSimulationMessage(response),
            request,
            response,
            identity: workflowTriggerSimulationIdentity(request, response),
          }
          setState(next)
          onSimulationChangeRef.current(next)
        })
        .catch((error: unknown) => {
          if (
            controller.signal.aborted ||
            generationRef.current !== generation ||
            latestRequestIdentityRef.current !== requestIdentity
          ) {
            return
          }
          const next: WorkflowTriggerSimulatorState = {
            status: "error",
            message: errorMessage(error),
          }
          setState(next)
          onSimulationChangeRef.current(next)
        })
    }, 250)

    return () => {
      window.clearTimeout(timeout)
      controller.abort()
    }
  }, [candidate, disabled])

  useEffect(
    () => () => {
      generationRef.current += 1
      controllerRef.current?.abort()
      onSimulationChangeRef.current({
        status: "idle",
        message: "The trigger simulator is not active.",
      })
    },
    [],
  )

  const invalidate = () => {
    generationRef.current += 1
    controllerRef.current?.abort()
    latestRequestIdentityRef.current = ""
    const next: WorkflowTriggerSimulatorState = {
      status: "idle",
      message: "Scenario changed. Waiting to simulate the new values…",
    }
    setState(next)
    onSimulationChangeRef.current(next)
  }

  const chooseKind = (value: string) => {
    invalidate()
    selectionWasExplicitRef.current = value !== ""
    setSelectedKind(value as WorkflowTriggerKind | "")
  }

  return (
    <div className="grid min-w-0 gap-3">
      {inspectionState.yaml !== yaml || inspectionState.status === "loading" ? (
        <SimulatorStatus
          status="loading"
          message="Inspecting the current workflow triggers…"
        />
      ) : inspectionState.status === "error" ? (
        <SimulatorStatus
          status="error"
          message={`Workflow trigger inspection failed: ${inspectionState.reason ?? "unknown error"}`}
        />
      ) : presentKinds.length === 0 ? (
        <SimulatorStatus
          status="error"
          message="The current workflow does not declare a supported trigger."
        />
      ) : (
        <>
          <div className="grid gap-2">
            <Label htmlFor="workflow-simulation-trigger">
              Trigger scenario
            </Label>
            <select
              id="workflow-simulation-trigger"
              value={selectedKind}
              onChange={(event) => chooseKind(event.target.value)}
              disabled={disabled}
              className="border-input bg-background ring-offset-background focus-visible:ring-ring min-h-9 w-full rounded-md border px-3 py-1 text-sm shadow-xs outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {presentKinds.length > 1 ? (
                <option value="">Select one present trigger</option>
              ) : null}
              {presentKinds.map((kind) => (
                <option key={kind} value={kind}>
                  {triggerLabels[kind]}
                </option>
              ))}
            </select>
            {presentKinds.length > 1 && selectedKind === "" ? (
              <p className="text-muted-foreground text-xs">
                This draft has several triggers. Select the exact family to
                simulate; the dashboard will not guess.
              </p>
            ) : null}
          </div>

          {selectedKind === "manual" ? (
            <InvocationFields
              idPrefix="workflow-simulation-manual"
              value={manual}
              disabled={disabled}
              onChange={(next) => {
                invalidate()
                setManual(next)
              }}
            />
          ) : selectedKind === "workflow_call" ? (
            <InvocationFields
              idPrefix="workflow-simulation-call"
              value={workflowCall}
              disabled={disabled}
              onChange={(next) => {
                invalidate()
                setWorkflowCall(next)
              }}
            />
          ) : selectedKind === "schedule" ? (
            <ScheduleFields
              schedules={schedules}
              scheduleIndex={scheduleIndex}
              scheduledAt={scheduledAt}
              disabled={disabled}
              onScheduleIndexChange={(value) => {
                invalidate()
                setScheduleIndex(value)
              }}
              onScheduledAtChange={(value) => {
                invalidate()
                setScheduledAt(value)
              }}
            />
          ) : selectedKind === "channel_message" ? (
            <MessageFields
              idPrefix="workflow-simulation-channel"
              value={channelMessage}
              disabled={disabled}
              onChange={(next) => {
                invalidate()
                setChannelMessage(next)
              }}
            />
          ) : selectedKind === "command" ? (
            <MessageFields
              idPrefix="workflow-simulation-command"
              value={command}
              disabled={disabled}
              onChange={(next) => {
                invalidate()
                setCommand(next)
              }}
            />
          ) : selectedKind === "runtime_event" ? (
            <RuntimeEventFields
              value={runtimeEvent}
              disabled={disabled}
              onChange={(next) => {
                invalidate()
                setRuntimeEvent(next)
              }}
            />
          ) : selectedKind === "event" ? (
            <DurableEventFields
              events={events}
              eventID={eventID}
              loading={eventsQuery.isPending}
              error={eventsQuery.error}
              disabled={disabled}
              onChange={(value) => {
                invalidate()
                setEventID(value)
              }}
            />
          ) : null}

          <SimulatorStatus status={state.status} message={state.message}>
            {state.status === "ready" ? (
              <div className="mt-2 flex flex-wrap gap-1.5">
                <Badge variant="outline">
                  {state.response.review.job_count} jobs
                </Badge>
                <Badge variant="outline">
                  {state.response.review.step_count} steps
                </Badge>
                <Badge variant="outline">
                  {state.response.simulation.context_summary.input_count} inputs
                </Badge>
                <Badge variant="outline">
                  {state.response.simulation.context_summary.secret_count}{" "}
                  provided secrets
                </Badge>
              </div>
            ) : null}
          </SimulatorStatus>
        </>
      )}
    </div>
  )
}

function InvocationFields({
  idPrefix,
  value,
  disabled,
  onChange,
}: {
  idPrefix: string
  value: InvocationDraft
  disabled: boolean
  onChange: (value: InvocationDraft) => void
}) {
  return (
    <div className="grid gap-3">
      <JSONField
        id={`${idPrefix}-inputs`}
        label="Inputs JSON"
        value={value.inputs}
        disabled={disabled}
        onChange={(inputs) => onChange({ ...value, inputs })}
      />
      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-session`}>Session</Label>
        <Input
          id={`${idPrefix}-session`}
          value={value.session}
          disabled={disabled}
          placeholder="workflow:test"
          onChange={(event) =>
            onChange({ ...value, session: event.target.value })
          }
          className="font-mono text-xs"
        />
      </div>
      <JSONField
        id={`${idPrefix}-delivery`}
        label="Delivery JSON"
        value={value.delivery}
        disabled={disabled}
        onChange={(delivery) => onChange({ ...value, delivery })}
      />
      <JSONField
        id={`${idPrefix}-secrets`}
        label="Secrets JSON"
        value={value.secrets}
        disabled={disabled}
        onChange={(secrets) => onChange({ ...value, secrets })}
        secret
      />
    </div>
  )
}

function ScheduleFields({
  schedules,
  scheduleIndex,
  scheduledAt,
  disabled,
  onScheduleIndexChange,
  onScheduledAtChange,
}: {
  schedules: Array<{ cron?: string }>
  scheduleIndex: string
  scheduledAt: string
  disabled: boolean
  onScheduleIndexChange: (value: string) => void
  onScheduledAtChange: (value: string) => void
}) {
  return (
    <div className="grid gap-3">
      <div className="grid gap-2">
        <Label htmlFor="workflow-simulation-schedule-index">
          Declared schedule
        </Label>
        <select
          id="workflow-simulation-schedule-index"
          value={scheduleIndex}
          disabled={disabled}
          onChange={(event) => onScheduleIndexChange(event.target.value)}
          className="border-input bg-background min-h-9 w-full rounded-md border px-3 py-1 text-sm"
        >
          {schedules.length !== 1 ? (
            <option value="">Select an exact schedule</option>
          ) : null}
          {schedules.map((schedule, index) => (
            <option key={`${index}:${schedule.cron ?? ""}`} value={index}>
              #{index + 1} · {schedule.cron || "schedule"}
            </option>
          ))}
        </select>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="workflow-simulation-scheduled-at">
          Scheduled time (RFC 3339)
        </Label>
        <Input
          id="workflow-simulation-scheduled-at"
          value={scheduledAt}
          disabled={disabled}
          onChange={(event) => onScheduledAtChange(event.target.value)}
          className="font-mono text-xs"
        />
      </div>
    </div>
  )
}

function MessageFields({
  idPrefix,
  value,
  disabled,
  onChange,
}: {
  idPrefix: string
  value: MessageDraft
  disabled: boolean
  onChange: (value: MessageDraft) => void
}) {
  const fields: Array<[keyof MessageDraft, string]> = [
    ["channel", "Channel"],
    ["account", "Account"],
    ["chatID", "Chat ID"],
    ["chatType", "Chat type"],
    ["topicID", "Topic ID"],
    ["spaceID", "Space ID"],
    ["spaceType", "Space type"],
    ["senderID", "Sender ID"],
    ["senderUsername", "Sender username"],
    ["senderName", "Sender name"],
    ["messageID", "Message ID"],
    ["replyToMessageID", "Reply-to message ID"],
  ]
  return (
    <div className="grid gap-3">
      <div className="grid gap-3 sm:grid-cols-2">
        {fields.map(([field, label]) => (
          <div key={field} className="grid gap-2">
            <Label htmlFor={`${idPrefix}-${field}`}>{label}</Label>
            <Input
              id={`${idPrefix}-${field}`}
              value={String(value[field])}
              disabled={disabled}
              onChange={(event) =>
                onChange({ ...value, [field]: event.target.value })
              }
              className="font-mono text-xs"
            />
          </div>
        ))}
      </div>
      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-text`}>Message text</Label>
        <Textarea
          id={`${idPrefix}-text`}
          value={value.text}
          disabled={disabled}
          onChange={(event) => onChange({ ...value, text: event.target.value })}
          className="min-h-20 resize-none"
        />
      </div>
      <div className="border-border flex items-center justify-between rounded-md border p-3">
        <Label htmlFor={`${idPrefix}-mentioned`}>Agent was mentioned</Label>
        <Switch
          id={`${idPrefix}-mentioned`}
          checked={value.mentioned}
          disabled={disabled}
          onCheckedChange={(mentioned) => onChange({ ...value, mentioned })}
        />
      </div>
      <JSONField
        id={`${idPrefix}-additional`}
        label="Additional message envelope JSON"
        value={value.additional}
        disabled={disabled}
        onChange={(additional) => onChange({ ...value, additional })}
      />
    </div>
  )
}

function RuntimeEventFields({
  value,
  disabled,
  onChange,
}: {
  value: RuntimeEventDraft
  disabled: boolean
  onChange: (value: RuntimeEventDraft) => void
}) {
  const fields: Array<[keyof RuntimeEventDraft, string]> = [
    ["id", "Runtime event ID"],
    ["kind", "Runtime event kind"],
    ["time", "Event time (RFC 3339)"],
    ["sourceComponent", "Source component"],
    ["sourceName", "Source name"],
    ["severity", "Severity"],
  ]
  return (
    <div className="grid gap-3">
      <div className="grid gap-3 sm:grid-cols-2">
        {fields.map(([field, label]) => (
          <div key={field} className="grid gap-2">
            <Label htmlFor={`workflow-simulation-runtime-${field}`}>
              {label}
            </Label>
            <Input
              id={`workflow-simulation-runtime-${field}`}
              value={value[field]}
              disabled={disabled}
              onChange={(event) =>
                onChange({ ...value, [field]: event.target.value })
              }
              className="font-mono text-xs"
            />
          </div>
        ))}
      </div>
      {(
        [
          ["scope", "Scope JSON"],
          ["correlation", "Correlation JSON"],
          ["payload", "Payload JSON"],
          ["attrs", "Attributes JSON"],
        ] as const
      ).map(([field, label]) => (
        <JSONField
          key={field}
          id={`workflow-simulation-runtime-${field}`}
          label={label}
          value={value[field]}
          disabled={disabled}
          onChange={(next) => onChange({ ...value, [field]: next })}
        />
      ))}
    </div>
  )
}

function DurableEventFields({
  events,
  eventID,
  loading,
  error,
  disabled,
  onChange,
}: {
  events: EventView[]
  eventID: string
  loading: boolean
  error: unknown
  disabled: boolean
  onChange: (value: string) => void
}) {
  const selectedIsMissing =
    eventID !== "" && !events.some((event) => event.id === eventID)
  return (
    <div className="grid gap-2">
      <Label htmlFor="workflow-simulation-event-id">Durable event</Label>
      <select
        id="workflow-simulation-event-id"
        value={eventID}
        disabled={disabled || loading}
        onChange={(event) => onChange(event.target.value)}
        className="border-input bg-background min-h-9 w-full rounded-md border px-3 py-1 text-sm"
      >
        <option value="">
          {loading ? "Loading recent event metadata…" : "Select a recent event"}
        </option>
        {events.map((event) => (
          <option key={event.id} value={event.id}>
            {eventMetadataLabel(event)}
          </option>
        ))}
        {selectedIsMissing ? (
          <option value={eventID}>Stored event · {eventID}</option>
        ) : null}
      </select>
      <p className="text-muted-foreground text-xs">
        Only event metadata and its ID are shown. Protected payload content
        stays server-side and is never returned by simulation.
      </p>
      {error ? (
        <p role="alert" className="text-destructive text-xs">
          {errorMessage(error)}
        </p>
      ) : null}
    </div>
  )
}

function JSONField({
  id,
  label,
  value,
  disabled,
  onChange,
  secret = false,
}: {
  id: string
  label: string
  value: string
  disabled: boolean
  onChange: (value: string) => void
  secret?: boolean
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Textarea
        id={id}
        value={value}
        disabled={disabled}
        spellCheck={false}
        autoComplete={secret ? "off" : undefined}
        data-1p-ignore={secret ? "true" : undefined}
        onChange={(event) => onChange(event.target.value)}
        className="min-h-20 resize-none font-mono text-xs"
      />
    </div>
  )
}

function SimulatorStatus({
  status,
  message,
  children,
}: {
  status: WorkflowTriggerSimulatorState["status"]
  message: string
  children?: ReactNode
}) {
  return (
    <div
      role={status === "error" ? "alert" : "status"}
      aria-live="polite"
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        status === "error"
          ? "border-destructive/40 bg-destructive/10 text-destructive"
          : "border-border bg-muted/30",
      )}
    >
      <div className="flex items-start gap-2">
        {status === "ready" ? (
          <IconCheck className="mt-0.5 size-4 shrink-0" />
        ) : status === "error" ? (
          <IconX className="mt-0.5 size-4 shrink-0" />
        ) : (
          <IconRefresh
            className={cn(
              "mt-0.5 size-4 shrink-0",
              status === "loading" && "animate-spin",
            )}
          />
        )}
        <span className="break-words">{message}</span>
      </div>
      {children}
    </div>
  )
}

function workflowTriggerSimulationCandidate({
  session,
  prompt,
  targetRef,
  yaml,
  inspectionReady,
  presentKinds,
  selectedKind,
  scheduleIndex,
  scheduledAt,
  manual,
  workflowCall,
  channelMessage,
  command,
  runtimeEvent,
  eventID,
}: {
  session: WorkflowDevelopmentSession
  prompt: string
  targetRef: string
  yaml: string
  inspectionReady: boolean
  presentKinds: WorkflowTriggerKind[]
  selectedKind: WorkflowTriggerKind | ""
  scheduleIndex: string
  scheduledAt: string
  manual: InvocationDraft
  workflowCall: InvocationDraft
  channelMessage: MessageDraft
  command: MessageDraft
  runtimeEvent: RuntimeEventDraft
  eventID: string
}): {
  request?: WorkflowTriggerSimulationRequest
  identity: string
  message: string
} {
  const unavailable = (message: string) => ({ identity: message, message })
  if (!inspectionReady) {
    return unavailable("Wait for the current YAML trigger inspection.")
  }
  if (targetRef.trim() === "" || yaml.trim() === "") {
    return unavailable("Add a target and workflow YAML before simulating.")
  }
  if (selectedKind === "" || !presentKinds.includes(selectedKind)) {
    return unavailable(
      presentKinds.length > 1
        ? "Select one present trigger family."
        : "The draft does not have a selectable trigger.",
    )
  }
  const base = {
    session_id: session.id,
    expected_session_revision: session.session_revision,
    expected_draft_revision: session.draft_revision,
    prompt,
    target_ref: targetRef,
    yaml,
  }
  try {
    let request: WorkflowTriggerSimulationRequest
    switch (selectedKind) {
      case "manual":
      case "workflow_call": {
        const draft = selectedKind === "manual" ? manual : workflowCall
        const scenario = invocationScenario(draft)
        request =
          selectedKind === "manual"
            ? { ...base, trigger: { type: "manual" }, scenario }
            : { ...base, trigger: { type: "workflow_call" }, scenario }
        break
      }
      case "schedule": {
        if (scheduleIndex === "") {
          return unavailable("Select the exact declared schedule.")
        }
        const index = Number(scheduleIndex)
        if (!Number.isSafeInteger(index) || index < 0) {
          return unavailable("Select the exact declared schedule.")
        }
        if (scheduledAt.trim() === "" || !validRFC3339(scheduledAt)) {
          return unavailable("Scheduled time must be a valid RFC 3339 value.")
        }
        request = {
          ...base,
          trigger: { type: "schedule", schedule_index: index },
          scenario: { scheduled_at: scheduledAt },
        }
        break
      }
      case "channel_message":
      case "command": {
        const message = messageEnvelope(
          selectedKind === "command" ? command : channelMessage,
        )
        request =
          selectedKind === "command"
            ? { ...base, trigger: { type: "command" }, scenario: { message } }
            : {
                ...base,
                trigger: { type: "channel_message" },
                scenario: { message },
              }
        break
      }
      case "runtime_event":
        request = {
          ...base,
          trigger: { type: "runtime_event" },
          scenario: { event: runtimeEventEnvelope(runtimeEvent) },
        }
        break
      case "event":
        if (eventID === "") {
          return unavailable("Select a durable event by metadata.")
        }
        request = {
          ...base,
          trigger: { type: "event" },
          scenario: { event_id: eventID },
        }
        break
    }
    return {
      request,
      identity: workflowTriggerSimulationIdentity(request),
      message: "",
    }
  } catch (error) {
    const message = errorMessage(error)
    return unavailable(message)
  }
}

function invocationScenario(draft: InvocationDraft) {
  return {
    inputs: parseJSONObject(draft.inputs, "Inputs"),
    secrets: parseStringJSONObject(draft.secrets, "Secrets"),
    ...(draft.session.trim() === "" ? {} : { session: draft.session }),
    delivery: parseJSONObject(draft.delivery, "Delivery"),
  }
}

function messageEnvelope(draft: MessageDraft): WorkflowTriggerMessageEnvelope {
  const additional = parseAdditionalMessageEnvelope(
    draft.additional,
    "Additional message envelope",
  )
  return {
    ...additional,
    ...optionalRecordEntry("channel", draft.channel),
    ...optionalRecordEntry("account", draft.account),
    ...optionalRecordEntry("chat_id", draft.chatID),
    ...optionalRecordEntry("chat_type", draft.chatType),
    ...optionalRecordEntry("topic_id", draft.topicID),
    ...optionalRecordEntry("space_id", draft.spaceID),
    ...optionalRecordEntry("space_type", draft.spaceType),
    ...optionalRecordEntry("sender_id", draft.senderID),
    ...optionalRecordEntry("sender_username", draft.senderUsername),
    ...optionalRecordEntry("sender_name", draft.senderName),
    ...optionalRecordEntry("message_id", draft.messageID),
    ...optionalRecordEntry("reply_to_message_id", draft.replyToMessageID),
    mentioned: draft.mentioned,
    text: draft.text,
  } as WorkflowTriggerMessageEnvelope
}

function runtimeEventEnvelope(
  draft: RuntimeEventDraft,
): WorkflowTriggerRuntimeEventEnvelope {
  if (
    draft.id.trim() === "" ||
    draft.kind.trim() === "" ||
    draft.sourceComponent.trim() === ""
  ) {
    throw new Error(
      "Runtime event ID, kind, and source component are required.",
    )
  }
  if (!validRFC3339(draft.time)) {
    throw new Error("Runtime event time must be a valid RFC 3339 value.")
  }
  return {
    id: draft.id,
    kind: draft.kind,
    time: draft.time,
    source: {
      component: draft.sourceComponent,
      ...optionalRecordEntry("name", draft.sourceName),
    },
    scope: parseJSONObject(draft.scope, "Runtime event scope"),
    correlation: parseJSONObject(
      draft.correlation,
      "Runtime event correlation",
    ),
    ...optionalRecordEntry("severity", draft.severity),
    payload: parseJSONValue(draft.payload, "Runtime event payload"),
    attrs: parseJSONObject(draft.attrs, "Runtime event attributes"),
  }
}

function optionalRecordEntry(name: string, value: string) {
  return value === "" ? {} : { [name]: value }
}

function parseJSONObject(
  value: string,
  label: string,
): Record<string, unknown> {
  const parsed = parseJSONValue(value, label)
  if (parsed == null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object.`)
  }
  return parsed as Record<string, unknown>
}

function parseStringJSONObject(
  value: string,
  label: string,
): Record<string, string> {
  const parsed = parseJSONObject(value, label)
  if (Object.values(parsed).some((item) => typeof item !== "string")) {
    throw new Error(`${label} values must all be strings.`)
  }
  return parsed as Record<string, string>
}

function parseJSONValue(value: string, label: string): unknown {
  let parsed: unknown
  try {
    parsed = parseStrictWorkflowSimulationJSON(value)
  } catch {
    throw new Error(`${label} must be valid JSON.`)
  }
  if (!workflowSimulationJSONValueIsSafe(parsed, 0, { remaining: 4096 })) {
    throw new Error(
      `${label} contains an unsafe number or is too large or deeply nested.`,
    )
  }
  return parsed
}

function parseAdditionalMessageEnvelope(
  value: string,
  label: string,
): Pick<WorkflowTriggerMessageEnvelope, "media" | "reply_handles" | "raw"> {
  const parsed = parseJSONObject(value, label)
  if (
    Object.keys(parsed).some(
      (key) => !["media", "reply_handles", "raw"].includes(key),
    )
  ) {
    throw new Error(`${label} may contain only media, reply_handles, and raw.`)
  }
  if (
    parsed.media !== undefined &&
    (!Array.isArray(parsed.media) ||
      parsed.media.some((item) => typeof item !== "string"))
  ) {
    throw new Error(`${label} media must be an array of strings.`)
  }
  for (const field of ["reply_handles", "raw"] as const) {
    const item = parsed[field]
    if (
      item !== undefined &&
      (item == null ||
        typeof item !== "object" ||
        Array.isArray(item) ||
        Object.values(item).some((entry) => typeof entry !== "string"))
    ) {
      throw new Error(`${label} ${field} must be a string map.`)
    }
  }
  return parsed as Pick<
    WorkflowTriggerMessageEnvelope,
    "media" | "reply_handles" | "raw"
  >
}

function parseStrictWorkflowSimulationJSON(source: string): unknown {
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
            ? !workflowSimulationJSONKeySafe(parsed)
            : !workflowSimulationJSONStringSafe(parsed))
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

  const parseValue = (depth: number): unknown => {
    whitespace()
    nodes += 1
    if (depth > 32 || nodes > 4096) {
      throw new Error("too deep")
    }
    const character = source[index]
    if (character === '"') {
      return string(64 << 10)
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
        const name = string(1024, true)
        if (names.has(name)) {
          throw new Error("duplicate object member")
        }
        names.add(name)
        whitespace()
        if (source[index] !== ":") {
          throw new Error("expected colon")
        }
        index += 1
        members.push([name, parseValue(depth + 1)])
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
        items.push(parseValue(depth + 1))
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
    if (!workflowJSONNumberIsBrowserSafe(numberMatch[0])) {
      throw new Error("unsafe number")
    }
    return Number(numberMatch[0])
  }

  const parsed = parseValue(0)
  whitespace()
  if (index !== source.length) {
    throw new Error("trailing input")
  }
  return parsed
}

function workflowSimulationJSONStringSafe(value: string) {
  if (
    hasUnpairedSurrogate(value) ||
    new TextEncoder().encode(value).byteLength > 64 << 10
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

function workflowSimulationJSONKeySafe(value: string) {
  return (
    value !== "" &&
    !hasUnpairedSurrogate(value) &&
    new TextEncoder().encode(value).byteLength <= 1024 &&
    !/[\p{Cc}\p{Cf}\p{Cs}]/u.test(value)
  )
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

function workflowSimulationJSONValueIsSafe(
  value: unknown,
  depth: number,
  budget: { remaining: number },
): boolean {
  budget.remaining -= 1
  if (budget.remaining < 0 || depth > 32) {
    return false
  }
  if (
    value == null ||
    typeof value === "boolean" ||
    typeof value === "string"
  ) {
    return (
      typeof value !== "string" ||
      new TextEncoder().encode(value).length <= 64 * 1024
    )
  }
  if (typeof value === "number") {
    return (
      Number.isFinite(value) &&
      (!Number.isInteger(value) || Number.isSafeInteger(value))
    )
  }
  if (Array.isArray(value)) {
    return value.every((item) =>
      workflowSimulationJSONValueIsSafe(item, depth + 1, budget),
    )
  }
  if (typeof value === "object") {
    return Object.entries(value).every(
      ([key, item]) =>
        new TextEncoder().encode(key).length <= 1024 &&
        workflowSimulationJSONValueIsSafe(item, depth + 1, budget),
    )
  }
  return false
}

function validRFC3339(value: string) {
  const match = value.match(
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|([+-])(\d{2}):(\d{2}))$/,
  )
  if (match == null) {
    return false
  }
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6])
  const zoneHour = Number(match[8] ?? "0")
  const zoneMinute = Number(match[9] ?? "0")
  return (
    month >= 1 &&
    month <= 12 &&
    day >= 1 &&
    day <= daysInMonth(year, month) &&
    hour <= 23 &&
    minute <= 59 &&
    second <= 59 &&
    zoneHour <= 23 &&
    zoneMinute <= 59
  )
}

function daysInMonth(year: number, month: number) {
  if (month === 2) {
    return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28
  }
  return [4, 6, 9, 11].includes(month) ? 30 : 31
}

function workflowTriggerSimulationMessage(
  response: WorkflowTriggerSimulationResponse,
) {
  switch (response.simulation.reason) {
    case "matched":
      return response.review_token
        ? "Matched. A server-reviewed execution token is ready."
        : "Matched, but the server did not authorize execution."
    case "shadowed_by_command":
      return "This channel-message scenario is shadowed by the command trigger."
    case "runtime_feedback_suppressed":
      return "This runtime lifecycle event is suppressed to prevent workflow feedback."
    case "not_matched":
      return "The selected scenario does not match this trigger."
    case "trigger_absent":
      return "The selected trigger is no longer present in the exact draft."
    case "schedule_index_required":
    case "schedule_index_out_of_range":
      return "Select an exact current schedule before executing."
    case "invalid_workflow":
      return "The exact workflow draft is invalid."
    case "invalid_scenario":
      return "The server rejected this trigger scenario."
    case "trigger_evaluation_failed":
      return "The production trigger evaluator could not evaluate this scenario."
    case "review_incomplete":
      return "The server could not produce a complete execution review."
  }
}

function eventMetadataLabel(event: EventView) {
  const actor = event.actor?.display_name ?? event.actor?.id
  return [event.type, event.source, event.connector, actor, event.received_at]
    .filter(Boolean)
    .join(" · ")
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}
