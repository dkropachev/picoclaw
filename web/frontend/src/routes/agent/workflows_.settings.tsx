import { createFileRoute } from "@tanstack/react-router"

import { normalizeWorkflowDefinitionsSearch } from "@/components/workflows/workflow-collection-route-state"
import { WorkflowSettingsPage } from "@/components/workflows/workflow-settings-page"

export const Route = createFileRoute("/agent/workflows_/settings")({
  validateSearch: normalizeWorkflowDefinitionsSearch,
  component: WorkflowSettingsRoute,
})

function WorkflowSettingsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <WorkflowSettingsPage
      onBack={() => void navigate({ to: "/agent/workflows", search })}
    />
  )
}
