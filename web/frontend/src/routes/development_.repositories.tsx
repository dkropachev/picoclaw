import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { PRLifecycleRepositoryAssignmentsCollectionPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeRepositoryAssignmentsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
} from "@/hooks/use-collection-route-state"

function DevelopmentRepositoriesRoutePage() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeRepositoryAssignmentsSearch({ ...routeSearch }),
    [routeSearch],
  )
  useEffect(() => {
    if (location.pathname !== "/development/repositories") return
    if (!collectionRouteSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/development/repositories") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )
  return (
    <PRLifecycleRepositoryAssignmentsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onOpen={(assignment) =>
        void navigate({
          to: "/development/repositories/$id",
          params: { id: assignment.id },
          search,
        })
      }
      onEdit={(assignment) =>
        void navigate({
          to: "/development/repositories/$id/edit",
          params: { id: assignment.id },
          search,
        })
      }
      onNew={() =>
        void navigate({ to: "/development/repositories/new", search })
      }
    />
  )
}

export const Route = createFileRoute("/development_/repositories")({
  validateSearch: normalizeRepositoryAssignmentsSearch,
  component: DevelopmentRepositoriesRoutePage,
})
