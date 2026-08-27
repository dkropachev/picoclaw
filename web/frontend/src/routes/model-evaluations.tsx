import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { ModelEvaluationsCollectionPage } from "@/components/model-evaluations/model-evaluation-collection"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

const defaultQuery = "ORDER BY updated DESC"

export const Route = createFileRoute("/model-evaluations")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery }),
  component: ModelEvaluationsRoute,
})

function ModelEvaluationsRoute() {
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
    if (location.pathname !== "/model-evaluations") return
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
      if (location.pathname !== "/model-evaluations") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
  )
  return (
    <ModelEvaluationsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/model-evaluations/new", search })}
      onOpen={(evaluation) =>
        void navigate({
          to: "/model-evaluations/$id",
          params: { id: evaluation.id },
          search,
        })
      }
      onEdit={(evaluation) =>
        void navigate({
          to: "/model-evaluations/$id/edit",
          params: { id: evaluation.id },
          search,
        })
      }
    />
  )
}
