import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useEffect } from "react"

import { PRWorkspacePortfolioPage } from "@/components/pr-workspaces/pr-workspace-portfolio-page"
import { updatePRNavigationState } from "@/routes/-pr-navigation"

function PullRequestsRoutePage() {
  const navigate = Route.useNavigate()
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const canonical =
    pathname !== "/pull-requests" ||
    (Object.keys(locationSearch).length === 0 && locationHash === "")

  useEffect(() => {
    if (canonical) return
    void navigate({ search: {}, hash: "", replace: true })
  }, [canonical, navigate])

  if (!canonical) return null

  return (
    <PRWorkspacePortfolioPage
      onOpenWorkspace={(workspaceID) =>
        void navigate({
          to: "/pull-requests/$workspaceID",
          params: { workspaceID },
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "portfolio",
              prParentIndex: current.__TSR_index,
              prParentKey: current.__TSR_key,
              prWorkIndex: current.__TSR_index,
              prWorkKey: current.__TSR_key,
            })),
        })
      }
      onOpenWorkflowConfigurations={() =>
        void navigate({
          to: "/pull-requests/workflow-configurations",
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "portfolio",
              prParentIndex: current.__TSR_index,
              prParentKey: current.__TSR_key,
              prWorkIndex: current.__TSR_index,
              prWorkKey: current.__TSR_key,
            })),
        })
      }
      onOpenRepositoryAssignments={() =>
        void navigate({
          to: "/pull-requests/repository-assignments",
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "portfolio",
              prParentIndex: current.__TSR_index,
              prParentKey: current.__TSR_key,
              prWorkIndex: current.__TSR_index,
              prWorkKey: current.__TSR_key,
            })),
        })
      }
    />
  )
}

export const Route = createFileRoute("/pull-requests")({
  validateSearch: () => ({}),
  component: PullRequestsRoutePage,
})
