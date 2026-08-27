import { createFileRoute } from "@tanstack/react-router"

import {
  accountCollectionViews,
  accountRoutersDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { AccountRouterDetailPage } from "@/components/credentials/account-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/routers_/$id")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountRoutersDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: AccountRouterDetailRoute,
})

function AccountRouterDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AccountRouterDetailPage
      id={id}
      onBack={() => void navigate({ to: "/accounts/routers", search })}
      onEdit={() =>
        void navigate({
          to: "/accounts/routers/$id/edit",
          params: { id },
          search,
        })
      }
    />
  )
}
