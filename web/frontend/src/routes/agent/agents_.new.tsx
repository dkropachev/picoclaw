import { createFileRoute } from "@tanstack/react-router"

import {
  AgentCollectionEditorPage,
  normalizeAgentCollectionSearch,
} from "@/components/collections/pilots/agent-collection"

export const Route = createFileRoute("/agent/agents_/new")({
  validateSearch: normalizeAgentCollectionSearch,
  component: NewAgentRoute,
})

function NewAgentRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AgentCollectionEditorPage
      mode="create"
      onBack={() => void navigate({ to: "/agent/agents", search })}
      onSaved={(id) =>
        void navigate({
          to: "/agent/agents/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}
