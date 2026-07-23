import { createFileRoute } from "@tanstack/react-router"

import { AccountRouterEditorPage } from "@/components/credentials/account-router-editor-page"

export const Route = createFileRoute("/accounts/account-router/new")({
  component: () => <AccountRouterEditorPage mode="create" />,
})
