import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationEditorPage } from "@/components/model-evaluations/model-evaluation-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/model-evaluations_/$id_/edit")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: "ORDER BY updated DESC",
    }),
  component: EditModelEvaluationRoute,
})

function EditModelEvaluationRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ModelEvaluationEditorPage
      id={id}
      onBack={() =>
        void navigate({ to: "/model-evaluations/$id", params: { id }, search })
      }
      onSaved={(savedID) =>
        void navigate({
          to: "/model-evaluations/$id",
          params: { id: savedID },
          search,
          replace: true,
        })
      }
    />
  )
}
