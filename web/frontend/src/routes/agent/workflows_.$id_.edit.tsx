import { createFileRoute } from "@tanstack/react-router"

import { WorkflowAuthoringRoutePage } from "@/components/workflows/workflow-authoring-route-page"
import { normalizeWorkflowDefinitionsSearch } from "@/components/workflows/workflow-collection-route-state"

export const Route = createFileRoute("/agent/workflows_/$id_/edit")({
  validateSearch: normalizeWorkflowDefinitionsSearch,
  component: EditWorkflowRoute,
})

function EditWorkflowRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <WorkflowAuthoringRoutePage
      intent={{ kind: "edit", workflowID: id }}
      onBack={() =>
        void navigate({
          to: "/agent/workflows/$id",
          params: { id },
          search,
        })
      }
      onOpenActiveNew={() =>
        void navigate({
          to: "/agent/workflows/new",
          search,
          replace: true,
        })
      }
      onOpenActiveEdit={(workflowID) =>
        void navigate({
          to: "/agent/workflows/$id/edit",
          params: { id: workflowID },
          search,
          replace: true,
        })
      }
      onOpenRun={(runID) =>
        void navigate({
          to: "/agent/workflows/runs/$id",
          params: { id: runID },
          search: { q: "ORDER BY created DESC" },
        })
      }
      onPublished={(workflowID) =>
        void navigate({
          to: "/agent/workflows/$id",
          params: { id: workflowID },
          search,
          replace: true,
        })
      }
    />
  )
}
