import { createFileRoute } from "@tanstack/react-router"

import { normalizeEventSourcesCollectionSearch } from "@/components/events/event-source-collection-route-state"
import { EventSourceEditorPage } from "@/components/events/event-source-editor-page"

export const Route = createFileRoute("/event-sources_/new")({
  validateSearch: normalizeEventSourcesCollectionSearch,
  component: NewEventSourceRoute,
})

function NewEventSourceRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <EventSourceEditorPage
      mode="create"
      onBack={() => void navigate({ to: "/event-sources", search })}
      onSaved={(id) =>
        void navigate({
          to: "/event-sources/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}
