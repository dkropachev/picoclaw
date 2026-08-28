import { createFileRoute } from "@tanstack/react-router"

import { PRLifecycleWorkflowConfigurationDetailPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeWorkflowConfigurationsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

function DevelopmentWorkflowConfigurationDetailRoutePage() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleWorkflowConfigurationDetailPage
      configurationID={id}
      onBack={() =>
        void navigate({
          to: "/development/workflow-configurations",
          search,
        })
      }
      onEdit={() =>
        void navigate({
          to: "/development/workflow-configurations/$id/edit",
          params: { id },
          search: { ...search, flow: "review" },
        })
      }
      onDeleted={() =>
        void navigate({
          to: "/development/workflow-configurations",
          search,
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute(
  "/development_/workflow-configurations_/$id",
)({
  validateSearch: normalizeWorkflowConfigurationsSearch,
  component: DevelopmentWorkflowConfigurationDetailRoutePage,
})
