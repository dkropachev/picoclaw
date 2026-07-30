import {
  IconAlertTriangle,
  IconLoader2,
  IconPlayerPause,
  IconPlayerPlay,
  IconRefresh,
} from "@tabler/icons-react"
import { useAtomValue } from "jotai"
import { useEffect, useMemo, useRef, useState } from "react"

import {
  type AgentActivityEvent,
  type AgentActivityResponse,
  AgentsAPIError,
  getAgentActivity,
} from "@/api/agents"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { gatewayAtom } from "@/store/gateway"

import { mergeActivityEvents } from "./agent-activity"

const activityPollIntervalMS = 2500
const activityRowLimit = 200

type ActivitySeverity = AgentActivityEvent["severity"]

export function AgentActivityPanel({ agentID }: { agentID: string }) {
  const gateway = useAtomValue(gatewayAtom)
  const [events, setEvents] = useState<AgentActivityEvent[]>([])
  const [visible, setVisible] = useState(
    () =>
      typeof document === "undefined" || document.visibilityState !== "hidden",
  )
  const [browserOnline, setBrowserOnline] = useState(
    () => typeof navigator === "undefined" || navigator.onLine,
  )
  const [userPaused, setUserPaused] = useState(false)
  const [errorPaused, setErrorPaused] = useState(false)
  const [loading, setLoading] = useState(true)
  const [online, setOnline] = useState<boolean | null>(null)
  const [errorKind, setErrorKind] = useState<"offline" | "transient" | null>(
    null,
  )
  const [resetWarning, setResetWarning] = useState(false)
  const [truncatedWarning, setTruncatedWarning] = useState(false)
  const [dropped, setDropped] = useState<AgentActivityResponse["dropped"]>({
    subscription: "0",
    retention: "0",
    projection: "0",
  })
  const [retryGeneration, setRetryGeneration] = useState(0)
  const [severity, setSeverity] = useState<Record<ActivitySeverity, boolean>>({
    info: true,
    warn: true,
    error: true,
  })
  const cursorRef = useRef("")

  useEffect(() => {
    const onVisibility = () => setVisible(document.visibilityState !== "hidden")
    document.addEventListener("visibilitychange", onVisibility)
    return () => document.removeEventListener("visibilitychange", onVisibility)
  }, [])

  useEffect(() => {
    const onOnline = () => setBrowserOnline(true)
    const onOffline = () => setBrowserOnline(false)
    window.addEventListener("online", onOnline)
    window.addEventListener("offline", onOffline)
    return () => {
      window.removeEventListener("online", onOnline)
      window.removeEventListener("offline", onOffline)
    }
  }, [])

  useEffect(() => {
    cursorRef.current = ""
    setEvents([])
    setLoading(true)
    setOnline(null)
    setErrorPaused(false)
    setErrorKind(null)
    setResetWarning(false)
    setTruncatedWarning(false)
    setDropped({ subscription: "0", retention: "0", projection: "0" })
  }, [agentID])

  const gatewayRunning = gateway.status === "running"
  const canPoll =
    gatewayRunning && browserOnline && visible && !userPaused && !errorPaused

  useEffect(() => {
    if (!canPoll) {
      setLoading(false)
      if (!gatewayRunning) {
        setOnline(false)
      }
      return
    }

    let disposed = false
    let timer: ReturnType<typeof setTimeout> | null = null
    let controller: AbortController | null = null

    const poll = async () => {
      controller = new AbortController()
      setLoading(true)
      try {
        const response = await getAgentActivity(agentID, {
          cursor: cursorRef.current || undefined,
          limit: 100,
          signal: controller.signal,
        })
        if (disposed) return

        cursorRef.current = response.next_cursor
        setEvents((current) =>
          mergeActivityEvents(
            response.reset ? [] : current,
            response.events,
            activityRowLimit,
          ),
        )
        setResetWarning((current) => current || response.reset)
        setTruncatedWarning((current) => current || response.truncated)
        setDropped(response.dropped)
        setLoading(false)
        setOnline(true)
        setErrorKind(null)
        timer = setTimeout(() => void poll(), activityPollIntervalMS)
      } catch (error) {
        if (disposed || controller.signal.aborted) return
        setLoading(false)
        setOnline(false)
        setErrorPaused(true)
        setErrorKind(
          error instanceof AgentsAPIError && error.status === 503
            ? "offline"
            : "transient",
        )
      }
    }

    void poll()
    return () => {
      disposed = true
      if (timer != null) clearTimeout(timer)
      controller?.abort()
    }
  }, [agentID, browserOnline, canPoll, gatewayRunning, retryGeneration])

  const filteredEvents = useMemo(
    () => events.filter((event) => severity[event.severity]),
    [events, severity],
  )

  const retry = () => {
    setErrorPaused(false)
    setErrorKind(null)
    setLoading(true)
    setRetryGeneration((current) => current + 1)
  }

  return (
    <section className="space-y-4" aria-labelledby="agent-activity-title">
      <div className="border-border bg-muted/20 flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
        <div>
          <h2 id="agent-activity-title" className="text-sm font-medium">
            Live agent activity
          </h2>
          <p className="text-muted-foreground mt-1 text-xs">
            Privacy-safe lifecycle signals only. Message text, prompts, tool
            arguments, results, and error text are never shown.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge
            variant={
              online === true
                ? "secondary"
                : online === false
                  ? "outline"
                  : "outline"
            }
          >
            {!browserOnline
              ? "Browser offline"
              : online === true
                ? "Online"
                : online === false
                  ? "Unavailable"
                  : "Connecting"}
          </Badge>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={!gatewayRunning && !userPaused}
            onClick={() => setUserPaused((current) => !current)}
          >
            {userPaused ? (
              <IconPlayerPlay className="size-4" />
            ) : (
              <IconPlayerPause className="size-4" />
            )}
            {userPaused ? "Resume" : "Pause"}
          </Button>
        </div>
      </div>

      {!gatewayRunning && (
        <div
          className="border-border bg-muted/30 rounded-lg border p-4"
          role="status"
        >
          <p className="text-sm font-medium">Gateway is not running</p>
          <p className="text-muted-foreground mt-1 text-xs">
            Start the gateway to view agent activity.
          </p>
        </div>
      )}

      {!browserOnline && (
        <div
          className="border-border bg-muted/30 rounded-lg border p-4"
          role="status"
        >
          <p className="text-sm font-medium">Browser is offline</p>
          <p className="text-muted-foreground mt-1 text-xs">
            Existing activity is preserved. Polling resumes when the browser is
            online.
          </p>
        </div>
      )}

      {errorKind != null && (
        <div className="bg-destructive/10 rounded-lg p-4" role="alert">
          <p className="text-destructive text-sm font-medium">
            {errorKind === "offline"
              ? "Agent activity is unavailable"
              : "Activity update failed"}
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            Existing activity is preserved. Polling is paused until you retry.
          </p>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="mt-3"
            disabled={!gatewayRunning}
            onClick={retry}
          >
            <IconRefresh className="size-4" />
            Retry activity
          </Button>
        </div>
      )}

      {(resetWarning || truncatedWarning || hasDroppedActivity(dropped)) && (
        <div
          className="border-warning/40 bg-warning/10 rounded-lg border p-3"
          role="status"
        >
          <div className="flex items-start gap-2">
            <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
            <div className="space-y-1 text-xs">
              {resetWarning && (
                <p>The runtime restarted; the activity cursor was reset.</p>
              )}
              {truncatedWarning && (
                <p>Some activity was omitted by the bounded activity window.</p>
              )}
              {dropped.subscription !== "0" && (
                <p>
                  {dropped.subscription} records were dropped before delivery.
                </p>
              )}
              {dropped.retention !== "0" && (
                <p>
                  {dropped.retention} records were overwritten by bounded
                  retention.
                </p>
              )}
              {dropped.projection !== "0" && (
                <p>
                  {dropped.projection} unsupported records were omitted by the
                  privacy projection.
                </p>
              )}
            </div>
          </div>
        </div>
      )}

      <fieldset className="border-border flex flex-wrap gap-x-5 gap-y-3 rounded-lg border p-3">
        <legend className="px-1 text-xs font-medium">
          Visible severity levels
        </legend>
        {(["info", "warn", "error"] as const).map((level) => (
          <div key={level} className="flex items-center gap-2">
            <Switch
              id={`agent-activity-${level}`}
              checked={severity[level]}
              onCheckedChange={(checked) =>
                setSeverity((current) => ({ ...current, [level]: checked }))
              }
            />
            <Label htmlFor={`agent-activity-${level}`} className="capitalize">
              {level}
            </Label>
          </div>
        ))}
      </fieldset>

      <div className="border-border overflow-hidden rounded-lg border">
        {loading && events.length === 0 ? (
          <div
            className="flex items-center justify-center gap-2 py-14"
            aria-label="Loading agent activity"
          >
            <IconLoader2 className="text-muted-foreground size-5 animate-spin" />
            <span className="text-muted-foreground text-sm">Connecting…</span>
          </div>
        ) : filteredEvents.length === 0 ? (
          <p className="text-muted-foreground px-4 py-12 text-center text-sm">
            {events.length === 0
              ? "No activity has been recorded yet."
              : "No activity matches the selected severity levels."}
          </p>
        ) : (
          <ol
            className="divide-border max-h-[34rem] divide-y overflow-y-auto"
            aria-label="Agent activity timeline"
          >
            {filteredEvents.map((event) => (
              <ActivityRow
                key={`${event.agent_id}:${event.sequence}`}
                event={event}
              />
            ))}
          </ol>
        )}
      </div>
      <p className="text-muted-foreground text-xs" aria-live="polite">
        {userPaused
          ? "Activity polling paused."
          : !browserOnline
            ? "Activity polling pauses while the browser is offline."
            : !visible
              ? "Activity polling pauses while this tab is hidden."
              : online
                ? "Activity polling every 2.5 seconds."
                : ""}
      </p>
    </section>
  )
}

