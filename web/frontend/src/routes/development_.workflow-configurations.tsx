import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { PRLifecycleWorkflowConfigurationsCollectionPage } from "@/components/pr-workspaces/pr-lifecycle-admin-collections"
import { normalizeWorkflowConfigurationsSearch } from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
} from "@/hooks/use-collection-route-state"

function DevelopmentWorkflowConfigurationsRoutePage() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeWorkflowConfigurationsSearch({ ...routeSearch }),
    [routeSearch],
  )
  useEffect(() => {
    if (location.pathname !== "/development/workflow-configurations") return
    if (!collectionRouteSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/development/workflow-configurations") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )
  return (
    <PRLifecycleWorkflowConfigurationsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onOpen={(configuration) =>
        void navigate({
          to: "/development/workflow-configurations/$id",
          params: { id: configuration.id },
          search,
        })
      }
      onEdit={(configuration) =>
        void navigate({
          to: "/development/workflow-configurations/$id/edit",
          params: { id: configuration.id },
          search: { ...search, flow: "review" },
        })
      }
      onNew={() =>
        void navigate({
          to: "/development/workflow-configurations/new",
          search,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/workflow-configurations")({
  validateSearch: normalizeWorkflowConfigurationsSearch,
  component: DevelopmentWorkflowConfigurationsRoutePage,
})
