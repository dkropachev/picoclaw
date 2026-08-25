import { createFileRoute } from "@tanstack/react-router"

import { ModelAliasDetailPage } from "@/components/collections/pilots/model-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/models_/aliases_/$name")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: ModelAliasDetailRoute,
})

function ModelAliasDetailRoute() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelAliasDetailPage
      name={name}
      onBack={() => void navigate({ to: "/models/aliases", search })}
      onEdit={() =>
        void navigate({
          to: "/models/aliases/$name/edit",
          params: { name },
          search,
        })
      }
    />
  )
}
