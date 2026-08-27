import { createFileRoute } from "@tanstack/react-router"

import { normalizeToolsCollectionSearch } from "@/components/agent/skill-tool-collection-route-state"
import { ToolAdaptationSettingsPage } from "@/components/agent/tools/tool-collections"

export const Route = createFileRoute("/agent/tools_/settings_/adaptation")({
  validateSearch: normalizeToolsCollectionSearch,
  component: ToolAdaptationRoute,
})

function ToolAdaptationRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ToolAdaptationSettingsPage
      onBack={() => void navigate({ to: "/agent/tools", search })}
    />
  )
}
