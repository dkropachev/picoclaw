import { createFileRoute } from "@tanstack/react-router"

import { ModelRouterEditorPage } from "@/components/models/model-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/models_/routers_/$name_/edit")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: EditModelRouterRoute,
})

function EditModelRouterRoute() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelRouterEditorPage
      name={name}
      onBack={() =>
        void navigate({ to: "/models/routers/$name", params: { name }, search })
      }
      onSaved={(savedName) =>
        void navigate({
          to: "/models/routers/$name",
          params: { name: savedName },
          search,
          replace: true,
        })
      }
    />
  )
}
