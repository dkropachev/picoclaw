import {
  createFileRoute,
  redirect,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect } from "react"

import { PRWorkspacePage } from "@/components/pr-workspaces/pr-workspace-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

function PullRequestWorkspaceRoutePage() {
  const { workspaceID } = Route.useParams()
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const canonical =
    pathname !== `/pull-requests/${workspaceID}` ||
    (Object.keys(locationSearch).length === 0 && locationHash === "")

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/$workspaceID",
      params: { workspaceID },
      search: {},
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, workspaceID])

  if (!canonical) return null
  return (
    <PRWorkspacePage
      workspaceID={workspaceID}
      onBack={() => {
        const fallback = () =>
          void navigate({ to: "/pull-requests", replace: true })
        if (
          locationState.prParent &&
          locationState.prParentKey &&
          goToMarkedPRHistory(
            router.history,
            locationState,
            locationState.prParentIndex,
            locationState.prParentKey,
            fallback,
          )
        ) {
          return
        }
        fallback()
      }}
      onOpenWorkflowConfigurations={() =>
        void navigate({
          to: "/pull-requests/workflow-configurations",
          search: { from: workspaceID },
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "workspace",
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

export const Route = createFileRoute("/pull-requests_/$workspaceID")({
  validateSearch: () => ({}),
  beforeLoad: ({ params }) => {
    if (!workspaceIDPattern.test(params.workspaceID)) {
      throw redirect({ to: "/pull-requests", replace: true })
    }
  },
  component: PullRequestWorkspaceRoutePage,
})
