import { createFileRoute } from "@tanstack/react-router"

import { normalizeToolsCollectionSearch } from "@/components/agent/skill-tool-collection-route-state"
import { ToolEditorPage } from "@/components/agent/tools/tool-collections"

export const Route = createFileRoute("/agent/tools_/$id_/edit")({
  validateSearch: normalizeToolsCollectionSearch,
  component: ToolEditRoute,
})

function ToolEditRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ToolEditorPage
      toolID={id}
      onBack={() =>
        void navigate({
          to: "/agent/tools/$id",
          params: { id },
          search,
        })
      }
    />
  )
}
