import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"

import {
  accountCollectionViews,
  accountRoutersDefaultQuery,
  accountsDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { AccountsCollectionPage } from "@/components/credentials/account-collections"
import { AccountOAuthCallbackRecovery } from "@/components/credentials/account-oauth-callback-recovery"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts")({
  validateSearch: (raw: Record<string, unknown>) => {
    const normalized = normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountsDefaultQuery,
      supportedViews: accountCollectionViews,
    })
    const callbackFlowID =
      typeof raw.oauth_flow_id === "string" && raw.oauth_flow_id.trim()
        ? raw.oauth_flow_id.trim().slice(0, 256)
        : undefined
    return {
      ...normalized,
      ...(callbackFlowID ? { oauth_flow_id: callbackFlowID } : {}),
    }
  },
  component: AccountsRoute,
})

function AccountsRoute() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const [oauthCallbackFlowID] = useState(() => routeSearch.oauth_flow_id ?? "")
  const search = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...routeSearch },
        {
          defaultQuery: accountsDefaultQuery,
          supportedViews: accountCollectionViews,
        },
      ),
    [routeSearch],
  )
  useEffect(() => {
    if (location.pathname !== "/accounts") return
    if (!collectionRouteSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [location.pathname, location.search, navigate, search])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname === "/accounts") {
        void navigate({ search: next, replace })
      }
    },
    [location.pathname, navigate],
  )
  return (
    <>
      {oauthCallbackFlowID && (
        <AccountOAuthCallbackRecovery
          flowID={oauthCallbackFlowID}
          onConsumed={() => void navigate({ search, replace: true })}
        />
      )}
      <AccountsCollectionPage
        search={search}
        onSearchChange={changeSearch}
        onAdd={() => void navigate({ to: "/accounts/new", search })}
        onOpen={(account) =>
          void navigate({
            to: "/accounts/$id",
            params: { id: account.id },
            search,
          })
        }
        onEdit={(account) =>
          void navigate({
            to: "/accounts/$id/edit",
            params: { id: account.id },
            search,
          })
        }
        onRouters={() =>
          void navigate({
            to: "/accounts/routers",
            search: { q: accountRoutersDefaultQuery },
          })
        }
      />
    </>
  )
}
