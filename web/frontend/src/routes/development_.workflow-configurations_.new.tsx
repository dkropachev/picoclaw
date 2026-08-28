import { createFileRoute } from "@tanstack/react-router"

import { PRLifecycleWorkflowConfigurationCreatePage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeWorkflowConfigurationsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

function DevelopmentWorkflowConfigurationNewRoutePage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleWorkflowConfigurationCreatePage
      onBack={() =>
        void navigate({
          to: "/development/workflow-configurations",
          search,
        })
      }
      onSaved={(id) =>
        void navigate({
          to: "/development/workflow-configurations/$id/edit",
          params: { id },
          search: { ...search, flow: "review" },
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute(
  "/development_/workflow-configurations_/new",
)({
  validateSearch: normalizeWorkflowConfigurationsSearch,
  component: DevelopmentWorkflowConfigurationNewRoutePage,
})
