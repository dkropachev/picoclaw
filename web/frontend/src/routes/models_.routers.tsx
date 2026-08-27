import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { ModelRoutersCollectionPage } from "@/components/models/model-collections"
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
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const locationSearch = location.search
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeCollectionRouteSearch({ ...routeSearch }, { defaultQuery }),
    [routeSearch],
  )
  const normalizedLocationSearch = useMemo(
    () =>
      normalizeCollectionRouteSearch({ ...locationSearch }, { defaultQuery }),
    [locationSearch],
  )
  useEffect(() => {
    if (location.pathname !== "/models/routers") return
    if (!collectionRouteSearchIsCanonical(normalizedLocationSearch, search)) {
      return
    }
    if (!collectionRouteSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [
    location.pathname,
    locationSearch,
    navigate,
    normalizedLocationSearch,
    search,
  ])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname !== "/models/routers") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
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
