import { createFileRoute } from "@tanstack/react-router"

import { ToolsPage } from "@/components/agent/tools/tools-page"
import type { ToolsPageTab } from "@/components/agent/tools/types"

type AgentToolsRouteSearch = {
  tab?: ToolsPageTab
}

const TOOL_TABS = new Set([
  "library",
  "web-search",
  "thread-policy",
  "adaptation",
])

function normalizeSearch(raw: Record<string, unknown>): AgentToolsRouteSearch {
  const tab = typeof raw.tab === "string" ? raw.tab.trim() : ""
  return {
    ...(TOOL_TABS.has(tab) ? { tab: tab as ToolsPageTab } : {}),
  }
}

export const Route = createFileRoute("/agent/tools")({
  validateSearch: normalizeSearch,
  component: AgentToolsRoute,
})

function AgentToolsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const activeTab = search.tab ?? "library"

  return (
    <ToolsPage
      activeTab={activeTab}
      onTabChange={(tab) => {
        void navigate({
          search: { tab },
        })
      }}
    />
  )
}
