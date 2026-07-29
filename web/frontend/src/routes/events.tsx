import { createFileRoute } from "@tanstack/react-router"
import { useCallback } from "react"

import type { EventRoutingStatus } from "@/api/events"
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

function normalizeSearch(raw: Record<string, unknown>): EventsRouteSearch {
  const source = optionalText(raw.source, 128)
  const connector = optionalText(raw.connector, 256)
  const type = optionalText(raw.type, 256)
  const routingStatus =
    typeof raw.routing_status === "string" &&
    routingStatuses.has(raw.routing_status as EventRoutingStatus)
      ? (raw.routing_status as EventRoutingStatus)
      : undefined
  const event =
    typeof raw.event === "string" && /^ev_[0-9a-f]{32}$/.test(raw.event)
      ? raw.event
      : undefined

  return {
    ...(source ? { source } : {}),
    ...(connector ? { connector } : {}),
    ...(type ? { type } : {}),
    ...(routingStatus ? { routing_status: routingStatus } : {}),
    ...(event ? { event } : {}),
  }
}

function EventsRoutePage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const changeSearch = useCallback(
    (next: EventsRouteSearch, replace = false) => {
      void navigate({ search: next, replace })
    },
    [navigate],
  )

  return <EventsPage search={search} onSearchChange={changeSearch} />
}

export const Route = createFileRoute("/events")({
  validateSearch: normalizeSearch,
  component: EventsRoutePage,
})

function optionalText(
  value: unknown,
  maximumLength: number,
): string | undefined {
  if (typeof value !== "string") {
    return undefined
  }
  const normalized = value.trim()
  return normalized !== "" && normalized.length <= maximumLength
    ? normalized
    : undefined
}
