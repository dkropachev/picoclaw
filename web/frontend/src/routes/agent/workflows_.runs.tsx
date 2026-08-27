import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  normalizeWorkflowRunsSearch,
  workflowCollectionSearchIsCanonical,
} from "@/components/workflows/workflow-collection-route-state"
import { WorkflowRunsCollectionPage } from "@/components/workflows/workflow-collections"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/workflows_/runs")({
  validateSearch: normalizeWorkflowRunsSearch,
  component: WorkflowRunsRoute,
})

function WorkflowRunsRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeWorkflowRunsSearch({ ...location.search }),
    [location.search],
  )

  useEffect(() => {
    if (location.pathname !== "/agent/workflows/runs") return
    if (!workflowCollectionSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/agent/workflows/runs") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )

  return (
    <WorkflowRunsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onOpen={(run) =>
        void navigate({
          to: "/agent/workflows/runs/$id",
          params: { id: run.id },
          search,
        })
      }
      onDefinitions={() =>
        void navigate({
          to: "/agent/workflows",
          search: { q: "ORDER BY ref ASC" },
        })
      }
    />
  )
}
