import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  accountCollectionViews,
  accountRoutersDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { AccountRoutersCollectionPage } from "@/components/credentials/account-collections"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/routers")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountRoutersDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: AccountRoutersRoute,
})

function AccountRoutersRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...routeSearch },
        {
          defaultQuery: accountRoutersDefaultQuery,
          supportedViews: accountCollectionViews,
        },
      ),
    [routeSearch],
  )
  useEffect(() => {
    if (location.pathname !== "/accounts/routers") return
    if (!collectionRouteSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/accounts/routers") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )
  return (
    <AccountRoutersCollectionPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() => void navigate({ to: "/accounts/routers/new", search })}
      onOpen={(router) =>
        void navigate({
          to: "/accounts/routers/$id",
          params: { id: router.id },
          search,
        })
      }
      onEdit={(router) =>
        void navigate({
          to: "/accounts/routers/$id/edit",
          params: { id: router.id },
          search,
        })
      }
    />
  )
}
