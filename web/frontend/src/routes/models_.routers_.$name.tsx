import { createFileRoute } from "@tanstack/react-router"

import { ModelRouterDetailPage } from "@/components/collections/pilots/model-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/models_/routers_/$name")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: ModelRouterDetailRoute,
})

function ModelRouterDetailRoute() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelRouterDetailPage
      name={name}
      onBack={() => void navigate({ to: "/models/routers", search })}
      onEdit={() =>
        void navigate({
          to: "/models/routers/$name/edit",
          params: { name },
          search,
        })
      }
    />
  )
}
