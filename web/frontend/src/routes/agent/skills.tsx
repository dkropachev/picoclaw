import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  normalizeSkillsCollectionSearch,
  skillToolCollectionSearchIsCanonical,
} from "@/components/agent/skill-tool-collection-route-state"
import { SkillsCollectionPage } from "@/components/agent/skills/skill-collections"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/skills")({
  validateSearch: normalizeSkillsCollectionSearch,
  component: SkillsCollectionRoute,
})

function SkillsCollectionRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeSkillsCollectionSearch({ ...routeSearch }),
    [routeSearch],
  )

  useEffect(() => {
    if (location.pathname !== "/agent/skills") return
    if (!skillToolCollectionSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/agent/skills") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )

  return (
    <SkillsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/agent/skills/new", search })}
      onOpen={(skill) =>
        void navigate({
          to: "/agent/skills/$id",
          params: { id: skill.id },
          search,
        })
      }
    />
  )
}