function ActivityRow({ event }: { event: AgentActivityEvent }) {
  return (
    <li className="grid min-w-0 gap-2 px-3 py-3 sm:grid-cols-[9rem_minmax(0,1fr)] sm:px-4">
      <div className="flex items-center gap-2 sm:block">
        <time
          className="text-muted-foreground block text-xs"
          dateTime={event.timestamp}
        >
          {formatTimestamp(event.timestamp)}
        </time>
        <Badge
          className="mt-1 capitalize"
          variant={
            event.severity === "error"
              ? "destructive"
              : event.severity === "warn"
                ? "outline"
                : "secondary"
          }
        >
          {event.severity}
        </Badge>
      </div>
      <div className="min-w-0">
        <p className="text-sm font-medium">{activityLabel(event.kind)}</p>
        <p className="text-muted-foreground mt-1 text-xs break-words">
          {activityDetails(event)}
        </p>
      </div>
    </li>
  )
}

function activityLabel(kind: AgentActivityEvent["kind"]): string {
  switch (kind) {
    case "agent.turn.start":
      return "Turn started"
    case "agent.turn.end":
      return "Turn ended"
    case "agent.llm.request":
      return "Model request"
    case "agent.llm.response":
      return "Model response"
    case "agent.llm.retry":
      return "Model retry"
    case "agent.context.compress":
      return "Context compressed"
    case "agent.session.summarize":
      return "Session summarized"
    case "agent.tool.exec_start":
      return "Tool execution started"
    case "agent.tool.exec_end":
      return "Tool execution ended"
    case "agent.tool.exec_skipped":
      return "Tool execution skipped"
    case "agent.steering.injected":
      return "Steering received"
    case "agent.follow_up.queued":
      return "Follow-up queued"
    case "agent.interrupt.received":
      return "Interrupt received"
    case "agent.subturn.spawn":
      return "Subagent started"
    case "agent.subturn.end":
      return "Subagent ended"
    case "agent.subturn.result_delivered":
      return "Subagent result delivered"
    case "agent.subturn.orphan":
      return "Subagent detached"
    case "agent.error":
      return "Agent error"
  }
}

