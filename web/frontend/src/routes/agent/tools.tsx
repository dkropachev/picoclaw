import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  normalizeToolsCollectionSearch,
  skillToolCollectionSearchIsCanonical,
} from "@/components/agent/skill-tool-collection-route-state"
import { ToolsCollectionPage } from "@/components/agent/tools/tool-collections"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/agent/tools")({
  validateSearch: normalizeToolsCollectionSearch,
  component: ToolsCollectionRoute,
})

function ToolsCollectionRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeToolsCollectionSearch({ ...routeSearch }),
    [routeSearch],
  )

  useEffect(() => {
    if (location.pathname !== "/agent/tools") return
    if (!skillToolCollectionSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])

  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/agent/tools") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )

  return (
    <ToolsCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onOpen={(tool) =>
        void navigate({
          to: "/agent/tools/$id",
          params: { id: tool.id },
          search,
        })
      }
      onEdit={(tool) =>
        void navigate({
          to: "/agent/tools/$id/edit",
          params: { id: tool.id },
          search,
        })
      }
      onAdaptation={() =>
        void navigate({ to: "/agent/tools/settings/adaptation", search })
      }
    />
  )
}
