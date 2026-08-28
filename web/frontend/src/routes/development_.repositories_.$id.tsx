import { createFileRoute } from "@tanstack/react-router"

import { PRLifecycleRepositoryAssignmentDetailPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeRepositoryAssignmentsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

function DevelopmentRepositoryAssignmentDetailRoutePage() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleRepositoryAssignmentDetailPage
      assignmentID={id}
      onBack={() =>
        void navigate({
          to: "/development/repositories",
          search,
        })
      }
      onEdit={() =>
        void navigate({
          to: "/development/repositories/$id/edit",
          params: { id },
          search,
        })
      }
      onDeleted={() =>
        void navigate({
          to: "/development/repositories",
          search,
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/repositories_/$id")({
  validateSearch: normalizeRepositoryAssignmentsSearch,
  component: DevelopmentRepositoryAssignmentDetailRoutePage,
})
