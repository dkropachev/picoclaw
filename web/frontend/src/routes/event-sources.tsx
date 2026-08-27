import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  eventSourceCollectionSearchIsCanonical,
  normalizeEventSourcesCollectionSearch,
} from "@/components/events/event-source-collection-route-state"
import { EventSourcesCollectionPage } from "@/components/events/event-source-collections"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/event-sources")({
  validateSearch: normalizeEventSourcesCollectionSearch,
  component: EventSourcesCollectionRoute,
})

function EventSourcesCollectionRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeEventSourcesCollectionSearch({ ...routeSearch }),
    [routeSearch],
  )

  useEffect(() => {
    if (location.pathname !== "/event-sources") return
    if (
      !eventSourceCollectionSearchIsCanonical({ ...location.search }, search)
    ) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/event-sources") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )

  return (
    <EventSourcesCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/event-sources/new", search })}
      onOpen={(source) =>
        void navigate({
          to: "/event-sources/$id",
          params: { id: source.id },
          search,
        })
      }
      onEdit={(source) =>
        void navigate({
          to: "/event-sources/$id/edit",
          params: { id: source.id },
          search,
        })
      }
      onSettings={() =>
        void navigate({ to: "/event-sources/settings", search })
      }
    />
  )
}
