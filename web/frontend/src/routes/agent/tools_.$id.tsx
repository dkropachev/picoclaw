import { createFileRoute } from "@tanstack/react-router"

import { normalizeToolsCollectionSearch } from "@/components/agent/skill-tool-collection-route-state"
import { ToolDetailPage } from "@/components/agent/tools/tool-collections"

export const Route = createFileRoute("/agent/tools_/$id")({
  validateSearch: normalizeToolsCollectionSearch,
  component: ToolDetailRoute,
})

function ToolDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ToolDetailPage
      toolID={id}
      onBack={() => void navigate({ to: "/agent/tools", search })}
      onEdit={() =>
        void navigate({
          to: "/agent/tools/$id/edit",
          params: { id },
          search,
        })
      }
    />
  )
}
