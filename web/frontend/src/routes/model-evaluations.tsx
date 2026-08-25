import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { ModelEvaluationsCollectionPage } from "@/components/collections/pilots/model-evaluation-collection"
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
