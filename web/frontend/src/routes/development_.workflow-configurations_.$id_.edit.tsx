import { createFileRoute } from "@tanstack/react-router"

import type { PRLifecycleDecisionPoint } from "@/api/pr-lifecycle-flow"
import { PRLifecycleWorkflowConfigurationEditorPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeWorkflowConfigurationEditorSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

function DevelopmentWorkflowConfigurationEditRoutePage() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const collectionSearch = {
    q: search.q,
    ...(search.view ? { view: search.view } : {}),
  }
  return (
    <PRLifecycleWorkflowConfigurationEditorPage
      configurationID={id}
      flowID={search.flow}
      decisionPoint={search.gate as PRLifecycleDecisionPoint | undefined}
      onBack={() =>
        void navigate({
          to: "/development/workflow-configurations/$id",
          params: { id },
          search: collectionSearch,
        })
      }
      onFlowChange={(flow) =>
        void navigate({
          to: "/development/workflow-configurations/$id/edit",
          params: { id },
          search: { ...collectionSearch, flow },
        })
      }
      onDecisionPointChange={(gate) =>
        void navigate({
          to: "/development/workflow-configurations/$id/edit",
          params: { id },
          search: {
            ...collectionSearch,
            flow: search.flow,
            ...(gate ? { gate } : {}),
          },
        })
      }
    />
  )
}

export const Route = createFileRoute(
  "/development_/workflow-configurations_/$id_/edit",
)({
  validateSearch: normalizeWorkflowConfigurationEditorSearch,
  component: DevelopmentWorkflowConfigurationEditRoutePage,
})
