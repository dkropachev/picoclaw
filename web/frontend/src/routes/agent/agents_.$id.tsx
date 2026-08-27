import { createFileRoute } from "@tanstack/react-router"

import {
  AgentCollectionDetailPage,
  normalizeAgentCollectionSearch,
} from "@/components/agent/agents/agent-collection"

export const Route = createFileRoute("/agent/agents_/$id")({
  validateSearch: normalizeAgentCollectionSearch,
  component: AgentDetailRoute,
})

function AgentDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AgentCollectionDetailPage
      agentID={id}
      onBack={() => void navigate({ to: "/agent/agents", search })}
      onEdit={() =>
        void navigate({
          to: "/agent/agents/$id/edit",
          params: { id },
          search,
        })
      }
      onCapabilities={() =>
        void navigate({
          to: "/agent/agents/$id/capabilities",
          params: { id },
          search,
        })
      }
      onActivity={() =>
        void navigate({
          to: "/agent/agents/$id/activity",
          params: { id },
          search,
        })
      }
    />
  )
}
