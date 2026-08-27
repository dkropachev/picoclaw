import { createFileRoute } from "@tanstack/react-router"

import { normalizeWorkflowDefinitionsSearch } from "@/components/workflows/workflow-collection-route-state"
import { WorkflowDefinitionDetailPage } from "@/components/workflows/workflow-definition-detail-page"

export const Route = createFileRoute("/agent/workflows_/$id")({
  validateSearch: normalizeWorkflowDefinitionsSearch,
  component: WorkflowDefinitionRoute,
})

function WorkflowDefinitionRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <WorkflowDefinitionDetailPage
      workflowID={id}
      onBack={() => void navigate({ to: "/agent/workflows", search })}
      onEdit={() =>
        void navigate({
          to: "/agent/workflows/$id/edit",
          params: { id },
          search,
        })
      }
      onRuns={() =>
        void navigate({
          to: "/agent/workflows/runs",
          search: { q: "ORDER BY created DESC" },
        })
      }
      onRunCreated={(runID) =>
        void navigate({
          to: "/agent/workflows/runs/$id",
          params: { id: runID },
          search: { q: "ORDER BY created DESC" },
        })
      }
    />
  )
}
