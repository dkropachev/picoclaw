import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationsPage } from "@/components/model-evaluations/model-evaluations-page"

const evaluationIDPattern = /^rme_[0-9a-f]{32}$/

interface ModelEvaluationsSearch {
  probe?: string
}

function ModelEvaluationsRoutePage() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <ModelEvaluationsPage
      initialEvaluationID={search.probe}
      onOpenReport={(evaluationID) =>
        void navigate({
          to: "/model-evaluations/$evaluationID/report",
          params: { evaluationID },
        })
      }
    />
  )
}

export const Route = createFileRoute("/model-evaluations")({
  validateSearch: (raw: Record<string, unknown>): ModelEvaluationsSearch => ({
    ...(typeof raw.probe === "string" && evaluationIDPattern.test(raw.probe)
      ? { probe: raw.probe }
      : {}),
  }),
  component: ModelEvaluationsRoutePage,
})
