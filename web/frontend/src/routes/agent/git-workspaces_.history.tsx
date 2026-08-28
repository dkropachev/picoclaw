import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { GitWorkspaceHistoryCollectionPage } from "@/components/agent/git-workspaces/git-workspace-collections"
import {
  gitWorkspaceSearchIsCanonical,
  normalizeGitWorkspaceHistorySearch,
} from "@/components/agent/git-workspaces/git-workspace-route-state"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/git-workspaces_/history")({
  validateSearch: normalizeGitWorkspaceHistorySearch,
  component: GitWorkspaceHistoryRoute,
})

function GitWorkspaceHistoryRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeGitWorkspaceHistorySearch({ ...location.search }),
    [location.search],
  )
  useEffect(() => {
    if (location.pathname !== "/agent/git-workspaces/history") return
    if (!gitWorkspaceSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/agent/git-workspaces/history") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )
  return (
    <GitWorkspaceHistoryCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onWorkspaces={() =>
        void navigate({
          to: "/agent/git-workspaces",
          search: { q: "ORDER BY updated DESC" },
        })
      }
    />
  )
}
