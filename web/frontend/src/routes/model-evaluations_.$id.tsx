import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationDetailPage } from "@/components/collections/pilots/model-evaluation-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/model-evaluations_/$id")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: "ORDER BY updated DESC",
    }),
  component: ModelEvaluationDetailRoute,
})

function ModelEvaluationDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelEvaluationDetailPage
      id={id}
      onBack={() => void navigate({ to: "/model-evaluations", search })}
      onEdit={() =>
        void navigate({
          to: "/model-evaluations/$id/edit",
          params: { id },
          search,
        })
      }
      onLanguages={() =>
        void navigate({
          to: "/model-evaluations/$id/languages",
          params: { id },
          search,
        })
      }
      onCorpus={() =>
        void navigate({
          to: "/model-evaluations/$id/corpus",
          params: { id },
          search,
        })
      }
      onReport={() =>
        void navigate({
          to: "/model-evaluations/$evaluationID/report",
          params: { evaluationID: id },
          search,
        })
      }
    />
  )
}
