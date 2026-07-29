import {
  IconAlertTriangle,
  IconCheck,
  IconRefresh,
  IconX,
} from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useEffect, useMemo, useRef, useState } from "react"

import { type EventView, listEvents } from "@/api/events"
import {
  type WorkflowEventTriggerMatchResult,
  matchWorkflowEventTrigger,
} from "@/api/workflows"
import { EventPayloadPanel } from "@/components/events/event-payload-panel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

export interface WorkflowEventTestMatchState {
  eventID?: string
  status: "idle" | "checking" | "matched" | "not_matched" | "error"
  message: string
}

export function WorkflowEventTestContext({
  yaml,
  disabled,
  initialEventID,
  onMatchStateChange,
}: {
  yaml: string
  disabled: boolean
  initialEventID?: string
  onMatchStateChange: (state: WorkflowEventTestMatchState) => void
}) {
  const latestYAMLRef = useRef(yaml)
  const latestEventIDRef = useRef("")
  const [eventID, setEventID] = useState(initialEventID ?? "")
  const [matchResult, setMatchResult] =
    useState<WorkflowEventTriggerMatchResult | null>(null)
  const [status, setStatus] =
    useState<WorkflowEventTestMatchState["status"]>("idle")
  const [message, setMessage] = useState(
    "Select a recent event to check this trigger.",
  )
  const [matchVersion, setMatchVersion] = useState(0)

  useEffect(() => {
    latestYAMLRef.current = yaml
    latestEventIDRef.current = eventID
  }, [eventID, yaml])

  const eventsQuery = useQuery({
    queryKey: ["events", "workflow-draft-context"],
    queryFn: () => listEvents({ limit: 20 }),
    staleTime: 5000,
    refetchOnWindowFocus: false,
  })
  const events = useMemo(
    () => eventsQuery.data?.events ?? [],
    [eventsQuery.data?.events],
  )
  const selectedEvent = events.find((event) => event.id === eventID)

  useEffect(() => {
    setEventID(initialEventID ?? "")
  }, [initialEventID, yaml])

  useEffect(() => {
    onMatchStateChange({
      ...(eventID ? { eventID } : {}),
      status,
      message,
    })
  }, [eventID, message, onMatchStateChange, status])

  useEffect(() => {
    setMatchResult(null)
    if (eventID === "") {
      setStatus("idle")
      setMessage("Select a recent event to check this trigger.")
      return
    }

    const controller = new AbortController()
    setStatus("checking")
    setMessage("Checking the selected event against the current YAML…")
    const timeout = window.setTimeout(() => {
      void matchWorkflowEventTrigger(
        { yaml, event_id: eventID },
        controller.signal,
      )
        .then((result) => {
          if (
            controller.signal.aborted ||
            latestYAMLRef.current !== yaml ||
            latestEventIDRef.current !== eventID
          ) {
            return
          }
          setMatchResult(result)
          if (result.matched) {
            setStatus("matched")
            setMessage(
              "The selected event matches. The draft test will use its redacted event context.",
            )
          } else {
            setStatus("not_matched")
            setMessage(
              "The selected event does not match every populated trigger filter.",
            )
          }
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted) {
            return
          }
          setStatus("error")
          setMessage(errorMessage(error))
        })
    }, 200)

    return () => {
      window.clearTimeout(timeout)
      controller.abort()
    }
  }, [eventID, matchVersion, yaml])

  return (
    <div className="grid min-w-0 gap-3">
      <div className="border-border bg-muted/30 grid gap-3 rounded-md border p-3">
        <div>
          <Label htmlFor="workflow-test-event">Draft test event</Label>
          <p
            id="workflow-test-event-help"
            className="text-muted-foreground mt-1 text-xs"
          >
            Recent event metadata loads without payload content. The server
            resolves the already-redacted payload only when the draft test
            starts.
          </p>
        </div>
        <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
          <select
            id="workflow-test-event"
            value={eventID}
            onChange={(event) => setEventID(event.target.value)}
            disabled={disabled || eventsQuery.isPending}
            aria-describedby="workflow-test-event-help"
            className="border-input bg-background ring-offset-background focus-visible:ring-ring min-h-9 min-w-0 flex-1 rounded-md border px-3 py-1 text-sm shadow-xs outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50"
          >
            <option value="">
              {eventsQuery.isPending
                ? "Loading recent events…"
                : "Select a recent event"}
            </option>
            {events.map((event) => (
              <option key={event.id} value={event.id}>
                {eventOptionLabel(event)}
              </option>
            ))}
            {eventID !== "" && selectedEvent == null ? (
              <option value={eventID}>Stored event · {eventID}</option>
            ) : null}
          </select>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setMatchVersion((current) => current + 1)}
            disabled={disabled || eventID === "" || status === "checking"}
          >
            <IconRefresh className="size-4" />
            Recheck
          </Button>
        </div>

        {eventsQuery.error ? (
          <div role="alert" className="text-destructive text-xs break-words">
            {errorMessage(eventsQuery.error)}
          </div>
        ) : eventsQuery.isSuccess && events.length === 0 ? (
          <div className="text-muted-foreground text-xs">
            No recent events are available. Configure an event source and
            receive an event before testing this trigger.
          </div>
        ) : null}

        <MatchStatus status={status} message={message} />
        {matchResult ? <MatchChecks result={matchResult} /> : null}
      </div>

      {selectedEvent ? (
        <EventPayloadPanel
          eventID={selectedEvent.id}
          payloadBytes={selectedEvent.payload_bytes}
        />
      ) : null}

      <div className="border-border bg-muted/30 flex items-start gap-2 rounded-md border px-3 py-2 text-xs">
        <IconAlertTriangle className="text-muted-foreground mt-0.5 size-4 shrink-0" />
        <p className="text-muted-foreground leading-relaxed">
          A draft test executes declared agent, tool, MCP, and function steps.
          It can repeat external effects. Use a safe event and review every
          authority-bearing action first.
        </p>
      </div>
    </div>
  )
}

