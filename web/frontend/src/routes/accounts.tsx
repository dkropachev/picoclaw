import { createFileRoute } from "@tanstack/react-router"

import { AccountsPage } from "@/components/credentials/accounts-page"

export const Route = createFileRoute("/accounts")({
  component: AccountsPage,
})
