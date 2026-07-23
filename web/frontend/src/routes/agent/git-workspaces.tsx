import { createFileRoute } from "@tanstack/react-router"

import { GitWorkspacesPage } from "@/components/agent/git-workspaces/git-workspaces-page"

export const Route = createFileRoute("/agent/git-workspaces")({
  component: GitWorkspacesRoute,
})

function GitWorkspacesRoute() {
  return <GitWorkspacesPage />
}
