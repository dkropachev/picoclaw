import { createFileRoute } from "@tanstack/react-router"

import { ModelAliasEditorPage } from "@/components/models/model-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/models_/aliases_/$name_/edit")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: EditModelAliasRoute,
})

function EditModelAliasRoute() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelAliasEditorPage
      name={name}
      onBack={() =>
        void navigate({ to: "/models/aliases/$name", params: { name }, search })
      }
      onSaved={(savedName) =>
        void navigate({
          to: "/models/aliases/$name",
          params: { name: savedName },
          search,
          replace: true,
        })
      }
    />
  )
}
