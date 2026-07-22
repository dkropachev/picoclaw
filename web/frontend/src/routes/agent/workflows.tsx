import { createFileRoute } from "@tanstack/react-router"

import { WorkflowsPage } from "@/components/workflows/workflows-page"

export const Route = createFileRoute("/agent/workflows")({
  component: WorkflowsRoute,
})

function WorkflowsRoute() {
  return <WorkflowsPage />
}
