import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import type { DispatchStatus, EventRoutingStatus } from "@/api/events"
import {
  EventsPage,
  type EventsRouteSearch,
} from "@/components/events/events-page"

const routingStatuses = new Set<EventRoutingStatus>([
  "pending",
  "claimed",
  "succeeded",
  "dead",
])

const dispatchStatuses = new Set<DispatchStatus>([
  "pending",
  "claimed",
  "running",
  "succeeded",
  "failed",
  "dead",
])

const eventIDPattern = /^ev_[0-9a-f]{32}$/
const dispatchIDPattern = /^dsp_[0-9a-f]{32}$/

export function normalizeEventsSearch(
  raw: Record<string, unknown>,
): EventsRouteSearch {
  const view = raw.view === "dispatches" ? "dispatches" : undefined
  const source = optionalByteText(raw.source, 128)
  const connector = optionalByteText(raw.connector, 256)
  const type = optionalByteText(raw.type, 256)
  const routingStatus =
    typeof raw.routing_status === "string" &&
    routingStatuses.has(raw.routing_status as EventRoutingStatus)
      ? (raw.routing_status as EventRoutingStatus)
      : undefined
  const event =
    typeof raw.event === "string" && eventIDPattern.test(raw.event)
      ? raw.event
      : undefined
  const dispatchEvent =
    typeof raw.dispatch_event === "string" &&
    eventIDPattern.test(raw.dispatch_event)
      ? raw.dispatch_event
      : undefined
  const workflow = optionalByteText(raw.workflow, 1024)
  const dispatchStatus =
    typeof raw.dispatch_status === "string" &&
    dispatchStatuses.has(raw.dispatch_status as DispatchStatus)
      ? (raw.dispatch_status as DispatchStatus)
      : undefined
  const dispatch =
    typeof raw.dispatch === "string" && dispatchIDPattern.test(raw.dispatch)
      ? raw.dispatch
      : undefined

  return {
    ...(view ? { view } : {}),
    ...(source ? { source } : {}),
    ...(connector ? { connector } : {}),
    ...(type ? { type } : {}),
    ...(routingStatus ? { routing_status: routingStatus } : {}),
    ...(event ? { event } : {}),
    ...(dispatchEvent ? { dispatch_event: dispatchEvent } : {}),
    ...(workflow ? { workflow } : {}),
    ...(dispatchStatus ? { dispatch_status: dispatchStatus } : {}),
    ...(dispatch ? { dispatch } : {}),
  }
}

export function eventsSearchIsCanonical(
  raw: Record<string, unknown>,
  normalized: EventsRouteSearch,
): boolean {
  const rawKeys = Object.keys(raw)
  const normalizedKeys = Object.keys(normalized) as Array<
    keyof EventsRouteSearch
  >
  return (
    rawKeys.length === normalizedKeys.length &&
    normalizedKeys.every((key) => raw[key] === normalized[key])
  )
}

function EventsRoutePage() {
  const locationSearch = useLocation({
    select: (location) => location.search,
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeEventsSearch({ ...locationSearch }),
    [locationSearch],
  )
  useEffect(() => {
    if (!eventsSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [locationSearch, navigate, search])
  const changeSearch = useCallback(
    (next: EventsRouteSearch, replace = false) => {
      void navigate({ search: next, replace })
    },
    [navigate],
  )

  return <EventsPage search={search} onSearchChange={changeSearch} />
}

export const Route = createFileRoute("/events")({
  validateSearch: normalizeEventsSearch,
  component: EventsRoutePage,
})

function optionalByteText(
  value: unknown,
  maximumBytes: number,
): string | undefined {
  if (typeof value !== "string") {
    return undefined
  }
  const normalized = value.trim()
  return normalized !== "" &&
    new TextEncoder().encode(normalized).byteLength <= maximumBytes
    ? normalized
    : undefined
}
