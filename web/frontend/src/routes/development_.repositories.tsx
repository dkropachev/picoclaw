import { createFileRoute } from "@tanstack/react-router"

import { PRLifecycleRepositoryAssignmentsPage } from "@/components/pr-workspaces/pr-lifecycle-repository-assignments-page"

function DevelopmentRepositoriesRoutePage() {
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleRepositoryAssignmentsPage
      onBack={() => void navigate({ to: "/development" })}
    />
  )
}

export const Route = createFileRoute("/development_/repositories")({
  validateSearch: () => ({}),
  component: DevelopmentRepositoriesRoutePage,
})
