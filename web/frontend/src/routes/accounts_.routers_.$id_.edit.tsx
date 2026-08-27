import { createFileRoute } from "@tanstack/react-router"

import {
  accountCollectionViews,
  accountRoutersDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { AccountRouterEditorPage } from "@/components/credentials/account-router-editor-page"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/routers_/$id_/edit")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountRoutersDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: EditAccountRouterRoute,
})

function EditAccountRouterRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AccountRouterEditorPage
      mode="edit"
      routerID={id}
      onBack={() =>
        void navigate({
          to: "/accounts/routers/$id",
          params: { id },
          search,
        })
      }
      onSaved={(savedID) =>
        void navigate({
          to: "/accounts/routers/$id",
          params: { id: savedID },
          search,
          replace: true,
        })
      }
    />
  )
}
