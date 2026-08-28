import { createFileRoute } from "@tanstack/react-router"

import { PRLifecycleRepositoryAssignmentEditorPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeRepositoryAssignmentsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

function DevelopmentRepositoryAssignmentNewRoutePage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleRepositoryAssignmentEditorPage
      mode="create"
      onBack={() =>
        void navigate({
          to: "/development/repositories",
          search,
        })
      }
      onSaved={(id) =>
        void navigate({
          to: "/development/repositories/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/repositories_/new")({
  validateSearch: normalizeRepositoryAssignmentsSearch,
  component: DevelopmentRepositoryAssignmentNewRoutePage,
})
