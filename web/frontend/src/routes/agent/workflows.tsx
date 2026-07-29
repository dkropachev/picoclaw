import { createFileRoute } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  type WorkflowsRouteSearch,
  normalizeWorkflowsSearch,
  workflowsSearchIsCanonical,
} from "@/components/workflows/workflow-route-search"
import { WorkflowsPage } from "@/components/workflows/workflows-page"

export const Route = createFileRoute("/agent/workflows")({
  validateSearch: normalizeWorkflowsSearch,
  component: WorkflowsRoute,
})

function WorkflowsRoute() {
  const rawSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeWorkflowsSearch({ ...rawSearch }),
    [rawSearch],
  )
  useEffect(() => {
    if (!workflowsSearchIsCanonical({ ...rawSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [navigate, rawSearch, search])
  const changeSearch = useCallback(
    (next: WorkflowsRouteSearch, replace = false) => {
      void navigate({ search: next, replace })
    },
    [navigate],
  )

  return <WorkflowsPage search={search} onSearchChange={changeSearch} />
}
