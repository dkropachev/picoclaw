import { createFileRoute } from "@tanstack/react-router"

import { AccountAuthEditorPage } from "@/components/credentials/account-auth-editor-page"
import {
  accountCollectionViews,
  accountsDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/$id_/edit")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountsDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: EditAccountRoute,
})

function EditAccountRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AccountAuthEditorPage
      mode="edit"
      id={id}
      onBack={() =>
        void navigate({ to: "/accounts/$id", params: { id }, search })
      }
      onSaved={() =>
        void navigate({
          to: "/accounts/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}