function activityDetails(event: AgentActivityEvent): string {
  switch (event.kind) {
    case "agent.turn.start":
      return `${event.details.media_count} media attachment(s)`
    case "agent.turn.end":
      return `${event.details.status}; ${event.details.iterations} iteration(s); ${event.details.duration_ms} ms`
    case "agent.llm.request":
      return `${event.details.messages_count} messages; ${event.details.tools_count} tools available`
    case "agent.llm.response":
      return `${event.details.tool_calls} tool call(s); reasoning ${event.details.has_reasoning ? "present" : "not present"}`
    case "agent.llm.retry":
      return `Attempt ${event.details.attempt} of ${event.details.max_retries}; ${event.details.backoff_ms} ms backoff`
    case "agent.context.compress":
      return `${compressionReason(event.details.reason)}; ${event.details.dropped_messages} dropped; ${event.details.remaining_messages} remaining`
    case "agent.session.summarize":
      return `${event.details.summarized_messages} summarized; ${event.details.kept_messages} kept; oversized content ${event.details.omitted_oversized ? "omitted" : "not omitted"}`
    case "agent.tool.exec_end":
      return `${event.details.tool_name}; ${event.details.duration_ms} ms; ${event.details.is_error ? "failed" : "completed"}; ${event.details.async ? "asynchronous" : "synchronous"}`
    case "agent.tool.exec_start":
      return `${event.details.tool_name}; execution started`
    case "agent.tool.exec_skipped":
      return `${event.details.tool_name}; execution skipped`
    case "agent.steering.injected":
      return `${event.details.count} steering item(s)`
    case "agent.interrupt.received":
      return `${interruptLabel(event.details.interrupt_kind)}; queue depth ${event.details.queue_depth}`
    case "agent.subturn.spawn":
      return `Target agent ${event.details.target_agent_id}`
    case "agent.subturn.end":
      return `Target agent ${event.details.target_agent_id}; ${event.details.status}`
    case "agent.follow_up.queued":
    case "agent.subturn.result_delivered":
    case "agent.subturn.orphan":
    case "agent.error":
      return "No sensitive details are collected for this event."
  }
}

function hasDroppedActivity(dropped: AgentActivityResponse["dropped"]) {
  return (
    dropped.subscription !== "0" ||
    dropped.retention !== "0" ||
    dropped.projection !== "0"
  )
}

function compressionReason(
  reason: "proactive_budget" | "llm_retry" | "summarize",
): string {
  if (reason === "proactive_budget") return "Proactive context budget"
  if (reason === "llm_retry") return "Model retry"
  return "Summarization"
}

function interruptLabel(kind: "steering" | "graceful" | "hard_abort") {
  if (kind === "hard_abort") return "Hard abort"
  if (kind === "graceful") return "Graceful stop"
  return "Steering"
}

function formatTimestamp(timestamp: string) {
  const date = new Date(timestamp)
  if (Number.isNaN(date.valueOf())) return "Unknown time"
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(date)
}
