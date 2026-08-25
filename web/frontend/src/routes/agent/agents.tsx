import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  AgentCollectionPage,
  agentCollectionSearchIsCanonical,
  normalizeAgentCollectionSearch,
} from "@/components/collections/pilots/agent-collection"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/agents")({
  validateSearch: normalizeAgentCollectionSearch,
  component: AgentsCollectionRoute,
})

function AgentsCollectionRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const locationSearch = location.search
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeAgentCollectionSearch({ ...routeSearch }),
    [routeSearch],
  )
  const normalizedLocationSearch = useMemo(
    () => normalizeAgentCollectionSearch({ ...locationSearch }),
    [locationSearch],
  )

  useEffect(() => {
    if (location.pathname !== "/agent/agents") return
    if (!agentCollectionSearchIsCanonical(normalizedLocationSearch, search)) {
      return
    }
    if (!agentCollectionSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [
    location.pathname,
    locationSearch,
    navigate,
    normalizedLocationSearch,
    search,
  ])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname !== "/agent/agents") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
  )

  return (
    <AgentCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/agent/agents/new", search })}
      onOpen={(agent) =>
        void navigate({
          to: "/agent/agents/$id",
          params: { id: agent.id },
          search,
        })
      }
      onEdit={(agent) =>
        void navigate({
          to: "/agent/agents/$id/edit",
          params: { id: agent.id },
          search,
        })
      }
      onCapabilities={(agent) =>
        void navigate({
          to: "/agent/agents/$id/capabilities",
          params: { id: agent.id },
          search,
        })
      }
      onActivity={(agent) =>
        void navigate({
          to: "/agent/agents/$id/activity",
          params: { id: agent.id },
          search,
        })
      }
    />
  )
}
