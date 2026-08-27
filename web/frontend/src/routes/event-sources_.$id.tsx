import { createFileRoute } from "@tanstack/react-router"

import { normalizeEventSourcesCollectionSearch } from "@/components/events/event-source-collection-route-state"
import { EventSourceDetailPage } from "@/components/events/event-source-collections"

export const Route = createFileRoute("/event-sources_/$id")({
  validateSearch: normalizeEventSourcesCollectionSearch,
  component: EventSourceDetailRoute,
})

function EventSourceDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <EventSourceDetailPage
      id={id}
      onBack={() => void navigate({ to: "/event-sources", search })}
      onEdit={() =>
        void navigate({
          to: "/event-sources/$id/edit",
          params: { id },
          search,
        })
      }
      onRemoved={() =>
        void navigate({ to: "/event-sources", search, replace: true })
      }
    />
  )
}
