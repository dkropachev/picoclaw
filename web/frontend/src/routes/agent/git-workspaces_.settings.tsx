import { createFileRoute } from "@tanstack/react-router"

import { normalizeGitWorkspacesSearch } from "@/components/agent/git-workspaces/git-workspace-route-state"
import { GitWorkspaceSettingsPage } from "@/components/agent/git-workspaces/git-workspace-settings-page"

export const Route = createFileRoute("/agent/git-workspaces_/settings")({
  validateSearch: normalizeGitWorkspacesSearch,
  component: GitWorkspaceSettingsRoute,
})

function GitWorkspaceSettingsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <GitWorkspaceSettingsPage
      onBack={() => void navigate({ to: "/agent/git-workspaces", search })}
    />
  )
}
