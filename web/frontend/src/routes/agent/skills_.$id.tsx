import { createFileRoute } from "@tanstack/react-router"

import { normalizeSkillsCollectionSearch } from "@/components/agent/skill-tool-collection-route-state"
import { SkillDetailPage } from "@/components/agent/skills/skill-collections"

export const Route = createFileRoute("/agent/skills_/$id")({
  validateSearch: normalizeSkillsCollectionSearch,
  component: SkillDetailRoute,
})

function SkillDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <SkillDetailPage
      skillID={id}
      onBack={() => void navigate({ to: "/agent/skills", search })}
    />
  )
}
