import { createFileRoute } from "@tanstack/react-router"

import { normalizeEventSourcesCollectionSearch } from "@/components/events/event-source-collection-route-state"
import { EventSourceEditorPage } from "@/components/events/event-source-editor-page"

export const Route = createFileRoute("/event-sources_/$id_/edit")({
  validateSearch: normalizeEventSourcesCollectionSearch,
  component: EditEventSourceRoute,
})

function EditEventSourceRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <EventSourceEditorPage
      mode="edit"
      id={id}
      onBack={() =>
        void navigate({ to: "/event-sources/$id", params: { id }, search })
      }
      onSaved={(savedID) =>
        void navigate({
          to: "/event-sources/$id",
          params: { id: savedID },
          search,
          replace: true,
        })
      }
    />
  )
}
