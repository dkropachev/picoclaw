import { createFileRoute } from "@tanstack/react-router"

import {
  AgentCollectionEditorPage,
  normalizeAgentCollectionSearch,
} from "@/components/collections/pilots/agent-collection"

export const Route = createFileRoute("/agent/agents_/$id_/edit")({
  validateSearch: normalizeAgentCollectionSearch,
  component: EditAgentRoute,
})

function EditAgentRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const openDetail = (nextID = id, replace = false) =>
    void navigate({
      to: "/agent/agents/$id",
      params: { id: nextID },
      search,
      replace,
    })
  return (
    <AgentCollectionEditorPage
      mode="edit"
      agentID={id}
      onBack={() => openDetail()}
      onSaved={(nextID) => openDetail(nextID, true)}
    />
  )
}
