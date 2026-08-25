import { createFileRoute } from "@tanstack/react-router"

import { MCPServerEditorPage } from "@/components/collections/pilots/mcp-server-collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/mcp_/servers_/$name_/edit")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, { defaultQuery: "ORDER BY name ASC" }),
  component: EditMCPServerRoute,
})

function EditMCPServerRoute() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <MCPServerEditorPage
      name={name}
      onBack={() =>
        void navigate({
          to: "/agent/mcp/servers/$name",
          params: { name },
          search,
        })
      }
      onSaved={(savedName) =>
        void navigate({
          to: "/agent/mcp/servers/$name",
          params: { name: savedName },
          search,
          replace: true,
        })
      }
    />
  )
}
