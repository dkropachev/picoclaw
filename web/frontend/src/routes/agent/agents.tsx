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
  const locationSearch = useLocation({
    select: (location) => location.search,
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeAgentCollectionSearch({ ...locationSearch }),
    [locationSearch],
  )

  useEffect(() => {
    if (!agentCollectionSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [locationSearch, navigate, search])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      void navigate({ search: next, replace })
    },
    [navigate],
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
