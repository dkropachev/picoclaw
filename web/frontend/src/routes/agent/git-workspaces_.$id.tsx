import { createFileRoute } from "@tanstack/react-router"

import { GitWorkspaceDetailPage } from "@/components/agent/git-workspaces/git-workspace-detail-page"
import { normalizeGitWorkspacesSearch } from "@/components/agent/git-workspaces/git-workspace-route-state"

export const Route = createFileRoute("/agent/git-workspaces_/$id")({
  validateSearch: normalizeGitWorkspacesSearch,
  component: GitWorkspaceDetailRoute,
})

function GitWorkspaceDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <GitWorkspaceDetailPage
      workspaceID={id}
      onBack={() => void navigate({ to: "/agent/git-workspaces", search })}
      onDropped={() =>
        void navigate({
          to: "/agent/git-workspaces",
          search,
          replace: true,
        })
      }
    />
  )
}
