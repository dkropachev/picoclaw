import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  normalizeWorkflowDefinitionsSearch,
  workflowCollectionSearchIsCanonical,
} from "@/components/workflows/workflow-collection-route-state"
import { WorkflowDefinitionsCollectionPage } from "@/components/workflows/workflow-collections"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/workflows")({
  validateSearch: normalizeWorkflowDefinitionsSearch,
  component: WorkflowDefinitionsRoute,
})

function WorkflowDefinitionsRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeWorkflowDefinitionsSearch({ ...location.search }),
    [location.search],
  )

  useEffect(() => {
    if (location.pathname !== "/agent/workflows") return
    if (!workflowCollectionSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/agent/workflows") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )

  return (
    <WorkflowDefinitionsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onOpen={(workflow) =>
        void navigate({
          to: "/agent/workflows/$id",
          params: { id: workflow.id },
          search,
        })
      }
      onEdit={(workflow) =>
        void navigate({
          to: "/agent/workflows/$id/edit",
          params: { id: workflow.id },
          search,
        })
      }
      onRun={(workflow) =>
        void navigate({
          to: "/agent/workflows/$id",
          params: { id: workflow.id },
          search,
        })
      }
      onNew={() => void navigate({ to: "/agent/workflows/new", search })}
      onRuns={() =>
        void navigate({
          to: "/agent/workflows/runs",
          search: { q: "ORDER BY created DESC" },
        })
      }
      onSettings={() =>
        void navigate({ to: "/agent/workflows/settings", search })
      }
    />
  )
}
