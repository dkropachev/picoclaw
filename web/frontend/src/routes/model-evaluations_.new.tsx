import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationEditorPage } from "@/components/collections/pilots/model-evaluation-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/model-evaluations_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: "ORDER BY updated DESC",
    }),
  component: NewModelEvaluationRoute,
})

function NewModelEvaluationRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <ModelEvaluationEditorPage
      onBack={() => void navigate({ to: "/model-evaluations", search })}
      onSaved={(id) =>
        void navigate({
          to: "/model-evaluations/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}
