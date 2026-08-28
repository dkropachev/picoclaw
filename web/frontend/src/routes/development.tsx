import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { DevelopmentPortfolioPage } from "@/components/development-workspaces/development-portfolio-page"
import {
  developmentWorkspacesDefaultQuery,
  normalizeDevelopmentWorkspacesSearch,
} from "@/components/development-workspaces/development-workspace-collection-route-state"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
} from "@/hooks/use-collection-route-state"

function DevelopmentRoutePage() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(() => {
    const normalized = normalizeDevelopmentWorkspacesSearch({
      ...routeSearch,
    })
    return {
      q: normalized.q ?? developmentWorkspacesDefaultQuery,
      ...(normalized.view ? { view: normalized.view } : {}),
    }
  }, [routeSearch])
  useEffect(() => {
    if (location.pathname !== "/development") return
    if (!collectionRouteSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/development") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )
  return (
    <DevelopmentPortfolioPage
      search={search}
      onSearchChange={changeSearch}
      onCreate={() => void navigate({ to: "/development/new", search })}
      onOpenWorkspace={(workspaceID) =>
        void navigate({
          to: "/development/$workspaceID",
          params: { workspaceID },
          search: { ...search, tab: "overview" },
        })
      }
    />
  )
}

export const Route = createFileRoute("/development")({
  validateSearch: normalizeDevelopmentWorkspacesSearch,
  component: DevelopmentRoutePage,
})
