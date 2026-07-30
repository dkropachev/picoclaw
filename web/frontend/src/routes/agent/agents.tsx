import { createFileRoute } from "@tanstack/react-router"

import { AgentsPage } from "@/components/agent/agents/agents-page"

export const Route = createFileRoute("/agent/agents")({
  component: AgentsRoute,
})

function AgentsRoute() {
  return <AgentsPage />
}
