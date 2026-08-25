import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { ModelRoutersCollectionPage } from "@/components/collections/pilots/model-collections"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

const defaultQuery = "ORDER BY name ASC"

export const Route = createFileRoute("/models_/routers")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery }),
  component: ModelRoutersRoute,
})

function ModelRoutersRoute() {
  const locationSearch = useLocation({ select: (location) => location.search })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () =>
      normalizeCollectionRouteSearch({ ...locationSearch }, { defaultQuery }),
    [locationSearch],
  )
  useEffect(() => {
    if (!collectionRouteSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [locationSearch, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) =>
      void navigate({ search: next, replace }),
    [navigate],
  )
  return (
    <ModelRoutersCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/models/routers/new", search })}
      onOpen={(router) =>
        void navigate({
          to: "/models/routers/$name",
          params: { name: router.name ?? "" },
          search,
        })
      }
      onEdit={(router) =>
        void navigate({
          to: "/models/routers/$name/edit",
          params: { name: router.name ?? "" },
          search,
        })
      }
    />
  )
}
