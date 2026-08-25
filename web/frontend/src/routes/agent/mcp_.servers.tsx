import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { MCPServersCollectionPage } from "@/components/collections/pilots/mcp-server-collection"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

const defaultQuery = "ORDER BY name ASC"

export const Route = createFileRoute("/agent/mcp_/servers")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery }),
  component: MCPServersRoute,
})

function MCPServersRoute() {
  const locationSearch = useLocation({ select: (location) => location.search })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () =>
      normalizeCollectionRouteSearch({ ...locationSearch }, { defaultQuery }),
    [locationSearch],
  )
  useEffect(() => {
    if (!collectionRouteSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [locationSearch, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) =>
      void navigate({ search: next, replace }),
    [navigate],
  )
  return (
    <MCPServersCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/agent/mcp/servers/new", search })}
      onSettings={() => void navigate({ to: "/agent/mcp/settings", search })}
      onOpen={(server) =>
        void navigate({
          to: "/agent/mcp/servers/$name",
          params: { name: server.name },
          search,
        })
      }
      onEdit={(server) =>
        void navigate({
          to: "/agent/mcp/servers/$name/edit",
          params: { name: server.name },
          search,
        })
      }
    />
  )
}
