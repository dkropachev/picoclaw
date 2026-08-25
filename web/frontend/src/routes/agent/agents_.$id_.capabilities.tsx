import { createFileRoute } from "@tanstack/react-router"

import {
  AgentCollectionCapabilitiesPage,
  normalizeAgentCollectionSearch,
} from "@/components/collections/pilots/agent-collection"

export const Route = createFileRoute("/agent/agents_/$id_/capabilities")({
  validateSearch: normalizeAgentCollectionSearch,
  component: AgentCapabilitiesRoute,
})

function AgentCapabilitiesRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AgentCollectionCapabilitiesPage
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
