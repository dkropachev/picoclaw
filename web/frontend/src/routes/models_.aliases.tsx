import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { ModelAliasesCollectionPage } from "@/components/collections/pilots/model-collections"
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
