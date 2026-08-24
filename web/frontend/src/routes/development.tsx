import { createFileRoute } from "@tanstack/react-router"

import { DevelopmentPortfolioPage } from "@/components/development-workspaces/development-portfolio-page"

function DevelopmentRoutePage() {
  const navigate = Route.useNavigate()
  return (
    <DevelopmentPortfolioPage
      onCreate={() => void navigate({ to: "/development/new" })}
      onOpenWorkspace={(workspaceID) =>
        void navigate({
          to: "/development/$workspaceID",
          params: { workspaceID },
          search: { tab: "overview" },
        })
      }
    />
  )
}

export const Route = createFileRoute("/development")({
  validateSearch: () => ({}),
  component: DevelopmentRoutePage,
})
