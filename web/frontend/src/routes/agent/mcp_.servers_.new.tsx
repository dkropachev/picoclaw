import { createFileRoute } from "@tanstack/react-router"

import { MCPServerEditorPage } from "@/components/agent/mcp/mcp-server-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/mcp_/servers_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: NewMCPServerRoute,
})

function NewMCPServerRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <MCPServerEditorPage
      onBack={() => void navigate({ to: "/agent/mcp/servers", search })}
      onSaved={(name) =>
        void navigate({
          to: "/agent/mcp/servers/$name",
          params: { name },
          search,
          replace: true,
        })
      }
    />
  )
}
