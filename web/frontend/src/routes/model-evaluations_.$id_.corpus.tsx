import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationCorpusPage } from "@/components/model-evaluations/model-evaluation-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/model-evaluations_/$id_/corpus")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: "ORDER BY updated DESC",
    }),
  component: ModelEvaluationCorpusRoute,
})

function ModelEvaluationCorpusRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelEvaluationCorpusPage
      id={id}
      onBack={() =>
        void navigate({ to: "/model-evaluations/$id", params: { id }, search })
      }
    />
  )
}
