import { createFileRoute } from "@tanstack/react-router"

import {
  accountCollectionViews,
  accountRoutersDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { AccountRouterEditorPage } from "@/components/credentials/account-router-editor-page"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/routers_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountRoutersDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: NewAccountRouterRoute,
})

function NewAccountRouterRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <AccountRouterEditorPage
      mode="create"
      onBack={() => void navigate({ to: "/accounts/routers", search })}
      onSaved={(id) =>
        void navigate({
          to: "/accounts/routers/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}
