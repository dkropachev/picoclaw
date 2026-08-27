import { createFileRoute } from "@tanstack/react-router"

import { normalizeSkillsCollectionSearch } from "@/components/agent/skill-tool-collection-route-state"
import { SkillImportPage } from "@/components/agent/skills/skill-import-page"

export const Route = createFileRoute("/agent/skills_/new")({
  validateSearch: normalizeSkillsCollectionSearch,
  component: NewSkillRoute,
})

function NewSkillRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <SkillImportPage
      onBack={() => void navigate({ to: "/agent/skills", search })}
      onImported={(id) =>
        void navigate({
          to: "/agent/skills/$id",
          params: { id },
          search,
          replace: true,
        })
      }
      onOpenMarketplace={() => void navigate({ to: "/agent/hub" })}
    />
  )
}
