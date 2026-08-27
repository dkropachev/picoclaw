import { createFileRoute } from "@tanstack/react-router"

import { AccountAuthEditorPage } from "@/components/credentials/account-auth-editor-page"
import {
  accountCollectionViews,
  accountsDefaultQuery,
} from "@/components/credentials/account-collection-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/accounts_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: accountsDefaultQuery,
      supportedViews: accountCollectionViews,
    }),
  component: NewAccountRoute,
})

function NewAccountRoute() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <AccountAuthEditorPage
      mode="create"
      onBack={() => void navigate({ to: "/accounts", search })}
      onSaved={() => void navigate({ to: "/accounts", search, replace: true })}
    />
  )
}
