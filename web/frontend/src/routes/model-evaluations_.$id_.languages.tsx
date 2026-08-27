import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationLanguagesPage } from "@/components/model-evaluations/model-evaluation-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/model-evaluations_/$id_/languages")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: "ORDER BY updated DESC",
    }),
  component: ModelEvaluationLanguagesRoute,
})

function ModelEvaluationLanguagesRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelEvaluationLanguagesPage
      id={id}
      onBack={() =>
        void navigate({ to: "/model-evaluations/$id", params: { id }, search })
      }
    />
  )
}
