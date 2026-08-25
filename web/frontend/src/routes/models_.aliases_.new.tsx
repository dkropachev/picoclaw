import { createFileRoute } from "@tanstack/react-router"

import { ModelAliasEditorPage } from "@/components/collections/pilots/model-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/models_/aliases_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: NewModelAliasRoute,
})

function NewModelAliasRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <ModelAliasEditorPage
      onBack={() => void navigate({ to: "/models/aliases", search })}
      onSaved={(name) =>
        void navigate({
          to: "/models/aliases/$name",
          params: { name },
          search,
          replace: true,
        })
      }
    />
  )
}