function MatchStatus({
  status,
  message,
}: {
  status: WorkflowEventTestMatchState["status"]
  message: string
}) {
  return (
    <div
      role={status === "error" ? "alert" : "status"}
      aria-live="polite"
      className={cn(
        "flex items-start gap-2 rounded-md border px-3 py-2 text-xs",
        status === "matched"
          ? "border-border bg-background text-foreground"
          : status === "error" || status === "not_matched"
            ? "border-destructive/40 bg-destructive/10 text-destructive"
            : "border-border bg-background text-muted-foreground",
      )}
    >
      {status === "matched" ? (
        <IconCheck className="mt-0.5 size-4 shrink-0" />
      ) : status === "not_matched" || status === "error" ? (
        <IconX className="mt-0.5 size-4 shrink-0" />
      ) : (
        <IconRefresh
          className={cn(
            "mt-0.5 size-4 shrink-0",
            status === "checking" && "animate-spin",
          )}
        />
      )}
      <span className="break-words">{message}</span>
    </div>
  )
}

function MatchChecks({ result }: { result: WorkflowEventTriggerMatchResult }) {
  if (result.checks.length === 0) {
    return null
  }
  return (
    <div className="grid gap-2">
      <div className="text-xs font-medium">Trigger checks</div>
      <ul className="grid gap-1.5">
        {result.checks.map((check, index) => (
          <li
            key={`${check.path}-${index}`}
            className="border-border bg-background flex min-w-0 flex-wrap items-center gap-2 rounded-md border px-2.5 py-2 text-xs"
          >
            <Badge variant={check.matched ? "outline" : "destructive"}>
              {check.matched ? "Match" : "No match"}
            </Badge>
            <code className="min-w-0 break-all">{check.path}</code>
            {!check.present ? (
              <span className="text-muted-foreground">
                missing from selected event
              </span>
            ) : check.value !== undefined ? (
              <span className="text-muted-foreground min-w-0 break-all">
                {displayCheckValue(check.value)}
              </span>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  )
}

function eventOptionLabel(event: EventView) {
  return `${event.source} · ${event.connector} · ${event.type} · ${event.id.slice(-8)}`
}

function displayCheckValue(value: unknown) {
  if (typeof value === "string") {
    return value
  }
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function errorMessage(error: unknown) {
  return error instanceof Error
    ? error.message
    : "Failed to check the event trigger"
}
