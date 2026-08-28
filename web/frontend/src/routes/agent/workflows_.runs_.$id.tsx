import { createFileRoute } from "@tanstack/react-router"

import { normalizeWorkflowRunsSearch } from "@/components/workflows/workflow-collection-route-state"
import { WorkflowRunDetailPage } from "@/components/workflows/workflow-run-detail-page"

export const Route = createFileRoute("/agent/workflows_/runs_/$id")({
  validateSearch: normalizeWorkflowRunsSearch,
  component: WorkflowRunRoute,
})

function WorkflowRunRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <WorkflowRunDetailPage
      runID={id}
      onBack={() => void navigate({ to: "/agent/workflows/runs", search })}
      onRetryCreated={(runID) =>
        void navigate({
          to: "/agent/workflows/runs/$id",
          params: { id: runID },
          search,
          replace: true,
        })
      }
    />
  )
}
