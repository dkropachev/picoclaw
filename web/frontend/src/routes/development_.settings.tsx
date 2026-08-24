import { createFileRoute } from "@tanstack/react-router"

import {
  type PRLifecycleSettingsTab,
  PRLifecycleWorkflowConfigurationsPage,
} from "@/components/pr-workspaces/pr-lifecycle-workflow-configurations-page"

function normalizeSearch(raw: Record<string, unknown>): {
  tab: PRLifecycleSettingsTab
} {
  return { tab: raw.tab === "scope" ? "scope" : "nudging" }
}

function DevelopmentSettingsRoutePage() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <PRLifecycleWorkflowConfigurationsPage
      page="settings"
      settingsTab={search.tab}
      onBack={() => void navigate({ to: "/development" })}
      onSettingsTabChange={(tab) =>
        void navigate({ to: "/development/settings", search: { tab } })
      }
    />
  )
}

export const Route = createFileRoute("/development_/settings")({
  validateSearch: normalizeSearch,
  component: DevelopmentSettingsRoutePage,
})
