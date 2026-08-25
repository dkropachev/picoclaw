import { createFileRoute } from "@tanstack/react-router"

import { ModelRouterEditorPage } from "@/components/collections/pilots/model-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/models_/routers_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: NewModelRouterRoute,
})

function NewModelRouterRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <ModelRouterEditorPage
      onBack={() => void navigate({ to: "/models/routers", search })}
      onSaved={(name) =>
        void navigate({
          to: "/models/routers/$name",
          params: { name },
          search,
          replace: true,
        })
      }
    />
  )
}
