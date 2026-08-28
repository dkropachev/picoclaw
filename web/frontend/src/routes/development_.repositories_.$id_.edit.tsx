import { createFileRoute } from "@tanstack/react-router"

import { PRLifecycleRepositoryAssignmentEditorPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeRepositoryAssignmentsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

function DevelopmentRepositoryAssignmentEditRoutePage() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleRepositoryAssignmentEditorPage
      mode="edit"
      assignmentID={id}
      onBack={() =>
        void navigate({
          to: "/development/repositories/$id",
          params: { id },
          search,
        })
      }
      onSaved={(savedID) =>
        void navigate({
          to: "/development/repositories/$id",
          params: { id: savedID },
          search,
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/repositories_/$id_/edit")({
  validateSearch: normalizeRepositoryAssignmentsSearch,
  component: DevelopmentRepositoryAssignmentEditRoutePage,
})
