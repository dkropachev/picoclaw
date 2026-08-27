import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { ModelAliasesCollectionPage } from "@/components/models/model-collections"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

const defaultQuery = "ORDER BY name ASC"

export const Route = createFileRoute("/models_/aliases")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery }),
  component: ModelAliasesRoute,
})

function ModelAliasesRoute() {
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
    if (location.pathname !== "/models/aliases") return
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
      if (location.pathname !== "/models/aliases") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
  )
  return (
    <ModelAliasesCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/models/aliases/new", search })}
      onOpen={(alias) =>
        void navigate({
          to: "/models/aliases/$name",
          params: { name: alias.name },
          search,
        })
      }
      onEdit={(alias) =>
        void navigate({
          to: "/models/aliases/$name/edit",
          params: { name: alias.name },
          search,
        })
      }
    />
  )
}
