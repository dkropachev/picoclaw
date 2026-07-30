import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  type AgentsRouteSearch,
  agentsSearchIsCanonical,
  normalizeAgentsSearch,
} from "@/components/agent/agents/agent-route-search"
import { AgentsPage } from "@/components/agent/agents/agents-page"

export const Route = createFileRoute("/agent/agents")({
  validateSearch: normalizeAgentsSearch,
  component: AgentsRoute,
})

function AgentsRoute() {
  const locationSearch = useLocation({
    select: (location) => location.search,
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeAgentsSearch({ ...locationSearch }),
    [locationSearch],
  )

  useEffect(() => {
    if (!agentsSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [locationSearch, navigate, search])

  const changeSearch = useCallback(
    (next: AgentsRouteSearch, replace = false) => {
      void navigate({ search: next, replace })
    },
    [navigate],
  )

  return <AgentsPage search={search} onSearchChange={changeSearch} />
}
