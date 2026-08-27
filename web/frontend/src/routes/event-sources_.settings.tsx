import { createFileRoute } from "@tanstack/react-router"

import { normalizeEventSourcesCollectionSearch } from "@/components/events/event-source-collection-route-state"
import { EventSourceSettingsPage } from "@/components/events/event-source-settings-page"

export const Route = createFileRoute("/event-sources_/settings")({
  validateSearch: normalizeEventSourcesCollectionSearch,
  component: EventSourceSettingsRoute,
})

function EventSourceSettingsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <EventSourceSettingsPage
      onBack={() => void navigate({ to: "/event-sources", search })}
    />
  )
}
