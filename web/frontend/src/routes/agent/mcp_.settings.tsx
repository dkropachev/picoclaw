import { createFileRoute } from "@tanstack/react-router"

import { MCPSettingsPage } from "@/components/agent/mcp/mcp-server-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/mcp_/settings")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: MCPSettingsRoute,
})

function MCPSettingsRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <MCPSettingsPage
      onBack={() => void navigate({ to: "/agent/mcp/servers", search })}
    />
  )
}
