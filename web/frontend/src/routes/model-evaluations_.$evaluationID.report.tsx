import { createFileRoute, redirect } from "@tanstack/react-router"

import { ModelEvaluationReportPage } from "@/components/model-evaluations/model-evaluation-report-page"

const evaluationIDPattern = /^rme_[0-9a-f]{32}$/

function ModelEvaluationReportRoutePage() {
  const { evaluationID } = Route.useParams()
  const navigate = Route.useNavigate()
  return (
    <ModelEvaluationReportPage
      evaluationID={evaluationID}
      onBack={() =>
        void navigate({
          to: "/model-evaluations",
          search: { probe: evaluationID },
        })
      }
    />
  )
}

export const Route = createFileRoute(
  "/model-evaluations_/$evaluationID/report",
)({
  beforeLoad: ({ params }) => {
    if (!evaluationIDPattern.test(params.evaluationID)) {
      throw redirect({ to: "/model-evaluations" })
    }
  },
  component: ModelEvaluationReportRoutePage,
})
