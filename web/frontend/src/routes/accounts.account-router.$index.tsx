import { createFileRoute } from "@tanstack/react-router"

import { AccountRouterEditorPage } from "@/components/credentials/account-router-editor-page"

export const Route = createFileRoute("/accounts/account-router/$index")({
  component: AccountRouterEditRoute,
})

function AccountRouterEditRoute() {
  const { index } = Route.useParams()
  return <AccountRouterEditorPage mode="edit" modelIndex={Number(index)} />
}
