import { createFileRoute, redirect } from "@tanstack/react-router"

import { ModelEvaluationReportPage } from "@/components/model-evaluations/model-evaluation-report-page"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

const evaluationIDPattern = /^rme_[0-9a-f]{32}$/

function ModelEvaluationReportRoutePage() {
  const { evaluationID } = Route.useParams()
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <ModelEvaluationReportPage
      evaluationID={evaluationID}
      onBack={() =>
        void navigate({
          to: "/model-evaluations/$id",
          params: { id: evaluationID },
          search,
        })
      }
    />
  )
}

export const Route = createFileRoute(
  "/model-evaluations_/$evaluationID/report",
)({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: "ORDER BY updated DESC",
    }),
  beforeLoad: ({ params }) => {
    if (!evaluationIDPattern.test(params.evaluationID)) {
      throw redirect({
        to: "/model-evaluations",
        search: { q: "ORDER BY updated DESC" },
      })
    }
  },
  component: ModelEvaluationReportRoutePage,
})
