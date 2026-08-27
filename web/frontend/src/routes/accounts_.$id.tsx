import { createFileRoute } from "@tanstack/react-router"

import {
  accountCollectionViews,
  accountsDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { AccountDetailPage } from "@/components/credentials/account-collections"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/$id")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountsDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: AccountDetailRoute,
})

function AccountDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AccountDetailPage
      id={id}
      onBack={() => void navigate({ to: "/accounts", search })}
      onEdit={() =>
        void navigate({
          to: "/accounts/$id/edit",
          params: { id },
          search,
        })
      }
      onRemoved={() =>
        void navigate({ to: "/accounts", search, replace: true })
      }
    />
  )
}
