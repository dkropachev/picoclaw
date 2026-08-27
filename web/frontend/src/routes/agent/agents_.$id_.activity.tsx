import { createFileRoute } from "@tanstack/react-router"

import {
  AgentCollectionActivityPage,
  normalizeAgentCollectionSearch,
} from "@/components/agent/agents/agent-collection"

export const Route = createFileRoute("/agent/agents_/$id_/activity")({
  validateSearch: normalizeAgentCollectionSearch,
  component: AgentActivityRoute,
})

function AgentActivityRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AgentCollectionActivityPage
      agentID={id}
      onBack={() =>
        void navigate({
          to: "/agent/agents/$id",
          params: { id },
          search,
        })
      }
      onEdit={() =>
        void navigate({
          to: "/agent/agents/$id/edit",
          params: { id },
          search,
        })
      }
    />
  )
}
