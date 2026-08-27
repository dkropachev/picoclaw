import { createFileRoute } from "@tanstack/react-router"

import { MCPServerDetailPage } from "@/components/agent/mcp/mcp-server-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/mcp_/servers_/$name")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: MCPServerDetailRoute,
})

function MCPServerDetailRoute() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <MCPServerDetailPage
      name={name}
      onBack={() => void navigate({ to: "/agent/mcp/servers", search })}
      onEdit={() =>
        void navigate({
          to: "/agent/mcp/servers/$name/edit",
          params: { name },
          search,
        })
      }
    />
  )
}
