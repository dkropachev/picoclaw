import { createFileRoute } from "@tanstack/react-router"

import { ModelEvaluationsPage } from "@/components/model-evaluations/model-evaluations-page"

export const Route = createFileRoute("/model-evaluations")({
  component: ModelEvaluationsPage,
})
